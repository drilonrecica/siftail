package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/database"
)

const RestoreConfirmation = "RESTORE"

// Current database states classified before a restore changes anything
// (ADR 0009, ARCHITECTURE.md §25.5.1).
const (
	CurrentHealthy   = "healthy"
	CurrentMissing   = "missing"
	CurrentUnhealthy = "unhealthy"
)

const (
	RestoreErrCurrentMissing = "current database is missing; " +
		"rerun with --replace-unhealthy to restore without a rollback copy"
	RestoreErrCurrentUnhealthy = "current database is not a compatible rollback " +
		"source; rerun with --replace-unhealthy to preserve it and restore " +
		"without a rollback copy"
	RestoreErrFailedNoDatabase = "restore failed; no database was installed"
	unhealthyTimestampLayout   = "20060102T150405.000000Z"
)

type RestoreOptions struct {
	DataDirectory string
	DatabasePath  string
	ArtifactPath  string
	Confirmation  string
	// ReplaceUnhealthy allows restore when the current database is missing
	// or cannot become a verified rollback. It has no effect on a healthy
	// current database.
	ReplaceUnhealthy bool
}

type RestoreResult struct {
	Type         string
	ArtifactName string
	CurrentState string
	// RollbackName is empty when no healthy current database existed.
	RollbackName string
	// PreservedName is the basename of the directory holding the moved
	// unhealthy database files, or empty when nothing was preserved.
	PreservedName string
	SchemaBefore  int
	SchemaAfter   int
	Migrated      bool
}

func (r RestoreResult) Validate() error {
	var namesValid bool
	switch r.CurrentState {
	case CurrentHealthy:
		namesValid = safeBasename(r.RollbackName) && r.PreservedName == ""
	case CurrentMissing:
		namesValid = r.RollbackName == "" &&
			(r.PreservedName == "" || safeBasename(r.PreservedName))
	case CurrentUnhealthy:
		namesValid = r.RollbackName == "" && safeBasename(r.PreservedName)
	}
	if !namesValid || !validBackupType(r.Type) ||
		!safeBasename(r.ArtifactName) || r.SchemaBefore < 1 ||
		r.SchemaBefore > database.MaxSchemaVersion ||
		r.SchemaAfter != database.MaxSchemaVersion ||
		r.Migrated != (r.SchemaBefore < r.SchemaAfter) {
		return errors.New("invalid restore result")
	}
	return nil
}

type restoreHooks struct {
	afterInstall          func() error
	beforeRollbackPublish func() error
}

type Restorer struct {
	hooks restoreHooks
}

func NewRestorer() *Restorer {
	return &Restorer{}
}

func RestoreRecoveryRequired(dataDirectory string) error {
	if dataDirectory == "" {
		return errors.New("restore recovery check is unavailable")
	}
	info, err := os.Lstat(filepath.Join(dataDirectory, "restore-staging"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("inspect restore recovery state")
	}
	if !info.IsDir() {
		return errors.New("unsafe restore recovery state exists")
	}
	return errors.New("incomplete restore requires manual recovery")
}

func (r *Restorer) Restore(
	ctx context.Context,
	options RestoreOptions,
) (RestoreResult, error) {
	if ctx == nil {
		return RestoreResult{}, errors.New("restore context is nil")
	}
	if r == nil {
		return RestoreResult{}, errors.New("restore service is unavailable")
	}
	if options.Confirmation != RestoreConfirmation {
		return RestoreResult{}, errors.New(
			"restore requires exact RESTORE confirmation",
		)
	}
	databasePath, artifactPath, rollbackPath, err :=
		validateRestorePaths(options)
	if err != nil {
		return RestoreResult{}, err
	}
	lock, err := database.AcquireMaintenanceLock(options.DataDirectory)
	if err != nil {
		if errors.Is(err, database.ErrMaintenanceActive) {
			return RestoreResult{}, errors.New(
				"restore requires the Siftail server to be stopped",
			)
		}
		return RestoreResult{}, errors.New("restore maintenance lock failed")
	}
	lockClosed := false
	defer func() {
		if !lockClosed {
			_ = lock.Close()
		}
	}()

	artifact, err := Verify(ctx, artifactPath)
	if err != nil {
		return RestoreResult{}, errors.New("restore artifact verification failed")
	}
	current, err := classifyCurrentDatabase(ctx, databasePath)
	if err != nil {
		return RestoreResult{}, err
	}
	healthy := current.state == CurrentHealthy
	if !healthy && !options.ReplaceUnhealthy {
		if current.state == CurrentMissing {
			return RestoreResult{}, errors.New(RestoreErrCurrentMissing)
		}
		return RestoreResult{}, errors.New(RestoreErrCurrentUnhealthy)
	}
	if healthy {
		if err := validateExistingRollback(rollbackPath); err != nil {
			return RestoreResult{}, err
		}
	}
	if err := ensureRestoreSpace(
		options.DataDirectory, artifact.Bytes, current.bytes,
	); err != nil {
		return RestoreResult{}, err
	}

	stagingDirectory := filepath.Join(
		options.DataDirectory, "restore-staging",
	)
	if err := os.Mkdir(stagingDirectory, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return RestoreResult{}, errors.New(
				"an incomplete restore staging directory requires recovery",
			)
		}
		return RestoreResult{}, errors.New("create restore staging directory")
	}
	restoredCandidate := filepath.Join(stagingDirectory, "restored.sqlite")
	rollbackCandidate := filepath.Join(stagingDirectory, "rollback.sqlite")
	cleanup := true
	defer func() {
		if cleanup {
			_ = cleanupRestoreStaging(
				stagingDirectory, restoredCandidate, rollbackCandidate,
			)
		}
	}()

	if err := copyRestoreArtifact(
		ctx, artifactPath, restoredCandidate,
	); err != nil {
		return RestoreResult{}, err
	}
	stagedArtifact, err := Verify(ctx, restoredCandidate)
	if err != nil || stagedArtifact.Type != artifact.Type ||
		stagedArtifact.SHA256 != artifact.SHA256 {
		return RestoreResult{}, errors.New(
			"staged restore artifact verification failed",
		)
	}
	preservedName := ""
	if healthy {
		if err := createRollbackArtifact(
			ctx, databasePath, rollbackCandidate,
		); err != nil {
			return RestoreResult{}, err
		}
		if err := removeDatabaseSidecars(databasePath); err != nil {
			return RestoreResult{}, err
		}
	} else {
		preservedName, err = preserveUnhealthyDatabase(
			databasePath, time.Now().UTC(),
		)
		if err != nil {
			return RestoreResult{
				CurrentState: current.state, PreservedName: preservedName,
			}, err
		}
	}
	// failed keeps the classification and preserved location visible to the
	// operator once preservation has moved files.
	failed := func(err error) (RestoreResult, error) {
		return RestoreResult{
			CurrentState: current.state, PreservedName: preservedName,
		}, err
	}

	if err := os.Rename(restoredCandidate, databasePath); err != nil {
		return failed(errors.New("install restored database"))
	}
	installed := true
	rollbackSource := rollbackCandidate
	rollbackInstalled := func(cause error) (RestoreResult, error) {
		if installed && !healthy {
			// No verified pre-restore state exists to reinstall. Remove the
			// installed copy of the untouched input artifact; preserved
			// unhealthy files stay where they are.
			if err := removeInstalledDatabase(databasePath); err != nil {
				cleanup = false
				return failed(errors.New(
					"restore failed and rollback requires manual recovery",
				))
			}
			installed = false
			return failed(errors.New(RestoreErrFailedNoDatabase))
		}
		if installed {
			if rollbackErr := restoreRollbackCandidate(
				ctx, databasePath, rollbackSource, artifact.Type,
				artifact.Name, cause,
			); rollbackErr != nil {
				cleanup = false
				return RestoreResult{}, errors.New(
					"restore failed and rollback requires manual recovery",
				)
			}
			installed = false
		}
		return RestoreResult{}, cause
	}
	if err := syncDirectory(filepath.Dir(databasePath)); err != nil {
		return rollbackInstalled(errors.New(
			"synchronize restored database",
		))
	}
	if r.hooks.afterInstall != nil {
		if err := r.hooks.afterInstall(); err != nil {
			return rollbackInstalled(errors.New(
				"restore interrupted after database replacement",
			))
		}
	}

	restoredDB, err := database.Open(ctx, databasePath)
	if err != nil {
		return rollbackInstalled(errors.New(
			"restored database startup validation failed",
		))
	}
	if err := finalizeRestoredDatabase(
		ctx, restoredDB, artifact.Type, artifact.Name,
		audit.OutcomeSucceeded, "",
	); err != nil {
		_ = restoredDB.Close()
		return rollbackInstalled(errors.New(
			"restored database finalization failed",
		))
	}
	if err := validateRestoredDatabase(ctx, restoredDB); err != nil {
		_ = restoredDB.Close()
		return rollbackInstalled(errors.New(
			"restored database validation failed",
		))
	}
	if err := restoredDB.CheckpointTruncate(ctx); err != nil {
		_ = restoredDB.Close()
		return rollbackInstalled(errors.New(
			"restored database checkpoint failed",
		))
	}
	if err := restoredDB.Close(); err != nil {
		return rollbackInstalled(errors.New(
			"close restored database after validation",
		))
	}
	if err := os.Chmod(databasePath, 0600); err != nil {
		return rollbackInstalled(errors.New(
			"secure restored database permissions",
		))
	}
	postRestore, err := database.CheckPath(ctx, databasePath, true)
	if err != nil || !postRestore.Compatible ||
		postRestore.SchemaVersion != database.MaxSchemaVersion ||
		postRestore.Integrity != "ok" {
		return rollbackInstalled(errors.New(
			"closed restored database validation failed",
		))
	}
	if r.hooks.beforeRollbackPublish != nil {
		if err := r.hooks.beforeRollbackPublish(); err != nil {
			return rollbackInstalled(errors.New(
				"restore interrupted before rollback publication",
			))
		}
	}
	rollbackName := ""
	if healthy {
		if err := os.Rename(rollbackCandidate, rollbackPath); err != nil {
			return rollbackInstalled(errors.New(
				"publish managed restore rollback",
			))
		}
		rollbackSource = rollbackPath
		if err := syncDirectory(filepath.Dir(rollbackPath)); err != nil {
			return rollbackInstalled(errors.New(
				"synchronize managed restore rollback",
			))
		}
		if _, err := Verify(ctx, rollbackPath); err != nil {
			return rollbackInstalled(errors.New(
				"verify published managed restore rollback",
			))
		}
		rollbackName = filepath.Base(rollbackPath)
	}
	installed = false
	if err := cleanupRestoreStaging(
		stagingDirectory, restoredCandidate, rollbackCandidate,
	); err != nil {
		cleanup = false
		return failed(errors.New(
			"restore completed but staging cleanup requires manual recovery",
		))
	}
	cleanup = false
	result := RestoreResult{
		Type: artifact.Type, ArtifactName: artifact.Name,
		CurrentState: current.state, RollbackName: rollbackName,
		PreservedName: preservedName,
		SchemaBefore:  artifact.SchemaVersion,
		SchemaAfter:   database.MaxSchemaVersion,
		Migrated:      artifact.SchemaVersion < database.MaxSchemaVersion,
	}
	if err := result.Validate(); err != nil {
		return RestoreResult{}, err
	}
	if err := lock.Close(); err != nil {
		return RestoreResult{}, errors.New("release restore maintenance lock")
	}
	lockClosed = true
	return result, nil
}

func validateRestorePaths(
	options RestoreOptions,
) (string, string, string, error) {
	if options.DataDirectory == "" || options.DatabasePath == "" ||
		options.ArtifactPath == "" {
		return "", "", "", errors.New("restore paths are incomplete")
	}
	dataDirectory, err := filepath.Abs(options.DataDirectory)
	if err != nil {
		return "", "", "", errors.New("restore data directory is invalid")
	}
	databasePath, err := filepath.Abs(options.DatabasePath)
	if err != nil || filepath.Dir(databasePath) != dataDirectory {
		return "", "", "", errors.New("restore database path is invalid")
	}
	artifactPath, err := filepath.Abs(options.ArtifactPath)
	if err != nil || artifactPath == databasePath {
		return "", "", "", errors.New("restore artifact path is invalid")
	}
	rollbackPath := databasePath + ".rollback"
	if artifactPath == rollbackPath {
		return "", "", "", errors.New(
			"managed rollback requires an explicit recovery copy",
		)
	}
	return databasePath, artifactPath, rollbackPath, nil
}

type currentDatabase struct {
	state string
	// bytes is the logical database plus WAL size a healthy rollback
	// snapshot needs; preserving by rename needs no extra space.
	bytes int64
}

// classifyCurrentDatabase inspects the stopped current database read-only.
// A missing main file is missing; a regular file that fails the full check or
// is not at this binary's current schema is unhealthy. A non-regular entry is
// refused in every mode because it is not a file Siftail owns.
func classifyCurrentDatabase(
	ctx context.Context,
	databasePath string,
) (currentDatabase, error) {
	info, err := os.Lstat(databasePath)
	if errors.Is(err, os.ErrNotExist) {
		return currentDatabase{state: CurrentMissing}, nil
	}
	if err != nil {
		return currentDatabase{}, errors.New("inspect current database")
	}
	if !info.Mode().IsRegular() {
		return currentDatabase{}, errors.New(
			"current database is not a regular owned file",
		)
	}
	report, err := database.CheckPath(ctx, databasePath, true)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return currentDatabase{}, ctxErr
	}
	if err != nil || !report.Compatible ||
		report.SchemaVersion != database.MaxSchemaVersion ||
		report.Integrity != "ok" {
		return currentDatabase{state: CurrentUnhealthy}, nil
	}
	return currentDatabase{
		state: CurrentHealthy, bytes: report.DatabaseBytes + report.WALBytes,
	}, nil
}

var databaseFileSuffixes = []string{"", "-wal", "-shm", "-journal"}

// preserveUnhealthyDatabase moves the main database file and any sidecars,
// unmodified, into a new owner-private <database>.unhealthy-<UTC timestamp>
// directory beside it. It returns the directory's basename, or "" when there
// was nothing to preserve. On failure it moves already-moved files back so a
// main file and its sidecars are never separated.
func preserveUnhealthyDatabase(databasePath string, now time.Time) (string, error) {
	var present []string
	for _, suffix := range databaseFileSuffixes {
		name := filepath.Base(databasePath) + suffix
		if _, err := os.Lstat(databasePath + suffix); err == nil {
			present = append(present, name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("inspect unhealthy current database")
		}
	}
	if len(present) == 0 {
		return "", nil
	}
	parent := filepath.Dir(databasePath)
	directoryName := filepath.Base(databasePath) + ".unhealthy-" +
		now.UTC().Format(unhealthyTimestampLayout)
	directory := filepath.Join(parent, directoryName)
	if err := os.Mkdir(directory, 0700); err != nil {
		return "", errors.New("create unhealthy database preservation directory")
	}
	var moved []string
	for _, name := range present {
		if err := os.Rename(
			filepath.Join(parent, name), filepath.Join(directory, name),
		); err != nil {
			for _, back := range moved {
				if os.Rename(
					filepath.Join(directory, back), filepath.Join(parent, back),
				) != nil {
					return directoryName, errors.New(
						"preserving the unhealthy database requires manual recovery",
					)
				}
			}
			_ = os.Remove(directory)
			return "", errors.New("preserve unhealthy current database")
		}
		moved = append(moved, name)
	}
	if err := syncDirectory(directory); err != nil {
		return directoryName, errors.New(
			"synchronize unhealthy database preservation directory",
		)
	}
	if err := syncDirectory(parent); err != nil {
		return directoryName, errors.New(
			"synchronize unhealthy database preservation directory",
		)
	}
	return directoryName, nil
}

// removeInstalledDatabase removes a restored copy installed where no healthy
// pre-restore database existed. The input artifact it came from is untouched.
func removeInstalledDatabase(databasePath string) error {
	for _, suffix := range databaseFileSuffixes {
		if err := os.Remove(databasePath + suffix); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return errors.New("remove installed restored database")
		}
	}
	return syncDirectory(filepath.Dir(databasePath))
}

func validateExistingRollback(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() ||
		info.Mode().Perm()&0077 != 0 {
		return errors.New("existing managed rollback is unsafe")
	}
	return nil
}

func ensureRestoreSpace(directory string, artifactBytes, currentBytes int64) error {
	if artifactBytes <= 0 || currentBytes < 0 ||
		artifactBytes > int64(^uint64(0)>>1)-currentBytes {
		return errors.New("restore size is unsupported")
	}
	required := artifactBytes + currentBytes
	slack := required / 20
	if slack < 1<<20 {
		slack = 1 << 20
	}
	if required > int64(^uint64(0)>>1)-slack {
		return errors.New("restore size is unsupported")
	}
	required += slack
	var stats syscall.Statfs_t
	if err := syscall.Statfs(directory, &stats); err != nil {
		return errors.New("inspect restore capacity")
	}
	available := uint64(stats.Bavail) * uint64(stats.Bsize)
	if available < uint64(required) {
		return errors.New("restore has insufficient destination capacity")
	}
	return nil
}

func copyRestoreArtifact(ctx context.Context, sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return errors.New("open restore artifact")
	}
	defer source.Close()
	destination, err := os.OpenFile(
		destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600,
	)
	if err != nil {
		return errors.New("create staged restore artifact")
	}
	remove := true
	defer func() {
		_ = destination.Close()
		if remove {
			_ = os.Remove(destinationPath)
		}
	}()
	buffer := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, err := destination.Write(buffer[:read])
			if err != nil || written != read {
				return errors.New("write staged restore artifact")
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return errors.New("read restore artifact")
		}
	}
	if err := destination.Sync(); err != nil {
		return errors.New("synchronize staged restore artifact")
	}
	if err := destination.Close(); err != nil {
		return errors.New("close staged restore artifact")
	}
	remove = false
	return nil
}

func createRollbackArtifact(
	ctx context.Context,
	databasePath string,
	outputPath string,
) error {
	output, err := os.OpenFile(
		outputPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600,
	)
	if err != nil {
		return errors.New("create current database rollback")
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(outputPath)
		return errors.New("close current database rollback")
	}
	current, err := database.Open(ctx, databasePath)
	if err != nil {
		return errors.New("open current database for rollback")
	}
	closed := false
	defer func() {
		if !closed {
			_ = current.Close()
		}
	}()
	if err := database.IntegrityCheck(ctx, current.Writer()); err != nil {
		return errors.New("current database integrity check failed")
	}
	if err := database.SnapshotInto(
		ctx, databasePath, outputPath, nil,
	); err != nil {
		return errors.New("create current database rollback")
	}
	if err := finalizeSnapshot(
		ctx, outputPath, TypeFull, time.Now().UTC(),
	); err != nil {
		return errors.New("finalize current database rollback")
	}
	if _, err := Verify(ctx, outputPath); err != nil {
		return errors.New("verify current database rollback")
	}
	if err := current.CheckpointTruncate(ctx); err != nil {
		return errors.New("checkpoint current database before restore")
	}
	if err := current.Close(); err != nil {
		return errors.New("close current database before restore")
	}
	closed = true
	if err := syncFile(databasePath); err != nil {
		return errors.New("synchronize current database before restore")
	}
	if err := syncDirectory(filepath.Dir(databasePath)); err != nil {
		return errors.New("synchronize current database directory")
	}
	return nil
}

func removeDatabaseSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.Remove(path + suffix); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return errors.New("remove checkpointed database sidecar")
		}
	}
	return nil
}

func finalizeRestoredDatabase(
	ctx context.Context,
	db *database.DB,
	backupType string,
	artifactName string,
	outcome audit.Outcome,
	category string,
) error {
	tx, err := db.Writer().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(
		ctx, "DROP TABLE IF EXISTS siftail_backup_metadata",
	); err != nil {
		return err
	}
	metadata := audit.Metadata{
		audit.MetadataBackupType: backupType,
		audit.MetadataBackupName: artifactName,
	}
	if category != "" {
		metadata[audit.MetadataResultCategory] = category
	}
	auditCtx := audit.ContextWithAttribution(ctx, audit.Attribution{
		ActorType: audit.ActorLocalOperator,
	})
	input := audit.InputFromContext(
		auditCtx, audit.CategoryBackupRestore, "restore.apply",
		outcome, metadata,
	)
	input.OccurredAt = time.Now().UTC()
	if _, err := audit.RecordTx(auditCtx, tx, input); err != nil {
		return err
	}
	return tx.Commit()
}

func validateRestoredDatabase(ctx context.Context, db *database.DB) error {
	if err := database.IntegrityCheck(ctx, db.Writer()); err != nil {
		return err
	}
	for _, table := range []string{
		"administrators", "servers", "ingestion_tokens", "settings",
		"sources", "container_instances", "log_events", "sessions",
		"security_audit_events", "security_audit_event_count",
		"security_audit_unauthenticated_count",
	} {
		var count int
		if err := db.Reader().QueryRowContext(
			ctx, `SELECT count(*) FROM sqlite_schema
				WHERE type='table' AND name=?`, table,
		).Scan(&count); err != nil || count != 1 {
			return errors.New("restored database critical schema is incomplete")
		}
	}
	var schemaVersion, sessions, metadata int
	if err := db.Reader().QueryRowContext(
		ctx, "SELECT coalesce(max(version),0) FROM schema_migrations",
	).Scan(&schemaVersion); err != nil ||
		schemaVersion != database.MaxSchemaVersion {
		return errors.New("restored database schema is incompatible")
	}
	if err := db.Reader().QueryRowContext(
		ctx, "SELECT count(*) FROM sessions",
	).Scan(&sessions); err != nil || sessions != 0 {
		return errors.New("restored database contains sessions")
	}
	if err := db.Reader().QueryRowContext(
		ctx, `SELECT count(*) FROM sqlite_schema
			WHERE type='table' AND name='siftail_backup_metadata'`,
	).Scan(&metadata); err != nil || metadata != 0 {
		return errors.New("restored database retained artifact metadata")
	}
	return nil
}

func restoreRollbackCandidate(
	ctx context.Context,
	databasePath string,
	rollbackCandidate string,
	backupType string,
	artifactName string,
	cause error,
) error {
	recoveryContext := context.WithoutCancel(ctx)
	if err := removeDatabaseSidecars(databasePath); err != nil {
		return err
	}
	if err := os.Rename(rollbackCandidate, databasePath); err != nil {
		return err
	}
	db, err := database.Open(recoveryContext, databasePath)
	if err != nil {
		return err
	}
	category := "restore_failed"
	outcome := audit.OutcomeFailed
	if (cause != nil && errors.Is(cause, context.Canceled)) ||
		ctx.Err() != nil {
		category = "canceled"
		outcome = audit.OutcomeCanceled
	}
	if err := finalizeRestoredDatabase(
		recoveryContext, db, backupType, artifactName, outcome, category,
	); err != nil {
		_ = db.Close()
		return err
	}
	if err := validateRestoredDatabase(recoveryContext, db); err != nil {
		_ = db.Close()
		return err
	}
	if err := db.CheckpointTruncate(recoveryContext); err != nil {
		_ = db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(databasePath))
}

func cleanupRestoreStaging(directory string, paths ...string) error {
	var cleanupErrors []error
	for _, path := range paths {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			if err := os.Remove(path + suffix); err != nil &&
				!errors.Is(err, os.ErrNotExist) {
				cleanupErrors = append(
					cleanupErrors, errors.New("remove restore staging file"),
				)
			}
		}
	}
	if err := os.Remove(directory); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		cleanupErrors = append(
			cleanupErrors, errors.New("remove restore staging directory"),
		)
	}
	return errors.Join(cleanupErrors...)
}

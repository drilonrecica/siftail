// Package backup owns verified backup artifact workflows.
package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/database"
)

const (
	FormatVersion = 1
	TypeFull      = "full"
)

type Result struct {
	Type          string    `json:"type"`
	Name          string    `json:"name"`
	CreatedAt     time.Time `json:"created_at"`
	SchemaVersion int       `json:"schema_version"`
	Bytes         int64     `json:"bytes"`
	SHA256        string    `json:"sha256"`
}

func (r Result) Validate() error {
	if r.Type != TypeFull || !safeBasename(r.Name) || r.CreatedAt.IsZero() ||
		r.CreatedAt.Year() < 1 || r.CreatedAt.Year() > 9999 ||
		r.SchemaVersion < 1 || r.SchemaVersion > database.MaxSchemaVersion ||
		r.Bytes <= 0 || len(r.SHA256) != sha256.Size*2 {
		return errors.New("invalid backup result")
	}
	if _, err := hex.DecodeString(r.SHA256); err != nil {
		return errors.New("invalid backup result")
	}
	return nil
}

type Service struct {
	source     *database.DB
	sourcePath string
	audit      *audit.Store
	now        func() time.Time
	copy       func(context.Context, *sql.DB, string, int, func(database.BackupProgress)) error
	verify     func(context.Context, string) (Result, error)
	stepPages  int
}

func NewService(
	source *database.DB,
	sourcePath string,
	auditStore *audit.Store,
) *Service {
	return &Service{
		source: source, sourcePath: sourcePath, audit: auditStore,
		now: time.Now, copy: database.OnlineBackup, verify: Verify,
		stepPages: database.DefaultBackupStepPages,
	}
}

func (s *Service) CreateFull(
	ctx context.Context,
	outputPath string,
	progress func(database.BackupProgress),
) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("backup context is nil")
	}
	if s == nil || s.source == nil || s.source.Reader() == nil ||
		s.sourcePath == "" || s.audit == nil || s.copy == nil || s.verify == nil {
		return Result{}, errors.New("backup service is unavailable")
	}
	finalPath, err := validateOutputPath(s.sourcePath, outputPath)
	if err != nil {
		return s.fail(ctx, outputPath, "invalid_output", err)
	}
	if err := ensureBackupSpace(s.source.Reader(), filepath.Dir(finalPath)); err != nil {
		return s.fail(ctx, finalPath, "insufficient_space", err)
	}
	stagingPath, cleanup, err := createStagingFile(finalPath)
	if err != nil {
		return s.fail(ctx, finalPath, "destination_unavailable", err)
	}
	keepStaging := false
	defer func() {
		if !keepStaging {
			cleanup()
		}
	}()

	if err := s.copy(
		ctx, s.source.Reader(), stagingPath,
		s.stepPages, progress,
	); err != nil {
		return s.fail(ctx, finalPath, failureCategory(err), err)
	}
	createdAt := s.now().UTC()
	if err := finalizeSnapshot(ctx, stagingPath, createdAt); err != nil {
		return s.fail(ctx, finalPath, failureCategory(err), err)
	}
	verified, err := s.verify(ctx, stagingPath)
	if err != nil {
		return s.fail(ctx, finalPath, "verification_failed", err)
	}
	if err := syncFile(stagingPath); err != nil {
		return s.fail(ctx, finalPath, "destination_unavailable", err)
	}
	if err := os.Link(stagingPath, finalPath); err != nil {
		return s.fail(ctx, finalPath, "destination_unavailable",
			fmt.Errorf("publish verified backup: %w", err))
	}
	if err := os.Remove(stagingPath); err != nil {
		_ = os.Remove(finalPath)
		return s.fail(ctx, finalPath, "destination_unavailable",
			errors.New("remove published backup staging link"))
	}
	keepStaging = true
	removeFinal := true
	defer func() {
		if removeFinal {
			_ = os.Remove(finalPath)
		}
	}()
	if err := syncDirectory(filepath.Dir(finalPath)); err != nil {
		return s.fail(ctx, finalPath, "destination_unavailable", err)
	}
	verified, err = s.verify(ctx, finalPath)
	if err != nil {
		return s.fail(ctx, finalPath, "verification_failed", err)
	}
	if verified.CreatedAt.IsZero() {
		verified.CreatedAt = createdAt
	}
	if _, err := s.audit.Record(ctx, backupAuditInput(
		ctx, audit.OutcomeSucceeded, finalPath, "",
	)); err != nil {
		return Result{}, errors.New("record successful full backup audit")
	}
	removeFinal = false
	return verified, nil
}

func (s *Service) fail(
	ctx context.Context,
	outputPath string,
	category string,
	cause error,
) (Result, error) {
	if s != nil && s.audit != nil {
		_, _ = s.audit.Record(ctx, backupAuditInput(
			ctx, audit.OutcomeFailed, outputPath, category,
		))
	}
	return Result{}, cause
}

func backupAuditInput(
	ctx context.Context,
	outcome audit.Outcome,
	outputPath string,
	category string,
) audit.Input {
	name := filepath.Base(outputPath)
	if !safeBasename(name) {
		name = "invalid"
	}
	metadata := audit.Metadata{
		audit.MetadataBackupType: TypeFull,
		audit.MetadataBackupName: name,
	}
	if category != "" {
		metadata[audit.MetadataResultCategory] = category
	}
	input := audit.InputFromContext(
		ctx, audit.CategoryBackupRestore, "backup.full", outcome, metadata,
	)
	input.OccurredAt = time.Now().UTC()
	return input
}

func validateOutputPath(sourcePath, outputPath string) (string, error) {
	if strings.TrimSpace(outputPath) == "" || strings.IndexByte(outputPath, 0) >= 0 {
		return "", errors.New("backup output path is invalid")
	}
	absolute, err := filepath.Abs(outputPath)
	if err != nil {
		return "", errors.New("backup output path is invalid")
	}
	absolute = filepath.Clean(absolute)
	sourceAbsolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", errors.New("backup source path is invalid")
	}
	if absolute == filepath.Clean(sourceAbsolute) ||
		absolute == filepath.Clean(sourceAbsolute)+"-wal" ||
		absolute == filepath.Clean(sourceAbsolute)+"-shm" ||
		!safeBasename(filepath.Base(absolute)) {
		return "", errors.New("backup output path is unsafe")
	}
	parent, err := os.Stat(filepath.Dir(absolute))
	if err != nil || !parent.IsDir() {
		return "", errors.New("backup output directory is unavailable")
	}
	if _, err := os.Lstat(absolute); err == nil {
		return "", errors.New("backup output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("backup output cannot be inspected")
	}
	return absolute, nil
}

func createStagingFile(finalPath string) (string, func(), error) {
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", func() {}, errors.New("generate backup staging name")
	}
	stagingPath := filepath.Join(
		filepath.Dir(finalPath),
		"."+filepath.Base(finalPath)+".partial-"+hex.EncodeToString(suffix[:]),
	)
	file, err := os.OpenFile(stagingPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", func() {}, errors.New("create backup staging file")
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(stagingPath)
		return "", func() {}, errors.New("close backup staging file")
	}
	return stagingPath, func() { _ = os.Remove(stagingPath) }, nil
}

func ensureBackupSpace(source *sql.DB, destinationDirectory string) error {
	var pageCount, pageSize int64
	if err := source.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		return database.Classify("measure backup source pages", err)
	}
	if err := source.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return database.Classify("measure backup source page size", err)
	}
	if pageCount < 0 || pageSize <= 0 ||
		pageCount > int64(^uint64(0)>>1)/pageSize {
		return errors.New("backup size is unsupported")
	}
	required := pageCount * pageSize
	slack := required / 20
	if slack < 1<<20 {
		slack = 1 << 20
	}
	if required > int64(^uint64(0)>>1)-slack {
		return errors.New("backup size is unsupported")
	}
	required += slack
	var stats syscall.Statfs_t
	if err := syscall.Statfs(destinationDirectory, &stats); err != nil {
		return errors.New("inspect backup destination capacity")
	}
	available := uint64(stats.Bavail) * uint64(stats.Bsize)
	if available < uint64(required) {
		return errors.New("backup destination has insufficient space")
	}
	return nil
}

func finalizeSnapshot(ctx context.Context, path string, createdAt time.Time) error {
	db, err := sql.Open("sqlite3", sqlitePath(path, "rw"))
	if err != nil {
		return errors.New("open staged backup")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, `
		PRAGMA busy_timeout=5000;
		PRAGMA synchronous=FULL;
		PRAGMA foreign_keys=ON;
		PRAGMA secure_delete=ON;
		PRAGMA journal_mode=DELETE;
	`); err != nil {
		return database.Classify("configure staged backup", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return database.Classify("begin staged backup finalization", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions"); err != nil {
		return database.Classify("exclude backup sessions", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DROP TABLE IF EXISTS siftail_backup_metadata;
		CREATE TABLE siftail_backup_metadata(
			format_version INTEGER NOT NULL,
			backup_type TEXT NOT NULL,
			created_at_us INTEGER NOT NULL,
			source_schema_version INTEGER NOT NULL,
			complete INTEGER NOT NULL CHECK(complete IN (0,1))
		) STRICT;
		INSERT INTO siftail_backup_metadata(
			format_version,backup_type,created_at_us,
			source_schema_version,complete
		) VALUES(?,?,?,?,1)`,
		FormatVersion, TypeFull, createdAt.UnixMicro(),
		database.MaxSchemaVersion,
	); err != nil {
		return database.Classify("write backup metadata", err)
	}
	if err := tx.Commit(); err != nil {
		return database.Classify("commit backup finalization", err)
	}
	return nil
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return errors.New("open backup for synchronization")
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return errors.New("synchronize backup")
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.New("open backup directory for synchronization")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("synchronize backup directory")
	}
	return nil
}

func checksum(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, errors.New("open backup checksum input")
	}
	defer file.Close()
	hash := sha256.New()
	bytes, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, errors.New("checksum backup")
	}
	return hex.EncodeToString(hash.Sum(nil)), bytes, nil
}

func sqlitePath(path, mode string) string {
	location := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := location.Query()
	query.Set("mode", mode)
	query.Set("_busy_timeout", "5000")
	query.Set("_foreign_keys", "on")
	location.RawQuery = query.Encode()
	return location.String()
}

func safeBasename(value string) bool {
	return value != "" && value != "." && value != ".." &&
		len(value) <= 255 && !strings.ContainsAny(value, `/\`) &&
		utf8.ValidString(value) &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func failureCategory(err error) string {
	var category *database.CategoryError
	if errors.As(err, &category) {
		switch category.Category {
		case database.CategoryFull:
			return "storage_full"
		case database.CategoryBusy:
			return "busy"
		case database.CategoryCorrupt:
			return "corrupt"
		case database.CategoryIO:
			return "io"
		}
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	return "unavailable"
}

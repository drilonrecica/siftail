package backup

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/logs"
)

func TestRestoreFullAndConfigurationPreservesVerifiedRollback(t *testing.T) {
	for _, backupType := range []string{TypeFull, TypeConfiguration} {
		t.Run(backupType, func(t *testing.T) {
			dataDirectory := t.TempDir()
			databasePath := filepath.Join(dataDirectory, "siftail.db")
			current := newBackupFixtureAt(t, databasePath)
			seedFullBackupState(t, current.db.Writer())
			if _, err := current.db.Writer().Exec(`
				INSERT INTO settings(key,value_json,updated_at_us)
				VALUES('current_only','{"current":true}',2)
			`); err != nil {
				t.Fatal(err)
			}
			current.stop()

			source := newBackupFixture(t)
			seedFullBackupState(t, source.db.Writer())
			if _, err := source.db.Writer().Exec(`
				INSERT INTO settings(key,value_json,updated_at_us)
				VALUES('artifact_only','{"artifact":true}',2)
			`); err != nil {
				t.Fatal(err)
			}
			artifactPath := filepath.Join(
				t.TempDir(), backupType+"-artifact.sqlite",
			)
			var err error
			if backupType == TypeFull {
				_, err = source.service.CreateFull(
					context.Background(), artifactPath, nil,
				)
			} else {
				_, err = source.service.CreateConfiguration(
					context.Background(), artifactPath, nil,
				)
			}
			if err != nil {
				t.Fatal(err)
			}

			result, err := NewRestorer().Restore(
				context.Background(), RestoreOptions{
					DataDirectory: dataDirectory,
					DatabasePath:  databasePath,
					ArtifactPath:  artifactPath,
					Confirmation:  RestoreConfirmation,
				},
			)
			if err != nil || result.Type != backupType ||
				result.RollbackName != "siftail.db.rollback" ||
				result.Validate() != nil {
				t.Fatalf("restore = %#v, %v", result, err)
			}
			assertRestoredState(t, databasePath, backupType)

			rollbackPath := databasePath + ".rollback"
			rollback, err := Verify(context.Background(), rollbackPath)
			if err != nil || rollback.Type != TypeFull {
				t.Fatalf("rollback verification = %#v, %v", rollback, err)
			}
			rollbackDB, err := sql.Open(
				"sqlite3", sqlitePath(rollbackPath, "ro"),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackDB.Close()
			var currentOnly int
			if err := rollbackDB.QueryRow(`
				SELECT count(*) FROM settings WHERE key='current_only'
			`).Scan(&currentOnly); err != nil || currentOnly != 1 {
				t.Fatalf("rollback current state = %d, %v", currentOnly, err)
			}
			if info, err := os.Stat(databasePath); err != nil ||
				info.Mode().Perm() != 0600 {
				t.Fatalf("restored permissions = %#v, %v", info, err)
			}
			if _, err := os.Stat(
				filepath.Join(dataDirectory, "restore-staging"),
			); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("restore staging remains: %v", err)
			}
		})
	}
}

func TestRestoreMigratesSupportedArtifactAndRejectsActiveOwner(t *testing.T) {
	dataDirectory := t.TempDir()
	databasePath := filepath.Join(dataDirectory, "siftail.db")
	current := newBackupFixtureAt(t, databasePath)
	seedFullBackupState(t, current.db.Writer())
	current.stop()
	artifact := createVerifiedTestArtifact(t)
	execBackupMutation(t, artifact, `
		INSERT INTO settings(key,value_json,updated_at_us)
		VALUES('artifact_only','{"artifact":true}',2);
		DROP TABLE security_audit_events;
		DELETE FROM schema_migrations WHERE version=4;
		UPDATE siftail_backup_metadata SET source_schema_version=3;
	`)
	if _, err := Verify(context.Background(), artifact); err != nil {
		t.Fatalf("supported older artifact: %v", err)
	}

	lock, err := database.AcquireMaintenanceLock(dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRestorer().Restore(context.Background(), RestoreOptions{
		DataDirectory: dataDirectory, DatabasePath: databasePath,
		ArtifactPath: artifact, Confirmation: RestoreConfirmation,
	})
	if err == nil {
		t.Fatal("restore succeeded while database owner was active")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := NewRestorer().Restore(
		context.Background(), RestoreOptions{
			DataDirectory: dataDirectory, DatabasePath: databasePath,
			ArtifactPath: artifact, Confirmation: RestoreConfirmation,
		},
	)
	if err != nil || !result.Migrated || result.SchemaBefore != 3 ||
		result.SchemaAfter != database.MaxSchemaVersion {
		t.Fatalf("migrated restore = %#v, %v", result, err)
	}
	assertRestoredState(t, databasePath, TypeFull)
}

func TestRestoreFaultAfterReplacementRollsBackCurrentDatabase(t *testing.T) {
	for _, fault := range []string{
		"after install", "before rollback publish", "canceled after install",
	} {
		t.Run(fault, func(t *testing.T) {
			dataDirectory := t.TempDir()
			databasePath := filepath.Join(dataDirectory, "siftail.db")
			current := newBackupFixtureAt(t, databasePath)
			seedFullBackupState(t, current.db.Writer())
			if _, err := current.db.Writer().Exec(`
				INSERT INTO settings(key,value_json,updated_at_us)
				VALUES('current_only','{"current":true}',2)
			`); err != nil {
				t.Fatal(err)
			}
			current.stop()
			artifact := createVerifiedTestArtifact(t)
			restorer := NewRestorer()
			restoreContext := context.Background()
			if fault == "after install" {
				restorer.hooks.afterInstall = func() error {
					return errors.New("injected interruption")
				}
			} else if fault == "canceled after install" {
				var cancel context.CancelFunc
				restoreContext, cancel = context.WithCancel(
					context.Background(),
				)
				restorer.hooks.afterInstall = func() error {
					cancel()
					return context.Canceled
				}
			} else {
				restorer.hooks.beforeRollbackPublish = func() error {
					return errors.New("injected interruption")
				}
			}
			if _, err := restorer.Restore(
				restoreContext, RestoreOptions{
					DataDirectory: dataDirectory,
					DatabasePath:  databasePath,
					ArtifactPath:  artifact,
					Confirmation:  RestoreConfirmation,
				},
			); err == nil {
				t.Fatal("faulted restore succeeded")
			}
			db, err := database.Open(context.Background(), databasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var currentOnly, sessions, failedAudit int
			if err := db.Reader().QueryRow(`
				SELECT count(*) FROM settings WHERE key='current_only'
			`).Scan(&currentOnly); err != nil {
				t.Fatal(err)
			}
			if err := db.Reader().QueryRow(
				"SELECT count(*) FROM sessions",
			).Scan(&sessions); err != nil {
				t.Fatal(err)
			}
			if err := db.Reader().QueryRow(`
				SELECT count(*) FROM security_audit_events
				WHERE action='restore.apply' AND
					outcome IN ('failed','canceled')
			`).Scan(&failedAudit); err != nil {
				t.Fatal(err)
			}
			if currentOnly != 1 || sessions != 0 || failedAudit != 1 {
				t.Fatalf(
					"rolled back state = current %d sessions %d audit %d",
					currentOnly, sessions, failedAudit,
				)
			}
		})
	}
}

func TestManagedRollbackCanRecoverThePreRestoreDatabase(t *testing.T) {
	dataDirectory := t.TempDir()
	databasePath := filepath.Join(dataDirectory, "siftail.db")
	current := newBackupFixtureAt(t, databasePath)
	seedFullBackupState(t, current.db.Writer())
	if _, err := current.db.Writer().Exec(`
		INSERT INTO settings(key,value_json,updated_at_us)
		VALUES('current_only','{"current":true}',2)
	`); err != nil {
		t.Fatal(err)
	}
	current.stop()

	source := newBackupFixture(t)
	seedFullBackupState(t, source.db.Writer())
	if _, err := source.db.Writer().Exec(`
		INSERT INTO settings(key,value_json,updated_at_us)
		VALUES('artifact_only','{"artifact":true}',2)
	`); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(t.TempDir(), "artifact.sqlite")
	if _, err := source.service.CreateFull(
		context.Background(), artifact, nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRestorer().Restore(
		context.Background(), RestoreOptions{
			DataDirectory: dataDirectory, DatabasePath: databasePath,
			ArtifactPath: artifact, Confirmation: RestoreConfirmation,
		},
	); err != nil {
		t.Fatal(err)
	}
	recoveryCopy := filepath.Join(t.TempDir(), "rollback-recovery.sqlite")
	if err := copyRestoreArtifact(
		context.Background(), databasePath+".rollback", recoveryCopy,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRestorer().Restore(
		context.Background(), RestoreOptions{
			DataDirectory: dataDirectory, DatabasePath: databasePath,
			ArtifactPath: recoveryCopy, Confirmation: RestoreConfirmation,
		},
	); err != nil {
		t.Fatal(err)
	}
	recovered, err := database.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	var currentOnly, artifactOnly, sessions int
	if err := recovered.Reader().QueryRow(`
		SELECT count(*) FROM settings WHERE key='current_only'
	`).Scan(&currentOnly); err != nil {
		t.Fatal(err)
	}
	if err := recovered.Reader().QueryRow(`
		SELECT count(*) FROM settings WHERE key='artifact_only'
	`).Scan(&artifactOnly); err != nil {
		t.Fatal(err)
	}
	if err := recovered.Reader().QueryRow(
		"SELECT count(*) FROM sessions",
	).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if currentOnly != 1 || artifactOnly != 0 || sessions != 0 {
		t.Fatalf("recovered rollback = current %d artifact %d sessions %d",
			currentOnly, artifactOnly, sessions)
	}
}

func TestRestoreRequiresConfirmationAndPreservesInvalidInputs(t *testing.T) {
	dataDirectory := t.TempDir()
	databasePath := filepath.Join(dataDirectory, "siftail.db")
	current := newBackupFixtureAt(t, databasePath)
	seedFullBackupState(t, current.db.Writer())
	current.stop()
	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(t.TempDir(), "private-artifact.sqlite")
	if err := os.WriteFile(corrupt, []byte("private-content"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []RestoreOptions{
		{
			DataDirectory: dataDirectory, DatabasePath: databasePath,
			ArtifactPath: corrupt, Confirmation: "",
		},
		{
			DataDirectory: dataDirectory, DatabasePath: databasePath,
			ArtifactPath: corrupt, Confirmation: RestoreConfirmation,
		},
	} {
		if _, err := NewRestorer().Restore(
			context.Background(), test,
		); err == nil {
			t.Fatal("invalid restore succeeded")
		}
	}
	after, err := os.ReadFile(databasePath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("invalid restore changed database: %v", err)
	}

	stagingDirectory := filepath.Join(dataDirectory, "restore-staging")
	if err := os.Mkdir(stagingDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	recoveryMarker := filepath.Join(stagingDirectory, "rollback.sqlite")
	if err := os.WriteFile(
		recoveryMarker, []byte("preserve-recovery"), 0600,
	); err != nil {
		t.Fatal(err)
	}
	validArtifact := createVerifiedTestArtifact(t)
	if _, err := NewRestorer().Restore(
		context.Background(), RestoreOptions{
			DataDirectory: dataDirectory, DatabasePath: databasePath,
			ArtifactPath: validArtifact, Confirmation: RestoreConfirmation,
		},
	); err == nil {
		t.Fatal("restore replaced incomplete recovery staging")
	}
	recovery, err := os.ReadFile(recoveryMarker)
	if err != nil || string(recovery) != "preserve-recovery" {
		t.Fatalf("recovery staging changed: %q, %v", recovery, err)
	}
}

func newBackupFixtureAt(t *testing.T, sourcePath string) *backupFixture {
	t.Helper()
	db, err := database.Open(context.Background(), sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := database.NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	auditStore := audit.NewStore(db.Reader(), coordinator)
	var once sync.Once
	stop := func() {
		once.Do(func() {
			coordinator.Close()
			cancel()
			if err := <-done; err != nil {
				t.Errorf("coordinator: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Errorf("database close: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return &backupFixture{
		db: db, coordinator: coordinator, audit: auditStore,
		service:    NewService(db, sourcePath, auditStore),
		sourcePath: sourcePath, stop: stop,
	}
}

func assertRestoredState(t *testing.T, path, backupType string) {
	t.Helper()
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var artifactOnly, currentOnly, sessions, metadata, audits, events int
	for query, target := range map[string]*int{
		"SELECT count(*) FROM settings WHERE key='artifact_only'": &artifactOnly,
		"SELECT count(*) FROM settings WHERE key='current_only'":  &currentOnly,
		"SELECT count(*) FROM sessions":                           &sessions,
		`SELECT count(*) FROM sqlite_schema
			WHERE type='table' AND name='siftail_backup_metadata'`: &metadata,
		`SELECT count(*) FROM security_audit_events
			WHERE action='restore.apply' AND outcome='succeeded'`: &audits,
		"SELECT count(*) FROM log_events": &events,
	} {
		if err := db.Reader().QueryRow(query).Scan(target); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	wantEvents := 1
	if backupType == TypeConfiguration {
		wantEvents = 0
	}
	if artifactOnly != 1 || currentOnly != 0 || sessions != 0 ||
		metadata != 0 || audits != 1 || events != wantEvents {
		t.Fatalf(
			"restored state = artifact %d current %d sessions %d metadata %d audits %d events %d",
			artifactOnly, currentOnly, sessions, metadata, audits, events,
		)
	}
	coordinator := database.NewCoordinator(db.Writer())
	coordinatorContext, cancelCoordinator := context.WithCancel(
		context.Background(),
	)
	coordinatorDone := make(chan error, 1)
	go func() {
		coordinatorDone <- coordinator.Run(coordinatorContext)
	}()
	<-coordinator.Ready()
	writer := ingest.NewBatchWriter(coordinator, nil)
	event := logs.CanonicalEvent{
		EventAtUS: 10, ReceivedAtUS: 10,
		Source: logs.SourceIdentity{
			ServerID: 1, Project: "p", Environment: "e",
			Application: "a", Service: "s", ProjectLabel: "p",
			EnvLabel: "e", AppLabel: "a", ServiceLabel: "s",
		},
		Stream: logs.StreamStdout, Level: logs.LevelInfo,
		MessageRaw: []byte("post-restore"), MessageText: "post-restore",
		Attributes: []byte("{}"), SourceEventID: "post-restore",
	}
	if err := writer.Persist(context.Background(), &ingest.WriteBatch{
		Events: []logs.CanonicalEvent{event}, AuthenticatedServerID: 1,
	}); err != nil {
		t.Fatalf("post-restore ingestion: %v", err)
	}
	coordinator.Close()
	cancelCoordinator()
	if err := <-coordinatorDone; err != nil {
		t.Fatalf("post-restore coordinator: %v", err)
	}
	recent, err := logs.NewStore(db.Reader()).Recent(context.Background(), 10)
	if err != nil || len(recent) != wantEvents+1 ||
		recent[0].MessageText != "post-restore" {
		t.Fatalf("restored History query = %#v, %v", recent, err)
	}
}

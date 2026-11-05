package backup

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/database"
)

type backupFixture struct {
	db          *database.DB
	coordinator *database.Coordinator
	audit       *audit.Store
	service     *Service
	sourcePath  string
	stop        func()
}

func newBackupFixture(t *testing.T) *backupFixture {
	t.Helper()
	sourcePath := filepath.Join(t.TempDir(), "siftail.db")
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

func TestCreateFullBackupExcludesSessionsAndRestores(t *testing.T) {
	fixture := newBackupFixture(t)
	seedFullBackupState(t, fixture.db.Writer())
	output := filepath.Join(t.TempDir(), "full.sqlite")
	var progress []Progress
	result, err := fixture.service.CreateFull(
		context.Background(), output,
		func(value Progress) { progress = append(progress, value) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) < 2 || progress[0].Completed != 0 ||
		progress[len(progress)-1].Completed != progress[len(progress)-1].Total ||
		progress[0].Total <= 0 || progress[0].Unit != "bytes" {
		t.Fatalf("progress = %#v", progress)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result: %v", err)
	}
	if result.Name != "full.sqlite" || result.Type != TypeFull {
		t.Fatalf("result = %#v", result)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("backup permissions = %04o", info.Mode().Perm())
	}
	artifactBytes, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(artifactBytes, []byte("private-session-marker")) {
		t.Fatal("backup retained deleted browser session content")
	}
	assertNoPartialFiles(t, filepath.Dir(output))

	verified, err := Verify(context.Background(), output)
	if err != nil {
		t.Fatal(err)
	}
	if verified.SHA256 != result.SHA256 || verified.Bytes != result.Bytes {
		t.Fatalf("verification result = %#v, create result = %#v",
			verified, result)
	}

	restoredPath := filepath.Join(t.TempDir(), "restored.sqlite")
	restoreFile, err := os.OpenFile(
		restoredPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.SnapshotInto(
		context.Background(), output, restoredPath, nil,
	); err != nil {
		t.Fatalf("restore artifact snapshot: %v", err)
	}
	restored, err := sql.Open("sqlite3", sqlitePath(restoredPath, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for query, want := range map[string]int{
		"SELECT count(*) FROM administrators":          1,
		"SELECT count(*) FROM ingestion_tokens":        1,
		"SELECT count(*) FROM sources":                 1,
		"SELECT count(*) FROM log_events":              1,
		"SELECT count(*) FROM security_audit_events":   1,
		"SELECT count(*) FROM sessions":                0,
		"SELECT count(*) FROM siftail_backup_metadata": 1,
	} {
		var got int
		if err := restored.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}
	var integrity string
	if err := restored.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil ||
		integrity != "ok" {
		t.Fatalf("restored integrity = %q, %v", integrity, err)
	}

	page, err := fixture.audit.List(context.Background(), audit.Query{
		Action: "backup.full", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 ||
		page.Events[0].Outcome != audit.OutcomeSucceeded ||
		page.Events[0].Metadata[audit.MetadataBackupName] != "full.sqlite" ||
		page.Events[0].Metadata[audit.MetadataBackupType] != TypeFull {
		t.Fatalf("backup audit = %#v", page.Events)
	}
}

func TestCreateFullBackupFailureCleansStagingAndNeverOverwrites(t *testing.T) {
	tests := []struct {
		name string
		copy func(context.Context, string, string, func(database.SnapshotProgress)) error
	}{
		{
			name: "destination full",
			copy: func(
				_ context.Context,
				_ string,
				path string,
				_ func(database.SnapshotProgress),
			) error {
				destination, err := sql.Open("sqlite3", sqlitePath(path, "rw"))
				if err != nil {
					return err
				}
				defer destination.Close()
				if _, err := destination.Exec(`
					PRAGMA page_size=4096;
					PRAGMA max_page_count=2;
					CREATE TABLE destination_quota(value BLOB);
				`); err != nil {
					return err
				}
				_, err = destination.Exec(
					"INSERT INTO destination_quota VALUES(zeroblob(32768))",
				)
				return database.Classify("write backup destination", err)
			},
		},
		{
			name: "interrupted partial",
			copy: func(_ context.Context, _ string, path string, _ func(database.SnapshotProgress)) error {
				if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
					return err
				}
				return errors.New("interrupted")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBackupFixture(t)
			outputDirectory := t.TempDir()
			output := filepath.Join(outputDirectory, "failed.sqlite")
			fixture.service.copy = test.copy
			if _, err := fixture.service.CreateFull(
				context.Background(), output, nil,
			); err == nil {
				t.Fatal("failed backup succeeded")
			}
			if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed final artifact exists: %v", err)
			}
			assertNoPartialFiles(t, outputDirectory)
			page, err := fixture.audit.List(context.Background(), audit.Query{
				Action: "backup.full", Limit: 10,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Events) != 1 ||
				page.Events[0].Outcome != audit.OutcomeFailed ||
				page.Events[0].Metadata[audit.MetadataBackupName] !=
					"failed.sqlite" ||
				page.Events[0].Metadata[audit.MetadataResultCategory] == "" {
				t.Fatalf("failed backup audit = %#v", page.Events)
			}
		})
	}

	fixture := newBackupFixture(t)
	output := filepath.Join(t.TempDir(), "existing.sqlite")
	if err := os.WriteFile(output, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.CreateFull(
		context.Background(), output, nil,
	); err == nil {
		t.Fatal("existing destination was overwritten")
	}
	contents, err := os.ReadFile(output)
	if err != nil || string(contents) != "preserve" {
		t.Fatalf("existing destination changed: %q, %v", contents, err)
	}

	fixture = newBackupFixture(t)
	seedFullBackupState(t, fixture.db.Writer())
	outputDirectory := t.TempDir()
	output = filepath.Join(outputDirectory, "raced.sqlite")
	verifyCalls := 0
	fixture.service.verify = func(ctx context.Context, staging string) (Result, error) {
		result, err := Verify(ctx, staging)
		if err == nil && verifyCalls == 0 {
			verifyCalls++
			if writeErr := os.WriteFile(
				output, []byte("concurrent-owner"), 0600,
			); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		return result, err
	}
	if _, err := fixture.service.CreateFull(
		context.Background(), output, nil,
	); err == nil {
		t.Fatal("raced destination was overwritten")
	}
	contents, err = os.ReadFile(output)
	if err != nil || string(contents) != "concurrent-owner" {
		t.Fatalf("raced destination changed: %q, %v", contents, err)
	}
	assertNoPartialFiles(t, outputDirectory)
}

func TestCreateFullBackupCancellationAndVerificationFailureCleanUp(t *testing.T) {
	fixture := newBackupFixture(t)
	outputDirectory := t.TempDir()
	output := filepath.Join(outputDirectory, "canceled.sqlite")
	fixture.service.copy = func(
		ctx context.Context,
		_ string,
		_ string,
		_ func(database.SnapshotProgress),
	) error {
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.service.CreateFull(ctx, output, nil); !errors.Is(
		err, context.Canceled,
	) {
		t.Fatalf("canceled backup = %v", err)
	}
	assertNoPartialFiles(t, outputDirectory)

	fixture = newBackupFixture(t)
	seedFullBackupState(t, fixture.db.Writer())
	outputDirectory = t.TempDir()
	output = filepath.Join(outputDirectory, "unverified.sqlite")
	fixture.service.verify = func(context.Context, string) (Result, error) {
		return Result{}, errors.New("injected verification failure")
	}
	if _, err := fixture.service.CreateFull(
		context.Background(), output, nil,
	); err == nil {
		t.Fatal("unverified backup succeeded")
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unverified final artifact exists: %v", err)
	}
	assertNoPartialFiles(t, outputDirectory)
}

func TestCreateFullBackupRejectsUnsafePaths(t *testing.T) {
	fixture := newBackupFixture(t)
	for _, output := range []string{
		"", fixture.sourcePath, fixture.sourcePath + "-wal",
		filepath.Join(t.TempDir(), "missing", "backup.sqlite"),
	} {
		if _, err := fixture.service.CreateFull(
			context.Background(), output, nil,
		); err == nil {
			t.Fatalf("unsafe output %q succeeded", output)
		}
	}
}

func seedFullBackupState(t *testing.T, db *sql.DB) {
	t.Helper()
	message := strings.Repeat("retained-", 16<<10)
	if _, err := db.Exec(`
		INSERT INTO administrators(
			id,username,password_hash,created_at_us,password_changed_at_us
		) VALUES(1,'admin',
			'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			1,1);
		INSERT INTO sessions(
			id,administrator_id,token_hash,created_at_us,expires_at_us,
			last_used_at_us,user_agent_summary,client_identity_summary
		) VALUES(1,1,zeroblob(32),1,9999999999999999,1,
			'private-session-marker','local');
		INSERT INTO servers(id,name,created_at_us)
		VALUES(1,'server',1);
		INSERT INTO ingestion_tokens(
			id,server_id,name,token_hash,fingerprint,created_at_us
		) VALUES(1,1,'token',zeroblob(32),'123456789012',1);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			first_seen_at_us,last_seen_at_us
		) VALUES(1,1,'p','e','a','s','p','e','a','s',1,1);
		INSERT INTO log_events(
			id,event_at_us,received_at_us,source_id,stream,level_normalized,
			message_raw,message_text,attributes_json
		) VALUES(1,1,1,1,'stdout','info',?,?, '{}');
		INSERT INTO security_audit_events(
			id,occurred_at_us,category,action,outcome,actor_type,
			safe_metadata_json
		) VALUES(1,1,'authentication','session.login','succeeded',
			'administrator','{}')`,
		[]byte(message), message,
	); err != nil {
		t.Fatal(err)
	}
}

func assertNoPartialFiles(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".partial-") {
			t.Fatalf("partial backup remains: %s", entry.Name())
		}
	}
}

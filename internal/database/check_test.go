package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoppedDatabaseCheckIsReadOnlyBoundedAndReportsCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(`INSERT INTO settings(
		key,value_json,updated_at_us
	) VALUES('check-preserved','{}',1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := CheckPath(context.Background(), path, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Mode != "quick" || !report.Compatible ||
		report.SchemaVersion != MaxSchemaVersion ||
		report.SQLiteVersion == "" ||
		report.Integrity != "ok" || report.JournalMode != "wal" ||
		report.Synchronous != "2" || !report.ForeignKeys ||
		report.AutoVacuum != "incremental" ||
		report.Checkpoint != "not_run_read_only" ||
		report.DatabaseBytes <= 0 || report.PageCount <= 0 ||
		before.Size() != after.Size() {
		t.Fatalf("report = %#v, before/after = %d/%d",
			report, before.Size(), after.Size())
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	full, err := CheckPath(context.Background(), path, true)
	if err != nil || full.Mode != "full" || full.Integrity != "ok" {
		t.Fatalf("full report = %#v, err=%v", full, err)
	}
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var count int
	if err := raw.QueryRow(
		"SELECT count(*) FROM settings WHERE key='check-preserved'",
	).Scan(&count); err != nil || count != 1 {
		t.Fatalf("preserved count = %d, err=%v", count, err)
	}
}

func TestStoppedDatabaseCheckClassifiesCorruptIncompatibleReadOnlyAndCanceled(t *testing.T) {
	t.Run("corrupt", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "corrupt.db")
		marker := []byte("private-payload-token-not-a-database")
		if err := os.WriteFile(path, marker, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := CheckPath(context.Background(), path, false)
		var category *CategoryError
		if !errors.As(err, &category) ||
			category.Category != CategoryCorrupt ||
			strings.Contains(err.Error(), string(marker)) {
			t.Fatalf("corrupt error = %v", err)
		}
		after, readErr := os.ReadFile(path)
		if readErr != nil || string(after) != string(marker) {
			t.Fatal("corrupt check changed its target")
		}
	})

	t.Run("incompatible", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "newer.db")
		raw, err := sql.Open("sqlite3", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(`CREATE TABLE schema_migrations(
			version INTEGER PRIMARY KEY
		); INSERT INTO schema_migrations VALUES(99)`); err != nil {
			t.Fatal(err)
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		report, err := CheckPath(context.Background(), path, false)
		var tooNew *SchemaTooNewError
		if !errors.As(err, &tooNew) || report.Compatible ||
			report.SchemaVersion != 99 || report.SupportedSchema != MaxSchemaVersion {
			t.Fatalf("newer report = %#v, err=%v", report, err)
		}
	})

	t.Run("read only and canceled", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "readonly.db")
		db, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0400); err != nil {
			t.Fatal(err)
		}
		report, err := CheckPath(context.Background(), path, false)
		if err != nil {
			t.Fatal(err)
		}
		if report.Writable ||
			report.WritabilitySource != "filesystem_access" {
			t.Fatalf("read-only report = %#v", report)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = CheckPath(ctx, path, false)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled error = %v", err)
		}
	})
}

func TestActiveDatabaseCheckUsesCoordinatorAndReportsBusyCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	coordinator, cancel, done := runTestCoordinator(t, db)
	defer func() {
		coordinator.Close()
		cancel()
		<-done
	}()
	if err := coordinator.Do(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO settings(
			key,value_json,updated_at_us
		) VALUES('before-reader','{}',1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reader, err := db.Reader().Begin()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reader.QueryRow("SELECT count(*) FROM settings").Scan(&count); err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	if err := coordinator.Do(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO settings(
			key,value_json,updated_at_us
		) VALUES('after-reader','{}',2)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	checker := NewActiveChecker(db, path, coordinator, func() bool { return true })
	report, err := checker.Check(context.Background())
	var category *CategoryError
	if !errors.As(err, &category) || category.Category != CategoryBusy ||
		report.Checkpoint != "busy" || report.WALFrames <= 0 ||
		report.CheckpointedFrames >= report.WALFrames ||
		!report.Writable || report.WritabilitySource != "operational_state" {
		t.Fatalf("active report = %#v, err=%v", report, err)
	}
	last, ok := checker.Last()
	if !ok || last.ErrorCategory != string(CategoryBusy) {
		t.Fatalf("last active check = %#v, ok=%v", last, ok)
	}
}

func TestDatabaseCheckSummaryIsFixedAndPathFree(t *testing.T) {
	report := CheckReport{
		Mode: "quick", SchemaVersion: 4, SupportedSchema: 4,
		SQLiteVersion: "3.51.2", Compatible: true, Integrity: "ok", JournalMode: "wal",
		Synchronous: "2", ForeignKeys: true, AutoVacuum: "incremental",
		Writable: true, WritabilitySource: "operational_state",
		Checkpoint: "completed", DatabaseBytes: 1, PageCount: 1,
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	output := strings.Join(report.SummaryLines(), "\n")
	for _, forbidden := range []string{
		"/data/private.db", "password", "token", "payload", "authorization",
	} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("summary exposed %q: %s", forbidden, output)
		}
	}
}

func BenchmarkStoppedDatabaseCheck(b *testing.B) {
	path := filepath.Join(b.TempDir(), "benchmark.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := db.Writer().Exec(`WITH RECURSIVE n(value) AS (
		SELECT 1 UNION ALL SELECT value+1 FROM n WHERE value < 100000
	)
	INSERT INTO security_audit_events(
		occurred_at_us,category,action,outcome,actor_type,safe_metadata_json
	)
	SELECT value,'authentication','sign_in','rejected',
		'unauthenticated','{}' FROM n`); err != nil {
		b.Fatal(err)
	}
	if err := db.Close(); err != nil {
		b.Fatal(err)
	}
	for _, full := range []bool{false, true} {
		name := "quick"
		if full {
			name = "full"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				report, err := CheckPath(context.Background(), path, full)
				if err != nil || report.Integrity != "ok" {
					b.Fatalf("report = %#v, err=%v", report, err)
				}
			}
		})
	}
}

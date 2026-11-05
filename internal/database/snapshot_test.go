package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func createEmptySnapshotFile(t *testing.T) string {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "snapshot.sqlite")
	file, err := os.OpenFile(
		destination, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return destination
}

func openSnapshotSource(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func TestSnapshotIntoCopiesConsistentDataWithoutFreePages(t *testing.T) {
	db, sourcePath := openSnapshotSource(t)
	if _, err := db.Writer().Exec(`
		CREATE TABLE snapshot_bulk(id INTEGER PRIMARY KEY, value BLOB);
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<64)
		INSERT INTO snapshot_bulk(id,value) SELECT i, randomblob(32768) FROM n;
		DELETE FROM snapshot_bulk WHERE id > 32;
	`); err != nil {
		t.Fatal(err)
	}
	var freelist int
	if err := db.Writer().QueryRow("PRAGMA freelist_count").Scan(&freelist); err != nil ||
		freelist == 0 {
		t.Fatalf("source freelist = %d, %v", freelist, err)
	}
	destination := createEmptySnapshotFile(t)
	var reports []SnapshotProgress
	if err := SnapshotInto(
		context.Background(), sourcePath, destination,
		func(progress SnapshotProgress) { reports = append(reports, progress) },
	); err != nil {
		t.Fatal(err)
	}
	if len(reports) < 2 || reports[0].Written != 0 ||
		reports[len(reports)-1].Written != reports[len(reports)-1].Total ||
		reports[0].Total <= 0 {
		t.Fatalf("progress reports = %#v", reports)
	}
	for _, report := range reports {
		if report.Written < 0 || report.Written > report.Total {
			t.Fatalf("progress report out of range = %#v", report)
		}
	}
	copied, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(destination)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	var rows, copiedFreelist int
	var integrity string
	if err := copied.QueryRow("SELECT count(*) FROM snapshot_bulk").Scan(&rows); err != nil ||
		rows != 32 {
		t.Fatalf("copied rows = %d, %v", rows, err)
	}
	if err := copied.QueryRow("PRAGMA freelist_count").Scan(&copiedFreelist); err != nil ||
		copiedFreelist != 0 {
		t.Fatalf("copied freelist = %d, %v", copiedFreelist, err)
	}
	if err := copied.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil ||
		integrity != "ok" {
		t.Fatalf("copied integrity = %q, %v", integrity, err)
	}
}

func TestSnapshotIntoHonorsCancellation(t *testing.T) {
	db, sourcePath := openSnapshotSource(t)
	if _, err := db.Writer().Exec(`
		CREATE TABLE snapshot_bulk(value BLOB);
		INSERT INTO snapshot_bulk(value) VALUES(zeroblob(2097152));
	`); err != nil {
		t.Fatal(err)
	}
	destination := createEmptySnapshotFile(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := SnapshotInto(ctx, sourcePath, destination,
		func(SnapshotProgress) { cancel() },
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled snapshot = %v", err)
	}
	if info, err := os.Stat(destination); err != nil || info.Size() >= 2097152 {
		t.Fatalf("canceled snapshot destination = %#v, %v", info, err)
	}
	cancel()
}

func TestSnapshotIntoInterruptsRunningStatement(t *testing.T) {
	db, sourcePath := openSnapshotSource(t)
	if _, err := db.Writer().Exec(`
		CREATE TABLE snapshot_bulk(id INTEGER PRIMARY KEY, value BLOB);
		WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<1024)
		INSERT INTO snapshot_bulk(id,value) SELECT i, randomblob(65536) FROM n;
	`); err != nil {
		t.Fatal(err)
	}
	previous := snapshotProgressInterval
	snapshotProgressInterval = time.Millisecond
	t.Cleanup(func() { snapshotProgressInterval = previous })
	destination := createEmptySnapshotFile(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var total int64
	err := SnapshotInto(ctx, sourcePath, destination,
		func(progress SnapshotProgress) {
			total = progress.Total
			if progress.Written > 0 && progress.Written < progress.Total {
				cancel()
			}
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted snapshot = %v", err)
	}
	info, statErr := os.Stat(destination)
	if statErr != nil || info.Size() >= total {
		t.Fatalf("interrupted snapshot destination = %#v of %d, %v",
			info, total, statErr)
	}
}

func TestSnapshotIntoRejectsUnavailableInputs(t *testing.T) {
	_, sourcePath := openSnapshotSource(t)
	if err := SnapshotInto(nil, sourcePath, "destination", nil); err == nil {
		t.Fatal("nil context snapshot succeeded")
	}
	if err := SnapshotInto(
		context.Background(), "", "destination", nil,
	); err == nil {
		t.Fatal("nil source snapshot succeeded")
	}
	missing := filepath.Join(t.TempDir(), "missing.sqlite")
	if err := SnapshotInto(
		context.Background(), sourcePath, missing, nil,
	); err == nil {
		t.Fatal("missing destination snapshot succeeded")
	}
	nonEmpty := createEmptySnapshotFile(t)
	if err := os.WriteFile(nonEmpty, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotInto(
		context.Background(), sourcePath, nonEmpty, nil,
	); err == nil {
		t.Fatal("non-empty destination snapshot succeeded")
	}
	if contents, err := os.ReadFile(nonEmpty); err != nil ||
		string(contents) != "occupied" {
		t.Fatalf("non-empty destination changed: %q, %v", contents, err)
	}
}

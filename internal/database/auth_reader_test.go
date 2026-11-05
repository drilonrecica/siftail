package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// holdReaderPool checks out every ordinary reader connection and opens a read
// snapshot on each, as concurrent exports and contains searches would.
func holdReaderPool(t *testing.T, db *DB) {
	t.Helper()
	for range readConnections {
		conn, err := db.Reader().Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		tx, err := conn.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		var count int
		if err := tx.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = tx.Rollback()
			_ = conn.Close()
		})
	}
}

func TestAuthReaderIsIsolatedFromExhaustedReaderPool(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if db.AuthReader() == nil || db.AuthReader() == db.Reader() {
		t.Fatal("authentication reader must be its own pool")
	}
	if stats := db.AuthReader().Stats(); stats.MaxOpenConnections != authConnections {
		t.Fatalf("authentication pool cap = %d", stats.MaxOpenConnections)
	}
	holdReaderPool(t, db)

	starved, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var count int
	if err := db.Reader().QueryRowContext(starved,
		"SELECT count(*) FROM ingestion_tokens").Scan(&count); err == nil {
		t.Fatal("ordinary reader pool was not exhausted by the test")
	}

	bounded, cancelBounded := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelBounded()
	if err := db.AuthReader().QueryRowContext(bounded,
		"SELECT count(*) FROM ingestion_tokens").Scan(&count); err != nil {
		t.Fatalf("authentication read waited on the ordinary pool: %v", err)
	}
	if _, err := db.AuthReader().ExecContext(bounded,
		"INSERT INTO servers(name, created_at_us) VALUES ('x', 1)"); err == nil {
		t.Fatal("authentication reader accepted a write")
	}
}

func TestAuthReaderClosesWithDatabase(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	auth := db.AuthReader()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := auth.PingContext(context.Background()); err == nil {
		t.Fatal("authentication reader still open after Close")
	}
}

// The History and export bounds rely on SQLite interrupting a running
// statement when its context expires, which releases the read snapshot.
func TestReaderContextDeadlineInterruptsRunningQuery(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	var count int64
	err = db.Reader().QueryRowContext(ctx, `WITH RECURSIVE counter(x) AS (
		SELECT 1 UNION ALL SELECT x + 1 FROM counter
	) SELECT count(*) FROM counter`).Scan(&count)
	if err == nil {
		t.Fatalf("unbounded query completed with %d rows", count)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("interrupt took %s", elapsed)
	}
	if ctx.Err() == nil {
		t.Fatalf("query failed before its deadline: %v", err)
	}
	stats := db.Reader().Stats()
	if stats.InUse != 0 {
		t.Fatalf("interrupted query kept %d reader connections", stats.InUse)
	}
}

package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenConfiguresDurabilityAndPools(t *testing.T) {
	db := openTestDB(t)
	checks := map[string]string{
		"PRAGMA journal_mode": "wal",
		"PRAGMA synchronous":  "2",
		"PRAGMA foreign_keys": "1",
		"PRAGMA busy_timeout": "5000",
		"PRAGMA temp_store":   "2",
		"PRAGMA auto_vacuum":  "2",
	}
	for query, want := range checks {
		var got string
		if err := db.Writer().QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", query, got, want)
		}
	}
	if got := db.Writer().Stats().MaxOpenConnections; got != 1 {
		t.Errorf("writer max connections = %d", got)
	}
	if got := db.Reader().Stats().MaxOpenConnections; got != 4 {
		t.Errorf("reader max connections = %d", got)
	}
	if _, err := db.Reader().Exec("CREATE TABLE forbidden (id INTEGER)"); err == nil {
		t.Fatal("read pool allowed a write")
	}
}

func TestOpenRejectsCorruptDatabaseWithoutReplacingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	original := []byte("not a sqlite database and must remain")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), path)
	if err == nil {
		t.Fatal("corrupt database opened")
	}
	var category *CategoryError
	if !errors.As(err, &category) || category.Category != CategoryCorrupt {
		t.Fatalf("error = %#v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(original) {
		t.Fatal("corrupt database was replaced")
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA auto_vacuum=INCREMENTAL; VACUUM; CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY); INSERT INTO schema_migrations VALUES (99)"); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	_, err = Open(context.Background(), path)
	var tooNew *SchemaTooNewError
	if !errors.As(err, &tooNew) || tooNew.Actual != 99 ||
		tooNew.Supported != MaxSchemaVersion {
		t.Fatalf("error = %#v", err)
	}
}

func TestCheckpointAndIdempotentClose(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Writer().Exec("CREATE TABLE checkpoint_test (id INTEGER); INSERT INTO checkpoint_test VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestClassifyMarksOnlyForeignKeyConstraints(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, foreignKey := db.Writer().Exec(`INSERT INTO sources(
		server_id, project_key, environment_key, application_key, service_key,
		project_label, environment_label, application_label, service_label,
		first_seen_at_us, last_seen_at_us
	) VALUES (999, 'p', 'e', 'a', 's', 'p', 'e', 'a', 's', 1, 1)`)
	classified := classify("insert source", foreignKey)
	var category *CategoryError
	if !errors.As(classified, &category) || category.Category != CategoryConstraint ||
		!IsForeignKeyViolation(classified) {
		t.Fatalf("foreign-key failure classified as %#v (raw %v)", classified, foreignKey)
	}
	_, check := db.Writer().Exec(`INSERT INTO servers(name, created_at_us) VALUES ('', 1)`)
	if check == nil {
		t.Fatal("empty server name was accepted")
	}
	classified = classify("insert server", check)
	if !errors.As(classified, &category) || category.Category != CategoryConstraint ||
		IsForeignKeyViolation(classified) {
		t.Fatalf("CHECK failure classified as %#v (raw %v)", classified, check)
	}
	if IsForeignKeyViolation(errors.New("plain")) || IsForeignKeyViolation(nil) {
		t.Fatal("unclassified error reported as a foreign-key violation")
	}
}

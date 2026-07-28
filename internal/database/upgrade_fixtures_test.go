package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

var upgradeFixtureDigests = map[string]string{
	"schema-1.db": "fa41df2de53d37597eedf87dba7e854539c0a61197f25f2d8f98d76e8a74d0a3",
	"schema-2.db": "3d12a740144c0c1a08dff3b8a629a9d79180f2c0a4ab2d5f5180e45f8dbce9d4",
	"schema-3.db": "feaef12f35a3a01256eebce642c6679960dc4219ddb1df9d1952143e0056320b",
	"schema-4.db": "5c9926b8c354e9622147acb0f10454e81db764068677776bd31fcddac92e4fe2",
}

var releasedMigrationDigests = map[string]string{
	"0001_initial_ingestion.sql": "0ab04b3124455d3a85506240e78ecebcb16d55efd582499aa91866ac6a931083",
	"0002_administrator.sql":     "462a28b4f485b3e3f2f1c1dc0865b679d5b320cdcf0f9e01b8f0cb81c995cc41",
	"0003_sessions.sql":          "ff06187d1d558e1a454487293d40733d4eb0ba445909fc42e48f51f9c8a0af31",
	"0004_security_audit.sql":    "713a2f0114794fadd13e14809a8d8c7682d180bc4c804dd1b517ecc83dbef6b2",
}

func TestReleasedMigrationsAndHistoricalFixturesAreImmutable(t *testing.T) {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(releasedMigrationDigests) != len(migrations) {
		t.Fatalf("pinned migration digests = %d, embedded migrations = %d",
			len(releasedMigrationDigests), len(migrations))
	}
	for _, migration := range migrations {
		want, ok := releasedMigrationDigests[migration.name]
		if !ok {
			t.Fatalf("migration %q has no pinned digest", migration.name)
		}
		assertFileDigest(t, filepath.Join("migrations", migration.name), want)
	}
	if len(upgradeFixtureDigests) != MaxSchemaVersion {
		t.Fatalf("pinned fixture digests = %d, current schema = %d",
			len(upgradeFixtureDigests), MaxSchemaVersion)
	}
	for version := 1; version <= MaxSchemaVersion; version++ {
		name := schemaFixtureName(version)
		want, ok := upgradeFixtureDigests[name]
		if !ok {
			t.Fatalf("schema version %d has no pinned fixture digest", version)
		}
		assertFileDigest(t, filepath.Join("testdata", "upgrades", name), want)
	}
	for name := range releasedMigrationDigests {
		// The ordered loop above validates every embedded migration. This
		// second check catches stale manifest entries after an explicit policy
		// change instead of silently ignoring them.
		found := false
		for _, migration := range migrations {
			if migration.name == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("pinned migration %q is not embedded", name)
		}
	}
}

func TestHistoricalFixtureFailedNextMigrationRollsBackOnlyFailedVersion(t *testing.T) {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	migrations = append(migrations, migration{
		version: MaxSchemaVersion + 1,
		name:    "0005_synthetic_failure.sql",
		sql: `CREATE TABLE migration_must_rollback(id INTEGER) STRICT;
			THIS IS NOT VALID SQL;`,
	})
	for version := 1; version <= MaxSchemaVersion; version++ {
		t.Run(schemaFixtureName(version), func(t *testing.T) {
			path := copyUpgradeFixture(t, version)
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
				t.Fatal(err)
			}
			if err := applyMigrations(
				context.Background(), db, migrations,
			); err == nil {
				t.Fatal("synthetic failed migration succeeded")
			}
			assertSchemaVersion(t, db, MaxSchemaVersion)
			var partial int
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema
				WHERE type='table' AND name='migration_must_rollback'`).
				Scan(&partial); err != nil {
				t.Fatal(err)
			}
			if partial != 0 {
				t.Fatal("failed migration left partial schema state")
			}
			assertRepresentativeFixtureRows(t, db, version)
			if err := IntegrityCheck(context.Background(), db); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProductionOpenRefusesNewerHistoricalFixtureWithoutDownMigration(t *testing.T) {
	path := copyUpgradeFixture(t, MaxSchemaVersion)
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_migrations(
		version,name,applied_at_us
	) VALUES(5,'0005_future.sql',1785196800000005)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(context.Background(), path)
	if db != nil {
		_ = db.Close()
		t.Fatal("newer schema returned a database")
	}
	var tooNew *SchemaTooNewError
	if !errors.As(err, &tooNew) ||
		tooNew.Actual != 5 || tooNew.Supported != MaxSchemaVersion {
		t.Fatalf("newer schema error = %#v", err)
	}
	raw, err = sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	assertSchemaVersion(t, raw, 5)
	assertRepresentativeFixtureRows(t, raw, MaxSchemaVersion)
}

func assertFileDigest(t *testing.T, path, want string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		t.Fatalf("%s digest = %s, want %s; released fixtures are immutable",
			path, got, want)
	}
}

func copyUpgradeFixture(t *testing.T, version int) string {
	t.Helper()
	sourcePath := filepath.Join(
		"testdata", "upgrades", schemaFixtureName(version),
	)
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destinationPath := filepath.Join(t.TempDir(), "siftail.db")
	destination, err := os.OpenFile(
		destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	return destinationPath
}

func schemaFixtureName(version int) string {
	return "schema-" + strconv.Itoa(version) + ".db"
}

func assertRepresentativeFixtureRows(t *testing.T, db *sql.DB, origin int) {
	t.Helper()
	var server, source, event, setting int
	if err := db.QueryRow("SELECT count(*) FROM servers WHERE id=11").Scan(&server); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM sources WHERE id=13").Scan(&source); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM log_events
		WHERE id=15 AND message_text LIKE 'synthetic migration fixture event%'`).
		Scan(&event); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM settings
		WHERE key='history_cursor_hmac_key'`).Scan(&setting); err != nil {
		t.Fatal(err)
	}
	if server != 1 || source != 1 || event != 1 || setting != 1 {
		t.Fatalf("schema-%d representative rows = server:%d source:%d event:%d setting:%d",
			origin, server, source, event, setting)
	}
}

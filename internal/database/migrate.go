package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// foreignKeysOffDirective, as the first line of a migration, runs that
// migration with foreign-key enforcement disabled outside its transaction.
// SQLite requires this to rebuild a table that other tables reference
// (https://sqlite.org/lang_altertable.html#otheralter): PRAGMA foreign_keys is
// a no-op inside a transaction, and DROP TABLE on a referenced parent fails
// even when constraints are deferred. The runner checks every foreign key
// before commit, so the migration cannot commit a dangling reference.
const foreignKeysOffDirective = "-- siftail:foreign-keys-off"

type migration struct {
	version        int
	name           string
	sql            string
	foreignKeysOff bool
}

// Migrate applies every embedded forward migration in one transaction per
// version. A failed version is rolled back and is not recorded.
func Migrate(ctx context.Context, db *sql.DB) error {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		return err
	}
	return applyMigrations(ctx, db, migrations)
}

func loadMigrations(files fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %q has no numeric prefix", entry.Name())
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version < 1 {
			return nil, fmt.Errorf("migration %q has invalid version", entry.Name())
		}
		body, err := fs.ReadFile(files, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		firstLine, _, _ := strings.Cut(string(body), "\n")
		migrations = append(migrations, migration{
			version: version, name: entry.Name(), sql: string(body),
			foreignKeysOff: strings.TrimSpace(firstLine) == foreignKeysOffDirective,
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	for i, migration := range migrations {
		if migration.version != i+1 {
			return nil, fmt.Errorf("migrations are not contiguous at version %d", i+1)
		}
	}
	return migrations, nil
}

func applyMigrations(ctx context.Context, db *sql.DB, migrations []migration) error {
	// Every step runs on one pinned connection so that a connection-scoped
	// pragma provably applies to the migration transaction that follows it.
	conn, err := db.Conn(ctx)
	if err != nil {
		return classify("acquire migration connection", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at_us INTEGER NOT NULL
	) STRICT`); err != nil {
		return classify("initialize migrations", err)
	}
	var current int
	if err := conn.QueryRowContext(ctx, "SELECT coalesce(max(version), 0) FROM schema_migrations").Scan(&current); err != nil {
		return classify("read migration version", err)
	}
	if current > len(migrations) {
		return &SchemaTooNewError{Actual: current, Supported: len(migrations)}
	}
	for _, migration := range migrations {
		if migration.version <= current {
			continue
		}
		if err := applyMigration(ctx, conn, migration); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, migration migration) (err error) {
	if migration.foreignKeysOff {
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return classify("disable migration foreign keys", err)
		}
		defer func() {
			if restoreErr := restoreForeignKeys(conn); restoreErr != nil {
				// Never return a writer connection without enforcement to
				// the pool: discard it, and fail the open.
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
				err = errors.Join(err, restoreErr)
			}
		}()
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return classify("begin migration", err)
	}
	if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
		_ = tx.Rollback()
		return classify("apply migration", err)
	}
	if migration.foreignKeysOff {
		if err := checkForeignKeys(ctx, tx); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, name, applied_at_us) VALUES (?, ?, CAST(unixepoch('subsec') * 1000000 AS INTEGER))",
		migration.version, migration.name); err != nil {
		_ = tx.Rollback()
		return classify("record migration", err)
	}
	if err := tx.Commit(); err != nil {
		return classify("commit migration", err)
	}
	return nil
}

func checkForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return classify("check migration foreign keys", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var index int
		if err := rows.Scan(&table, &rowID, &parent, &index); err != nil {
			return classify("read migration foreign-key violation", err)
		}
		return fmt.Errorf("migration left a foreign-key violation in %s referencing %s", table, parent)
	}
	if err := rows.Err(); err != nil {
		return classify("check migration foreign keys", err)
	}
	return nil
}

// restoreForeignKeys uses a fresh context: enforcement must be restored even
// when the migration context was canceled.
func restoreForeignKeys(conn *sql.Conn) error {
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return classify("restore migration foreign keys", err)
	}
	var enabled int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
		return classify("verify migration foreign keys", err)
	}
	if enabled != 1 {
		return errors.New("migration could not restore foreign-key enforcement")
	}
	return nil
}

// Package database owns Siftail's SQLite connection lifecycle, durability
// settings, migrations, integrity checks, and checkpoints.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mattn/go-sqlite3"
)

const (
	MaxSchemaVersion = 0
	readConnections  = 4
	writerDriverName = "siftail-sqlite-writer"
	readerDriverName = "siftail-sqlite-reader"
)

func init() {
	sql.Register(writerDriverName, configuredDriver(false))
	sql.Register(readerDriverName, configuredDriver(true))
}

func configuredDriver(readOnly bool) *sqlite3.SQLiteDriver {
	return &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		pragmas := []string{
			"PRAGMA journal_mode = WAL",
			"PRAGMA synchronous = FULL",
			"PRAGMA foreign_keys = ON",
			"PRAGMA busy_timeout = 5000",
			"PRAGMA temp_store = MEMORY",
		}
		if readOnly {
			pragmas = append(pragmas, "PRAGMA query_only = ON")
		}
		for _, pragma := range pragmas {
			if _, err := conn.Exec(pragma, nil); err != nil {
				return err
			}
		}
		return nil
	}}
}

// DB owns the single writer connection and the bounded ordinary read pool.
type DB struct {
	writer *sql.DB
	reader *sql.DB
	once   sync.Once
	err    error
}

// Open opens and verifies a SQLite database without applying application schema
// migrations. Existing files are never removed or recreated after an error.
func Open(ctx context.Context, path string) (*DB, error) {
	if path == "" {
		return nil, fmt.Errorf("database path is empty")
	}
	if err := requireWritableParent(path); err != nil {
		return nil, err
	}

	writer, err := sql.Open(writerDriverName, dsn(path, false))
	if err != nil {
		return nil, classify("open writer", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	if err := writer.PingContext(ctx); err != nil {
		_ = writer.Close()
		return nil, classify("connect writer", err)
	}

	db := &DB{writer: writer}
	fail := func(err error) (*DB, error) {
		_ = db.Close()
		return nil, err
	}
	if err := initializeVacuum(ctx, writer); err != nil {
		return fail(err)
	}
	if err := verifyPragmas(ctx, writer); err != nil {
		return fail(err)
	}
	if err := checkSchemaCompatible(ctx, writer, MaxSchemaVersion); err != nil {
		return fail(err)
	}
	if err := QuickCheck(ctx, writer); err != nil {
		return fail(err)
	}
	if err := verifyWritable(ctx, writer); err != nil {
		return fail(err)
	}

	reader, err := sql.Open(readerDriverName, dsn(path, true))
	if err != nil {
		return fail(classify("open reader", err))
	}
	reader.SetMaxOpenConns(readConnections)
	reader.SetMaxIdleConns(readConnections)
	if err := reader.PingContext(ctx); err != nil {
		_ = reader.Close()
		return fail(classify("connect reader", err))
	}
	db.reader = reader
	return db, nil
}

func dsn(path string, readOnly bool) string {
	mode := "rwc"
	if readOnly {
		mode = "ro"
	}
	query := url.Values{
		"mode":          {mode},
		"_journal_mode": {"WAL"},
		"_synchronous":  {"FULL"},
		"_foreign_keys": {"on"},
		"_busy_timeout": {"5000"},
	}
	return "file:" + filepath.ToSlash(path) + "?" + query.Encode()
}

func requireWritableParent(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("database directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("database directory is not a directory")
	}
	probe, err := os.CreateTemp(parent, ".siftail-database-check-*")
	if err != nil {
		return fmt.Errorf("database directory is not writable: %w", err)
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("database write check: %w", err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("database write check cleanup: %w", err)
	}
	return nil
}

func initializeVacuum(ctx context.Context, db *sql.DB) error {
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
		return classify("inspect fresh database", err)
	}
	if count == 0 {
		if _, err := db.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
			return classify("configure incremental auto-vacuum", err)
		}
		var mode int
		if err := db.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&mode); err != nil {
			return classify("verify incremental auto-vacuum", err)
		}
		if mode != 2 {
			if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
				return classify("initialize incremental auto-vacuum", err)
			}
		}
	}
	return nil
}

func verifyPragmas(ctx context.Context, db *sql.DB) error {
	checks := []struct {
		query string
		want  string
	}{
		{"PRAGMA journal_mode", "wal"},
		{"PRAGMA synchronous", "2"},
		{"PRAGMA foreign_keys", "1"},
		{"PRAGMA busy_timeout", "5000"},
		{"PRAGMA temp_store", "2"},
		{"PRAGMA auto_vacuum", "2"},
	}
	for _, check := range checks {
		var got string
		if err := db.QueryRowContext(ctx, check.query).Scan(&got); err != nil {
			return classify("verify SQLite settings", err)
		}
		if !strings.EqualFold(got, check.want) {
			return fmt.Errorf("database configuration mismatch for %s", check.query)
		}
	}
	return nil
}

func verifyWritable(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return classify("begin database write check", err)
	}
	if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS __siftail_write_check (id INTEGER)"); err != nil {
		_ = tx.Rollback()
		return classify("database write check", err)
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE __siftail_write_check"); err != nil {
		_ = tx.Rollback()
		return classify("database write check cleanup", err)
	}
	if err := tx.Commit(); err != nil {
		return classify("commit database write check", err)
	}
	return nil
}

func checkSchemaCompatible(ctx context.Context, db *sql.DB, supported int) error {
	var exists int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='schema_migrations'").Scan(&exists); err != nil {
		return classify("inspect schema", err)
	}
	if exists == 0 {
		return nil
	}
	var actual sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT max(version) FROM schema_migrations").Scan(&actual); err != nil {
		return classify("read schema version", err)
	}
	if actual.Valid && actual.Int64 > int64(supported) {
		return &SchemaTooNewError{Actual: int(actual.Int64), Supported: supported}
	}
	return nil
}

// Writer returns the application-owned writer pool, capped at one connection.
func (d *DB) Writer() *sql.DB { return d.writer }

// Reader returns the read-only pool, capped at four connections.
func (d *DB) Reader() *sql.DB { return d.reader }

// Checkpoint requests a passive WAL checkpoint.
func (d *DB) Checkpoint(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	if err := d.writer.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return classify("passive WAL checkpoint", err)
	}
	if busy != 0 {
		return &CategoryError{Category: CategoryBusy, Operation: "passive WAL checkpoint"}
	}
	return nil
}

// QuickCheck runs SQLite's bounded startup integrity check.
func QuickCheck(ctx context.Context, db *sql.DB) error {
	return integrityCheck(ctx, db, "PRAGMA quick_check")
}

// IntegrityCheck runs SQLite's full integrity check.
func IntegrityCheck(ctx context.Context, db *sql.DB) error {
	return integrityCheck(ctx, db, "PRAGMA integrity_check")
}

func integrityCheck(ctx context.Context, db *sql.DB, query string) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return classify("database integrity check", err)
	}
	defer rows.Close()
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return classify("database integrity check", err)
		}
		if result != "ok" {
			return &CategoryError{Category: CategoryCorrupt, Operation: "database integrity check"}
		}
	}
	if err := rows.Err(); err != nil {
		return classify("database integrity check", err)
	}
	return nil
}

// Close performs a passive checkpoint and closes both pools exactly once.
func (d *DB) Close() error {
	d.once.Do(func() {
		var errs []error
		if d.writer != nil {
			if err := d.Checkpoint(context.Background()); err != nil && !errors.Is(err, sql.ErrConnDone) {
				errs = append(errs, err)
			}
		}
		if d.reader != nil {
			errs = append(errs, d.reader.Close())
		}
		if d.writer != nil {
			errs = append(errs, d.writer.Close())
		}
		d.err = errors.Join(errs...)
	})
	return d.err
}

type Category string

const (
	CategoryBusy       Category = "busy"
	CategoryCorrupt    Category = "corrupt"
	CategoryFull       Category = "full"
	CategoryIO         Category = "io"
	CategoryConstraint Category = "constraint"
	CategoryInternal   Category = "internal"
)

type CategoryError struct {
	Category  Category
	Operation string
}

func (e *CategoryError) Error() string {
	return fmt.Sprintf("%s: database %s", e.Operation, e.Category)
}

type SchemaTooNewError struct {
	Actual    int
	Supported int
}

func (e *SchemaTooNewError) Error() string {
	return fmt.Sprintf("database schema version %d is newer than supported version %d", e.Actual, e.Supported)
}

func classify(operation string, err error) error {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) {
		category := CategoryInternal
		switch sqliteErr.Code {
		case sqlite3.ErrBusy, sqlite3.ErrLocked:
			category = CategoryBusy
		case sqlite3.ErrCorrupt, sqlite3.ErrNotADB:
			category = CategoryCorrupt
		case sqlite3.ErrFull:
			category = CategoryFull
		case sqlite3.ErrIoErr, sqlite3.ErrCantOpen, sqlite3.ErrReadonly:
			category = CategoryIO
		case sqlite3.ErrConstraint:
			category = CategoryConstraint
		}
		return &CategoryError{Category: category, Operation: operation}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

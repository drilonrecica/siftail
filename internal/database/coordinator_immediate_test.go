package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func settingKeys(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query("SELECT key FROM settings WHERE key LIKE 'immediate-%' ORDER BY key")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(keys, ",")
}

func insertSetting(tx Statements, key string) error {
	_, err := tx.ExecContext(context.Background(),
		"INSERT INTO settings(key,value_json,updated_at_us) VALUES(?,'{}',1)", key)
	return err
}

func TestDoImmediateCommitsAndHonoursSavepoints(t *testing.T) {
	db := openTestDB(t)
	coordinator, stop := startRecoveryCoordinator(t, db)
	defer stop()
	err := coordinator.DoImmediate(context.Background(), func(tx Statements) error {
		ctx := context.Background()
		if err := insertSetting(tx, "immediate-a"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "SAVEPOINT member"); err != nil {
			return err
		}
		if err := insertSetting(tx, "immediate-b"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO member"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "RELEASE member"); err != nil {
			return err
		}
		return insertSetting(tx, "immediate-c")
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := settingKeys(t, db.Reader()); got != "immediate-a,immediate-c" {
		t.Fatalf("committed keys = %q", got)
	}
}

func TestDoImmediateTakesTheWriteLockBeforeTheFirstWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	coordinator, stop := startRecoveryCoordinator(t, db)
	defer stop()
	other, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec("PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}
	var competing error
	if err := coordinator.DoImmediate(context.Background(), func(Statements) error {
		// No statement has run yet: only BEGIN IMMEDIATE holds the lock.
		_, competing = other.Exec("INSERT INTO settings(key,value_json,updated_at_us) VALUES('immediate-other','{}',1)")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if competing == nil || !strings.Contains(competing.Error(), "locked") {
		t.Fatalf("competing writer during immediate transaction: %v", competing)
	}
}

func TestDoImmediateFailureRollsBackAndLeavesTheWriterUsable(t *testing.T) {
	db := openTestDB(t)
	coordinator, stop := startRecoveryCoordinator(t, db)
	defer stop()
	poison := errors.New("poison")
	for name, run := range map[string]func(Statements) error{
		"error after a write": func(tx Statements) error {
			if err := insertSetting(tx, "immediate-discarded"); err != nil {
				return err
			}
			return poison
		},
		// SQLite may roll a transaction back itself (I/O, full, busy); the
		// coordinator's own ROLLBACK then finds nothing to roll back.
		"transaction already rolled back": func(tx Statements) error {
			if err := insertSetting(tx, "immediate-discarded"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				return err
			}
			return poison
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := coordinator.DoImmediate(context.Background(), run); !errors.Is(err, poison) {
				t.Fatalf("DoImmediate error = %v", err)
			}
			if err := coordinator.Do(context.Background(), func(tx *sql.Tx) error {
				return insertSetting(tx, "immediate-next-"+strings.ReplaceAll(name, " ", "-"))
			}); err != nil {
				t.Fatalf("next mutation: %v", err)
			}
		})
	}
	if got := settingKeys(t, db.Reader()); strings.Contains(got, "immediate-discarded") ||
		strings.Count(got, "immediate-next") != 2 {
		t.Fatalf("committed keys = %q", got)
	}
}

func TestDoImmediateRejectsNilOperation(t *testing.T) {
	db := openTestDB(t)
	coordinator, stop := startRecoveryCoordinator(t, db)
	defer stop()
	if err := coordinator.DoImmediate(context.Background(), nil); err == nil {
		t.Fatal("nil operation accepted")
	}
}

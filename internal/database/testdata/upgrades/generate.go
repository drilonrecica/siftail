//go:build ignore

// Command generate creates the immutable synthetic SQLite upgrade fixtures.
// Existing fixtures are never replaced unless -overwrite is supplied.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/argon2"
)

const (
	currentSchema = 4
	fixtureTimeUS = int64(1785196800000000)
)

var migrationNames = []string{
	"0001_initial_ingestion.sql",
	"0002_administrator.sql",
	"0003_sessions.sql",
	"0004_security_audit.sql",
}

func main() {
	overwrite := flag.Bool("overwrite", false, "replace existing immutable fixtures")
	version := flag.Int("version", currentSchema, "single schema version to generate")
	all := flag.Bool("all", false, "generate every schema version")
	output := flag.String(
		"output", "internal/database/testdata/upgrades",
		"fixture output directory",
	)
	flag.Parse()
	from, to := *version, *version
	if *all {
		from, to = 1, currentSchema
	}
	if from < 1 || to > currentSchema {
		fmt.Fprintln(os.Stderr, "fixture version is outside the supported schema range")
		os.Exit(2)
	}
	for fixtureVersion := from; fixtureVersion <= to; fixtureVersion++ {
		if err := createFixture(*output, fixtureVersion, *overwrite); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func createFixture(output string, version int, overwrite bool) error {
	finalPath := filepath.Join(output, fmt.Sprintf("schema-%d.db", version))
	if !overwrite {
		if _, err := os.Lstat(finalPath); err == nil {
			return fmt.Errorf("%s exists; use -overwrite for an intentional refresh", finalPath)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect %s: %w", finalPath, err)
		}
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		return fmt.Errorf("create fixture directory: %w", err)
	}
	temporary, err := os.CreateTemp(output, ".schema-fixture-*.db")
	if err != nil {
		return fmt.Errorf("create schema-%d staging fixture: %w", version, err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close schema-%d staging fixture: %w", version, err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	db, err := sql.Open("sqlite3", temporaryPath)
	if err != nil {
		return fmt.Errorf("open schema-%d fixture: %w", version, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode=DELETE;
		PRAGMA synchronous=FULL;
		PRAGMA foreign_keys=ON;
		PRAGMA auto_vacuum=INCREMENTAL;
		VACUUM;
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at_us INTEGER NOT NULL
		) STRICT;
	`); err != nil {
		_ = db.Close()
		return fmt.Errorf("initialize schema-%d fixture: %w", version, err)
	}
	for index := 0; index < version; index++ {
		name := migrationNames[index]
		body, err := os.ReadFile(filepath.Join(
			"internal", "database", "migrations", name,
		))
		if err != nil {
			_ = db.Close()
			return fmt.Errorf("read %s: %w", name, err)
		}
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			_ = db.Close()
			return fmt.Errorf("begin %s: %w", name, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations(version,name,applied_at_us)
			VALUES(?,?,?)`,
			index+1, name, fixtureTimeUS+int64(index),
		); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			_ = db.Close()
			return fmt.Errorf("commit %s: %w", name, err)
		}
	}
	if err := seedFixture(db, version); err != nil {
		_ = db.Close()
		return fmt.Errorf("seed schema-%d fixture: %w", version, err)
	}
	if _, err := db.Exec("VACUUM"); err != nil {
		_ = db.Close()
		return fmt.Errorf("compact schema-%d fixture: %w", version, err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close schema-%d fixture: %w", version, err)
	}
	if err := os.Chmod(temporaryPath, 0644); err != nil {
		return fmt.Errorf("set schema-%d fixture permissions: %w", version, err)
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return fmt.Errorf("publish schema-%d fixture: %w", version, err)
	}
	removeTemporary = false
	return nil
}

func seedFixture(db *sql.DB, version int) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cursorKey := base64.RawURLEncoding.EncodeToString(
		[]byte("0123456789abcdef0123456789abcdef"),
	)
	cursorJSON, err := json.Marshal(cursorKey)
	if err != nil {
		return err
	}
	token := "synthetic-fixture-ingestion-token"
	tokenHash := sha256.Sum256([]byte(token))
	if _, err := tx.Exec(`
		INSERT INTO settings(key,value_json,updated_at_us) VALUES
			('retention_days','14',?),
			('maximum_database_gib','4',?),
			('history_cursor_hmac_key',?,?);
		INSERT INTO servers(id,name,hostname,created_at_us)
			VALUES(11,'Synthetic fixture server','fixture.invalid',?);
		INSERT INTO ingestion_tokens(
			id,server_id,name,token_hash,fingerprint,created_at_us,last_used_at_us
		) VALUES(12,11,'Synthetic fixture token',?,?,?,?);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			alias,first_seen_at_us,last_seen_at_us
		) VALUES(
			13,11,'synthetic-project','fixture','synthetic-app','web',
			'Synthetic project','Fixture','Synthetic app','Web',
			'Migration fixture source',?,?
		);
		INSERT INTO container_instances(
			id,source_id,container_id,container_name,first_seen_at_us,last_seen_at_us
		) VALUES(14,13,'synthetic-container-id','synthetic-container',?,?);
		INSERT INTO log_events(
			id,event_at_us,received_at_us,source_id,container_instance_id,
			stream,level_normalized,level_original,message_raw,message_text,
			attributes_json,source_event_id,logger,request_id,error_type,
			http_method,http_path,http_status,duration_ms
		) VALUES(
			15,?,?,13,14,'stderr','warn','WARNING',?,?,
			?,'synthetic-event-1','fixture.logger','fixture-request-1',
			'SyntheticError','POST','/synthetic/fixture',429,12.5
		)`,
		fixtureTimeUS, fixtureTimeUS,
		string(cursorJSON), fixtureTimeUS,
		fixtureTimeUS,
		tokenHash[:], hex.EncodeToString(tokenHash[:])[:12],
		fixtureTimeUS, fixtureTimeUS+1,
		fixtureTimeUS, fixtureTimeUS+2,
		fixtureTimeUS, fixtureTimeUS+2,
		fixtureTimeUS+1, fixtureTimeUS+2,
		[]byte("synthetic migration fixture event\nsecond line"),
		"synthetic migration fixture event\nsecond line",
		fmt.Sprintf(`{"fixture":true,"schema_origin":%d}`, version),
	); err != nil {
		return err
	}
	if version >= 2 {
		salt := []byte("siftail-fixture!")
		key := argon2.IDKey(
			[]byte("synthetic-fixture-password"), salt, 1, 8*1024, 1, 32,
		)
		passwordHash := fmt.Sprintf(
			"$argon2id$v=19$m=8192,t=1,p=1$%s$%s",
			base64.RawStdEncoding.EncodeToString(salt),
			base64.RawStdEncoding.EncodeToString(key),
		)
		if _, err := tx.Exec(`INSERT INTO administrators(
			id,username,password_hash,created_at_us,password_changed_at_us
		) VALUES(1,'FixtureAdmin',?,?,?)`,
			passwordHash, fixtureTimeUS, fixtureTimeUS,
		); err != nil {
			return err
		}
	}
	if version >= 3 {
		sessionHash := sha256.Sum256([]byte("synthetic-fixture-session-token"))
		if _, err := tx.Exec(`INSERT INTO sessions(
			id,administrator_id,token_hash,created_at_us,last_used_at_us,
			expires_at_us,user_agent_summary,client_identity_summary
		) VALUES(16,1,?,?,?,?,?,?)`,
			sessionHash[:], fixtureTimeUS, fixtureTimeUS+1,
			fixtureTimeUS+int64(14*24*time.Hour/time.Microsecond),
			"Synthetic fixture browser", "192.0.2.10",
		); err != nil {
			return err
		}
	}
	if version >= 4 {
		if _, err := tx.Exec(`INSERT INTO security_audit_events(
			id,occurred_at_us,category,action,outcome,actor_type,
			administrator_id,server_id,source_id,safe_metadata_json,request_id
		) VALUES(
			17,?,'authentication','fixture.upgrade','succeeded','system',
			NULL,11,13,'{"setting_name":"synthetic_fixture"}','fixture-audit-request'
		)`, fixtureTimeUS+3); err != nil {
			return err
		}
	}
	return tx.Commit()
}

package backup

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/database"
)

func TestCreateConfigurationBackupCopiesOnlyConsistentConfiguration(t *testing.T) {
	fixture := newBackupFixture(t)
	seedFullBackupState(t, fixture.db.Writer())
	if _, err := fixture.db.Writer().Exec(`
		UPDATE servers SET hostname=? WHERE id=1;
		UPDATE ingestion_tokens SET name=?, revoked_at_us=3 WHERE id=1;
		UPDATE sources SET alias=? WHERE id=1;
		INSERT INTO settings(key,value_json,updated_at_us)
		VALUES('boundary_setting',?,2);
		INSERT INTO container_instances(
			id,source_id,container_id,container_name,first_seen_at_us,last_seen_at_us
		) VALUES(1,1,'private-container-id','private-container-name',1,1)
	`, strings.Repeat("h", 255), strings.Repeat("t", 128),
		strings.Repeat("a", 128),
		`{"value":"`+strings.Repeat("s", 1024)+`"}`,
	); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "configuration.sqlite")
	writeDone := make(chan error, 1)
	var startWrite sync.Once
	result, err := fixture.service.CreateConfiguration(
		context.Background(), output, func(Progress) {
			startWrite.Do(func() {
				go func() {
					writeDone <- fixture.coordinator.Do(
						context.Background(), func(tx *sql.Tx) error {
							_, err := tx.Exec(`INSERT INTO settings(
								key,value_json,updated_at_us
							) VALUES('after_snapshot','{"new":true}',4)`)
							return err
						},
					)
				}()
			})
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("concurrent configuration write: %v", err)
	}
	if result.Type != TypeConfiguration || result.Validate() != nil {
		t.Fatalf("result = %#v", result)
	}
	verified, err := Verify(context.Background(), output)
	if err != nil || verified.Type != TypeConfiguration {
		t.Fatalf("verified = %#v, %v", verified, err)
	}

	artifact, err := sql.Open("sqlite3", sqlitePath(output, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Close()
	for query, want := range map[string]int{
		"SELECT count(*) FROM administrators":          1,
		"SELECT count(*) FROM servers":                 1,
		"SELECT count(*) FROM ingestion_tokens":        1,
		"SELECT count(*) FROM sources":                 1,
		"SELECT count(*) FROM settings":                1,
		"SELECT count(*) FROM log_events":              0,
		"SELECT count(*) FROM container_instances":     0,
		"SELECT count(*) FROM security_audit_events":   0,
		"SELECT count(*) FROM sessions":                0,
		"SELECT count(*) FROM siftail_backup_metadata": 1,
	} {
		var got int
		if err := artifact.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}
	var alias, tokenName, settingJSON, passwordHash string
	var tokenHash []byte
	if err := artifact.QueryRow(
		"SELECT alias FROM sources WHERE id=1",
	).Scan(&alias); err != nil {
		t.Fatal(err)
	}
	if err := artifact.QueryRow(
		"SELECT name,token_hash FROM ingestion_tokens WHERE id=1",
	).Scan(&tokenName, &tokenHash); err != nil {
		t.Fatal(err)
	}
	if err := artifact.QueryRow(
		"SELECT value_json FROM settings WHERE key='boundary_setting'",
	).Scan(&settingJSON); err != nil {
		t.Fatal(err)
	}
	if err := artifact.QueryRow(
		"SELECT password_hash FROM administrators WHERE id=1",
	).Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if alias != strings.Repeat("a", 128) ||
		tokenName != strings.Repeat("t", 128) || len(tokenHash) != 32 ||
		!strings.Contains(settingJSON, strings.Repeat("s", 1024)) ||
		len(passwordHash) != 64 {
		t.Fatalf("configuration boundary values changed")
	}
	var afterSnapshot int
	if err := artifact.QueryRow(
		"SELECT count(*) FROM settings WHERE key='after_snapshot'",
	).Scan(&afterSnapshot); err != nil || afterSnapshot != 0 {
		t.Fatalf("post-snapshot setting = %d, %v", afterSnapshot, err)
	}

	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, excluded := range []string{
		"retained-", "private-session-marker",
		"private-container-id", "private-container-name",
	} {
		if bytes.Contains(raw, []byte(excluded)) {
			t.Fatalf("configuration artifact retained %q", excluded)
		}
	}
	restoredPath := filepath.Join(t.TempDir(), "restored.sqlite")
	restoredFile, err := os.OpenFile(
		restoredPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := restoredFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.OnlineBackup(
		context.Background(), artifact, restoredPath, 8, nil,
	); err != nil {
		t.Fatalf("round-trip configuration artifact: %v", err)
	}
	restored, err := sql.Open("sqlite3", sqlitePath(restoredPath, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for query, want := range map[string]int{
		"SELECT count(*) FROM administrators":        1,
		"SELECT count(*) FROM servers":               1,
		"SELECT count(*) FROM ingestion_tokens":      1,
		"SELECT count(*) FROM sources":               1,
		"SELECT count(*) FROM settings":              1,
		"SELECT count(*) FROM log_events":            0,
		"SELECT count(*) FROM container_instances":   0,
		"SELECT count(*) FROM security_audit_events": 0,
		"SELECT count(*) FROM sessions":              0,
	} {
		var got int
		if err := restored.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("restored %s: %v", query, err)
		}
		if got != want {
			t.Fatalf("restored %s = %d, want %d", query, got, want)
		}
	}
	var restoredIntegrity string
	if err := restored.QueryRow("PRAGMA integrity_check").Scan(
		&restoredIntegrity,
	); err != nil || restoredIntegrity != "ok" {
		t.Fatalf("restored integrity = %q, %v", restoredIntegrity, err)
	}
	assertNoPartialFiles(t, filepath.Dir(output))

	page, err := fixture.audit.List(context.Background(), audit.Query{
		Action: "backup.configuration", Limit: 10,
	})
	if err != nil || len(page.Events) != 1 ||
		page.Events[0].Outcome != audit.OutcomeSucceeded ||
		page.Events[0].Metadata[audit.MetadataBackupType] !=
			TypeConfiguration {
		t.Fatalf("configuration audit = %#v, %v", page.Events, err)
	}
}

func TestConfigurationBackupIsDeterministicForUnchangedConfiguration(t *testing.T) {
	fixture := newBackupFixture(t)
	seedFullBackupState(t, fixture.db.Writer())
	fixture.service.now = func() time.Time {
		return time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	}
	firstPath := filepath.Join(t.TempDir(), "first.sqlite")
	first, err := fixture.service.CreateConfiguration(
		context.Background(), firstPath, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(t.TempDir(), "second.sqlite")
	second, err := fixture.service.CreateConfiguration(
		context.Background(), secondPath, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.Bytes != second.Bytes || !first.CreatedAt.Equal(second.CreatedAt) ||
		configurationSummary(t, firstPath) !=
			configurationSummary(t, secondPath) {
		t.Fatalf("nondeterministic artifacts: %#v %#v", first, second)
	}
}

func configurationSummary(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite3", sqlitePath(path, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var summary string
	if err := db.QueryRow(`SELECT
		(SELECT group_concat(
			id || ':' || username || ':' || password_hash, '|'
		) FROM administrators) || ';' ||
		(SELECT group_concat(
			id || ':' || name || ':' || coalesce(hostname,''), '|'
		) FROM servers) || ';' ||
		(SELECT group_concat(
			id || ':' || server_id || ':' || name || ':' || hex(token_hash) ||
			':' || fingerprint || ':' || coalesce(revoked_at_us,''), '|'
		) FROM ingestion_tokens) || ';' ||
		coalesce((SELECT group_concat(
			key || ':' || value_json || ':' || updated_at_us, '|'
		) FROM settings),'') || ';' ||
		(SELECT group_concat(
			id || ':' || server_id || ':' || project_key || ':' ||
			environment_key || ':' || application_key || ':' || service_key ||
			':' || coalesce(alias,''), '|'
		) FROM sources)`).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	return summary
}

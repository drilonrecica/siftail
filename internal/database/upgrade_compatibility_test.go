package database_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/drilonrecica/siftail/internal/auth"
	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/retention"
)

func TestEveryHistoricalSchemaFixtureUpgradesThroughProductionCriticalPaths(
	t *testing.T,
) {
	for origin := 1; origin <= database.MaxSchemaVersion; origin++ {
		t.Run("schema-"+strconv.Itoa(origin), func(t *testing.T) {
			path := copyHistoricalFixture(t, origin)
			db, err := database.Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			assertCurrentMigrationHistory(t, db.Reader())
			if err := database.IntegrityCheck(
				context.Background(), db.Writer(),
			); err != nil {
				t.Fatal(err)
			}
			assertPreservedHistoricalState(t, db.Reader(), db.Writer(), origin)
			exerciseProductionCriticalPaths(t, db, origin)
		})
	}
}

func TestFreshDatabaseUsesSameProductionCriticalPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertCurrentMigrationHistory(t, db.Reader())
	exerciseProductionCriticalPaths(t, db, 0)
}

func assertCurrentMigrationHistory(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`SELECT version,name FROM schema_migrations
		ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []string{
		"0001_initial_ingestion.sql",
		"0002_administrator.sql",
		"0003_sessions.sql",
		"0004_security_audit.sql",
	}
	var got []string
	for rows.Next() {
		var version int
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			t.Fatal(err)
		}
		if version != len(got)+1 {
			t.Fatalf("migration version = %d after %#v", version, got)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("migration names = %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("migration %d name = %q, want %q",
				index+1, got[index], want[index])
		}
	}
}

func assertPreservedHistoricalState(
	t *testing.T,
	db *sql.DB,
	writer *sql.DB,
	origin int,
) {
	t.Helper()
	var serverName, message, attributes string
	if err := db.QueryRow("SELECT name FROM servers WHERE id=11").
		Scan(&serverName); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT message_text,attributes_json
		FROM log_events WHERE id=15`).Scan(&message, &attributes); err != nil {
		t.Fatal(err)
	}
	if serverName != "Synthetic fixture server" ||
		message != "synthetic migration fixture event\nsecond line" ||
		attributes != `{"fixture":true,"schema_origin":`+
			strconv.Itoa(origin)+`}` {
		t.Fatalf("schema-%d preserved state = %q %q %q",
			origin, serverName, message, attributes)
	}
	if _, err := writer.Exec(
		"UPDATE log_events SET message_text='changed' WHERE id=15",
	); err == nil {
		t.Fatal("migrated event lost immutability")
	}

	var administrators, sessions, audits int
	if err := db.QueryRow("SELECT count(*) FROM administrators").
		Scan(&administrators); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM security_audit_events").
		Scan(&audits); err != nil {
		t.Fatal(err)
	}
	wantAdministrator, wantSession, wantAudit := 0, 0, 0
	if origin >= 2 {
		wantAdministrator = 1
	}
	if origin >= 3 {
		wantSession = 1
	}
	if origin >= 4 {
		wantAudit = 1
	}
	if administrators != wantAdministrator || sessions != wantSession ||
		audits != wantAudit {
		t.Fatalf("schema-%d optional rows = administrators:%d sessions:%d audits:%d",
			origin, administrators, sessions, audits)
	}
	if origin >= 2 {
		administrator, matches, err := auth.NewStore(db).Verify(
			context.Background(), "FixtureAdmin",
			[]byte("synthetic-fixture-password"),
		)
		if err != nil || !matches || administrator.ID != 1 {
			t.Fatalf("schema-%d administrator verification = %#v %t %v",
				origin, administrator, matches, err)
		}
	}
}

func exerciseProductionCriticalPaths(
	t *testing.T,
	db *database.DB,
	origin int,
) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := database.NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	defer func() {
		coordinator.Close()
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}()

	if origin == 0 {
		tokenHash := sha256.Sum256([]byte("synthetic-fixture-ingestion-token"))
		if err := coordinator.Do(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO servers(
				id,name,hostname,created_at_us
			) VALUES(11,'Synthetic fixture server','fixture.invalid',1785196800000000);
			INSERT INTO ingestion_tokens(
				id,server_id,name,token_hash,fingerprint,created_at_us
			) VALUES(12,11,'Synthetic fixture token',?,?,1785196800000000)`,
				tokenHash[:], hex.EncodeToString(tokenHash[:])[:12],
			)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}

	history := logs.NewStore(db.Reader())
	before, err := history.Recent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if origin > 0 && (len(before) != 1 || before[0].ID != 15) {
		t.Fatalf("schema-%d initial History = %#v", origin, before)
	}
	if origin == 0 && len(before) != 0 {
		t.Fatalf("fresh initial History = %#v", before)
	}

	eventAt := int64(1785196800000100)
	batch := &ingest.WriteBatch{
		AuthenticatedServerID: 11,
		AuthenticatedTokenID:  12,
		Events: []logs.CanonicalEvent{{
			EventAtUS: eventAt, ReceivedAtUS: eventAt + 1,
			Source: logs.SourceIdentity{
				ServerID: 11, Project: "synthetic-project",
				Environment: "fixture", Application: "after-upgrade",
				Service: "writer", ProjectLabel: "Synthetic project",
				EnvLabel: "Fixture", AppLabel: "After upgrade",
				ServiceLabel: "Writer",
			},
			Stream: logs.StreamStdout, Level: logs.LevelInfo,
			MessageRaw:    []byte("synthetic post-upgrade event"),
			MessageText:   "synthetic post-upgrade event",
			Attributes:    []byte(`{"fixture":true}`),
			SourceEventID: "synthetic-post-upgrade-" + strconv.Itoa(origin),
		}},
	}
	if err := ingest.NewBatchWriter(coordinator, nil).Persist(
		context.Background(), batch,
	); err != nil {
		t.Fatal(err)
	}
	after, err := history.Recent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 ||
		after[0].MessageText != "synthetic post-upgrade event" {
		t.Fatalf("schema-%d post-upgrade History = %#v", origin, after)
	}

	settings, err := retention.NewStore(db.Reader(), coordinator).Save(
		context.Background(), retention.Input{
			AgeDays: 30, MaxDatabaseGiB: 8,
		},
	)
	if err != nil || settings.AgeDays != 30 ||
		settings.MaxDatabaseGiB() != 8 {
		t.Fatalf("schema-%d retention write = %#v %v", origin, settings, err)
	}
	var auditCount int
	if err := db.Reader().QueryRow(`SELECT count(*) FROM security_audit_events
		WHERE action='retention.update' AND outcome='succeeded'`).
		Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("schema-%d critical audit write = %d %v",
			origin, auditCount, err)
	}
	if err := database.IntegrityCheck(
		context.Background(), db.Writer(),
	); err != nil {
		t.Fatal(err)
	}
}

func copyHistoricalFixture(t *testing.T, version int) string {
	t.Helper()
	source, err := os.Open(filepath.Join(
		"testdata", "upgrades", "schema-"+strconv.Itoa(version)+".db",
	))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	path := filepath.Join(t.TempDir(), "siftail.db")
	destination, err := os.OpenFile(
		path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600,
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
	return path
}

package backup

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/logs"
)

func TestEveryUpgradeFixturePassesFullAndConfigurationBackupRestore(t *testing.T) {
	for origin := 1; origin <= database.MaxSchemaVersion; origin++ {
		for _, backupType := range []string{TypeFull, TypeConfiguration} {
			t.Run("schema-"+strconv.Itoa(origin)+"/"+backupType, func(t *testing.T) {
				testUpgradeFixtureBackupRestore(t, origin, backupType)
			})
		}
	}
}

func testUpgradeFixtureBackupRestore(
	t *testing.T,
	origin int,
	backupType string,
) {
	dataDirectory := t.TempDir()
	databasePath := filepath.Join(dataDirectory, "siftail.db")
	copyBackupUpgradeFixture(t, origin, databasePath)
	db, err := database.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	coordinatorContext, cancelCoordinator := context.WithCancel(
		context.Background(),
	)
	coordinator := database.NewCoordinator(db.Writer())
	coordinatorDone := make(chan error, 1)
	go func() { coordinatorDone <- coordinator.Run(coordinatorContext) }()
	<-coordinator.Ready()
	auditStore := audit.NewStore(db.Reader(), coordinator)
	service := NewService(db, databasePath, auditStore)
	artifactPath := filepath.Join(
		dataDirectory, "schema-"+strconv.Itoa(origin)+"-"+backupType+".siftail",
	)
	var artifact Result
	switch backupType {
	case TypeFull:
		artifact, err = service.CreateFull(
			context.Background(), artifactPath, nil,
		)
	case TypeConfiguration:
		artifact, err = service.CreateConfiguration(
			context.Background(), artifactPath, nil,
		)
	default:
		t.Fatalf("unexpected backup type %q", backupType)
	}
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(context.Background(), artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Type != backupType ||
		verified.SchemaVersion != database.MaxSchemaVersion ||
		artifact.SHA256 != verified.SHA256 {
		t.Fatalf("schema-%d %s artifact = %#v verified=%#v",
			origin, backupType, artifact, verified)
	}
	coordinator.Close()
	cancelCoordinator()
	if err := <-coordinatorDone; err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := NewRestorer().Restore(context.Background(), RestoreOptions{
		DataDirectory: dataDirectory,
		DatabasePath:  databasePath,
		ArtifactPath:  artifactPath,
		Confirmation:  RestoreConfirmation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Type != backupType ||
		result.SchemaBefore != database.MaxSchemaVersion ||
		result.SchemaAfter != database.MaxSchemaVersion ||
		result.Migrated {
		t.Fatalf("schema-%d %s restore = %#v", origin, backupType, result)
	}
	rollbackPath := filepath.Join(dataDirectory, result.RollbackName)
	rollback, err := Verify(context.Background(), rollbackPath)
	if err != nil || rollback.Type != TypeFull {
		t.Fatalf("schema-%d rollback = %#v %v", origin, rollback, err)
	}

	restored, err := database.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := database.IntegrityCheck(
		context.Background(), restored.Writer(),
	); err != nil {
		t.Fatal(err)
	}
	assertRestoredUpgradeFixture(t, restored.Reader(), origin, backupType)
	exerciseRestoredUpgradeFixture(t, restored, origin, backupType)
}

func assertRestoredUpgradeFixture(
	t *testing.T,
	db *sql.DB,
	origin int,
	backupType string,
) {
	t.Helper()
	count := func(query string) int {
		var value int
		if err := db.QueryRow(query).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	administrators := count("SELECT count(*) FROM administrators")
	wantAdministrators := 0
	if origin >= 2 {
		wantAdministrators = 1
	}
	events := count("SELECT count(*) FROM log_events")
	containers := count("SELECT count(*) FROM container_instances")
	wantEvents, wantContainers := 1, 1
	if backupType == TypeConfiguration {
		wantEvents, wantContainers = 0, 0
	}
	if administrators != wantAdministrators ||
		count("SELECT count(*) FROM servers WHERE id=11") != 1 ||
		count("SELECT count(*) FROM ingestion_tokens WHERE id=12") != 1 ||
		count("SELECT count(*) FROM sources WHERE id=13") != 1 ||
		count("SELECT count(*) FROM sessions") != 0 ||
		events != wantEvents || containers != wantContainers {
		t.Fatalf("schema-%d %s restored rows = administrators:%d events:%d containers:%d",
			origin, backupType, administrators, events, containers)
	}
	var metadata int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema
		WHERE type='table' AND name='siftail_backup_metadata'`).
		Scan(&metadata); err != nil || metadata != 0 {
		t.Fatalf("schema-%d %s metadata = %d %v",
			origin, backupType, metadata, err)
	}
	var restoreAudit int
	if err := db.QueryRow(`SELECT count(*) FROM security_audit_events
		WHERE action='restore.apply' AND outcome='succeeded'`).
		Scan(&restoreAudit); err != nil || restoreAudit != 1 {
		t.Fatalf("schema-%d %s restore audit = %d %v",
			origin, backupType, restoreAudit, err)
	}
}

func exerciseRestoredUpgradeFixture(
	t *testing.T,
	db *database.DB,
	origin int,
	backupType string,
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
	eventAt := int64(1785196800000200)
	batch := &ingest.WriteBatch{
		AuthenticatedServerID: 11,
		AuthenticatedTokenID:  12,
		Events: []logs.CanonicalEvent{{
			EventAtUS: eventAt, ReceivedAtUS: eventAt + 1,
			Source: logs.SourceIdentity{
				ServerID: 11, Project: "synthetic-project",
				Environment: "fixture", Application: "restored",
				Service: "writer", ProjectLabel: "Synthetic project",
				EnvLabel: "Fixture", AppLabel: "Restored",
				ServiceLabel: "Writer",
			},
			Stream: logs.StreamStdout, Level: logs.LevelInfo,
			MessageRaw:  []byte("synthetic post-restore event"),
			MessageText: "synthetic post-restore event",
			Attributes:  []byte(`{"fixture":true}`),
			SourceEventID: "synthetic-post-restore-" +
				strconv.Itoa(origin) + "-" + backupType,
		}},
	}
	if err := ingest.NewBatchWriter(coordinator, nil).Persist(
		context.Background(), batch,
	); err != nil {
		t.Fatal(err)
	}
	recent, err := logs.NewStore(db.Reader()).Recent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	want := 2
	if backupType == TypeConfiguration {
		want = 1
	}
	if len(recent) != want ||
		recent[0].MessageText != "synthetic post-restore event" {
		t.Fatalf("schema-%d %s post-restore History = %#v",
			origin, backupType, recent)
	}
}

func copyBackupUpgradeFixture(t *testing.T, version int, destinationPath string) {
	t.Helper()
	source, err := os.Open(filepath.Join(
		"..", "database", "testdata", "upgrades",
		"schema-"+strconv.Itoa(version)+".db",
	))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
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
}

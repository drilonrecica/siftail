package sources

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestSourceAliasChangesPresentationOnly(t *testing.T) {
	db := sourceMutationFixture(t, 2)
	store := NewStore(db.Writer())
	var beforeServer int64
	var beforeProject, beforeEnvironment, beforeApplication, beforeService string
	if err := db.Reader().QueryRow(`SELECT
		server_id,project_key,environment_key,application_key,service_key
		FROM sources WHERE id=1`).Scan(
		&beforeServer, &beforeProject, &beforeEnvironment, &beforeApplication, &beforeService,
	); err != nil {
		t.Fatal(err)
	}

	if err := store.SetAlias(context.Background(), 1, "  Public API  "); err != nil {
		t.Fatal(err)
	}
	detail, err := NewStore(db.Reader()).SourceDetail(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Source.Alias == nil || *detail.Source.Alias != "Public API" ||
		detail.Source.DisplayName() != "Public API" {
		t.Fatalf("alias detail = %#v", detail.Source)
	}
	var afterServer, eventSource int64
	var project, environment, application, service string
	if err := db.Reader().QueryRow(`SELECT
		server_id,project_key,environment_key,application_key,service_key
		FROM sources WHERE id=1`).Scan(
		&afterServer, &project, &environment, &application, &service,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Reader().QueryRow(`SELECT source_id FROM log_events WHERE id=1`).
		Scan(&eventSource); err != nil {
		t.Fatal(err)
	}
	if afterServer != beforeServer || eventSource != 1 ||
		project != beforeProject || environment != beforeEnvironment ||
		application != beforeApplication || service != beforeService {
		t.Fatal("alias update changed stable source identity or event ownership")
	}
	if err := store.SetAlias(context.Background(), 1, ""); err != nil {
		t.Fatal(err)
	}
	var alias sql.NullString
	if err := db.Reader().QueryRow(`SELECT alias FROM sources WHERE id=1`).Scan(&alias); err != nil {
		t.Fatal(err)
	}
	if alias.Valid {
		t.Fatalf("removed alias = %#v", alias)
	}
	for _, invalid := range []string{"\x00", string(make([]byte, 129))} {
		if err := store.SetAlias(context.Background(), 1, invalid); err == nil {
			t.Fatalf("invalid alias %q accepted", invalid)
		}
	}
}

func TestClearLogsUsesChunksAndPreservesSourceOwnership(t *testing.T) {
	db := sourceMutationFixture(t, purgeChunkEvents+2)
	store := NewStore(db.Writer())
	if err := store.SetAlias(context.Background(), 1, "Public API"); err != nil {
		t.Fatal(err)
	}
	result, err := store.ClearLogs(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != purgeChunkEvents+2 || result.Watermark != purgeChunkEvents+2 ||
		result.Removed {
		t.Fatalf("clear result = %#v", result)
	}
	assertCount(t, db, `SELECT count(*) FROM log_events WHERE source_id=1`, 0)
	assertCount(t, db, `SELECT count(*) FROM log_events WHERE source_id=2`, 1)
	assertCount(t, db, `SELECT count(*) FROM sources WHERE id=1 AND alias='Public API'`, 1)
	assertCount(t, db, `SELECT count(*) FROM container_instances WHERE source_id=1`, 1)
	assertCount(t, db, `SELECT count(*) FROM servers WHERE id=1`, 1)
}

func TestClearLogsRollsBackFailingChunk(t *testing.T) {
	db := sourceMutationFixture(t, 3)
	if _, err := db.Writer().Exec(`CREATE TRIGGER block_source_purge
		BEFORE DELETE ON log_events WHEN OLD.source_id=1
		BEGIN SELECT RAISE(ABORT, 'blocked purge'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(db.Writer()).ClearLogs(context.Background(), 1); err == nil {
		t.Fatal("blocked purge succeeded")
	}
	assertCount(t, db, `SELECT count(*) FROM log_events WHERE source_id=1`, 3)
}

func TestRemoveSourceDeletesOwnedMetadataButNeverServer(t *testing.T) {
	db := sourceMutationFixture(t, 2)
	store := NewStore(db.Writer())
	if err := store.SetAlias(context.Background(), 1, "Public API"); err != nil {
		t.Fatal(err)
	}
	result, err := store.RemoveSource(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Removed || result.Deleted != 2 {
		t.Fatalf("remove result = %#v", result)
	}
	assertCount(t, db, `SELECT count(*) FROM sources WHERE id=1`, 0)
	assertCount(t, db, `SELECT count(*) FROM container_instances WHERE source_id=1`, 0)
	assertCount(t, db, `SELECT count(*) FROM log_events WHERE source_id=1`, 0)
	assertCount(t, db, `SELECT count(*) FROM sources WHERE id=2`, 1)
	assertCount(t, db, `SELECT count(*) FROM log_events WHERE source_id=2`, 1)
	assertCount(t, db, `SELECT count(*) FROM servers WHERE id=1`, 1)
}

func TestRemoveSourcePreservesEventsCommittedAfterWatermark(t *testing.T) {
	db := sourceMutationFixture(t, 2)
	coordinator := database.NewCoordinator(db.Writer())
	coordinatorDone := make(chan error, 1)
	go func() { coordinatorDone <- coordinator.Run(context.Background()) }()
	<-coordinator.Ready()
	t.Cleanup(func() {
		coordinator.Close()
		if err := <-coordinatorDone; err != nil {
			t.Errorf("coordinator shutdown: %v", err)
		}
	})
	mutator := &interleavingSourceMutator{
		coordinator: coordinator,
	}
	store := NewCoordinatedStore(db.Reader(), mutator)
	if err := store.SetAlias(context.Background(), 1, "Public API"); err != nil {
		t.Fatal(err)
	}
	mutator.calls = 0
	mutator.afterCall = 2
	result, err := store.RemoveSource(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed || result.Watermark != 2 || result.Deleted != 2 {
		t.Fatalf("interleaved remove result = %#v", result)
	}
	assertCount(t, db, `SELECT count(*) FROM sources WHERE id=1 AND alias IS NULL`, 1)
	assertCount(t, db, `SELECT count(*) FROM log_events WHERE source_id=1 AND id=100000`, 1)
}

type interleavingSourceMutator struct {
	coordinator *database.Coordinator
	calls       int
	afterCall   int
}

func (m *interleavingSourceMutator) Do(ctx context.Context, run func(*sql.Tx) error) error {
	if err := m.coordinator.Do(ctx, run); err != nil {
		return err
	}
	m.calls++
	if m.calls == m.afterCall {
		inserted := make(chan error, 1)
		go func() {
			inserted <- m.coordinator.Do(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO log_events(
					id,event_at_us,received_at_us,source_id,
					stream,level_normalized,message_raw,message_text
				) VALUES (100000,100000,100000,1,'stdout','info',x'6e6577','new')`)
				return err
			})
		}()
		return <-inserted
	}
	return nil
}

func sourceMutationFixture(t *testing.T, sourceOneEvents int) *database.DB {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tx, err := db.Writer().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO servers(id,name,created_at_us) VALUES (1,'Production',1)`,
		`INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			first_seen_at_us,last_seen_at_us
		) VALUES
			(1,1,'project','production','api','web',
			 'Project','Production','API','Web',1,1),
			(2,1,'project','production','worker','jobs',
			 'Project','Production','Worker','Jobs',1,1)`,
		`INSERT INTO container_instances(
			id,source_id,container_name,first_seen_at_us,last_seen_at_us
		) VALUES (1,1,'api-1',1,1),(2,2,'worker-1',1,1)`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	eventStatement, err := tx.Prepare(`INSERT INTO log_events(
		id,event_at_us,received_at_us,source_id,container_instance_id,
		stream,level_normalized,message_raw,message_text
	) VALUES (?,?,?,?,?,'stdout','info',x'6c6f67','log')`)
	if err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= sourceOneEvents; id++ {
		if _, err := eventStatement.Exec(id, id, id, 1, 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := eventStatement.Exec(sourceOneEvents+1, sourceOneEvents+1,
		sourceOneEvents+1, 2, 2); err != nil {
		t.Fatal(err)
	}
	if err := eventStatement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return db
}

func assertCount(t *testing.T, db *database.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.Reader().QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s = %d, want %d", query, got, want)
	}
}

func TestSourceMutationsRejectMissingAndCanceledRequests(t *testing.T) {
	db := sourceMutationFixture(t, 1)
	store := NewStore(db.Writer())
	if err := store.SetAlias(context.Background(), 404, "Missing"); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("missing alias error = %v", err)
	}
	if _, err := store.ClearLogs(context.Background(), 404); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("missing clear error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.RemoveSource(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled remove error = %v", err)
	}
	if err := store.SetAlias(ctx, 1, "Canceled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled alias error = %v", err)
	}
}

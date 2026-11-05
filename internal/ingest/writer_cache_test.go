package ingest

import (
	"context"
	"database/sql"
	"net/http"
	"testing"

	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/sources"
)

func sourceIDFor(t *testing.T, db *sql.DB, service string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow("SELECT id FROM sources WHERE service_key=?", service).Scan(&id); err != nil {
		t.Fatalf("source %q: %v", service, err)
	}
	return id
}

func eventSource(t *testing.T, db *sql.DB, message string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow("SELECT source_id FROM log_events WHERE message_text=?", message).Scan(&id); err != nil {
		t.Fatalf("event %q: %v", message, err)
	}
	return id
}

// DAT-02: RemoveSource deletes a source the writer still caches. SQLite then
// hands the freed rowid to the next new source, so an unverified cache hit
// would either fail the FK forever or attach events to the wrong source.
func TestBatchWriterReingestsAfterSourceRemovalWithReusedRowID(t *testing.T) {
	db, coordinator, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	withService := func(message, service string, seen int64) logs.CanonicalEvent {
		event := canonicalEvent(serverID, "", message, seen)
		event.Source.Service, event.Source.ServiceLabel = service, service
		event.Container.ID = "container-" + service
		return event
	}
	persistEvents(t, writer, serverID, withService("api first", "api", 1))
	persistEvents(t, writer, serverID, withService("worker first", "worker", 2))
	removedID := sourceIDFor(t, db.Reader(), "worker")
	workerKey := sourceCacheKey(withService("", "worker", 0).Source)
	if id, ok := writer.sources.Get(workerKey); !ok || id != removedID {
		t.Fatalf("worker source not cached: %d %t", id, ok)
	}

	store := sources.NewCoordinatedStore(db.Reader(), coordinator)
	if result, err := store.RemoveSource(context.Background(), removedID); err != nil || !result.Removed {
		t.Fatalf("remove source = %#v, %v", result, err)
	}

	// A new source takes the freed rowid while the cache still maps the
	// removed worker identity to it.
	persistEvents(t, writer, serverID, withService("cron first", "cron", 3))
	if reused := sourceIDFor(t, db.Reader(), "cron"); reused != removedID {
		t.Fatalf("expected SQLite to reuse rowid %d, got %d", removedID, reused)
	}

	persistEvents(t, writer, serverID, withService("worker again", "worker", 4))
	fresh := sourceIDFor(t, db.Reader(), "worker")
	if fresh == removedID {
		t.Fatalf("worker resolved to the reused rowid %d", fresh)
	}
	if got := eventSource(t, db.Reader(), "worker again"); got != fresh {
		t.Fatalf("worker event source = %d, want fresh %d", got, fresh)
	}
	if got := eventSource(t, db.Reader(), "cron first"); got != removedID {
		t.Fatalf("cron event source = %d, want %d", got, removedID)
	}
	if id, ok := writer.sources.Get(workerKey); !ok || id != fresh {
		t.Fatalf("cache after re-ingestion = %d %t, want %d", id, ok, fresh)
	}
	// The removed worker took its first event and container instance along.
	assertCounts(t, db.Reader(), 3, 3, 3)
}

// A stale ID that escapes verification surfaces as an FK failure on INSERT.
// The writer clears the batch's entries and retries exactly once.
func TestBatchWriterRetriesOnceAfterForeignKeyFailure(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	persistEvents(t, writer, serverID, canonicalEvent(serverID, "", "cached", 1))
	key := sourceCacheKey(canonicalEvent(serverID, "", "", 0).Source)
	sourceID, ok := writer.sources.Get(key)
	if !ok {
		t.Fatal("source was not cached")
	}
	if _, ok := writer.containers.Get(containerKey{sourceID: sourceID, id: "container-1"}); !ok {
		t.Fatal("container was not cached")
	}

	attempts := 0
	writer.beforeInsert = func(tx database.Statements) error {
		attempts++
		if attempts > 1 {
			return nil
		}
		// The cached rows vanish after verification; the INSERT then
		// violates the foreign keys and the attempt rolls back.
		_, err := tx.ExecContext(context.Background(), "DELETE FROM sources WHERE id=?", sourceID)
		return err
	}
	persistEvents(t, writer, serverID, canonicalEvent(serverID, "", "retried", 2))
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	assertCounts(t, db.Reader(), 1, 1, 2)
	if id, ok := writer.sources.Get(key); !ok || id != sourceID {
		t.Fatalf("cache after retry = %d %t", id, ok)
	}
}

func TestBatchWriterForeignKeyRetryIsBounded(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	attempts := 0
	writer.beforeInsert = func(tx database.Statements) error {
		attempts++
		_, err := tx.ExecContext(context.Background(), "DELETE FROM sources")
		return err
	}
	err := writer.Persist(context.Background(), &WriteBatch{
		Events:                []logs.CanonicalEvent{canonicalEvent(serverID, "", "never", 1)},
		AuthenticatedServerID: serverID,
	})
	if err == nil {
		t.Fatal("persistent FK failure succeeded")
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want exactly one retry", attempts)
	}
	assertCounts(t, db.Reader(), 0, 0, 0)
	if writer.sources.Len() != 0 || writer.containers.Len() != 0 {
		t.Fatalf("failed batch populated caches: %s, %s", writer.sources, writer.containers)
	}
}

func (f *ingestionIntegration) ingest(t *testing.T, body string) {
	t.Helper()
	response, done := f.request(context.Background(), body)
	<-done
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body=%q", response.Code, response.Body.String())
	}
}

// DAT-02 acceptance over HTTP: removing a cached source never poisons later
// ingestion; the next request gets 204 and a fresh source row.
func TestIngestionAfterCachedSourceRemovalCreatesFreshSource(t *testing.T) {
	fixture := newIngestionIntegration(t, 10, 1<<20)
	fixture.ingest(t, testEventJSON("", "before removal"))
	sourceID := sourceIDFor(t, fixture.db.Reader(), "api")
	store := sources.NewCoordinatedStore(fixture.db.Reader(), fixture.coordinator)
	if err := store.SetAlias(context.Background(), sourceID, "old alias"); err != nil {
		t.Fatal(err)
	}
	if result, err := store.RemoveSource(context.Background(), sourceID); err != nil || !result.Removed {
		t.Fatalf("remove source = %#v, %v", result, err)
	}

	fixture.ingest(t, testEventJSON("", "after removal"))
	var count int
	var alias sql.NullString
	if err := fixture.db.Reader().QueryRow(
		"SELECT count(*), max(alias) FROM sources",
	).Scan(&count, &alias); err != nil {
		t.Fatal(err)
	}
	if count != 1 || alias.Valid {
		t.Fatalf("sources = %d alias=%#v, want one fresh row without the removed alias", count, alias)
	}
	fresh := sourceIDFor(t, fixture.db.Reader(), "api")
	if got := eventSource(t, fixture.db.Reader(), "after removal"); got != fresh {
		t.Fatalf("event source = %d, want %d", got, fresh)
	}
	assertEventCount(t, fixture.db.Reader(), 1)
}

// The same for a container instance pruned while cached: Clear logs drops its
// events and retention pruning deletes the unreferenced instance.
func TestIngestionAfterCachedContainerRemovalCreatesFreshInstance(t *testing.T) {
	fixture := newIngestionIntegration(t, 10, 1<<20)
	fixture.ingest(t, testEventJSON("", "before removal"))
	sourceID := sourceIDFor(t, fixture.db.Reader(), "api")
	store := sources.NewCoordinatedStore(fixture.db.Reader(), fixture.coordinator)
	if _, err := store.ClearLogs(context.Background(), sourceID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.coordinator.Do(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM container_instances WHERE NOT EXISTS (
			SELECT 1 FROM log_events WHERE container_instance_id = container_instances.id)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var instances int
	if err := fixture.db.Reader().QueryRow("SELECT count(*) FROM container_instances").Scan(&instances); err != nil || instances != 0 {
		t.Fatalf("container instances after pruning = %d, %v", instances, err)
	}

	fixture.ingest(t, testEventJSON("", "after removal"))
	var instanceID int64
	var containerID string
	if err := fixture.db.Reader().QueryRow(
		"SELECT id, container_id FROM container_instances",
	).Scan(&instanceID, &containerID); err != nil {
		t.Fatal(err)
	}
	if containerID != "container-1" {
		t.Fatalf("fresh instance container ID = %q", containerID)
	}
	var eventInstance int64
	if err := fixture.db.Reader().QueryRow(
		"SELECT container_instance_id FROM log_events WHERE message_text='after removal'",
	).Scan(&eventInstance); err != nil || eventInstance != instanceID {
		t.Fatalf("event instance = %d, want %d (%v)", eventInstance, instanceID, err)
	}
}

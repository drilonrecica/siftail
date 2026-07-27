package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/logs"
)

type recordingPublisher struct {
	mu     sync.Mutex
	events []CommittedEvent
}

func (p *recordingPublisher) TryPublish(events []CommittedEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, events...)
}

func TestBatchWriterAtomicPersistenceOrderingAndIdempotency(t *testing.T) {
	db, coordinator, writer := newWriterTest(t, WriterOptions{})
	publisher := &recordingPublisher{}
	writer.publisher = publisher
	serverID := insertTestServer(t, db.Writer())

	first := canonicalEvent(serverID, "evt-1", "first", 20)
	second := canonicalEvent(serverID, "", "second", 10)
	batch := &WriteBatch{Events: []logs.CanonicalEvent{first, second}, AuthenticatedServerID: serverID}
	if err := writer.Persist(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	assertCounts(t, db.Reader(), 1, 1, 2)
	if len(publisher.events) != 2 || publisher.events[0].ID >= publisher.events[1].ID {
		t.Fatalf("published events = %#v", publisher.events)
	}

	retry := first
	retry.ReceivedAtUS += 1_000_000
	retry.Source.ProjectLabel = "latest display label"
	if err := writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{retry}, AuthenticatedServerID: serverID,
	}); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	assertCounts(t, db.Reader(), 1, 1, 2)
	if len(publisher.events) != 2 {
		t.Fatalf("idempotent retry was published: %#v", publisher.events)
	}

	coordinator.Close()
}

func TestBatchWriterConflictRollsBackCompleteBatch(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	original := canonicalEvent(serverID, "stable", "original", 1)
	if err := writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{original}, AuthenticatedServerID: serverID,
	}); err != nil {
		t.Fatal(err)
	}

	newSource := canonicalEvent(serverID, "", "must roll back", 2)
	newSource.Source.Service = "worker"
	newSource.Source.ServiceLabel = "worker"
	conflict := original
	conflict.MessageText = "different"
	conflict.MessageRaw = []byte("different")
	err := writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{newSource, conflict}, AuthenticatedServerID: serverID,
	})
	var ingestErr *Error
	if !errors.As(err, &ingestErr) || ingestErr.Category != CategoryConflict {
		t.Fatalf("conflict error = %#v", err)
	}
	assertCounts(t, db.Reader(), 1, 1, 1)
}

func TestBatchWriterEnforcesServerTrustAndDiscoveryQuotas(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{SourceLimit: 1, ContainerLimit: 1})
	serverID := insertTestServer(t, db.Writer())

	first := canonicalEvent(serverID, "", "one", 1)
	if err := writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{first}, AuthenticatedServerID: serverID,
	}); err != nil {
		t.Fatal(err)
	}
	secondSource := canonicalEvent(serverID, "", "two", 2)
	secondSource.Source.Service = "worker"
	secondSource.Source.ServiceLabel = "worker"
	err := writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{secondSource}, AuthenticatedServerID: serverID,
	})
	assertCategory(t, err, CategoryForbidden)
	assertCounts(t, db.Reader(), 1, 1, 1)

	secondContainer := canonicalEvent(serverID, "", "three", 3)
	secondContainer.Container = &logs.ContainerIdentity{ID: "container-2", Name: "app-2"}
	err = writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{secondContainer}, AuthenticatedServerID: serverID,
	})
	assertCategory(t, err, CategoryForbidden)
	assertCounts(t, db.Reader(), 1, 1, 1)

	untrusted := canonicalEvent(serverID+1, "", "spoof", 4)
	err = writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{untrusted}, AuthenticatedServerID: serverID,
	})
	assertCategory(t, err, CategoryForbidden)
}

func TestBatchWriterCachesRemainBoundedAndReconstructable(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{
		SourceLimit: 20, ContainerLimit: 20, SourceCache: 2, ContainerCache: 3,
	})
	serverID := insertTestServer(t, db.Writer())
	for i := 0; i < 8; i++ {
		event := canonicalEvent(serverID, "", fmt.Sprintf("event-%d", i), int64(i+1))
		event.Source.Service = fmt.Sprintf("service-%d", i)
		event.Source.ServiceLabel = event.Source.Service
		event.Container = &logs.ContainerIdentity{
			ID: fmt.Sprintf("container-%d", i), Name: fmt.Sprintf("app-%d", i),
		}
		if err := writer.Persist(context.Background(), &WriteBatch{
			Events: []logs.CanonicalEvent{event}, AuthenticatedServerID: serverID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if writer.sources.Len() != 2 || writer.containers.Len() != 3 {
		t.Fatalf("cache sizes = %d/%d", writer.sources.Len(), writer.containers.Len())
	}

	// Revisit an evicted identity. SQLite remains authoritative and no duplicate
	// source or container is created.
	event := canonicalEvent(serverID, "", "again", 20)
	event.Source.Service = "service-0"
	event.Source.ServiceLabel = "service-0"
	event.Container = &logs.ContainerIdentity{ID: "container-0", Name: "app-0"}
	if err := writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{event}, AuthenticatedServerID: serverID,
	}); err != nil {
		t.Fatal(err)
	}
	assertCounts(t, db.Reader(), 8, 8, 9)
}

func TestBatchWriterConcurrentStress(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			event := canonicalEvent(serverID, fmt.Sprintf("stable-%d", i), fmt.Sprintf("message-%d", i), int64(i+1))
			if err := writer.Persist(context.Background(), &WriteBatch{
				Events: []logs.CanonicalEvent{event}, AuthenticatedServerID: serverID,
			}); err != nil {
				t.Errorf("persist %d: %v", i, err)
			}
		}(i)
	}
	workers.Wait()
	assertCounts(t, db.Reader(), 1, 1, 32)
}

func TestBatchWriterBusyFailureIsTemporary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Writer().Exec("PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	serverID := insertTestServer(t, db.Writer())

	ctx, cancel := context.WithCancel(context.Background())
	coordinator := database.NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	defer func() {
		coordinator.Close()
		cancel()
		<-done
	}()

	locker, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	if _, err := locker.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}
	lock, err := locker.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Exec("INSERT INTO settings(key,value_json,updated_at_us) VALUES('writer-lock','{}',1)"); err != nil {
		t.Fatal(err)
	}

	event := canonicalEvent(serverID, "", "busy", 1)
	err = NewBatchWriter(coordinator, nil).Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{event}, AuthenticatedServerID: serverID,
	})
	assertCategory(t, err, CategoryUnavailable)
	if statusFor(err) != 503 {
		t.Fatalf("busy status = %d", statusFor(err))
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertCounts(t, db.Reader(), 0, 0, 0)
}

func TestPersistenceErrorClassification(t *testing.T) {
	tests := []struct {
		err      error
		category ErrorCategory
	}{
		{&database.CategoryError{Category: database.CategoryBusy}, CategoryUnavailable},
		{&database.CategoryError{Category: database.CategoryIO}, CategoryUnavailable},
		{&database.CategoryError{Category: database.CategoryFull}, CategoryStorageFull},
		{errCanonicalConflict, CategoryConflict},
	}
	for _, test := range tests {
		classified := persistenceError(test.err)
		assertCategory(t, classified, test.category)
		if got, want := statusFor(classified), map[ErrorCategory]int{
			CategoryUnavailable: 503,
			CategoryStorageFull: 507,
			CategoryConflict:    409,
		}[test.category]; got != want {
			t.Fatalf("status = %d, want %d", got, want)
		}
	}
}

func newWriterTest(t *testing.T, options WriterOptions) (*database.DB, *database.Coordinator, *BatchWriter) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := database.NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	select {
	case <-coordinator.Ready():
	case <-time.After(time.Second):
		t.Fatal("coordinator did not start")
	}
	t.Cleanup(func() {
		coordinator.Close()
		cancel()
		<-done
		_ = db.Close()
	})
	return db, coordinator, NewBatchWriterWithOptions(coordinator, nil, options)
}

func insertTestServer(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	result, err := db.Exec("INSERT INTO servers(name,created_at_us) VALUES('test-server',1)")
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func canonicalEvent(serverID int64, sourceEventID, message string, seen int64) logs.CanonicalEvent {
	return logs.CanonicalEvent{
		EventAtUS: seen, ReceivedAtUS: seen + 100,
		Source: logs.SourceIdentity{
			ServerID: serverID, Project: "project", Environment: "production",
			Application: "app", Service: "api", ProjectLabel: "project",
			EnvLabel: "production", AppLabel: "app", ServiceLabel: "api",
		},
		Container:     &logs.ContainerIdentity{ID: "container-1", Name: "app-1"},
		Stream:        logs.StreamStdout,
		Level:         logs.LevelInfo,
		MessageRaw:    []byte(message),
		MessageText:   message,
		Attributes:    []byte(`{"key":"value"}`),
		SourceEventID: sourceEventID,
	}
}

func assertCounts(t *testing.T, db *sql.DB, sources, containers, events int) {
	t.Helper()
	for table, want := range map[string]int{
		"sources": sources, "container_instances": containers, "log_events": events,
	} {
		var got int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
}

func assertCategory(t *testing.T, err error, want ErrorCategory) {
	t.Helper()
	var ingestErr *Error
	if !errors.As(err, &ingestErr) || ingestErr.Category != want {
		t.Fatalf("error = %#v, want category %s", err, want)
	}
}

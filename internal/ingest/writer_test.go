package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/retention"
)

type recordingPublisher struct {
	mu     sync.Mutex
	events []logs.CommittedEvent
}

func (p *recordingPublisher) TryPublish(events []logs.CommittedEvent) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, events...)
	return true
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
	for _, event := range publisher.events {
		if event.SourceID <= 0 || event.ContainerInstanceID <= 0 {
			t.Fatalf("published database identities = %#v", event)
		}
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

func TestBatchWriterDropsConflictingRecordAndCommitsRest(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	publisher := &recordingPublisher{}
	writer.publisher = publisher
	serverID := insertTestServer(t, db.Writer())
	original := canonicalEvent(serverID, "stable", "original", 1)
	if err := writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{original}, AuthenticatedServerID: serverID,
	}); err != nil {
		t.Fatal(err)
	}

	newSource := canonicalEvent(serverID, "", "co-batched", 2)
	newSource.Source.Service = "worker"
	newSource.Source.ServiceLabel = "worker"
	conflict := original
	conflict.MessageText = "different"
	conflict.MessageRaw = []byte("different")
	result, err := writer.PersistWithResult(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{newSource, conflict}, AuthenticatedServerID: serverID,
	})
	if err != nil || result.ConflictsDropped != 1 {
		t.Fatalf("conflict result = %#v, %v", result, err)
	}
	assertCounts(t, db.Reader(), 2, 2, 2)
	var stored string
	if err := db.Reader().QueryRow(
		"SELECT message_text FROM log_events WHERE source_event_id='stable'",
	).Scan(&stored); err != nil || stored != "original" {
		t.Fatalf("authoritative content = %q, err=%v", stored, err)
	}
	if len(publisher.events) != 2 || publisher.events[1].Event.MessageText != "co-batched" {
		t.Fatalf("published events = %#v", publisher.events)
	}

	// A batch whose only record conflicts still commits as a no-op.
	result, err = writer.PersistWithResult(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{conflict}, AuthenticatedServerID: serverID,
	})
	if err != nil || result.ConflictsDropped != 1 {
		t.Fatalf("all-conflict result = %#v, %v", result, err)
	}
	assertCounts(t, db.Reader(), 2, 2, 2)
	if len(publisher.events) != 2 {
		t.Fatalf("dropped record was published: %#v", publisher.events)
	}
}

func TestBatchWriterDefaultedEventTimeIsPersistedAndIgnoredByDeduplication(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	first := canonicalEvent(serverID, "no-time", "defaulted", 0)
	first.EventAtUS, first.ReceivedAtUS, first.EventAtDefaulted = 1_000, 1_000, true
	if err := writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{first}, AuthenticatedServerID: serverID,
	}); err != nil {
		t.Fatal(err)
	}
	var defaulted bool
	if err := db.Reader().QueryRow(
		"SELECT event_at_defaulted FROM log_events WHERE source_event_id='no-time'",
	).Scan(&defaulted); err != nil || !defaulted {
		t.Fatalf("persisted defaulted flag = %v, err=%v", defaulted, err)
	}

	retry := first
	retry.EventAtUS, retry.ReceivedAtUS = 5_000_000, 5_000_000
	result, err := writer.PersistWithResult(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{retry}, AuthenticatedServerID: serverID,
	})
	if err != nil || result.ConflictsDropped != 0 {
		t.Fatalf("defaulted retry = %#v, %v", result, err)
	}
	assertCounts(t, db.Reader(), 1, 1, 1)

	// A supplied time never set the flag and stays part of equality.
	supplied := canonicalEvent(serverID, "with-time", "supplied", 10)
	if err := writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{supplied}, AuthenticatedServerID: serverID,
	}); err != nil {
		t.Fatal(err)
	}
	moved := supplied
	moved.EventAtUS++
	result, err = writer.PersistWithResult(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{moved}, AuthenticatedServerID: serverID,
	})
	if err != nil || result.ConflictsDropped != 1 {
		t.Fatalf("supplied-time conflict = %#v, %v", result, err)
	}
}

func TestBatchWriterContainerRenameBetweenAttemptsIsNotAConflict(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	first := canonicalEvent(serverID, "renamed", "same content", 10)
	if err := writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{first}, AuthenticatedServerID: serverID,
	}); err != nil {
		t.Fatal(err)
	}
	// Another event renames the container instance, then the first record is
	// retried carrying the new name (ADR 0006: the name is a mutable label).
	retry := first
	retry.Container = &logs.ContainerIdentity{ID: "container-1", Name: "app-1-renamed"}
	retry.ReceivedAtUS += 1_000_000
	result, err := writer.PersistWithResult(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{retry}, AuthenticatedServerID: serverID,
	})
	if err != nil || result.ConflictsDropped != 0 {
		t.Fatalf("renamed retry = %#v, %v", result, err)
	}
	assertCounts(t, db.Reader(), 1, 1, 1)

	recreated := first
	recreated.Container = &logs.ContainerIdentity{ID: "container-2", Name: "app-1-renamed"}
	result, err = writer.PersistWithResult(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{recreated}, AuthenticatedServerID: serverID,
	})
	if err != nil || result.ConflictsDropped != 1 {
		t.Fatalf("different container ID = %#v, %v", result, err)
	}
}

func TestBatchWriterTreatsMigratedDefaultedRowAsDefaulted(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	// The row as migration 0006 leaves a pre-ADR-0007 timestamp-less event:
	// equal times, flagged defaulted, no container.
	if _, err := db.Writer().Exec(`INSERT INTO sources(id,server_id,project_key,environment_key,
		application_key,service_key,project_label,environment_label,application_label,service_label,
		first_seen_at_us,last_seen_at_us) VALUES(1,?,'project','production','app','api',
		'project','production','app','api',1,1);
		INSERT INTO log_events(event_at_us,event_at_defaulted,received_at_us,source_id,stream,
		level_normalized,message_raw,message_text,attributes_json,source_event_id)
		VALUES(700,1,700,1,'stdout','info',x'6d69677261746564','migrated','{"key":"value"}','legacy')`,
		serverID); err != nil {
		t.Fatal(err)
	}
	retry := canonicalEvent(serverID, "legacy", "migrated", 0)
	retry.Container = nil
	retry.EventAtUS, retry.ReceivedAtUS, retry.EventAtDefaulted = 9_000, 9_000, true
	result, err := writer.PersistWithResult(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{retry}, AuthenticatedServerID: serverID,
	})
	if err != nil || result.ConflictsDropped != 0 {
		t.Fatalf("retry of migrated row = %#v, %v", result, err)
	}
	assertCounts(t, db.Reader(), 1, 0, 1)
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

func TestBatchWriterRejectsTokenRevokedBeforeCommit(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	result, err := db.Writer().Exec(`INSERT INTO ingestion_tokens(
		server_id,name,token_hash,fingerprint,created_at_us
	) VALUES(?,?,zeroblob(32),?,?)`, serverID, "revoked-before-commit", "123456789012", 1)
	if err != nil {
		t.Fatal(err)
	}
	tokenID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(
		"UPDATE ingestion_tokens SET revoked_at_us=? WHERE id=?",
		2, tokenID,
	); err != nil {
		t.Fatal(err)
	}

	event := canonicalEvent(serverID, "", "must not commit", 3)
	err = writer.Persist(context.Background(), &WriteBatch{
		Events:                []logs.CanonicalEvent{event},
		AuthenticatedServerID: serverID,
		AuthenticatedTokenID:  tokenID,
	})
	assertCategory(t, err, CategoryForbidden)
	assertCounts(t, db.Reader(), 0, 0, 0)

	var lastUsedAtUS sql.NullInt64
	if err := db.Reader().QueryRow(
		"SELECT last_used_at_us FROM ingestion_tokens WHERE id=?",
		tokenID,
	).Scan(&lastUsedAtUS); err != nil {
		t.Fatal(err)
	}
	if lastUsedAtUS.Valid {
		t.Fatalf("last_used_at_us = %d after rejected commit", lastUsedAtUS.Int64)
	}
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

func TestBatchWriterStorageFullRollsBackPreservesReadsAndRecovers(t *testing.T) {
	db, coordinator, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	baseline := canonicalEvent(serverID, "baseline", "retained", 1)
	if err := writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{baseline}, AuthenticatedServerID: serverID,
	}); err != nil {
		t.Fatal(err)
	}

	var pages int
	if err := db.Writer().QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(
		"PRAGMA max_page_count=" + strconv.Itoa(pages),
	); err != nil {
		t.Fatal(err)
	}

	readErrors := make(chan error, 1)
	go func() {
		for index := 0; index < 20; index++ {
			var message string
			if err := db.Reader().QueryRow(
				"SELECT message_text FROM log_events WHERE source_event_id='baseline'",
			).Scan(&message); err != nil {
				readErrors <- err
				return
			}
			if message != "retained" {
				readErrors <- fmt.Errorf("retained message = %q", message)
				return
			}
		}
		readErrors <- nil
	}()

	large := canonicalEvent(serverID, "must-rollback", strings.Repeat("x", 64<<10), 2)
	err := writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{large}, AuthenticatedServerID: serverID,
	})
	assertCategory(t, err, CategoryStorageFull)
	if err := <-readErrors; err != nil {
		t.Fatalf("read while full: %v", err)
	}
	assertCounts(t, db.Reader(), 1, 1, 1)

	if _, err := db.Writer().Exec(
		"PRAGMA max_page_count=" + strconv.Itoa(pages+1024),
	); err != nil {
		t.Fatal(err)
	}
	if err := database.ProbeWritable(
		context.Background(), coordinator, time.Now(),
	); err != nil {
		t.Fatalf("recovery probe: %v", err)
	}
	if err := writer.Persist(context.Background(), &WriteBatch{
		Events: []logs.CanonicalEvent{large}, AuthenticatedServerID: serverID,
	}); err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	assertCounts(t, db.Reader(), 1, 1, 2)
}

func TestPersistenceErrorClassification(t *testing.T) {
	tests := []struct {
		err      error
		category ErrorCategory
	}{
		{&database.CategoryError{Category: database.CategoryBusy}, CategoryUnavailable},
		{&database.CategoryError{Category: database.CategoryIO}, CategoryUnavailable},
		{&database.CategoryError{Category: database.CategoryCorrupt}, CategoryUnavailable},
		{&database.CategoryError{Category: database.CategoryFull}, CategoryStorageFull},
		{&database.CategoryError{Category: database.CategoryConstraint}, CategoryBadRequest},
		{&database.CategoryError{Category: database.CategoryConstraint, ForeignKey: true}, CategoryInternal},
		{&database.CategoryError{Category: database.CategoryInternal}, CategoryInternal},
		{errors.New("unclassified"), CategoryInternal},
		{database.ErrCoordinatorClosed, CategoryUnavailable},
	}
	for _, test := range tests {
		classified := persistenceError(test.err)
		assertCategory(t, classified, test.category)
		if got, want := statusFor(classified), map[ErrorCategory]int{
			CategoryUnavailable: 503,
			CategoryStorageFull: 507,
			CategoryBadRequest:  400,
			CategoryInternal:    500,
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

type containerRow struct {
	id          int64
	containerID string
	name        string
}

func containerRows(t *testing.T, db *sql.DB) []containerRow {
	t.Helper()
	rows, err := db.Query(`SELECT id, coalesce(container_id, ''), coalesce(container_name, '')
		FROM container_instances ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []containerRow
	for rows.Next() {
		var row containerRow
		if err := rows.Scan(&row.id, &row.containerID, &row.name); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func eventContainer(t *testing.T, db *sql.DB, message string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow("SELECT container_instance_id FROM log_events WHERE message_text=?", message).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func persistEvents(t *testing.T, writer *BatchWriter, serverID int64, events ...logs.CanonicalEvent) {
	t.Helper()
	if err := writer.Persist(context.Background(), &WriteBatch{
		Events: events, AuthenticatedServerID: serverID,
	}); err != nil {
		t.Fatal(err)
	}
}

// `docker compose up --force-recreate` and Coolify redeploys reuse the
// container name with a new container ID. Before migration 0005 the INSERT
// violated UNIQUE (source_id, container_name) and blocked the server forever.
func TestBatchWriterRecreatedContainerSameNameNewIDNeverBlocks(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	before := canonicalEvent(serverID, "", "before recreate", 1)
	before.Container = &logs.ContainerIdentity{ID: "old-id", Name: "app-web-1"}
	persistEvents(t, writer, serverID, before)

	// Fluent Bit chunks mix the tail of the old container with the head of
	// the new one.
	oldTail := canonicalEvent(serverID, "", "old tail", 2)
	oldTail.Container = &logs.ContainerIdentity{ID: "old-id", Name: "app-web-1"}
	newHead := canonicalEvent(serverID, "", "new head", 3)
	newHead.Container = &logs.ContainerIdentity{ID: "new-id", Name: "app-web-1"}
	persistEvents(t, writer, serverID, oldTail, newHead)

	again := canonicalEvent(serverID, "", "after recreate", 4)
	again.Container = &logs.ContainerIdentity{ID: "new-id", Name: "app-web-1"}
	persistEvents(t, writer, serverID, again)

	rows := containerRows(t, db.Reader())
	if len(rows) != 2 || rows[0].containerID != "old-id" || rows[1].containerID != "new-id" ||
		rows[0].name != "app-web-1" || rows[1].name != "app-web-1" {
		t.Fatalf("container instances = %#v", rows)
	}
	if eventContainer(t, db.Reader(), "before recreate") != rows[0].id ||
		eventContainer(t, db.Reader(), "old tail") != rows[0].id ||
		eventContainer(t, db.Reader(), "new head") != rows[1].id ||
		eventContainer(t, db.Reader(), "after recreate") != rows[1].id {
		t.Fatal("events attached to the wrong container instance")
	}
	assertCounts(t, db.Reader(), 1, 2, 4)
}

func TestBatchWriterRenamedContainerSameIDUpdatesLabel(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	first := canonicalEvent(serverID, "", "first", 10)
	first.Container = &logs.ContainerIdentity{ID: "stable-id", Name: "old-name"}
	persistEvents(t, writer, serverID, first)

	// The cached ID still updates the stored label.
	renamed := canonicalEvent(serverID, "", "renamed", 20)
	renamed.Container = &logs.ContainerIdentity{ID: "stable-id", Name: "new-name"}
	persistEvents(t, writer, serverID, renamed)
	rows := containerRows(t, db.Reader())
	if len(rows) != 1 || rows[0].name != "new-name" {
		t.Fatalf("renamed container instances = %#v", rows)
	}
	if eventContainer(t, db.Reader(), "first") != rows[0].id ||
		eventContainer(t, db.Reader(), "renamed") != rows[0].id {
		t.Fatal("rename split one container into several instances")
	}

	// A redelivered older chunk does not revert the newer label.
	stale := canonicalEvent(serverID, "", "stale redelivery", 5)
	stale.Container = &logs.ContainerIdentity{ID: "stable-id", Name: "old-name"}
	persistEvents(t, writer, serverID, stale)
	if rows := containerRows(t, db.Reader()); len(rows) != 1 || rows[0].name != "new-name" {
		t.Fatalf("stale label overwrote the newer one: %#v", rows)
	}

	// Within one batch the newest observation names the instance, and a name
	// can be dropped and come back.
	unnamed := canonicalEvent(serverID, "", "unnamed", 30)
	unnamed.Container = &logs.ContainerIdentity{ID: "stable-id"}
	persistEvents(t, writer, serverID, unnamed)
	if rows := containerRows(t, db.Reader()); len(rows) != 1 || rows[0].name != "" {
		t.Fatalf("absent name was not recorded: %#v", rows)
	}
	later := canonicalEvent(serverID, "", "batch newest", 50)
	later.Container = &logs.ContainerIdentity{ID: "stable-id", Name: "newest"}
	earlier := canonicalEvent(serverID, "", "batch older", 40)
	earlier.Container = &logs.ContainerIdentity{ID: "stable-id", Name: "older"}
	persistEvents(t, writer, serverID, later, earlier)
	if rows := containerRows(t, db.Reader()); len(rows) != 1 || rows[0].name != "newest" {
		t.Fatalf("batch label = %#v", rows)
	}
	assertCounts(t, db.Reader(), 1, 1, 6)
}

func TestBatchWriterReingestsAfterContainerInstanceRemoval(t *testing.T) {
	db, coordinator, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	first := canonicalEvent(serverID, "", "before removal", 1)
	persistEvents(t, writer, serverID, first)
	removed := containerRows(t, db.Reader())
	if len(removed) != 1 {
		t.Fatalf("container instances = %#v", removed)
	}
	if _, ok := writer.containers.Get(containerKey{sourceID: 1, id: "container-1"}); !ok {
		t.Fatal("container was not cached")
	}

	// Retention purges the events, then prunes the unreferenced instance
	// while the writer still caches its ID.
	if err := coordinator.Do(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM log_events"); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM container_instances")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// SQLite reuses the freed rowid for a different container. The stale
	// cache entry must not attach container-1 events to it.
	other := canonicalEvent(serverID, "", "other container", 2)
	other.Container = &logs.ContainerIdentity{ID: "container-2", Name: "app-2"}
	persistEvents(t, writer, serverID, other)
	reused := containerRows(t, db.Reader())
	if len(reused) != 1 || reused[0].id != removed[0].id {
		t.Fatalf("expected SQLite to reuse the freed instance ID: %#v (removed %#v)", reused, removed)
	}

	again := canonicalEvent(serverID, "", "after removal", 3)
	persistEvents(t, writer, serverID, again)
	rows := containerRows(t, db.Reader())
	if len(rows) != 2 || rows[1].containerID != "container-1" || rows[1].name != "app-1" ||
		rows[0].containerID != "container-2" || rows[0].name != "app-2" {
		t.Fatalf("re-ingested container instances = %#v", rows)
	}
	if eventContainer(t, db.Reader(), "after removal") != rows[1].id ||
		eventContainer(t, db.Reader(), "other container") != rows[0].id {
		t.Fatal("re-ingested event does not reference its own instance")
	}
	if id, ok := writer.containers.Get(containerKey{sourceID: 1, id: "container-1"}); !ok || id != rows[1].id {
		t.Fatalf("cache after re-ingestion = %d %t", id, ok)
	}
}

func TestBatchWriterNameOnlyContainerNeverGuessesAnIdentifiedInstance(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	identified := canonicalEvent(serverID, "", "identified", 1)
	identified.Container = &logs.ContainerIdentity{ID: "with-id", Name: "shared-name"}
	nameOnly := canonicalEvent(serverID, "", "name only", 2)
	nameOnly.Container = &logs.ContainerIdentity{Name: "shared-name"}
	persistEvents(t, writer, serverID, identified, nameOnly)

	nameOnlyAgain := canonicalEvent(serverID, "", "name only again", 3)
	nameOnlyAgain.Container = &logs.ContainerIdentity{Name: "shared-name"}
	persistEvents(t, writer, serverID, nameOnlyAgain)

	rows := containerRows(t, db.Reader())
	if len(rows) != 2 || rows[0].containerID != "with-id" || rows[1].containerID != "" ||
		rows[1].name != "shared-name" {
		t.Fatalf("container instances = %#v", rows)
	}
	if eventContainer(t, db.Reader(), "name only") != rows[1].id ||
		eventContainer(t, db.Reader(), "name only again") != rows[1].id {
		t.Fatal("name-only events did not resolve to the ID-less instance")
	}
}

func TestBatchWriterContainerCapCountsTheWholeBatch(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{ContainerLimit: 2})
	serverID := insertTestServer(t, db.Writer())
	var events []logs.CanonicalEvent
	for i := 0; i < 3; i++ {
		event := canonicalEvent(serverID, "", fmt.Sprintf("event-%d", i), int64(i+1))
		event.Container = &logs.ContainerIdentity{ID: fmt.Sprintf("id-%d", i), Name: "replica"}
		events = append(events, event)
	}
	err := writer.Persist(context.Background(), &WriteBatch{Events: events, AuthenticatedServerID: serverID})
	assertCategory(t, err, CategoryForbidden)
	assertCounts(t, db.Reader(), 0, 0, 0)
	persistEvents(t, writer, serverID, events[:2]...)
	assertCounts(t, db.Reader(), 1, 2, 2)
}

func TestBatchWriterCountsLateEventsAgainstPersistedAgeLimit(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	day := int64(24 * time.Hour / time.Microsecond)
	received := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).UnixMicro()
	event := func(id, message string, eventAt int64, defaulted bool) logs.CanonicalEvent {
		value := canonicalEvent(serverID, id, message, 0)
		value.EventAtUS, value.ReceivedAtUS, value.EventAtDefaulted = eventAt, received, defaulted
		return value
	}
	persist := func(events ...logs.CanonicalEvent) PersistResult {
		t.Helper()
		result, err := writer.PersistWithResult(context.Background(), &WriteBatch{
			Events: events, AuthenticatedServerID: serverID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	cutoff := retention.AgeCutoffUS(time.UnixMicro(received), retention.DefaultAgeDays)
	if cutoff != received-retention.DefaultAgeDays*day {
		t.Fatalf("default cutoff = %d", cutoff)
	}

	// Default 14-day policy: only an event time before the cutoff is late.
	result := persist(
		event("late", "delayed backlog", cutoff-1, false),
		event("", "exactly at the cutoff", cutoff, false),
		event("", "recent", received-day, false),
		event("", "future-skewed", received+365*day, false),
		event("", "defaulted", received, true),
	)
	if result.LateEvents != 1 || result.ConflictsDropped != 0 {
		t.Fatalf("default-policy result = %#v", result)
	}
	assertCounts(t, db.Reader(), 1, 1, 5)

	// A late event is stored like any other; an idempotent retry of it is
	// not stored again and not counted again.
	if result := persist(event("late", "delayed backlog", cutoff-1, false)); result.LateEvents != 0 {
		t.Fatalf("idempotent late retry result = %#v", result)
	}
	assertCounts(t, db.Reader(), 1, 1, 5)

	// The persisted policy decides: under 30 days, 15 days old is not late.
	if _, err := db.Writer().Exec(`INSERT INTO settings(key,value_json,updated_at_us)
		VALUES('application_retention','{"age_days":30,"max_database_bytes":4294967296}',1)`); err != nil {
		t.Fatal(err)
	}
	result = persist(
		event("", "fifteen days", received-15*day, false),
		event("", "thirty-one days", received-31*day, false),
	)
	if result.LateEvents != 1 {
		t.Fatalf("30-day-policy result = %#v", result)
	}

	// An invalid stored policy leaves late events uncounted but never fails
	// ingestion; retention reports the invalid policy itself.
	if _, err := db.Writer().Exec(`UPDATE settings SET value_json='{"age_days":0}'
		WHERE key='application_retention'`); err != nil {
		t.Fatal(err)
	}
	result = persist(event("", "invalid policy", received-400*day, false))
	if result.LateEvents != 0 {
		t.Fatalf("invalid-policy result = %#v", result)
	}
	assertCounts(t, db.Reader(), 1, 1, 8)
}

func TestPersistenceErrorCategories(t *testing.T) {
	categoryErr := func(category database.Category, foreignKey bool) error {
		return &database.CategoryError{Category: category, Operation: "test", ForeignKey: foreignKey}
	}
	for name, tt := range map[string]struct {
		err  error
		want ErrorCategory
	}{
		"ingest error":       {&Error{Category: CategoryForbidden}, CategoryForbidden},
		"busy":               {categoryErr(database.CategoryBusy, false), CategoryUnavailable},
		"io":                 {categoryErr(database.CategoryIO, false), CategoryUnavailable},
		"corrupt":            {categoryErr(database.CategoryCorrupt, false), CategoryUnavailable},
		"full":               {categoryErr(database.CategoryFull, false), CategoryStorageFull},
		"constraint":         {categoryErr(database.CategoryConstraint, false), CategoryBadRequest},
		"stale foreign key":  {categoryErr(database.CategoryConstraint, true), CategoryInternal},
		"internal":           {categoryErr(database.CategoryInternal, false), CategoryInternal},
		"coordinator closed": {database.ErrCoordinatorClosed, CategoryUnavailable},
		"canceled":           {context.Canceled, CategoryUnavailable},
		"deadline":           {fmt.Errorf("wrapped: %w", context.DeadlineExceeded), CategoryUnavailable},
		"unknown":            {errors.New("other"), CategoryInternal},
		// A classified internal failure stays internal even if its cause
		// is a cancellation.
		"internal wrapping canceled": {&database.CategoryError{
			Category: database.CategoryInternal, Operation: "test", Err: context.Canceled,
		}, CategoryInternal},
	} {
		var got *Error
		if !errors.As(persistenceError(tt.err), &got) || got.Category != tt.want {
			t.Errorf("%s: persistenceError = %v, want %s", name, persistenceError(tt.err), tt.want)
		}
	}
}

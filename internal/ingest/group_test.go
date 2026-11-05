package ingest

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/logs"
)

// visibilityPublisher records every publish call and, at the moment of the
// call, whether the published events are already visible to a separate reader,
// which proves they committed before Live saw them.
type visibilityPublisher struct {
	reader *sql.DB
	mu     sync.Mutex
	calls  [][]logs.CommittedEvent
	hidden int
}

func (p *visibilityPublisher) TryPublish(events []logs.CommittedEvent) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, event := range events {
		var found int
		if err := p.reader.QueryRow("SELECT count(*) FROM log_events WHERE id=?", event.ID).Scan(&found); err != nil || found != 1 {
			p.hidden++
		}
	}
	p.calls = append(p.calls, append([]logs.CommittedEvent(nil), events...))
	return true
}

func (p *visibilityPublisher) snapshot() ([][]logs.CommittedEvent, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]logs.CommittedEvent(nil), p.calls...), p.hidden
}

func serviceEvent(serverID int64, service, sourceEventID, message string, seen int64) logs.CanonicalEvent {
	event := canonicalEvent(serverID, sourceEventID, message, seen)
	event.Source.Service, event.Source.ServiceLabel = service, service
	return event
}

func testBatch(serverID int64, events ...logs.CanonicalEvent) *WriteBatch {
	return &WriteBatch{Events: events, AuthenticatedServerID: serverID}
}

// failOnCall returns a beforeInsert seam that fails the nth batch attempt of a
// group with err. Attempts run in group order, one per batch unless a batch is
// retried after a foreign-key failure.
func failOnCall(n int, err error) func(database.Statements) error {
	calls := 0
	return func(database.Statements) error {
		calls++
		if calls == n {
			return err
		}
		return nil
	}
}

func messages(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query("SELECT message_text FROM log_events ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var message string
		if err := rows.Scan(&message); err != nil {
			t.Fatal(err)
		}
		out = append(out, message)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPersistGroupBatchScopedFailureKeepsOtherBatches(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	publisher := &visibilityPublisher{reader: db.Reader()}
	writer.publisher = publisher
	serverID := insertTestServer(t, db.Writer())
	writer.beforeInsert = failOnCall(2, &database.CategoryError{
		Category: database.CategoryConstraint, Operation: "poison",
	})

	// The failing batch creates a new source inside its savepoint; the next
	// batch needs the same source and must not reuse the rolled-back row.
	outcomes := writer.PersistGroup(context.Background(), []*WriteBatch{
		testBatch(serverID, serviceEvent(serverID, "api", "", "first", 1)),
		testBatch(serverID, serviceEvent(serverID, "worker", "", "poisoned", 2)),
		testBatch(serverID, serviceEvent(serverID, "worker", "", "third", 3)),
	})
	if outcomes[0].Err != nil || outcomes[2].Err != nil {
		t.Fatalf("healthy outcomes = %v / %v", outcomes[0].Err, outcomes[2].Err)
	}
	assertCategory(t, outcomes[1].Err, CategoryBadRequest)
	if got := messages(t, db.Reader()); strings.Join(got, ",") != "first,third" {
		t.Fatalf("committed messages = %v", got)
	}
	assertCounts(t, db.Reader(), 2, 2, 2)

	workerID := sourceIDFor(t, db.Reader(), "worker")
	key := sourceCacheKey(serviceEvent(serverID, "worker", "", "", 0).Source)
	if cached, ok := writer.sources.Get(key); !ok || cached != workerID {
		t.Fatalf("cached worker source = %d %t, stored %d", cached, ok, workerID)
	}

	calls, hidden := publisher.snapshot()
	if hidden != 0 || len(calls) != 2 || calls[0][0].Event.MessageText != "first" ||
		calls[1][0].Event.MessageText != "third" || calls[0][0].ID >= calls[1][0].ID {
		t.Fatalf("publications = %+v hidden=%d", calls, hidden)
	}
}

func TestPersistGroupQuotaAndDeduplicationSeeEarlierBatches(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{SourceLimit: 1})
	serverID := insertTestServer(t, db.Writer())
	outcomes := writer.PersistGroup(context.Background(), []*WriteBatch{
		testBatch(serverID, serviceEvent(serverID, "api", "stable", "original", 1)),
		testBatch(serverID, serviceEvent(serverID, "api", "stable", "different", 1)),
		testBatch(serverID, serviceEvent(serverID, "second-source", "", "over quota", 2)),
		testBatch(serverID, serviceEvent(serverID, "api", "", "after rejection", 3)),
	})
	if outcomes[0].Err != nil || outcomes[1].Err != nil || outcomes[3].Err != nil {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if outcomes[1].Result.ConflictsDropped != 1 {
		t.Fatalf("conflicts = %d, want 1", outcomes[1].Result.ConflictsDropped)
	}
	assertCategory(t, outcomes[2].Err, CategoryForbidden)
	if got := messages(t, db.Reader()); strings.Join(got, ",") != "original,after rejection" {
		t.Fatalf("committed messages = %v", got)
	}
}

func TestPersistGroupTransactionFailureFailsEveryBatch(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	publisher := &visibilityPublisher{reader: db.Reader()}
	writer.publisher = publisher
	serverID := insertTestServer(t, db.Writer())
	writer.beforeInsert = failOnCall(2, &database.CategoryError{
		Category: database.CategoryBusy, Operation: "injected busy",
	})
	outcomes := writer.PersistGroup(context.Background(), []*WriteBatch{
		testBatch(serverID, serviceEvent(serverID, "api", "", "first", 1)),
		testBatch(serverID, serviceEvent(serverID, "worker", "", "second", 2)),
		testBatch(serverID, serviceEvent(serverID, "api", "", "third", 3)),
	})
	for index, outcome := range outcomes {
		assertCategory(t, outcome.Err, CategoryUnavailable)
		if statusFor(outcome.Err) != 503 {
			t.Fatalf("batch %d status = %d", index, statusFor(outcome.Err))
		}
	}
	assertCounts(t, db.Reader(), 0, 0, 0)
	if writer.sources.Len() != 0 || writer.containers.Len() != 0 {
		t.Fatalf("caches filled from a rolled-back group: %d/%d", writer.sources.Len(), writer.containers.Len())
	}
	if calls, _ := publisher.snapshot(); len(calls) != 0 {
		t.Fatalf("published from a rolled-back group: %+v", calls)
	}

	// The writer connection is usable again for the next group.
	writer.beforeInsert = nil
	outcomes = writer.PersistGroup(context.Background(), []*WriteBatch{
		testBatch(serverID, serviceEvent(serverID, "api", "", "retried", 4)),
	})
	if outcomes[0].Err != nil {
		t.Fatalf("next group: %v", outcomes[0].Err)
	}
	assertCounts(t, db.Reader(), 1, 1, 1)
}

func TestPersistGroupStorageFullFailsEveryBatchAndRecovers(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	if err := writer.Persist(context.Background(), testBatch(serverID,
		serviceEvent(serverID, "api", "baseline", "retained", 1))); err != nil {
		t.Fatal(err)
	}
	var pages int
	if err := db.Writer().QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec("PRAGMA max_page_count=" + strconv.Itoa(pages)); err != nil {
		t.Fatal(err)
	}
	group := func() []*WriteBatch {
		return []*WriteBatch{
			testBatch(serverID, serviceEvent(serverID, "api", "small", "small", 2)),
			testBatch(serverID, serviceEvent(serverID, "api", "large", strings.Repeat("x", 64<<10), 3)),
		}
	}
	for _, outcome := range writer.PersistGroup(context.Background(), group()) {
		assertCategory(t, outcome.Err, CategoryStorageFull)
	}
	assertCounts(t, db.Reader(), 1, 1, 1)

	if _, err := db.Writer().Exec("PRAGMA max_page_count=" + strconv.Itoa(pages+1024)); err != nil {
		t.Fatal(err)
	}
	for index, outcome := range writer.PersistGroup(context.Background(), group()) {
		if outcome.Err != nil {
			t.Fatalf("batch %d after recovery: %v", index, outcome.Err)
		}
	}
	assertCounts(t, db.Reader(), 1, 1, 3)
}

func TestPersistGroupForeignKeyRetryRollsBackOnlyThatBatch(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	persistEvents(t, writer, serverID, serviceEvent(serverID, "cached", "", "warm", 1))
	cachedID := sourceIDFor(t, db.Reader(), "cached")

	attempts := 0
	writer.beforeInsert = func(tx database.Statements) error {
		attempts++
		if attempts != 2 {
			return nil
		}
		// The second batch's cached source vanishes after verification, so
		// its INSERT violates the foreign key and only its savepoint rolls
		// back before the uncached retry.
		_, err := tx.ExecContext(context.Background(), "DELETE FROM sources WHERE id=?", cachedID)
		return err
	}
	outcomes := writer.PersistGroup(context.Background(), []*WriteBatch{
		testBatch(serverID, serviceEvent(serverID, "other", "", "independent", 2)),
		testBatch(serverID, serviceEvent(serverID, "cached", "", "retried", 3)),
	})
	if outcomes[0].Err != nil || outcomes[1].Err != nil {
		t.Fatalf("outcomes = %v / %v", outcomes[0].Err, outcomes[1].Err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if got := messages(t, db.Reader()); strings.Join(got, ",") != "warm,independent,retried" {
		t.Fatalf("committed messages = %v", got)
	}
}

func TestPersistGroupRejectsEmptyMembersWithoutBlockingOthers(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	outcomes := writer.PersistGroup(context.Background(), []*WriteBatch{
		nil, testBatch(serverID), testBatch(serverID, serviceEvent(serverID, "api", "", "kept", 1)),
	})
	assertCategory(t, outcomes[0].Err, CategoryUnavailable)
	assertCategory(t, outcomes[1].Err, CategoryUnavailable)
	if outcomes[2].Err != nil {
		t.Fatal(outcomes[2].Err)
	}
	assertEventCount(t, db.Reader(), 1)
}

type groupQueue struct {
	admission *Admission
	queue     *Queue
}

func newGroupQueue() groupQueue {
	admission := NewAdmission(AdmissionLimits{
		MaxDecoders: 64, ResidentMaxEvents: 1000, ResidentMaxBytes: 1 << 30,
		QueueMaxEvents: 1000, QueueMaxBytes: 1 << 30,
	})
	return groupQueue{admission: admission, queue: NewQueue(admission)}
}

// enqueue admits one batch carrying events with approxBytes of retained
// accounting, exactly as the decoder hands it to the queue.
func (g groupQueue) enqueue(t *testing.T, serverID, approxBytes int64, events ...logs.CanonicalEvent) *WriteBatch {
	t.Helper()
	lease, err := g.admission.beginDecode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.add(int64(len(events)), approxBytes); err != nil {
		t.Fatal(err)
	}
	lease.decodingDone()
	batch := NewWriteBatch(DecodedBatch{Events: events, ApproxBytes: approxBytes, lease: lease}, "group", serverID)
	if err := g.queue.Enqueue(batch); err != nil {
		t.Fatal(err)
	}
	return batch
}

func TestWriterWorkerAcknowledgesPublishesAndReleasesOnlyAfterGroupCommit(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	publisher := &visibilityPublisher{reader: db.Reader()}
	writer.publisher = publisher
	serverID := insertTestServer(t, db.Writer())
	reached, release := make(chan struct{}), make(chan struct{})
	calls := 0
	writer.beforeInsert = func(database.Statements) error {
		calls++
		if calls == 3 {
			close(reached)
			<-release
		}
		return nil
	}

	g := newGroupQueue()
	batches := []*WriteBatch{
		g.enqueue(t, serverID, 100, serviceEvent(serverID, "api", "", "one", 1)),
		g.enqueue(t, serverID, 100, serviceEvent(serverID, "api", "", "two", 2)),
		g.enqueue(t, serverID, 100, serviceEvent(serverID, "worker", "", "three", 3)),
	}
	g.queue.Close()
	done := make(chan error, 1)
	go func() { done <- NewWriterWorker(g.queue, writer).Run(context.Background()) }()

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("group did not reach the third batch")
	}
	// The first two batches are written but not committed: they are in the
	// same transaction as the third, so nothing is acknowledged, visible,
	// published, or released yet.
	for index, batch := range batches[:2] {
		select {
		case err := <-batch.Result:
			t.Fatalf("batch %d acknowledged before commit: %v", index, err)
		default:
		}
	}
	assertEventCount(t, db.Reader(), 0)
	if published, _ := publisher.snapshot(); len(published) != 0 {
		t.Fatalf("published before commit: %+v", published)
	}
	if stats := g.admission.Stats(); stats.QueuedEvents != 3 || stats.ResidentEvents != 3 {
		t.Fatalf("accounting released before commit: %+v", stats)
	}

	close(release)
	for index, batch := range batches {
		if err := <-batch.Result; err != nil {
			t.Fatalf("batch %d: %v", index, err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertEventCount(t, db.Reader(), 3)
	published, hidden := publisher.snapshot()
	if len(published) != 3 || hidden != 0 {
		t.Fatalf("publications = %d hidden = %d", len(published), hidden)
	}
	for index := 1; index < len(published); index++ {
		if published[index][0].ID <= published[index-1][0].ID {
			t.Fatalf("publications out of ID order: %+v", published)
		}
	}
	if stats := g.admission.Stats(); stats != (AdmissionStats{}) {
		t.Fatalf("accounting after commit = %+v", stats)
	}
}

func TestWriterWorkerShutdownDrainsQueuedBatchesThroughGroups(t *testing.T) {
	db, _, writer := newWriterTest(t, WriterOptions{})
	serverID := insertTestServer(t, db.Writer())
	g := newGroupQueue()
	var batches []*WriteBatch
	for index := 0; index < 7; index++ {
		batches = append(batches, g.enqueue(t, serverID, 100,
			serviceEvent(serverID, "api", "", "drained-"+strconv.Itoa(index), int64(index+1))))
	}
	g.queue.Close()
	worker := NewWriterWorker(g.queue, writer).WithGroupLimits(GroupLimits{MaxBatches: 3, MaxBytes: 1 << 20})
	if err := worker.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for index, batch := range batches {
		if err := <-batch.Result; err != nil {
			t.Fatalf("batch %d: %v", index, err)
		}
	}
	assertEventCount(t, db.Reader(), 7)
}

func TestWriterWorkerCollectEnforcesGroupBounds(t *testing.T) {
	event := canonicalEvent(1, "", "bounded", 1)
	for _, tt := range []struct {
		name       string
		limits     GroupLimits
		bytes      []int64
		wantGroup  int
		wantCarry  bool
		wantQueued int64
	}{
		{"batch count", GroupLimits{MaxBatches: 2, MaxBytes: 1 << 20}, []int64{10, 10, 10, 10}, 2, false, 2},
		{"byte total", GroupLimits{MaxBatches: 64, MaxBytes: 25}, []int64{10, 10, 10, 10}, 2, true, 1},
		{"oversized first batch", GroupLimits{MaxBatches: 64, MaxBytes: 25}, []int64{100, 10, 10}, 1, false, 2},
		{"exact byte bound", GroupLimits{MaxBatches: 64, MaxBytes: 30}, []int64{10, 10, 10, 10}, 3, true, 0},
		{"single batch groups", GroupLimits{MaxBatches: 1, MaxBytes: 1 << 20}, []int64{10, 10}, 1, false, 1},
		{"empty queue", DefaultGroupLimits(), []int64{10}, 1, false, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := newGroupQueue()
			var enqueued []*WriteBatch
			for _, bytes := range tt.bytes {
				enqueued = append(enqueued, g.enqueue(t, 1, bytes, event))
			}
			worker := NewWriterWorker(g.queue, &BatchWriter{}).WithGroupLimits(tt.limits)
			first, err := g.queue.Dequeue(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			group, carry := worker.collect(first)
			if len(group) != tt.wantGroup || (carry != nil) != tt.wantCarry {
				t.Fatalf("group = %d carry = %t", len(group), carry != nil)
			}
			for index, batch := range group {
				if batch != enqueued[index] {
					t.Fatalf("group member %d out of queue order", index)
				}
			}
			if carry != nil && carry != enqueued[len(group)] {
				t.Fatal("carried batch is not the next in queue order")
			}
			var total int64
			for _, batch := range group {
				total += batch.ApproxBytes
			}
			if len(group) > 1 && total > tt.limits.MaxBytes {
				t.Fatalf("group bytes %d exceed bound %d", total, tt.limits.MaxBytes)
			}
			remaining := int64(0)
			for {
				if _, ok := g.queue.next(nil); !ok {
					break
				}
				remaining++
			}
			if remaining != tt.wantQueued {
				t.Fatalf("still queued = %d, want %d", remaining, tt.wantQueued)
			}
		})
	}
}

func TestWriterWorkerCollectWaitsOnlyUpToMaxWait(t *testing.T) {
	event := canonicalEvent(1, "", "waited", 1)

	t.Run("zero wait takes only ready batches", func(t *testing.T) {
		g := newGroupQueue()
		first := g.enqueue(t, 1, 10, event)
		dequeued, _ := g.queue.Dequeue(context.Background())
		worker := NewWriterWorker(g.queue, &BatchWriter{})
		started := time.Now()
		group, _ := worker.collect(dequeued)
		if len(group) != 1 || group[0] != first || time.Since(started) > 50*time.Millisecond {
			t.Fatalf("group = %d after %s", len(group), time.Since(started))
		}
	})

	t.Run("positive wait collects a late batch", func(t *testing.T) {
		g := newGroupQueue()
		g.enqueue(t, 1, 10, event)
		dequeued, _ := g.queue.Dequeue(context.Background())
		worker := NewWriterWorker(g.queue, &BatchWriter{}).WithGroupLimits(GroupLimits{
			MaxBatches: 2, MaxBytes: 1 << 20, MaxWait: MaxGroupMaxWait,
		})
		go func() {
			time.Sleep(5 * time.Millisecond)
			g.enqueue(t, 1, 10, event)
		}()
		if group, _ := worker.collect(dequeued); len(group) != 2 {
			t.Fatalf("group = %d, want 2", len(group))
		}
	})

	t.Run("positive wait is bounded", func(t *testing.T) {
		g := newGroupQueue()
		g.enqueue(t, 1, 10, event)
		dequeued, _ := g.queue.Dequeue(context.Background())
		wait := 20 * time.Millisecond
		worker := NewWriterWorker(g.queue, &BatchWriter{}).WithGroupLimits(GroupLimits{
			MaxBatches: 2, MaxBytes: 1 << 20, MaxWait: wait,
		})
		started := time.Now()
		group, _ := worker.collect(dequeued)
		elapsed := time.Since(started)
		if len(group) != 1 || elapsed < wait || elapsed > wait+time.Second {
			t.Fatalf("group = %d after %s", len(group), elapsed)
		}
	})
}

func TestWriterWorkerRejectsGroupLimitsOutsideDocumentedRange(t *testing.T) {
	for _, limits := range []GroupLimits{
		{MaxBatches: 0, MaxBytes: 1},
		{MaxBatches: MaxGroupMaxBatches + 1, MaxBytes: 1},
		{MaxBatches: 1, MaxBytes: 0},
		{MaxBatches: 1, MaxBytes: 1, MaxWait: -time.Millisecond},
		{MaxBatches: 1, MaxBytes: 1, MaxWait: MaxGroupMaxWait + time.Millisecond},
	} {
		g := newGroupQueue()
		if err := NewWriterWorker(g.queue, &BatchWriter{}).WithGroupLimits(limits).Run(context.Background()); err == nil {
			t.Fatalf("limits %+v accepted", limits)
		}
	}
	if err := DefaultGroupLimits().Validate(); err != nil {
		t.Fatalf("default limits rejected: %v", err)
	}
}

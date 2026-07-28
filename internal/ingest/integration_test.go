package ingest

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/sources"
)

type ingestionIntegration struct {
	db              *database.DB
	coordinator     *database.Coordinator
	admission       *Admission
	queue           *Queue
	handler         *Handler
	token           string
	coordinatorStop context.CancelFunc
	coordinatorDone chan error
	writerDone      chan error
	observer        *recordingIngestObserver
}

type recordingIngestObserver struct {
	mu              sync.Mutex
	acceptedBatches int
	acceptedEvents  int
	rejectedBatches int
	databaseFailure bool
}

func (o *recordingIngestObserver) RecordIngestAccepted(events int, _ time.Time) {
	o.mu.Lock()
	o.acceptedBatches++
	o.acceptedEvents += events
	o.mu.Unlock()
}

func (o *recordingIngestObserver) RecordIngestRejected(
	_ ErrorCategory,
	databaseFailure bool,
	_ time.Time,
) {
	o.mu.Lock()
	o.rejectedBatches++
	o.databaseFailure = o.databaseFailure || databaseFailure
	o.mu.Unlock()
}

func newIngestionIntegration(t *testing.T, queueEvents, queueBytes int64) *ingestionIntegration {
	return newIngestionIntegrationWithPublisher(t, queueEvents, queueBytes, nil)
}

func newIngestionIntegrationWithPublisher(
	t *testing.T,
	queueEvents, queueBytes int64,
	publisher Publisher,
) *ingestionIntegration {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	adminStore := sources.NewStore(db.Writer())
	server, err := adminStore.CreateServer(context.Background(), "Integration", "")
	if err != nil {
		t.Fatal(err)
	}
	token, err := adminStore.CreateToken(context.Background(), server.ID, "test")
	if err != nil {
		t.Fatal(err)
	}

	coordinatorCtx, coordinatorStop := context.WithCancel(context.Background())
	coordinator := database.NewCoordinator(db.Writer())
	coordinatorDone := make(chan error, 1)
	go func() { coordinatorDone <- coordinator.Run(coordinatorCtx) }()
	<-coordinator.Ready()

	admission := NewAdmission(AdmissionLimits{
		MaxDecoders: 2, ResidentMaxEvents: 100, ResidentMaxBytes: 4 << 20,
		QueueMaxEvents: queueEvents, QueueMaxBytes: queueBytes,
	})
	queue := NewQueue(admission)
	observer := &recordingIngestObserver{}
	decoder := NewJSONDecoder(DecoderLimits{
		MaxCompressedBytes: 1 << 20, MaxDecompressedBytes: 2 << 20,
		MaxEventBytes: 1 << 20, MaxEvents: 100, MaxJSONDepth: 32,
	}).WithAdmission(admission)
	handler := NewHandler(sources.NewCoordinatedStore(db.Reader(), coordinator), decoder, Limits{
		MaxCompressedBytes: 1 << 20, RequestTimeout: time.Second,
	}).WithQueue(queue).WithObserver(observer)
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- NewWriterWorker(
			queue, NewBatchWriter(coordinator, publisher),
		).WithObserver(observer).Run(context.Background())
	}()

	fixture := &ingestionIntegration{
		db: db, coordinator: coordinator, admission: admission, queue: queue,
		handler: handler, token: token.Token, coordinatorStop: coordinatorStop,
		coordinatorDone: coordinatorDone, writerDone: writerDone,
		observer: observer,
	}
	t.Cleanup(func() {
		admission.Close()
		queue.Close()
		<-writerDone
		coordinator.Close()
		coordinatorStop()
		<-coordinatorDone
		_ = db.Close()
	})
	return fixture
}

func TestIngestionObserverCountsDurableAndTransportOutcomes(t *testing.T) {
	fixture := newIngestionIntegration(t, 10, 1<<20)
	success, done := fixture.request(
		context.Background(), testEventJSON("", "private-observer-payload"),
	)
	<-done
	if success.Code != http.StatusNoContent {
		t.Fatalf("success status = %d", success.Code)
	}
	unauthorized := httptest.NewRequest(
		http.MethodPost, "/api/v1/ingest", strings.NewReader(`{"log":"private"}`),
	)
	unauthorized.Header.Set("Content-Type", "application/json")
	rejected := httptest.NewRecorder()
	fixture.handler.ServeHTTP(rejected, unauthorized)
	if rejected.Code != http.StatusUnauthorized {
		t.Fatalf("rejected status = %d", rejected.Code)
	}
	fixture.observer.mu.Lock()
	defer fixture.observer.mu.Unlock()
	if fixture.observer.acceptedBatches != 1 ||
		fixture.observer.acceptedEvents != 1 ||
		fixture.observer.rejectedBatches != 1 ||
		fixture.observer.databaseFailure {
		t.Fatalf("observer = %#v", fixture.observer)
	}
}

func TestIngestionPublishesOnlyAfterCommit(t *testing.T) {
	broker := logs.NewLiveBroker(logs.LiveBrokerOptions{})
	brokerDone := make(chan error, 1)
	go func() { brokerDone <- broker.Run(context.Background()) }()
	<-broker.Ready()
	t.Cleanup(func() {
		broker.Stop()
		<-brokerDone
	})

	fixture := newIngestionIntegrationWithPublisher(t, 10, 1<<20, broker)
	subscription, err := broker.Subscribe(context.Background(), logs.LiveFilter{})
	if err != nil {
		t.Fatal(err)
	}
	release := fixture.blockCoordinator(t)
	response, requestDone := fixture.request(context.Background(), testEventJSON("live-commit", "committed"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := subscription.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("event published before commit: %v", err)
	}
	close(release)
	<-requestDone
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body=%q", response.Code, response.Body.String())
	}
	message := nextIntegrationLive(t, subscription)
	if message.Event.ID <= 0 || message.Event.SourceID <= 0 ||
		message.Event.ContainerInstanceID <= 0 ||
		message.Event.Event.MessageText != "committed" {
		t.Fatalf("published event = %#v", message.Event)
	}
}

func TestIngestionDoesNotPublishRolledBackBatch(t *testing.T) {
	broker := logs.NewLiveBroker(logs.LiveBrokerOptions{})
	brokerDone := make(chan error, 1)
	go func() { brokerDone <- broker.Run(context.Background()) }()
	<-broker.Ready()
	t.Cleanup(func() {
		broker.Stop()
		<-brokerDone
	})

	fixture := newIngestionIntegrationWithPublisher(t, 10, 1<<20, broker)
	subscription, err := broker.Subscribe(context.Background(), logs.LiveFilter{})
	if err != nil {
		t.Fatal(err)
	}
	first, firstDone := fixture.request(context.Background(), testEventJSON("stable-live", "original"))
	<-firstDone
	if first.Code != http.StatusNoContent {
		t.Fatalf("initial status = %d", first.Code)
	}
	_ = nextIntegrationLive(t, subscription)

	body := `[` + testEventJSON("new-live", "must roll back") + `,` +
		testEventJSON("stable-live", "conflict") + `]`
	response, requestDone := fixture.request(context.Background(), body)
	<-requestDone
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body=%q", response.Code, response.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := subscription.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rolled-back event was published: %v", err)
	}
	assertEventCount(t, fixture.db.Reader(), 1)
	var lastUsed sql.NullInt64
	if err := fixture.db.Reader().QueryRow(
		`SELECT last_used_at_us FROM ingestion_tokens WHERE name='test'`,
	).Scan(&lastUsed); err != nil || !lastUsed.Valid {
		t.Fatalf("committed token last use = %#v, err=%v", lastUsed, err)
	}
}

func (f *ingestionIntegration) request(ctx context.Context, body string) (*httptest.ResponseRecorder, chan struct{}) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ingest", strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+f.token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.handler.ServeHTTP(response, request)
	}()
	return response, done
}

func (f *ingestionIntegration) blockCoordinator(t *testing.T) chan struct{} {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = f.coordinator.Do(context.Background(), func(*sql.Tx) error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("coordinator blocker did not start")
	}
	return release
}

func TestIngestionAcknowledgesOnlyAfterCommit(t *testing.T) {
	fixture := newIngestionIntegration(t, 10, 1<<20)
	release := fixture.blockCoordinator(t)
	response, done := fixture.request(context.Background(), testEventJSON("commit-1", "committed"))

	select {
	case <-done:
		t.Fatalf("handler returned before commit with status %d", response.Code)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not return after commit")
	}
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body=%q", response.Code, response.Body.String())
	}
	assertEventCount(t, fixture.db.Reader(), 1)
	if stats := fixture.admission.Stats(); stats.ResidentEvents != 0 || stats.QueuedEvents != 0 {
		t.Fatalf("capacity not released before acknowledgement: %#v", stats)
	}
}

func TestIngestionLastRecordConflictRollsBackRequest(t *testing.T) {
	fixture := newIngestionIntegration(t, 10, 1<<20)
	first, done := fixture.request(context.Background(), testEventJSON("stable", "original"))
	<-done
	if first.Code != http.StatusNoContent {
		t.Fatalf("initial status = %d", first.Code)
	}

	body := `[` + testEventJSON("new", "must roll back") + `,` + testEventJSON("stable", "conflict") + `]`
	response, done := fixture.request(context.Background(), body)
	<-done
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body=%q", response.Code, response.Body.String())
	}
	assertEventCount(t, fixture.db.Reader(), 1)
}

func TestIngestionCancellationAfterAdmissionStillCommits(t *testing.T) {
	fixture := newIngestionIntegration(t, 10, 1<<20)
	release := fixture.blockCoordinator(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, done := fixture.request(ctx, testEventJSON("", "ambiguous"))
	waitForQueuedEvents(t, fixture.admission, 1)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled handler did not leave")
	}
	close(release)
	waitForEventCount(t, fixture.db.Reader(), 1)
	waitForQueuedEvents(t, fixture.admission, 0)
}

func TestIngestionQueueSaturationIsRetryable(t *testing.T) {
	fixture := newIngestionIntegration(t, 1, 1<<20)
	release := fixture.blockCoordinator(t)
	_, firstDone := fixture.request(context.Background(), testEventJSON("", "first"))
	waitForQueuedEvents(t, fixture.admission, 1)

	second, secondDone := fixture.request(context.Background(), testEventJSON("", "second"))
	<-secondDone
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("saturation status = %d", second.Code)
	}
	close(release)
	<-firstDone
	assertEventCount(t, fixture.db.Reader(), 1)
}

func TestIngestionClosedAdmissionIsRetryable(t *testing.T) {
	fixture := newIngestionIntegration(t, 10, 1<<20)
	fixture.admission.Close()
	response, done := fixture.request(context.Background(), testEventJSON("", "shutdown"))
	<-done
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed admission status = %d", response.Code)
	}
	assertEventCount(t, fixture.db.Reader(), 0)
}

func TestWriterWorkerDrainsClosedQueue(t *testing.T) {
	fixture := newIngestionIntegration(t, 10, 1<<20)
	release := fixture.blockCoordinator(t)
	first, firstDone := fixture.request(context.Background(), testEventJSON("", "first"))
	second, secondDone := fixture.request(context.Background(), testEventJSON("", "second"))
	waitForQueuedEvents(t, fixture.admission, 2)
	fixture.admission.Close()
	fixture.queue.Close()
	close(release)
	<-firstDone
	<-secondDone
	if first.Code != http.StatusNoContent || second.Code != http.StatusNoContent {
		t.Fatalf("drained statuses = %d/%d", first.Code, second.Code)
	}
	assertEventCount(t, fixture.db.Reader(), 2)
}

func testEventJSON(sourceEventID, message string) string {
	id := ""
	if sourceEventID != "" {
		id = `,"source_event_id":"` + sourceEventID + `"`
	}
	return `{"timestamp":"2026-07-28T00:00:00Z","project":"p","environment":"e",` +
		`"application":"a","service":"api","container_id":"container-1",` +
		`"stream":"stdout","level":"info","log":"` + message + `"` + id + `}`
}

func assertEventCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT count(*) FROM log_events").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("event count = %d, want %d", got, want)
	}
}

func waitForEventCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var got int
		if err := db.QueryRow("SELECT count(*) FROM log_events").Scan(&got); err == nil && got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	assertEventCount(t, db, want)
}

func waitForQueuedEvents(t *testing.T, admission *Admission, want int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if admission.Stats().QueuedEvents == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queued events = %d, want %d", admission.Stats().QueuedEvents, want)
}

func nextIntegrationLive(t *testing.T, subscription *logs.LiveSubscription) logs.LiveMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	message, err := subscription.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

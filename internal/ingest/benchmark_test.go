package ingest

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/sources"
)

func BenchmarkDecodeBatch100(b *testing.B) {
	var body strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&body, `{"timestamp":"2026-07-28T08:00:00Z","application":"app","service":"api","log":"message-%d","request_id":"request-%d"}`+"\n", i, i)
	}
	payload := []byte(body.String())
	admission := NewAdmission(AdmissionLimits{
		MaxDecoders: 1, ResidentMaxEvents: 101, ResidentMaxBytes: 4 << 20,
		QueueMaxEvents: 101, QueueMaxBytes: 4 << 20,
	})
	decoder := NewJSONDecoder(DecoderLimits{
		MaxCompressedBytes: 1 << 20, MaxDecompressedBytes: 2 << 20,
		MaxEventBytes: 1 << 20, MaxEvents: 100, MaxJSONDepth: 32,
	}).WithAdmission(admission)
	request := DecodeRequest{
		MediaType: "application/x-ndjson", ReceivedAt: time.Unix(1, 0),
		Server: logs.TrustedServer{ID: 1},
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		request.Body = bytes.NewReader(payload)
		batch, err := decoder.Decode(context.Background(), request)
		if err != nil {
			b.Fatal(err)
		}
		batch.lease.release()
	}
}

func BenchmarkQueueAdmission(b *testing.B) {
	admission := NewAdmission(AdmissionLimits{
		MaxDecoders: 1, ResidentMaxEvents: 2, ResidentMaxBytes: 4096,
		QueueMaxEvents: 2, QueueMaxBytes: 4096,
	})
	queue := NewQueue(admission)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lease, err := admission.beginDecode(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		if err := lease.add(1, 1024); err != nil {
			b.Fatal(err)
		}
		lease.decodingDone()
		batch := NewWriteBatch(DecodedBatch{
			Events:      []logs.CanonicalEvent{{MessageText: "representative"}},
			ApproxBytes: 1024, lease: lease,
		}, "benchmark", 1)
		if err := queue.Enqueue(batch); err != nil {
			b.Fatal(err)
		}
		dequeued, err := queue.Dequeue(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		dequeued.Complete(nil)
		<-dequeued.Result
	}
}

func BenchmarkBatchWriterSQLiteCommit(b *testing.B) {
	db, coordinator, writer, serverID := benchmarkWriter(b, WriterOptions{})
	_ = db
	event := canonicalEvent(serverID, "", "benchmark commit", 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		event.SourceEventID = fmt.Sprintf("commit-%d", i)
		event.EventAtUS = int64(i + 1)
		if err := writer.Persist(context.Background(), &WriteBatch{
			Events: []logs.CanonicalEvent{event}, AuthenticatedServerID: serverID,
		}); err != nil {
			b.Fatal(err)
		}
	}
	coordinator.Close()
}

func BenchmarkBatchWriterSourceResolution(b *testing.B) {
	db, coordinator, writer, serverID := benchmarkWriter(b, WriterOptions{
		SourceLimit: int(^uint(0) >> 1),
	})
	_ = db
	event := canonicalEvent(serverID, "", "source resolution", 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		event.Source.Service = fmt.Sprintf("service-%d", i)
		event.Source.ServiceLabel = event.Source.Service
		event.SourceEventID = fmt.Sprintf("source-%d", i)
		if err := writer.Persist(context.Background(), &WriteBatch{
			Events: []logs.CanonicalEvent{event}, AuthenticatedServerID: serverID,
		}); err != nil {
			b.Fatal(err)
		}
	}
	coordinator.Close()
}

func BenchmarkBatchWriterIdempotentRetry(b *testing.B) {
	db, coordinator, writer, serverID := benchmarkWriter(b, WriterOptions{})
	_ = db
	event := canonicalEvent(serverID, "idempotent", "same event", 1)
	batch := &WriteBatch{Events: []logs.CanonicalEvent{event}, AuthenticatedServerID: serverID}
	if err := writer.Persist(context.Background(), batch); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		event.ReceivedAtUS = int64(i + 100)
		if err := writer.Persist(context.Background(), &WriteBatch{
			Events: []logs.CanonicalEvent{event}, AuthenticatedServerID: serverID,
		}); err != nil {
			b.Fatal(err)
		}
	}
	coordinator.Close()
}

func BenchmarkHTTPCommitLatency(b *testing.B) {
	fixture := benchmarkHTTPFixture(b)
	durations := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := fmt.Sprintf(`{"timestamp":"2026-07-28T08:00:00Z","application":"benchmark","service":"api","log":"commit","source_event_id":"http-%d"}`, i)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/ingest", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+fixture.token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		started := time.Now()
		fixture.handler.ServeHTTP(response, request)
		durations = append(durations, time.Since(started))
		if response.Code != http.StatusNoContent {
			b.Fatalf("status = %d", response.Code)
		}
	}
	b.StopTimer()
	reportLatencyPercentiles(b, durations)
}

func BenchmarkHTTPSustained100EventBatch(b *testing.B) {
	fixture := benchmarkHTTPFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var body strings.Builder
		body.WriteByte('[')
		for event := 0; event < 100; event++ {
			if event > 0 {
				body.WriteByte(',')
			}
			fmt.Fprintf(&body, `{"timestamp":"2026-07-28T08:00:00Z","application":"benchmark","service":"api","log":"sustained-%d","source_event_id":"batch-%d-%d"}`, event, i, event)
		}
		body.WriteByte(']')
		request := httptest.NewRequest(http.MethodPost, "/api/v1/ingest", strings.NewReader(body.String()))
		request.Header.Set("Authorization", "Bearer "+fixture.token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		fixture.handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			b.Fatalf("status = %d", response.Code)
		}
	}
	b.StopTimer()
	elapsed := b.Elapsed().Seconds()
	if elapsed > 0 {
		b.ReportMetric(float64(b.N*100)/elapsed, "events/s")
	}
}

func BenchmarkQueueSaturationRSS(b *testing.B) {
	const events = 8192
	const retainedBytes = int64(4096)
	for i := 0; i < b.N; i++ {
		admission := NewAdmission(AdmissionLimits{
			MaxDecoders: 1, ResidentMaxEvents: events + 1, ResidentMaxBytes: (events + 1) * retainedBytes,
			QueueMaxEvents: events, QueueMaxBytes: events * retainedBytes,
		})
		queue := NewQueue(admission)
		before := residentSetBytes()
		for event := 0; event < events; event++ {
			lease, err := admission.beginDecode(context.Background())
			if err != nil {
				b.Fatal(err)
			}
			if err := lease.add(1, retainedBytes); err != nil {
				b.Fatal(err)
			}
			lease.decodingDone()
			message := strings.Repeat("x", int(retainedBytes/2))
			batch := NewWriteBatch(DecodedBatch{
				Events:      []logs.CanonicalEvent{{MessageRaw: []byte(message), MessageText: message}},
				ApproxBytes: retainedBytes, lease: lease,
			}, "rss", 1)
			if err := queue.Enqueue(batch); err != nil {
				b.Fatal(err)
			}
		}
		stats := admission.Stats()
		after := residentSetBytes()
		b.ReportMetric(float64(stats.QueuedBytes), "ledger-bytes")
		if after >= before {
			b.ReportMetric(float64(after-before), "rss-delta-bytes")
		}
		queue.Drain(ErrQueueClosed)
	}
}

type benchmarkHTTP struct {
	handler *Handler
	token   string
}

func benchmarkHTTPFixture(b *testing.B) benchmarkHTTP {
	b.Helper()
	db, coordinator, writer, serverID := benchmarkWriter(b, WriterOptions{})
	store := sources.NewStore(db.Reader())
	token, err := sources.NewCoordinatedStore(db.Reader(), coordinator).CreateToken(
		context.Background(), serverID, "benchmark",
	)
	if err != nil {
		b.Fatal(err)
	}
	admission := NewAdmission(AdmissionLimits{
		MaxDecoders: 1, ResidentMaxEvents: 1000, ResidentMaxBytes: 8 << 20,
		QueueMaxEvents: 1000, QueueMaxBytes: 8 << 20,
	})
	queue := NewQueue(admission)
	decoder := NewJSONDecoder(DecoderLimits{
		MaxCompressedBytes: 1 << 20, MaxDecompressedBytes: 4 << 20,
		MaxEventBytes: 1 << 20, MaxEvents: 1000, MaxJSONDepth: 32,
	}).WithAdmission(admission)
	handler := NewHandler(store, decoder, Limits{
		MaxCompressedBytes: 1 << 20, RequestTimeout: 5 * time.Second,
	}).WithQueue(queue)
	workerDone := make(chan error, 1)
	go func() { workerDone <- NewWriterWorker(queue, writer).Run(context.Background()) }()
	b.Cleanup(func() {
		admission.Close()
		queue.Close()
		<-workerDone
	})
	return benchmarkHTTP{handler: handler, token: token.Token}
}

func benchmarkWriter(b *testing.B, options WriterOptions) (*database.DB, *database.Coordinator, *BatchWriter, int64) {
	b.Helper()
	db, err := database.Open(context.Background(), filepath.Join(b.TempDir(), "siftail.db"))
	if err != nil {
		b.Fatal(err)
	}
	result, err := db.Writer().Exec("INSERT INTO servers(name,created_at_us) VALUES('benchmark',1)")
	if err != nil {
		b.Fatal(err)
	}
	serverID, err := result.LastInsertId()
	if err != nil {
		b.Fatal(err)
	}
	coordinator := database.NewCoordinator(db.Writer())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	b.Cleanup(func() {
		coordinator.Close()
		cancel()
		<-done
		_ = db.Close()
	})
	return db, coordinator, NewBatchWriterWithOptions(coordinator, nil, options), serverID
}

func reportLatencyPercentiles(b *testing.B, durations []time.Duration) {
	if len(durations) == 0 {
		return
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	for label, percentile := range map[string]float64{"p50-ns": 0.50, "p95-ns": 0.95, "p99-ns": 0.99} {
		index := int(float64(len(durations)-1) * percentile)
		b.ReportMetric(float64(durations[index].Nanoseconds()), label)
	}
}

func residentSetBytes() uint64 {
	body, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kib, err := strconv.ParseUint(fields[1], 10, 64)
		if err == nil {
			return kib * 1024
		}
	}
	return 0
}

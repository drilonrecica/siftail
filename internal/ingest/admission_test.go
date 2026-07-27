package ingest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/logs"
)

func TestAdmissionDecoderSemaphoreAndCancellation(t *testing.T) {
	admission := NewAdmission(AdmissionLimits{
		MaxDecoders: 2, ResidentMaxEvents: 10, ResidentMaxBytes: 100,
		QueueMaxEvents: 5, QueueMaxBytes: 50,
	})
	first, err := admission.beginDecode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := admission.beginDecode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := admission.beginDecode(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third decoder error = %v", err)
	}
	first.release()
	third, err := admission.beginDecode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second.release()
	third.release()
	if got := admission.Stats(); got.Decoders != 0 {
		t.Fatalf("stats = %#v", got)
	}
}

func TestResidentAndQueueLedgersTransferWithoutDoubleCounting(t *testing.T) {
	admission := NewAdmission(AdmissionLimits{
		MaxDecoders: 1, ResidentMaxEvents: 5, ResidentMaxBytes: 50,
		QueueMaxEvents: 3, QueueMaxBytes: 30,
	})
	queue := NewQueue(admission)
	batch := makeResidentBatch(t, admission, 2, 20)
	before := admission.Stats()
	if before.ResidentEvents != 2 || before.ResidentBytes != 20 || before.QueuedEvents != 0 {
		t.Fatalf("before = %#v", before)
	}
	if err := queue.Enqueue(batch); err != nil {
		t.Fatal(err)
	}
	after := admission.Stats()
	if after.ResidentEvents != 2 || after.ResidentBytes != 20 ||
		after.QueuedEvents != 2 || after.QueuedBytes != 20 {
		t.Fatalf("after = %#v", after)
	}
	dequeued, err := queue.Dequeue(context.Background())
	if err != nil || dequeued != batch {
		t.Fatalf("dequeue = %p / %v", dequeued, err)
	}
	dequeued.Complete(nil)
	dequeued.Complete(errors.New("must be ignored"))
	if got := admission.Stats(); got.ResidentEvents != 0 || got.QueuedEvents != 0 || got.Decoders != 0 {
		t.Fatalf("released stats = %#v", got)
	}
	if result := <-dequeued.Result; result != nil {
		t.Fatalf("result = %v", result)
	}
}

func TestAggregateResidentLimitAndDecodeFailureRelease(t *testing.T) {
	admission := NewAdmission(AdmissionLimits{
		MaxDecoders: 2, ResidentMaxEvents: 2, ResidentMaxBytes: 20,
		QueueMaxEvents: 2, QueueMaxBytes: 20,
	})
	first := makeResidentBatch(t, admission, 2, 20)
	lease, err := admission.beginDecode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.add(1, 1); err == nil {
		t.Fatal("aggregate resident cap was exceeded")
	}
	lease.release()
	first.Complete(nil)

	decoder := testJSONDecoder(100, 100, 100, 10).WithAdmission(admission)
	if _, err := decoder.Decode(context.Background(), DecodeRequest{
		Body: strings.NewReader(`{"log":`), MediaType: "application/json",
		ReceivedAt: time.Unix(1, 0), Server: logs.TrustedServer{ID: 1},
	}); err == nil {
		t.Fatal("malformed decode succeeded")
	}
	if got := admission.Stats(); got.Decoders != 0 || got.ResidentEvents != 0 {
		t.Fatalf("decode failure leaked: %#v", got)
	}
}

func TestQueueSaturationRejectsWholeBatchAndReleases(t *testing.T) {
	admission := NewAdmission(AdmissionLimits{
		MaxDecoders: 2, ResidentMaxEvents: 10, ResidentMaxBytes: 100,
		QueueMaxEvents: 2, QueueMaxBytes: 20,
	})
	queue := NewQueue(admission)
	first := makeResidentBatch(t, admission, 2, 20)
	if err := queue.Enqueue(first); err != nil {
		t.Fatal(err)
	}
	rejected := makeResidentBatch(t, admission, 1, 1)
	err := queue.Enqueue(rejected)
	var ingestErr *Error
	if !errors.As(err, &ingestErr) || ingestErr.Category != CategoryUnavailable {
		t.Fatalf("rejection = %#v", err)
	}
	stats := admission.Stats()
	if stats.ResidentEvents != 2 || stats.QueuedEvents != 2 {
		t.Fatalf("rejected capacity leaked: %#v", stats)
	}
	if result := <-rejected.Result; result == nil {
		t.Fatal("rejected batch received success")
	}
	first.Complete(nil)
}

func TestQueuedBatchSurvivesCallerCancellation(t *testing.T) {
	admission := NewAdmission(AdmissionLimits{
		MaxDecoders: 1, ResidentMaxEvents: 2, ResidentMaxBytes: 20,
		QueueMaxEvents: 2, QueueMaxBytes: 20,
	})
	queue := NewQueue(admission)
	batch := makeResidentBatch(t, admission, 1, 10)
	if err := queue.Enqueue(batch); err != nil {
		t.Fatal(err)
	}
	handlerContext, cancel := context.WithCancel(context.Background())
	cancel()
	<-handlerContext.Done()
	if got := admission.Stats(); got.QueuedEvents != 1 {
		t.Fatalf("cancellation removed queued batch: %#v", got)
	}
	dequeued, err := queue.Dequeue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dequeued.Complete(nil)
}

func TestQueueCloseAndDrainReleasesEverything(t *testing.T) {
	admission := NewAdmission(AdmissionLimits{
		MaxDecoders: 2, ResidentMaxEvents: 4, ResidentMaxBytes: 40,
		QueueMaxEvents: 4, QueueMaxBytes: 40,
	})
	queue := NewQueue(admission)
	first := makeResidentBatch(t, admission, 1, 10)
	second := makeResidentBatch(t, admission, 1, 10)
	if err := queue.Enqueue(first); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(second); err != nil {
		t.Fatal(err)
	}
	queue.Drain(ErrQueueClosed)
	if got := admission.Stats(); got.ResidentEvents != 0 || got.QueuedEvents != 0 {
		t.Fatalf("drain stats = %#v", got)
	}
	if !errors.Is(<-first.Result, ErrQueueClosed) || !errors.Is(<-second.Result, ErrQueueClosed) {
		t.Fatal("drained batch did not receive shutdown result")
	}
	rejected := makeResidentBatch(t, admission, 1, 1)
	if err := queue.Enqueue(rejected); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("closed enqueue = %v", err)
	}
	if got := admission.Stats(); got.ResidentEvents != 0 {
		t.Fatalf("closed rejection leaked: %#v", got)
	}
}

func TestAdmissionConcurrentAccountingStress(t *testing.T) {
	admission := NewAdmission(AdmissionLimits{
		MaxDecoders: 4, ResidentMaxEvents: 100, ResidentMaxBytes: 1000,
		QueueMaxEvents: 100, QueueMaxBytes: 1000,
	})
	queue := NewQueue(admission)
	var producers sync.WaitGroup
	for i := 0; i < 8; i++ {
		producers.Add(1)
		go func() {
			defer producers.Done()
			for j := 0; j < 100; j++ {
				lease, err := admission.beginDecode(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				if err := lease.add(1, 1); err != nil {
					lease.release()
					continue
				}
				lease.decodingDone()
				batch := NewWriteBatch(DecodedBatch{
					Events:      []logs.CanonicalEvent{{MessageText: "x"}},
					ApproxBytes: 1, lease: lease,
				}, "stress", 1)
				if err := queue.Enqueue(batch); err == nil {
					dequeued, err := queue.Dequeue(context.Background())
					if err != nil {
						t.Error(err)
						return
					}
					dequeued.Complete(nil)
				}
			}
		}()
	}
	producers.Wait()
	queue.Drain(ErrQueueClosed)
	if got := admission.Stats(); got.Decoders != 0 || got.ResidentEvents != 0 || got.QueuedEvents != 0 {
		t.Fatalf("stress leaked accounting: %#v", got)
	}
}

func BenchmarkAdmissionAccounting(b *testing.B) {
	admission := NewAdmission(AdmissionLimits{
		MaxDecoders: 1, ResidentMaxEvents: int64(b.N + 1), ResidentMaxBytes: int64(b.N + 1),
		QueueMaxEvents: int64(b.N + 1), QueueMaxBytes: int64(b.N + 1),
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lease, _ := admission.beginDecode(context.Background())
		_ = lease.add(1, 1)
		lease.release()
	}
}

func makeResidentBatch(t *testing.T, admission *Admission, events int, bytes int64) *WriteBatch {
	t.Helper()
	lease, err := admission.beginDecode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.add(int64(events), bytes); err != nil {
		lease.release()
		t.Fatal(err)
	}
	lease.decodingDone()
	canonical := make([]logs.CanonicalEvent, events)
	return NewWriteBatch(DecodedBatch{Events: canonical, ApproxBytes: bytes, lease: lease}, "request", 1)
}

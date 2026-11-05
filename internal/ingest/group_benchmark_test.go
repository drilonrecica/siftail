package ingest

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/logs"
)

const groupBenchmarkEventSize = 512

// BenchmarkGroupCommitConcurrent measures the durable write path under
// concurrent producers: each producer submits batches through admission, the
// queue, and the writer worker, and waits for its outcome, as an HTTP client
// does. It reports batches and events per second and the p50/p95/p99 latency
// from enqueue to acknowledgement for each commit-group batch bound. The
// numbers only mean something with TMPDIR on a real disk (AGENTS.md §17.2); on
// tmpfs fsync costs nothing.
func BenchmarkGroupCommitConcurrent(b *testing.B) {
	for _, events := range []int{1, 10, 100} {
		for _, producers := range []int{16, 64} {
			for _, maxBatches := range []int{1, 16, DefaultGroupMaxBatches, 256} {
				name := fmt.Sprintf("events=%d/producers=%d/max-batches=%d", events, producers, maxBatches)
				b.Run(name, func(b *testing.B) {
					limits := DefaultGroupLimits()
					limits.MaxBatches = maxBatches
					runGroupCommitBenchmark(b, events, producers, func(worker *WriterWorker) *WriterWorker {
						return worker.WithGroupLimits(limits)
					})
				})
			}
		}
	}
}

func runGroupCommitBenchmark(
	b *testing.B, batchEvents, producers int, configure func(*WriterWorker) *WriterWorker,
) {
	b.Logf("TMPDIR=%q", os.TempDir())
	_, _, writer, serverID := benchmarkWriter(b, WriterOptions{})
	residentEvents := int64(producers * batchEvents)
	admission := NewAdmission(AdmissionLimits{
		MaxDecoders:       producers,
		ResidentMaxEvents: residentEvents, ResidentMaxBytes: residentEvents * groupBenchmarkEventSize,
		QueueMaxEvents: residentEvents, QueueMaxBytes: residentEvents * groupBenchmarkEventSize,
	})
	queue := NewQueue(admission)
	workerDone := make(chan error, 1)
	go func() { workerDone <- configure(NewWriterWorker(queue, writer)).Run(context.Background()) }()
	defer func() {
		queue.Close()
		if err := <-workerDone; err != nil {
			b.Fatal(err)
		}
	}()

	var next atomic.Int64
	latencies := make([][]time.Duration, producers)
	failures := make(chan error, producers)
	var wg sync.WaitGroup
	b.ReportAllocs()
	b.ResetTimer()
	started := time.Now()
	for producer := 0; producer < producers; producer++ {
		wg.Add(1)
		go func(producer int) {
			defer wg.Done()
			for {
				index := next.Add(1) - 1
				if index >= int64(b.N) {
					return
				}
				batch, err := groupBenchmarkBatch(admission, serverID, index, batchEvents)
				if err != nil {
					failures <- err
					return
				}
				enqueued := time.Now()
				if err := queue.Enqueue(batch); err != nil {
					failures <- err
					return
				}
				if err := <-batch.Result; err != nil {
					failures <- err
					return
				}
				latencies[producer] = append(latencies[producer], time.Since(enqueued))
			}
		}(producer)
	}
	wg.Wait()
	elapsed := time.Since(started)
	b.StopTimer()
	close(failures)
	if err := <-failures; err != nil {
		b.Fatal(err)
	}
	var all []time.Duration
	for _, producer := range latencies {
		all = append(all, producer...)
	}
	reportLatencyPercentiles(b, all)
	if seconds := elapsed.Seconds(); seconds > 0 {
		b.ReportMetric(float64(b.N)/seconds, "batches/s")
		b.ReportMetric(float64(b.N*batchEvents)/seconds, "events/s")
	}
}

func groupBenchmarkBatch(admission *Admission, serverID, index int64, count int) (*WriteBatch, error) {
	lease, err := admission.beginDecode(context.Background())
	if err != nil {
		return nil, err
	}
	events := make([]logs.CanonicalEvent, count)
	for event := range events {
		events[event] = canonicalEvent(
			serverID, fmt.Sprintf("group-%d-%d", index, event),
			fmt.Sprintf("group commit benchmark batch %d event %d", index, event), index+1,
		)
	}
	if err := lease.add(int64(count), int64(count)*groupBenchmarkEventSize); err != nil {
		lease.release()
		return nil, err
	}
	lease.decodingDone()
	return NewWriteBatch(DecodedBatch{
		Events: events, ApproxBytes: int64(count) * groupBenchmarkEventSize, lease: lease,
	}, "group-benchmark", serverID), nil
}

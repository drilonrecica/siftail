package ingest

import (
	"context"
	"errors"
	"time"
)

// Group-commit bounds (ADR 0009). Defaults come from the real-disk benchmark
// recorded in docs/performance/group-commit.md; the limits are the documented
// configuration range.
const (
	DefaultGroupMaxBatches = 64
	DefaultGroupMaxBytes   = 8 << 20 // 8 MiB
	DefaultGroupMaxWait    = time.Duration(0)

	MaxGroupMaxBatches = 1024
	MaxGroupMaxWait    = 100 * time.Millisecond
)

// GroupLimits bounds one commit group. A group always holds at least one
// batch; further already-queued batches join while the group stays within
// MaxBatches and MaxBytes of approximate retained bytes. MaxWait is how long
// the writer may wait for more batches after the first; zero adds no latency.
type GroupLimits struct {
	MaxBatches int
	MaxBytes   int64
	MaxWait    time.Duration
}

// DefaultGroupLimits returns the documented default bounds.
func DefaultGroupLimits() GroupLimits {
	return GroupLimits{
		MaxBatches: DefaultGroupMaxBatches,
		MaxBytes:   DefaultGroupMaxBytes,
		MaxWait:    DefaultGroupMaxWait,
	}
}

// Validate reports bounds outside the documented range.
func (l GroupLimits) Validate() error {
	if l.MaxBatches < 1 || l.MaxBatches > MaxGroupMaxBatches {
		return errors.New("commit group batch bound must be between 1 and 1024")
	}
	if l.MaxBytes < 1 {
		return errors.New("commit group byte bound must be positive")
	}
	if l.MaxWait < 0 || l.MaxWait > MaxGroupMaxWait {
		return errors.New("commit group wait must be between 0 and 100ms")
	}
	return nil
}

// WriterWorker drains complete admitted batches through the transactional
// writer in commit groups. Queue closure is the graceful stop signal, and
// queued batches still drain through the same grouping; caller cancellation
// never abandons work that admission already accepted.
type WriterWorker struct {
	queue    *Queue
	writer   *BatchWriter
	observer Observer
	limits   GroupLimits
}

func (w *WriterWorker) WithObserver(observer Observer) *WriterWorker {
	w.observer = observer
	return w
}

// WithGroupLimits replaces the commit-group bounds. Run refuses bounds outside
// the documented range.
func (w *WriterWorker) WithGroupLimits(limits GroupLimits) *WriterWorker {
	w.limits = limits
	return w
}

func NewWriterWorker(queue *Queue, writer *BatchWriter) *WriterWorker {
	return &WriterWorker{queue: queue, writer: writer, limits: DefaultGroupLimits()}
}

func (w *WriterWorker) Run(ctx context.Context) error {
	if w == nil || w.queue == nil || w.writer == nil {
		return errors.New("ingestion writer worker is not configured")
	}
	if err := w.limits.Validate(); err != nil {
		return err
	}
	// carry is a batch taken from the queue that did not fit the previous
	// group's byte bound; it opens the next group, so queue order holds.
	var carry *WriteBatch
	for {
		first := carry
		carry = nil
		if first == nil {
			batch, err := w.queue.Dequeue(context.Background())
			if errors.Is(err, ErrQueueClosed) {
				return nil
			}
			if err != nil {
				return err
			}
			first = batch
		}
		select {
		case <-ctx.Done():
			first.Complete(&Error{Category: CategoryUnavailable})
			w.queue.Drain(&Error{Category: CategoryUnavailable})
			return nil
		default:
		}
		var group []*WriteBatch
		group, carry = w.collect(first)
		outcomes := w.writer.PersistGroup(ctx, group)
		for index, batch := range group {
			w.observe(batch, outcomes[index])
			batch.Complete(outcomes[index].Err)
		}
	}
}

// collect builds one commit group starting with first. It returns the group
// and, when a dequeued batch would exceed the byte bound, that batch to open
// the next group. A first batch at or above the byte bound forms a group of
// one.
func (w *WriterWorker) collect(first *WriteBatch) ([]*WriteBatch, *WriteBatch) {
	group := []*WriteBatch{first}
	bytes := first.ApproxBytes
	if bytes >= w.limits.MaxBytes {
		return group, nil
	}
	var wait <-chan time.Time
	if w.limits.MaxWait > 0 {
		timer := time.NewTimer(w.limits.MaxWait)
		defer timer.Stop()
		wait = timer.C
	}
	for len(group) < w.limits.MaxBatches {
		batch, ok := w.queue.next(wait)
		if !ok {
			break
		}
		if bytes+batch.ApproxBytes > w.limits.MaxBytes {
			return group, batch
		}
		group = append(group, batch)
		bytes += batch.ApproxBytes
	}
	return group, nil
}

func (w *WriterWorker) observe(batch *WriteBatch, outcome BatchOutcome) {
	if w.observer == nil {
		return
	}
	if outcome.Err == nil {
		now := time.Now().UTC()
		w.observer.RecordIngestAccepted(len(batch.Events), now)
		if outcome.Result.ConflictsDropped > 0 {
			w.observer.RecordIngestConflicts(
				batch.AuthenticatedServerID, outcome.Result.ConflictsDropped, now,
			)
		}
		if outcome.Result.LateEvents > 0 {
			w.observer.RecordIngestLate(
				batch.AuthenticatedServerID, outcome.Result.LateEvents, now,
			)
		}
		return
	}
	category := CategoryUnavailable
	var ingestErr *Error
	if errors.As(outcome.Err, &ingestErr) {
		category = ingestErr.Category
	}
	// Only storage-wide categories degrade ingestion globally; policy,
	// constraint, and internal failures are scoped to this batch.
	databaseFailure := category == CategoryUnavailable ||
		category == CategoryStorageFull
	w.observer.RecordIngestRejected(category, databaseFailure, time.Now().UTC())
}

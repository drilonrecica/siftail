package ingest

import (
	"context"
	"errors"
	"time"
)

// WriterWorker drains complete admitted batches through the transactional
// writer. Queue closure is the graceful stop signal; caller cancellation never
// abandons work that admission already accepted.
type WriterWorker struct {
	queue    *Queue
	writer   *BatchWriter
	observer Observer
}

func (w *WriterWorker) WithObserver(observer Observer) *WriterWorker {
	w.observer = observer
	return w
}

func NewWriterWorker(queue *Queue, writer *BatchWriter) *WriterWorker {
	return &WriterWorker{queue: queue, writer: writer}
}

func (w *WriterWorker) Run(ctx context.Context) error {
	if w == nil || w.queue == nil || w.writer == nil {
		return errors.New("ingestion writer worker is not configured")
	}
	for {
		batch, err := w.queue.Dequeue(context.Background())
		if errors.Is(err, ErrQueueClosed) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			batch.Complete(&Error{Category: CategoryUnavailable})
			w.queue.Drain(&Error{Category: CategoryUnavailable})
			return nil
		default:
		}
		persistErr := w.writer.Persist(ctx, batch)
		if w.observer != nil {
			if persistErr == nil {
				w.observer.RecordIngestAccepted(len(batch.Events), time.Now().UTC())
			} else {
				category := CategoryUnavailable
				var ingestErr *Error
				if errors.As(persistErr, &ingestErr) {
					category = ingestErr.Category
				}
				databaseFailure := category == CategoryUnavailable ||
					category == CategoryStorageFull
				w.observer.RecordIngestRejected(
					category, databaseFailure, time.Now().UTC(),
				)
			}
		}
		batch.Complete(persistErr)
	}
}

package ingest

import (
	"context"
	"errors"
)

// WriterWorker drains complete admitted batches through the transactional
// writer. Queue closure is the graceful stop signal; caller cancellation never
// abandons work that admission already accepted.
type WriterWorker struct {
	queue  *Queue
	writer *BatchWriter
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
		batch.Complete(w.writer.Persist(ctx, batch))
	}
}

package sessions

import (
	"context"
	"time"
)

type CleanupWorker struct {
	store    *Store
	interval time.Duration
	onError  func(error)
}

func NewCleanupWorker(store *Store, interval time.Duration, onError func(error)) *CleanupWorker {
	if interval <= 0 {
		interval = time.Hour
	}
	return &CleanupWorker{store: store, interval: interval, onError: onError}
}

func (w *CleanupWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := w.store.Cleanup(ctx, 1000); err != nil && w.onError != nil {
				w.onError(err)
			}
		}
	}
}

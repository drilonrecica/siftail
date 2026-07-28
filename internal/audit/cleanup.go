package audit

import (
	"context"
	"errors"
	"time"
)

const DefaultCleanupInterval = time.Hour

type CleanupWorker struct {
	store    *Store
	interval time.Duration
	onError  func(error)
}

func NewCleanupWorker(
	store *Store,
	interval time.Duration,
	onError func(error),
) *CleanupWorker {
	if interval <= 0 {
		interval = DefaultCleanupInterval
	}
	return &CleanupWorker{store: store, interval: interval, onError: onError}
}

func (w *CleanupWorker) Run(ctx context.Context) error {
	if w == nil || w.store == nil {
		return errors.New("security audit cleanup worker is unavailable")
	}
	if ctx == nil {
		return errors.New("security audit cleanup context is nil")
	}
	w.runOnce(ctx)
	timer := time.NewTimer(w.interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			w.runOnce(ctx)
			timer.Reset(w.interval)
		}
	}
}

func (w *CleanupWorker) runOnce(ctx context.Context) {
	_, err := w.store.Cleanup(ctx, DefaultRetentionDays, MaxCleanupChunk)
	if err != nil && !errors.Is(err, context.Canceled) && w.onError != nil {
		w.onError(err)
	}
}

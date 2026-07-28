package retention

import (
	"context"
	"errors"
	"time"
)

type Worker struct {
	cleaner  *Cleaner
	interval time.Duration
	onError  func(error)
}

func NewWorker(cleaner *Cleaner, interval time.Duration, onError func(error)) *Worker {
	if interval <= 0 {
		interval = DefaultCleanupInterval
	}
	return &Worker{cleaner: cleaner, interval: interval, onError: onError}
}

func (w *Worker) Run(ctx context.Context) error {
	if w == nil || w.cleaner == nil {
		return errors.New("retention worker is unavailable")
	}
	if ctx == nil {
		return errors.New("retention worker context is nil")
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

func (w *Worker) runOnce(ctx context.Context) {
	if _, err := w.cleaner.RunOnce(ctx); err != nil &&
		!errors.Is(err, context.Canceled) && w.onError != nil {
		w.onError(err)
	}
}

package database

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const DefaultRecoveryProbeInterval = 5 * time.Second

// ProbeWritable commits a net-zero settings mutation through the active
// coordinator. SQLite must complete a real main-database/WAL commit, but no
// row remains after the transaction.
func ProbeWritable(
	ctx context.Context,
	coordinator MutationCoordinator,
	at time.Time,
) error {
	if coordinator == nil {
		return errors.New("database recovery probe is unavailable")
	}
	return coordinator.Do(ctx, func(tx *sql.Tx) error {
		mutationCtx := context.WithoutCancel(ctx)
		if _, err := tx.ExecContext(mutationCtx, `INSERT INTO settings(
			key,value_json,updated_at_us
		) VALUES(
			'storage_recovery_probe',
			json_object('probe',hex(zeroblob(32768))),
			?
		)`, at.UTC().UnixMicro()); err != nil {
			return Classify("write database recovery probe", err)
		}
		if _, err := tx.ExecContext(mutationCtx,
			"DELETE FROM settings WHERE key='storage_recovery_probe'"); err != nil {
			return Classify("remove database recovery probe", err)
		}
		return nil
	})
}

type RecoveryWorker struct {
	coordinator MutationCoordinator
	degraded    func() bool
	recovered   func(time.Time)
	onError     func(error)
	interval    time.Duration
	now         func() time.Time
}

func NewRecoveryWorker(
	coordinator MutationCoordinator,
	degraded func() bool,
	recovered func(time.Time),
	onError func(error),
	interval time.Duration,
) *RecoveryWorker {
	if interval <= 0 {
		interval = DefaultRecoveryProbeInterval
	}
	return &RecoveryWorker{
		coordinator: coordinator, degraded: degraded, recovered: recovered,
		onError: onError, interval: interval, now: time.Now,
	}
}

func (w *RecoveryWorker) Run(ctx context.Context) error {
	if w == nil || w.coordinator == nil || w.degraded == nil ||
		w.recovered == nil {
		return errors.New("database recovery worker is unavailable")
	}
	if ctx == nil {
		return errors.New("database recovery context is nil")
	}
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

func (w *RecoveryWorker) runOnce(ctx context.Context) {
	if !w.degraded() {
		return
	}
	at := w.now().UTC()
	if err := ProbeWritable(ctx, w.coordinator, at); err != nil {
		if !errors.Is(err, context.Canceled) && w.onError != nil {
			w.onError(err)
		}
		return
	}
	w.recovered(at)
}

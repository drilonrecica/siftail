package audit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCleanupWorkerRunsImmediatelyAndStopsWithItsOwner(t *testing.T) {
	db, store, _ := auditFixture(t)
	store.now = func() time.Time {
		return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	}
	input := validInput()
	input.OccurredAt = store.now().Add(-366 * 24 * time.Hour)
	if _, err := store.Record(context.Background(), input); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- NewCleanupWorker(store, time.Hour, nil).Run(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var count int
		if err := db.Reader().QueryRow(
			"SELECT count(*) FROM security_audit_events",
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial audit cleanup did not complete")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCleanupWorkerValidatesLifecycleAndReportsRecoverableErrors(t *testing.T) {
	if err := (*CleanupWorker)(nil).Run(context.Background()); err == nil {
		t.Fatal("nil worker succeeded")
	}
	_, store, _ := auditFixture(t)
	if err := NewCleanupWorker(store, time.Hour, nil).Run(nil); err == nil {
		t.Fatal("nil context succeeded")
	}

	db, failingStore, _ := auditFixture(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reported := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	worker := NewCleanupWorker(failingStore, time.Hour, func(err error) {
		reported <- err
		cancel()
	})
	if err := worker.Run(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-reported:
		if err == nil || errors.Is(err, context.Canceled) {
			t.Fatalf("reported error = %v", err)
		}
	default:
		t.Fatal("cleanup error was not reported")
	}
}

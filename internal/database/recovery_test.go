package database

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestProbeWritableCommitsNetZeroMutation(t *testing.T) {
	db := openTestDB(t)
	coordinator, stop := startRecoveryCoordinator(t, db)
	defer stop()

	if err := ProbeWritable(context.Background(), coordinator, time.Now()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.Reader().QueryRow(
		"SELECT count(*) FROM settings WHERE key='storage_recovery_probe'",
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("recovery probe rows = %d", count)
	}

	if _, err := db.Writer().Exec(`INSERT INTO settings(
		key,value_json,updated_at_us
	) VALUES('storage_recovery_probe','{"existing":true}',1)`); err != nil {
		t.Fatal(err)
	}
	if err := ProbeWritable(context.Background(), coordinator, time.Now()); err == nil {
		t.Fatal("probe replaced a pre-existing reserved row")
	}
	var value string
	if err := db.Reader().QueryRow(
		"SELECT value_json FROM settings WHERE key='storage_recovery_probe'",
	).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != `{"existing":true}` {
		t.Fatalf("pre-existing probe row changed to %q", value)
	}
}

func TestProbeWritableDetectsFullWALAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, stop := startRecoveryCoordinator(t, db)

	var pages int
	if err := db.Writer().QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	setMaxPageCount(t, db, pages)
	err = ProbeWritable(context.Background(), coordinator, time.Now())
	assertDatabaseCategory(t, err, CategoryFull)
	assertNoRecoveryProbe(t, db)

	setMaxPageCount(t, db, pages+1024)
	if err := ProbeWritable(context.Background(), coordinator, time.Now()); err != nil {
		t.Fatalf("probe after clearing quota: %v", err)
	}
	assertNoRecoveryProbe(t, db)

	stop()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("restart after full condition: %v", err)
	}
	defer reopened.Close()
	if err := QuickCheck(context.Background(), reopened.Reader()); err != nil {
		t.Fatalf("integrity after restart: %v", err)
	}
}

func TestClassifyTemporaryStorageFull(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Writer().Exec(`
		PRAGMA temp_store=FILE;
		PRAGMA temp.page_size=4096;
		PRAGMA temp.max_page_count=2;
		CREATE TEMP TABLE full_temp(value BLOB);
	`); err != nil {
		t.Fatal(err)
	}
	_, err := db.Writer().Exec(
		"INSERT INTO full_temp(value) VALUES(zeroblob(32768))",
	)
	assertDatabaseCategory(t, Classify("write bounded temporary data", err), CategoryFull)
}

func TestRecoveryWorkerRetriesOnlyWhileDegraded(t *testing.T) {
	db := openTestDB(t)
	coordinator, stopCoordinator := startRecoveryCoordinator(t, db)
	defer stopCoordinator()

	var pages int
	if err := db.Writer().QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	setMaxPageCount(t, db, pages)

	var mu sync.Mutex
	degraded := true
	var failures int
	recovered := make(chan time.Time, 1)
	worker := NewRecoveryWorker(
		coordinator,
		func() bool {
			mu.Lock()
			defer mu.Unlock()
			return degraded
		},
		func(at time.Time) {
			mu.Lock()
			degraded = false
			mu.Unlock()
			recovered <- at
		},
		func(err error) {
			var category *CategoryError
			if !errors.As(err, &category) || category.Category != CategoryFull {
				t.Errorf("probe error = %#v", err)
			}
			mu.Lock()
			failures++
			mu.Unlock()
		},
		5*time.Millisecond,
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		gotFailures := failures
		mu.Unlock()
		if gotFailures > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not report the full condition")
		}
		time.Sleep(time.Millisecond)
	}
	setMaxPageCount(t, db, pages+1024)
	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("worker did not report recovery")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertNoRecoveryProbe(t, db)
}

func TestRecoveryWorkerRejectsUnavailableLifecycle(t *testing.T) {
	if err := (*RecoveryWorker)(nil).Run(context.Background()); err == nil {
		t.Fatal("nil worker was accepted")
	}
	worker := NewRecoveryWorker(nil, func() bool { return true }, func(time.Time) {}, nil, 0)
	if err := worker.Run(context.Background()); err == nil {
		t.Fatal("nil coordinator was accepted")
	}
	db := openTestDB(t)
	coordinator, stop := startRecoveryCoordinator(t, db)
	defer stop()
	worker = NewRecoveryWorker(coordinator, func() bool { return true }, func(time.Time) {}, nil, 0)
	if err := worker.Run(nil); err == nil {
		t.Fatal("nil context was accepted")
	}
}

func BenchmarkProbeWritable(b *testing.B) {
	db, err := Open(context.Background(), filepath.Join(b.TempDir(), "siftail.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	defer func() {
		coordinator.Close()
		cancel()
		if err := <-done; err != nil {
			b.Error(err)
		}
	}()

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if err := ProbeWritable(context.Background(), coordinator, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}

func startRecoveryCoordinator(t *testing.T, db *DB) (*Coordinator, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	var once sync.Once
	return coordinator, func() {
		once.Do(func() {
			coordinator.Close()
			cancel()
			if err := <-done; err != nil {
				t.Errorf("coordinator: %v", err)
			}
		})
	}
}

func setMaxPageCount(t *testing.T, db *DB, pages int) {
	t.Helper()
	var got int
	if err := db.Writer().QueryRow(
		"PRAGMA max_page_count=" + strconv.Itoa(pages),
	).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != pages {
		t.Fatalf("max_page_count = %d, want %d", got, pages)
	}
}

func assertDatabaseCategory(t *testing.T, err error, want Category) {
	t.Helper()
	var category *CategoryError
	if !errors.As(err, &category) || category.Category != want {
		t.Fatalf("error = %#v, want category %q", err, want)
	}
}

func assertNoRecoveryProbe(t *testing.T, db *DB) {
	t.Helper()
	var count int
	if err := db.Reader().QueryRow(
		"SELECT count(*) FROM settings WHERE key='storage_recovery_probe'",
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("recovery probe rows = %d", count)
	}
}

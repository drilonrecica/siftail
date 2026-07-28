package retention

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestCleanerDeletesCanonicalAgeOrderInBoundedCommittedChunks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := settingsTestDatabase(t, path)
	defer stop()
	store := NewStore(db.Reader(), coordinator)
	if _, err := store.Save(context.Background(), Input{
		AgeDays: 1, MaxDatabaseGiB: 1,
	}); err != nil {
		t.Fatal(err)
	}
	insertRetentionSource(t, db.Writer())
	now := time.Unix(200_000, 0).UTC()
	cutoff := now.AddDate(0, 0, -1).UnixMicro()
	insertRetentionEvent(t, db.Writer(), 1, cutoff-20, now.UnixMicro())
	insertRetentionEvent(t, db.Writer(), 2, now.Add(time.Hour).UnixMicro(), cutoff-10)
	insertRetentionEvent(t, db.Writer(), 3, cutoff-10, cutoff-10)
	insertRetentionEvent(t, db.Writer(), 4, cutoff, cutoff)
	insertRetentionEvent(t, db.Writer(), 5, cutoff+1, cutoff+1)

	var notifications atomic.Int64
	cleaner := NewCleaner(db.Reader(), coordinator, path, store, CleanerOptions{
		DeleteChunk: 2, VacuumPages: 1, Now: func() time.Time { return now },
		AfterDelete: func(deleted int64) {
			notification := notifications.Add(1)
			var remaining int
			if err := db.Reader().QueryRow("SELECT count(*) FROM log_events").
				Scan(&remaining); err != nil {
				t.Error(err)
			}
			if remaining >= 5 {
				t.Error("retention notification ran before commit")
			}
			if notification == 1 {
				assertRetentionEventIDs(t, db.Reader(), []int64{3, 4, 5})
			}
		},
		MeasureFootprint: func(string) (int64, error) { return 1, nil },
	})
	result, err := cleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.AgeDeleted != 3 || result.SizeDeleted != 0 ||
		notifications.Load() != 2 {
		t.Fatalf("cleanup result = %#v, notifications=%d", result, notifications.Load())
	}
	assertRetentionEventIDs(t, db.Reader(), []int64{4, 5})
	var sources, settings int
	if err := db.Reader().QueryRow("SELECT count(*) FROM sources").Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if err := db.Reader().QueryRow("SELECT count(*) FROM settings").Scan(&settings); err != nil {
		t.Fatal(err)
	}
	if sources != 1 || settings == 0 {
		t.Fatalf("non-event state changed: sources=%d settings=%d", sources, settings)
	}
}

func TestCleanerSizeTriggerDeletesOldestTowardTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := settingsTestDatabase(t, path)
	defer stop()
	store := NewStore(db.Reader(), coordinator)
	if _, err := store.Save(context.Background(), Input{
		AgeDays: MaximumAgeDays, MaxDatabaseGiB: 1,
	}); err != nil {
		t.Fatal(err)
	}
	insertRetentionSource(t, db.Writer())
	for id := int64(1); id <= 5; id++ {
		insertRetentionEvent(t, db.Writer(), id, id, id)
	}
	var measurements atomic.Int64
	cleaner := NewCleaner(db.Reader(), coordinator, path, store, CleanerOptions{
		DeleteChunk: 2, VacuumPages: 1,
		Now: func() time.Time { return time.Unix(1, 0).UTC() },
		MeasureFootprint: func(string) (int64, error) {
			if measurements.Add(1) == 1 {
				return 980 << 20, nil
			}
			return 890 << 20, nil
		},
	})
	result, err := cleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.SizeTriggered || !result.SizeTargetReached ||
		result.SizeDeleted != 2 || result.EventsExhausted {
		t.Fatalf("size cleanup result = %#v", result)
	}
	assertRetentionEventIDs(t, db.Reader(), []int64{3, 4, 5})
}

func TestCleanerRollbackCancellationAndExhaustedTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := settingsTestDatabase(t, path)
	defer stop()
	store := NewStore(db.Reader(), coordinator)
	if _, err := store.Save(context.Background(), Input{
		AgeDays: 1, MaxDatabaseGiB: 1,
	}); err != nil {
		t.Fatal(err)
	}
	insertRetentionSource(t, db.Writer())
	for id := int64(1); id <= 3; id++ {
		insertRetentionEvent(t, db.Writer(), id, id, id)
	}
	if _, err := db.Writer().Exec(`CREATE TRIGGER reject_retention_delete
		BEFORE DELETE ON log_events
		BEGIN SELECT raise(ABORT, 'blocked retention'); END`); err != nil {
		t.Fatal(err)
	}
	var notifications atomic.Int64
	cleaner := NewCleaner(db.Reader(), coordinator, path, store, CleanerOptions{
		DeleteChunk: 1, Now: func() time.Time { return time.Unix(200_000, 0).UTC() },
		AfterDelete:      func(int64) { notifications.Add(1) },
		MeasureFootprint: func(string) (int64, error) { return 1, nil },
	})
	if _, err := cleaner.RunOnce(context.Background()); err == nil {
		t.Fatal("blocked retention succeeded")
	}
	assertRetentionEventIDs(t, db.Reader(), []int64{1, 2, 3})
	if notifications.Load() != 0 {
		t.Fatal("rolled-back retention published a notification")
	}
	if _, err := db.Writer().Exec("DROP TRIGGER reject_retention_delete"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cleaner.afterDelete = func(int64) { cancel() }
	if _, err := cleaner.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cleanup error = %v", err)
	}
	assertRetentionEventIDs(t, db.Reader(), []int64{2, 3})
	if err := database.QuickCheck(context.Background(), db.Reader()); err != nil {
		t.Fatal(err)
	}

	cleaner.now = func() time.Time { return time.Unix(1, 0).UTC() }
	cleaner.afterDelete = nil
	cleaner.measureFootprint = func(string) (int64, error) { return 980 << 20, nil }
	result, err := cleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.SizeTriggered || result.SizeTargetReached ||
		!result.EventsExhausted || result.SizeDeleted != 2 {
		t.Fatalf("exhausted cleanup result = %#v", result)
	}
	assertRetentionEventIDs(t, db.Reader(), nil)
}

func TestCleanerStopsSizeDeletionWhenCheckpointIsBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := settingsTestDatabase(t, path)
	defer stop()
	store := NewStore(db.Reader(), coordinator)
	if _, err := store.Save(context.Background(), Input{
		AgeDays: 1, MaxDatabaseGiB: 1,
	}); err != nil {
		t.Fatal(err)
	}
	insertRetentionSource(t, db.Writer())
	now := time.Unix(200_000, 0).UTC()
	insertRetentionEvent(t, db.Writer(), 1, 1, 1)
	for id := int64(2); id <= 5; id++ {
		insertRetentionEvent(t, db.Writer(), id, now.UnixMicro(), now.UnixMicro())
	}
	if err := coordinator.DoMaintenance(context.Background(), func(writer *sql.DB) error {
		_, err := writer.Exec("PRAGMA busy_timeout=1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	readTx, err := db.Reader().Begin()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := readTx.QueryRow("SELECT count(*) FROM log_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback()

	cleaner := NewCleaner(db.Reader(), coordinator, path, store, CleanerOptions{
		DeleteChunk: 1, VacuumPages: 1,
		Now:              func() time.Time { return now },
		MeasureFootprint: func(string) (int64, error) { return 980 << 20, nil },
	})
	result, err := cleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.SizeTriggered || !result.CheckpointBusy ||
		result.AgeDeleted != 1 || result.SizeDeleted != 0 || result.SizeTargetReached {
		t.Fatalf("busy-checkpoint cleanup = %#v", result)
	}
	assertRetentionEventIDs(t, db.Reader(), []int64{2, 3, 4, 5})
}

func TestCleanerCanReleaseQuotaPressureForRecoveryProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := settingsTestDatabase(t, path)
	defer stop()
	store := NewStore(db.Reader(), coordinator)
	if _, err := store.Save(context.Background(), Input{
		AgeDays: 1, MaxDatabaseGiB: 1,
	}); err != nil {
		t.Fatal(err)
	}
	insertRetentionSource(t, db.Writer())
	now := time.Unix(200_000, 0).UTC()
	cutoff := now.AddDate(0, 0, -1).UnixMicro()
	if _, err := db.Writer().Exec(`INSERT INTO log_events(
		id,event_at_us,received_at_us,source_id,stream,
		level_normalized,message_raw,message_text,attributes_json
	) VALUES(1,?,?,1,'stdout','info',zeroblob(65536),'old','{}')`,
		cutoff-1, cutoff-1); err != nil {
		t.Fatal(err)
	}
	var pages int
	if err := db.Writer().QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec(
		"PRAGMA max_page_count=" + strconv.Itoa(pages),
	); err != nil {
		t.Fatal(err)
	}
	if err := database.ProbeWritable(
		context.Background(), coordinator, now,
	); err == nil {
		t.Fatal("recovery probe succeeded before retention released pages")
	}

	cleaner := NewCleaner(db.Reader(), coordinator, path, store, CleanerOptions{
		DeleteChunk: 1, VacuumPages: 1, Now: func() time.Time { return now },
		MeasureFootprint: func(string) (int64, error) { return 1, nil },
	})
	result, err := cleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.AgeDeleted != 1 {
		t.Fatalf("cleanup result = %#v", result)
	}
	if err := database.ProbeWritable(
		context.Background(), coordinator, now.Add(time.Second),
	); err != nil {
		t.Fatalf("probe after bounded retention cleanup: %v", err)
	}
}

func TestActiveFootprintAndWorkerShutdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := settingsTestDatabase(t, path)
	defer stop()
	footprint, err := ActiveFootprint(path)
	if err != nil || footprint <= 0 {
		t.Fatalf("active footprint = %d, err=%v", footprint, err)
	}
	store := NewStore(db.Reader(), coordinator)
	cleaner := NewCleaner(db.Reader(), coordinator, path, store, CleanerOptions{
		MeasureFootprint: func(string) (int64, error) { return 1, nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- NewWorker(cleaner, time.Millisecond, nil).Run(ctx) }()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retention worker did not stop")
	}
}

func TestWorkerReportsRecoverableCleanupErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := settingsTestDatabase(t, path)
	defer stop()
	if _, err := db.Writer().Exec(`INSERT INTO settings(key,value_json,updated_at_us)
		VALUES('application_retention','{"unexpected":true}',1)`); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db.Reader(), coordinator)
	cleaner := NewCleaner(db.Reader(), coordinator, path, store, CleanerOptions{})
	reported := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- NewWorker(cleaner, time.Hour, func(err error) {
			reported <- err
		}).Run(ctx)
	}()
	select {
	case err := <-reported:
		if !errors.Is(err, ErrInvalidStored) {
			t.Fatalf("reported cleanup error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("retention worker swallowed cleanup error")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCleanerInterleavesWithConcurrentIngestionAndReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := settingsTestDatabase(t, path)
	defer stop()
	store := NewStore(db.Reader(), coordinator)
	if _, err := store.Save(context.Background(), Input{
		AgeDays: 1, MaxDatabaseGiB: 1,
	}); err != nil {
		t.Fatal(err)
	}
	insertRetentionSource(t, db.Writer())
	for id := int64(1); id <= 200; id++ {
		insertRetentionEvent(t, db.Writer(), id, 1, 1)
	}
	now := time.Unix(200_000, 0).UTC()
	cleaner := NewCleaner(db.Reader(), coordinator, path, store, CleanerOptions{
		DeleteChunk: 5, VacuumPages: 1, Now: func() time.Time { return now },
		AfterDelete:      func(int64) { time.Sleep(100 * time.Microsecond) },
		MeasureFootprint: func(string) (int64, error) { return 1, nil },
	})

	start := make(chan struct{})
	var workers sync.WaitGroup
	var writeErr, readErr atomic.Value
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for index := int64(1); index <= 50; index++ {
			id := 1_000 + index
			err := coordinator.Do(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO log_events(
					id,event_at_us,received_at_us,source_id,stream,level_normalized,
					message_raw,message_text
				) VALUES(?,?,?,?,?,?,?,?)`,
					id, now.UnixMicro(), now.UnixMicro(), 1,
					"stdout", "info", []byte("new"), "new",
				)
				return err
			})
			if err != nil {
				writeErr.Store(err)
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 100; index++ {
			var count int
			if err := db.Reader().QueryRow("SELECT count(*) FROM log_events").
				Scan(&count); err != nil {
				readErr.Store(err)
				return
			}
		}
	}()
	close(start)
	result, err := cleaner.RunOnce(context.Background())
	workers.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if stored := writeErr.Load(); stored != nil {
		t.Fatal(stored)
	}
	if stored := readErr.Load(); stored != nil {
		t.Fatal(stored)
	}
	if result.AgeDeleted != 200 {
		t.Fatalf("concurrent age deletion = %d", result.AgeDeleted)
	}
	var remaining int
	if err := db.Reader().QueryRow("SELECT count(*) FROM log_events").
		Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 50 {
		t.Fatalf("concurrent remaining events = %d", remaining)
	}
	if err := database.QuickCheck(context.Background(), db.Reader()); err != nil {
		t.Fatal(err)
	}
}

func insertRetentionSource(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO servers(id,name,created_at_us)
		VALUES(1,'retention-server',1);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			first_seen_at_us,last_seen_at_us
		) VALUES(1,1,'project','environment','application','service',
			'project','environment','application','service',1,1)`); err != nil {
		t.Fatal(err)
	}
}

func insertRetentionEvent(
	t *testing.T,
	db *sql.DB,
	id, eventAtUS, receivedAtUS int64,
) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO log_events(
		id,event_at_us,received_at_us,source_id,stream,level_normalized,
		message_raw,message_text
	) VALUES(?,?,?,?,?,?,?,?)`,
		id, eventAtUS, receivedAtUS, 1, "stdout", "info", []byte("event"), "event",
	); err != nil {
		t.Fatal(err)
	}
}

func assertRetentionEventIDs(t *testing.T, db *sql.DB, want []int64) {
	t.Helper()
	rows, err := db.Query("SELECT id FROM log_events ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("event IDs = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("event IDs = %v, want %v", got, want)
		}
	}
}

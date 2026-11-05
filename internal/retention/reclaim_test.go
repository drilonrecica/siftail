package retention

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

// maxVacuumChunkHold is the per-chunk writer-hold ceiling the reclaim test
// enforces. docs/performance/retention.md records 72–189 ms for a
// 1,024-page chunk on a SATA SSD; the ceiling leaves room for slower disks
// while still failing on the former per-page commits, which held the writer
// for about 19 s per 8,192-page cycle on the same disk.
const maxVacuumChunkHold = time.Second

// maintenanceTurn is one DoMaintenance operation as the coordinator worker ran
// it: its writer hold and the free pages it released.
type maintenanceTurn struct {
	started, finished time.Time
	released          int
}

func (turn maintenanceTurn) hold() time.Duration {
	return turn.finished.Sub(turn.started)
}

// recordingCoordinator times every maintenance operation inside the
// coordinator worker, so a hold is exactly the time the writer was occupied.
type recordingCoordinator struct {
	*database.Coordinator
	mu    sync.Mutex
	turns []maintenanceTurn
}

func (r *recordingCoordinator) DoMaintenance(
	ctx context.Context,
	run func(*sql.DB) error,
) error {
	return r.Coordinator.DoMaintenance(ctx, func(db *sql.DB) error {
		var before, after int
		if err := db.QueryRow("PRAGMA freelist_count").Scan(&before); err != nil {
			return err
		}
		started := time.Now()
		err := run(db)
		finished := time.Now()
		if scanErr := db.QueryRow("PRAGMA freelist_count").Scan(&after); scanErr != nil {
			return scanErr
		}
		r.mu.Lock()
		r.turns = append(r.turns, maintenanceTurn{
			started: started, finished: finished, released: before - after,
		})
		r.mu.Unlock()
		return err
	})
}

func (r *recordingCoordinator) snapshot() []maintenanceTurn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]maintenanceTurn(nil), r.turns...)
}

func vacuumTurns(turns []maintenanceTurn) []maintenanceTurn {
	var vacuums []maintenanceTurn
	for _, turn := range turns {
		if turn.released > 0 {
			vacuums = append(vacuums, turn)
		}
	}
	return vacuums
}

// insertPayloadEvents inserts count events with a payload-byte raw message in
// one transaction; ids and receive times start at firstID and receivedAtUS.
func insertPayloadEvents(
	tb testing.TB,
	db *sql.DB,
	firstID, count, receivedAtUS int64,
	payloadBytes int,
) {
	tb.Helper()
	tx, err := db.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO log_events(
		id,event_at_us,received_at_us,source_id,stream,level_normalized,
		message_raw,message_text
	) VALUES(?,?,?,1,'stdout','info',zeroblob(?),'event')`)
	if err != nil {
		tb.Fatal(err)
	}
	for index := int64(0); index < count; index++ {
		if _, err := statement.Exec(
			firstID+index, receivedAtUS+index, receivedAtUS+index, payloadBytes,
		); err != nil {
			tb.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		tb.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
}

func truncateCheckpoint(tb testing.TB, db *sql.DB) {
	tb.Helper()
	var busy, frames, checkpointed int
	if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&busy, &frames, &checkpointed); err != nil {
		tb.Fatal(err)
	}
	if busy != 0 {
		tb.Fatal("test checkpoint was busy")
	}
}

func fileSize(tb testing.TB, path string) int64 {
	tb.Helper()
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		tb.Fatal(err)
	}
	return info.Size()
}

func TestActiveFootprintCountsInUsePagesAndWALNotFreePages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, _, stop := settingsTestDatabase(t, path)
	defer stop()
	ctx := context.Background()
	insertRetentionSource(t, db.Writer())
	insertPayloadEvents(t, db.Writer(), 1, 50, 1, 64<<10)
	truncateCheckpoint(t, db.Writer())
	before, err := ActiveFootprint(ctx, db.Reader(), path)
	if err != nil {
		t.Fatal(err)
	}
	fileBefore := fileSize(t, path)

	if _, err := db.Writer().Exec("DELETE FROM log_events"); err != nil {
		t.Fatal(err)
	}
	walAfterDelete := fileSize(t, path+"-wal")
	withWAL, err := ActiveFootprint(ctx, db.Reader(), path)
	if err != nil {
		t.Fatal(err)
	}
	truncateCheckpoint(t, db.Writer())
	after, err := ActiveFootprint(ctx, db.Reader(), path)
	if err != nil {
		t.Fatal(err)
	}

	var pages, free, pageSize int64
	if err := db.Writer().QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err := db.Writer().QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
		t.Fatal(err)
	}
	if err := db.Writer().QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	// Nothing was vacuumed: the file keeps its size and the freed pages sit on
	// the freelist, yet the footprint no longer counts them.
	if fileSize(t, path) != fileBefore || free < 50*16 {
		t.Fatalf("file %d -> %d bytes, freelist %d pages", fileBefore, fileSize(t, path), free)
	}
	if before-after < 50*64<<10 {
		t.Fatalf("footprint %d -> %d did not drop by the deleted payload", before, after)
	}
	want := (pages-free)*pageSize + fileSize(t, path+"-wal") + fileSize(t, path+"-shm")
	if after != want {
		t.Fatalf("footprint = %d, want (page_count-freelist)*page_size+wal+shm = %d", after, want)
	}
	// The uncheckpointed WAL counts in full.
	if walAfterDelete == 0 || withWAL != after+walAfterDelete {
		t.Fatalf("footprint with WAL = %d, want %d + %d", withWAL, after, walAfterDelete)
	}
}

func TestActiveFootprintRejectsMissingInputs(t *testing.T) {
	if _, err := ActiveFootprint(context.Background(), nil, "siftail.db"); err == nil {
		t.Fatal("footprint without a database succeeded")
	}
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, _, stop := settingsTestDatabase(t, path)
	defer stop()
	if _, err := ActiveFootprint(context.Background(), db.Reader(), ""); err == nil {
		t.Fatal("footprint without a path succeeded")
	}
}

// Freed pages awaiting vacuum used to keep the measured file size high,
// so size retention deleted every event. With in-use pages it stops once the
// deletions themselves meet the target, even though almost nothing is
// vacuumed (one page per cycle here).
func TestCleanerSizeRetentionCountsFreedPagesTowardTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := settingsTestDatabase(t, path)
	defer stop()
	ctx := context.Background()
	store := NewStore(db.Reader(), coordinator)
	if _, err := store.Save(ctx, Input{
		AgeDays: MaximumAgeDays, MaxDatabaseGiB: 1,
	}); err != nil {
		t.Fatal(err)
	}
	insertRetentionSource(t, db.Writer())
	const events = 100
	insertPayloadEvents(t, db.Writer(), 1, events, 1, 100_000)
	truncateCheckpoint(t, db.Writer())
	base, err := ActiveFootprint(ctx, db.Reader(), path)
	if err != nil {
		t.Fatal(err)
	}
	// The minimum limit is 1 GiB, so the real footprint is scaled and offset
	// to start exactly at the 95% trigger. Reaching the 90% target then
	// needs (5% of 1 GiB)/scale ≈ 1.7 MB of deleted payload: 17 events.
	const scale = 32
	limit := int64(1) << 30
	offset := limit*95/100 - base*scale
	cleaner := NewCleaner(db.Reader(), coordinator, path, store, CleanerOptions{
		DeleteChunk: 5, VacuumPages: 1,
		Now: func() time.Time { return time.Unix(1, 0).UTC() },
		MeasureFootprint: func(ctx context.Context) (int64, error) {
			footprint, err := ActiveFootprint(ctx, db.Reader(), path)
			return offset + footprint*scale, err
		},
	})
	result, err := cleaner.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !result.SizeTriggered || !result.SizeTargetReached || result.EventsExhausted ||
		result.CheckpointBusy || result.SizeDeleted != 20 {
		t.Fatalf("size cleanup = %#v, want 20 deleted and the target reached", result)
	}
	if result.FootprintAfter > limit*90/100 {
		t.Fatalf("footprint after = %d, above the target", result.FootprintAfter)
	}
	var remaining, free int
	if err := db.Reader().QueryRow("SELECT count(*) FROM log_events").
		Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if err := db.Writer().QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
		t.Fatal(err)
	}
	if remaining != events-20 || free == 0 {
		t.Fatalf("remaining events %d, freelist %d pages", remaining, free)
	}
}

// Reclamation runs as bounded incremental-vacuum chunks, each its own
// writer turn, while ingestion commits continuously through the same
// coordinator. Run with TMPDIR on a real disk (AGENTS.md §17.2); the log line
// names the filesystem.
func TestCleanerReclaimsInBoundedChunksDuringConcurrentIngestion(t *testing.T) {
	directory := t.TempDir()
	t.Logf("temp directory filesystem: %s", testFilesystem(directory))
	path := filepath.Join(directory, "siftail.db")
	db, coordinator, stop := settingsTestDatabase(t, path)
	defer stop()
	ctx := context.Background()
	store := NewStore(db.Reader(), coordinator)
	if _, err := store.Save(ctx, Input{AgeDays: 1, MaxDatabaseGiB: 1}); err != nil {
		t.Fatal(err)
	}
	insertRetentionSource(t, db.Writer())
	now := time.Unix(200_000, 0).UTC()
	// Expired events fill the front of the file, retained ones its tail, so
	// the vacuum must relocate live pages into the freed space: about one
	// page per 3,000-byte event, 10,000 freed pages against the 8,192 cap.
	insertPayloadEvents(t, db.Writer(), 1, 10_000, 1, 3_000)
	insertPayloadEvents(t, db.Writer(), 10_001, 2_000, now.UnixMicro(), 3_000)
	truncateCheckpoint(t, db.Writer())
	fileBefore := fileSize(t, path)

	recording := &recordingCoordinator{Coordinator: coordinator}
	cleaner := NewCleaner(db.Reader(), recording, path, store, CleanerOptions{
		Now:              func() time.Time { return now },
		MeasureFootprint: func(context.Context) (int64, error) { return 1, nil },
	})

	var (
		ingestMu  sync.Mutex
		commitsAt []time.Time
		latencies []time.Duration
	)
	done := make(chan struct{})
	ingested := make(chan error, 1)
	go func() {
		for id := int64(100_000); ; id++ {
			select {
			case <-done:
				ingested <- nil
				return
			default:
			}
			submitted := time.Now()
			err := coordinator.Do(ctx, func(tx *sql.Tx) error {
				if _, err := tx.Exec(`INSERT INTO log_events(
					id,event_at_us,received_at_us,source_id,stream,level_normalized,
					message_raw,message_text
				) VALUES(?,?,?,1,'stdout','info',zeroblob(512),'live')`,
					id, now.UnixMicro(), now.UnixMicro()); err != nil {
					return err
				}
				// Recorded on the coordinator worker, so the time falls
				// strictly between two maintenance turns.
				ingestMu.Lock()
				commitsAt = append(commitsAt, time.Now())
				ingestMu.Unlock()
				return nil
			})
			if err != nil {
				ingested <- err
				return
			}
			ingestMu.Lock()
			latencies = append(latencies, time.Since(submitted))
			ingestMu.Unlock()
		}
	}()

	result, err := cleaner.RunOnce(ctx)
	close(done)
	if ingestErr := <-ingested; ingestErr != nil {
		t.Fatal(ingestErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if result.AgeDeleted != 10_000 || result.CheckpointBusy {
		t.Fatalf("cleanup result = %#v", result)
	}

	turns := recording.snapshot()
	vacuums := vacuumTurns(turns)
	var released int
	var maxChunk, maxTurn time.Duration
	for _, turn := range vacuums {
		released += turn.released
		if turn.released > defaultVacuumChunkPages {
			t.Fatalf("one writer turn released %d pages, chunk is %d",
				turn.released, defaultVacuumChunkPages)
		}
		maxChunk = max(maxChunk, turn.hold())
	}
	for _, turn := range turns {
		maxTurn = max(maxTurn, turn.hold())
	}
	t.Logf("%d maintenance turns, %d vacuum chunks released %d pages; "+
		"max chunk hold %v, max maintenance hold %v; file %d -> %d bytes",
		len(turns), len(vacuums), released, maxChunk, maxTurn,
		fileBefore, fileSize(t, path))
	// 10,000 freed pages exceed the 8,192-page cycle cap, so every chunk is
	// full: a full chunk proves the pragma ran to completion in one turn.
	if len(vacuums) != defaultVacuumPages/defaultVacuumChunkPages ||
		released != defaultVacuumPages {
		t.Fatalf("vacuum chunks = %d releasing %d pages, want %d releasing %d",
			len(vacuums), released,
			defaultVacuumPages/defaultVacuumChunkPages, defaultVacuumPages)
	}
	if maxChunk > maxVacuumChunkHold {
		t.Fatalf("vacuum chunk held the writer %v, ceiling %v", maxChunk, maxVacuumChunkHold)
	}

	ingestMu.Lock()
	defer ingestMu.Unlock()
	interleaved := 0
	for _, at := range commitsAt {
		if at.After(vacuums[0].finished) && at.Before(vacuums[len(vacuums)-1].started) {
			interleaved++
		}
	}
	var maxLatency time.Duration
	for _, latency := range latencies {
		maxLatency = max(maxLatency, latency)
	}
	t.Logf("%d ingestion commits, %d between vacuum chunks, max commit latency %v",
		len(commitsAt), interleaved, maxLatency)
	if interleaved == 0 {
		t.Fatal("no ingestion commit ran between vacuum chunks")
	}

	var count int
	if err := db.Reader().QueryRow(`SELECT count(*) FROM log_events
		WHERE id >= 100000`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(commitsAt) {
		t.Fatalf("stored ingestion events = %d, committed %d", count, len(commitsAt))
	}
	if err := database.QuickCheck(ctx, db.Reader()); err != nil {
		t.Fatal(err)
	}
}

func TestIncrementalVacuumReleasesWholeChunkAndRejectsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, _, stop := settingsTestDatabase(t, path)
	defer stop()
	insertRetentionSource(t, db.Writer())
	insertPayloadEvents(t, db.Writer(), 1, 300, 1, 3_000)
	if _, err := db.Writer().Exec("DELETE FROM log_events WHERE id <= 200"); err != nil {
		t.Fatal(err)
	}
	if _, err := incrementalVacuum(db.Writer(), 0); err == nil {
		t.Fatal("zero-page vacuum succeeded; it would release the whole freelist")
	}
	released, err := incrementalVacuum(db.Writer(), 64)
	if err != nil || released != 64 {
		t.Fatalf("released %d pages, err=%v; want 64", released, err)
	}
	released, err = incrementalVacuum(db.Writer(), 10_000)
	if err != nil || released == 0 || released >= 10_000 {
		t.Fatalf("draining vacuum released %d pages, err=%v", released, err)
	}
	released, err = incrementalVacuum(db.Writer(), 64)
	if err != nil || released != 0 {
		t.Fatalf("empty-freelist vacuum released %d pages, err=%v", released, err)
	}
	if err := database.QuickCheck(context.Background(), db.Reader()); err != nil {
		t.Fatal(err)
	}
}

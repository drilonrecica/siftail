package retention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

func BenchmarkRetentionDeleteChunk10000(b *testing.B) {
	for iteration := 0; iteration < b.N; iteration++ {
		b.StopTimer()
		path, db, coordinator, stop := benchmarkRetentionDatabase(b, iteration, 10_000, 1_024)
		store := NewStore(db.Reader(), coordinator)
		cleaner := NewCleaner(db.Reader(), coordinator, path, store, CleanerOptions{
			Now: func() time.Time { return time.Unix(200_000, 0).UTC() },
		})
		databaseBefore, walBefore := benchmarkFileSizes(b, path)
		pagesBefore, freeBefore := benchmarkPageCounts(b, db.Writer())
		b.StartTimer()
		result, err := cleaner.RunOnce(context.Background())
		b.StopTimer()
		if err != nil {
			b.Fatal(err)
		}
		if result.AgeDeleted != 10_000 {
			b.Fatalf("deleted = %d", result.AgeDeleted)
		}
		databaseAfter, walAfter := benchmarkFileSizes(b, path)
		pagesAfter, freeAfter := benchmarkPageCounts(b, db.Writer())
		b.ReportMetric(float64(databaseBefore)/(1<<20), "db_before_MiB")
		b.ReportMetric(float64(databaseAfter)/(1<<20), "db_after_MiB")
		b.ReportMetric(float64(walBefore)/(1<<20), "wal_before_MiB")
		b.ReportMetric(float64(walAfter)/(1<<20), "wal_after_MiB")
		b.ReportMetric(float64(pagesBefore), "pages_before")
		b.ReportMetric(float64(pagesAfter), "pages_after")
		b.ReportMetric(float64(freeBefore), "free_before")
		b.ReportMetric(float64(freeAfter), "free_after")
		stop()
	}
	b.SetBytes(10_000 * 1_024)
	b.ReportAllocs()
}

func BenchmarkRetentionWriterBaseline(b *testing.B) {
	for iteration := 0; iteration < b.N; iteration++ {
		b.StopTimer()
		_, _, coordinator, stop := benchmarkRetentionDatabase(b, iteration, 20_000, 256)
		b.StartTimer()
		latencies := benchmarkWriterCommits(b, coordinator, iteration)
		b.StopTimer()
		b.ReportMetric(float64(latencies[94].Microseconds()), "writer_p95_us")
		stop()
	}
	b.ReportAllocs()
}

func BenchmarkRetentionWriterInterference(b *testing.B) {
	for iteration := 0; iteration < b.N; iteration++ {
		b.StopTimer()
		path, db, coordinator, stop := benchmarkRetentionDatabase(b, iteration, 20_000, 256)
		store := NewStore(db.Reader(), coordinator)
		firstCommit := make(chan struct{}, 1)
		cleaner := NewCleaner(db.Reader(), coordinator, path, store, CleanerOptions{
			DeleteChunk: 1_000,
			Now:         func() time.Time { return time.Unix(200_000, 0).UTC() },
			AfterDelete: func(int64) {
				select {
				case firstCommit <- struct{}{}:
				default:
				}
			},
		})
		cleanupDone := make(chan error, 1)
		go func() {
			_, err := cleaner.RunOnce(context.Background())
			cleanupDone <- err
		}()
		<-firstCommit

		b.StartTimer()
		latencies := benchmarkWriterCommits(b, coordinator, iteration)
		if err := <-cleanupDone; err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		b.ReportMetric(float64(latencies[94].Microseconds()), "writer_p95_us")
		stop()
	}
	b.ReportAllocs()
}

// BenchmarkRetentionReclaimChunks measures one full reclamation cycle with
// relocation: 10,000 expired 3,000-byte events at the front of the file and
// 2,000 retained ones at its tail, so the vacuum moves live pages. It reports
// the longest vacuum-chunk and checkpoint writer holds.
func BenchmarkRetentionReclaimChunks(b *testing.B) {
	b.Logf("temp directory filesystem: %s", testFilesystem(b.TempDir()))
	for iteration := 0; iteration < b.N; iteration++ {
		b.StopTimer()
		path, db, coordinator, stop := benchmarkRetentionDatabase(b, iteration, 0, 0)
		insertPayloadEvents(b, db.Writer(), 1, 10_000, 1, 3_000)
		now := time.Unix(200_000, 0).UTC()
		insertPayloadEvents(b, db.Writer(), 10_001, 2_000, now.UnixMicro(), 3_000)
		truncateCheckpoint(b, db.Writer())
		store := NewStore(db.Reader(), coordinator)
		recording := &recordingCoordinator{Coordinator: coordinator}
		cleaner := NewCleaner(db.Reader(), recording, path, store, CleanerOptions{
			Now:              func() time.Time { return now },
			MeasureFootprint: func(context.Context) (int64, error) { return 1, nil },
		})
		databaseBefore := fileSize(b, path)
		b.StartTimer()
		result, err := cleaner.RunOnce(context.Background())
		b.StopTimer()
		if err != nil {
			b.Fatal(err)
		}
		if result.AgeDeleted != 10_000 || result.CheckpointBusy {
			b.Fatalf("cleanup result = %#v", result)
		}
		var maxChunk, maxCheckpoint, reclaim time.Duration
		var chunks, released int
		for _, turn := range recording.snapshot() {
			reclaim += turn.hold()
			if turn.released > 0 {
				chunks++
				released += turn.released
				maxChunk = max(maxChunk, turn.hold())
				continue
			}
			maxCheckpoint = max(maxCheckpoint, turn.hold())
		}
		b.ReportMetric(float64(maxChunk.Microseconds())/1_000, "max_chunk_ms")
		b.ReportMetric(float64(maxCheckpoint.Microseconds())/1_000, "max_checkpoint_ms")
		b.ReportMetric(float64(reclaim.Microseconds())/1_000, "reclaim_ms")
		b.ReportMetric(float64(chunks), "chunks")
		b.ReportMetric(float64(released), "pages_released")
		b.ReportMetric(float64(databaseBefore)/(1<<20), "db_before_MiB")
		b.ReportMetric(float64(fileSize(b, path))/(1<<20), "db_after_MiB")
		stop()
	}
}

func benchmarkWriterCommits(
	b *testing.B,
	coordinator *database.Coordinator,
	iteration int,
) []time.Duration {
	b.Helper()
	latencies := make([]time.Duration, 100)
	for index := range latencies {
		started := time.Now()
		err := coordinator.Do(context.Background(), func(tx *sql.Tx) error {
			id := 100_000 + iteration*1_000 + index
			_, err := tx.Exec(`INSERT INTO log_events(
				id,event_at_us,received_at_us,source_id,stream,level_normalized,
				message_raw,message_text
			) VALUES(?,?,?,?,?,?,?,?)`,
				id, int64(300_000_000_000), int64(300_000_000_000), 1,
				"stdout", "info", []byte("concurrent"), "concurrent",
			)
			return err
		})
		if err != nil {
			b.Fatal(err)
		}
		latencies[index] = time.Since(started)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	return latencies
}

func benchmarkRetentionDatabase(
	b *testing.B,
	iteration, events, payloadBytes int,
) (string, *database.DB, *database.Coordinator, func()) {
	b.Helper()
	path := filepath.Join(b.TempDir(), fmt.Sprintf("retention-%d.db", iteration))
	db, err := database.Open(context.Background(), path)
	if err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := database.NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	store := NewStore(db.Reader(), coordinator)
	if _, err := store.Save(context.Background(), Input{
		AgeDays: 1, MaxDatabaseGiB: 1,
	}); err != nil {
		b.Fatal(err)
	}
	insertRetentionSourceBenchmark(b, db.Writer())
	tx, err := db.Writer().Begin()
	if err != nil {
		b.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO log_events(
		id,event_at_us,received_at_us,source_id,stream,level_normalized,
		message_raw,message_text
	) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	payload := make([]byte, payloadBytes)
	for index := 1; index <= events; index++ {
		if _, err := statement.Exec(
			index, 1, 1, 1, "stdout", "info", payload, string(payload),
		); err != nil {
			b.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	stop := func() {
		coordinator.Close()
		cancel()
		<-done
		if err := db.Close(); err != nil {
			b.Fatal(err)
		}
	}
	return path, db, coordinator, stop
}

func insertRetentionSourceBenchmark(b *testing.B, db *sql.DB) {
	b.Helper()
	if _, err := db.Exec(`INSERT INTO servers(id,name,created_at_us)
		VALUES(1,'retention-server',1);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			first_seen_at_us,last_seen_at_us
		) VALUES(1,1,'project','environment','application','service',
			'project','environment','application','service',1,1)`); err != nil {
		b.Fatal(err)
	}
}

func benchmarkFileSizes(b *testing.B, path string) (int64, int64) {
	b.Helper()
	size := func(name string) int64 {
		info, err := os.Stat(name)
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		if err != nil {
			b.Fatal(err)
		}
		return info.Size()
	}
	return size(path), size(path + "-wal")
}

func benchmarkPageCounts(b *testing.B, db *sql.DB) (int64, int64) {
	b.Helper()
	var pages, free int64
	if err := db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		b.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
		b.Fatal(err)
	}
	return pages, free
}

package backup

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

const (
	// concurrentBackupBulkRows × concurrentBackupRowBytes ≈ 256 MiB of
	// retained events, the multi-hundred-MB fixture DAT-04 calls for.
	concurrentBackupBulkRows = 8192
	concurrentBackupRowBytes = 32 << 10
	// concurrentBackupBound is far beyond a healthy run (seconds) and far
	// below "never": the previous step-wise online backup restarted on every
	// concurrent commit and did not finish under this load.
	concurrentBackupBound = 90 * time.Second
	// concurrentBackupMinCommits is the number of committed ingestion
	// transactions the backup must overlap.
	concurrentBackupMinCommits = 200
	// concurrentBackupFirstID keeps continuously ingested rows apart from the
	// bulk fixture so the captured prefix can be checked exactly.
	concurrentBackupFirstID = 1_000_000
)

func TestCreateFullBackupCompletesUnderContinuousIngestion(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-hundred-MB continuous-ingestion backup")
	}
	fixture := newBackupFixture(t)
	seedFullBackupState(t, fixture.db.Writer())
	seedBulkEvents(t, fixture.db.Writer())

	var committed atomic.Int64
	var copyStartCommits, copyEndCommits int64
	snapshot := fixture.service.copy
	fixture.service.copy = func(
		ctx context.Context,
		sourcePath string,
		stagingPath string,
		progress func(database.SnapshotProgress),
	) error {
		copyStartCommits = committed.Load()
		err := snapshot(ctx, sourcePath, stagingPath, progress)
		copyEndCommits = committed.Load()
		return err
	}

	stopIngest := make(chan struct{})
	ingestDone := make(chan error, 1)
	go func() {
		ingestDone <- ingestContinuously(
			fixture.coordinator, stopIngest, &committed,
		)
	}()
	// The backup starts only once ingestion is demonstrably committing.
	waitForCommits(t, &committed, 20)

	output := filepath.Join(t.TempDir(), "under-ingestion.sqlite")
	ctx, cancel := context.WithTimeout(
		context.Background(), concurrentBackupBound,
	)
	defer cancel()
	startCommits := committed.Load()
	started := time.Now()
	result, backupErr := fixture.service.CreateFull(ctx, output, nil)
	elapsed := time.Since(started)
	endCommits := committed.Load()
	close(stopIngest)
	if err := <-ingestDone; err != nil {
		t.Fatalf("continuous ingestion: %v", err)
	}
	finalCommits := committed.Load()
	if backupErr != nil {
		t.Fatalf("backup under continuous ingestion after %s and %d commits: %v",
			elapsed, endCommits-startCommits, backupErr)
	}
	if elapsed >= concurrentBackupBound {
		t.Fatalf("backup took %s, bound %s", elapsed, concurrentBackupBound)
	}
	if overlapped := endCommits - startCommits; overlapped < concurrentBackupMinCommits {
		t.Fatalf("backup overlapped %d commits, want at least %d",
			overlapped, concurrentBackupMinCommits)
	}
	if copyEndCommits <= copyStartCommits {
		t.Fatalf("no commit landed while the snapshot was copied (%d → %d)",
			copyStartCommits, copyEndCommits)
	}
	t.Logf("backup %s: %d bytes in %s; %d commits during backup, %d during copy",
		result.Name, result.Bytes, elapsed,
		endCommits-startCommits, copyEndCommits-copyStartCommits)

	verified, err := Verify(context.Background(), output)
	if err != nil {
		t.Fatalf("verify artifact: %v", err)
	}
	if verified.SHA256 != result.SHA256 || verified.Bytes != result.Bytes ||
		verified.Type != TypeFull {
		t.Fatalf("verified = %#v, created = %#v", verified, result)
	}

	artifact, err := sql.Open("sqlite3", sqlitePath(output, "ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Close()
	var bulk int
	if err := artifact.QueryRow(
		"SELECT count(*) FROM log_events WHERE id < ?", concurrentBackupFirstID,
	).Scan(&bulk); err != nil || bulk != concurrentBackupBulkRows+1 {
		t.Fatalf("artifact bulk events = %d, %v", bulk, err)
	}
	// One snapshot means the captured ingested rows are exactly the first k
	// commits: no gaps, no torn transaction, nothing committed after the
	// snapshot began, and everything committed before the copy started.
	var captured, maxID int64
	if err := artifact.QueryRow(`SELECT count(*), coalesce(max(id),?)
		FROM log_events WHERE id >= ?`,
		concurrentBackupFirstID-1, concurrentBackupFirstID,
	).Scan(&captured, &maxID); err != nil {
		t.Fatal(err)
	}
	if maxID-concurrentBackupFirstID+1 != captured {
		t.Fatalf("captured %d ingested rows but highest id is %d: not one snapshot",
			captured, maxID)
	}
	if captured < copyStartCommits || captured > copyEndCommits {
		t.Fatalf("captured %d ingested rows, want between %d and %d",
			captured, copyStartCommits, copyEndCommits)
	}
	var sourceIngested int64
	if err := fixture.db.Reader().QueryRow(
		"SELECT count(*) FROM log_events WHERE id >= ?", concurrentBackupFirstID,
	).Scan(&sourceIngested); err != nil || sourceIngested != finalCommits {
		t.Fatalf("source ingested rows = %d, committed %d, %v",
			sourceIngested, finalCommits, err)
	}
}

func seedBulkEvents(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		WITH RECURSIVE n(i) AS (
			SELECT 2 UNION ALL SELECT i+1 FROM n WHERE i < ?
		)
		INSERT INTO log_events(
			id,event_at_us,received_at_us,source_id,stream,level_normalized,
			message_raw,message_text,attributes_json
		)
		SELECT i,i,i,1,'stdout','info',randomblob(?),hex(randomblob(?)),'{}'
		FROM n`,
		concurrentBackupBulkRows+1,
		concurrentBackupRowBytes/2, concurrentBackupRowBytes/4,
	); err != nil {
		t.Fatal(err)
	}
}

// ingestContinuously commits one small event per transaction through the
// single writer coordinator, as ingestion does, until stop is closed.
func ingestContinuously(
	coordinator *database.Coordinator,
	stop <-chan struct{},
	committed *atomic.Int64,
) error {
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		id := concurrentBackupFirstID + committed.Load()
		if err := coordinator.Do(
			context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO log_events(
					id,event_at_us,received_at_us,source_id,stream,
					level_normalized,message_raw,message_text,attributes_json
				) VALUES(?,?,?,1,'stdout','info',
					CAST('continuous' AS BLOB),'continuous','{}')`,
					id, id, id,
				)
				return err
			},
		); err != nil {
			return err
		}
		committed.Add(1)
	}
}

func waitForCommits(t *testing.T, committed *atomic.Int64, want int64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for committed.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("ingestion committed %d transactions, want %d",
				committed.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

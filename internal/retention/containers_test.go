package retention

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestCleanerPrunesUnreferencedContainersPastAgeCutoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := settingsTestDatabase(t, path)
	defer stop()
	store := NewStore(db.Reader(), coordinator)
	if _, err := store.Save(context.Background(), Input{AgeDays: 1, MaxDatabaseGiB: 1}); err != nil {
		t.Fatal(err)
	}
	insertRetentionSource(t, db.Writer())
	now := time.Unix(500_000, 0).UTC()
	cutoff := now.AddDate(0, 0, -1).UnixMicro()
	insertRetentionContainer(t, db.Writer(), 10, 1, cutoff-30) // old, unreferenced
	insertRetentionContainer(t, db.Writer(), 11, 1, cutoff-20) // old, retained event
	insertRetentionContainer(t, db.Writer(), 12, 1, cutoff+10) // recent, unreferenced
	insertRetentionContainer(t, db.Writer(), 13, 1, cutoff-10) // old, event purged this cycle
	insertRetentionContainer(t, db.Writer(), 14, 1, cutoff-5)  // old, unreferenced
	insertContainerEvent(t, db.Writer(), 1, 1, 11, cutoff+1)
	insertContainerEvent(t, db.Writer(), 2, 1, 13, cutoff-1)

	cleaner := NewCleaner(db.Reader(), coordinator, path, store, CleanerOptions{
		DeleteChunk: 10, VacuumPages: 1, ContainerPruneChunk: 1,
		Now:              func() time.Time { return now },
		MeasureFootprint: func(context.Context) (int64, error) { return 1, nil },
	})
	result, err := cleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.AgeDeleted != 1 || result.ContainersPruned != 3 {
		t.Fatalf("cleanup result = %#v", result)
	}
	assertContainerIDs(t, db.Reader(), []int64{11, 12})
	assertRetentionEventIDs(t, db.Reader(), []int64{1})

	// A second cycle has nothing left to prune.
	result, err = cleaner.RunOnce(context.Background())
	if err != nil || result.ContainersPruned != 0 {
		t.Fatalf("idle cleanup = %#v, %v", result, err)
	}
	if err := database.IntegrityCheck(context.Background(), db.Writer()); err != nil {
		t.Fatal(err)
	}
}

func TestCleanerPrunesOldestUnreferencedContainersAboveServerTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := settingsTestDatabase(t, path)
	defer stop()
	store := NewStore(db.Reader(), coordinator)
	if _, err := store.Save(context.Background(), Input{AgeDays: 1, MaxDatabaseGiB: 1}); err != nil {
		t.Fatal(err)
	}
	insertRetentionSource(t, db.Writer())
	for server := int64(2); server <= 3; server++ {
		if _, err := db.Writer().Exec(`INSERT INTO servers(id,name,created_at_us) VALUES(?,?,1);
			INSERT INTO sources(
				id,server_id,project_key,environment_key,application_key,service_key,
				project_label,environment_label,application_label,service_label,
				first_seen_at_us,last_seen_at_us
			) VALUES(?,?,'p','e','a','s','p','e','a','s',1,1)`,
			server, fmt.Sprintf("server-%d", server), server, server); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Unix(500_000, 0).UTC()
	recent := now.UnixMicro() - 1_000

	// Server 1 holds 15 recent instances (a cap lowered below its count):
	// the oldest three have retained events, so the six oldest unreferenced
	// instances go, leaving it at the target of 9.
	for i := int64(0); i < 15; i++ {
		insertRetentionContainer(t, db.Writer(), 100+i, 1, recent+i)
	}
	for i := int64(0); i < 3; i++ {
		insertContainerEvent(t, db.Writer(), 1+i, 1, 100+i, recent)
	}
	// Server 2 is at the cap but every instance is referenced: nothing goes.
	for i := int64(0); i < 10; i++ {
		insertRetentionContainer(t, db.Writer(), 200+i, 2, recent+i)
		insertContainerEvent(t, db.Writer(), 20+i, 2, 200+i, recent)
	}
	// Server 3 is at its target: nothing goes.
	for i := int64(0); i < 9; i++ {
		insertRetentionContainer(t, db.Writer(), 300+i, 3, recent+i)
	}

	cleaner := NewCleaner(db.Reader(), coordinator, path, store, CleanerOptions{
		VacuumPages: 1, ContainerLimit: 10, ContainerPruneChunk: 2,
		Now:              func() time.Time { return now },
		MeasureFootprint: func(context.Context) (int64, error) { return 1, nil },
	})
	result, err := cleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.AgeDeleted != 0 || result.ContainersPruned != 6 {
		t.Fatalf("cleanup result = %#v", result)
	}
	want := []int64{100, 101, 102, 109, 110, 111, 112, 113, 114}
	for i := int64(0); i < 10; i++ {
		want = append(want, 200+i)
	}
	for i := int64(0); i < 9; i++ {
		want = append(want, 300+i)
	}
	assertContainerIDs(t, db.Reader(), want)
	if err := database.IntegrityCheck(context.Background(), db.Writer()); err != nil {
		t.Fatal(err)
	}
}

// Before pruning, a Server that had cycled through the cap rejected
// every new container forever, even after retention purged all its events.
func TestContainerPruneProbeUsesContainerEventIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, _, stop := settingsTestDatabase(t, path)
	defer stop()
	rows, err := db.Reader().Query(`EXPLAIN QUERY PLAN
		SELECT c.id FROM container_instances AS c
		WHERE c.last_seen_at_us < 1
		AND NOT EXISTS (
			SELECT 1 FROM log_events AS e WHERE e.container_instance_id = c.id
		)
		ORDER BY c.last_seen_at_us, c.id LIMIT 10`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "log_events_container_time_idx") {
		t.Fatalf("prune probe plan does not use the container event index:\n%s", joined)
	}
}

func insertRetentionContainer(t *testing.T, db *sql.DB, id, sourceID, lastSeen int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO container_instances(
		id,source_id,container_id,container_name,first_seen_at_us,last_seen_at_us
	) VALUES(?,?,?,?,?,?)`, id, sourceID, fmt.Sprintf("id-%d", id), "replica", lastSeen, lastSeen); err != nil {
		t.Fatal(err)
	}
}

func insertContainerEvent(t *testing.T, db *sql.DB, id, sourceID, containerID, at int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO log_events(
		id,event_at_us,received_at_us,source_id,container_instance_id,stream,level_normalized,
		message_raw,message_text
	) VALUES(?,?,?,?,?,?,?,?,?)`,
		id, at, at, sourceID, containerID, "stdout", "info", []byte("event"), "event",
	); err != nil {
		t.Fatal(err)
	}
}

func assertContainerIDs(t *testing.T, db *sql.DB, want []int64) {
	t.Helper()
	rows, err := db.Query("SELECT id FROM container_instances ORDER BY id")
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
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("container instance IDs = %v, want %v", got, want)
	}
}

package status

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/retention"
)

func TestStoreReadsOnlyBoundedSanitizedFacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Writer().Exec(`INSERT INTO servers(id,name,created_at_us)
		VALUES(1,'server',1);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			first_seen_at_us,last_seen_at_us
		) VALUES(1,1,'p','e','a','s','p','e','a','s',1,1);
		INSERT INTO log_events(
			id,event_at_us,received_at_us,source_id,stream,level_normalized,
			message_raw,message_text,attributes_json
		) VALUES(1,10,20,1,'stdout','info',
			CAST('private-payload-marker' AS BLOB),'private-payload-marker',
			'{"authorization":"private-secret-marker"}')`); err != nil {
		t.Fatal(err)
	}
	state := NewState(time.Unix(1, 0))
	state.SetWriterReady(true)
	state.RecordIngestAccepted(1, time.UnixMicro(30))
	store := NewStore(
		db.Reader(), path, nil, retention.NewStore(db.Reader(), nil), state,
	)
	store.now = func() time.Time { return time.UnixMicro(30) }
	snapshot, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.EventsToday != 1 || snapshot.OldestEventUS == nil ||
		*snapshot.OldestEventUS != 10 || snapshot.NewestEventUS == nil ||
		*snapshot.NewestEventUS != 10 || snapshot.DatabaseBytes <= 0 ||
		snapshot.Retention.AgeDays != retention.DefaultAgeDays {
		t.Fatalf("status snapshot = %#v", snapshot)
	}
	rows, err := db.Reader().Query("EXPLAIN QUERY PLAN " + statusEventBoundsSQL)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if !strings.Contains(plan.String(), "log_events_time_idx") {
		t.Fatalf("status event bounds plan = %s", plan.String())
	}
}

func TestStoreResolvesBoundedConflictServerNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Writer().Exec(`INSERT INTO servers(id,name,created_at_us)
		VALUES(1,'alpha',1),(2,'beta',1)`); err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1, 0).UTC()
	state := NewState(started)
	state.RecordIngestConflicts(1, 2, started.Add(time.Second))
	state.RecordIngestConflicts(99, 1, started.Add(2*time.Second))
	for server := int64(100); server < 100+ConflictDisplayLimit; server++ {
		state.RecordIngestConflicts(server, 1, started)
	}
	store := NewStore(db.Reader(), path, nil, retention.NewStore(db.Reader(), nil), state)
	snapshot, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Conflicts) != ConflictDisplayLimit ||
		len(snapshot.State.Conflicts) != ConflictDisplayLimit+2 {
		t.Fatalf("conflict rows = %d shown of %d", len(snapshot.Conflicts), len(snapshot.State.Conflicts))
	}
	// Most recent first; a removed Server has no name.
	if got := snapshot.Conflicts[0]; got.ServerID != 99 || got.ServerName != "" {
		t.Fatalf("first conflict row = %#v", got)
	}
	if got := snapshot.Conflicts[1]; got.ServerID != 1 || got.ServerName != "alpha" || got.Dropped != 2 {
		t.Fatalf("second conflict row = %#v", got)
	}
}

func TestStoreResolvesLateEventServerNamesAlongsideConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Writer().Exec(`INSERT INTO servers(id,name,created_at_us)
		VALUES(1,'alpha',1),(2,'beta',1)`); err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1, 0).UTC()
	state := NewState(started)
	state.RecordIngestConflicts(1, 1, started)
	state.RecordIngestLate(2, 4, started.Add(time.Second))
	state.RecordIngestLate(77, 1, started.Add(2*time.Second))
	for server := int64(100); server < 100+LateDisplayLimit; server++ {
		state.RecordIngestLate(server, 1, started)
	}
	store := NewStore(db.Reader(), path, nil, retention.NewStore(db.Reader(), nil), state)
	snapshot, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Late) != LateDisplayLimit || len(snapshot.State.Late) != LateDisplayLimit+2 {
		t.Fatalf("late rows = %d shown of %d", len(snapshot.Late), len(snapshot.State.Late))
	}
	if got := snapshot.Late[0]; got.ServerID != 77 || got.ServerName != "" {
		t.Fatalf("first late row = %#v", got)
	}
	if got := snapshot.Late[1]; got.ServerID != 2 || got.ServerName != "beta" || got.Late != 4 {
		t.Fatalf("second late row = %#v", got)
	}
	if len(snapshot.Conflicts) != 1 || snapshot.Conflicts[0].ServerName != "alpha" {
		t.Fatalf("conflict rows = %#v", snapshot.Conflicts)
	}
}

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

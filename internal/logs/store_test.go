package logs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestStoreRecentUsesCanonicalOrderAndBounds(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writer := db.Writer()
	if _, err := writer.Exec(`INSERT INTO servers(id,name,created_at_us) VALUES(1,'server',1);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			first_seen_at_us,last_seen_at_us
		) VALUES(1,1,'p','e','a','s','p','e','a','s',1,1);
		INSERT INTO log_events(
			event_at_us,received_at_us,source_id,stream,level_normalized,message_raw,message_text
		) VALUES
			(10,10,1,'stdout','info',x'6f6c646572','older'),
			(20,20,1,'stdout','info',x'6669727374','first'),
			(20,20,1,'stdout','info',x'7365636f6e64','second')`); err != nil {
		t.Fatal(err)
	}
	events, err := NewStore(db.Reader()).Recent(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].MessageText != "second" || events[1].MessageText != "first" {
		t.Fatalf("events = %#v", events)
	}
	if _, err := NewStore(db.Reader()).Recent(context.Background(), 501); err == nil {
		t.Fatal("unbounded recent query accepted")
	}
}

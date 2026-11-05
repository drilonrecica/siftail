package retention_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/retention"
)

// This test drives the ingestion writer, which itself depends on retention,
// so it lives in the external test package.
func TestRecreatedContainerAcceptedAgainAfterPruningUnderTheCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := retention.SettingsTestDatabase(t, path)
	defer stop()
	store := retention.NewStore(db.Reader(), coordinator)
	if _, err := store.Save(context.Background(), retention.Input{AgeDays: 1, MaxDatabaseGiB: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec("INSERT INTO servers(id,name,created_at_us) VALUES(1,'cap-server',1)"); err != nil {
		t.Fatal(err)
	}
	const limit = 4
	writer := ingest.NewBatchWriterWithOptions(coordinator, nil, ingest.WriterOptions{ContainerLimit: limit})
	day := time.Unix(1_000_000, 0).UTC()
	event := func(deployment int, at time.Time) logs.CanonicalEvent {
		return logs.CanonicalEvent{
			EventAtUS: at.UnixMicro(), ReceivedAtUS: at.UnixMicro(),
			Source: logs.SourceIdentity{
				ServerID: 1, Project: "p", Environment: "e", Application: "a", Service: "web",
				ProjectLabel: "p", EnvLabel: "e", AppLabel: "a", ServiceLabel: "web",
			},
			Container:   &logs.ContainerIdentity{ID: fmt.Sprintf("deployment-%d", deployment), Name: "app-web-1"},
			Stream:      logs.StreamStdout,
			Level:       logs.LevelInfo,
			MessageRaw:  []byte("hello"),
			MessageText: "hello",
		}
	}
	persist := func(deployment int, at time.Time) error {
		return writer.Persist(context.Background(), &ingest.WriteBatch{
			Events: []logs.CanonicalEvent{event(deployment, at)}, AuthenticatedServerID: 1,
		})
	}
	for deployment := 0; deployment < limit; deployment++ {
		if err := persist(deployment, day); err != nil {
			t.Fatalf("deployment %d: %v", deployment, err)
		}
	}
	later := day.AddDate(0, 0, 3)
	var rejected *ingest.Error
	if err := persist(limit, later); !errors.As(err, &rejected) || rejected.Category != ingest.CategoryForbidden {
		t.Fatalf("deployment beyond the cap = %v", err)
	}

	cleaner := retention.NewCleaner(db.Reader(), coordinator, path, store, retention.CleanerOptions{
		VacuumPages: 1, ContainerLimit: limit,
		Now:              func() time.Time { return later },
		MeasureFootprint: func(context.Context) (int64, error) { return 1, nil },
	})
	result, err := cleaner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.AgeDeleted != limit || result.ContainersPruned != limit {
		t.Fatalf("cleanup result = %#v", result)
	}
	if err := persist(limit, later); err != nil {
		t.Fatalf("recreated container after pruning: %v", err)
	}
	var instances int
	if err := db.Reader().QueryRow("SELECT count(*) FROM container_instances").Scan(&instances); err != nil || instances != 1 {
		t.Fatalf("container instances after re-ingestion = %d, %v", instances, err)
	}
}

// TestAcknowledgedLateEventIsNotPurgedByTheNextAgeRun is the audit DAT-10
// scenario: a client with a wrong clock, or a replayed backlog, must not get
// 204 for events the next retention run deletes (ADR 0008).
func TestAcknowledgedLateEventIsNotPurgedByTheNextAgeRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siftail.db")
	db, coordinator, stop := retention.SettingsTestDatabase(t, path)
	defer stop()
	store := retention.NewStore(db.Reader(), coordinator)
	if _, err := store.Save(context.Background(), retention.Input{AgeDays: 14, MaxDatabaseGiB: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec("INSERT INTO servers(id,name,created_at_us) VALUES(1,'clock-server',1)"); err != nil {
		t.Fatal(err)
	}
	writer := ingest.NewBatchWriter(coordinator, nil)
	received := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	event := func(message string, eventAt, receivedAt time.Time) logs.CanonicalEvent {
		return logs.CanonicalEvent{
			EventAtUS: eventAt.UnixMicro(), ReceivedAtUS: receivedAt.UnixMicro(),
			Source: logs.SourceIdentity{
				ServerID: 1, Project: "p", Environment: "e", Application: "a", Service: "web",
				ProjectLabel: "p", EnvLabel: "e", AppLabel: "a", ServiceLabel: "web",
			},
			Stream: logs.StreamStdout, Level: logs.LevelInfo,
			MessageRaw: []byte(message), MessageText: message,
		}
	}
	result, err := writer.PersistWithResult(context.Background(), &ingest.WriteBatch{
		AuthenticatedServerID: 1,
		Events: []logs.CanonicalEvent{
			// A host clock 60 days behind.
			event("wrong clock", received.AddDate(0, 0, -60), received),
			// A host clock 10 years ahead, received 20 days ago.
			event("future clock", received.AddDate(10, 0, 0), received.AddDate(0, 0, -20)),
		},
	})
	if err != nil || result.LateEvents != 1 {
		t.Fatalf("persist result = %#v, %v", result, err)
	}
	countMessages := func() []string {
		t.Helper()
		rows, err := db.Reader().Query("SELECT message_text FROM log_events ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var messages []string
		for rows.Next() {
			var message string
			if err := rows.Scan(&message); err != nil {
				t.Fatal(err)
			}
			messages = append(messages, message)
		}
		return messages
	}
	run := func(now time.Time) retention.CleanupResult {
		t.Helper()
		cleaner := retention.NewCleaner(db.Reader(), coordinator, path, store, retention.CleanerOptions{
			VacuumPages: 1, Now: func() time.Time { return now },
			MeasureFootprint: func(context.Context) (int64, error) { return 1, nil },
		})
		result, err := cleaner.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	// The next run, an hour later: the late event stays, and the
	// future-skewed event expires by its receive time.
	if result := run(received.Add(time.Hour)); result.AgeDeleted != 1 {
		t.Fatalf("next-run cleanup = %#v", result)
	}
	if got := countMessages(); len(got) != 1 || got[0] != "wrong clock" {
		t.Fatalf("after next run = %v", got)
	}
	// Exactly at the age limit after receipt the event is still retained;
	// past it, it expires like any other event.
	if result := run(received.AddDate(0, 0, 14)); result.AgeDeleted != 0 {
		t.Fatalf("boundary cleanup = %#v", result)
	}
	if result := run(received.AddDate(0, 0, 14).Add(time.Microsecond)); result.AgeDeleted != 1 {
		t.Fatalf("expiry cleanup = %#v", result)
	}
}

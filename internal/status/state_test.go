package status

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/retention"
)

func TestStateReadinessRateRecoveryAndDiagnosticBound(t *testing.T) {
	started := time.Unix(1_000, 0).UTC()
	state := NewState(started)
	if state.Ready(false) {
		t.Fatal("state was ready before writer")
	}
	state.SetWriterReady(true)
	if !state.Ready(false) || state.Ready(true) {
		t.Fatal("readiness did not honor writer and shutdown")
	}
	for index := 0; index < diagnosticCapacity+6; index++ {
		state.RecordIngestRejected(
			ingest.CategoryStorageFull, true,
			started.Add(time.Duration(index)*time.Second),
		)
	}
	if state.Ready(false) {
		t.Fatal("storage degradation remained ready")
	}
	state.RecordIngestAccepted(5, started.Add(70*time.Second))
	state.RecordIngestAccepted(3, started.Add(71*time.Second))
	if !state.Ready(false) {
		t.Fatal("successful commit did not recover readiness")
	}
	state.RecordCleanup(retention.CleanupResult{
		AgeDeleted: 10, CheckpointBusy: true,
	}, started.Add(72*time.Second))

	snapshot := state.Snapshot(started.Add(72 * time.Second))
	if snapshot.AcceptedBatches != 2 || snapshot.AcceptedEvents != 8 ||
		snapshot.EventsToday != 8 ||
		snapshot.RejectedBatches != diagnosticCapacity+6 ||
		snapshot.RecentEvents != 8 || len(snapshot.Diagnostics) != diagnosticCapacity ||
		snapshot.LastDatabaseError == nil || snapshot.LastCleanup == nil ||
		snapshot.LastCleanupResult.AgeDeleted != 10 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Diagnostics[0].Summary !=
		"Ingestion could not commit because storage was unavailable." {
		t.Fatalf("unsafe diagnostic = %#v", snapshot.Diagnostics[0])
	}
	for _, diagnostic := range snapshot.Diagnostics {
		if err := diagnostic.Validate(); err != nil {
			t.Fatalf("invalid bounded diagnostic %#v: %v", diagnostic, err)
		}
	}
	if later := state.Snapshot(started.Add(132 * time.Second)); later.RecentEvents != 0 {
		t.Fatalf("expired recent events = %d", later.RecentEvents)
	}
	state.RecordCleanup(retention.CleanupResult{
		SizeTriggered: true, EventsExhausted: true,
	}, started.Add(133*time.Second))
	state.RecordIngestAccepted(1, started.Add(134*time.Second))
	if state.Ready(false) ||
		state.Snapshot(started.Add(134*time.Second)).DegradedCategory !=
			"retention_target_unmet" {
		t.Fatal("successful ingest cleared critical retention degradation")
	}
	state.RecordCleanup(retention.CleanupResult{
		SizeTriggered: true, SizeTargetReached: true,
	}, started.Add(135*time.Second))
	if !state.Ready(false) {
		t.Fatal("successful retention recovery did not restore readiness")
	}
}

func TestDiagnosticInputIsClosedSanitizedAndBounded(t *testing.T) {
	state := NewState(time.Now())
	at := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	recovered := at.Add(time.Minute)
	if err := state.RecordDiagnostic(DiagnosticInput{
		At: at, Component: "database", Category: "database_check_succeeded",
		RequestID: "request-safe-1", RecoveredAt: &recovered,
	}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []DiagnosticInput{
		{Component: "database", Category: "database_check_succeeded"},
		{At: at, Component: "database", Category: "private-payload-token"},
		{
			At: at, Component: "database", Category: "database_check_succeeded",
			RequestID: "request\nprivate",
		},
		{
			At: at, Component: "database", Category: "database_check_succeeded",
			RecoveredAt: func() *time.Time {
				value := at.Add(-time.Second)
				return &value
			}(),
		},
	} {
		if err := state.RecordDiagnostic(input); err == nil {
			t.Fatalf("unsafe diagnostic accepted: %#v", input)
		}
	}
	for index := 0; index < diagnosticCapacity+5; index++ {
		if err := state.RecordDiagnostic(DiagnosticInput{
			At:        at.Add(time.Duration(index+1) * time.Second),
			Component: "audit", Category: "audit_cleanup",
		}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := state.Snapshot(at.Add(time.Hour))
	if len(snapshot.Diagnostics) != diagnosticCapacity ||
		snapshot.Diagnostics[0].Category != "audit_cleanup" ||
		snapshot.Diagnostics[diagnosticCapacity-1].At != at.Add(6*time.Second) {
		t.Fatalf("bounded diagnostics = %#v", snapshot.Diagnostics)
	}
	for _, diagnostic := range snapshot.Diagnostics {
		if err := diagnostic.Validate(); err != nil {
			t.Fatalf("invalid diagnostic %#v: %v", diagnostic, err)
		}
		for _, forbidden := range []string{
			"private", "payload", "token", "password", "authorization",
		} {
			if strings.Contains(strings.ToLower(diagnostic.Summary), forbidden) {
				t.Fatalf("diagnostic summary exposed %q: %#v",
					forbidden, diagnostic)
			}
		}
	}
}

func TestStateConcurrentRecordingAndSnapshot(t *testing.T) {
	state := NewState(time.Now())
	state.SetWriterReady(true)
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for index := 0; index < 100; index++ {
				at := time.Unix(int64(1_000+index), 0)
				if worker%2 == 0 {
					state.RecordIngestAccepted(1, at)
				} else {
					state.RecordIngestRejected(
						ingest.CategoryBadRequest, false, at,
					)
				}
				_ = state.Snapshot(at)
			}
		}(worker)
	}
	workers.Wait()
	snapshot := state.Snapshot(time.Unix(1_100, 0))
	if snapshot.AcceptedEvents != 800 || snapshot.AcceptedBatches != 800 ||
		snapshot.RejectedBatches != 800 || len(snapshot.Diagnostics) != 0 {
		t.Fatalf("concurrent snapshot = %#v", snapshot)
	}
}

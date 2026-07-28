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
		state.IngestUnavailable() != ingest.CategoryStorageFull ||
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

func TestStateGatesKnownDatabaseFailureAndRecordsProbeRecovery(t *testing.T) {
	at := time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC)
	state := NewState(at.Add(-time.Hour))
	state.SetWriterReady(true)
	state.RecordIngestRejected(ingest.CategoryStorageFull, true, at)
	if got := state.IngestUnavailable(); got != ingest.CategoryStorageFull {
		t.Fatalf("ingestion gate = %q", got)
	}
	if state.DatabaseWritable() || state.Ready(false) {
		t.Fatal("full storage remained writable or ready")
	}

	recoveredAt := at.Add(5 * time.Second)
	state.RecordDatabaseRecovered(recoveredAt)
	if got := state.IngestUnavailable(); got != "" ||
		!state.DatabaseWritable() || !state.Ready(false) {
		t.Fatalf("state did not recover: gate=%q snapshot=%#v",
			got, state.Snapshot(recoveredAt))
	}
	snapshot := state.Snapshot(recoveredAt)
	if snapshot.LastDatabaseError == nil ||
		snapshot.LastDatabaseError.RecoveredAt == nil ||
		!snapshot.LastDatabaseError.RecoveredAt.Equal(recoveredAt) ||
		len(snapshot.Diagnostics) != 2 ||
		snapshot.Diagnostics[0].Category != "storage_recovered" ||
		snapshot.Diagnostics[1].RecoveredAt == nil {
		t.Fatalf("recovery diagnostics = %#v", snapshot)
	}

	state.RecordDatabaseRecovered(recoveredAt.Add(time.Second))
	if got := len(state.Snapshot(recoveredAt.Add(time.Second)).Diagnostics); got != 2 {
		t.Fatalf("healthy probe added diagnostics: %d", got)
	}
}

func TestBackupDiagnosticsUseClosedPayloadFreeSummaries(t *testing.T) {
	state := NewState(time.Now())
	at := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)
	for _, category := range []string{"backup_succeeded", "backup_failed"} {
		if err := state.RecordDiagnostic(DiagnosticInput{
			At: at, Component: "backup", Category: category,
		}); err != nil {
			t.Fatal(err)
		}
	}
	diagnostics := state.Snapshot(at).Diagnostics
	if len(diagnostics) != 2 ||
		diagnostics[0].Summary !=
			"A backup did not produce a verified artifact." ||
		diagnostics[1].Summary !=
			"A backup completed and passed verification." {
		t.Fatalf("backup diagnostics = %#v", diagnostics)
	}
}

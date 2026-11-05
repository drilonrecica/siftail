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

func TestStateBatchScopedIngestFailuresNeverDegrade(t *testing.T) {
	at := time.Unix(2_000, 0).UTC()
	state := NewState(at)
	state.SetWriterReady(true)
	for _, category := range []ingest.ErrorCategory{
		ingest.CategoryInternal, ingest.CategoryBadRequest,
		ingest.CategoryConflict, ingest.CategoryForbidden,
	} {
		state.RecordIngestRejected(category, false, at)
	}
	if !state.Ready(false) || !state.DatabaseWritable() ||
		state.IngestUnavailable() != "" || state.HealthSeverity() != "Healthy" {
		t.Fatal("a batch-scoped rejection degraded ingestion")
	}
	snapshot := state.Snapshot(at)
	if snapshot.RejectedBatches != 4 || snapshot.Degraded ||
		snapshot.LastDatabaseError != nil || len(snapshot.Diagnostics) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	diagnostic := snapshot.Diagnostics[0]
	if diagnostic.Component != "ingestion" ||
		diagnostic.Category != "batch_internal_error" ||
		diagnostic.Severity != "attention" {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
	if err := diagnostic.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStateCountsConflictsPerServerWithRateLimitedDiagnostic(t *testing.T) {
	started := time.Unix(10_000, 0).UTC()
	state := NewState(started)
	state.RecordIngestConflicts(7, 2, started)
	state.RecordIngestConflicts(7, 1, started.Add(30*time.Second))
	state.RecordIngestConflicts(8, 4, started.Add(40*time.Second))
	state.RecordIngestConflicts(7, 1, started.Add(61*time.Second))
	state.RecordIngestConflicts(7, 0, started.Add(62*time.Second))
	state.RecordIngestConflicts(0, 5, started.Add(63*time.Second))

	snapshot := state.Snapshot(started.Add(70 * time.Second))
	if snapshot.ConflictsDropped != 8 {
		t.Fatalf("total conflicts = %d, want 8", snapshot.ConflictsDropped)
	}
	want := []ServerConflicts{
		{ServerID: 7, Dropped: 4, LastAt: started.Add(61 * time.Second)},
		{ServerID: 8, Dropped: 4, LastAt: started.Add(40 * time.Second)},
	}
	if len(snapshot.Conflicts) != len(want) {
		t.Fatalf("per-Server conflicts = %#v", snapshot.Conflicts)
	}
	for index := range want {
		if snapshot.Conflicts[index] != want[index] {
			t.Fatalf("per-Server conflicts = %#v, want %#v", snapshot.Conflicts, want)
		}
	}
	// Server 7: one at 0s, suppressed at 30s, another at 61s; Server 8: one.
	diagnostics := 0
	for _, diagnostic := range snapshot.Diagnostics {
		if diagnostic.Component != "ingestion" || diagnostic.Category != "conflicting_duplicate" {
			t.Fatalf("unexpected diagnostic %#v", diagnostic)
		}
		if err := diagnostic.Validate(); err != nil {
			t.Fatalf("conflict diagnostic is not in the closed table: %v", err)
		}
		if diagnostic.RequestID != "" || strings.Contains(diagnostic.Summary, "7") {
			t.Fatalf("conflict diagnostic carries attribution: %#v", diagnostic)
		}
		diagnostics++
	}
	if diagnostics != 3 {
		t.Fatalf("conflict diagnostics = %d, want 3", diagnostics)
	}
	// Conflicts are batch-scoped outcomes of committed requests.
	state.SetWriterReady(true)
	if !state.Ready(false) || state.HealthSeverity() != "Healthy" {
		t.Fatal("conflicts changed readiness or health")
	}
}

func TestStateConflictCountersAreBounded(t *testing.T) {
	started := time.Unix(20_000, 0).UTC()
	state := NewState(started)
	for server := int64(1); server <= serverCounterCapacity+5; server++ {
		state.RecordIngestConflicts(server, 1, started.Add(time.Duration(server)*time.Second))
	}
	snapshot := state.Snapshot(started)
	if len(snapshot.Conflicts) != serverCounterCapacity ||
		snapshot.ConflictsDropped != serverCounterCapacity+5 {
		t.Fatalf("bounded conflicts = %d rows, %d total",
			len(snapshot.Conflicts), snapshot.ConflictsDropped)
	}
	// The servers with the oldest occurrences were evicted.
	if last := snapshot.Conflicts[len(snapshot.Conflicts)-1]; last.ServerID != 6 {
		t.Fatalf("oldest retained Server = %d, want 6", last.ServerID)
	}
	if first := snapshot.Conflicts[0]; first.ServerID != serverCounterCapacity+5 {
		t.Fatalf("newest Server = %d", first.ServerID)
	}
}

func TestStateCountsLateEventsPerServerWithHourlyDiagnostic(t *testing.T) {
	started := time.Unix(30_000, 0).UTC()
	state := NewState(started)
	state.RecordIngestLate(7, 3, started)
	state.RecordIngestLate(7, 2, started.Add(59*time.Minute))
	state.RecordIngestLate(8, 1, started.Add(10*time.Minute))
	state.RecordIngestLate(7, 1, started.Add(61*time.Minute))
	state.RecordIngestLate(7, 0, started.Add(62*time.Minute))
	state.RecordIngestLate(0, 5, started.Add(63*time.Minute))

	snapshot := state.Snapshot(started.Add(70 * time.Minute))
	if snapshot.LateEvents != 7 || snapshot.ConflictsDropped != 0 {
		t.Fatalf("late total = %d, conflicts = %d", snapshot.LateEvents, snapshot.ConflictsDropped)
	}
	want := []ServerLateEvents{
		{ServerID: 7, Late: 6, LastAt: started.Add(61 * time.Minute)},
		{ServerID: 8, Late: 1, LastAt: started.Add(10 * time.Minute)},
	}
	if len(snapshot.Late) != len(want) || len(snapshot.Conflicts) != 0 {
		t.Fatalf("per-Server late = %#v, conflicts = %#v", snapshot.Late, snapshot.Conflicts)
	}
	for index := range want {
		if snapshot.Late[index] != want[index] {
			t.Fatalf("per-Server late = %#v, want %#v", snapshot.Late, want)
		}
	}
	// Server 7: one at 0, suppressed at 59 min, another at 61 min; Server 8:
	// one.
	diagnostics := 0
	for _, diagnostic := range snapshot.Diagnostics {
		if diagnostic.Component != "ingestion" || diagnostic.Category != "late_event" {
			t.Fatalf("unexpected diagnostic %#v", diagnostic)
		}
		if err := diagnostic.Validate(); err != nil {
			t.Fatalf("late-event diagnostic is not in the closed table: %v", err)
		}
		if diagnostic.RequestID != "" || strings.Contains(diagnostic.Summary, "7") {
			t.Fatalf("late-event diagnostic carries attribution: %#v", diagnostic)
		}
		diagnostics++
	}
	if diagnostics != 3 {
		t.Fatalf("late-event diagnostics = %d, want 3", diagnostics)
	}
	// Late events are stored normally and never affect health.
	state.SetWriterReady(true)
	if !state.Ready(false) || state.HealthSeverity() != "Healthy" {
		t.Fatal("late events changed readiness or health")
	}
}

func TestStateLateEventCountersAreBounded(t *testing.T) {
	started := time.Unix(40_000, 0).UTC()
	state := NewState(started)
	for server := int64(1); server <= serverCounterCapacity+3; server++ {
		state.RecordIngestLate(server, 2, started.Add(time.Duration(server)*time.Second))
	}
	snapshot := state.Snapshot(started)
	if len(snapshot.Late) != serverCounterCapacity ||
		snapshot.LateEvents != 2*(serverCounterCapacity+3) {
		t.Fatalf("bounded late = %d rows, %d total", len(snapshot.Late), snapshot.LateEvents)
	}
	if last := snapshot.Late[len(snapshot.Late)-1]; last.ServerID != 4 {
		t.Fatalf("oldest retained Server = %d, want 4", last.ServerID)
	}
}

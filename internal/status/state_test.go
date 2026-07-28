package status

import (
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

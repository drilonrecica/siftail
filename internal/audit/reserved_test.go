package audit

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// seedAuditRows inserts count rows with consecutive IDs and times starting at
// firstID. Privileged rows are system actions; the rest are unauthenticated.
func seedAuditRows(t *testing.T, db *sql.DB, firstID, count int, unauthenticated bool) {
	t.Helper()
	category, action, outcome, actor :=
		"session", "session.cleanup", "succeeded", "system"
	if unauthenticated {
		category, action, outcome, actor =
			"authentication", "sign_in", "rejected", "unauthenticated"
	}
	if _, err := db.Exec(`WITH RECURSIVE n(value) AS (
			SELECT ? UNION ALL SELECT value+1 FROM n WHERE value < ?
		)
		INSERT INTO security_audit_events(
			id,occurred_at_us,category,action,outcome,actor_type,
			safe_metadata_json
		)
		SELECT value, 1000000 + value, ?, ?, ?, ?, '{}' FROM n`,
		firstID, firstID+count-1, category, action, outcome, actor); err != nil {
		t.Fatal(err)
	}
}

func auditActorCounts(t *testing.T, db *sql.DB) (privileged, unauthenticated int) {
	t.Helper()
	var stored int
	if err := db.QueryRow(`SELECT
		(SELECT count(*) FROM security_audit_events
			WHERE actor_type <> 'unauthenticated'),
		(SELECT count(*) FROM security_audit_events
			WHERE actor_type = 'unauthenticated'),
		(SELECT row_count FROM security_audit_unauthenticated_count WHERE id = 1)`).
		Scan(&privileged, &unauthenticated, &stored); err != nil {
		t.Fatal(err)
	}
	if stored != unauthenticated {
		t.Fatalf("unauthenticated counter = %d, rows = %d", stored, unauthenticated)
	}
	return privileged, unauthenticated
}

func summaryInput(at time.Time) Input {
	return Input{
		OccurredAt: at, Category: CategoryAuthentication,
		Action: "sign_in.rejected_summary", Outcome: OutcomeRejected,
		ActorType: ActorUnauthenticated,
		Metadata: Metadata{
			MetadataWindowStartedAt:         at.Add(-5 * time.Minute).UTC().Format(WindowTimeLayout),
			MetadataInvalidCredentialsCount: "4",
			MetadataThrottledCount:          "99990",
			MetadataInFlightCount:           "5",
			MetadataOverloadedCount:         "1",
			MetadataDistinctClientCount:     "1024+",
			MetadataTopClient:               "198.51.100.7/32",
			MetadataTopClientCount:          "50000",
		},
	}
}

func TestUnauthenticatedInsertAtSubCapEvictsOnlyOldestUnauthenticatedRow(t *testing.T) {
	db, store, _ := auditFixture(t)
	// The oldest 90,000 rows are privileged, so a plain oldest-first cap
	// would evict one of them.
	seedAuditRows(t, db.Writer(), 1, MaxRecords-MaxUnauthenticatedRecords, false)
	seedAuditRows(t, db.Writer(), MaxRecords-MaxUnauthenticatedRecords+1,
		MaxUnauthenticatedRecords, true)
	assertTableCount(t, db.Reader(), "security_audit_events", MaxRecords)

	event, err := store.Record(context.Background(),
		summaryInput(time.Date(2026, 11, 5, 12, 5, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, db.Reader(), "security_audit_events", MaxRecords)
	privileged, unauthenticated := auditActorCounts(t, db.Reader())
	if privileged != MaxRecords-MaxUnauthenticatedRecords ||
		unauthenticated != MaxUnauthenticatedRecords {
		t.Fatalf("after summary: privileged=%d unauthenticated=%d",
			privileged, unauthenticated)
	}
	var evicted, kept int
	if err := db.Reader().QueryRow(`SELECT
		(SELECT count(*) FROM security_audit_events WHERE id = ?),
		(SELECT count(*) FROM security_audit_events WHERE id = ?)`,
		MaxRecords-MaxUnauthenticatedRecords+1, event.ID).
		Scan(&evicted, &kept); err != nil {
		t.Fatal(err)
	}
	if evicted != 0 || kept != 1 {
		t.Fatalf("oldest unauthenticated present=%d, new summary present=%d",
			evicted, kept)
	}
}

func TestUnauthenticatedInsertAtTotalCapNeverEvictsPrivilegedRows(t *testing.T) {
	db, store, _ := auditFixture(t)
	// Below the sub-cap but at the total cap: the total excess must still be
	// taken from unauthenticated rows.
	seedAuditRows(t, db.Writer(), 1, 500, true)
	seedAuditRows(t, db.Writer(), 501, MaxRecords-500, false)
	for index := range 3 {
		if _, err := store.Record(context.Background(), summaryInput(
			time.Date(2026, 11, 5, 12, 5*(index+1), 0, 0, time.UTC),
		)); err != nil {
			t.Fatal(err)
		}
	}
	assertTableCount(t, db.Reader(), "security_audit_events", MaxRecords)
	privileged, unauthenticated := auditActorCounts(t, db.Reader())
	if privileged != MaxRecords-500 || unauthenticated != 500 {
		t.Fatalf("privileged=%d unauthenticated=%d", privileged, unauthenticated)
	}

	// A privileged insert keeps the original oldest-first total cap.
	if _, err := store.Record(context.Background(), Input{
		OccurredAt: time.Date(2026, 11, 5, 13, 0, 0, 0, time.UTC),
		Category:   CategorySession, Action: "session.cleanup",
		Outcome: OutcomeSucceeded, ActorType: ActorSystem,
	}); err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, db.Reader(), "security_audit_events", MaxRecords)
	privileged, unauthenticated = auditActorCounts(t, db.Reader())
	// The oldest row overall was unauthenticated, so the total cap took it.
	if privileged != MaxRecords-499 || unauthenticated != 499 {
		t.Fatalf("after privileged insert: privileged=%d unauthenticated=%d",
			privileged, unauthenticated)
	}
}

func TestLegacyUnauthenticatedExcessConvergesInBoundedSteps(t *testing.T) {
	db, store, _ := auditFixture(t)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	// Per-attempt rows written before migration 0010 may exceed the sub-cap.
	const legacy = MaxUnauthenticatedRecords + 2_500
	seedAuditRows(t, db.Writer(), 1, 100, false)
	seedAuditRows(t, db.Writer(), 101, legacy, true)

	if _, err := store.Record(context.Background(), summaryInput(now)); err != nil {
		t.Fatal(err)
	}
	privileged, unauthenticated := auditActorCounts(t, db.Reader())
	if privileged != 100 || unauthenticated != legacy+1-MaxCleanupChunk {
		t.Fatalf("after insert: privileged=%d unauthenticated=%d",
			privileged, unauthenticated)
	}
	// Each later insert removes at most another MaxCleanupChunk rows: 11,501
	// and then 10,502 rows before the third insert settles at the sub-cap.
	for index, want := range []int{
		legacy + 2 - 2*MaxCleanupChunk, MaxUnauthenticatedRecords,
	} {
		if _, err := store.Record(context.Background(),
			summaryInput(now.Add(time.Duration(index+1)*5*time.Minute))); err != nil {
			t.Fatal(err)
		}
		privileged, unauthenticated = auditActorCounts(t, db.Reader())
		if privileged != 100 || unauthenticated != want {
			t.Fatalf("after insert %d: privileged=%d unauthenticated=%d, want %d",
				index+2, privileged, unauthenticated, want)
		}
	}
}

func TestCleanupTrimsUnauthenticatedSubCapBeforeTotalCap(t *testing.T) {
	db, store, _ := auditFixture(t)
	// Recent rows so that age cleanup deletes nothing.
	now := time.UnixMicro(1_000_000 + 50_000)
	store.now = func() time.Time { return now }
	seedAuditRows(t, db.Writer(), 1, 100, false)
	seedAuditRows(t, db.Writer(), 101, MaxUnauthenticatedRecords+1_500, true)

	result, err := store.Cleanup(context.Background(), DefaultRetentionDays, MaxCleanupChunk)
	if err != nil {
		t.Fatal(err)
	}
	if result.AgeDeleted != 0 || result.UnauthenticatedCapDeleted != MaxCleanupChunk ||
		result.CapDeleted != 0 {
		t.Fatalf("first cleanup = %#v", result)
	}
	result, err = store.Cleanup(context.Background(), DefaultRetentionDays, MaxCleanupChunk)
	if err != nil || result.UnauthenticatedCapDeleted != 500 || result.CapDeleted != 0 {
		t.Fatalf("second cleanup = %#v, err=%v", result, err)
	}
	privileged, unauthenticated := auditActorCounts(t, db.Reader())
	if privileged != 100 || unauthenticated != MaxUnauthenticatedRecords {
		t.Fatalf("privileged=%d unauthenticated=%d", privileged, unauthenticated)
	}
	result, err = store.Cleanup(context.Background(), DefaultRetentionDays, MaxCleanupChunk)
	if err != nil || result != (CleanupResult{}) {
		t.Fatalf("settled cleanup = %#v, err=%v", result, err)
	}
}

func TestAggregateMetadataValidation(t *testing.T) {
	_, store, _ := auditFixture(t)
	at := time.Date(2026, 11, 5, 12, 5, 0, 0, time.UTC)
	if _, err := store.Record(context.Background(), summaryInput(at)); err != nil {
		t.Fatalf("valid summary: %v", err)
	}
	for key, value := range map[string]string{
		MetadataWindowStartedAt:         "2026-11-05 12:00:00",
		MetadataThrottledCount:          "-1",
		MetadataInFlightCount:           "01",
		MetadataDistinctClientCount:     "+",
		MetadataTopClientCount:          "many",
		MetadataInvalidCredentialsCount: "1+",
	} {
		input := summaryInput(at)
		input.Metadata[key] = value
		if _, err := store.Record(context.Background(), input); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("%s=%q error = %v", key, value, err)
		}
	}
}

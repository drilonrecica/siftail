package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestStoreRecordsListsFiltersAndKeepsEventsImmutable(t *testing.T) {
	db, store, _ := auditFixture(t)
	administratorID := int64(1)
	serverID := int64(7)
	at := time.UnixMicro(1_000_000)
	inputs := []Input{
		{
			OccurredAt: at, Category: CategoryAuthentication,
			Action: "sign_in", Outcome: OutcomeRejected,
			ActorType: ActorUnauthenticated,
			Metadata:  Metadata{MetadataClientAddress: "192.0.2.1"},
			RequestID: "request-1",
		},
		{
			OccurredAt: at, Category: CategoryIngestionToken,
			Action: "ingestion_token.create", Outcome: OutcomeSucceeded,
			ActorType: ActorAdministrator, AdministratorID: &administratorID,
			ServerID: &serverID,
			Metadata: Metadata{
				MetadataServerName: "production", MetadataTokenName: "primary",
			},
			RequestID: "request-2",
		},
		{
			OccurredAt: at.Add(time.Microsecond),
			Category:   CategorySession, Action: "session.revoke_all",
			Outcome: OutcomeSucceeded, ActorType: ActorLocalOperator,
			Metadata: Metadata{MetadataSessionCount: "2"},
		},
	}
	var recorded []Event
	for _, input := range inputs {
		event, err := store.Record(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		recorded = append(recorded, event)
	}
	inputs[1].Metadata[MetadataTokenName] = "mutated"
	if recorded[1].Metadata[MetadataTokenName] != "primary" {
		t.Fatal("recorded metadata aliases caller state")
	}

	first, err := store.List(context.Background(), Query{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || !first.HasMore ||
		first.Events[0].ID != recorded[2].ID ||
		first.Events[1].ID != recorded[1].ID ||
		first.NextBeforeOccurredAtUS != at.UnixMicro() ||
		first.NextBeforeID != recorded[1].ID {
		t.Fatalf("first page = %#v", first)
	}
	second, err := store.List(context.Background(), Query{
		BeforeOccurredAtUS: first.NextBeforeOccurredAtUS,
		BeforeID:           first.NextBeforeID,
		Limit:              2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.HasMore ||
		second.Events[0].ID != recorded[0].ID {
		t.Fatalf("second page = %#v", second)
	}
	filtered, err := store.List(context.Background(), Query{
		Category: CategoryAuthentication, Action: "sign_in",
		Outcome: OutcomeRejected, FromOccurredAtUS: at.UnixMicro(),
		ToOccurredAtUS: at.Add(time.Microsecond).UnixMicro(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Events) != 1 ||
		filtered.Events[0].Metadata[MetadataClientAddress] != "192.0.2.1" {
		t.Fatalf("filtered page = %#v", filtered)
	}
	outside, err := store.List(context.Background(), Query{
		Action: "sign_in", FromOccurredAtUS: at.Add(time.Microsecond).UnixMicro(),
		ToOccurredAtUS: at.Add(2 * time.Microsecond).UnixMicro(),
	})
	if err != nil || len(outside.Events) != 0 {
		t.Fatalf("outside time range = %#v, err=%v", outside, err)
	}

	if _, err := db.Writer().Exec(
		"UPDATE security_audit_events SET action='changed' WHERE id=?",
		recorded[0].ID,
	); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("immutable update error = %v", err)
	}
	var indexSQL string
	if err := db.Reader().QueryRow(`SELECT sql FROM sqlite_schema
		WHERE type='index' AND name='security_audit_events_time_idx'`).
		Scan(&indexSQL); err != nil ||
		!strings.Contains(indexSQL, "occurred_at_us DESC, id DESC") {
		t.Fatalf("time index = %q, err=%v", indexSQL, err)
	}
}

func TestRecordTxIsAtomicWithCallerMutation(t *testing.T) {
	db, store, coordinator := auditFixture(t)
	input := validInput()
	rollbackMarker := errors.New("rollback audit and sibling")
	err := coordinator.Do(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO settings(key,value_json,updated_at_us)
			VALUES('atomic_audit','{}',1)`); err != nil {
			return err
		}
		if _, err := RecordTx(context.Background(), tx, input); err != nil {
			return err
		}
		return rollbackMarker
	})
	if !errors.Is(err, rollbackMarker) {
		t.Fatalf("rollback error = %v", err)
	}
	assertTableCount(t, db.Reader(), "settings", 0)
	assertTableCount(t, db.Reader(), "security_audit_events", 0)

	err = coordinator.Do(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO settings(key,value_json,updated_at_us)
			VALUES('atomic_audit','{}',1)`); err != nil {
			return err
		}
		_, err := RecordTx(context.Background(), tx, input)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, db.Reader(), "settings", 1)
	page, err := store.List(context.Background(), Query{})
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("committed audit page = %#v, err=%v", page, err)
	}
}

func TestStoreRejectsUnsafeFieldsWithoutPersistingOrEchoingThem(t *testing.T) {
	db, store, _ := auditFixture(t)
	secret := "private-password-token-payload-marker"
	invalid := []Input{}

	value := validInput()
	value.Category = Category("unknown")
	invalid = append(invalid, value)
	value = validInput()
	value.Outcome = Outcome("unknown")
	invalid = append(invalid, value)
	value = validInput()
	value.ActorType = ActorAdministrator
	invalid = append(invalid, value)
	value = validInput()
	id := int64(1)
	value.AdministratorID = &id
	invalid = append(invalid, value)
	value = validInput()
	value.Action = "Password Change"
	invalid = append(invalid, value)
	value = validInput()
	value.RequestID = "request\nsecret"
	invalid = append(invalid, value)
	value = validInput()
	zero := int64(0)
	value.ServerID = &zero
	invalid = append(invalid, value)
	for _, key := range []string{
		"password", "password_hash", "session_token", "ingestion_token",
		"token_hash", "authorization_header", "application_payload",
	} {
		value = validInput()
		value.Metadata = Metadata{key: secret}
		invalid = append(invalid, value)
	}
	value = validInput()
	value.Metadata = Metadata{MetadataResultCategory: secret + "\n"}
	invalid = append(invalid, value)
	value = validInput()
	value.Metadata = Metadata{MetadataResultCategory: "hidden\u0085control"}
	invalid = append(invalid, value)
	value = validInput()
	value.Metadata = Metadata{MetadataResultCategory: strings.Repeat("x", 257)}
	invalid = append(invalid, value)
	for key, invalidValue := range map[string]string{
		MetadataAffectedCount:    "-1",
		MetadataRetentionAgeDays: "0",
		MetadataTokenFingerprint: "NOT-A-FINGERPRINT",
		MetadataBackupName:       "../../siftail.db",
		MetadataBackupType:       "arbitrary",
		MetadataExportFormat:     "html",
		MetadataResultCategory:   "raw error detail",
	} {
		value = validInput()
		value.Metadata = Metadata{key: invalidValue}
		invalid = append(invalid, value)
	}
	value = validInput()
	value.Metadata = Metadata{
		MetadataActorName: "1", MetadataClientAddress: "2",
		MetadataServerName: "3", MetadataSourceName: "4",
		MetadataTokenName: "5", MetadataTokenFingerprint: "6",
		MetadataAffectedCount: "7", MetadataSessionCount: "8",
		MetadataRetentionAgeDays: "9", MetadataMaximumDatabaseGiB: "10",
		MetadataBackupType: "11", MetadataBackupName: "12",
		MetadataExportFormat: "13",
	}
	invalid = append(invalid, value)

	for index, input := range invalid {
		_, err := store.Record(context.Background(), input)
		if !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("case %d error = %v", index, err)
		}
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("case %d echoed sensitive input", index)
		}
	}
	assertTableCount(t, db.Reader(), "security_audit_events", 0)
}

func TestCleanupUsesIndependentAgeAndCountBounds(t *testing.T) {
	db, store, _ := auditFixture(t)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	old := now.Add(-366 * 24 * time.Hour).UnixMicro()
	recent := now.Add(-time.Hour).UnixMicro()
	if _, err := db.Writer().Exec(`WITH RECURSIVE n(value) AS (
			SELECT 1 UNION ALL SELECT value+1 FROM n WHERE value < 100002
		)
		INSERT INTO security_audit_events(
			id,occurred_at_us,category,action,outcome,actor_type,
			safe_metadata_json
		)
		SELECT value,
			CASE WHEN value <= 2 THEN ? ELSE ? + value END,
			'authentication','sign_in','rejected','unauthenticated','{}'
		FROM n`, old, recent); err != nil {
		t.Fatal(err)
	}
	result, err := store.Cleanup(context.Background(), DefaultRetentionDays, 3)
	if err != nil {
		t.Fatal(err)
	}
	if result.AgeDeleted != 2 || result.CapDeleted != 0 {
		t.Fatalf("first cleanup = %#v", result)
	}
	assertTableCount(t, db.Reader(), "security_audit_events", MaxRecords)

	if _, err := db.Writer().Exec(`INSERT INTO security_audit_events(
			occurred_at_us,category,action,outcome,actor_type,safe_metadata_json
		) VALUES
			(?,'session','session.revoke','succeeded','system','{}'),
			(?,'session','session.revoke','succeeded','system','{}'),
			(?,'session','session.revoke','succeeded','system','{}'),
			(?,'session','session.revoke','succeeded','system','{}'),
			(?,'session','session.revoke','succeeded','system','{}')`,
		recent+200000, recent+200001, recent+200002, recent+200003,
		recent+200004); err != nil {
		t.Fatal(err)
	}
	result, err = store.Cleanup(context.Background(), DefaultRetentionDays, 3)
	if err != nil || result.AgeDeleted != 0 || result.CapDeleted != 3 {
		t.Fatalf("bounded cap cleanup = %#v, err=%v", result, err)
	}
	assertTableCount(t, db.Reader(), "security_audit_events", MaxRecords+2)
	result, err = store.Cleanup(context.Background(), DefaultRetentionDays, 3)
	if err != nil || result.CapDeleted != 2 {
		t.Fatalf("final cap cleanup = %#v, err=%v", result, err)
	}
	assertTableCount(t, db.Reader(), "security_audit_events", MaxRecords)

	event, err := store.Record(context.Background(), Input{
		OccurredAt: now, Category: CategorySession, Action: "session.cleanup",
		Outcome: OutcomeSucceeded, ActorType: ActorSystem,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, db.Reader(), "security_audit_events", MaxRecords)
	var newest int64
	if err := db.Reader().QueryRow(
		"SELECT id FROM security_audit_events ORDER BY occurred_at_us DESC, id DESC LIMIT 1",
	).Scan(&newest); err != nil || newest != event.ID {
		t.Fatalf("newest audit ID = %d, want %d, err=%v", newest, event.ID, err)
	}
}

func TestApplicationDeletionDoesNotDeleteAuditHistory(t *testing.T) {
	db, store, _ := auditFixture(t)
	if _, err := db.Writer().Exec(`INSERT INTO servers(id,name,created_at_us)
			VALUES(1,'server',1);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			first_seen_at_us,last_seen_at_us
		) VALUES(1,1,'p','e','a','s','p','e','a','s',1,1);
		INSERT INTO log_events(
			id,event_at_us,received_at_us,source_id,stream,level_normalized,
			message_raw,message_text
		) VALUES(1,1,1,1,'stdout','info',x'78','x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(context.Background(), validInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec("DELETE FROM log_events"); err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, db.Reader(), "log_events", 0)
	assertTableCount(t, db.Reader(), "security_audit_events", 1)
}

func TestConcurrentRecordsRemainBoundedAndOrdered(t *testing.T) {
	db, store, _ := auditFixture(t)
	const workers = 8
	const perWorker = 50
	var wait sync.WaitGroup
	errorsByWorker := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for index := 0; index < perWorker; index++ {
				input := validInput()
				input.OccurredAt = input.OccurredAt.Add(
					time.Duration(worker*perWorker+index) * time.Microsecond,
				)
				input.RequestID = fmt.Sprintf("request-%d-%d", worker, index)
				if _, err := store.Record(context.Background(), input); err != nil {
					errorsByWorker <- err
					return
				}
			}
		}(worker)
	}
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		t.Error(err)
	}
	assertTableCount(t, db.Reader(), "security_audit_events", workers*perWorker)
	page, err := store.List(context.Background(), Query{Limit: MaxPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != MaxPageLimit || !page.HasMore {
		t.Fatalf("bounded concurrent page = %#v", page)
	}
	for index := 1; index < len(page.Events); index++ {
		previous, current := page.Events[index-1], page.Events[index]
		if previous.OccurredAt.Before(current.OccurredAt) ||
			(previous.OccurredAt.Equal(current.OccurredAt) &&
				previous.ID <= current.ID) {
			t.Fatalf("events out of order at %d: %#v then %#v",
				index, previous, current)
		}
	}
}

func TestInvalidQueriesCleanupAndStoredMetadataFailExplicitly(t *testing.T) {
	db, store, _ := auditFixture(t)
	for _, query := range []Query{
		{Category: "unknown"}, {Outcome: "unknown"},
		{BeforeOccurredAtUS: 1}, {BeforeID: 1}, {Limit: MaxPageLimit + 1},
	} {
		if _, err := store.List(context.Background(), query); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("query %#v error = %v", query, err)
		}
	}
	for _, values := range [][2]int{{0, 1}, {366, 1}, {365, 0}, {365, 1001}} {
		if _, err := store.Cleanup(
			context.Background(), values[0], values[1],
		); !errors.Is(err, ErrInvalidCleanup) {
			t.Errorf("cleanup %v error = %v", values, err)
		}
	}
	if _, err := db.Writer().Exec(`INSERT INTO security_audit_events(
		occurred_at_us,category,action,outcome,actor_type,safe_metadata_json
	) VALUES(1,'authentication','sign_in','rejected','unauthenticated',
		'{"password":"must-not-render"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background(), Query{}); err == nil ||
		strings.Contains(err.Error(), "must-not-render") {
		t.Fatalf("stored invalid metadata error = %v", err)
	}
}

func validInput() Input {
	return Input{
		OccurredAt: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
		Category:   CategoryAuthentication, Action: "sign_in",
		Outcome: OutcomeSucceeded, ActorType: ActorUnauthenticated,
		Metadata:  Metadata{MetadataClientAddress: "192.0.2.10"},
		RequestID: "request-safe",
	}
}

func auditFixture(
	t *testing.T,
) (*database.DB, *Store, *database.Coordinator) {
	t.Helper()
	db, err := database.Open(context.Background(), t.TempDir()+"/siftail.db")
	if err != nil {
		t.Fatal(err)
	}
	coordinator := database.NewCoordinator(db.Writer())
	runDone := make(chan error, 1)
	go func() {
		runDone <- coordinator.Run(context.Background())
	}()
	<-coordinator.Ready()
	t.Cleanup(func() {
		coordinator.Close()
		if err := <-runDone; err != nil {
			t.Errorf("coordinator shutdown: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Errorf("database close: %v", err)
		}
	})
	return db, NewStore(db.Reader(), coordinator), coordinator
}

func assertTableCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	query := "SELECT count(*) FROM " + table // table is a test constant.
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

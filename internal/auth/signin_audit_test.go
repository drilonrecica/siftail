package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
)

type fakeAuditClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeAuditClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeAuditClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func auditRowCount(t *testing.T, fixture *browserFixture) int {
	t.Helper()
	var count int
	if err := fixture.db.Reader().QueryRow(
		"SELECT count(*) FROM security_audit_events",
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSignInRejectionsAggregateByWindowWithBoundedClients(t *testing.T) {
	clock := &fakeAuditClock{now: time.Date(2026, 11, 5, 12, 0, 1, 0, time.UTC)}
	rejections := newSignInRejections()
	rejections.now = clock.Now

	// The most frequent client wins; a tie goes to the smaller key.
	for range 3 {
		rejections.add(rejectionInvalidCredentials, "client:2001:db8:1::/64")
		rejections.add(rejectionInFlight, "client:2001:db8:0::/64")
	}
	// Then 1,030 more distinct clients: only the first 1,022 fit beside the
	// two above, and the rest are counted but not tracked.
	for index := range signInAuditMaxClients + 6 {
		rejections.add(rejectionThrottled, fmt.Sprintf("client:198.51.%d.%d/32", index/256, index%256))
	}
	rejections.add(rejectionOverloaded, "client:"+strings.Repeat("x", 300))
	rejections.add(rejectionOverloaded, "client:bad\x00value")

	clock.Set(clock.Now().Add(4 * time.Minute))
	if summaries, dropped := rejections.take(false); len(summaries) != 0 || dropped != 0 {
		t.Fatalf("open window taken early: %#v %d", summaries, dropped)
	}
	clock.Set(time.Date(2026, 11, 5, 12, 5, 0, 0, time.UTC))
	rejections.add(rejectionThrottled, "client:192.0.2.1/32")
	clock.Set(time.Date(2026, 11, 5, 12, 10, 0, 0, time.UTC))
	summaries, dropped := rejections.take(false)
	if dropped != 0 || len(summaries) != 2 {
		t.Fatalf("summaries = %#v, dropped %d", summaries, dropped)
	}
	first := summaries[0].input()
	want := audit.Metadata{
		audit.MetadataWindowStartedAt:         "2026-11-05T12:00:00Z",
		audit.MetadataInvalidCredentialsCount: "3",
		audit.MetadataThrottledCount:          strconv.Itoa(signInAuditMaxClients + 6),
		audit.MetadataInFlightCount:           "3",
		audit.MetadataOverloadedCount:         "2",
		audit.MetadataDistinctClientCount:     "1024+",
		audit.MetadataTopClient:               "2001:db8:0::/64",
		audit.MetadataTopClientCount:          "3",
	}
	if fmt.Sprint(first.Metadata) != fmt.Sprint(want) ||
		!first.OccurredAt.Equal(time.Date(2026, 11, 5, 12, 5, 0, 0, time.UTC)) ||
		first.Action != signInAuditAction || first.ActorType != audit.ActorUnauthenticated {
		t.Fatalf("first window = %#v", first)
	}
	second := summaries[1].input()
	if second.Metadata[audit.MetadataDistinctClientCount] != "1" ||
		second.Metadata[audit.MetadataThrottledCount] != "1" ||
		second.Metadata[audit.MetadataTopClient] != "192.0.2.1/32" {
		t.Fatalf("second window = %#v", second)
	}
	// Oversized or control-character client keys become "unknown" but still
	// count; neither overflow window drops data.
	if got := clientSummary("client:bad\x00value"); got != "unknown" {
		t.Fatalf("unsafe client summary = %q", got)
	}
}

// TestRejectedSignInFloodWritesBoundedRowsAndKeepsAuthenticatedHistory is the
// SFT-105 acceptance test: 100,000 rejected attempts produce one row per
// window, write nothing while they are answered, and leave every
// authenticated row in place.
func TestRejectedSignInFloodWritesBoundedRowsAndKeepsAuthenticatedHistory(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	login := httptest.NewRecorder()
	fixture.handler().ServeHTTP(login, loginRequest("Admin", "browser-password", "/logs"))
	if login.Code != http.StatusSeeOther {
		t.Fatalf("administrator sign-in = %d", login.Code)
	}
	administratorID := int64(1)
	for index := range 20 {
		if _, err := fixture.browser.audit.Record(context.Background(), audit.Input{
			OccurredAt: time.Now(), Category: audit.CategorySession,
			Action: "session.revoke", Outcome: audit.OutcomeSucceeded,
			ActorType: audit.ActorAdministrator, AdministratorID: &administratorID,
			RequestID: fmt.Sprintf("privileged-%d", index),
		}); err != nil {
			t.Fatal(err)
		}
	}
	authenticatedRows := auditRowCount(t, fixture)
	nonUnauthenticated := func() int {
		t.Helper()
		var count int
		if err := fixture.db.Reader().QueryRow(`SELECT count(*)
			FROM security_audit_events WHERE actor_type <> 'unauthenticated'`).
			Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if nonUnauthenticated() != authenticatedRows {
		t.Fatal("fixture already holds unauthenticated audit rows")
	}

	clock := &fakeAuditClock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	fixture.browser.signInRejections.now = clock.Now

	// Over HTTP from one client: wrong passwords until the client is
	// throttled, then cheap throttled 429s. No request writes a row.
	const overHTTP = 2_000
	statuses := map[int]int{}
	for range overHTTP {
		response := httptest.NewRecorder()
		fixture.handler().ServeHTTP(response,
			loginFrom("Admin", "wrong-password", "203.0.113.9:4000"))
		statuses[response.Code]++
	}
	if statuses[http.StatusUnauthorized]+statuses[http.StatusTooManyRequests] != overHTTP {
		t.Fatalf("flood statuses = %v", statuses)
	}
	if got := auditRowCount(t, fixture); got != authenticatedRows {
		t.Fatalf("audit rows during flood = %d, want %d", got, authenticatedRows)
	}

	// The rest arrive from many /64s over the following 15 minutes.
	const total = 100_000
	for index := range total - overHTTP {
		clock.Set(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).
			Add(time.Duration(index) * 15 * time.Minute / (total - overHTTP)))
		fixture.browser.signInRejections.add(
			signInRejectionReason(index%int(rejectionReasonCount)),
			fmt.Sprintf("client:2001:db8:%x::/64", index%5000),
		)
	}
	if got := auditRowCount(t, fixture); got != authenticatedRows {
		t.Fatalf("audit rows before the writer runs = %d", got)
	}
	if err := fixture.browser.FlushSignInAudit(context.Background()); err != nil {
		t.Fatal(err)
	}

	page, err := fixture.browser.audit.List(context.Background(), audit.Query{
		Action: signInAuditAction, Limit: audit.MaxPageLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("aggregated rows = %d, want one per 5-minute window", len(page.Events))
	}
	var counted uint64
	for _, event := range page.Events {
		for _, key := range []string{
			audit.MetadataInvalidCredentialsCount, audit.MetadataThrottledCount,
			audit.MetadataInFlightCount, audit.MetadataOverloadedCount,
		} {
			value, err := strconv.ParseUint(event.Metadata[key], 10, 64)
			if err != nil {
				t.Fatalf("%s = %q", key, event.Metadata[key])
			}
			counted += value
		}
		if event.Metadata[audit.MetadataDistinctClientCount] != "1024+" {
			t.Fatalf("distinct clients = %q", event.Metadata[audit.MetadataDistinctClientCount])
		}
	}
	if counted != total {
		t.Fatalf("aggregated attempts = %d, want %d", counted, total)
	}
	if got := auditRowCount(t, fixture); got != authenticatedRows+3 {
		t.Fatalf("audit rows after flush = %d, want %d", got, authenticatedRows+3)
	}
	if privileged := nonUnauthenticated(); privileged != authenticatedRows {
		t.Fatalf("authenticated rows = %d, want %d", privileged, authenticatedRows)
	}
}

func TestSignInAuditWriterRequeuesFailuresAndBoundsPendingWindows(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", false)
	var reported []error
	fixture.browser.signInAuditError = func(err error) { reported = append(reported, err) }
	clock := &fakeAuditClock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	fixture.browser.signInRejections.now = clock.Now
	working := fixture.browser.audit
	fixture.browser.audit = audit.NewStore(fixture.db.Reader(), nil)

	// Twenty windows close while storage refuses writes.
	for index := range 20 {
		clock.Set(time.Date(2026, 11, 5, 12, 5*index, 0, 0, time.UTC))
		fixture.browser.signInRejections.add(rejectionThrottled, "client:192.0.2.1/32")
		if index%5 == 4 {
			if err := fixture.browser.writeSignInAudit(context.Background(), false); err == nil {
				t.Fatal("write with unavailable storage succeeded")
			}
		}
	}
	if len(reported) == 0 {
		t.Fatal("writer failure was not reported")
	}

	fixture.browser.audit = working
	reported = nil
	err := fixture.browser.FlushSignInAudit(context.Background())
	if !errors.Is(err, errSignInAuditWindowsDropped) || len(reported) != 1 {
		t.Fatalf("flush error = %v, reported %v", err, reported)
	}
	if got := auditRowCount(t, fixture); got != signInAuditMaxPending {
		t.Fatalf("written windows = %d, want the %d newest", got, signInAuditMaxPending)
	}
	var newest string
	if err := fixture.db.Reader().QueryRow(`SELECT
		json_extract(safe_metadata_json, '$.window_started_at')
		FROM security_audit_events ORDER BY occurred_at_us DESC LIMIT 1`).
		Scan(&newest); err != nil || newest != "2026-11-05T13:35:00Z" {
		t.Fatalf("newest window = %q, err=%v", newest, err)
	}
	if err := fixture.browser.FlushSignInAudit(context.Background()); err != nil {
		t.Fatalf("empty flush = %v", err)
	}
	if got := auditRowCount(t, fixture); got != signInAuditMaxPending {
		t.Fatalf("windows after empty flush = %d", got)
	}
}

func TestSignInAuditWriterWritesEndedWindowAndStopsWithItsOwner(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", false)
	clock := &fakeAuditClock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	fixture.browser.signInRejections.now = clock.Now
	fixture.browser.signInRejections.add(rejectionInvalidCredentials, "client:192.0.2.1/32")
	// The window has ended, so the writer wakes immediately.
	clock.Set(time.Date(2026, 11, 5, 12, 7, 0, 0, time.UTC))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fixture.browser.RunSignInAudit(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for auditRowCount(t, fixture) != 1 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("writer did not write the ended window")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not stop with its owner")
	}
	if err := fixture.browser.RunSignInAudit(nil); err == nil { //nolint:staticcheck // nil context is the error under test.
		t.Fatal("nil context accepted")
	}
	if got := auditRowCount(t, fixture); got != 1 {
		t.Fatalf("rows after stop = %d", got)
	}
}

func TestAuditViewRendersSignInAggregateClearly(t *testing.T) {
	event := audit.Event{
		OccurredAt: time.Date(2026, 11, 5, 12, 5, 0, 0, time.UTC),
		Category:   audit.CategoryAuthentication, Action: signInAuditAction,
		Outcome: audit.OutcomeRejected, ActorType: audit.ActorUnauthenticated,
		Metadata: audit.Metadata{
			audit.MetadataWindowStartedAt:         "2026-11-05T12:00:00Z",
			audit.MetadataInvalidCredentialsCount: "4",
			audit.MetadataThrottledCount:          "99990",
			audit.MetadataInFlightCount:           "5",
			audit.MetadataOverloadedCount:         "1",
			audit.MetadataDistinctClientCount:     "1024+",
			audit.MetadataTopClient:               "198.51.100.7/32",
			audit.MetadataTopClientCount:          "50000",
		},
	}
	want := "Rejected sign-ins 2026-11-05 12:00–12:05 UTC · 4 invalid credentials · " +
		"99,990 throttled · 5 concurrent attempt · 1 overloaded · " +
		"1,024 distinct clients (at least) · most frequent 198.51.100.7/32 (50,000)"
	if got := auditSummary(event, utcDisplayZone); got != want {
		t.Fatalf("summary =\n%s\nwant\n%s", got, want)
	}
	if got := auditActorLabel(event); got != "unauthenticated (5-minute aggregate)" {
		t.Fatalf("actor = %q", got)
	}
	for value, want := range map[string]string{
		"": "0", "7": "7", "1000": "1,000", "1234567": "1,234,567",
	} {
		if got := groupDigits(value); got != want {
			t.Errorf("groupDigits(%q) = %q, want %q", value, got, want)
		}
	}
}

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

func TestIssueStoresOnlyHashAndLookupTouchesAtMostEveryFiveMinutes(t *testing.T) {
	db, store := newSessionStore(t)
	base := time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	issued, err := store.Issue(context.Background(), 1, "Firefox", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(issued.Token)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("token entropy bytes = %d, err=%v", len(decoded), err)
	}
	var stored []byte
	if err := db.Reader().QueryRow("SELECT token_hash FROM sessions WHERE id=?", issued.Session.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(issued.Token))
	if string(stored) != string(hash[:]) || string(stored) == issued.Token {
		t.Fatal("session token was not stored as SHA-256 only")
	}

	store.now = func() time.Time { return base.Add(4*time.Minute + 59*time.Second) }
	session, err := store.Lookup(context.Background(), issued.Token)
	if err != nil || !session.LastUsedAt.Equal(base) {
		t.Fatalf("early lookup = %#v, %v", session, err)
	}
	store.now = func() time.Time { return base.Add(5 * time.Minute) }
	session, err = store.Lookup(context.Background(), issued.Token)
	if err != nil || !session.LastUsedAt.Equal(base.Add(5*time.Minute)) {
		t.Fatalf("touch lookup = %#v, %v", session, err)
	}
}

func TestSessionLifetimesAreTwelveHoursIdleAndSevenDaysAbsolute(t *testing.T) {
	if IdleLifetime != 12*time.Hour || AbsoluteLifetime != 7*24*time.Hour {
		t.Fatalf("lifetimes = idle %s, absolute %s", IdleLifetime, AbsoluteLifetime)
	}
	_, store := newSessionStore(t)
	base := time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	absolute, err := store.Issue(context.Background(), 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !absolute.Session.ExpiresAt.Equal(base.Add(AbsoluteLifetime)) {
		t.Fatalf("issued expiry = %s", absolute.Session.ExpiresAt)
	}
	// Use the session every 11 hours: it never idles out, yet it still ends
	// exactly at the absolute limit.
	at := base
	for at.Add(11 * time.Hour).Before(base.Add(AbsoluteLifetime)) {
		at = at.Add(11 * time.Hour)
		store.now = func() time.Time { return at }
		if _, err := store.Lookup(context.Background(), absolute.Token); err != nil {
			t.Fatalf("active session at %s: %v", at.Sub(base), err)
		}
	}
	store.now = func() time.Time { return base.Add(AbsoluteLifetime - time.Microsecond) }
	if _, err := store.Lookup(context.Background(), absolute.Token); err != nil {
		t.Fatalf("session before absolute limit: %v", err)
	}
	store.now = func() time.Time { return base.Add(AbsoluteLifetime) }
	if _, err := store.Lookup(context.Background(), absolute.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("absolute expiry error = %v", err)
	}

	store.now = func() time.Time { return base }
	idle, err := store.Issue(context.Background(), 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return base.Add(IdleLifetime - time.Microsecond) }
	if _, err := store.Lookup(context.Background(), idle.Token); err != nil {
		t.Fatalf("session before idle limit: %v", err)
	}
	store.now = func() time.Time { return base.Add(IdleLifetime - time.Microsecond + IdleLifetime) }
	if _, err := store.Lookup(context.Background(), idle.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("idle expiry error = %v", err)
	}

	store.now = func() time.Time { return base.Add(time.Hour) }
	revoked, err := store.Issue(context.Background(), 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(context.Background(), revoked.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lookup(context.Background(), revoked.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked lookup error = %v", err)
	}
}

// A session issued by an earlier version stores a 14-day expiry. Lookup, the
// active-session cap, and cleanup must all apply the 7-day absolute limit
// from creation without rewriting the row (ADR 0010 point 15).
func TestLegacyFourteenDaySessionFollowsSevenDayAbsoluteLimit(t *testing.T) {
	db, store := newSessionStore(t)
	base := time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	legacy, err := store.Issue(context.Background(), 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	legacyExpiry := base.Add(14 * 24 * time.Hour).UnixMicro()
	if _, err := db.Writer().Exec("UPDATE sessions SET expires_at_us=? WHERE id=?",
		legacyExpiry, legacy.Session.ID); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return base.Add(time.Hour) }
	session, err := store.Lookup(context.Background(), legacy.Token)
	if err != nil || !session.ExpiresAt.Equal(base.Add(AbsoluteLifetime)) {
		t.Fatalf("legacy lookup = %#v, %v", session, err)
	}
	// Keep it used so only the absolute limit can end it.
	beforeLimit := base.Add(AbsoluteLifetime - time.Hour)
	if _, err := db.Writer().Exec("UPDATE sessions SET last_used_at_us=? WHERE id=?",
		beforeLimit.UnixMicro(), legacy.Session.ID); err != nil {
		t.Fatal(err)
	}
	afterLimit := base.Add(AbsoluteLifetime + time.Minute)
	store.now = func() time.Time { return afterLimit }
	if _, err := store.Lookup(context.Background(), legacy.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("legacy session after 7 days = %v", err)
	}
	other, err := store.CountOthers(context.Background(), 1, -1)
	if err != nil || other != 0 {
		t.Fatalf("legacy session still counted active: %d, %v", other, err)
	}
	var stored int64
	if err := db.Reader().QueryRow("SELECT expires_at_us FROM sessions WHERE id=?",
		legacy.Session.ID).Scan(&stored); err != nil || stored != legacyExpiry {
		t.Fatalf("legacy row was rewritten: %d, %v", stored, err)
	}

	store.now = func() time.Time { return base.Add(AbsoluteLifetime + CleanupGrace - time.Minute) }
	if deleted, err := store.Cleanup(context.Background(), 10); err != nil || deleted != 0 {
		t.Fatalf("cleanup inside grace deleted=%d err=%v", deleted, err)
	}
	store.now = func() time.Time { return base.Add(AbsoluteLifetime + CleanupGrace) }
	if deleted, err := store.Cleanup(context.Background(), 10); err != nil || deleted != 1 {
		t.Fatalf("cleanup after absolute limit and grace deleted=%d err=%v", deleted, err)
	}
}

func TestRevokeOthersKeepsCurrentSessionAndAuditsCount(t *testing.T) {
	db, store := newSessionStore(t)
	base := time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC)
	// One other session has idled out and one is already revoked; neither
	// counts as signed out by this action.
	store.now = func() time.Time { return base.Add(-IdleLifetime) }
	idle, err := store.Issue(context.Background(), 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return base }
	issued := []Issued{}
	for i := 0; i < 3; i++ {
		session, err := store.Issue(context.Background(), 1, "", "")
		if err != nil {
			t.Fatal(err)
		}
		issued = append(issued, session)
	}
	issued = append(issued, idle)
	if err := store.Revoke(context.Background(), issued[2].Token); err != nil {
		t.Fatal(err)
	}
	current := issued[0]
	store.now = func() time.Time { return base.Add(time.Minute) }
	if count, err := store.CountOthers(context.Background(), 1, current.Session.ID); err != nil || count != 1 {
		t.Fatalf("other active sessions = %d, %v", count, err)
	}
	revoked, err := store.RevokeOthers(context.Background(), 1, current.Session.ID)
	if err != nil || revoked != 1 {
		t.Fatalf("revoke others = %d, %v", revoked, err)
	}
	if _, err := store.Lookup(context.Background(), current.Token); err != nil {
		t.Fatalf("current session revoked: %v", err)
	}
	for _, other := range issued[1:] {
		if _, err := store.Lookup(context.Background(), other.Token); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("other session still valid: %v", err)
		}
	}
	var action, metadata string
	if err := db.Reader().QueryRow(`SELECT action, safe_metadata_json
		FROM security_audit_events WHERE category='session'
		ORDER BY id DESC LIMIT 1`).Scan(&action, &metadata); err != nil {
		t.Fatal(err)
	}
	if action != "session.revoke_others" || metadata != `{"session_count":"1"}` {
		t.Fatalf("audit = %q %s", action, metadata)
	}

	// A current session revoked concurrently cannot sign out the others.
	store.now = func() time.Time { return base.Add(2 * time.Minute) }
	survivor, err := store.Issue(context.Background(), 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(context.Background(), current.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeOthers(context.Background(), 1, current.Session.ID); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked current session error = %v", err)
	}
	if _, err := store.Lookup(context.Background(), survivor.Token); err != nil {
		t.Fatalf("rejected revoke-others still revoked a session: %v", err)
	}
	if _, err := store.RevokeOthers(context.Background(), 1, survivor.Session.ID+100); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("unknown current session error = %v", err)
	}
}

func TestSessionCapEvictsDeterministicLeastRecentlyUsed(t *testing.T) {
	db, store := newSessionStore(t)
	base := time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	issued := make([]Issued, 0, MaxActive+1)
	for i := 0; i < MaxActive+1; i++ {
		session, err := store.Issue(context.Background(), 1, "", "")
		if err != nil {
			t.Fatal(err)
		}
		issued = append(issued, session)
	}
	var active int
	if err := db.Reader().QueryRow("SELECT count(*) FROM sessions WHERE revoked_at_us IS NULL").Scan(&active); err != nil ||
		active != MaxActive {
		t.Fatalf("active sessions = %d, err=%v", active, err)
	}
	if _, err := store.Lookup(context.Background(), issued[0].Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("oldest session error = %v", err)
	}
	if _, err := store.Lookup(context.Background(), issued[1].Token); err != nil {
		t.Fatalf("second session was evicted: %v", err)
	}
	if _, err := store.Lookup(context.Background(), issued[MaxActive].Token); err != nil {
		t.Fatalf("new session invalid: %v", err)
	}
}

func TestSessionCleanupAfterGraceAndBound(t *testing.T) {
	db, store := newSessionStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	for i := 0; i < 3; i++ {
		if _, err := store.Issue(context.Background(), 1, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Writer().Exec("UPDATE sessions SET revoked_at_us=?", base.UnixMicro()); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return base.Add(CleanupGrace) }
	deleted, err := store.Cleanup(context.Background(), 2)
	if err != nil || deleted != 2 {
		t.Fatalf("cleanup deleted=%d err=%v", deleted, err)
	}
	var remaining int
	if err := db.Reader().QueryRow("SELECT count(*) FROM sessions").Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("remaining = %d, err=%v", remaining, err)
	}
}

func TestConcurrentLookupAndRevocation(t *testing.T) {
	_, store := newSessionStore(t)
	issued, err := store.Issue(context.Background(), 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := store.Lookup(context.Background(), issued.Token)
			if err != nil && !errors.Is(err, ErrInvalidSession) {
				t.Errorf("lookup error = %v", err)
			}
		}()
	}
	if err := store.Revoke(context.Background(), issued.Token); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	if _, err := store.Lookup(context.Background(), issued.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("final revoked lookup = %v", err)
	}
}

func TestCleanupWorkerRunsAndStops(t *testing.T) {
	db, store := newSessionStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	if _, err := store.Issue(context.Background(), 1, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Writer().Exec("UPDATE sessions SET revoked_at_us=?", base.UnixMicro()); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return base.Add(CleanupGrace) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- NewCleanupWorker(store, time.Millisecond, nil).Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := db.Reader().QueryRow("SELECT count(*) FROM sessions").Scan(&count); err == nil && count == 0 {
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("cleanup worker did not remove expired session")
}

func newSessionStore(t *testing.T) (*database.DB, *Store) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Writer().Exec(`INSERT INTO administrators(
		id,username,password_hash,created_at_us,password_changed_at_us
	) VALUES(1,'Admin',?,1,1)`, strings.Repeat("h", 64)); err != nil {
		t.Fatal(err)
	}
	return db, NewStore(db.Writer())
}

package sessions

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
)

// A read-only database (the query_only reader pool used as the mutation
// path) still authenticates: the touch write fails and is skipped.
func TestLookupSkipsTouchWhenWriteFails(t *testing.T) {
	db, store := newSessionStore(t)
	base := time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base }
	issued, err := store.Issue(context.Background(), 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	readOnly := NewCoordinatedStore(db.Reader(), directMutator{db: db.Reader()})
	readOnly.now = func() time.Time { return base.Add(10 * time.Minute) }

	var category *database.CategoryError
	if err := readOnly.touch(context.Background(), issued.Session.ID, readOnly.now()); !errors.As(err, &category) ||
		category.Category != database.CategoryIO {
		t.Fatalf("read-only touch error = %#v, want classified io", err)
	}
	session, err := readOnly.Lookup(context.Background(), issued.Token)
	if err != nil {
		t.Fatalf("lookup with read-only database = %v", err)
	}
	if session.ID != issued.Session.ID || !session.LastUsedAt.Equal(base) {
		t.Fatalf("session = %#v, want unchanged last use %s", session, base)
	}
	var stored int64
	if err := db.Reader().QueryRow("SELECT last_used_at_us FROM sessions WHERE id=?",
		issued.Session.ID).Scan(&stored); err != nil || stored != base.UnixMicro() {
		t.Fatalf("stored last use = %d, %v", stored, err)
	}
	// Validity is still judged on the read: a revoked session stays invalid.
	if err := store.Revoke(context.Background(), issued.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := readOnly.Lookup(context.Background(), issued.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked lookup with read-only database = %v", err)
	}
}

func TestLookupInfrastructureErrorIsNotInvalidSession(t *testing.T) {
	db, store := newSessionStore(t)
	issued, err := store.Issue(context.Background(), 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	broken := closedReader(t, db)
	_, err = NewCoordinatedStore(broken, directMutator{db: broken}).Lookup(context.Background(), issued.Token)
	if !errors.Is(err, ErrSessionUnavailable) || errors.Is(err, ErrInvalidSession) {
		t.Fatalf("lookup on unavailable database = %v, want ErrSessionUnavailable only", err)
	}
	if _, err := store.Lookup(context.Background(), issued.Token); err != nil {
		t.Fatalf("session after database recovered = %v", err)
	}
	if _, err := store.Lookup(context.Background(), "unknown-token"); !errors.Is(err, ErrInvalidSession) ||
		errors.Is(err, ErrSessionUnavailable) {
		t.Fatalf("unknown token = %v, want ErrInvalidSession only", err)
	}
}

// When a parallel request touches the session between this lookup's read and
// its guarded update, the update matches no row. That must not sign the
// administrator out; a revocation in the same window still must.
func TestLookupRacingTouchOrRevocation(t *testing.T) {
	for _, test := range []struct {
		name    string
		between string
		valid   bool
	}{
		{"concurrent touch", "UPDATE sessions SET last_used_at_us=? WHERE id=1", true},
		{"concurrent revocation", "UPDATE sessions SET revoked_at_us=? WHERE id=1", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, store := newSessionStore(t)
			base := time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC)
			store.now = func() time.Time { return base }
			issued, err := store.Issue(context.Background(), 1, "", "")
			if err != nil {
				t.Fatal(err)
			}
			at := base.Add(10 * time.Minute)
			racing := NewCoordinatedStore(db.Writer(), interleavedMutator{
				db: db.Writer(), statement: test.between, at: at,
			})
			racing.now = func() time.Time { return at }
			session, err := racing.Lookup(context.Background(), issued.Token)
			if test.valid {
				if err != nil || !session.LastUsedAt.Equal(at) {
					t.Fatalf("lookup = %#v, %v; want valid session touched at %s", session, err, at)
				}
				return
			}
			if !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("lookup = %v, want ErrInvalidSession", err)
			}
		})
	}
}

// interleavedMutator commits statement (bound to at) on its own before the
// store's mutation runs, emulating a request that won the race.
type interleavedMutator struct {
	db        *sql.DB
	statement string
	at        time.Time
}

func (m interleavedMutator) Do(ctx context.Context, run func(*sql.Tx) error) error {
	if _, err := m.db.ExecContext(ctx, m.statement, m.at.UnixMicro()); err != nil {
		return err
	}
	return directMutator{db: m.db}.Do(ctx, run)
}

func closedReader(t *testing.T, db *database.DB) *sql.DB {
	t.Helper()
	var path string
	if err := db.Reader().QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	other, err := database.Open(context.Background(), filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	reader := other.Reader()
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	return reader
}

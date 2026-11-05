package auth

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/sessions"
)

// txMutator runs each mutation as one transaction on db; over the query_only
// reader pool every write fails as SQLite read-only.
type txMutator struct{ db *sql.DB }

func (m txMutator) Do(ctx context.Context, run func(*sql.Tx) error) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return database.Classify("begin", err)
	}
	if err := run(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return database.Classify("commit", tx.Commit())
}

type failingMutator struct{ err error }

func (m failingMutator) Do(context.Context, func(*sql.Tx) error) error { return m.err }

func signIn(t *testing.T, fixture *browserFixture) *http.Cookie {
	t.Helper()
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, loginRequest("Admin", "browser-password", "/status"))
	if response.Code != http.StatusSeeOther {
		t.Fatalf("login = %d %q", response.Code, response.Body.String())
	}
	return response.Result().Cookies()[0]
}

func TestProtectAnswers503WhenSessionLookupFails(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := signIn(t, fixture)
	healthy := fixture.browser.sessions

	other, err := database.Open(context.Background(), databasePath(t, fixture.db))
	if err != nil {
		t.Fatal(err)
	}
	broken := other.Reader()
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.browser.sessions = sessions.NewCoordinatedStore(broken, txMutator{db: broken})

	for _, test := range []struct {
		name   string
		method string
		path   string
		htmx   bool
		retry  string
	}{
		{"page", http.MethodGet, "/status", false, `href="/status"`},
		{"htmx", http.MethodGet, "/logs?mode=history", true, ""},
		{"form", http.MethodPost, "/session/logout", false, `href="/status"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			request.AddCookie(cookie)
			if test.htmx {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			fixture.handler().ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; body %q", response.Code, body)
			}
			if response.Header().Get("Location") != "" || response.Header().Get("HX-Redirect") != "" ||
				len(response.Result().Cookies()) != 0 {
				t.Fatalf("unavailable response signed out: %#v", response.Header())
			}
			if response.Header().Get("Cache-Control") != "no-store" ||
				!strings.Contains(body, "data-session-unavailable") ||
				!strings.Contains(body, "Your session was not ended") {
				t.Fatalf("unavailable response = %#v %q", response.Header(), body)
			}
			if test.htmx == strings.Contains(body, "<html") {
				t.Fatalf("htmx=%v rendered wrong shape: %q", test.htmx, body)
			}
			if test.retry != "" && !strings.Contains(body, test.retry) {
				t.Fatalf("retry link missing %s: %q", test.retry, body)
			}
		})
	}

	fixture.browser.sessions = healthy
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status after recovery = %d", response.Code)
	}
}

func TestProtectAuthenticatesWhenTouchCannotBeWritten(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := signIn(t, fixture)
	// Make the session due for a touch.
	stale := time.Now().Add(-10 * time.Minute).UnixMicro()
	if _, err := fixture.db.Writer().Exec(
		"UPDATE sessions SET created_at_us=?, last_used_at_us=?", stale, stale,
	); err != nil {
		t.Fatal(err)
	}
	reader := fixture.db.Reader()
	fixture.browser.sessions = sessions.NewCoordinatedStore(reader, txMutator{db: reader})

	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status with read-only database = %d %q", response.Code, response.Body.String())
	}
	var lastUsed int64
	if err := reader.QueryRow("SELECT last_used_at_us FROM sessions").Scan(&lastUsed); err != nil ||
		lastUsed != stale {
		t.Fatalf("last use = %d, %v; want untouched %d", lastUsed, err, stale)
	}
}

func TestLoginDuringStorageFull(t *testing.T) {
	t.Run("session row writable", func(t *testing.T) {
		fixture := newBrowserFixture(t, "https://logs.example.test", true)
		fixture.operational.RecordIngestRejected(ingest.CategoryStorageFull, true, time.Now())
		if fixture.operational.DatabaseWritable() {
			t.Fatal("fixture is not degraded")
		}
		cookie := signIn(t, fixture)
		request := httptest.NewRequest(http.MethodGet, "/status", nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		fixture.handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("degraded Status = %d", response.Code)
		}
	})
	for _, test := range []struct {
		name    string
		mutator func(*browserFixture) database.MutationCoordinator
		want    string
	}{
		{"read-only database", func(f *browserFixture) database.MutationCoordinator {
			return txMutator{db: f.db.Reader()}
		}, "database is temporarily unavailable"},
		{"storage full", func(*browserFixture) database.MutationCoordinator {
			return failingMutator{err: &database.CategoryError{
				Category: database.CategoryFull, Operation: "issue session",
			}}
		}, "database storage is full"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBrowserFixture(t, "https://logs.example.test", true)
			fixture.browser.sessions = sessions.NewCoordinatedStore(
				fixture.db.Reader(), test.mutator(fixture),
			)
			response := httptest.NewRecorder()
			fixture.handler().ServeHTTP(response, loginRequest("Admin", "browser-password", "/status"))
			body := response.Body.String()
			if response.Code != http.StatusServiceUnavailable || len(response.Result().Cookies()) != 0 {
				t.Fatalf("login = %d cookies %d", response.Code, len(response.Result().Cookies()))
			}
			if !strings.Contains(body, test.want) || !strings.Contains(body, `role="alert"`) ||
				!strings.Contains(body, `value="/status"`) {
				t.Fatalf("login failure page = %q", body)
			}
			var count int
			if err := fixture.db.Reader().QueryRow("SELECT count(*) FROM sessions").Scan(&count); err != nil ||
				count != 0 {
				t.Fatalf("sessions = %d, %v", count, err)
			}
		})
	}
}

func databasePath(t *testing.T, db *database.DB) string {
	t.Helper()
	var path string
	if err := db.Reader().QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	return path
}

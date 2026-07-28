package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/backup"
)

func TestBrowserBackupIsProtectedStartsAndShowsVerifiedOutcome(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	handler := fixture.handler()

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated,
		httptest.NewRequest(http.MethodGet, "/backup", nil))
	if unauthenticated.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated backup = %d", unauthenticated.Code)
	}

	pageRequest := httptest.NewRequest(http.MethodGet, "/backup", nil)
	pageRequest.AddCookie(cookie)
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, pageRequest)
	body := page.Body.String()
	if page.Code != http.StatusOK ||
		page.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(body, `aria-current="page">Backup</a>`) ||
		!strings.Contains(body, "Browser sessions are always excluded") ||
		!strings.Contains(body, "Never copy only the live main database file") {
		t.Fatalf("backup page = %d %#v %q", page.Code, page.Header(), body)
	}

	output := filepath.Join(t.TempDir(), "private-server-path-full.sqlite")
	form := url.Values{
		"csrf_token":  {CSRFToken(cookie.Value)},
		"output_path": {output},
	}
	start := httptest.NewRequest(
		http.MethodPost, "/backup/full", strings.NewReader(form.Encode()),
	)
	start.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	start.Header.Set("Origin", "https://logs.example.test")
	start.AddCookie(cookie)
	startResponse := httptest.NewRecorder()
	handler.ServeHTTP(startResponse, start)
	if startResponse.Code != http.StatusSeeOther ||
		startResponse.Header().Get("Location") != "/backup" {
		t.Fatalf("backup start = %d %q %q",
			startResponse.Code, startResponse.Header().Get("Location"),
			startResponse.Body.String())
	}
	waitBrowserBackup(t, fixture, backup.StateSucceeded)

	completedRequest := httptest.NewRequest(http.MethodGet, "/backup", nil)
	completedRequest.AddCookie(cookie)
	completed := httptest.NewRecorder()
	handler.ServeHTTP(completed, completedRequest)
	body = completed.Body.String()
	if completed.Code != http.StatusOK ||
		!strings.Contains(body, "Backup verified.") ||
		!strings.Contains(body, "private-server-path-full.sqlite") ||
		!strings.Contains(body, "SHA-256") ||
		strings.Contains(body, filepath.Dir(output)) {
		t.Fatalf("completed backup = %d %q", completed.Code, body)
	}

	auditPage, err := fixture.browser.audit.List(context.Background(), audit.Query{
		Action: "backup.full", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(auditPage.Events) != 1 ||
		auditPage.Events[0].ActorType != audit.ActorAdministrator ||
		auditPage.Events[0].AdministratorID == nil ||
		auditPage.Events[0].Outcome != audit.OutcomeSucceeded {
		t.Fatalf("browser backup audit = %#v", auditPage.Events)
	}
}

func TestBrowserBackupRequiresCSRFOriginAndBoundedPath(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	for _, test := range []struct {
		name   string
		origin string
		csrf   string
		path   string
		status int
	}{
		{"origin", "https://attacker.test", CSRFToken(cookie.Value), "/tmp/x", 403},
		{"csrf", "https://logs.example.test", "wrong", "/tmp/x", 403},
		{"path", "https://logs.example.test", CSRFToken(cookie.Value), "bad\npath", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{
				"csrf_token": {test.csrf}, "output_path": {test.path},
			}
			request := httptest.NewRequest(
				http.MethodPost, "/backup/full",
				strings.NewReader(form.Encode()),
			)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Origin", test.origin)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			fixture.handler().ServeHTTP(response, request)
			if response.Code != test.status ||
				strings.Contains(response.Body.String(), test.path) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
		})
	}
}

func waitBrowserBackup(
	t *testing.T,
	fixture *browserFixture,
	want string,
) backup.Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := fixture.browser.backups.Snapshot()
		if status.State == want {
			return status
		}
		if status.State == backup.StateFailed ||
			status.State == backup.StateCanceled ||
			time.Now().After(deadline) {
			t.Fatalf("backup status = %#v", status)
		}
		time.Sleep(time.Millisecond)
	}
}

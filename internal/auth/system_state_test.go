package auth

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/retention"
)

func getPage(t *testing.T, fixture *browserFixture, cookie *http.Cookie, target string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	return response.Code, response.Body.String()
}

// The global degraded banner appears below the header on every
// authenticated page while a critical condition holds, with a Status link
// and no dismiss control, and the topbar shows the severity as text
// (DESIGN.md §6.1, §6.3; audit UX-15).
func TestDegradedBannerAndHealthLabelOnEveryPage(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	pages := []string{"/logs?mode=live", "/sources", "/servers", "/settings", "/status", "/backup"}

	for _, target := range pages {
		code, body := getPage(t, fixture, cookie, target)
		if code != http.StatusOK {
			t.Fatalf("healthy %s = %d", target, code)
		}
		if strings.Contains(body, "data-degraded-banner") || strings.Contains(body, `class="health-label"`) {
			t.Fatalf("healthy %s shows a degraded state", target)
		}
		if !strings.Contains(body, `System health: </span><span class="visually-hidden">Healthy</span>`) {
			t.Fatalf("healthy %s lacks the health accessible name", target)
		}
	}

	fixture.operational.RecordIngestRejected(ingest.CategoryStorageFull, true, time.Now())
	for _, target := range pages {
		code, body := getPage(t, fixture, cookie, target)
		if code != http.StatusOK {
			t.Fatalf("degraded %s = %d", target, code)
		}
		for _, want := range []string{
			`data-degraded-banner`,
			`<strong>Ingestion unavailable:</strong> the database cannot write because storage is full.`,
			`Existing logs remain available. Free disk space or reduce retention.`,
			`<a href="/status">Open Status</a>`,
			`<span class="health-label">Degraded</span>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("degraded %s missing %q", target, want)
			}
		}
		banner := body[strings.Index(body, "data-degraded-banner"):]
		banner = banner[:strings.Index(banner, "</div>")]
		if strings.Contains(banner, "<button") || strings.Contains(strings.ToLower(banner), "dismiss") {
			t.Errorf("degraded %s banner is dismissible: %q", target, banner)
		}
	}

	fixture.operational.RecordDatabaseRecovered(time.Now())
	if _, body := getPage(t, fixture, cookie, "/sources"); strings.Contains(body, "data-degraded-banner") {
		t.Fatal("banner remained after recovery")
	}
}

func TestDegradedBannerNamesEachCondition(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)

	fixture.operational.RecordCleanup(retention.CleanupResult{
		SizeTriggered: true, EventsExhausted: true,
	}, time.Now())
	if _, body := getPage(t, fixture, cookie, "/sources"); !strings.Contains(body,
		"the database is over its configured size limit and retention cannot free enough space.") {
		t.Fatal("retention target banner missing")
	}

	fixture.operational.RecordIngestRejected(ingest.CategoryUnavailable, true, time.Now())
	if _, body := getPage(t, fixture, cookie, "/sources"); !strings.Contains(body,
		"the database cannot write. Siftail tests recovery automatically") {
		t.Fatal("database unavailable banner missing")
	}
}

// Live failures render in the application shell, like History's, instead of
// a raw text page (audit UX-21).
func TestLivePageErrorsRenderInTheShell(t *testing.T) {
	fixture, _ := newLiveBrowserFixture(t, liveTestBrokerOptions())
	cookie := loginBrowserCookie(t, fixture)

	code, body := getPage(t, fixture, cookie, "/logs?mode=live&levels=notice")
	if code != http.StatusBadRequest {
		t.Fatalf("invalid Live = %d", code)
	}
	for _, want := range []string{
		`class="app-header"`, `aria-current="page">Logs</a>`, `role="alert"`,
		`Live could not be started`, `Check the Live filters.`,
		`href="/logs?mode=live">Try Live again</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("invalid Live missing %q", want)
		}
	}
	if strings.Contains(body, "data-live-workspace") {
		t.Error("invalid Live still starts the stream")
	}

	fixture.browser.history = nil
	code, body = getPage(t, fixture, cookie, "/logs?mode=live&project=api")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable Live = %d", code)
	}
	for _, want := range []string{
		`class="app-header"`, `Live could not start because the database is temporarily unavailable.`,
		`href="/logs?mode=history&amp;project=api">open History</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("unavailable Live missing %q", want)
		}
	}
}

// Live states that it needs JavaScript and offers History; the stream-only
// controls stay hidden until live.js runs (DESIGN.md §33.5; audit UX-23).
func TestLivePageStatesItsJavaScriptRequirement(t *testing.T) {
	fixture, _ := newLiveBrowserFixture(t, liveTestBrokerOptions())
	cookie := loginBrowserCookie(t, fixture)
	code, body := getPage(t, fixture, cookie, "/logs?mode=live&project=api")
	if code != http.StatusOK {
		t.Fatalf("Live = %d", code)
	}
	noscript := body[strings.Index(body, "<noscript>\n    <div class=\"notice"):]
	noscript = noscript[:strings.Index(noscript, "</noscript>")]
	if !strings.Contains(noscript, "Live needs JavaScript") ||
		!strings.Contains(noscript, `href="/logs?mode=history&amp;project=api">Open History</a>`) {
		t.Fatalf("Live noscript = %q", noscript)
	}
	for _, want := range []string{
		`class="live-toolbar" data-live-script-only hidden`,
		`data-live-empty data-live-script-only hidden`,
		`class="live-status" role="status" aria-live="polite" data-live-script-only hidden`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Live missing %q", want)
		}
	}
}

// The export form carries its own CSRF token and a link opener, so it works
// as a plain form submission without JavaScript (audit UX-23).
func TestHistoryExportFormWorksWithoutJavaScript(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	code, page := getPage(t, fixture, cookie, "/logs?from=2026-07-28T00:00:00Z&to=2026-07-28T01:00:00Z")
	if code != http.StatusOK {
		t.Fatalf("History = %d", code)
	}
	// The sign-out form carries the session's token.
	signOut := page[strings.Index(page, `action="/session/logout"`):]
	csrf := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(signOut)
	if csrf == nil {
		t.Fatal("sign-out form lacks a CSRF token")
	}
	form := page[strings.Index(page, "data-export-form>"):]
	form = form[:strings.Index(form, "</form>")]
	if !strings.Contains(form, `<input type="hidden" name="csrf_token" value="`+csrf[1]+`">`) {
		t.Fatalf("export form lacks its own CSRF field: %q", form)
	}
	for _, want := range []string{
		`href="#history-export-dialog" aria-haspopup="dialog" data-open-export>`,
		`<dialog id="history-export-dialog"`,
		`href="#history-export-open" data-close-export>Cancel</a>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("History missing %q", want)
		}
	}
}

// A filter fragment refreshes the export scope and submit target out of
// band but never carries the form or its CSRF token (SFT-046).
func TestHistoryFragmentUpdatesExportTargetWithoutCSRF(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	request := httptest.NewRequest(http.MethodGet,
		"/logs/rows?from=2026-07-28T00:00:00Z&to=2026-07-28T01:00:00Z&contains=needle", nil)
	request.AddCookie(cookie)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("fragment = %d %q", response.Code, body)
	}
	for _, want := range []string{
		`hx-swap-oob="innerHTML:#history-export-scope"`,
		`hx-swap-oob="outerHTML:#history-export-create"`,
		`formaction="/logs/export?`,
		`contains=needle`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment missing %q", want)
		}
	}
	if strings.Contains(body, "csrf_token") || strings.Contains(body, "data-export-form") {
		t.Fatal("fragment carried the export form or CSRF material")
	}
}

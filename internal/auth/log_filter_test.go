package auth

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/logs"
)

func TestSurroundingURLOpensOneMinuteEachSideWithScopeAndHighlight(t *testing.T) {
	at := time.Date(2026, 7, 28, 0, 30, 0, 0, time.UTC)
	target := surroundingURL(logs.SourceScope{ServerID: 1, Application: "api"}, at, 7)
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	values := parsed.Query()
	for name, want := range map[string]string{
		"mode": "history", "from": "2026-07-28T00:29:00Z", "to": "2026-07-28T00:31:00Z",
		"server": "1", "application": "api", "highlight": "7", "direction": "older",
		"limit": "500",
	} {
		if got := values.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, absent := range []string{"levels", "streams", "contains", "project"} {
		if values.Has(absent) {
			t.Errorf("surrounding URL carries %s", absent)
		}
	}
}

func TestEventDetailOffersEventsAroundThisTimeInCurrentScope(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	get := func(target string) (int, string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		fixture.handler().ServeHTTP(response, request)
		return response.Code, response.Body.String()
	}

	code, body := get("/logs/events/1?server=1&project=project")
	if code != http.StatusOK || !strings.Contains(body, ">Show events around this time</a>") {
		t.Fatalf("detail = %d %q", code, body)
	}
	href := extractAttribute(t, body, "data-surrounding-events", "href")
	parsed, err := url.Parse(href)
	if err != nil {
		t.Fatal(err)
	}
	values := parsed.Query()
	if values.Get("server") != "1" || values.Get("project") != "project" ||
		values.Get("highlight") != "1" || values.Get("from") != "2026-07-28T00:29:00Z" ||
		values.Get("to") != "2026-07-28T00:31:00Z" {
		t.Fatalf("surrounding link = %q", href)
	}
	for _, bad := range []string{
		"/logs/events/1?server=0", "/logs/events/1?project=a&project=b",
		"/logs/events/1?levels=error",
	} {
		if code, _ := get(bad); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}

	// Following the link lands on History with the event highlighted.
	code, page := get(href)
	if code != http.StatusOK {
		t.Fatalf("surrounding History = %d %q", code, page)
	}
	if !strings.Contains(page, `data-event-id="1" tabindex="-1" data-highlighted aria-current="true"`) {
		t.Fatalf("History did not highlight event 1: %q", page)
	}
	if strings.Count(page, "data-highlighted") != 1 {
		t.Fatalf("highlighted rows = %d", strings.Count(page, "data-highlighted"))
	}
	if strings.Contains(page, "data-highlight-missing") {
		t.Fatal("highlight notice shown although the event is loaded")
	}
	// Export ignores view-only highlight state.
	if action := extractAttribute(t, page, "data-export-form", "action"); strings.Contains(action, "highlight") {
		t.Fatalf("export URL carries highlight: %q", action)
	}
}

func TestHistoryHighlightOutsideResultsIsExplainedAndValidated(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	get := func(target string) (int, string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		fixture.handler().ServeHTTP(response, request)
		return response.Code, response.Body.String()
	}
	base := "/logs?mode=history&from=2026-07-28T00:00:00Z&to=2026-07-28T01:00:00Z"
	code, page := get(base + "&highlight=999")
	if code != http.StatusOK || !strings.Contains(page, "The highlighted event is not in these results.") {
		t.Fatalf("missing highlight = %d %q", code, page)
	}
	for _, bad := range []string{"&highlight=0", "&highlight=x", "&highlight=1&highlight=2"} {
		if code, _ := get(base + bad); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
}

// extractAttribute returns attribute name of the first tag carrying marker.
func extractAttribute(t *testing.T, body, marker, name string) string {
	t.Helper()
	index := strings.Index(body, marker)
	if index < 0 {
		t.Fatalf("marker %q not found", marker)
	}
	start := strings.LastIndex(body[:index], "<")
	end := strings.Index(body[index:], ">") + index
	tag := body[start:end]
	attribute := name + `="`
	at := strings.Index(tag, attribute)
	if at < 0 {
		t.Fatalf("attribute %q not in %q", name, tag)
	}
	value := tag[at+len(attribute):]
	return html.UnescapeString(value[:strings.Index(value, `"`)])
}

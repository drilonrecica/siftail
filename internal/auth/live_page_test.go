package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/drilonrecica/siftail/internal/logs"
)

func TestLivePageRendersCanonicalExplicitFilterState(t *testing.T) {
	fixture, _ := newLiveBrowserFixture(t, liveTestBrokerOptions())
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	values := url.Values{
		"mode":        {"live"},
		"server":      {"1"},
		"project":     {"project"},
		"application": {"api"},
		"levels":      {"error,fatal"},
		"streams":     {"stderr"},
		"contains":    {`needle <unsafe>`},
	}
	request := httptest.NewRequest(http.MethodGet, "/logs?"+values.Encode(), nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Live page = %d %#v %q", response.Code, response.Header(), body)
	}
	for _, want := range []string{
		`data-live-workspace`,
		`id="live-server-filter"`,
		`id="live-service-filter"`,
		`value="1" selected`,
		`value="project" selected`,
		`value="api" selected`,
		`data-list-filter="levels" checked`,
		`value="stderr" data-list-filter="streams" checked`,
		`needle &lt;unsafe&gt;`,
		`server=1`,
		`project=project`,
		`application=api`,
		`level=error`,
		`level=fatal`,
		`stream=stderr`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Live page missing %q", want)
		}
	}
	if strings.Contains(body, `<unsafe>`) {
		t.Fatal("Live page emitted unescaped filter content")
	}
	// The History mode link carries the shared filter model (DESIGN.md §8.1).
	history := `href="/logs?application=api&amp;contains=needle&#43;%3Cunsafe%3E&amp;levels=error%2Cfatal&amp;mode=history&amp;project=project&amp;server=1&amp;streams=stderr"`
	if !strings.Contains(body, history) {
		t.Errorf("Live page History link does not carry the filter; want %s", history)
	}
}

func TestHistoryModeLinksCarryTheSharedFilterModel(t *testing.T) {
	fixture, _ := newLiveBrowserFixture(t, liveTestBrokerOptions())
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	values := url.Values{
		"mode": {"history"}, "server": {"1"}, "project": {"project"},
		"levels": {"error,fatal"}, "streams": {"stderr"}, "contains": {"timeout"},
		"excludes": {"health"},
		"from":     {"2026-07-28T00:00:00Z"}, "to": {"2026-07-28T01:00:00Z"},
	}
	request := httptest.NewRequest(http.MethodGet, "/logs?"+values.Encode(), nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("History = %d %q", response.Code, body)
	}
	live := `href="/logs?contains=timeout&amp;levels=error%2Cfatal&amp;mode=live&amp;project=project&amp;server=1&amp;streams=stderr"`
	if !strings.Contains(body, live) {
		t.Fatalf("History Live link does not carry the filter; want %s", live)
	}
	// The Live page accepts exactly what the History link produces.
	liveRequest := httptest.NewRequest(http.MethodGet,
		"/logs?contains=timeout&levels=error%2Cfatal&mode=live&project=project&server=1&streams=stderr", nil)
	liveRequest.AddCookie(cookie)
	liveResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(liveResponse, liveRequest)
	if liveResponse.Code != http.StatusOK {
		t.Fatalf("Live from History link = %d %q", liveResponse.Code, liveResponse.Body.String())
	}
}

func TestLivePageRejectsUnknownDuplicateAndInvalidState(t *testing.T) {
	fixture, _ := newLiveBrowserFixture(t, liveTestBrokerOptions())
	cookie := loginBrowserCookie(t, fixture)
	for _, target := range []string{
		"/logs?mode=live&regex=x",
		"/logs?mode=live&source=1",
		"/logs?mode=live&server=0",
		"/logs?mode=live&server=1&server=2",
		"/logs?mode=live&service=a%01b",
		"/logs?mode=live&from=2026-07-28T00:00:00Z",
		"/logs?mode=live&levels=notice",
		"/logs?mode=live&streams=pipe",
		"/logs?mode=live&contains=a&contains=b",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		fixture.handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), "Check the Live filters.") {
			t.Fatalf("%s = %d %q", target, response.Code, response.Body.String())
		}
	}
}

func TestLivePageURLAndStreamURLRemainSeparate(t *testing.T) {
	query, err := parseLivePageQuery(url.Values{
		"mode": {"live"}, "server": {"2"}, "service": {"web"}, "levels": {"warn,error"},
		"streams": {"stdout,stderr"}, "contains": {"100%_\\"},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := query.liveURL()
	stream := liveStreamURL(query)
	if !strings.Contains(page, "mode=live") || strings.Contains(stream, "mode=") ||
		!strings.Contains(stream, "server=2") || !strings.Contains(stream, "service=web") ||
		!strings.Contains(stream, "level=warn") ||
		!strings.Contains(stream, "level=error") ||
		!strings.Contains(stream, "stream=stdout") ||
		!strings.Contains(stream, "contains=100%25_%5C") {
		t.Fatalf("page=%q stream=%q", page, stream)
	}
}

func liveTestBrokerOptions() logs.LiveBrokerOptions {
	return logs.LiveBrokerOptions{}
}

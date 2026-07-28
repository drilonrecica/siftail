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
		"mode":     {"live"},
		"source":   {"1"},
		"levels":   {"error,fatal"},
		"streams":  {"stderr"},
		"contains": {`needle <unsafe>`},
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
		`id="live-source-filter"`,
		`value="1" selected`,
		`data-list-filter="levels" checked`,
		`value="stderr" data-list-filter="streams" checked`,
		`needle &lt;unsafe&gt;`,
		`source=1`,
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
}

func TestLivePageRejectsUnknownDuplicateAndInvalidState(t *testing.T) {
	fixture, _ := newLiveBrowserFixture(t, liveTestBrokerOptions())
	cookie := loginBrowserCookie(t, fixture)
	for _, target := range []string{
		"/logs?mode=live&regex=x",
		"/logs?mode=live&source=0",
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
		"mode": {"live"}, "source": {"2"}, "levels": {"warn,error"},
		"streams": {"stdout,stderr"}, "contains": {"100%_\\"},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := livePageURL(query)
	stream := liveStreamURL(query)
	if !strings.Contains(page, "mode=live") || strings.Contains(stream, "mode=") ||
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

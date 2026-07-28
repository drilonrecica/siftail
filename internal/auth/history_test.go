package auth

import (
	"context"
	"database/sql"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestHistoryPageRedirectsToAbsoluteCanonicalDefault(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	now := time.Date(2026, 7, 28, 12, 0, 0, 123456000, time.UTC)
	fixture.browser.now = func() time.Time { return now }
	cookie := loginBrowserCookie(t, fixture)

	request := httptest.NewRequest(http.MethodGet, "/logs", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body=%q", response.Code, response.Body.String())
	}
	location := response.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/logs" ||
		parsed.Query().Get("from") != "2026-07-28T11:00:00.123456Z" ||
		parsed.Query().Get("to") != "2026-07-28T12:00:00.123456Z" ||
		parsed.Query().Get("direction") != "older" ||
		parsed.Query().Get("limit") != "200" ||
		parsed.Query().Get("preset") != "" {
		t.Fatalf("canonical location = %q", location)
	}
	for _, forbidden := range []string{"csrf", "session", "token", "password"} {
		if strings.Contains(strings.ToLower(location), forbidden) {
			t.Fatalf("location exposed %q: %q", forbidden, location)
		}
	}

	pageRequest := httptest.NewRequest(http.MethodGet, location, nil)
	pageRequest.AddCookie(cookie)
	pageResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(pageResponse, pageRequest)
	body := pageResponse.Body.String()
	if pageResponse.Code != http.StatusOK ||
		pageResponse.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(body, `id="history-filter-form"`) ||
		!strings.Contains(body, `hx-history="false"`) ||
		!strings.Contains(body, `No log sources have been discovered yet`) {
		t.Fatalf("History page = %d %#v %q", pageResponse.Code, pageResponse.Header(), body)
	}
}

func TestHistoryPresetResolvesOnceAndPreservesFilters(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	fixture.browser.now = func() time.Time { return now }
	cookie := loginBrowserCookie(t, fixture)
	request := httptest.NewRequest(
		http.MethodGet,
		"/logs?mode=history&preset=24h&contains=needle&levels=error%2Cfatal&direction=older&limit=200",
		nil,
	)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", response.Code)
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	values := location.Query()
	if values.Get("preset") != "" ||
		values.Get("from") != "2026-07-27T12:00:00Z" ||
		values.Get("to") != "2026-07-28T12:00:00Z" ||
		values.Get("contains") != "needle" ||
		values.Get("levels") != "error,fatal" {
		t.Fatalf("resolved preset = %q", location.String())
	}
}

func TestHistoryRowsMapsAllFiltersAndEscapesHostileEvents(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	values := baseHistoryValues()
	values.Set("server", "1")
	values.Set("project", "project")
	values.Set("environment", "production")
	values.Set("application", "api")
	values.Set("service", "web")
	values.Set("container", "1")
	values.Set("levels", "error,fatal")
	values.Set("streams", "stderr")
	values.Set("contains", `error 100%_\`)
	values.Set("excludes", "health")
	values.Set("request_id", "request-1")
	values.Set("logger", "http")
	values.Set("http_method", "POST")
	values.Set("http_status", "503")
	values.Set("error_type", "temporary")

	request := httptest.NewRequest(http.MethodGet, "/logs/rows?"+values.Encode(), nil)
	request.AddCookie(cookie)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK ||
		response.Header().Get("HX-Push-Url") != "/logs?"+values.Encode() {
		t.Fatalf("fragment = %d headers=%#v body=%q", response.Code, response.Header(), body)
	}
	for _, want := range []string{
		`data-event-id="1"`,
		`value="1" selected`,
		`value="error" data-list-filter="levels" checked`,
		`value="stderr" data-list-filter="streams" checked`,
		`value="request-1"`,
		`API &lt;prod&gt;/Web`,
		`&lt;script&gt;alert(1)&lt;/script&gt; Error 100%_\ Café`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment missing %q", want)
		}
	}
	if strings.Contains(body, `<script>alert(1)</script>`) ||
		strings.Contains(body, `data-event-id="2"`) {
		t.Fatalf("fragment leaked hostile HTML or unmatched event: %s", body)
	}
	if strings.Contains(body, "csrf_token") {
		t.Fatal("History fragment exposed CSRF state")
	}
}

func TestHistoryRejectsInvalidRangesInPagesAndFragments(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	invalid := url.Values{
		"mode":      {"history"},
		"from":      {"2026-07-29T00:00:00Z"},
		"to":        {"2026-07-28T00:00:00Z"},
		"direction": {"older"},
		"limit":     {"200"},
	}
	for _, route := range []string{"/logs?", "/logs/rows?"} {
		request := httptest.NewRequest(http.MethodGet, route+invalid.Encode(), nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		fixture.handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), "Check the time range and filters.") {
			t.Fatalf("%s = %d %q", route, response.Code, response.Body.String())
		}
		if route == "/logs/rows?" &&
			(response.Header().Get("HX-Retarget") != "#history-update-status" ||
				response.Header().Get("HX-Reswap") != "outerHTML") {
			t.Fatalf("fragment error headers = %#v", response.Header())
		}
	}
}

func TestHistoryLoadOlderAppendsWithoutReplacingContext(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	values := baseHistoryValues()
	values.Set("limit", "1")

	request := httptest.NewRequest(http.MethodGet, "/logs/rows?"+values.Encode(), nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `data-event-id="3"`) {
		t.Fatalf("first page = %d %q", response.Code, response.Body.String())
	}
	nextURL := extractHXGet(t, response.Body.String())
	if !strings.Contains(nextURL, "append=1") || !strings.Contains(nextURL, "cursor=") {
		t.Fatalf("next URL = %q", nextURL)
	}

	appendRequest := httptest.NewRequest(http.MethodGet, nextURL, nil)
	appendRequest.AddCookie(cookie)
	appendRequest.Header.Set("HX-Request", "true")
	appendResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(appendResponse, appendRequest)
	body := appendResponse.Body.String()
	if appendResponse.Code != http.StatusOK ||
		appendResponse.Header().Get("HX-Push-Url") != "" ||
		strings.Contains(body, "<!doctype html>") ||
		!strings.Contains(body, `data-event-id="2"`) ||
		!strings.Contains(body, `hx-swap-oob="beforeend:#history-rows"`) ||
		!strings.Contains(body, `id="history-pagination"`) ||
		!strings.Contains(body, `id="load-older"`) ||
		!strings.Contains(body, `1 additional event loaded.`) {
		t.Fatalf("append fragment = %d %#v %q", appendResponse.Code, appendResponse.Header(), body)
	}
}

func TestHistoryFragmentSessionExpiryAndSafeQueryFailure(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	if _, err := fixture.db.Writer().Exec(
		"UPDATE sessions SET expires_at_us=created_at_us+1",
	); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/logs/rows?"+baseHistoryValues().Encode(), nil)
	request.AddCookie(cookie)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized ||
		!strings.HasPrefix(response.Header().Get("HX-Redirect"), "/login?return=") {
		t.Fatalf("expired fragment = %d %#v", response.Code, response.Header())
	}

	active := newBrowserFixture(t, "https://logs.example.test", true)
	activeCookie := loginBrowserCookie(t, active)
	active.browser.history = nil
	failed := httptest.NewRequest(http.MethodGet, "/logs/rows?"+baseHistoryValues().Encode(), nil)
	failed.AddCookie(activeCookie)
	failedResponse := httptest.NewRecorder()
	active.handler().ServeHTTP(failedResponse, failed)
	if failedResponse.Code != http.StatusServiceUnavailable ||
		!strings.Contains(failedResponse.Body.String(), "database is temporarily unavailable") ||
		strings.Contains(strings.ToLower(failedResponse.Body.String()), "sqlite") ||
		failedResponse.Header().Get("HX-Retarget") != "#history-update-status" {
		t.Fatalf("failed query = %d %q", failedResponse.Code, failedResponse.Body.String())
	}
}

func seedBrowserHistory(t *testing.T, fixture *browserFixture) {
	t.Helper()
	err := fixture.coordinator.Do(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO servers(id,name,created_at_us)
			VALUES (1,'Primary',1)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			first_seen_at_us,last_seen_at_us
		) VALUES (
			1,1,'project','production','api','web',
			'Project','Production','API <prod>','Web',1,3
		)`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO container_instances(
			id,source_id,container_id,container_name,first_seen_at_us,last_seen_at_us
		) VALUES (1,1,'container-1','api-1',1,3)`); err != nil {
			return err
		}
		events := []struct {
			at      int64
			stream  string
			level   string
			message string
			common  bool
		}{
			{
				time.Date(2026, 7, 28, 0, 30, 0, 0, time.UTC).UnixMicro(),
				"stderr", "error", `<script>alert(1)</script> Error 100%_\ Café`, true,
			},
			{
				time.Date(2026, 7, 28, 0, 30, 0, 0, time.UTC).UnixMicro(),
				"stdout", "info", "ordinary event", false,
			},
			{
				time.Date(2026, 7, 28, 0, 40, 0, 0, time.UTC).UnixMicro(),
				"stdout", "warn", "health timeout", false,
			},
		}
		for _, event := range events {
			var logger, requestID, errorType, method any
			var status, path, duration, attributes any
			if event.common {
				logger, requestID, errorType, method, status =
					"http", "request-1", "temporary", "POST", int64(503)
				path, duration = "/v1/<unsafe>", 12.5
				attributes = `{"z":{"d":"<script>nested</script>","b":2},"a":"first"}`
			}
			if _, err := tx.Exec(`INSERT INTO log_events(
				event_at_us,received_at_us,source_id,container_instance_id,
				stream,level_normalized,message_raw,message_text,
				attributes_json,logger,request_id,error_type,http_method,http_path,
				http_status,duration_ms
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				event.at, event.at+1, 1, 1, event.stream, event.level,
				[]byte(event.message), event.message,
				attributes, logger, requestID, errorType, method, path, status, duration,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func loginBrowserCookie(t *testing.T, fixture *browserFixture) *http.Cookie {
	t.Helper()
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(
		response,
		loginRequest("Admin", "browser-password", "/logs"),
	)
	if response.Code != http.StatusSeeOther || len(response.Result().Cookies()) != 1 {
		t.Fatalf("login = %d %#v", response.Code, response.Header())
	}
	return response.Result().Cookies()[0]
}

func baseHistoryValues() url.Values {
	return url.Values{
		"mode":      {"history"},
		"from":      {"2026-07-28T00:00:00Z"},
		"to":        {"2026-07-28T01:00:00Z"},
		"direction": {"older"},
		"limit":     {"200"},
	}
}

func extractHXGet(t *testing.T, body string) string {
	t.Helper()
	expression := regexp.MustCompile(`hx-get="([^"]+)"`)
	allMatches := expression.FindAllStringSubmatch(body, -1)
	if len(allMatches) == 0 {
		t.Fatalf("hx-get not found in %q", body)
	}
	matches := allMatches[len(allMatches)-1]
	return html.UnescapeString(matches[1])
}

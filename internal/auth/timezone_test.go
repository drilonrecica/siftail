package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	statusstate "github.com/drilonrecica/siftail/internal/status"
)

func TestLoadDisplayZoneAcceptsOnlyBoundedLoadableNames(t *testing.T) {
	for _, valid := range []struct{ value, name, cookie string }{
		{"utc", "UTC", "utc"},
		{"UTC", "UTC", "utc"},
		{"Europe/Berlin", "Europe/Berlin", "Europe/Berlin"},
		{"America/Argentina/Buenos_Aires", "America/Argentina/Buenos_Aires", "America/Argentina/Buenos_Aires"},
		{"Etc/GMT+5", "Etc/GMT+5", "Etc/GMT+5"},
		{"America/Port-au-Prince", "America/Port-au-Prince", "America/Port-au-Prince"},
	} {
		zone, ok := loadDisplayZone(valid.value)
		if !ok || zone.name != valid.name || zone.cookieValue() != valid.cookie {
			t.Errorf("loadDisplayZone(%q) = %q %v", valid.value, zone.name, ok)
		}
	}
	for _, invalid := range []string{
		"", "Local", "Europe/Nowhere", "../etc/passwd", "/etc/localtime",
		"Europe/Berlin\"><script>", "Europe/Berlin.extra", "Europe Berlin",
		strings.Repeat("A", 65),
	} {
		zone, ok := loadDisplayZone(invalid)
		if ok || zone.name != "UTC" || zone.loc != time.UTC {
			t.Errorf("loadDisplayZone(%q) = %q %v, want UTC fallback", invalid, zone.name, ok)
		}
	}
}

func TestDisplayZoneFormatsAreFixedTwentyFourHour(t *testing.T) {
	zone, _ := loadDisplayZone("America/New_York")
	// 23:05 in New York; a 12-hour clock would render 11:05 PM.
	value := time.Date(2026, 11, 6, 4, 5, 6, 789000000, time.UTC)
	if got := zone.row(value, false); got != "23:05:06.789" {
		t.Errorf("row = %q", got)
	}
	if got := zone.row(value, true); got != "05 Nov 23:05:06.789" {
		t.Errorf("row with date = %q", got)
	}
	if got := zone.instant(value); got != "05 Nov 2026 23:05:06 America/New_York" {
		t.Errorf("instant = %q", got)
	}
	if got := zone.rangeText(value, value.Add(time.Hour)); got !=
		"05 Nov 2026 23:05–06 Nov 2026 00:05 America/New_York" {
		t.Errorf("range = %q", got)
	}
	if got := datetime(value.In(zone.loc)); got != "2026-11-06T04:05:06.789Z" {
		t.Errorf("datetime = %q", got)
	}
}

func TestTimezonePreferenceSavesValidatesAndRendersServerSide(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)

	save := func(timezone string) *httptest.ResponseRecorder {
		t.Helper()
		values := url.Values{
			"theme": {"system"}, "density": {"compact"}, "shortcuts": {"on"},
			"return": {"/settings"},
		}
		if timezone != "" {
			values.Set("timezone", timezone)
		}
		response := httptest.NewRecorder()
		fixture.handler().ServeHTTP(response, managementRequest(
			t, fixture, cookie, "/preferences", values, true,
		))
		return response
	}
	saved := save("Europe/Berlin")
	if saved.Code != http.StatusSeeOther ||
		!strings.Contains(saved.Header().Get("Set-Cookie"),
			"siftail_prefs=system.compact.on.Europe/Berlin;") {
		t.Fatalf("save Europe/Berlin = %d %#v", saved.Code, saved.Header())
	}
	if utc := save("utc"); !strings.Contains(utc.Header().Get("Set-Cookie"),
		"siftail_prefs=system.compact.on.utc;") {
		t.Fatalf("save utc = %d %#v", utc.Code, utc.Header())
	}
	for _, invalid := range []string{"Local", "Europe/Nowhere", `Europe/Berlin"><x>`} {
		response := save(invalid)
		if response.Code != http.StatusBadRequest || len(response.Result().Cookies()) != 0 {
			t.Errorf("save %q = %d %#v", invalid, response.Code, response.Result().Cookies())
		}
	}

	render := func(value string) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/settings", nil)
		request.AddCookie(cookie)
		if value != "" {
			request.AddCookie(&http.Cookie{Name: prefsCookieName, Value: value})
		}
		page := httptest.NewRecorder()
		fixture.handler().ServeHTTP(page, request)
		if page.Code != http.StatusOK {
			t.Fatalf("render %q = %d", value, page.Code)
		}
		return page.Body.String()
	}
	berlin := render("system.compact.on.Europe/Berlin")
	for _, want := range []string{
		`data-timezone="Europe/Berlin"`,
		`<option value="utc">UTC</option>`,
		`<option value="Europe/Berlin" selected>Europe/Berlin</option>`,
		`Choosing your browser's time zone needs JavaScript.`,
	} {
		if !strings.Contains(berlin, want) {
			t.Errorf("Berlin page missing %q", want)
		}
	}
	// Defaults, older three-field cookies, and zones that no longer load all
	// render UTC, labelled UTC.
	for _, value := range []string{"", "dark.compact.on", "dark.compact.on.Mars/Olympus"} {
		body := render(value)
		if !strings.Contains(body, `data-timezone="UTC"`) ||
			!strings.Contains(body, `<option value="utc" selected>UTC</option>`) ||
			strings.Contains(body, "Mars") {
			t.Errorf("cookie %q page lacks the UTC default", value)
		}
	}

	// Omitting the field keeps the stored zone.
	keptRequest := managementRequest(t, fixture, cookie, "/preferences",
		url.Values{"theme": {"dark"}, "density": {"compact"}}, true)
	keptRequest.AddCookie(&http.Cookie{
		Name: prefsCookieName, Value: "system.compact.off.Europe/Berlin",
	})
	kept := httptest.NewRecorder()
	fixture.handler().ServeHTTP(kept, keptRequest)
	if !strings.Contains(kept.Header().Get("Set-Cookie"),
		"siftail_prefs=dark.compact.off.Europe/Berlin;") {
		t.Fatalf("omitted timezone = %d %#v", kept.Code, kept.Header())
	}
}

func TestHistoryAndEventDetailRenderInTheDisplayZone(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	berlin := &http.Cookie{Name: prefsCookieName, Value: "system.compact.on.Europe/Berlin"}

	get := func(target string, cookies ...*http.Cookie) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		for _, value := range cookies {
			request.AddCookie(value)
		}
		response := httptest.NewRecorder()
		fixture.handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %q", target, response.Code, response.Body.String())
		}
		return response.Body.String()
	}

	page := get("/logs?"+baseHistoryValues().Encode(), cookie, berlin)
	for _, want := range []string{
		`data-timezone="Europe/Berlin"`,
		`<time datetime="2026-07-28T00:30:00Z">02:30:00.000</time>`,
		`<time datetime="2026-07-28T00:40:00Z">02:40:00.000</time>`,
		`28 Jul 2026 02:00–28 Jul 2026 03:00 Europe/Berlin`,
		`<span class="tail-zone">Times in Europe/Berlin</span>`,
		`aria-label="History log events, times in Europe/Berlin"`,
		`<time datetime="2026-07-28T00:00:00Z">28 Jul 2026 02:00:00 Europe/Berlin</time>`,
		`From (<span data-range-zone>UTC</span>)`,
		`name="from" value="2026-07-28T00:00:00Z"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Berlin History missing %q", want)
		}
	}
	if strings.Contains(page, "data-show-date") || strings.Contains(page, "data-local-time") {
		t.Error("one-hour History must not carry dates or browser-rewritten times")
	}

	// Without a preference everything is UTC, labelled UTC, with no suffix
	// on rows.
	utc := get("/logs?"+baseHistoryValues().Encode(), cookie)
	for _, want := range []string{
		`<time datetime="2026-07-28T00:30:00Z">00:30:00.000</time>`,
		`28 Jul 2026 00:00–28 Jul 2026 01:00 UTC`,
		`Times in UTC`,
	} {
		if !strings.Contains(utc, want) {
			t.Errorf("UTC History missing %q", want)
		}
	}

	// A range longer than one day adds the date and widens the column.
	wide := baseHistoryValues()
	wide.Set("from", "2026-07-26T00:00:00Z")
	wideBody := get("/logs?"+wide.Encode(), cookie, berlin)
	if !strings.Contains(wideBody, `<time datetime="2026-07-28T00:30:00Z">28 Jul 02:30:00.000</time>`) ||
		!strings.Contains(wideBody, `role="list" aria-label="History log events, times in Europe/Berlin" data-show-date`) {
		t.Errorf("multi-day History lacks dated rows: %q", wideBody)
	}

	detail := get("/logs/events/1", cookie, berlin)
	for _, want := range []string{
		`<dt>Event time (Europe/Berlin)</dt><dd><time datetime="2026-07-28T00:30:00Z">28 Jul 2026 02:30:00.000000</time>`,
		`<dt>Event time (UTC)</dt><dd><time datetime="2026-07-28T00:30:00Z">2026-07-28T00:30:00Z</time>`,
		`<dt>Received time (Europe/Berlin)</dt><dd><time datetime="2026-07-28T00:30:00.000001Z">28 Jul 2026 02:30:00.000001</time>`,
		`<dt>Delivery delay</dt><dd>1µs</dd>`,
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("Berlin detail missing %q", want)
		}
	}
	utcDetail := get("/logs/events/1", cookie)
	if strings.Count(utcDetail, "Event time (") != 1 ||
		!strings.Contains(utcDetail, `<dt>Received time (UTC)</dt>`) {
		t.Errorf("UTC detail should show one event time and UTC received time: %q", utcDetail)
	}
}

func TestStatusAuditAndLiveLabelTheDisplayZone(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	berlin := &http.Cookie{Name: prefsCookieName, Value: "system.compact.on.Europe/Berlin"}
	at := time.Date(2026, 7, 28, 10, 15, 30, 0, time.UTC)
	if err := fixture.operational.RecordDiagnostic(statusstate.DiagnosticInput{
		At: at, Component: "database", Category: "database_check_succeeded",
	}); err != nil {
		t.Fatal(err)
	}

	var get func(target string) string
	get = func(target string) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(cookie)
		request.AddCookie(berlin)
		response := httptest.NewRecorder()
		fixture.handler().ServeHTTP(response, request)
		if response.Code == http.StatusSeeOther {
			return get(response.Header().Get("Location"))
		}
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", target, response.Code)
		}
		return response.Body.String()
	}

	status := get("/status")
	if !strings.Contains(status,
		`<time datetime="2026-07-28T10:15:30Z">28 Jul 2026 12:15:30 Europe/Berlin</time>`) {
		t.Errorf("Status diagnostic lacks a zoned, datetime-bearing time: %q", status)
	}

	// The sign-in above wrote an audit event; its time element must carry
	// datetime and the column header names the zone.
	audit := get("/audit")
	if !strings.Contains(audit, `<th scope="col">Time (Europe/Berlin)</th>`) ||
		!strings.Contains(audit, `<td><time datetime="`) ||
		strings.Contains(audit, "<time>") {
		t.Errorf("Audit lacks zone label or datetime: %q", audit)
	}

	live := get("/logs?mode=live")
	if !strings.Contains(live, `data-timezone="Europe/Berlin"`) ||
		!strings.Contains(live, `Times in Europe/Berlin`) {
		t.Errorf("Live lacks the zone attribute and label: %q", live)
	}
}

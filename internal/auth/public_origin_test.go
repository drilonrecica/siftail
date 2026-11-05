package auth

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/sessions"
)

func TestPublicOriginSerializesLikeBrowserOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		"https://logs.example.test":         "https://logs.example.test",
		"https://logs.example.test/":        "https://logs.example.test",
		"HTTPS://Logs.Example.Test":         "https://logs.example.test",
		"https://logs.example.test:443":     "https://logs.example.test",
		"http://127.0.0.1:80":               "http://127.0.0.1",
		"https://logs.example.test:8443":    "https://logs.example.test:8443",
		"http://127.0.0.1:8080":             "http://127.0.0.1:8080",
		"http://[::1]:8080":                 "http://[::1]:8080",
		"":                                  "",
		"ftp://logs.example.test":           "",
		"https://user:pw@logs.example.test": "",
	} {
		if got := publicOrigin(raw); got != want {
			t.Errorf("publicOrigin(%q) = %q, want %q", raw, got, want)
		}
	}
}

// DNS rebinding: an attacker's page whose name resolves to Siftail sends a
// matching Host and Origin. Neither Host nor forwarded headers may ever widen
// the allowed origin beyond SIFTAIL_PUBLIC_URL (ADR 0010, audit SEC-08).
func TestBrowserOriginComesOnlyFromPublicURL(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	for _, test := range []struct {
		name    string
		host    string
		origin  string
		headers map[string]string
	}{
		{"rebound host", "rebind.attacker.test", "http://rebind.attacker.test", nil},
		{"forwarded host", "rebind.attacker.test", "https://rebind.attacker.test",
			map[string]string{"X-Forwarded-Host": "rebind.attacker.test", "X-Forwarded-Proto": "https"}},
		{"scheme downgrade", "logs.example.test", "http://logs.example.test", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := loginRequest("Admin", "browser-password", "/logs")
			request.Host = test.host
			request.Header.Set("Origin", test.origin)
			for name, value := range test.headers {
				request.Header.Set(name, value)
			}
			response := httptest.NewRecorder()
			fixture.handler().ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || len(response.Result().Cookies()) != 0 {
				t.Fatalf("login = %d cookies=%#v", response.Code, response.Result().Cookies())
			}
		})
	}

	// Without a public URL every mutation fails closed, whatever Host says.
	unconfigured := newBrowserFixture(t, "", true)
	request := loginRequest("Admin", "browser-password", "/logs")
	request.Host = "logs.example.test"
	response := httptest.NewRecorder()
	unconfigured.handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("login without public URL = %d", response.Code)
	}
}

func TestHTTPSPublicURLUsesHostPrefixedCookieAndHSTS(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	loginPage := httptest.NewRecorder()
	fixture.handler().ServeHTTP(loginPage, httptest.NewRequest(http.MethodGet, "/login", nil))
	if got := loginPage.Header().Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Fatalf("HSTS = %q", got)
	}

	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, loginRequest("Admin", "browser-password", "/logs"))
	setCookies := response.Header().Values("Set-Cookie")
	if len(setCookies) != 2 ||
		!strings.HasPrefix(setCookies[0], "__Host-siftail_session=") ||
		!strings.Contains(setCookies[0], "; Path=/") ||
		!strings.Contains(setCookies[0], "; HttpOnly") ||
		!strings.Contains(setCookies[0], "; Secure") ||
		!strings.Contains(setCookies[0], "; SameSite=Strict") ||
		strings.Contains(strings.ToLower(setCookies[0]), "domain=") ||
		!strings.HasPrefix(setCookies[1], "siftail_session=;") ||
		!strings.Contains(setCookies[1], "Max-Age=0") {
		t.Fatalf("Set-Cookie = %q", setCookies)
	}
	cookie := loginBrowserCookie(t, fixture)

	// A legacy unprefixed cookie carrying a still-valid token is ignored on
	// an https origin: only the __Host- cookie authenticates.
	legacy := httptest.NewRequest(http.MethodGet, "/settings", nil)
	legacy.AddCookie(&http.Cookie{Name: sessions.CookieName, Value: cookie.Value})
	legacyResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(legacyResponse, legacy)
	if legacyResponse.Code != http.StatusSeeOther ||
		!strings.HasPrefix(legacyResponse.Header().Get("Location"), "/login?") {
		t.Fatalf("legacy cookie = %d %q", legacyResponse.Code, legacyResponse.Header().Get("Location"))
	}

	logout := httptest.NewRecorder()
	fixture.handler().ServeHTTP(logout, logoutRequest(cookie, "https://logs.example.test",
		"application/x-www-form-urlencoded", CSRFToken(cookie.Value)))
	cleared := logout.Header().Values("Set-Cookie")
	if logout.Code != http.StatusSeeOther || len(cleared) != 2 ||
		!strings.HasPrefix(cleared[0], "__Host-siftail_session=;") ||
		!strings.Contains(cleared[0], "; Secure") ||
		!strings.HasPrefix(cleared[1], "siftail_session=;") {
		t.Fatalf("logout = %d %q", logout.Code, cleared)
	}
}

func TestHTTPPublicURLUsesPlainCookieWithoutHSTS(t *testing.T) {
	fixture := newBrowserFixture(t, "http://127.0.0.1:8080", true)
	request := loginRequest("Admin", "browser-password", "/logs")
	request.Header.Set("Origin", "http://127.0.0.1:8080")
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	cookies := response.Result().Cookies()
	if response.Code != http.StatusSeeOther || len(cookies) != 1 ||
		cookies[0].Name != sessions.CookieName || cookies[0].Secure ||
		!cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("login = %d %#v", response.Code, cookies)
	}
	if got := response.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("http origin sent HSTS %q", got)
	}
	if fixture.browser.preferencesSecure() {
		t.Fatal("http origin marks preference cookies Secure")
	}
}

func TestSignOutOtherSessionsKeepsCurrentAndAudits(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	current := loginBrowserCookie(t, fixture)
	other := loginBrowserCookie(t, fixture)
	third := loginBrowserCookie(t, fixture)

	page := httptest.NewRequest(http.MethodGet, "/settings", nil)
	page.AddCookie(current)
	before := httptest.NewRecorder()
	fixture.handler().ServeHTTP(before, page)
	if before.Code != http.StatusOK ||
		!strings.Contains(before.Body.String(), `action="/settings/sessions/revoke-others"`) ||
		!strings.Contains(before.Body.String(), "2 other active sessions.") ||
		!strings.Contains(before.Body.String(), "12 hours without use and 7 days after sign-in") {
		t.Fatalf("Settings = %d %q", before.Code, before.Body.String())
	}

	for _, test := range []struct {
		name   string
		values url.Values
		csrf   bool
		origin string
		status int
	}{
		{"missing CSRF", url.Values{}, false, "https://logs.example.test", http.StatusForbidden},
		{"cross origin", url.Values{}, true, "https://attacker.test", http.StatusForbidden},
		{"unexpected field", url.Values{"session_id": {"1"}}, true,
			"https://logs.example.test", http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := managementRequest(t, fixture, current,
				"/settings/sessions/revoke-others", test.values, test.csrf)
			request.Header.Set("Origin", test.origin)
			response := httptest.NewRecorder()
			fixture.handler().ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			if _, err := fixture.sessions.Lookup(context.Background(), other.Value); err != nil {
				t.Fatalf("rejected request signed out another session: %v", err)
			}
		})
	}
	getRequest := httptest.NewRequest(http.MethodGet, "/settings/sessions/revoke-others", nil)
	getRequest.AddCookie(current)
	getResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET revoke-others = %d", getResponse.Code)
	}

	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, managementRequest(t, fixture, current,
		"/settings/sessions/revoke-others", url.Values{}, true))
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/settings" {
		t.Fatalf("revoke others = %d %q", response.Code, response.Header().Get("Location"))
	}
	var flash *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == flashCookieName {
			flash = cookie
		}
		if cookie.Name == fixture.browser.cookies.Name() {
			t.Fatalf("revoke others changed the current session cookie: %#v", cookie)
		}
	}
	if flash == nil || flash.Value != "other-sessions-signed-out" {
		t.Fatalf("flash = %#v", flash)
	}
	if _, err := fixture.sessions.Lookup(context.Background(), current.Value); err != nil {
		t.Fatalf("current session ended: %v", err)
	}
	for _, cookie := range []*http.Cookie{other, third} {
		request := httptest.NewRequest(http.MethodGet, "/logs?mode=history", nil)
		request.AddCookie(cookie)
		redirected := httptest.NewRecorder()
		fixture.handler().ServeHTTP(redirected, request)
		if redirected.Code != http.StatusSeeOther ||
			!strings.Contains(redirected.Header().Get("Location"), "expired=1") {
			t.Fatalf("signed-out session = %d %q", redirected.Code, redirected.Header().Get("Location"))
		}
	}

	var actor, metadata string
	if err := fixture.db.Reader().QueryRow(`SELECT actor_type, safe_metadata_json
		FROM security_audit_events WHERE action='session.revoke_others'`).
		Scan(&actor, &metadata); err != nil {
		t.Fatal(err)
	}
	if actor != "administrator" || metadata != `{"session_count":"2"}` {
		t.Fatalf("audit actor=%q metadata=%s", actor, metadata)
	}

	after := httptest.NewRequest(http.MethodGet, "/settings", nil)
	after.AddCookie(current)
	after.AddCookie(flash)
	afterResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(afterResponse, after)
	if !strings.Contains(afterResponse.Body.String(), "Other sessions signed out.") ||
		!strings.Contains(afterResponse.Body.String(), "No other active sessions.") {
		t.Fatalf("Settings after = %q", afterResponse.Body.String())
	}

	again := httptest.NewRecorder()
	fixture.handler().ServeHTTP(again, managementRequest(t, fixture, current,
		"/settings/sessions/revoke-others", url.Values{}, true))
	for _, cookie := range again.Result().Cookies() {
		if cookie.Name == flashCookieName && cookie.Value != "no-other-sessions" {
			t.Fatalf("second flash = %q", cookie.Value)
		}
	}
}

func TestSignOutOtherSessionsEndsTheirLiveStreams(t *testing.T) {
	fixture, _ := newLiveBrowserFixture(t, logs.LiveBrokerOptions{})
	fixture.browser.liveHeartbeat = 10 * time.Millisecond
	fixture.browser.liveSessionCheck = 10 * time.Millisecond
	current := loginBrowserCookie(t, fixture)
	other := loginBrowserCookie(t, fixture)
	server := httptest.NewServer(fixture.handler())
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	open := func(cookie *http.Cookie) *bufio.Reader {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet,
			server.URL+"/logs/live/stream", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(cookie)
		request.Header.Set("Accept", "text/event-stream")
		request.Header.Set("Origin", fixture.browser.publicURL)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		reader := bufio.NewReader(response.Body)
		_ = readLiveFrame(t, reader)
		return reader
	}
	currentStream := open(current)
	otherStream := open(other)

	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, managementRequest(t, fixture, current,
		"/settings/sessions/revoke-others", url.Values{}, true))
	if response.Code != http.StatusSeeOther {
		t.Fatalf("revoke others = %d", response.Code)
	}
	for {
		if frame := readLiveFrame(t, otherStream); strings.Contains(frame, `"type":"session_invalid"`) {
			break
		}
	}
	// The current session's stream keeps running past several re-checks.
	for range 5 {
		if frame := readLiveFrame(t, currentStream); strings.Contains(frame, `"type":"session_invalid"`) {
			t.Fatalf("current stream ended: %q", frame)
		}
	}
}

package auth

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/retention"
	"github.com/drilonrecica/siftail/internal/sessions"
	"github.com/drilonrecica/siftail/internal/sources"
	statusstate "github.com/drilonrecica/siftail/internal/status"
	"github.com/drilonrecica/siftail/internal/web"
)

type browserFixture struct {
	db          *database.DB
	browser     *Browser
	sessions    *sessions.Store
	coordinator *database.Coordinator
	operational *statusstate.State
	cancel      context.CancelFunc
	done        chan error
}

func newBrowserFixture(t *testing.T, publicURL string, administrator bool) *browserFixture {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "siftail.db")
	db, err := database.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if administrator {
		if _, err := NewStore(db.Writer()).Create(
			context.Background(), "Admin", []byte("browser-password"),
		); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := database.NewCoordinator(db.Writer())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	<-coordinator.Ready()
	adminStore := NewCoordinatedStore(db.Reader(), coordinator)
	sessionStore := sessions.NewCoordinatedStore(db.Reader(), coordinator)
	codec, err := logs.LoadCursorCodec(context.Background(), db.Reader(), coordinator)
	if err != nil {
		t.Fatal(err)
	}
	retentionStore := retention.NewStore(db.Reader(), coordinator)
	operationalState := statusstate.NewState(time.Now())
	operationalState.SetWriterReady(true)
	sourceStore := sources.NewCoordinatedStore(db.Reader(), coordinator)
	ingestPublicURL := "https://ingest.example.test/api/v1/ingest"
	guideTester, err := ingest.NewGuideTester(ingestPublicURL, sourceStore)
	if err != nil {
		t.Fatal(err)
	}
	browser := NewBrowser(adminStore, sessionStore, BrowserConfig{
		PublicURL: publicURL, IngestPublicURL: ingestPublicURL,
		GuideTester:    guideTester,
		HistoryStore:   logs.NewHistoryStore(db.Reader(), codec),
		SourceStore:    sourceStore,
		RetentionStore: retentionStore,
		StatusStore: statusstate.NewStore(
			db.Reader(), databasePath, nil, retentionStore, operationalState,
		),
	})
	fixture := &browserFixture{
		db: db, browser: browser, sessions: sessionStore,
		coordinator: coordinator, operational: operationalState,
		cancel: cancel, done: done,
	}
	t.Cleanup(func() {
		coordinator.Close()
		cancel()
		<-done
		_ = db.Close()
	})
	return fixture
}

func (f *browserFixture) handler() http.Handler {
	mux := http.NewServeMux()
	f.browser.Register(mux)
	return SecurityHeaders(f.browser.publicURL)(mux)
}

func TestLoginMissingAdministratorAndUniformFailures(t *testing.T) {
	missing := newBrowserFixture(t, "https://logs.example.test", false)
	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	response := httptest.NewRecorder()
	missing.handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "No administrator is configured") ||
		strings.Contains(response.Body.String(), "<name>") {
		t.Fatalf("missing administrator page = %d %q", response.Code, response.Body.String())
	}

	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	wrong := loginRequest("Admin", "wrong-password", "/logs")
	unknown := loginRequest("Missing", "wrong-password", "/logs")
	first := httptest.NewRecorder()
	second := httptest.NewRecorder()
	fixture.handler().ServeHTTP(first, wrong)
	fixture.handler().ServeHTTP(second, unknown)
	if first.Code != http.StatusUnauthorized || second.Code != http.StatusUnauthorized ||
		first.Body.String() != second.Body.String() {
		t.Fatalf("failure responses differ: %d/%q vs %d/%q",
			first.Code, first.Body.String(), second.Code, second.Body.String())
	}
}

func TestLoginSessionProtectedPageAndLogoutSecurity(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, loginRequest("Admin", "browser-password", "/logs?mode=history"))
	if response.Code != http.StatusSeeOther ||
		response.Header().Get("Location") != "/logs?mode=history" {
		t.Fatalf("login response = %d %q", response.Code, response.Header().Get("Location"))
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != sessions.CookieName || !cookie.HttpOnly || !cookie.Secure ||
		cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("session cookie = %#v", cookie)
	}

	logsRequest := httptest.NewRequest(http.MethodGet,
		"/logs?mode=history&from=2026-07-28T00%3A00%3A00Z&to=2026-07-28T01%3A00%3A00Z&direction=older&limit=200",
		nil,
	)
	logsRequest.AddCookie(cookie)
	logsResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(logsResponse, logsRequest)
	if logsResponse.Code != http.StatusOK ||
		logsResponse.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(logsResponse.Body.String(), "csrf_token") {
		t.Fatalf("protected response = %d %#v %q", logsResponse.Code, logsResponse.Header(), logsResponse.Body.String())
	}
	csrf := CSRFToken(cookie.Value)

	for _, test := range []struct {
		name        string
		origin      string
		contentType string
		csrf        string
	}{
		{"missing origin", "", "application/x-www-form-urlencoded", csrf},
		{"wrong origin", "https://attacker.test", "application/x-www-form-urlencoded", csrf},
		{"wrong type", "https://logs.example.test", "application/json", csrf},
		{"wrong csrf", "https://logs.example.test", "application/x-www-form-urlencoded", "wrong"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := logoutRequest(cookie, test.origin, test.contentType, test.csrf)
			response := httptest.NewRecorder()
			fixture.handler().ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d", response.Code)
			}
		})
	}

	logout := logoutRequest(cookie, "https://logs.example.test", "application/x-www-form-urlencoded", csrf)
	logoutResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(logoutResponse, logout)
	if logoutResponse.Code != http.StatusSeeOther ||
		logoutResponse.Header().Get("Location") != "/login" {
		t.Fatalf("logout = %d %q", logoutResponse.Code, logoutResponse.Header().Get("Location"))
	}
	if _, err := fixture.sessions.Lookup(context.Background(), cookie.Value); err == nil {
		t.Fatal("logged-out session remained valid")
	}
}

func TestLoginThrottleFifthFailureAndBoundedState(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	for attempt := 1; attempt <= 5; attempt++ {
		response := httptest.NewRecorder()
		fixture.handler().ServeHTTP(response, loginRequest("Admin", "wrong-password", "/logs"))
		want := http.StatusUnauthorized
		if attempt == 5 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.Code, want)
		}
		if attempt == 5 && response.Header().Get("Retry-After") == "" {
			t.Fatal("throttled response lacks Retry-After")
		}
	}

	throttle := newLoginThrottle()
	now := time.Unix(1, 0)
	throttle.now = func() time.Time { return now }
	for i := 0; i < throttleCapacity+100; i++ {
		throttle.Failure(clientThrottleKey(string(rune(i + 1))))
		now = now.Add(time.Microsecond)
	}
	if len(throttle.entries) != throttleCapacity {
		t.Fatalf("throttle entries = %d", len(throttle.entries))
	}
	now = now.Add(throttleIdle + time.Second)
	throttle.Check("new")
	if len(throttle.entries) != 0 {
		t.Fatalf("idle throttle entries = %d", len(throttle.entries))
	}
}

func TestExpiredSessionAndSafeReturnPaths(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, loginRequest("Admin", "browser-password", "https://attacker.test"))
	if response.Header().Get("Location") != "/logs" {
		t.Fatalf("unsafe login redirect = %q", response.Header().Get("Location"))
	}
	cookie := response.Result().Cookies()[0]
	if _, err := fixture.db.Writer().Exec("UPDATE sessions SET expires_at_us=created_at_us+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/logs?mode=history", nil)
	request.AddCookie(cookie)
	expired := httptest.NewRecorder()
	fixture.handler().ServeHTTP(expired, request)
	if expired.Code != http.StatusSeeOther ||
		!strings.HasPrefix(expired.Header().Get("Location"), "/login?return=") ||
		!strings.Contains(expired.Header().Get("Location"), "expired=1") {
		t.Fatalf("expired redirect = %d %q", expired.Code, expired.Header().Get("Location"))
	}
}

func TestProxyTrustIgnoresSpoofedForwardingFromUntrustedPeer(t *testing.T) {
	trust := newProxyTrust([]string{"10.0.0.0/8"})
	request := httptest.NewRequest(http.MethodGet, "http://internal/login", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "logs.example.test")
	if got := trust.clientIdentity(request); got != "192.0.2.10" {
		t.Fatalf("untrusted client identity = %q", got)
	}
	if got := trust.requestOrigin(request); got != "http://internal" {
		t.Fatalf("untrusted origin = %q", got)
	}
	request.RemoteAddr = "10.0.0.2:1234"
	if got := trust.clientIdentity(request); got != "203.0.113.9" {
		t.Fatalf("trusted client identity = %q", got)
	}
	if got := trust.requestOrigin(request); got != "https://logs.example.test" {
		t.Fatalf("trusted origin = %q", got)
	}
	// Identity headers are deliberately irrelevant to every resolver.
	request.Header.Set("Remote-User", "administrator")
	request.Header.Set("X-Forwarded-User", "administrator")
	if got := trust.clientIdentity(request); got != "203.0.113.9" {
		t.Fatalf("identity header changed client = %q", got)
	}
}

func TestSecurityHeadersPanicRecoveryRequestIDAndSensitiveLogs(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	handler := web.RequestID(web.PanicRecovery(logger)(SecurityHeaders("https://logs.example.test")(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("password private-marker")
		}),
	)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/login", nil))
	if response.Code != http.StatusInternalServerError ||
		response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("panic response = %d %#v", response.Code, response.Header())
	}
	for _, header := range []string{
		"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy",
		"Permissions-Policy", "Cross-Origin-Opener-Policy",
		"Cross-Origin-Resource-Policy", "X-Frame-Options", "Strict-Transport-Security",
	} {
		if response.Header().Get(header) == "" {
			t.Errorf("missing header %s", header)
		}
	}
	if strings.Contains(logs.String(), "password private-marker") {
		t.Fatalf("panic log leaked value: %s", logs.String())
	}
}

func TestLoginReferrerPolicySupportsExactSameOriginFormValidation(t *testing.T) {
	handler := SecurityHeaders("https://logs.example.test")(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	))
	for path, want := range map[string]string{
		"/login":     "same-origin",
		"/session":   "same-origin",
		"/logs":      "same-origin",
		"/logs/rows": "same-origin",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if got := response.Header().Get("Referrer-Policy"); got != want {
			t.Errorf("%s Referrer-Policy = %q, want %q", path, got, want)
		}
	}
}

func loginRequest(username, password, returnPath string) *http.Request {
	form := url.Values{"username": {username}, "password": {password}, "return": {returnPath}}
	request := httptest.NewRequest(http.MethodPost, "/session", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://logs.example.test")
	request.RemoteAddr = "192.0.2.1:1234"
	return request
}

func logoutRequest(cookie *http.Cookie, origin, contentType, csrf string) *http.Request {
	form := url.Values{"csrf_token": {csrf}}
	request := httptest.NewRequest(http.MethodPost, "/session/logout", strings.NewReader(form.Encode()))
	request.AddCookie(cookie)
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	request.Header.Set("Content-Type", contentType)
	return request
}

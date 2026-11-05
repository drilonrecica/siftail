package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestBrowserPreferencesPersistAndRenderBeforeFirstPaint(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)

	defaultRequest := httptest.NewRequest(http.MethodGet, "/settings", nil)
	defaultRequest.AddCookie(cookie)
	defaultPage := httptest.NewRecorder()
	fixture.handler().ServeHTTP(defaultPage, defaultRequest)
	body := defaultPage.Body.String()
	if defaultPage.Code != http.StatusOK ||
		strings.Contains(body, "data-theme=") ||
		strings.Contains(body, `data-density="comfortable"`) ||
		!strings.Contains(body, `action="/preferences"`) ||
		!strings.Contains(body, `name="return" value="/settings"`) {
		t.Fatalf("default preference page = %d %q", defaultPage.Code, body)
	}

	saved := httptest.NewRecorder()
	fixture.handler().ServeHTTP(saved, managementRequest(
		t, fixture, cookie, "/preferences",
		url.Values{
			"theme": {"dark"}, "density": {"comfortable"},
			"return": {"/settings"},
		}, true,
	))
	if saved.Code != http.StatusSeeOther ||
		saved.Header().Get("Location") != "/settings" {
		t.Fatalf("save preferences = %d %#v", saved.Code, saved.Header())
	}
	var prefsCookie *http.Cookie
	for _, candidate := range saved.Result().Cookies() {
		if candidate.Name == prefsCookieName {
			prefsCookie = candidate
		}
	}
	// The form omitted the shortcuts field (as a page rendered before it
	// existed would), so the current value, the default On, is kept.
	if prefsCookie == nil || prefsCookie.Value != "dark.comfortable.on" ||
		!prefsCookie.HttpOnly || !prefsCookie.Secure ||
		prefsCookie.SameSite != http.SameSiteLaxMode || prefsCookie.Path != "/" {
		t.Fatalf("preference cookie = %#v", prefsCookie)
	}

	themedRequest := httptest.NewRequest(http.MethodGet, "/settings", nil)
	themedRequest.AddCookie(cookie)
	themedRequest.AddCookie(prefsCookie)
	themed := httptest.NewRecorder()
	fixture.handler().ServeHTTP(themed, themedRequest)
	if themed.Code != http.StatusOK ||
		!strings.Contains(themed.Body.String(), `data-theme="dark"`) ||
		!strings.Contains(themed.Body.String(), `data-density="comfortable"`) ||
		strings.Contains(themed.Body.String(), `data-shortcuts=`) {
		t.Fatalf("themed page = %d %q", themed.Code, themed.Body.String())
	}

	loginRequest := httptest.NewRequest(http.MethodGet, "/login", nil)
	loginRequest.AddCookie(prefsCookie)
	login := httptest.NewRecorder()
	fixture.handler().ServeHTTP(login, loginRequest)
	if login.Code != http.StatusOK ||
		!strings.Contains(login.Body.String(), `data-theme="dark"`) {
		t.Fatalf("login theming = %d %q", login.Code, login.Body.String())
	}
}

func TestBrowserShortcutsPreferenceRendersServerSide(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)

	saved := httptest.NewRecorder()
	fixture.handler().ServeHTTP(saved, managementRequest(
		t, fixture, cookie, "/preferences",
		url.Values{
			"theme": {"system"}, "density": {"compact"}, "shortcuts": {"off"},
			"return": {"/logs"},
		}, true,
	))
	var prefsCookie *http.Cookie
	for _, candidate := range saved.Result().Cookies() {
		if candidate.Name == prefsCookieName {
			prefsCookie = candidate
		}
	}
	if saved.Code != http.StatusSeeOther || prefsCookie == nil ||
		prefsCookie.Value != "system.compact.off" {
		t.Fatalf("save shortcuts off = %d %#v", saved.Code, prefsCookie)
	}

	render := func(value string) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/settings", nil)
		request.AddCookie(cookie)
		request.AddCookie(&http.Cookie{Name: prefsCookieName, Value: value})
		page := httptest.NewRecorder()
		fixture.handler().ServeHTTP(page, request)
		if page.Code != http.StatusOK {
			t.Fatalf("render %q = %d", value, page.Code)
		}
		return page.Body.String()
	}
	off := render("system.compact.off")
	if !strings.Contains(off, `data-shortcuts="off"`) ||
		!strings.Contains(off, `<option value="off" selected>Off</option>`) {
		t.Fatalf("shortcuts off page = %q", off)
	}
	// A cookie written before the shortcuts field existed defaults it to On;
	// a trailing field a later version adds is ignored here.
	for _, value := range []string{"dark.comfortable", "dark.comfortable.on.UTC", "dark.comfortable.bogus"} {
		body := render(value)
		if strings.Contains(body, `data-shortcuts=`) ||
			!strings.Contains(body, `<option value="on" selected>On</option>`) ||
			!strings.Contains(body, `data-theme="dark"`) {
			t.Fatalf("cookie %q page = %q", value, body)
		}
	}

	// A submission that omits the field keeps the stored Off.
	kept := httptest.NewRecorder()
	keptRequest := managementRequest(
		t, fixture, cookie, "/preferences",
		url.Values{"theme": {"dark"}, "density": {"compact"}}, true,
	)
	keptRequest.AddCookie(prefsCookie)
	fixture.handler().ServeHTTP(kept, keptRequest)
	if kept.Code != http.StatusSeeOther ||
		!strings.Contains(kept.Header().Get("Set-Cookie"), "siftail_prefs=dark.compact.off") {
		t.Fatalf("omitted shortcuts = %d %#v", kept.Code, kept.Header())
	}

	invalid := httptest.NewRecorder()
	fixture.handler().ServeHTTP(invalid, managementRequest(
		t, fixture, cookie, "/preferences",
		url.Values{"theme": {"dark"}, "density": {"compact"}, "shortcuts": {"maybe"}}, true,
	))
	if invalid.Code != http.StatusBadRequest || len(invalid.Result().Cookies()) != 0 {
		t.Fatalf("invalid shortcuts = %d %#v", invalid.Code, invalid.Result().Cookies())
	}
}

func TestBrowserPreferencesRejectInvalidValuesAndUnsafeReturns(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)

	invalid := httptest.NewRecorder()
	fixture.handler().ServeHTTP(invalid, managementRequest(
		t, fixture, cookie, "/preferences",
		url.Values{"theme": {"solarized"}, "density": {"compact"}}, true,
	))
	if invalid.Code != http.StatusBadRequest || len(invalid.Result().Cookies()) != 0 {
		t.Fatalf("invalid theme = %d %#v", invalid.Code, invalid.Result().Cookies())
	}

	unsafeReturn := httptest.NewRecorder()
	fixture.handler().ServeHTTP(unsafeReturn, managementRequest(
		t, fixture, cookie, "/preferences",
		url.Values{
			"theme": {"light"}, "density": {"compact"},
			"return": {"https://attacker.example/logs"},
		}, true,
	))
	if unsafeReturn.Code != http.StatusSeeOther ||
		unsafeReturn.Header().Get("Location") != "/logs" {
		t.Fatalf("unsafe return = %d %#v", unsafeReturn.Code, unsafeReturn.Header())
	}

	noCSRF := httptest.NewRecorder()
	fixture.handler().ServeHTTP(noCSRF, managementRequest(
		t, fixture, cookie, "/preferences",
		url.Values{"theme": {"dark"}, "density": {"compact"}}, false,
	))
	if noCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF = %d", noCSRF.Code)
	}

	tamperedRequest := httptest.NewRequest(http.MethodGet, "/settings", nil)
	tamperedRequest.AddCookie(cookie)
	tamperedRequest.AddCookie(&http.Cookie{
		Name: prefsCookieName, Value: `injected."><script>`,
	})
	tampered := httptest.NewRecorder()
	fixture.handler().ServeHTTP(tampered, tamperedRequest)
	if tampered.Code != http.StatusOK ||
		strings.Contains(tampered.Body.String(), "data-theme=") ||
		strings.Contains(tampered.Body.String(), "<script>") {
		t.Fatalf("tampered preference cookie = %d %q",
			tampered.Code, tampered.Body.String())
	}
}

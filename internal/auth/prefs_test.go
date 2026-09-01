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
	if prefsCookie == nil || prefsCookie.Value != "dark.comfortable" ||
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
		!strings.Contains(themed.Body.String(), `data-density="comfortable"`) {
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

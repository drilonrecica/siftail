package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/drilonrecica/siftail/internal/retention"
)

func TestBrowserRetentionSettingsLifecycle(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)

	unauthenticated := httptest.NewRecorder()
	fixture.handler().ServeHTTP(unauthenticated,
		httptest.NewRequest(http.MethodGet, "/settings", nil))
	if unauthenticated.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated Settings status = %d", unauthenticated.Code)
	}

	initialRequest := httptest.NewRequest(http.MethodGet, "/settings", nil)
	initialRequest.AddCookie(cookie)
	initial := httptest.NewRecorder()
	fixture.handler().ServeHTTP(initial, initialRequest)
	body := initial.Body.String()
	if initial.Code != http.StatusOK ||
		initial.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(body, `name="retention_days"`) ||
		!strings.Contains(body, `value="14"`) ||
		!strings.Contains(body, `name="maximum_database_gib"`) ||
		!strings.Contains(body, `value="4"`) ||
		!strings.Contains(body, "not forensic erasure") {
		t.Fatalf("initial Settings = %d %#v %q", initial.Code, initial.Header(), body)
	}

	invalidAge := httptest.NewRecorder()
	fixture.handler().ServeHTTP(invalidAge, managementRequest(
		t, fixture, cookie, "/settings/retention",
		url.Values{"retention_days": {"0"}, "maximum_database_gib": {"4"}}, true,
	))
	if invalidAge.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(invalidAge.Body.String(),
			"Retention must be a whole number from 1 to 3,650 days.") ||
		!strings.Contains(invalidAge.Body.String(), `id="retention-days"`) ||
		!strings.Contains(invalidAge.Body.String(), "autofocus") {
		t.Fatalf("invalid age = %d %q", invalidAge.Code, invalidAge.Body.String())
	}
	if settings, err := fixture.browser.retention.Load(context.Background()); err != nil ||
		settings != retention.Defaults() {
		t.Fatalf("invalid input changed defaults: %#v, err=%v", settings, err)
	}

	ambiguousSize := httptest.NewRecorder()
	fixture.handler().ServeHTTP(ambiguousSize, managementRequest(
		t, fixture, cookie, "/settings/retention",
		url.Values{"retention_days": {"14"}, "maximum_database_gib": {"04"}}, true,
	))
	if ambiguousSize.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(ambiguousSize.Body.String(),
			"Maximum database size must be a whole number from 1 to 1,024 GiB.") {
		t.Fatalf("ambiguous size = %d %q", ambiguousSize.Code, ambiguousSize.Body.String())
	}

	saved := httptest.NewRecorder()
	fixture.handler().ServeHTTP(saved, managementRequest(
		t, fixture, cookie, "/settings/retention",
		url.Values{"retention_days": {"30"}, "maximum_database_gib": {"8"}}, true,
	))
	if saved.Code != http.StatusSeeOther ||
		saved.Header().Get("Location") != "/settings?notice=retention-saved" ||
		saved.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("save Settings = %d %#v", saved.Code, saved.Header())
	}
	settings, err := fixture.browser.retention.Load(context.Background())
	if err != nil || settings.AgeDays != 30 || settings.MaxDatabaseGiB() != 8 {
		t.Fatalf("saved Settings = %#v, err=%v", settings, err)
	}

	noticeRequest := httptest.NewRequest(
		http.MethodGet, "/settings?notice=retention-saved", nil,
	)
	noticeRequest.AddCookie(cookie)
	notice := httptest.NewRecorder()
	fixture.handler().ServeHTTP(notice, noticeRequest)
	if notice.Code != http.StatusOK ||
		!strings.Contains(notice.Body.String(), "Retention settings saved.") ||
		!strings.Contains(notice.Body.String(), `value="30"`) ||
		!strings.Contains(notice.Body.String(), `value="8"`) {
		t.Fatalf("saved Settings page = %d %q", notice.Code, notice.Body.String())
	}
}

func TestBrowserRetentionSettingsRejectsUnsafeRequestsAndExpiredSession(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	values := url.Values{"retention_days": {"14"}, "maximum_database_gib": {"4"}}

	noCSRF := httptest.NewRecorder()
	fixture.handler().ServeHTTP(noCSRF, managementRequest(
		t, fixture, cookie, "/settings/retention", values, false,
	))
	if noCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", noCSRF.Code)
	}

	wrongOriginRequest := managementRequest(
		t, fixture, cookie, "/settings/retention", values, true,
	)
	wrongOriginRequest.Header.Set("Origin", "https://attacker.example")
	wrongOrigin := httptest.NewRecorder()
	fixture.handler().ServeHTTP(wrongOrigin, wrongOriginRequest)
	if wrongOrigin.Code != http.StatusForbidden {
		t.Fatalf("wrong Origin status = %d", wrongOrigin.Code)
	}

	unknownField := httptest.NewRecorder()
	fixture.handler().ServeHTTP(unknownField, managementRequest(
		t, fixture, cookie, "/settings/retention",
		url.Values{
			"retention_days": {"14"}, "maximum_database_gib": {"4"},
			"disable_cleanup": {"true"},
		}, true,
	))
	if unknownField.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field status = %d", unknownField.Code)
	}

	if _, err := fixture.db.Writer().Exec(
		"UPDATE sessions SET expires_at_us=created_at_us+1",
	); err != nil {
		t.Fatal(err)
	}
	expiredRequest := httptest.NewRequest(http.MethodGet, "/settings", nil)
	expiredRequest.AddCookie(cookie)
	expired := httptest.NewRecorder()
	fixture.handler().ServeHTTP(expired, expiredRequest)
	if expired.Code != http.StatusSeeOther ||
		!strings.Contains(expired.Header().Get("Location"), "expired=1") {
		t.Fatalf("expired Settings session = %d %q",
			expired.Code, expired.Header().Get("Location"))
	}
}

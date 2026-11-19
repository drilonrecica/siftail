package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFlashMessagesAreConsumedOnFirstRender(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)

	first := httptest.NewRequest(http.MethodGet, "/settings", nil)
	first.AddCookie(cookie)
	first.AddCookie(&http.Cookie{Name: flashCookieName, Value: "retention-saved"})
	firstResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(firstResponse, first)
	if firstResponse.Code != http.StatusOK ||
		!strings.Contains(firstResponse.Body.String(), "Retention settings saved.") {
		t.Fatalf("flash render = %d %q", firstResponse.Code, firstResponse.Body.String())
	}
	consumed := false
	for _, set := range firstResponse.Result().Cookies() {
		if set.Name == flashCookieName && set.MaxAge < 0 {
			consumed = true
		}
	}
	if !consumed {
		t.Fatalf("flash cookie was not expired: %#v", firstResponse.Result().Cookies())
	}

	// A reload without the cookie shows a clean page.
	second := httptest.NewRequest(http.MethodGet, "/settings", nil)
	second.AddCookie(cookie)
	secondResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(secondResponse, second)
	if strings.Contains(secondResponse.Body.String(), "Retention settings saved.") {
		t.Fatal("stale flash re-rendered after reload")
	}

	// A tampered code renders nothing rather than arbitrary content.
	tampered := httptest.NewRequest(http.MethodGet, "/settings", nil)
	tampered.AddCookie(cookie)
	tampered.AddCookie(&http.Cookie{Name: flashCookieName, Value: "not-a-code"})
	tamperedResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(tamperedResponse, tampered)
	if tamperedResponse.Code != http.StatusOK ||
		strings.Contains(tamperedResponse.Body.String(), "data-flash") {
		t.Fatalf("tampered flash = %d", tamperedResponse.Code)
	}
}

// The flash is visible text, not a live region present at load; the shell's
// one announcer starts empty and app.js speaks the flash into it after load
// (DESIGN.md §26, audit UX-16).
func TestFlashIsAnnouncedThroughTheSharedAnnouncer(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	request := httptest.NewRequest(http.MethodGet, "/settings", nil)
	request.AddCookie(cookie)
	request.AddCookie(&http.Cookie{Name: flashCookieName, Value: "retention-saved"})
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	body := response.Body.String()
	if !strings.Contains(body, `<p class="notice notice-success" data-flash data-flash-dismiss>Retention settings saved.</p>`) {
		t.Fatalf("flash markup changed: %q", body)
	}
	announcer := `<p class="visually-hidden" role="status" aria-live="polite" aria-atomic="true" data-announcer></p>`
	if strings.Count(body, "data-announcer") != 1 || !strings.Contains(body, announcer) {
		t.Fatalf("shell announcer missing or not empty at load: %q", body)
	}
}

// Backup polling swaps #backup-region every second; the region holds no live
// region, and exposes its state for app.js to announce once (audit UX-31).
func TestBackupRegionHoldsNoLiveRegion(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	for _, target := range []string{"/backup", "/backup/region"} {
		code, body := getPage(t, fixture, cookie, target)
		if code != http.StatusOK {
			t.Fatalf("%s = %d", target, code)
		}
		start := strings.Index(body, `<section id="backup-region"`)
		if start < 0 {
			t.Fatalf("%s has no backup region: %q", target, body)
		}
		region := body[start:]
		if end := strings.Index(region, "</main>"); end >= 0 {
			region = region[:end]
		}
		if strings.Contains(region, "aria-live") || strings.Contains(region, `role="status"`) {
			t.Errorf("%s backup region contains a live region", target)
		}
		if !strings.Contains(region, `data-backup-state="idle"`) ||
			!strings.Contains(region, `data-backup-label="No backup has run during this process."`) {
			t.Errorf("%s backup region does not expose its state", target)
		}
	}
}

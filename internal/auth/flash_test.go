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

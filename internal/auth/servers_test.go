package auth

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/web"
)

func TestBrowserServerAndOneTimeTokenLifecycle(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)

	unauthenticated := httptest.NewRecorder()
	fixture.handler().ServeHTTP(unauthenticated,
		httptest.NewRequest(http.MethodGet, "/servers", nil))
	if unauthenticated.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated Servers status = %d", unauthenticated.Code)
	}
	noCSRF := managementRequest(t, fixture, cookie, "/servers",
		url.Values{"name": {"Production"}, "hostname": {""}}, false)
	noCSRFResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(noCSRFResponse, noCSRF)
	if noCSRFResponse.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", noCSRFResponse.Code)
	}

	createServer := managementRequest(t, fixture, cookie, "/servers",
		url.Values{"name": {"Production <unsafe>"}, "hostname": {"prod.example"}}, true)
	createServerResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(createServerResponse, createServer)
	if createServerResponse.Code != http.StatusSeeOther ||
		createServerResponse.Header().Get("Location") != "/servers/1" {
		t.Fatalf("create Server = %d %#v",
			createServerResponse.Code, createServerResponse.Header())
	}

	var processLogs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&processLogs, nil))
	handler := web.RequestID(web.RequestLogger(logger)(fixture.handler()))
	createToken := managementRequest(t, fixture, cookie, "/servers/1/tokens",
		url.Values{"name": {"primary"}}, true)
	createTokenResponse := httptest.NewRecorder()
	handler.ServeHTTP(createTokenResponse, createToken)
	body := createTokenResponse.Body.String()
	plaintext := oneTimeBrowserToken(t, body)
	if createTokenResponse.Code != http.StatusCreated ||
		createTokenResponse.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(body, "Siftail stores only a hash and cannot show it again") ||
		!strings.Contains(body, `data-one-time-token`) ||
		!strings.Contains(body, "Coolify custom Fluent Bit configuration") ||
		!strings.Contains(body, "Guided committed-receipt test") ||
		!strings.Contains(body, ingest.GuideTokenPlaceholder) ||
		strings.Count(body, plaintext) != 1 ||
		strings.Contains(createTokenResponse.Header().Get("Location"), plaintext) ||
		strings.Contains(processLogs.String(), plaintext) {
		t.Fatalf("one-time token response = %d %#v %q logs=%q",
			createTokenResponse.Code, createTokenResponse.Header(), body, processLogs.String())
	}
	var storedHash []byte
	if err := fixture.db.Reader().QueryRow(`SELECT token_hash FROM ingestion_tokens WHERE id=1`).
		Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(storedHash), plaintext) {
		t.Fatal("database stored token plaintext")
	}
	if _, err := fixture.browser.sources.VerifyToken(
		context.Background(), plaintext,
	); err != nil {
		t.Fatalf("created token did not authenticate: %v", err)
	}

	var delivered bool
	ingestion := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered = true
		if r.URL.Path != "/api/v1/ingest" ||
			r.Header.Get("Authorization") != "Bearer "+plaintext ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("guided request = %s %#v", r.URL.Path, r.Header)
		}
		w.Header().Set("X-Siftail-Ingest-Outcome", "committed")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ingestion.Close()
	tester, err := ingest.NewGuideTester(
		ingestion.URL+"/api/v1/ingest", fixture.browser.sources,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.browser.guideTester = tester
	guided := httptest.NewRecorder()
	handler.ServeHTTP(guided, managementRequest(
		t, fixture, cookie, "/servers/1/test-ingestion",
		url.Values{"token": {plaintext}}, true,
	))
	if guided.Code != http.StatusOK || !delivered ||
		!strings.Contains(guided.Body.String(), `"outcome":"committed"`) ||
		strings.Contains(guided.Body.String(), plaintext) ||
		strings.Contains(processLogs.String(), plaintext) {
		t.Fatalf("guided response = %d %q logs=%q",
			guided.Code, guided.Body.String(), processLogs.String())
	}

	wrongToken := httptest.NewRecorder()
	handler.ServeHTTP(wrongToken, managementRequest(
		t, fixture, cookie, "/servers/1/test-ingestion",
		url.Values{"token": {"sft_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}, true,
	))
	if wrongToken.Code != http.StatusOK ||
		!strings.Contains(wrongToken.Body.String(), `"outcome":"authentication-rejected"`) {
		t.Fatalf("wrong-token result = %d %q", wrongToken.Code, wrongToken.Body.String())
	}

	detailRequest := httptest.NewRequest(http.MethodGet, "/servers/1", nil)
	detailRequest.AddCookie(cookie)
	detailResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK ||
		strings.Contains(detailResponse.Body.String(), plaintext) ||
		!strings.Contains(detailResponse.Body.String(), "primary") ||
		!strings.Contains(detailResponse.Body.String(), "Active") {
		t.Fatalf("Server detail = %d %q", detailResponse.Code, detailResponse.Body.String())
	}

	replacementResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(replacementResponse, managementRequest(
		t, fixture, cookie, "/servers/1/tokens",
		url.Values{"name": {"replacement"}}, true,
	))
	replacement := oneTimeBrowserToken(t, replacementResponse.Body.String())
	for _, token := range []string{plaintext, replacement} {
		if _, err := fixture.browser.sources.VerifyToken(
			context.Background(), token,
		); err != nil {
			t.Fatalf("rotation revoked token early: %v", err)
		}
	}

	badRevoke := httptest.NewRecorder()
	fixture.handler().ServeHTTP(badRevoke, managementRequest(
		t, fixture, cookie, "/tokens/1/revoke",
		url.Values{"confirmation": {"wrong"}}, true,
	))
	if badRevoke.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(badRevoke.Body.String(), "Type the token name exactly") {
		t.Fatalf("bad revoke = %d %q", badRevoke.Code, badRevoke.Body.String())
	}
	revoke := httptest.NewRecorder()
	fixture.handler().ServeHTTP(revoke, managementRequest(
		t, fixture, cookie, "/tokens/1/revoke",
		url.Values{"confirmation": {"primary"}}, true,
	))
	if revoke.Code != http.StatusSeeOther ||
		revoke.Header().Get("Location") != "/servers/1?notice=token-revoked" {
		t.Fatalf("revoke = %d %#v", revoke.Code, revoke.Header())
	}
	if _, err := fixture.browser.sources.VerifyToken(
		context.Background(), plaintext,
	); err == nil {
		t.Fatal("revoked token still authenticated")
	}
	if _, err := fixture.browser.sources.VerifyToken(
		context.Background(), replacement,
	); err != nil {
		t.Fatal("replacement token was revoked")
	}
}

func TestBrowserServerManagementRejectsUnsafeAndDuplicateForms(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	for _, test := range []struct {
		target string
		values url.Values
		status int
	}{
		{"/servers", url.Values{"name": {strings.Repeat("x", 129)}, "hostname": {""}},
			http.StatusUnprocessableEntity},
		{"/servers", url.Values{"name": {"One"}, "hostname": {""}, "role": {"admin"}},
			http.StatusBadRequest},
		{"/servers/not-an-id/tokens", url.Values{"name": {"token"}}, http.StatusBadRequest},
		{"/tokens/not-an-id/revoke", url.Values{"confirmation": {"token"}}, http.StatusBadRequest},
	} {
		response := httptest.NewRecorder()
		fixture.handler().ServeHTTP(response,
			managementRequest(t, fixture, cookie, test.target, test.values, true))
		if response.Code != test.status {
			t.Errorf("%s = %d, want %d", test.target, response.Code, test.status)
		}
	}
	wrongOrigin := managementRequest(t, fixture, cookie, "/servers",
		url.Values{"name": {"Wrong Origin"}, "hostname": {""}}, true)
	wrongOrigin.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, wrongOrigin)
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong Origin status = %d", response.Code)
	}
}

func managementRequest(
	t *testing.T,
	fixture *browserFixture,
	cookie *http.Cookie,
	target string,
	values url.Values,
	withCSRF bool,
) *http.Request {
	t.Helper()
	if withCSRF {
		values.Set("csrf_token", CSRFToken(cookie.Value))
	}
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", fixture.browser.publicURL)
	request.AddCookie(cookie)
	return request
}

func oneTimeBrowserToken(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`data-token-secret type="password" value="(sft_[^"]+)"`).
		FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("one-time token missing from response: %q", body)
	}
	return match[1]
}

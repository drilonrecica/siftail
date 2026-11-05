package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/web"
)

func TestAuditPageRequiresAuthenticationDefaultsAndRendersSafeFilters(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	handler := fixture.handler()

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/audit", nil))
	if unauthenticated.Code != http.StatusSeeOther ||
		!strings.HasPrefix(unauthenticated.Header().Get("Location"), "/login?") {
		t.Fatalf("unauthenticated response = %d %q",
			unauthenticated.Code, unauthenticated.Header().Get("Location"))
	}

	login := httptest.NewRecorder()
	handler.ServeHTTP(login, loginRequest("Admin", "browser-password", "/audit"))
	if login.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d", login.Code)
	}
	cookie := login.Result().Cookies()[0]

	defaultRequest := httptest.NewRequest(http.MethodGet, "/audit", nil)
	defaultRequest.AddCookie(cookie)
	defaultResponse := httptest.NewRecorder()
	handler.ServeHTTP(defaultResponse, defaultRequest)
	if defaultResponse.Code != http.StatusSeeOther ||
		!strings.HasPrefix(defaultResponse.Header().Get("Location"), "/audit?") {
		t.Fatalf("default response = %d %q",
			defaultResponse.Code, defaultResponse.Header().Get("Location"))
	}

	now := time.Now().UTC().Truncate(time.Second)
	administratorID := int64(1)
	_, err := fixture.browser.audit.Record(context.Background(), audit.Input{
		OccurredAt: now, Category: audit.CategoryRetentionSettings,
		Action: "retention.update", Outcome: audit.OutcomeSucceeded,
		ActorType: audit.ActorAdministrator, AdministratorID: &administratorID,
		Metadata: audit.Metadata{
			audit.MetadataCurrentValue: "<script>alert(1)</script>",
		},
		RequestID: "request-audit-page",
	})
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{
		"from":     {now.Add(-time.Hour).Format(auditTimeLayout)},
		"to":       {now.Add(time.Hour).Format(auditTimeLayout)},
		"category": {string(audit.CategoryRetentionSettings)},
		"action":   {"retention.update"},
		"outcome":  {string(audit.OutcomeSucceeded)},
	}
	request := httptest.NewRequest(http.MethodGet, "/audit?"+values.Encode(), nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK ||
		response.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(body, "retention.update") ||
		!strings.Contains(body, "request-audit-page") ||
		!strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") ||
		strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("audit page = %d %#v %q", response.Code, response.Header(), body)
	}
	for _, forbidden := range []string{
		"password_hash", "session_token", "authorization_header",
		"application_payload",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("audit page exposed forbidden field %q", forbidden)
		}
	}
}

func TestAuditPageRejectsUnboundedMalformedAndUnknownQueries(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	login := httptest.NewRecorder()
	fixture.handler().ServeHTTP(
		login, loginRequest("Admin", "browser-password", "/audit"),
	)
	cookie := login.Result().Cookies()[0]
	now := time.Now().UTC().Truncate(time.Second)
	tests := []string{
		"from=bad&to=bad",
		url.Values{
			"from": {now.Add(-367 * 24 * time.Hour).Format(auditTimeLayout)},
			"to":   {now.Format(auditTimeLayout)},
		}.Encode(),
		url.Values{
			"from": {now.Add(-time.Hour).Format(auditTimeLayout)},
			"to":   {now.Format(auditTimeLayout)}, "category": {"unknown"},
		}.Encode(),
		url.Values{
			"from": {now.Add(-time.Hour).Format(auditTimeLayout)},
			"to":   {now.Format(auditTimeLayout)}, "unexpected": {"value"},
		}.Encode(),
	}
	for _, query := range tests {
		request := httptest.NewRequest(http.MethodGet, "/audit?"+query, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		fixture.handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%q status = %d", query, response.Code)
		}
	}
}

func TestLoginAuditAggregatesRejectedAndKeepsAtomicSuccessfulAttribution(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	handler := web.RequestID(fixture.handler())
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(
		rejected, loginRequest("Admin", "wrong-password", "/audit"),
	)
	if rejected.Code != http.StatusUnauthorized {
		t.Fatalf("rejected sign-in = %d", rejected.Code)
	}
	success := httptest.NewRecorder()
	handler.ServeHTTP(
		success, loginRequest("Admin", "browser-password", "/audit"),
	)
	page, err := fixture.browser.audit.List(context.Background(), audit.Query{
		Category: audit.CategoryAuthentication,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A rejection is only counted in memory; the request writes no row.
	if len(page.Events) != 1 {
		t.Fatalf("sign-in audits before flush = %#v", page.Events)
	}
	if page.Events[0].Action != "sign_in" ||
		page.Events[0].Outcome != audit.OutcomeSucceeded ||
		page.Events[0].ActorType != audit.ActorAdministrator ||
		page.Events[0].AdministratorID == nil ||
		*page.Events[0].AdministratorID != 1 ||
		page.Events[0].RequestID == "" {
		t.Fatalf("successful sign-in audit = %#v", page.Events[0])
	}

	if err := fixture.browser.FlushSignInAudit(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err = fixture.browser.audit.List(context.Background(), audit.Query{
		Category: audit.CategoryAuthentication, Action: signInAuditAction,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("aggregated rejections = %#v", page.Events)
	}
	summary := page.Events[0]
	if summary.Outcome != audit.OutcomeRejected ||
		summary.ActorType != audit.ActorUnauthenticated ||
		summary.AdministratorID != nil || summary.RequestID != "" ||
		summary.Metadata[audit.MetadataInvalidCredentialsCount] != "1" ||
		summary.Metadata[audit.MetadataThrottledCount] != "0" ||
		summary.Metadata[audit.MetadataDistinctClientCount] != "1" ||
		summary.Metadata[audit.MetadataTopClient] != "192.0.2.1/32" ||
		summary.Metadata[audit.MetadataTopClientCount] != "1" {
		t.Fatalf("aggregated rejection = %#v", summary)
	}
}

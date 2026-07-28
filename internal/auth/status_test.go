package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/web"
)

func TestBrowserStatusIsProtectedSanitizedAndShowsTransitions(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	if _, err := fixture.db.Writer().Exec(`INSERT INTO servers(id,name,created_at_us)
		VALUES(1,'status-server',1);
		INSERT INTO sources(
			id,server_id,project_key,environment_key,application_key,service_key,
			project_label,environment_label,application_label,service_label,
			first_seen_at_us,last_seen_at_us
		) VALUES(1,1,'p','e','a','s','p','e','a','s',1,1);
		INSERT INTO log_events(
			id,event_at_us,received_at_us,source_id,stream,level_normalized,
			message_raw,message_text,attributes_json
		) VALUES(1,10,20,1,'stdout','error',
			CAST('private-status-payload' AS BLOB),'private-status-payload',
			'{"token":"private-status-secret"}')`); err != nil {
		t.Fatal(err)
	}

	unauthenticated := httptest.NewRecorder()
	fixture.handler().ServeHTTP(unauthenticated,
		httptest.NewRequest(http.MethodGet, "/status", nil))
	if unauthenticated.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated Status = %d", unauthenticated.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK ||
		response.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(body, "Healthy") ||
		!strings.Contains(body, "Active SQLite footprint") ||
		!strings.Contains(body, "No recent operational diagnostics.") ||
		strings.Contains(body, "private-status-payload") ||
		strings.Contains(body, "private-status-secret") {
		t.Fatalf("healthy Status = %d %#v %q", response.Code, response.Header(), body)
	}

	fixture.operational.RecordIngestRejected(
		ingest.CategoryStorageFull, true, time.Now(),
	)
	degraded := httptest.NewRecorder()
	fixture.handler().ServeHTTP(degraded, request.Clone(context.Background()))
	if degraded.Code != http.StatusOK ||
		!strings.Contains(degraded.Body.String(), "Degraded") ||
		!strings.Contains(degraded.Body.String(), "Ingestion is returning 507") ||
		!strings.Contains(degraded.Body.String(),
			"Ingestion could not commit because storage was unavailable.") {
		t.Fatalf("degraded Status = %d %q", degraded.Code, degraded.Body.String())
	}
	fixture.operational.RecordIngestAccepted(2, time.Now())
	recovered := httptest.NewRecorder()
	fixture.handler().ServeHTTP(recovered, request.Clone(context.Background()))
	if !strings.Contains(recovered.Body.String(), "Healthy") ||
		!strings.Contains(recovered.Body.String(), ">2</dd>") {
		t.Fatalf("recovered Status = %q", recovered.Body.String())
	}

	invalid := httptest.NewRequest(
		http.MethodGet, "/status?secret=private-query-marker", nil,
	)
	invalid.AddCookie(cookie)
	invalidResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest ||
		strings.Contains(invalidResponse.Body.String(), "private-query-marker") {
		t.Fatalf("invalid Status = %d %q",
			invalidResponse.Code, invalidResponse.Body.String())
	}
}

func TestBrowserRunsAuthenticatedCorrelatedSafeDatabaseCheck(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	handler := web.RequestID(fixture.handler())
	form := url.Values{"csrf_token": {CSRFToken(cookie.Value)}}
	request := httptest.NewRequest(
		http.MethodPost, "/status/database-check",
		strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://logs.example.test")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther ||
		response.Header().Get("Location") !=
			"/status?notice=database-check-complete" {
		t.Fatalf("database check response = %d %q %q",
			response.Code, response.Header().Get("Location"), response.Body.String())
	}

	pageRequest := httptest.NewRequest(
		http.MethodGet, "/status?notice=database-check-complete", nil,
	)
	pageRequest.AddCookie(cookie)
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, pageRequest)
	body := page.Body.String()
	if page.Code != http.StatusOK ||
		!strings.Contains(body, "Database check completed.") ||
		!strings.Contains(body, "Healthy · schema 4/4 · SQLite ") ||
		!strings.Contains(body, "· ok · checkpoint completed") ||
		!strings.Contains(body, "database_check_succeeded") ||
		!strings.Contains(body, "The bounded database check completed successfully.") ||
		!strings.Contains(body, "Request ID:") {
		t.Fatalf("checked Status = %d %q", page.Code, body)
	}
	for _, forbidden := range []string{
		"private-status-payload", "private-status-secret",
		"password_hash", "token_hash", "/tmp/", "/home/",
	} {
		if forbidden != "" && strings.Contains(body, forbidden) {
			t.Fatalf("database check page exposed %q", forbidden)
		}
	}

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(
		http.MethodPost, "/status/database-check", nil,
	))
	if unauthenticated.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated database check = %d", unauthenticated.Code)
	}
}

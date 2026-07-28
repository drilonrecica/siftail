package auth

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestEventDetailRequiresAuthenticationAndEscapesCompleteMetadata(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)

	anonymous := httptest.NewRecorder()
	fixture.handler().ServeHTTP(anonymous,
		httptest.NewRequest(http.MethodGet, "/logs/events/1", nil))
	if anonymous.Code != http.StatusSeeOther ||
		!strings.HasPrefix(anonymous.Header().Get("Location"), "/login?return=") {
		t.Fatalf("anonymous detail = %d %#v", anonymous.Code, anonymous.Header())
	}

	request := httptest.NewRequest(http.MethodGet, "/logs/events/1", nil)
	request.AddCookie(loginBrowserCookie(t, fixture))
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK ||
		response.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
		response.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(response.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("detail response = %d %#v", response.Code, response.Header())
	}
	for _, want := range []string{
		`Event details`, `Message`, `Source`, `Timing`, `Level and stream`,
		`Common fields`, `Attributes`, `Raw payload`, `Primary`, `Project`,
		`Production`, `API &lt;prod&gt;`, `container-1`, `request-1`,
		`/v1/&lt;unsafe&gt;`, `12.5 ms`, `&lt;script&gt;alert(1)&lt;/script&gt;`,
		`\u003cscript\u003enested\u003c/script\u003e`, `data-copy-target="event-raw-1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}
	if strings.Contains(body, "<script>nested") ||
		strings.Contains(body, "<script>alert") {
		t.Fatal("detail emitted hostile event content as HTML")
	}
	if strings.Index(body, "&#34;a&#34;") > strings.Index(body, "&#34;z&#34;") ||
		strings.Index(body, "&#34;b&#34;") > strings.Index(body, "&#34;d&#34;") {
		t.Fatal("nested attributes are not recursively ordered")
	}
}

func TestEventDetailBoundsInitialPayloadAndAllowsExplicitFullView(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	largeRaw := strings.Repeat("raw-line\n", 3000) + "RAW-TAIL"
	largeMessage := strings.Repeat("message-line\n", 2000) + "MESSAGE-TAIL"
	largeAttribute := strings.Repeat("attribute-", 2000) + "ATTRIBUTE-TAIL"
	var eventID int64
	err := fixture.coordinator.Do(context.Background(), func(tx *sql.Tx) error {
		result, err := tx.Exec(`INSERT INTO log_events(
			event_at_us,received_at_us,source_id,container_instance_id,
			stream,level_normalized,message_raw,message_text,attributes_json
		) VALUES(?,?,?,?,?,?,?,?,?)`,
			int64(4), int64(5), int64(1), int64(1), "stdout", "info",
			[]byte(largeRaw), largeMessage, `{"large":"`+largeAttribute+`"}`,
		)
		if err != nil {
			return err
		}
		eventID, err = result.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	cookie := loginBrowserCookie(t, fixture)

	detailPath := "/logs/events/" + strconv.FormatInt(eventID, 10)
	initialRequest := httptest.NewRequest(http.MethodGet, detailPath, nil)
	initialRequest.AddCookie(cookie)
	initial := httptest.NewRecorder()
	fixture.handler().ServeHTTP(initial, initialRequest)
	initialBody := initial.Body.String()
	for _, size := range []int{len(largeRaw), len(largeMessage), len(`{"large":"` + largeAttribute + `"}`)} {
		if !strings.Contains(initialBody, strconv.Itoa(size)+" bytes") {
			t.Errorf("initial detail missing stored size %d", size)
		}
	}
	if !strings.Contains(initialBody, "preview truncated") ||
		!strings.Contains(initialBody, "Show complete stored content") ||
		strings.Contains(initialBody, "RAW-TAIL") ||
		strings.Contains(initialBody, "MESSAGE-TAIL") ||
		strings.Contains(initialBody, "ATTRIBUTE-TAIL") {
		t.Fatal("initial detail did not bound every large content section")
	}

	fullRequest := httptest.NewRequest(http.MethodGet, detailPath+"?full=1", nil)
	fullRequest.AddCookie(cookie)
	full := httptest.NewRecorder()
	fixture.handler().ServeHTTP(full, fullRequest)
	fullBody := full.Body.String()
	if full.Code != http.StatusOK ||
		!strings.Contains(fullBody, "RAW-TAIL") ||
		!strings.Contains(fullBody, "MESSAGE-TAIL") ||
		!strings.Contains(fullBody, "ATTRIBUTE-TAIL") ||
		!strings.Contains(fullBody, "Complete stored content is shown.") ||
		strings.Contains(fullBody, "Show complete stored content") {
		t.Fatal("explicit full detail did not return complete schema-bounded content")
	}
}

func TestEventDetailMissingDeletedAndInvalidRequestsAreSafe(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	assertSafeMissing := func(path string, status int) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		fixture.handler().ServeHTTP(response, request)
		body := response.Body.String()
		if response.Code != status ||
			!strings.Contains(body, "Event details could not be loaded.") ||
			strings.Contains(strings.ToLower(body), "sqlite") {
			t.Fatalf("%s = %d %q", path, response.Code, body)
		}
	}
	assertSafeMissing("/logs/events/999", http.StatusNotFound)
	assertSafeMissing("/logs/events/not-an-id", http.StatusNotFound)
	assertSafeMissing("/logs/events/1?full=0", http.StatusBadRequest)
	assertSafeMissing("/logs/events/1?unexpected=1", http.StatusBadRequest)

	if err := fixture.coordinator.Do(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM log_events WHERE id=1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assertSafeMissing("/logs/events/1", http.StatusNotFound)
}

func TestEventDetailDatabaseFailureIsPayloadFree(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	fixture.browser.history = nil
	request := httptest.NewRequest(http.MethodGet, "/logs/events/1", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), "database is temporarily unavailable") ||
		strings.Contains(strings.ToLower(response.Body.String()), "sqlite") {
		t.Fatalf("database failure = %d %q", response.Code, response.Body.String())
	}
}

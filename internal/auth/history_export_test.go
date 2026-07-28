package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/logs"
)

func TestHistoryExportRequiresAuthenticatedConfirmedBrowserPOST(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	cookie := loginBrowserCookie(t, fixture)
	exportURL := "/logs/export?" + baseHistoryValues().Encode()

	get := httptest.NewRecorder()
	fixture.handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, exportURL, nil))
	if get.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET export = %d %q", get.Code, get.Body.String())
	}

	unauthenticated := newExportRequest(exportURL, nil, "text", true)
	unauthenticatedResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusSeeOther ||
		!strings.HasPrefix(unauthenticatedResponse.Header().Get("Location"), "/login?") {
		t.Fatalf("unauthenticated export = %d %#v",
			unauthenticatedResponse.Code, unauthenticatedResponse.Header())
	}

	for name, mutate := range map[string]func(*http.Request){
		"origin": func(request *http.Request) {
			request.Header.Set("Origin", "https://attacker.example")
		},
		"csrf": func(request *http.Request) {
			request.PostForm = nil
			request.Body = io.NopCloser(newExportBody("wrong", "text", true))
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := newExportRequest(
				exportURL, cookie, "text", true,
			)
			mutate(request)
			response := httptest.NewRecorder()
			fixture.handler().ServeHTTP(response, request)
			if response.Code != http.StatusForbidden ||
				response.Header().Get("Content-Disposition") != "" {
				t.Fatalf("%s export = %d %#v", name, response.Code, response.Header())
			}
		})
	}

	rejected := newExportRequest(exportURL, cookie, "text", false)
	rejectedResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(rejectedResponse, rejected)
	if rejectedResponse.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rejectedResponse.Body.String(), "complete matching History range") ||
		rejectedResponse.Header().Get("Content-Disposition") != "" {
		t.Fatalf("unconfirmed export = %d %#v %q",
			rejectedResponse.Code, rejectedResponse.Header(), rejectedResponse.Body.String())
	}
	assertHistoryExportAudit(t, fixture, audit.OutcomeRejected,
		"confirmation_required", "text", 0)
	assertNoHistoryExportStaging(t, fixture)
}

func TestHistoryExportDeliversCompleteFilteredArtifactAfterAudit(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	values := baseHistoryValues()
	values.Set("levels", "error")
	values.Set("contains", "Error")
	values.Set("cursor", "ignored-by-export")
	values.Set("limit", "1")

	request := newExportRequest(
		"/logs/export?"+values.Encode(), cookie, "ndjson", true,
	)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK ||
		response.Header().Get("Content-Type") != "application/x-ndjson" ||
		response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" ||
		response.Header().Get("Content-Length") == "" ||
		response.Header().Get("Content-Disposition") !=
			`attachment; filename="siftail-history-20260728T000000Z-20260728T010000Z.ndjson"` ||
		!strings.Contains(body, `"message":"\u003cscript\u003e`) &&
			!strings.Contains(body, `"message":"<script>`) ||
		strings.Contains(body, "ordinary event") ||
		strings.Contains(body, "health timeout") {
		t.Fatalf("export = %d %#v %q", response.Code, response.Header(), body)
	}
	assertHistoryExportAudit(t, fixture, audit.OutcomeSucceeded,
		"complete", "ndjson", 1)
	assertNoHistoryExportStaging(t, fixture)
}

func TestHistoryExportRejectsLimitsBusyAndInvalidInputWithoutPartialData(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	exportURL := "/logs/export?" + baseHistoryValues().Encode()

	fixture.browser.exports = logs.NewExportStore(fixture.db.Reader(), logs.ExportLimits{
		MaxRows: 1,
	})
	limited := httptest.NewRecorder()
	fixture.handler().ServeHTTP(
		limited, newExportRequest(exportURL, cookie, "text", true),
	)
	if limited.Code != http.StatusUnprocessableEntity ||
		limited.Header().Get("Content-Disposition") != "" ||
		limited.Header().Get("Content-Type") == "text/plain; charset=utf-8" {
		t.Fatalf("limited export = %d %#v %q",
			limited.Code, limited.Header(), limited.Body.String())
	}
	assertHistoryExportAudit(t, fixture, audit.OutcomeRejected,
		"row_limit", "text", 1)
	assertNoHistoryExportStaging(t, fixture)

	fixture.browser.exportSlot <- struct{}{}
	busy := httptest.NewRecorder()
	fixture.handler().ServeHTTP(
		busy, newExportRequest(exportURL, cookie, "text", true),
	)
	<-fixture.browser.exportSlot
	if busy.Code != http.StatusConflict ||
		!strings.Contains(busy.Body.String(), "already in progress") {
		t.Fatalf("busy export = %d %q", busy.Code, busy.Body.String())
	}
	assertHistoryExportAudit(t, fixture, audit.OutcomeRejected, "busy", "text", 0)

	invalid := httptest.NewRecorder()
	fixture.handler().ServeHTTP(
		invalid, newExportRequest(exportURL, cookie, "csv", true),
	)
	if invalid.Code != http.StatusBadRequest ||
		invalid.Header().Get("Content-Disposition") != "" {
		t.Fatalf("invalid export = %d %#v", invalid.Code, invalid.Header())
	}
	assertHistoryExportAudit(t, fixture, audit.OutcomeRejected,
		"invalid_request", "text", 0)
}

func TestHistoryExportCancellationRemovesStagingAndAuditsWithoutPayload(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	values := baseHistoryValues()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	administratorID := int64(1)
	ctx = audit.ContextWithAttribution(ctx, audit.Attribution{
		ActorType: audit.ActorAdministrator, AdministratorID: &administratorID,
		RequestID: "export-canceled",
	})
	request := httptest.NewRequest(
		http.MethodPost, "/logs/export?"+values.Encode(),
		newExportBody("ignored-by-direct-handler", "text", true),
	).WithContext(ctx)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}
	request.PostForm.Set("csrf_token", "ignored-by-direct-handler")
	fixture.browser.historyExport(httptest.NewRecorder(), request)

	assertHistoryExportAudit(t, fixture, audit.OutcomeCanceled,
		"canceled", "text", 0)
	assertNoHistoryExportStaging(t, fixture)
	page, err := fixture.browser.audit.List(context.Background(), audit.Query{
		Category: audit.CategoryExport, Action: "export.history", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := page.Events[0].Metadata[audit.MetadataResultCategory]
	for _, forbidden := range []string{"script", "Error 100", "ordinary event"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("audit metadata contains payload %q: %#v", forbidden, page.Events[0])
		}
	}
}

func TestHistoryExportDeliveryDisconnectIsAuditedAfterMandatorySuccess(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	request := newExportRequest(
		"/logs/export?"+baseHistoryValues().Encode(), cookie, "text", true,
	)
	response := &disconnectResponseWriter{header: make(http.Header)}
	fixture.handler().ServeHTTP(response, request)
	if response.header.Get("Content-Disposition") == "" {
		t.Fatalf("disconnect response headers = %#v", response.header)
	}
	assertHistoryExportAudit(t, fixture, audit.OutcomeCanceled,
		"delivery_interrupted", "text", 3)
	page, err := fixture.browser.audit.List(context.Background(), audit.Query{
		Category: audit.CategoryExport, Action: "export.history", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) < 2 || page.Events[1].Outcome != audit.OutcomeSucceeded ||
		page.Events[1].Metadata[audit.MetadataResultCategory] != "complete" {
		t.Fatalf("mandatory pre-delivery audit missing: %#v", page.Events)
	}
	assertNoHistoryExportStaging(t, fixture)
}

func TestHistoryExportStartupCleanupIsBoundedAndRefusesUnsafeMatches(t *testing.T) {
	directory := t.TempDir()
	for index := range 300 {
		path := filepath.Join(
			directory, historyExportStagingPrefix+strconv.Itoa(index),
		)
		if err := os.WriteFile(path, []byte("synthetic partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{
		".siftail-export-not-numeric",
		".siftail-export-123-extra",
		"operator-file",
	} {
		if err := os.WriteFile(
			filepath.Join(directory, name), []byte("preserve"), 0600,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanupHistoryExportStaging(directory); err != nil {
		t.Fatal(err)
	}
	files, err := historyExportStagingFiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("strict non-staging matches = %#v", files)
	}
	for _, name := range []string{
		".siftail-export-not-numeric",
		".siftail-export-123-extra",
		"operator-file",
	} {
		if contents, err := os.ReadFile(filepath.Join(directory, name)); err != nil ||
			string(contents) != "preserve" {
			t.Fatalf("preserved file %q = %q %v", name, contents, err)
		}
	}

	target := filepath.Join(directory, "operator-file")
	symlink := filepath.Join(directory, historyExportStagingPrefix+"999")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if err := CleanupHistoryExportStaging(directory); err == nil {
		t.Fatal("History export cleanup followed or accepted a symlink")
	}
	if contents, err := os.ReadFile(target); err != nil ||
		string(contents) != "preserve" {
		t.Fatalf("symlink cleanup changed target = %q %v", contents, err)
	}
	if err := os.Remove(symlink); err != nil {
		t.Fatal(err)
	}
	broad := filepath.Join(directory, historyExportStagingPrefix+"1000")
	if err := os.WriteFile(broad, []byte("private"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CleanupHistoryExportStaging(directory); err == nil {
		t.Fatal("History export cleanup accepted broadly readable staging")
	}
	if _, err := os.Lstat(broad); err != nil {
		t.Fatalf("unsafe staging was silently removed: %v", err)
	}
}

func newExportRequest(
	target string,
	cookie *http.Cookie,
	format string,
	confirmed bool,
) *http.Request {
	csrf := "missing"
	if cookie != nil {
		csrf = CSRFToken(cookie.Value)
	}
	request := httptest.NewRequest(
		http.MethodPost, target, newExportBody(csrf, format, confirmed),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://logs.example.test")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	return request
}

func newExportBody(csrf, format string, confirmed bool) *strings.Reader {
	values := url.Values{"csrf_token": {csrf}, "format": {format}}
	confirmation := "not confirmed"
	if confirmed {
		confirmation = historyExportConfirmation
	}
	values.Set("confirmation", confirmation)
	return strings.NewReader(values.Encode())
}

func assertHistoryExportAudit(
	t *testing.T,
	fixture *browserFixture,
	outcome audit.Outcome,
	category, format string,
	rows int,
) {
	t.Helper()
	page, err := fixture.browser.audit.List(context.Background(), audit.Query{
		Category: audit.CategoryExport, Action: "export.history", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) == 0 {
		t.Fatal("History export audit missing")
	}
	event := page.Events[0]
	if event.Outcome != outcome ||
		event.ActorType != audit.ActorAdministrator ||
		event.AdministratorID == nil ||
		event.Metadata[audit.MetadataResultCategory] != category ||
		event.Metadata[audit.MetadataExportFormat] != format {
		t.Fatalf("History export audit = %#v", event)
	}
	if rows > 0 &&
		event.Metadata[audit.MetadataAffectedCount] != strconv.Itoa(rows) {
		t.Fatalf("History export affected count = %#v", event.Metadata)
	}
}

func assertNoHistoryExportStaging(t *testing.T, fixture *browserFixture) {
	t.Helper()
	files, err := historyExportStagingFiles(fixture.browser.exportDataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("partial History export files remain: %#v", files)
	}
}

type disconnectResponseWriter struct {
	header http.Header
	status int
}

func (w *disconnectResponseWriter) Header() http.Header {
	return w.header
}

func (w *disconnectResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *disconnectResponseWriter) Write([]byte) (int, error) {
	return 0, context.Canceled
}

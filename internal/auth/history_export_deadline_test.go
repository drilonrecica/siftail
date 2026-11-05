package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/logs"
)

// A real server whose 30-second-style WriteTimeout has already elapsed by the
// time staging finishes must still deliver the complete artifact: the export
// handler owns its response deadline (audit DAT-07, SEC-12).
func TestHistoryExportDeliveryOutlivesServerWriteTimeout(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	const serverWriteTimeout = 150 * time.Millisecond
	handler := fixture.handler()
	server := httptest.NewUnstartedServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// Simulate staging that outlasts the server-wide write deadline,
			// which is armed when the request is read.
			time.Sleep(3 * serverWriteTimeout)
			handler.ServeHTTP(w, r)
		},
	))
	server.Config.WriteTimeout = serverWriteTimeout
	server.Start()
	t.Cleanup(server.Close)

	request, err := http.NewRequest(http.MethodPost,
		server.URL+"/logs/export?"+baseHistoryValues().Encode(),
		newExportBody(CSRFToken(cookie.Value), "ndjson", true))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://logs.example.test")
	request.AddCookie(cookie)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("export request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("export body truncated after %d bytes: %v", len(body), err)
	}
	length, _ := strconv.Atoi(response.Header.Get("Content-Length"))
	if response.StatusCode != http.StatusOK || length == 0 ||
		len(body) != length || bytes.Count(body, []byte("\n")) != 3 {
		t.Fatalf("export = %d length %d body %d %q",
			response.StatusCode, length, len(body), body)
	}
	// Close waits for the handler, including its deferred staging removal,
	// which may still be running after the client has read every byte.
	server.Close()
	assertHistoryExportAudit(t, fixture, audit.OutcomeSucceeded,
		"complete", "ndjson", 3)
	assertNoHistoryExportStaging(t, fixture)
}

type deadlineRecordingWriter struct {
	header    http.Header
	body      bytes.Buffer
	deadlines []time.Time
	writes    int
	flushed   bool
	// failWrite is the 1-based index of the first failing Write; 0 never fails.
	failWrite int
}

func (w *deadlineRecordingWriter) Header() http.Header { return w.header }
func (w *deadlineRecordingWriter) WriteHeader(int)     {}
func (w *deadlineRecordingWriter) Write(value []byte) (int, error) {
	if w.failWrite > 0 && w.writes+1 >= w.failWrite {
		return 0, errors.New("synthetic stalled client")
	}
	w.writes++
	return w.body.Write(value)
}
func (w *deadlineRecordingWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}
func (w *deadlineRecordingWriter) FlushError() error {
	w.flushed = true
	return nil
}

func TestHistoryExportDeliveryExtendsDeadlinePerChunk(t *testing.T) {
	browser := &Browser{exportWriteTimeout: 7 * time.Second, now: time.Now}
	size := 3*historyExportChunkBytes + historyExportChunkBytes/2
	artifact := bytes.Repeat([]byte("x"), size)
	writer := &deadlineRecordingWriter{header: make(http.Header)}
	started := time.Now()
	if err := browser.deliverHistoryExport(
		writer, http.NewResponseController(writer),
		bytes.NewReader(artifact), int64(size),
	); err != nil {
		t.Fatal(err)
	}
	if writer.body.Len() != size || writer.writes != 4 || !writer.flushed {
		t.Fatalf("delivered %d bytes in %d writes, flushed %v",
			writer.body.Len(), writer.writes, writer.flushed)
	}
	// One deadline before every chunk plus one before the final flush.
	if len(writer.deadlines) != writer.writes+1 {
		t.Fatalf("deadlines = %d for %d writes", len(writer.deadlines), writer.writes)
	}
	for _, deadline := range writer.deadlines {
		if deadline.Before(started.Add(7*time.Second)) ||
			deadline.After(time.Now().Add(7*time.Second)) {
			t.Fatalf("deadline %s is not one chunk timeout from its write", deadline)
		}
	}

	stalled := &deadlineRecordingWriter{header: make(http.Header), failWrite: 3}
	if err := browser.deliverHistoryExport(
		stalled, http.NewResponseController(stalled),
		bytes.NewReader(artifact), int64(size),
	); err == nil {
		t.Fatal("stalled delivery reported success")
	}
	short := &deadlineRecordingWriter{header: make(http.Header)}
	if err := browser.deliverHistoryExport(
		short, http.NewResponseController(short),
		bytes.NewReader(artifact[:10]), int64(size),
	); err == nil {
		t.Fatal("short artifact reported success")
	}
}

func TestHistoryExportStalledDeliveryIsAuditedAsInterrupted(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	request := newExportRequest(
		"/logs/export?"+baseHistoryValues().Encode(), cookie, "text", true,
	)
	writer := &deadlineRecordingWriter{header: make(http.Header), failWrite: 1}
	fixture.handler().ServeHTTP(writer, request)
	if writer.header.Get("Content-Disposition") == "" || len(writer.deadlines) < 3 {
		t.Fatalf("stalled export headers %#v deadlines %d",
			writer.header, len(writer.deadlines))
	}
	assertHistoryExportAudit(t, fixture, audit.OutcomeCanceled,
		"delivery_interrupted", "text", 3)
	assertNoHistoryExportStaging(t, fixture)
}

func TestHistoryExportInterruptedAuditFailureRecordsDiagnostic(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	stopped := database.NewCoordinator(fixture.db.Writer())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- stopped.Run(ctx) }()
	<-stopped.Ready()
	stopped.Close()
	cancel()
	<-done
	fixture.browser.audit = audit.NewStore(fixture.db.Reader(), stopped)

	request := httptest.NewRequest(http.MethodPost, "/logs/export", nil)
	query, err := logs.ParseHistoryQuery(baseHistoryValues(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fixture.browser.recordHistoryExportInterrupted(request, query, "text", 3)
	diagnostics := fixture.operational.Snapshot(time.Now()).Diagnostics
	if len(diagnostics) == 0 ||
		diagnostics[len(diagnostics)-1].Component != "export" ||
		diagnostics[len(diagnostics)-1].Category != "export_delivery_audit_failed" {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestHistoryExportRefusesWithoutStagingSpace(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	var inspected string
	fixture.browser.exportSpace = func(directory string) (uint64, error) {
		inspected = directory
		return logs.DefaultExportBytes + historyExportSpaceReserve - 1, nil
	}
	request := newExportRequest(
		"/logs/export?"+baseHistoryValues().Encode(), cookie, "text", true,
	)
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	if response.Code != http.StatusInsufficientStorage ||
		response.Header().Get("Content-Disposition") != "" ||
		!strings.Contains(response.Body.String(), "enough free space") ||
		inspected != fixture.browser.exportDataDir {
		t.Fatalf("no-space export = %d %#v %q (inspected %q)", response.Code,
			response.Header(), response.Body.String(), inspected)
	}
	assertHistoryExportAudit(t, fixture, audit.OutcomeFailed,
		"insufficient_space", "text", 0)
	assertNoHistoryExportStaging(t, fixture)

	fixture.browser.exportSpace = func(string) (uint64, error) {
		return 0, errors.New("statfs failed")
	}
	failed := httptest.NewRecorder()
	fixture.handler().ServeHTTP(failed, newExportRequest(
		"/logs/export?"+baseHistoryValues().Encode(), cookie, "text", true,
	))
	if failed.Code != http.StatusInsufficientStorage ||
		failed.Header().Get("Content-Disposition") != "" {
		t.Fatalf("uninspectable volume export = %d %#v", failed.Code, failed.Header())
	}

	fixture.browser.exportSpace = func(string) (uint64, error) {
		return logs.DefaultExportBytes + historyExportSpaceReserve, nil
	}
	accepted := httptest.NewRecorder()
	fixture.handler().ServeHTTP(accepted, newExportRequest(
		"/logs/export?"+baseHistoryValues().Encode(), cookie, "text", true,
	))
	if accepted.Code != http.StatusOK {
		t.Fatalf("export with exact headroom = %d %q", accepted.Code, accepted.Body.String())
	}
}

func TestHistoryExportTimeoutRendersShellErrorWithoutFile(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	fixture.browser.exports = logs.NewExportStore(fixture.db.Reader(), logs.ExportLimits{
		Timeout: time.Nanosecond,
	})
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, newExportRequest(
		"/logs/export?"+baseHistoryValues().Encode(), cookie, "text", true,
	))
	if response.Code != http.StatusServiceUnavailable ||
		response.Header().Get("Content-Disposition") != "" ||
		!strings.Contains(response.Body.String(), "longer than its time limit") ||
		!strings.Contains(response.Body.String(), `id="history-region"`) &&
			!strings.Contains(response.Body.String(), "<main") {
		t.Fatalf("timed-out export = %d %#v %q",
			response.Code, response.Header(), response.Body.String())
	}
	assertHistoryExportAudit(t, fixture, audit.OutcomeFailed, "timeout", "text", 0)
	assertNoHistoryExportStaging(t, fixture)
}

func TestHistoryQueryTimeoutRendersShellErrorState(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	seedBrowserHistory(t, fixture)
	cookie := loginBrowserCookie(t, fixture)
	codec, err := logs.LoadCursorCodec(
		context.Background(), fixture.db.Reader(), fixture.coordinator,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.browser.history = logs.NewHistoryStore(fixture.db.Reader(), codec).
		WithQueryTimeout(time.Nanosecond)

	page := httptest.NewRequest(http.MethodGet,
		"/logs?"+baseHistoryValues().Encode(), nil)
	page.AddCookie(cookie)
	pageResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(pageResponse, page)
	if pageResponse.Code != http.StatusServiceUnavailable ||
		!strings.Contains(pageResponse.Body.String(), "took too long") ||
		!strings.Contains(pageResponse.Body.String(), "<main") {
		t.Fatalf("timed-out History page = %d %q",
			pageResponse.Code, pageResponse.Body.String())
	}

	rows := httptest.NewRequest(http.MethodGet,
		"/logs/rows?"+baseHistoryValues().Encode(), nil)
	rows.AddCookie(cookie)
	rows.Header.Set("HX-Request", "true")
	rowsResponse := httptest.NewRecorder()
	fixture.handler().ServeHTTP(rowsResponse, rows)
	if rowsResponse.Code != http.StatusServiceUnavailable ||
		!strings.Contains(rowsResponse.Body.String(), "took too long") ||
		rowsResponse.Header().Get("HX-Retarget") == "" {
		t.Fatalf("timed-out History rows = %d %#v %q", rowsResponse.Code,
			rowsResponse.Header(), rowsResponse.Body.String())
	}
}

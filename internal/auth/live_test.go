package auth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/logs"
	"github.com/drilonrecica/siftail/internal/web"
)

func TestLiveStreamRequiresSessionOriginAcceptAndValidFilters(t *testing.T) {
	fixture, broker := newLiveBrowserFixture(t, logs.LiveBrokerOptions{})
	cookie := loginBrowserCookie(t, fixture)

	tests := []struct {
		name   string
		target string
		cookie bool
		origin string
		accept string
		header string
		status int
	}{
		{
			name: "session", target: "/logs/live/stream", origin: fixture.browser.publicURL,
			accept: "text/event-stream", status: http.StatusSeeOther,
		},
		{
			name: "origin", target: "/logs/live/stream", cookie: true,
			accept: "text/event-stream", status: http.StatusForbidden,
		},
		{
			name: "accept", target: "/logs/live/stream", cookie: true,
			origin: fixture.browser.publicURL, accept: "application/json",
			status: http.StatusNotAcceptable,
		},
		{
			name: "unknown filter", target: "/logs/live/stream?regex=secret", cookie: true,
			origin: fixture.browser.publicURL, accept: "text/event-stream",
			status: http.StatusBadRequest,
		},
		{
			name: "invalid source", target: "/logs/live/stream?source=0", cookie: true,
			origin: fixture.browser.publicURL, accept: "text/event-stream",
			status: http.StatusBadRequest,
		},
		{
			name: "invalid reconnect", target: "/logs/live/stream", cookie: true,
			origin: fixture.browser.publicURL, accept: "text/event-stream",
			header: "payload-marker", status: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			if test.cookie {
				request.AddCookie(cookie)
			}
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			request.Header.Set("Accept", test.accept)
			if test.header != "" {
				request.Header.Set("Last-Event-ID", test.header)
			}
			response := httptest.NewRecorder()
			fixture.handler().ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d, body=%q", response.Code, test.status, response.Body.String())
			}
		})
	}

	broker.Stop()
	request := liveRequest(t, fixture, cookie, "/logs/live/stream")
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stopped broker status = %d", response.Code)
	}
}

func TestLiveStreamFiltersFramesPreviewsReconnectAndControls(t *testing.T) {
	fixture, broker := newLiveBrowserFixture(t, logs.LiveBrokerOptions{})
	fixture.browser.liveHeartbeat = time.Hour
	fixture.browser.liveSessionCheck = time.Hour
	cookie := loginBrowserCookie(t, fixture)
	server := httptest.NewServer(fixture.handler())
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target := server.URL + "/logs/live/stream?source=2&level=error&stream=stderr&contains=NEEDLE"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookie)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Origin", fixture.browser.publicURL)
	request.Header.Set("Last-Event-ID", "41")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		response.Header.Get("Content-Type") != "text/event-stream" ||
		response.Header.Get("Cache-Control") != "no-store" ||
		response.Header.Get("X-Accel-Buffering") != "no" ||
		response.Header.Get("Content-Encoding") != "identity" {
		t.Fatalf("stream response = %d %#v", response.StatusCode, response.Header)
	}
	reader := bufio.NewReader(response.Body)
	if frame := readLiveFrame(t, reader); !strings.Contains(frame, "retry: 3000") {
		t.Fatalf("initial frame = %q", frame)
	}
	if frame := readLiveFrame(t, reader); !strings.Contains(frame, `"type":"possible_gap"`) {
		t.Fatalf("reconnect frame = %q", frame)
	}

	if !broker.TryPublish([]logs.CommittedEvent{
		authLiveEvent(1, 1, logs.LevelInfo, logs.StreamStdout, "ignored"),
		authLiveEvent(2, 2, logs.LevelError, logs.StreamStderr,
			"<script>NEEDLE</script>"+strings.Repeat("x", 10<<10)),
	}) {
		t.Fatal("events were not accepted")
	}
	frame := readLiveFrame(t, reader)
	if !strings.HasPrefix(frame, "event: log\nid: 2\n") || strings.Contains(frame, "\nid: 1\n") {
		t.Fatalf("log frame = %q", frame)
	}
	var payload liveLogPayload
	decodeLiveData(t, frame, &payload)
	if payload.ID != 2 || payload.SourceID != 2 ||
		payload.MessageTruncated != true || len(payload.Message) > 6<<10 ||
		len(payload.Message)+len(payload.AttributesPreview) > livePreviewBytes {
		t.Fatalf("payload = %#v", payload)
	}
	if !strings.Contains(payload.Message, "<script>NEEDLE</script>") {
		t.Fatalf("message preview lost content: %q", payload.Message)
	}
	if strings.Contains(frame, "<script>") {
		t.Fatalf("JSON frame did not escape hostile HTML: %q", frame)
	}

	if !broker.TryPublishControl(logs.LiveControl{
		Type: logs.LiveControlSourcePurged, SourceID: 2,
	}) {
		t.Fatal("source control was not accepted")
	}
	frame = readLiveFrame(t, reader)
	if !strings.Contains(frame, `"type":"source_purged"`) ||
		!strings.Contains(frame, `"source_id":2`) {
		t.Fatalf("source control frame = %q", frame)
	}
	cancel()
}

func TestLiveStreamHeartbeatAndRevokedSessionClosure(t *testing.T) {
	fixture, _ := newLiveBrowserFixture(t, logs.LiveBrokerOptions{})
	fixture.browser.liveHeartbeat = 10 * time.Millisecond
	fixture.browser.liveSessionCheck = 10 * time.Millisecond
	cookie := loginBrowserCookie(t, fixture)
	server := httptest.NewServer(fixture.handler())
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/logs/live/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookie)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Origin", fixture.browser.publicURL)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	_ = readLiveFrame(t, reader)
	if frame := readLiveFrame(t, reader); !strings.Contains(frame, `"type":"heartbeat"`) {
		t.Fatalf("heartbeat frame = %q", frame)
	}
	if err := fixture.sessions.Revoke(context.Background(), cookie.Value); err != nil {
		t.Fatal(err)
	}
	for {
		frame := readLiveFrame(t, reader)
		if strings.Contains(frame, `"type":"session_invalid"`) {
			break
		}
	}
}

func TestLiveStreamBrokerShutdownControl(t *testing.T) {
	fixture, broker := newLiveBrowserFixture(t, logs.LiveBrokerOptions{})
	fixture.browser.liveHeartbeat = time.Hour
	fixture.browser.liveSessionCheck = time.Hour
	cookie := loginBrowserCookie(t, fixture)
	server := httptest.NewServer(fixture.handler())
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/logs/live/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookie)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Origin", fixture.browser.publicURL)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	_ = readLiveFrame(t, reader)
	broker.Stop()
	if frame := readLiveFrame(t, reader); !strings.Contains(frame, `"type":"shutdown"`) {
		t.Fatalf("shutdown frame = %q", frame)
	}
}

func TestLiveStreamDoesNotLeakSessionQueryOrEventToProcessLogs(t *testing.T) {
	fixture, broker := newLiveBrowserFixture(t, logs.LiveBrokerOptions{})
	fixture.browser.liveHeartbeat = time.Hour
	fixture.browser.liveSessionCheck = time.Hour
	cookie := loginBrowserCookie(t, fixture)
	var processLogs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&processLogs, nil))
	loggedHandler := web.RequestID(web.RequestLogger(logger)(fixture.handler()))
	requestDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loggedHandler.ServeHTTP(w, r)
		close(requestDone)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/logs/live/stream?contains=private-marker", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookie)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Origin", fixture.browser.publicURL)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	_ = readLiveFrame(t, reader)
	if !broker.TryPublish([]logs.CommittedEvent{
		authLiveEvent(1, 1, logs.LevelInfo, logs.StreamStdout, "private-marker event payload"),
	}) {
		t.Fatal("event was not accepted")
	}
	_ = readLiveFrame(t, reader)
	cancel()
	_ = response.Body.Close()
	select {
	case <-requestDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stream request did not finish")
	}
	logged := processLogs.String()
	for _, secret := range []string{"private-marker", cookie.Value, "event payload"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("process log leaked %q: %s", secret, logged)
		}
	}
	if !strings.Contains(logged, "GET /logs/live/stream") {
		t.Fatalf("safe route metadata missing: %s", logged)
	}
}

func TestLiveStreamConnectionLimitAndOverflowControl(t *testing.T) {
	fixture, broker := newLiveBrowserFixture(t, logs.LiveBrokerOptions{
		MaxSubscribers:        1,
		SubscriberMaxMessages: 1,
		SubscriberMaxBytes:    1 << 20,
	})
	fixture.browser.liveHeartbeat = time.Hour
	fixture.browser.liveSessionCheck = time.Hour
	cookie := loginBrowserCookie(t, fixture)

	writer := newBlockingLiveWriter()
	request := liveRequest(t, fixture, cookie, "/logs/live/stream")
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		fixture.handler().ServeHTTP(writer, request)
	}()
	select {
	case <-writer.initial:
	case <-time.After(time.Second):
		t.Fatal("stream did not write initial frame")
	}

	second := httptest.NewRecorder()
	fixture.handler().ServeHTTP(second, liveRequest(t, fixture, cookie, "/logs/live/stream"))
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("connection-limit status = %d", second.Code)
	}

	if !broker.TryPublish([]logs.CommittedEvent{
		authLiveEvent(1, 1, logs.LevelInfo, logs.StreamStdout, "one"),
	}) {
		t.Fatal("first event rejected")
	}
	select {
	case <-writer.logStarted:
	case <-time.After(time.Second):
		t.Fatal("handler did not begin blocked log write")
	}
	if !broker.TryPublish([]logs.CommittedEvent{
		authLiveEvent(2, 1, logs.LevelInfo, logs.StreamStdout, strings.Repeat("x", 600<<10)),
	}) {
		t.Fatal("overflow batch rejected by broker ingress")
	}
	close(writer.release)
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("overflow stream did not close")
	}
	if body := writer.String(); !strings.Contains(body, `"type":"truncated"`) {
		t.Fatalf("overflow response = %q", body)
	}
	if writer.deadlines == 0 {
		t.Fatal("stream writes did not set a deadline")
	}
}

func newLiveBrowserFixture(
	t *testing.T,
	options logs.LiveBrokerOptions,
) (*browserFixture, *logs.LiveBroker) {
	t.Helper()
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	broker := logs.NewLiveBroker(options)
	done := make(chan error, 1)
	go func() { done <- broker.Run(context.Background()) }()
	<-broker.Ready()
	fixture.browser.live = broker
	t.Cleanup(func() {
		broker.Stop()
		if err := <-done; err != nil {
			t.Errorf("broker shutdown: %v", err)
		}
	})
	return fixture, broker
}

func liveRequest(
	t *testing.T,
	fixture *browserFixture,
	cookie *http.Cookie,
	target string,
) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.AddCookie(cookie)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Origin", fixture.browser.publicURL)
	return request
}

func authLiveEvent(
	id, sourceID int64,
	level logs.Level,
	stream logs.Stream,
	message string,
) logs.CommittedEvent {
	return logs.CommittedEvent{
		ID: id, SourceID: sourceID, ContainerInstanceID: sourceID + 10,
		Event: logs.CanonicalEvent{
			EventAtUS: id * 10, ReceivedAtUS: id*10 + 1,
			Source: logs.SourceIdentity{
				ServerID: 1, Project: "project", Environment: "production",
				Application: "app", Service: "api", ServiceLabel: "API",
			},
			Container: &logs.ContainerIdentity{ID: "container", Name: "app-1"},
			Level:     level, Stream: stream, OriginalLevel: strings.ToUpper(string(level)),
			MessageRaw: []byte(message), MessageText: message,
			Attributes: []byte(`{"hostile":"</script>","key":"value"}`),
			Common:     logs.CommonFields{Logger: "http", RequestID: "request-1"},
		},
	}
}

func readLiveFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v", err)
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			return strings.Join(lines, "\n")
		}
		lines = append(lines, line)
	}
}

func decodeLiveData(t *testing.T, frame string, target any) {
	t.Helper()
	for _, line := range strings.Split(frame, "\n") {
		if strings.HasPrefix(line, "data: ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), target); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("frame has no data: %q", frame)
}

type blockingLiveWriter struct {
	header      http.Header
	initial     chan struct{}
	logStarted  chan struct{}
	release     chan struct{}
	initialOnce sync.Once
	logOnce     sync.Once
	mu          sync.Mutex
	body        strings.Builder
	status      int
	deadlines   int
}

func newBlockingLiveWriter() *blockingLiveWriter {
	return &blockingLiveWriter{
		header: make(http.Header), initial: make(chan struct{}),
		logStarted: make(chan struct{}), release: make(chan struct{}),
	}
}

func (w *blockingLiveWriter) Header() http.Header { return w.header }

func (w *blockingLiveWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}

func (w *blockingLiveWriter) Write(payload []byte) (int, error) {
	if strings.Contains(string(payload), "event: log") {
		w.logOnce.Do(func() { close(w.logStarted) })
		<-w.release
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_, _ = w.body.Write(payload)
	w.initialOnce.Do(func() { close(w.initial) })
	return len(payload), nil
}

func (w *blockingLiveWriter) Flush() {}

func (w *blockingLiveWriter) SetWriteDeadline(time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadlines++
	return nil
}

func (w *blockingLiveWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

var _ http.Flusher = (*blockingLiveWriter)(nil)

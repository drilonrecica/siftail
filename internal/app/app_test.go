package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/auth"
	"github.com/drilonrecica/siftail/internal/config"
	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/ingest"
	statusstate "github.com/drilonrecica/siftail/internal/status"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	return config.Config{
		DataDir:                     dir,
		DatabasePath:                filepath.Join(dir, "siftail.db"),
		UIAddr:                      freePort(t),
		IngestAddr:                  freePort(t),
		LogLevel:                    "error",
		LogFormat:                   "text",
		ShutdownTimeout:             5 * time.Second,
		MaxCompressedRequestBytes:   5 * 1024 * 1024,
		MaxDecompressedRequestBytes: 25 * 1024 * 1024,
		MaxEventsPerRequest:         10000,
		MaxEventBytes:               1024 * 1024,
		QueueMaxEvents:              20000,
		QueueMaxBytes:               32 * 1024 * 1024,
		IngestResidentMaxEvents:     30000,
		IngestResidentMaxBytes:      64 * 1024 * 1024,
		IngestMaxDecoders:           4,
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	return l.Addr().String()
}

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	logger, err := config.ConfigureLogger(config.Config{LogLevel: "error", LogFormat: "text"})
	if err != nil {
		t.Fatalf("ConfigureLogger: %v", err)
	}
	return logger
}

func TestAppStartsListeners(t *testing.T) {
	cfg := testConfig(t)
	app := New(cfg, testLogger(t))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx)
	}()

	waitForServer(t, "http://"+cfg.UIAddr+"/health/live")
	waitForServer(t, "http://"+cfg.IngestAddr+"/health/live")

	ingestReady, err := http.Get("http://" + cfg.IngestAddr + "/health/ready")
	if err != nil {
		t.Fatalf("ingest readiness request: %v", err)
	}
	ingestReady.Body.Close()
	if ingestReady.StatusCode != http.StatusNotFound {
		t.Fatalf("ingestion router unexpectedly exposes UI readiness: %d", ingestReady.StatusCode)
	}

	resp, err := http.Get("http://" + cfg.UIAddr + "/health/live")
	if err != nil {
		t.Fatalf("ui health request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ui health status = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID header")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("app did not stop after cancellation")
	}

	if _, err := os.Stat(filepath.Join(cfg.DataDir, "siftail-control.sock")); !os.IsNotExist(err) {
		t.Fatal("control socket was not removed on shutdown")
	}
}

func TestAppRefusesDatabaseOwnedByMaintenance(t *testing.T) {
	cfg := testConfig(t)
	lock, err := database.AcquireMaintenanceLock(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	err = New(cfg, testLogger(t)).Run(context.Background())
	if !errors.Is(err, database.ErrMaintenanceActive) {
		t.Fatalf("app maintenance ownership error = %v", err)
	}
	if _, err := os.Stat(cfg.DatabasePath); !os.IsNotExist(err) {
		t.Fatalf("app opened database while maintenance owned it: %v", err)
	}
}

func TestAppPreservesAndRefusesIncompleteRestoreStaging(t *testing.T) {
	cfg := testConfig(t)
	staging := filepath.Join(cfg.DataDir, "restore-staging")
	if err := os.Mkdir(staging, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(staging, "rollback.sqlite")
	if err := os.WriteFile(marker, []byte("recovery"), 0600); err != nil {
		t.Fatal(err)
	}
	err := New(cfg, testLogger(t)).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "manual recovery") {
		t.Fatalf("app restore recovery error = %v", err)
	}
	contents, readErr := os.ReadFile(marker)
	if readErr != nil || string(contents) != "recovery" {
		t.Fatalf("restore recovery state changed: %q, %v", contents, readErr)
	}
}

func TestAppRecoversInterruptedPrivateHistoryExportBeforeStartup(t *testing.T) {
	cfg := testConfig(t)
	staging := filepath.Join(cfg.DataDir, ".siftail-export-123456")
	if err := os.WriteFile(staging, []byte("synthetic partial"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(cfg, testLogger(t)).Run(ctx) }()
	waitForServer(t, "http://"+cfg.UIAddr+"/health/live")
	if _, err := os.Lstat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted History export remains after startup: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	unsafe := testConfig(t)
	target := filepath.Join(unsafe.DataDir, "operator-file")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		target, filepath.Join(unsafe.DataDir, ".siftail-export-999"),
	); err != nil {
		t.Fatal(err)
	}
	err := New(unsafe, testLogger(t)).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unsafe History export") {
		t.Fatalf("unsafe History export recovery = %v", err)
	}
	if _, err := os.Lstat(unsafe.DatabasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe recovery opened the database: %v", err)
	}
	if contents, err := os.ReadFile(target); err != nil ||
		string(contents) != "preserve" {
		t.Fatalf("unsafe recovery changed symlink target = %q %v", contents, err)
	}
}

func TestHealthLivenessAndReadinessTransitionsStayMinimal(t *testing.T) {
	state := statusstate.NewState(time.Now())
	state.SetWriterReady(true)
	application := New(testConfig(t), testLogger(t))
	application.status = state
	application.browser = auth.NewBrowser(nil, nil, auth.BrowserConfig{})
	handler := application.uiMux()

	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		return response
	}
	if live := request("/health/live"); live.Code != http.StatusOK ||
		strings.TrimSpace(live.Body.String()) != "ok" {
		t.Fatalf("healthy liveness = %d %q", live.Code, live.Body.String())
	}
	if ready := request("/health/ready"); ready.Code != http.StatusOK ||
		strings.TrimSpace(ready.Body.String()) != "ok" {
		t.Fatalf("healthy readiness = %d %q", ready.Code, ready.Body.String())
	}
	state.RecordIngestRejected(ingest.CategoryUnavailable, true, time.Now())
	if live := request("/health/live"); live.Code != http.StatusOK {
		t.Fatalf("busy liveness = %d", live.Code)
	}
	if ready := request("/health/ready"); ready.Code != http.StatusServiceUnavailable ||
		strings.TrimSpace(ready.Body.String()) != "not ready" {
		t.Fatalf("degraded readiness = %d %q", ready.Code, ready.Body.String())
	}
	state.RecordIngestAccepted(1, time.Now())
	if ready := request("/health/ready"); ready.Code != http.StatusOK {
		t.Fatalf("recovered readiness = %d", ready.Code)
	}
	application.shuttingDown.Store(true)
	if ready := request("/health/ready"); ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("shutdown readiness = %d", ready.Code)
	}
}

func TestAppControlSocketOwnerOnly(t *testing.T) {
	cfg := testConfig(t)
	app := New(cfg, testLogger(t))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx)
	}()

	waitForServer(t, "http://"+cfg.UIAddr+"/health/live")

	info, err := os.Stat(filepath.Join(cfg.DataDir, "siftail-control.sock"))
	if err != nil {
		t.Fatalf("stat control socket: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0600 {
		t.Fatalf("control socket mode = %04o, want 0600", mode)
	}

	cancel()
	<-errCh
}

func TestAppListenerCollision(t *testing.T) {
	cfg := testConfig(t)
	l, err := net.Listen("tcp", cfg.UIAddr)
	if err != nil {
		t.Fatalf("bind test listener: %v", err)
	}
	defer l.Close()

	app := New(cfg, testLogger(t))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = app.Run(ctx)
	if err == nil {
		t.Fatal("expected error for listener collision")
	}
	if !strings.Contains(err.Error(), "ui listener") {
		t.Fatalf("error does not mention ui listener: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "siftail-control.sock")); !os.IsNotExist(err) {
		t.Fatalf("control socket remains after critical listener failure: %v", err)
	}
}

func TestAppRejectsDataPathThatIsNotDirectory(t *testing.T) {
	cfg := testConfig(t)
	dataPath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dataPath, []byte("operator data"), 0600); err != nil {
		t.Fatalf("write data path: %v", err)
	}
	cfg.DataDir = dataPath
	cfg.DatabasePath = filepath.Join(dataPath, "siftail.db")

	app := New(cfg, testLogger(t))
	err := app.Run(context.Background())
	if err == nil {
		t.Fatal("expected invalid data path to fail")
	}
	if !strings.Contains(err.Error(), "data directory") {
		t.Fatalf("error does not identify data directory: %v", err)
	}
}

func TestAppStaleControlSocketRemoved(t *testing.T) {
	cfg := testConfig(t)
	stale := filepath.Join(cfg.DataDir, "siftail-control.sock")
	addr, err := net.ResolveUnixAddr("unix", stale)
	if err != nil {
		t.Fatalf("resolve stale socket: %v", err)
	}
	staleListener, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("listen stale socket: %v", err)
	}
	staleListener.SetUnlinkOnClose(false)
	if err := staleListener.Close(); err != nil {
		t.Fatalf("close stale socket: %v", err)
	}

	app := New(cfg, testLogger(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx)
	}()

	waitForServer(t, "http://"+cfg.UIAddr+"/health/live")

	info, err := os.Stat(stale)
	if err != nil {
		t.Fatalf("stat control socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatal("control socket path is not a socket after startup")
	}

	cancel()
	<-errCh
}

func TestAppDoesNotRemoveNonSocketAtControlPath(t *testing.T) {
	cfg := testConfig(t)
	path := filepath.Join(cfg.DataDir, "siftail-control.sock")
	const content = "operator data"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write control-path file: %v", err)
	}

	app := New(cfg, testLogger(t))
	if _, err := app.openControlSocket(); err == nil {
		t.Fatal("expected non-socket control path to be rejected")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read preserved file: %v", err)
	}
	if string(got) != content {
		t.Fatalf("control-path file was modified: %q", got)
	}
}

func TestAppDoesNotDetachLiveControlSocket(t *testing.T) {
	cfg := testConfig(t)
	path := filepath.Join(cfg.DataDir, "siftail-control.sock")
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatalf("resolve socket: %v", err)
	}
	live, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("listen socket: %v", err)
	}
	defer live.Close()

	app := New(cfg, testLogger(t))
	if _, err := app.openControlSocket(); err == nil {
		t.Fatal("expected live control socket to be rejected")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("live control socket was removed: %v", err)
	}
}

func TestControlUIDAuthorization(t *testing.T) {
	if !isAuthorizedControlUID(1000, 1000) {
		t.Fatal("owner UID was rejected")
	}
	if isAuthorizedControlUID(1000, 1001) {
		t.Fatal("different peer UID was authorized")
	}
}

func TestAppControlSocketPing(t *testing.T) {
	cfg := testConfig(t)
	app := New(cfg, testLogger(t))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx)
	}()

	waitForServer(t, "http://"+cfg.UIAddr+"/health/live")

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", filepath.Join(cfg.DataDir, "siftail-control.sock"))
			},
		},
	}

	resp, err := client.Get("http://unix/ping")
	if err != nil {
		t.Fatalf("control ping: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("control ping status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "pong" {
		t.Fatalf("control ping body = %q", body)
	}

	request, err := http.NewRequest(http.MethodPost, "http://unix/ping", strings.NewReader("ignored"))
	if err != nil {
		t.Fatalf("create control POST: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("control POST: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("control POST status = %d, want 405", response.StatusCode)
	}

	cancel()
	<-errCh
}

func TestAppDrainsWriterBeforeClosingLiveStreams(t *testing.T) {
	cfg := testConfig(t)
	app := New(cfg, testLogger(t))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()
	waitForServer(t, "http://"+cfg.UIAddr+"/health/live")

	controlClient := &http.Client{Transport: &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", filepath.Join(cfg.DataDir, "siftail-control.sock"))
		},
	}}
	create, err := http.NewRequest(http.MethodPost, "http://unix/administrator",
		strings.NewReader(`{"username":"Admin","password":"live-password"}`))
	if err != nil {
		t.Fatal(err)
	}
	create.Header.Set("Content-Type", "application/json")
	created, err := controlClient.Do(create)
	if err != nil {
		t.Fatal(err)
	}
	created.Body.Close()
	if created.StatusCode != http.StatusOK {
		t.Fatalf("administrator status = %d", created.StatusCode)
	}

	uiClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	form := url.Values{"username": {"Admin"}, "password": {"live-password"}}
	login, err := http.NewRequest(http.MethodPost, "http://"+cfg.UIAddr+"/session",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login.Header.Set("Origin", "http://"+cfg.UIAddr)
	loggedIn, err := uiClient.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	loggedIn.Body.Close()
	cookies := loggedIn.Cookies()
	if loggedIn.StatusCode != http.StatusSeeOther || len(cookies) != 1 {
		t.Fatalf("login = %d %#v", loggedIn.StatusCode, loggedIn.Header)
	}

	streamRequest, err := http.NewRequest(http.MethodGet,
		"http://"+cfg.UIAddr+"/logs/live/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamRequest.AddCookie(cookies[0])
	streamRequest.Header.Set("Accept", "text/event-stream")
	streamRequest.Header.Set("Origin", "http://"+cfg.UIAddr)
	stream, err := uiClient.Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	reader := bufio.NewReader(stream.Body)
	if frame := readAppSSEFrame(t, reader); !strings.Contains(frame, "retry: 3000") {
		t.Fatalf("initial frame = %q", frame)
	}

	cancel()
	if frame := readAppSSEFrame(t, reader); !strings.Contains(frame, `"type":"shutdown"`) {
		t.Fatalf("shutdown frame = %q", frame)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("application did not stop after closing Live stream")
	}
}

func readAppSSEFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v", err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			return strings.Join(lines, "\n")
		}
		lines = append(lines, line)
	}
}

func TestAppShutdownTimeout(t *testing.T) {
	cfg := testConfig(t)
	cfg.ShutdownTimeout = 50 * time.Millisecond
	app := New(cfg, testLogger(t))

	requestStarted := make(chan struct{})
	requestStopped := make(chan struct{})
	srv := app.newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
		close(requestStopped)
	}), "slow")
	addr := freePort(t)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.runHTTPServer(ctx, srv, addr, "slow")
	}()

	for {
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		_, _ = http.Get("http://" + addr)
	}()
	select {
	case <-requestStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("slow request did not start")
	}

	start := time.Now()
	cancel()
	err := <-errCh
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "shutdown exceeded") {
		t.Fatalf("shutdown error = %v, want timeout category", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("shutdown took too long: %v", elapsed)
	}
	select {
	case <-requestStopped:
	case <-time.After(time.Second):
		t.Fatal("forced close did not cancel the active request")
	}
	<-requestDone
}

func TestSafeHTTPErrorWriterDoesNotLogServerMessage(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	writer := safeHTTPErrorWriter{logger: logger, component: "ui"}

	const sensitive = "authorization bearer must-not-leak"
	written, err := writer.Write([]byte(sensitive))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if written != len(sensitive) {
		t.Fatalf("written = %d, want %d", written, len(sensitive))
	}
	if strings.Contains(output.String(), sensitive) {
		t.Fatalf("server message leaked into process log: %s", output.String())
	}
	if !strings.Contains(output.String(), "http_connection") {
		t.Fatalf("safe error category missing: %s", output.String())
	}
}

func waitForServer(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server did not become ready: %s", url)
}

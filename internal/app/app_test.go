package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/config"
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

package app

import (
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
}

func TestAppStaleControlSocketRemoved(t *testing.T) {
	cfg := testConfig(t)
	stale := filepath.Join(cfg.DataDir, "siftail-control.sock")
	if err := os.WriteFile(stale, []byte("stale"), 0644); err != nil {
		t.Fatalf("create stale socket: %v", err)
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

	cancel()
	<-errCh
}

func TestAppShutdownTimeout(t *testing.T) {
	cfg := testConfig(t)
	cfg.ShutdownTimeout = 100 * time.Millisecond

	app := New(cfg, testLogger(t))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx)
	}()

	waitForServer(t, "http://"+cfg.UIAddr+"/health/live")

	// Start a slow request that outlives the shutdown timeout.
	go http.Get("http://" + cfg.UIAddr + "/health/live")

	start := time.Now()
	cancel()
	<-errCh
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("shutdown took too long: %v", elapsed)
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

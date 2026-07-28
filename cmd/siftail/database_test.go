package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/app"
	"github.com/drilonrecica/siftail/internal/config"
	"github.com/drilonrecica/siftail/internal/database"
)

func TestDatabaseCheckCLIOfflineQuickFullAndSafeFailures(t *testing.T) {
	clearSiftailEnv(t)
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "siftail.db")
	t.Setenv("SIFTAIL_DATA_DIR", dataDir)
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"database", "check"},
		{"database", "check", "--full"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%v failed: %q", args, stderr.String())
		}
		output := stdout.String()
		for _, expected := range []string{
			"schema: 4/4", "compatible: true", "integrity: ok",
			"writable: true (filesystem_access)",
			"checkpoint: not_run_read_only",
		} {
			if !strings.Contains(output, expected) {
				t.Errorf("%v output lacks %q: %q", args, expected, output)
			}
		}
		if strings.Contains(output, dataDir) || strings.Contains(output, path) {
			t.Fatalf("%v output exposed path: %q", args, output)
		}
	}

	marker := "private-password-token-payload-marker"
	if err := os.WriteFile(path, []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"database", "check"}, &stdout, &stderr); code == 0 ||
		!strings.Contains(stderr.String(), "database check failed: corrupt") ||
		strings.Contains(stdout.String()+stderr.String(), marker) ||
		strings.Contains(stdout.String()+stderr.String(), path) {
		t.Fatalf("corrupt CLI = code %d, stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
}

func TestDatabaseCheckAndDiagnosticsCLIUseActiveControlSocket(t *testing.T) {
	clearSiftailEnv(t)
	dataDir := t.TempDir()
	cfg := config.Config{
		DataDir: dataDir, DatabasePath: filepath.Join(dataDir, "siftail.db"),
		UIAddr: freeTCPAddr(t), IngestAddr: freeTCPAddr(t),
		LogLevel: "error", LogFormat: "text", ShutdownTimeout: time.Second,
		MaxCompressedRequestBytes:   5 << 20,
		MaxDecompressedRequestBytes: 25 << 20,
		MaxEventsPerRequest:         10000, MaxEventBytes: 1 << 20,
		QueueMaxEvents: 20000, QueueMaxBytes: 32 << 20,
		IngestResidentMaxEvents: 30000, IngestResidentMaxBytes: 64 << 20,
		IngestMaxDecoders: 4,
	}
	t.Setenv("SIFTAIL_DATA_DIR", dataDir)
	t.Setenv("SIFTAIL_UI_ADDR", cfg.UIAddr)
	t.Setenv("SIFTAIL_INGEST_ADDR", cfg.IngestAddr)
	t.Setenv("SIFTAIL_LOG_LEVEL", "error")
	logger, err := config.ConfigureLogger(cfg)
	if err != nil {
		t.Fatal(err)
	}
	application := app.New(cfg, logger)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	waitForHTTP(t, "http://"+cfg.UIAddr+"/health/live", nil, &bytes.Buffer{})
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"database", "check"}, &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "checkpoint: completed") ||
		!strings.Contains(stdout.String(), "writable: true (operational_state)") {
		t.Fatalf("active check = code %d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(
		[]string{"database", "check", "--full"}, &stdout, &stderr,
	); code == 0 || !strings.Contains(
		stderr.String(), "full database check requires",
	) {
		t.Fatalf("active full check = code %d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"diagnostics"}, &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "database_check_succeeded") ||
		!strings.Contains(stdout.String(),
			"The bounded database check completed successfully.") ||
		!strings.Contains(stdout.String(), "request_id=") ||
		strings.Contains(stdout.String(), cfg.DatabasePath) {
		t.Fatalf("active diagnostics = code %d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
}

func TestDiagnosticsCLIRequiresActiveServerAndRejectsExtraArguments(t *testing.T) {
	clearSiftailEnv(t)
	t.Setenv("SIFTAIL_DATA_DIR", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"diagnostics"}, &stdout, &stderr); code == 0 ||
		!strings.Contains(stderr.String(), "server to be active") {
		t.Fatalf("offline diagnostics = code %d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(
		[]string{"diagnostics", "--all"}, &stdout, &stderr,
	); code == 0 || !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("unbounded diagnostics = code %d stderr=%q", code, stderr.String())
	}
}

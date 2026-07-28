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
	"github.com/drilonrecica/siftail/internal/backup"
	"github.com/drilonrecica/siftail/internal/config"
)

func TestBackupCLIUsesActiveControlSocketAndReportsVerifiedArtifact(t *testing.T) {
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

	output := filepath.Join(t.TempDir(), "cli-full.sqlite")
	var stdout, stderr bytes.Buffer
	if code := run(
		[]string{"backup", "--output", output}, &stdout, &stderr,
	); code != 0 {
		t.Fatalf("backup = code %d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
	for _, expected := range []string{
		"backup verified", "type: full", "artifact: cli-full.sqlite",
		"bytes:", "sha256:",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("backup output lacks %q: %q", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String()+stderr.String(), filepath.Dir(output)) {
		t.Fatalf("backup CLI exposed server path: %q %q",
			stdout.String(), stderr.String())
	}
	if _, err := backup.Verify(context.Background(), output); err != nil {
		t.Fatalf("CLI artifact: %v", err)
	}

	configurationOutput := filepath.Join(t.TempDir(), "cli-configuration.sqlite")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{
		"backup", "--configuration-only", "--output", configurationOutput,
	}, &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "type: configuration") ||
		!strings.Contains(stdout.String(),
			"artifact: cli-configuration.sqlite") ||
		strings.Contains(stdout.String()+stderr.String(),
			filepath.Dir(configurationOutput)) {
		t.Fatalf("configuration backup = code %d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(
		[]string{"backup", "verify", configurationOutput}, &stdout, &stderr,
	); code != 0 ||
		!strings.Contains(stdout.String(), "type: configuration") ||
		strings.Contains(stdout.String()+stderr.String(),
			filepath.Dir(configurationOutput)) {
		t.Fatalf("active verify = code %d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"diagnostics"}, &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "backup_succeeded") ||
		!strings.Contains(stdout.String(),
			"A backup completed and passed verification.") ||
		strings.Contains(stdout.String(), output) {
		t.Fatalf("backup diagnostics = code %d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
}

func TestBackupCLIRequiresActiveServerAndOutput(t *testing.T) {
	clearSiftailEnv(t)
	t.Setenv("SIFTAIL_DATA_DIR", t.TempDir())
	for _, args := range [][]string{
		{"backup"},
		{"backup", "--output", filepath.Join(t.TempDir(), "offline.sqlite")},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Fatalf("%v succeeded: %q", args, stdout.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), "private") {
			t.Fatalf("%v exposed unexpected content: %q %q",
				args, stdout.String(), stderr.String())
		}
	}

	corrupt := filepath.Join(t.TempDir(), "private-corrupt.sqlite")
	if err := os.WriteFile(
		corrupt, []byte("private-backup-payload"), 0600,
	); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(
		[]string{"backup", "verify", corrupt}, &stdout, &stderr,
	); code == 0 ||
		!strings.Contains(stderr.String(), "backup verification failed") ||
		strings.Contains(stdout.String()+stderr.String(), corrupt) ||
		strings.Contains(stdout.String()+stderr.String(),
			"private-backup-payload") {
		t.Fatalf("offline corrupt verify = code %d stdout=%q stderr=%q",
			code, stdout.String(), stderr.String())
	}
}

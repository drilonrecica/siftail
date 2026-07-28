package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/app"
	"github.com/drilonrecica/siftail/internal/config"
	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/logs"
)

func TestAdministrativeCLIOffline(t *testing.T) {
	clearSiftailEnv(t)
	dataDir := t.TempDir()
	t.Setenv("SIFTAIL_DATA_DIR", dataDir)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"server", "create", "--name", "Production", "--hostname", "prod.example"}, &stdout, &stderr); code != 0 {
		t.Fatalf("create server: %s", stderr.String())
	}
	stdout.Reset()
	if code := run([]string{"server", "list"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "Production") {
		t.Fatalf("list servers: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := run([]string{"token", "create", "--server", "1", "--name", "primary"}, &stdout, &stderr); code != 0 {
		t.Fatalf("create token: %s", stderr.String())
	}
	output := stdout.String()
	if strings.Count(output, "sft_") != 1 {
		t.Fatalf("plaintext token must appear exactly once: %q", output)
	}
	stdout.Reset()
	if code := run([]string{"token", "revoke", "--id", "1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("revoke token: %s", stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "sft_") {
		t.Fatal("later command leaked plaintext token")
	}
}

func TestAdministrativeCLIUsesLiveControlSocket(t *testing.T) {
	clearSiftailEnv(t)
	dataDir := t.TempDir()
	cfg := config.Config{
		DataDir: dataDir, DatabasePath: filepath.Join(dataDir, "siftail.db"),
		UIAddr: freeTCPAddr(t), IngestAddr: freeTCPAddr(t),
		LogLevel: "error", LogFormat: "text", ShutdownTimeout: time.Second,
		MaxCompressedRequestBytes: 5 << 20, MaxDecompressedRequestBytes: 25 << 20,
		MaxEventsPerRequest: 10000, MaxEventBytes: 1 << 20,
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
	errCh := make(chan error, 1)
	go func() { errCh <- application.Run(ctx) }()
	waitForHTTP(t, "http://"+cfg.UIAddr+"/health/live", nil, &bytes.Buffer{})

	var stdout, stderr bytes.Buffer
	if code := run([]string{"server", "create", "--name", "Online"}, &stdout, &stderr); code != 0 {
		t.Fatalf("online server create: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Online") {
		t.Fatalf("online output = %q", stdout.String())
	}
	cancel()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	for _, field := range []string{"siftail version ", "commit: ", "build date: ", "go version: "} {
		if !strings.Contains(stdout.String(), field) {
			t.Errorf("version output missing %q: %q", field, stdout.String())
		}
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"definitely-not-a-command"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("unknown command returned success")
	}
	if !strings.Contains(stderr.String(), "unknown command") ||
		!strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("unsafe or incomplete error output: %q", stderr.String())
	}
}

func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit code = %d", code)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("help output = %q", stdout.String())
	}
}

func TestRunConfigValidateDoesNotOpenDatabase(t *testing.T) {
	clearSiftailEnv(t)
	dataDir := t.TempDir()
	t.Setenv("SIFTAIL_DATA_DIR", dataDir)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"config", "validate"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "siftail.db")); !os.IsNotExist(err) {
		t.Fatalf("config validation opened or created the database: %v", err)
	}
}

func TestServeHandlesSIGTERMAndRemovesControlSocket(t *testing.T) {
	dataDir := t.TempDir()
	uiAddr := freeTCPAddr(t)
	ingestAddr := freeTCPAddr(t)

	cmd := exec.Command(os.Args[0], "-test.run=TestServeProcessHelper")
	cmd.Env = withoutSiftailEnv(os.Environ())
	cmd.Env = append(cmd.Env,
		"GO_WANT_SIFTAIL_SERVE_HELPER=1",
		"SIFTAIL_DATA_DIR="+dataDir,
		"SIFTAIL_UI_ADDR="+uiAddr,
		"SIFTAIL_INGEST_ADDR="+ingestAddr,
		"SIFTAIL_LOG_LEVEL=error",
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve helper: %v", err)
	}

	waitForHTTP(t, "http://"+uiAddr+"/health/live", cmd, &output)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("serve helper failed: %v\n%s", err, output.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "siftail-control.sock")); !os.IsNotExist(err) {
		t.Fatalf("control socket remains after SIGTERM: %v", err)
	}
}

func TestDurableIngestionSubprocessSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess smoke test")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	binary := filepath.Join(work, "siftail-smoke")
	build := exec.Command("go", "build", "-o", binary, "./cmd/siftail")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build smoke binary: %v\n%s", err, output)
	}

	dataDir := filepath.Join(work, "data")
	if err := os.Mkdir(dataDir, 0750); err != nil {
		t.Fatal(err)
	}
	uiAddr := freeTCPAddr(t)
	ingestAddr := freeTCPAddr(t)
	environment := append(withoutSiftailEnv(os.Environ()),
		"SIFTAIL_DATA_DIR="+dataDir,
		"SIFTAIL_UI_ADDR="+uiAddr,
		"SIFTAIL_INGEST_ADDR="+ingestAddr,
		"SIFTAIL_LOG_LEVEL=error",
	)
	serve := exec.Command(binary, "serve")
	serve.Env = environment
	var processOutput bytes.Buffer
	serve.Stdout = &processOutput
	serve.Stderr = &processOutput
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if waited {
			return
		}
		_ = serve.Process.Kill()
		_ = serve.Wait()
	}()
	waitForHTTP(t, "http://"+uiAddr+"/health/live", serve, &processOutput)

	const administratorPassword = "subprocess-admin-password"
	adminCommand := exec.Command(binary, "admin", "create", "--username", "SmokeAdmin")
	adminCommand.Env = environment
	adminCommand.Stdin = strings.NewReader(administratorPassword + "\n" + administratorPassword + "\n")
	adminOutput, err := adminCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("create smoke administrator through CLI: %v", err)
	}
	if bytes.Contains(adminOutput, []byte(administratorPassword)) {
		t.Fatal("subprocess administrator output leaked password")
	}
	revokeCommand := exec.Command(binary, "sessions", "revoke-all")
	revokeCommand.Env = environment
	revokeOutput, err := revokeCommand.CombinedOutput()
	if err != nil || !bytes.Contains(revokeOutput, []byte("revoked 0")) {
		t.Fatalf("revoke sessions through live CLI: %v", err)
	}

	var serverOutput []byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		command := exec.Command(binary, "server", "create", "--name", "Smoke")
		command.Env = environment
		serverOutput, err = command.CombinedOutput()
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || !strings.Contains(string(serverOutput), "server 1 created") {
		t.Fatalf("create smoke Server through CLI: %v", err)
	}

	tokenCommand := exec.Command(binary, "token", "create", "--server", "1", "--name", "smoke")
	tokenCommand.Env = environment
	tokenOutput, err := tokenCommand.Output()
	if err != nil {
		t.Fatalf("create smoke token through CLI: %v", err)
	}
	token := oneTimeToken(tokenOutput)
	if token == "" {
		t.Fatal("token CLI did not return one one-time token")
	}

	plain, err := os.ReadFile(filepath.Join(root, "cmd/siftail/testdata/ingest/canonical.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	postSmokeFixture(t, ingestAddr, token, "application/x-ndjson", "", plain)

	fluent, err := os.ReadFile(filepath.Join(root, "cmd/siftail/testdata/ingest/fluent-bit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	if _, err := gzipWriter.Write(fluent); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	postSmokeFixture(t, ingestAddr, token, "application/json", "gzip", compressed.Bytes())

	if err := serve.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := serve.Wait(); err != nil {
		t.Fatalf("smoke server shutdown: %v\n%s", err, processOutput.String())
	}
	waited = true
	if _, err := os.Stat(filepath.Join(dataDir, "siftail-control.sock")); !os.IsNotExist(err) {
		t.Fatalf("smoke control socket remains: %v", err)
	}

	db, err := database.Open(context.Background(), filepath.Join(dataDir, "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := logs.NewStore(db.Reader()).Recent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("committed smoke events = %d, want 2", len(events))
	}
	messages := map[string]bool{}
	for _, event := range events {
		messages[event.MessageText] = true
		if string(event.MessageRaw) != event.MessageText {
			t.Fatalf("raw payload was not preserved for %q", event.MessageText)
		}
	}
	if !messages["plain smoke event"] || !messages["gzip smoke event"] {
		t.Fatalf("smoke messages = %#v", messages)
	}
}

func oneTimeToken(output []byte) string {
	for _, line := range strings.Split(string(output), "\n") {
		const prefix = "token (shown once): "
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

func postSmokeFixture(t *testing.T, addr, token, mediaType, encoding string, body []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://"+addr+"/api/v1/ingest", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", mediaType)
	if encoding != "" {
		request.Header.Set("Content-Encoding", encoding)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("post smoke fixture: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("smoke ingestion status = %d", response.StatusCode)
	}
}

func TestServeProcessHelper(t *testing.T) {
	if os.Getenv("GO_WANT_SIFTAIL_SERVE_HELPER") != "1" {
		return
	}
	if err := runServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate address: %v", err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func waitForHTTP(t *testing.T, url string, cmd *exec.Cmd, output *bytes.Buffer) {
	t.Helper()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			response.Body.Close()
			return
		}
		if cmd != nil && cmd.ProcessState != nil {
			t.Fatalf("serve helper exited early\n%s", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cmd != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	t.Fatalf("serve helper did not become ready\n%s", output.String())
}

func withoutSiftailEnv(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, "SIFTAIL_") &&
			!strings.HasPrefix(entry, "GO_WANT_SIFTAIL_SERVE_HELPER=") {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func clearSiftailEnv(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(name, "SIFTAIL_") {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatalf("unset %s: %v", name, err)
			}
		}
	}
}

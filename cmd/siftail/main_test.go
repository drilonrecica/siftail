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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/drilonrecica/siftail/internal/app"
	"github.com/drilonrecica/siftail/internal/config"
	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/ingest"
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

	t.Setenv("SIFTAIL_INGEST_PUBLIC_URL", "https://ingest.example.test:8443/api/v1/ingest")
	for _, outputKind := range []string{"coolify", "generic", "curl"} {
		stdout.Reset()
		stderr.Reset()
		if code := run([]string{
			"token", "create", "--server", "1", "--name", "generated-" + outputKind,
			"--output", outputKind,
		}, &stdout, &stderr); code != 0 {
			t.Fatalf("%s output: %s", outputKind, stderr.String())
		}
		output := stdout.String()
		if strings.Count(output, "sft_") != 1 ||
			strings.Contains(output, ingest.GuideTokenPlaceholder) ||
			!strings.Contains(output, "ingest.example.test") {
			t.Fatalf("%s generated output = %q", outputKind, output)
		}
		if outputKind == "coolify" &&
			(!strings.Contains(output, "Exclude COOLIFY_APP_NAME ^siftail-self$") ||
				!strings.Contains(output, "tls                      On")) {
			t.Fatalf("unsafe Coolify output = %q", output)
		}
	}
	t.Setenv("SIFTAIL_INGEST_PUBLIC_URL", "")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{
		"token", "create", "--server", "1", "--name", "not-created",
		"--output", "coolify",
	}, &stdout, &stderr); code == 0 ||
		!strings.Contains(stderr.String(), "SIFTAIL_INGEST_PUBLIC_URL") {
		t.Fatalf("missing endpoint output = code %d, %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{
		"token", "create", "--server", "1", "--name", "not-created",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("failed generated output created an unseen token: %s", stderr.String())
	}
}

func TestAdministrativeCLIUsesLiveControlSocket(t *testing.T) {
	clearSiftailEnv(t)
	dataDir := t.TempDir()
	cfg := config.Config{
		DataDir: dataDir, DatabasePath: filepath.Join(dataDir, "siftail.db"),
		BackupDir: filepath.Join(dataDir, "backups"),
		UIAddr:    freeTCPAddr(t), IngestAddr: freeTCPAddr(t),
		PublicURL: "http://127.0.0.1",
		LogLevel:  "error", LogFormat: "text", ShutdownTimeout: time.Second,
		MaxCompressedRequestBytes: 5 << 20, MaxDecompressedRequestBytes: 25 << 20,
		MaxEventsPerRequest: 10000, MaxEventBytes: 1 << 20,
		QueueMaxEvents: 20000, QueueMaxBytes: 32 << 20,
		IngestResidentMaxEvents: 30000, IngestResidentMaxBytes: 64 << 20,
		IngestMaxDecoders:     4,
		IngestGroupMaxBatches: 64, IngestGroupMaxBytes: 8 << 20,
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
	errCh, appExited := startApplication(ctx, application)
	waitApplicationReady(t, cancel, cfg.UIAddr, cfg.IngestAddr, dataDir, appExited)

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
	t.Setenv("SIFTAIL_PUBLIC_URL", "https://logs.example.test")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"config", "validate"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "siftail.db")); !os.IsNotExist(err) {
		t.Fatalf("config validation opened or created the database: %v", err)
	}
}

// ADR 0010: serve refuses to start without SIFTAIL_PUBLIC_URL and names it;
// config validate reports the same error; commands that serve no browser
// traffic keep working without it.
func TestServeAndConfigValidateRequirePublicURL(t *testing.T) {
	clearSiftailEnv(t)
	dataDir := t.TempDir()
	t.Setenv("SIFTAIL_DATA_DIR", dataDir)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"config", "validate"}, &stdout, &stderr); code == 0 ||
		!strings.Contains(stderr.String(), "SIFTAIL_PUBLIC_URL is required") {
		t.Fatalf("config validate = %d %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"serve"}, &stdout, &stderr); code == 0 ||
		!strings.Contains(stderr.String(), "configuration invalid: SIFTAIL_PUBLIC_URL is required") {
		t.Fatalf("serve = %d %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "siftail.db")); !os.IsNotExist(err) {
		t.Fatalf("refused serve touched the database: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"server", "list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("offline command without public URL = %d %q", code, stderr.String())
	}

	t.Setenv("SIFTAIL_PUBLIC_URL", "https://logs.example.test/siftail")
	stderr.Reset()
	if code := run([]string{"config", "validate"}, &stdout, &stderr); code == 0 ||
		!strings.Contains(stderr.String(), "SIFTAIL_PUBLIC_URL must be an origin without a path") {
		t.Fatalf("config validate with sub-path = %d %q", code, stderr.String())
	}
}

func TestServeHandlesSIGTERMAndRemovesControlSocket(t *testing.T) {
	dataDir := t.TempDir()
	proc, _, _ := launchServe(t, dataDir, func(uiAddr, ingestAddr string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestServeProcessHelper")
		cmd.Env = append(withoutSiftailEnv(os.Environ()),
			"GO_WANT_SIFTAIL_SERVE_HELPER=1",
			"SIFTAIL_DATA_DIR="+dataDir,
			"SIFTAIL_UI_ADDR="+uiAddr,
			"SIFTAIL_INGEST_ADDR="+ingestAddr,
			"SIFTAIL_PUBLIC_URL=http://"+uiAddr,
			"SIFTAIL_LOG_LEVEL=error",
		)
		return cmd
	})
	if err := proc.terminate(); err != nil {
		t.Fatalf("serve helper failed: %v\n%s", err, proc.output.String())
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
	var environment []string
	serve, uiAddr, ingestAddr := launchServe(t, dataDir, func(uiAddr, ingestAddr string) *exec.Cmd {
		environment = append(withoutSiftailEnv(os.Environ()),
			"SIFTAIL_DATA_DIR="+dataDir,
			"SIFTAIL_UI_ADDR="+uiAddr,
			"SIFTAIL_INGEST_ADDR="+ingestAddr,
			"SIFTAIL_PUBLIC_URL=http://"+uiAddr,
			"SIFTAIL_INGEST_PUBLIC_URL=http://"+ingestAddr+"/api/v1/ingest",
			"SIFTAIL_LOG_LEVEL=error",
		)
		command := exec.Command(binary, "serve")
		command.Env = environment
		return command
	})

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
	generatedCommand := exec.Command(
		binary, "token", "create", "--server", "1", "--name", "generated",
		"--output", "coolify",
	)
	generatedCommand.Env = environment
	generatedOutput, err := generatedCommand.Output()
	if err != nil ||
		bytes.Count(generatedOutput, []byte("sft_")) != 1 ||
		bytes.Contains(generatedOutput, []byte(ingest.GuideTokenPlaceholder)) ||
		!bytes.Contains(generatedOutput, []byte("Exclude COOLIFY_APP_NAME ^siftail-self$")) ||
		!bytes.Contains(generatedOutput, []byte("tls                      Off")) {
		t.Fatalf("generated subprocess output: %v %q", err, generatedOutput)
	}

	// The fixture files carry fixed dates. Shift them so the newest event is
	// one hour old, which keeps every event inside the default retention
	// window on any date.
	shift := fixtureShift(time.Now())
	plain, err := os.ReadFile(filepath.Join(root, "cmd/siftail/testdata/ingest/canonical.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	plain = shiftFixtureTimestamps(t, plain, shift)
	postSmokeFixture(t, ingestAddr, token, "application/x-ndjson", "", plain)

	fluent, err := os.ReadFile(filepath.Join(root, "cmd/siftail/testdata/ingest/fluent-bit.json"))
	if err != nil {
		t.Fatal(err)
	}
	fluent = shiftFixtureTimestamps(t, fluent, shift)
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	if _, err := gzipWriter.Write(fluent); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	postSmokeFixture(t, ingestAddr, token, "application/json", "gzip", compressed.Bytes())

	coolify, err := os.ReadFile(filepath.Join(
		root, "internal/ingest/testdata/coolify-v4.1.1-json-lines.ndjson",
	))
	if err != nil {
		t.Fatal(err)
	}
	coolify = shiftFixtureTimestamps(t, coolify, shift)
	compressed.Reset()
	gzipWriter = gzip.NewWriter(&compressed)
	if _, err := gzipWriter.Write(coolify); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	postSmokeFixture(
		t, ingestAddr, token, "application/x-ndjson", "gzip", compressed.Bytes(),
	)

	generic, err := os.ReadFile(filepath.Join(
		root, "internal/ingest/testdata/fluent-bit-v5.0.9-json-array.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	generic = shiftFixtureTimestamps(t, generic, shift)
	postSmokeFixture(t, ingestAddr, token, "application/json", "", generic)
	postSmokeFixture(t, ingestAddr, token, "application/json", "", generic)

	if err := serve.terminate(); err != nil {
		t.Fatalf("smoke server shutdown: %v\n%s", err, serve.output.String())
	}
	restartCommand := exec.Command(binary, "serve")
	restartCommand.Env = environment
	serve = startProcess(t, restartCommand)
	if err := serve.waitReady(readyTargets{
		ui: uiAddr, ingest: ingestAddr, socket: filepath.Join(dataDir, "siftail-control.sock"),
	}); err != nil {
		t.Fatalf("restarted smoke server: %v\n%s", err, serve.output.String())
	}
	postSmokeFixture(t, ingestAddr, token, "application/json", "", generic)
	postSmokeFixture(t, ingestAddr, token, "application/x-ndjson", "", coolify)
	if err := serve.terminate(); err != nil {
		t.Fatalf("restarted smoke server shutdown: %v\n%s", err, serve.output.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "siftail-control.sock")); !os.IsNotExist(err) {
		t.Fatalf("smoke control socket remains: %v", err)
	}

	db, err := database.Open(context.Background(), filepath.Join(dataDir, "siftail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	events, err := logs.NewStore(db.Reader()).Recent(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 8 {
		t.Fatalf("committed smoke events = %d, want 8", len(events))
	}
	messages := map[string]int{}
	for _, event := range events {
		messages[event.MessageText]++
		if string(event.MessageRaw) != event.MessageText {
			if event.MessageText != "structured generic record" {
				t.Fatalf("raw payload was not preserved for %q", event.MessageText)
			}
		}
	}
	if messages["plain smoke event"] != 1 ||
		messages["gzip smoke event"] != 1 ||
		messages["checkout request complete"] != 2 ||
		messages["payment failed\n  at charge (payments.go:42)"] != 2 ||
		messages["generic forward input"] != 1 ||
		messages["structured generic record"] != 1 {
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

// reservedAddrs makes freeTCPAddr hand out each address at most once per test
// binary, so two calls can never return the same port even after the probe
// listener is closed.
var (
	reservedAddrsMu sync.Mutex
	reservedAddrs   = map[string]bool{}
)

// freeTCPAddr returns a loopback address that was free a moment ago. A port
// cannot be reserved across a process boundary without handing over the
// listener, so a foreign process may still take it before the server binds;
// launchServe retries with a fresh address when the server reports that.
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("allocate address: %v", err)
		}
		addr := listener.Addr().String()
		_ = listener.Close()
		reservedAddrsMu.Lock()
		taken := reservedAddrs[addr]
		reservedAddrs[addr] = true
		reservedAddrsMu.Unlock()
		if !taken {
			return addr
		}
	}
	t.Fatal("could not allocate an unused address")
	return ""
}

// syncBuffer is an output sink that is safe to read while the child process
// is still writing to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// serveProcess is a running child process. A goroutine owns cmd.Wait, so exit
// is observable through done without polling cmd.ProcessState.
type serveProcess struct {
	cmd     *exec.Cmd
	output  *syncBuffer
	done    chan struct{}
	waitErr error // valid after done is closed
}

func startProcess(t *testing.T, cmd *exec.Cmd) *serveProcess {
	t.Helper()
	p := &serveProcess{cmd: cmd, output: &syncBuffer{}, done: make(chan struct{})}
	cmd.Stdout = p.output
	cmd.Stderr = p.output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process: %v", err)
	}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

// terminate sends SIGTERM and waits for a clean exit.
func (p *serveProcess) terminate() error {
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	select {
	case <-p.done:
		return p.waitErr
	case <-time.After(15 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		return fmt.Errorf("process ignored SIGTERM for 15s")
	}
}

// startApplication runs an in-process application. The result is delivered on
// the buffered done channel; exited is closed after it was sent.
func startApplication(ctx context.Context, application *app.App) (chan error, <-chan struct{}) {
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		done <- application.Run(ctx)
		close(exited)
	}()
	return done, exited
}

// waitApplicationReady waits for both listeners and the control socket of an
// in-process application, cancelling it when readiness fails.
func waitApplicationReady(
	t *testing.T, cancel context.CancelFunc,
	uiAddr, ingestAddr, dataDir string, exited <-chan struct{},
) {
	t.Helper()
	err := waitReady(readyTargets{
		ui: uiAddr, ingest: ingestAddr,
		socket: filepath.Join(dataDir, "siftail-control.sock"),
	}, exited)
	if err != nil {
		cancel()
		t.Fatalf("application did not become ready: %v", err)
	}
}

type readyTargets struct {
	ui, ingest, socket string
}

func (p *serveProcess) waitReady(targets readyTargets) error {
	return waitReady(targets, p.done)
}

// waitReady returns once the UI listener answers HTTP, the ingestion listener
// accepts connections and the control socket accepts connections. It fails
// early when exited is closed.
func waitReady(targets readyTargets, exited <-chan struct{}) error {
	client := &http.Client{Timeout: 200 * time.Millisecond}
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return errProcessExited
		default:
		}
		if listenersReady(client, dialer, targets) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("not ready within 10s (ui %s, ingest %s, socket %s)",
		targets.ui, targets.ingest, targets.socket)
}

var errProcessExited = fmt.Errorf("process exited before becoming ready")

func listenersReady(client *http.Client, dialer net.Dialer, targets readyTargets) bool {
	response, err := client.Get("http://" + targets.ui + "/health/live")
	if err != nil {
		return false
	}
	response.Body.Close()
	for _, target := range [][2]string{{"tcp", targets.ingest}, {"unix", targets.socket}} {
		conn, err := dialer.Dial(target[0], target[1])
		if err != nil {
			return false
		}
		_ = conn.Close()
	}
	return true
}

// launchServe starts a server on freshly allocated addresses and waits until
// it is ready. If the child dies because another process took one of the
// addresses first, it retries with new ones.
func launchServe(
	t *testing.T, dataDir string, build func(uiAddr, ingestAddr string) *exec.Cmd,
) (*serveProcess, string, string) {
	t.Helper()
	var lastOutput string
	for attempt := 0; attempt < 3; attempt++ {
		uiAddr, ingestAddr := freeTCPAddr(t), freeTCPAddr(t)
		proc := startProcess(t, build(uiAddr, ingestAddr))
		err := proc.waitReady(readyTargets{
			ui: uiAddr, ingest: ingestAddr,
			socket: filepath.Join(dataDir, "siftail-control.sock"),
		})
		if err == nil {
			return proc, uiAddr, ingestAddr
		}
		lastOutput = proc.output.String()
		if err == errProcessExited && strings.Contains(lastOutput, "address already in use") {
			continue
		}
		t.Fatalf("serve process: %v\n%s", err, lastOutput)
	}
	t.Fatalf("serve process could not bind free addresses\n%s", lastOutput)
	return nil, "", ""
}

var (
	fixtureRFC3339 = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`)
	fixtureEpoch   = regexp.MustCompile(`("date"\s*:\s*)(\d+(?:\.\d+)?)`)
)

// fixtureAnchor is the newest event time in the checked-in ingestion fixtures.
var fixtureAnchor = time.Date(2026, 7, 28, 10, 5, 1, 0, time.UTC)

// fixtureShift is the whole-second offset that moves fixtureAnchor to one hour
// before now.
func fixtureShift(now time.Time) time.Duration {
	return now.Add(-time.Hour).Sub(fixtureAnchor).Truncate(time.Second)
}

// shiftFixtureTimestamps moves every RFC 3339 timestamp and numeric "date"
// epoch in an ingestion fixture by shift, preserving order and spacing.
func shiftFixtureTimestamps(t *testing.T, body []byte, shift time.Duration) []byte {
	t.Helper()
	body = fixtureRFC3339.ReplaceAllFunc(body, func(match []byte) []byte {
		parsed, err := time.Parse(time.RFC3339Nano, string(match))
		if err != nil {
			t.Fatalf("fixture timestamp %q: %v", match, err)
		}
		return []byte(parsed.Add(shift).UTC().Format("2006-01-02T15:04:05.000000Z"))
	})
	return fixtureEpoch.ReplaceAllFunc(body, func(match []byte) []byte {
		parts := fixtureEpoch.FindSubmatch(match)
		epoch, err := strconv.ParseFloat(string(parts[2]), 64)
		if err != nil {
			t.Fatalf("fixture epoch %q: %v", parts[2], err)
		}
		shifted := epoch + shift.Seconds()
		return append(append([]byte{}, parts[1]...), strconv.FormatFloat(shifted, 'f', 3, 64)...)
	})
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

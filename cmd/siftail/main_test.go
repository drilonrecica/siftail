package main

import (
	"bytes"
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
)

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
		if cmd.ProcessState != nil {
			t.Fatalf("serve helper exited early\n%s", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
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

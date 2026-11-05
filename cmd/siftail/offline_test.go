package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drilonrecica/siftail/internal/database"
)

// staleControlSocket leaves a socket file behind with nobody accepting on
// it, which is what SIGKILL does to a running server.
func staleControlSocket(t *testing.T, dataDir string) string {
	t.Helper()
	path := filepath.Join(dataDir, "siftail-control.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("stale socket was not left behind: %v", err)
	}
	return path
}

func listServers(t *testing.T) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"server", "list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("server list: %s", stderr.String())
	}
	return stdout.String()
}

func TestOfflineMutationsRefuseWhileDatabaseIsOwned(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, dataDir string)
	}{
		{
			// The server holds the lock from before it opens SQLite until it
			// creates the control socket.
			name:  "server startup window",
			setup: func(*testing.T, string) {},
		},
		{
			// A restarted server after SIGKILL: the old socket is stale while
			// the new process already owns the database.
			name: "server startup after SIGKILL",
			setup: func(t *testing.T, dataDir string) {
				staleControlSocket(t, dataDir)
			},
		},
		{
			// A restore holds the lock for its whole run and stages the
			// replacement next to the live database.
			name: "restore in progress",
			setup: func(t *testing.T, dataDir string) {
				if err := os.Mkdir(filepath.Join(dataDir, "restore-staging"), 0700); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearSiftailEnv(t)
			dataDir := t.TempDir()
			t.Setenv("SIFTAIL_DATA_DIR", dataDir)
			var stdout, stderr bytes.Buffer
			if code := run([]string{"server", "create", "--name", "Before"}, &stdout, &stderr); code != 0 {
				t.Fatalf("seed server: %s", stderr.String())
			}

			lock, err := database.AcquireMaintenanceLock(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			tc.setup(t, dataDir)
			for _, args := range [][]string{
				{"server", "create", "--name", "During"},
				{"server", "list"},
				{"token", "create", "--server", "1", "--name", "during"},
				{"token", "revoke", "--id", "1"},
				{"sessions", "revoke-all"},
			} {
				stdout.Reset()
				stderr.Reset()
				code := run(args, &stdout, &stderr)
				if code == 0 {
					t.Fatalf("%v succeeded while the database was owned: %q", args, stdout.String())
				}
				if !strings.Contains(stderr.String(), "in use by a starting server or a running restore") {
					t.Fatalf("%v error = %q", args, stderr.String())
				}
				if strings.Contains(stdout.String(), "sft_") {
					t.Fatalf("%v printed a token while refused", args)
				}
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			if tc.name == "restore in progress" {
				if err := os.Remove(filepath.Join(dataDir, "restore-staging")); err != nil {
					t.Fatal(err)
				}
			}
			servers := listServers(t)
			if !strings.Contains(servers, "Before") || strings.Contains(servers, "During") {
				t.Fatalf("database changed while owned: %q", servers)
			}
		})
	}
}

func TestOfflineMutationReleasesMaintenanceLock(t *testing.T) {
	clearSiftailEnv(t)
	dataDir := t.TempDir()
	t.Setenv("SIFTAIL_DATA_DIR", dataDir)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"server", "create", "--name", "Production"}, &stdout, &stderr); code != 0 {
		t.Fatalf("server create: %s", stderr.String())
	}
	lock, err := database.AcquireMaintenanceLock(dataDir)
	if err != nil {
		t.Fatalf("CLI kept the maintenance lock: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStaleControlSocketDoesNotBrickCLI(t *testing.T) {
	clearSiftailEnv(t)
	dataDir := t.TempDir()
	t.Setenv("SIFTAIL_DATA_DIR", dataDir)
	socket := staleControlSocket(t, dataDir)
	recovery := "rm " + socket

	var stdout, stderr bytes.Buffer
	if code := run([]string{"server", "create", "--name", "Offline"}, &stdout, &stderr); code != 0 {
		t.Fatalf("server create with stale socket: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Offline") {
		t.Fatalf("server create output = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "stale control socket") ||
		!strings.Contains(stderr.String(), recovery) {
		t.Fatalf("stale socket was not reported with recovery: %q", stderr.String())
	}
	if !strings.Contains(listServers(t), "Offline") {
		t.Fatal("offline mutation was not committed")
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"database", "check"}, &stdout, &stderr); code != 0 {
		t.Fatalf("database check with stale socket: %s", stderr.String())
	}

	for _, args := range [][]string{
		{"diagnostics"},
		{"backup", "--output", filepath.Join(t.TempDir(), "backup.db")},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Fatalf("%v succeeded without a server", args)
		}
		if !strings.Contains(stderr.String(), "requires the Siftail server to be active") &&
			!strings.Contains(stderr.String(), "require the Siftail server to be active") {
			t.Fatalf("%v error = %q", args, stderr.String())
		}
		if !strings.Contains(stderr.String(), recovery) {
			t.Fatalf("%v did not report the stale socket recovery: %q", args, stderr.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{
		"restore", "--confirm", "RESTORE", filepath.Join(t.TempDir(), "missing.db"),
	}, &stdout, &stderr); code == 0 {
		t.Fatal("restore of a missing artifact succeeded")
	}
	if strings.Contains(stderr.String(), "server to be stopped") {
		t.Fatalf("stale socket was treated as a live server by restore: %q", stderr.String())
	}
}

func TestProbeControlSocketStates(t *testing.T) {
	dataDir := t.TempDir()
	path := controlSocketPath(dataDir)
	if state, err := probeControlSocket(path); err != nil || state != controlSocketAbsent {
		t.Fatalf("absent = %v, %v", state, err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := probeControlSocket(path); err != nil || state != controlSocketLive {
		t.Fatalf("live = %v, %v", state, err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if state, err := probeControlSocket(path); err != nil || state != controlSocketStale {
		t.Fatalf("stale = %v, %v", state, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := probeControlSocket(path); err == nil ||
		!strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("regular file = %v", err)
	}
}

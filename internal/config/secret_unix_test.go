//go:build unix

package config

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A FIFO must be refused before open, which would block startup waiting for a
// writer.
func TestReadSecretFileRejectsFIFOWithoutBlocking(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	setEnv(t, testSecretFileName, path)
	_, _, err := readSecret(testSecretName)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("error = %v", err)
	}
}

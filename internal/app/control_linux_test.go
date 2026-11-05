//go:build linux

package app

import (
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestOwnerOnlyListenerSurvivesPeerCredentialFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	listener, err := openOwnerOnlyControlSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	owner := listener.(*ownerOnlyListener)
	var calls atomic.Int32
	owner.peerAllowed = func(conn *net.UnixConn, ownerUID uint32) (bool, error) {
		if calls.Add(1) == 1 {
			return false, errors.New("simulated SO_PEERCRED failure")
		}
		return controlPeerAllowed(conn, ownerUID)
	}

	accepted := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()

	first, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	// The rejected connection is closed by the listener: reads see EOF.
	_ = first.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := first.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection with failed credential lookup was not closed")
	}

	second, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	select {
	case conn := <-accepted:
		_ = conn.Close()
	case err := <-acceptErr:
		t.Fatalf("Accept returned an error after a peer credential failure: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("listener stopped accepting after a peer credential failure")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("peer checks = %d, want 2", got)
	}
}

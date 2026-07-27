//go:build linux

package app

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

type ownerOnlyListener struct {
	*net.UnixListener
	ownerUID uint32
}

func openOwnerOnlyControlSocket(path string) (net.Listener, error) {
	if err := removeStaleOwnedSocket(path); err != nil {
		return nil, err
	}

	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		return nil, fmt.Errorf("resolving control socket: %w", err)
	}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("listening on control socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)

	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("setting control socket permissions: %w", err)
	}

	return &ownerOnlyListener{
		UnixListener: listener,
		ownerUID:     uint32(os.Geteuid()),
	}, nil
}

func removeStaleOwnedSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspecting existing control socket: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("control socket path %q exists and is not a Unix socket", path)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("control socket path %q is not owned by the current user", path)
	}

	conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("another Siftail process is using control socket %q", path)
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) &&
		!errors.Is(dialErr, syscall.ENOENT) {
		return fmt.Errorf("checking existing control socket %q: %w", path, dialErr)
	}

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing stale control socket %q: %w", path, err)
	}
	return nil
}

func (l *ownerOnlyListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		allowed, err := controlPeerAllowed(conn, l.ownerUID)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("authorizing control connection: %w", err)
		}
		if !allowed {
			_ = conn.Close()
			continue
		}
		return conn, nil
	}
}

func controlPeerAllowed(conn *net.UnixConn, ownerUID uint32) (bool, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false, err
	}

	var credential *syscall.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		credential, socketErr = syscall.GetsockoptUcred(
			int(fd),
			syscall.SOL_SOCKET,
			syscall.SO_PEERCRED,
		)
	}); err != nil {
		return false, err
	}
	if socketErr != nil {
		return false, socketErr
	}
	return isAuthorizedControlUID(ownerUID, credential.Uid), nil
}

func isAuthorizedControlUID(ownerUID, peerUID uint32) bool {
	return ownerUID == peerUID
}

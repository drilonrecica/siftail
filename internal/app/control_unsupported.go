//go:build !linux

package app

import (
	"fmt"
	"net"
)

func openOwnerOnlyControlSocket(string) (net.Listener, error) {
	return nil, fmt.Errorf("the owner-only control socket is supported only on Linux")
}

func isAuthorizedControlUID(ownerUID, peerUID uint32) bool {
	return ownerUID == peerUID
}

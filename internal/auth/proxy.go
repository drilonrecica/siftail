package auth

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type proxyTrust struct {
	networks []netip.Prefix
}

func newProxyTrust(values []string) proxyTrust {
	trust := proxyTrust{}
	for _, value := range values {
		if prefix, err := netip.ParsePrefix(value); err == nil {
			trust.networks = append(trust.networks, prefix)
		}
	}
	return trust
}

func (p proxyTrust) trusted(address netip.Addr) bool {
	for _, network := range p.networks {
		if network.Contains(address) {
			return true
		}
	}
	return false
}

func (p proxyTrust) immediatePeer(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	address, _ := netip.ParseAddr(strings.Trim(host, "[]"))
	return address
}

func (p proxyTrust) clientIdentity(r *http.Request) string {
	peer := p.immediatePeer(r)
	if !peer.IsValid() {
		return "unknown"
	}
	if !p.trusted(peer) {
		return peer.String()
	}
	// A proxy may append its own header line rather than extending the
	// existing value, so every line is joined before the right-to-left walk.
	forwarded := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for index := len(forwarded) - 1; index >= 0; index-- {
		address, err := netip.ParseAddr(strings.TrimSpace(forwarded[index]))
		if err != nil {
			continue
		}
		if !p.trusted(address) {
			return address.String()
		}
	}
	return peer.String()
}

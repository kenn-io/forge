package server

import (
	"net"
	"net/http"
	"strings"
)

func isLocalTerminalClipboardRequest(
	r *http.Request,
	trustReverseProxy bool,
) bool {
	return isLocalTerminalClipboardRequestWithAddrs(
		r,
		trustReverseProxy,
		net.InterfaceAddrs,
	)
}

func isLocalTerminalClipboardRequestWithAddrs(
	r *http.Request,
	trustReverseProxy bool,
	interfaceAddrs func() ([]net.Addr, error),
) bool {
	// Forwarded client metadata is authoritative only when the immediate
	// peer is the local reverse proxy. Requiring one address also rejects
	// proxies that append to a client-supplied X-Forwarded-For chain.
	if !isLoopbackRemoteAddr(r.RemoteAddr) {
		return false
	}
	if !trustReverseProxy {
		return true
	}

	forwardedFor := r.Header.Values("X-Forwarded-For")
	if len(forwardedFor) != 1 || strings.Contains(forwardedFor[0], ",") {
		return false
	}
	clientIP := net.ParseIP(strings.TrimSpace(forwardedFor[0]))
	if clientIP == nil {
		return false
	}

	addrs, err := interfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		var interfaceIP net.IP
		switch value := addr.(type) {
		case *net.IPAddr:
			interfaceIP = value.IP
		case *net.IPNet:
			interfaceIP = value.IP
		}
		if interfaceIP != nil && interfaceIP.Equal(clientIP) {
			return true
		}
	}
	return false
}

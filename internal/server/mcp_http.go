package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"

	"go.kenn.io/forge/internal/config"
)

type MCPHTTPGuardOptions struct {
	Bind        config.HostKey
	Token       string
	RequireAuth bool
}

func NewMCPHTTPGuard(next http.Handler, opts MCPHTTPGuardOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mcpDirectLoopbackRequest(r, opts.Bind.Port) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if opts.RequireAuth && !mcpBearerMatches(r, opts.Token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mcpDirectLoopbackRequest(r *http.Request, port string) bool {
	if mcpHasForwardingHeader(r.Header) || !mcpLoopbackRemote(r.RemoteAddr) {
		return false
	}
	if !mcpLoopbackAuthority(r.Host, port) {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	return origin == "" || mcpLoopbackOrigin(origin, port)
}

func mcpHasForwardingHeader(header http.Header) bool {
	for name := range header {
		name = strings.ToLower(name)
		if name == "forwarded" || strings.HasPrefix(name, "x-forwarded-") {
			return true
		}
	}
	return false
}

func mcpLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func mcpLoopbackAuthority(authority, port string) bool {
	key, err := config.ParseHostKey(authority)
	if err != nil || key.Port != port {
		return false
	}
	return config.IsLoopbackHostname(strings.Trim(key.Host, "[]"))
}

func mcpLoopbackOrigin(origin, port string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return mcpLoopbackAuthority(parsed.Host, port)
}

func mcpBearerMatches(r *http.Request, token string) bool {
	got := strings.TrimSpace(r.Header.Get("Authorization"))
	want := "Bearer " + token
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

package authapi

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"go.kenn.io/forge/internal/config"
)

func DeriveHostCheckOptionsFromConfig(cfg *config.Config) (HostCheckOptions, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return HostCheckOptions{}, errors.New("host is empty")
	}
	if ip := net.ParseIP(cfg.Host); ip == nil {
		return HostCheckOptions{}, fmt.Errorf("config: invalid host %q", cfg.Host)
	} else if !ip.IsLoopback() {
		return HostCheckOptions{}, fmt.Errorf(
			"config: host %q is not loopback; only loopback addresses are supported",
			cfg.Host,
		)
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return HostCheckOptions{}, fmt.Errorf("port %d is outside 1-65535", cfg.Port)
	}
	bind, err := config.ParseHostKey(net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port)))
	if err != nil {
		return HostCheckOptions{}, fmt.Errorf("bind host %q: %w", cfg.ListenAddr(), err)
	}
	allowed := make([]config.HostKey, 0, len(cfg.AllowedHosts))
	for _, entry := range cfg.AllowedHosts {
		key, err := config.ParseHostKey(entry)
		if err != nil {
			return HostCheckOptions{}, fmt.Errorf("allowed_hosts entry %q: %w", entry, err)
		}
		allowed = append(allowed, key)
	}
	return HostCheckOptions{
		Bind:              bind,
		Allowed:           allowed,
		TrustReverseProxy: cfg.TrustReverseProxy,
	}, nil
}

func IsLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type SseController interface {
	SetWriteDeadline(time.Time) error
	Flush() error
}

// writeJSON encodes v as JSON and writes it with the given HTTP status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, v)
}

// writeError writes a JSON error response.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

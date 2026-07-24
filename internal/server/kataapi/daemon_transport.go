package kataapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/kata"
)

const (
	kataDaemonReadTimeout  = 20 * time.Second
	maxKataDaemonReadBytes = 128 << 20
)

type kataDaemonReadResult struct {
	status int
	body   []byte
	err    error
}

func kataDaemonGet(ctx context.Context, client *http.Client, d kata.Daemon, target string) kataDaemonReadResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return kataDaemonReadResult{err: err}
	}
	if token := kataDaemonForwardToken(d); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return kataDaemonReadResult{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	limited := io.LimitReader(resp.Body, maxKataDaemonReadBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return kataDaemonReadResult{err: err}
	}
	if len(body) > maxKataDaemonReadBytes {
		return kataDaemonReadResult{err: fmt.Errorf("kata daemon response exceeds %d bytes", maxKataDaemonReadBytes)}
	}
	return kataDaemonReadResult{status: resp.StatusCode, body: body}
}

func (h *Handler) kataDaemonHTTPClient(d kata.Daemon) (*http.Client, string, error) {
	target, transport, err := kataDaemonProxyTarget(d.URL)
	if err != nil {
		return nil, "", err
	}
	if transport == nil {
		transport = h.newHTTPTransport()
	}
	transport = disposableKataDaemonTransport(transport)
	base := strings.TrimSuffix(target.String(), "/")
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, base, nil
}

// DefaultDaemonHTTPClient builds a redirect-safe client using the standard
// Kata transport, including Unix-socket targets.
func DefaultDaemonHTTPClient(d kata.Daemon) (*http.Client, string, error) {
	target, transport, err := kataDaemonProxyTarget(d.URL)
	if err != nil {
		return nil, "", err
	}
	if transport == nil {
		transport = newDefaultKataDaemonTransport()
	}
	transport = disposableKataDaemonTransport(transport)
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, strings.TrimSuffix(target.String(), "/"), nil
}

package kata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	katacatalog "go.kenn.io/forge/internal/kata"
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

func kataDaemonGet(ctx context.Context, client *http.Client, d katacatalog.Daemon, target string) kataDaemonReadResult {
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

func (h *Handler) kataDaemonHTTPClient(d katacatalog.Daemon) (*http.Client, string, error) {
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
func DefaultDaemonHTTPClient(d katacatalog.Daemon) (*http.Client, string, error) {
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

func kataDaemonProxyTarget(target string) (*url.URL, http.RoundTripper, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, nil, err
	}
	switch parsed.Scheme {
	case "http", "https":
		if strings.TrimSpace(parsed.Hostname()) == "" {
			return nil, nil, errors.New("daemon url must include a host")
		}
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed, nil, nil
	case "unix":
		if strings.TrimSpace(parsed.Path) == "" {
			return nil, nil, errors.New("daemon url must include a socket path")
		}
		socketPath := parsed.Path
		return &url.URL{Scheme: "http", Host: "kata.invalid"}, &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		}, nil
	default:
		return nil, nil, errors.New("daemon url scheme must be http, https, or unix")
	}
}

func newDefaultKataDaemonTransport() http.RoundTripper {
	return (&http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}).Clone()
}

func disposableKataDaemonTransport(transport http.RoundTripper) http.RoundTripper {
	concrete, ok := transport.(*http.Transport)
	if !ok {
		return transport
	}
	owned := concrete.Clone()
	owned.DisableKeepAlives = true
	return owned
}

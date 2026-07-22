package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/kata"
)

// kataDaemonReadTimeout bounds server-side reads against a Kata daemon so a
// hung remote daemon cannot pin the API handler.
const kataDaemonReadTimeout = 20 * time.Second

// maxKataDaemonReadBytes caps the remaining raw project-list read. Keep it in
// line with generated detail and paginated responses; the former 8 MiB ceiling
// was too close to normal federated authority sizes.
const maxKataDaemonReadBytes = kataGeneratedResponseMaxBytes

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
	body, err := io.ReadAll(&kataLimitedDaemonResponseBody{
		body:      resp.Body,
		remaining: maxKataDaemonReadBytes,
		limit:     maxKataDaemonReadBytes,
		path:      req.URL.Path,
	})
	if err != nil {
		return kataDaemonReadResult{err: err}
	}
	return kataDaemonReadResult{status: resp.StatusCode, body: body}
}

// kataDaemonHTTPClient builds an HTTP client and base URL for server-side
// reads against a resolved daemon, reusing the proxy's target parsing so
// unix-socket daemons work identically.
func kataDaemonHTTPClient(d kata.Daemon) (*http.Client, string, error) {
	target, transport, err := kataDaemonProxyTarget(d.URL)
	if err != nil {
		return nil, "", err
	}
	if transport == nil {
		transport = newDefaultKataDaemonTransport()
	}
	transport = disposableKataDaemonTransport(transport)
	base := strings.TrimSuffix(target.String(), "/")
	// Like the proxy and health probe, never follow daemon redirects: a
	// misconfigured or malicious daemon must not bounce server-side reads
	// (and their Authorization header) to another target.
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, base, nil
}

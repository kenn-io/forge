// Package daemonclient discovers the local kenn-forge daemon and returns an
// HTTP client bound to its advertised address and base path. Every local
// thin client uses it, so they all resolve the daemon the same way.
package daemonclient

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/runtimelock"
)

// Client reaches a discovered daemon.
type Client struct {
	BaseURL   string
	Client    *http.Client
	TokenPath string
}

// Discover loads configPath, reads the running daemon's runtime record, and
// returns a client for it. The runtime record's base path wins; the config's
// base_path applies only when the record does not carry one.
func Discover(configPath string, timeout time.Duration) (Client, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return Client{}, fmt.Errorf("load config: %w", err)
	}
	status, err := runtimelock.Read(cfg.DataDir)
	if err != nil {
		return Client{}, fmt.Errorf("read runtime status: %w", err)
	}
	if !status.Running || status.Metadata == nil {
		return Client{}, fmt.Errorf(
			"no kenn-forge daemon is running on %s", cfg.DataDir,
		)
	}

	prefix := status.Metadata.BasePath
	if prefix == "" {
		prefix = cfg.BasePath
	}
	prefix = strings.TrimSuffix(prefix, "/")
	token, err := runtimelock.ReadAuthToken(cfg.DataDir)
	if err != nil {
		return Client{}, err
	}
	baseURL := fmt.Sprintf("http://%s%s", status.Metadata.ListenAddr, prefix)
	transport := daemonOriginTransport{
		token: token, origin: "http://" + status.Metadata.ListenAddr,
		base: http.DefaultTransport,
	}
	return Client{
		BaseURL:   baseURL,
		Client:    &http.Client{Timeout: timeout, Transport: transport},
		TokenPath: status.Metadata.TokenPath,
	}, nil
}

type daemonOriginTransport struct {
	token  string
	origin string
	base   http.RoundTripper
}

func (t daemonOriginTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	request := req.Clone(req.Context())
	request.Header = req.Header.Clone()
	requestOrigin := request.URL.Scheme + "://" + request.URL.Host
	if strings.EqualFold(requestOrigin, t.origin) {
		if t.token != "" {
			request.Header.Set("Authorization", "Bearer "+t.token)
		}
	} else {
		request.Header.Del("Authorization")
		request.Header.Del("X-Forwarded-Host")
	}
	return t.base.RoundTrip(request)
}

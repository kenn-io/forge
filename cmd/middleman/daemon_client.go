package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/runtimelock"
)

type daemonHTTPClient struct {
	BaseURL string
	Client  *http.Client
}

func discoverDaemonHTTP(configPath string, timeout time.Duration) (daemonHTTPClient, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return daemonHTTPClient{}, fmt.Errorf("load config: %w", err)
	}
	status, err := runtimelock.Read(cfg.DataDir)
	if err != nil {
		return daemonHTTPClient{}, fmt.Errorf("read runtime status: %w", err)
	}
	if !status.Running || status.Metadata == nil {
		return daemonHTTPClient{}, fmt.Errorf(
			"no middleman daemon is running on %s", cfg.DataDir,
		)
	}

	prefix := status.Metadata.BasePath
	if prefix == "" {
		prefix = cfg.BasePath
	}
	prefix = strings.TrimSuffix(prefix, "/")
	token, err := runtimelock.ReadAuthToken(cfg.DataDir)
	if err != nil {
		return daemonHTTPClient{}, err
	}
	transport := http.RoundTripper(http.DefaultTransport)
	baseURL := fmt.Sprintf("http://%s%s", status.Metadata.ListenAddr, prefix)
	if token != "" {
		transport = bearerTransport{
			token: token, origin: "http://" + status.Metadata.ListenAddr, base: transport,
		}
	}
	return daemonHTTPClient{
		BaseURL: baseURL,
		Client:  &http.Client{Timeout: timeout, Transport: transport},
	}, nil
}

type bearerTransport struct {
	token  string
	origin string
	base   http.RoundTripper
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	request := req.Clone(req.Context())
	request.Header = req.Header.Clone()
	requestOrigin := request.URL.Scheme + "://" + request.URL.Host
	if strings.EqualFold(requestOrigin, t.origin) {
		request.Header.Set("Authorization", "Bearer "+t.token)
	} else {
		request.Header.Del("Authorization")
	}
	return t.base.RoundTrip(request)
}

package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"go.kenn.io/middleman/internal/config"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

// TestNewCfgNilTestFriendlyDefault pins the test-friendly default
// installed by resolveHostCheckOptions for the cfg=nil server.New
// path. Future contributors must not widen this default
// accidentally (e.g., by adding 0.0.0.0 or attacker-style hosts).
// The default accepts loopback IPs at any port (httptest.NewServer
// uses ephemeral ports) plus the two named test hostnames; nothing
// else.
func TestNewCfgNilTestFriendlyDefault(t *testing.T) {
	srv := newServerForDefaultTest(t)

	cases := []struct {
		name   string
		host   string
		status int
	}{
		{name: "127.0.0.1:8091 accepted", host: "127.0.0.1:8091", status: http.StatusOK},
		{name: "127.0.0.1 ephemeral port accepted (httptest.NewServer)", host: "127.0.0.1:44321", status: http.StatusOK},
		{name: "[::1] ephemeral port accepted", host: "[::1]:44321", status: http.StatusOK},
		{name: "example.com accepted (httptest default)", host: "example.com", status: http.StatusOK},
		{name: "middleman.test accepted (apitest default)", host: "middleman.test", status: http.StatusOK},
		{name: "attacker.example rejected", host: "attacker.example", status: http.StatusForbidden},
		{name: "localhost ephemeral port rejected (no any-port for non-literal)", host: "localhost:44321", status: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Host = tc.host
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			Assert.Equal(t, tc.status, rr.Code, rr.Body.String())
		})
	}
}

func TestNewDerivesHostCheckFromUnvalidatedConfig(t *testing.T) {
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, emptyFrontend(), "/", &config.Config{
		Host:              "127.0.0.1",
		Port:              8091,
		AllowedHosts:      []string{"mm.example.com"},
		TrustReverseProxy: true,
	}, ServerOptions{})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "127.0.0.1:8091"
	req.Header.Set("X-Forwarded-Host", "mm.example.com")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	Assert.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
}

func TestNewRejectsUnvalidatedConfigWithNonLoopbackHost(t *testing.T) {
	old := allowUnvalidatedConfigHostCheckFallbackForTests
	allowUnvalidatedConfigHostCheckFallbackForTests = false
	t.Cleanup(func() {
		allowUnvalidatedConfigHostCheckFallbackForTests = old
	})

	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)

	Assert.PanicsWithError(t,
		`server: config did not provide valid Host check options: config: host "0.0.0.0" is not loopback; only loopback addresses are supported`,
		func() {
			New(database, syncer, emptyFrontend(), "/", &config.Config{
				Host: "0.0.0.0",
				Port: 8091,
			}, ServerOptions{})
		},
	)
}

func newServerForDefaultTest(t *testing.T) *Server {
	t.Helper()
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	// cfg=nil, ServerOptions zero — exercise the test-friendly
	// default branch of resolveHostCheckOptions.
	return New(database, syncer, emptyFrontend(), "/", nil, ServerOptions{})
}

func emptyFrontend() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte("<!DOCTYPE html><html><body>ok</body></html>"),
		},
	}
}

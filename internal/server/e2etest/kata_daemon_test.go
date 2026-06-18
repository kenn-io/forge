package e2etest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient"
	"go.kenn.io/middleman/internal/kata"
)

func TestKataLocalDaemonChallengeIsDownAndProxyUpstreamErrorE2E(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var mu sync.Mutex
	authorizations := []string{}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("ETag", `"upstream"`)
		w.Header().Set("WWW-Authenticate", `Bearer realm="kata"`)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"Authentication required"}`))
	}))
	defer daemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_AUTH_TOKEN", "")
	t.Setenv("KATA_DB", "")
	writeKataE2ECatalog(t, home, `
active_daemon = "local"

[[daemon]]
name = "local"
local = true
`)
	writeKataE2ERuntimeRecord(t, daemon.URL)
	srv, _ := setupTestServer(t)
	middleman := httptest.NewServer(srv)
	defer middleman.Close()

	client, err := apiclient.New(middleman.URL)
	require.NoError(err)
	roster, err := client.HTTP.ListKataDaemonsWithResponse(t.Context())
	require.NoError(err)
	require.Equal(http.StatusOK, roster.StatusCode(), string(roster.Body))
	require.NotNil(roster.JSON200)
	require.NotNil(roster.JSON200.Daemons)
	require.Len(*roster.JSON200.Daemons, 1)
	localDaemon := (*roster.JSON200.Daemons)[0]
	assert.Equal("local", localDaemon.Id)
	assert.Equal("none", localDaemon.Auth)
	assert.Equal("down", localDaemon.Health)

	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		middleman.URL+"/api/v1/kata/proxy/api/v1/instance",
		http.NoBody,
	)
	require.NoError(err)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := middleman.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(err)

	assert.Equal(http.StatusBadGateway, resp.StatusCode)
	assert.Empty(resp.Header.Get("Content-Encoding"))
	assert.Empty(resp.Header.Get("ETag"))
	assert.Empty(resp.Header.Get("WWW-Authenticate"))
	assert.Contains(string(body), `"code":"upstreamError"`)
	assert.NotContains(string(body), "Authentication required")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal([]string{"", ""}, authorizations)
}

func TestKataLocalDaemonTokenEnvIsNotUsedForRosterOrProxyE2E(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var mu sync.Mutex
	authorizations := []string{}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"instance":"local"}`))
	}))
	defer daemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_AUTH_TOKEN", "")
	t.Setenv("KATA_DB", "")
	t.Setenv("MIDDLEMAN_KATA_MISSING_TOKEN", "")
	writeKataE2ECatalog(t, home, `
active_daemon = "local"

[[daemon]]
name = "local"
local = true
token_env = "MIDDLEMAN_KATA_MISSING_TOKEN"
`)
	writeKataE2ERuntimeRecord(t, daemon.URL)
	srv, _ := setupTestServer(t)
	middleman := httptest.NewServer(srv)
	defer middleman.Close()

	client, err := apiclient.New(middleman.URL)
	require.NoError(err)
	roster, err := client.HTTP.ListKataDaemonsWithResponse(t.Context())
	require.NoError(err)
	require.Equal(http.StatusOK, roster.StatusCode(), string(roster.Body))
	require.NotNil(roster.JSON200)
	require.NotNil(roster.JSON200.Daemons)
	require.Len(*roster.JSON200.Daemons, 1)
	localDaemon := (*roster.JSON200.Daemons)[0]
	assert.Equal("local", localDaemon.Id)
	assert.Equal("none", localDaemon.Auth)
	assert.Equal("connected", localDaemon.Health)

	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		middleman.URL+"/api/v1/kata/proxy/api/v1/instance",
		http.NoBody,
	)
	require.NoError(err)
	req.Header.Set("Authorization", "Bearer caller-secret")
	resp, err := middleman.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(err)

	assert.Equal(http.StatusOK, resp.StatusCode, string(body))
	assert.JSONEq(`{"instance":"local"}`, string(body))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal([]string{"", ""}, authorizations)
}

func writeKataE2ECatalog(t *testing.T, home string, body string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600))
}

func writeKataE2ERuntimeRecord(t *testing.T, address string) {
	t.Helper()

	runtimeDir, err := kata.RuntimeDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(runtimeDir, 0o700))
	rec := kata.RuntimeRecord{
		PID:       os.Getpid(),
		Address:   address,
		StartedAt: time.Now().UTC(),
	}
	body, err := json.Marshal(rec)
	require.NoError(t, err)
	path := filepath.Join(runtimeDir, "daemon."+strconv.Itoa(rec.PID)+".json")
	require.NoError(t, os.WriteFile(path, body, 0o600))
}

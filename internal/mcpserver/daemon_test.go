package mcpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/runtimelock"
)

func TestDaemonErrorUsesStableStructuredEnvelope(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	err := (&daemonError{
		Kind: "conflict", Code: "conflict", Message: "workflow state changed",
		Details:   map[string]any{"current_status": "waiting"},
		Retryable: false, Ambiguous: true,
	}).Error()
	var envelope map[string]any
	require.NoError(json.Unmarshal([]byte(err), &envelope))
	assert.Equal("conflict", envelope["kind"])
	assert.Equal("conflict", envelope["code"])
	assert.Equal("workflow state changed", envelope["message"])
	assert.Equal(false, envelope["retryable"])
	assert.Equal(true, envelope["ambiguous"])
}

func writeFakeDaemonFiles(t *testing.T, ts *httptest.Server, token string) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, fmt.Appendf(nil, "data_dir = %q\n", dir), 0o600))

	handle, err := runtimelock.Acquire(dir)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, handle.Release())
	})

	u, err := url.Parse(ts.URL)
	require.NoError(t, err)
	require.NoError(t, handle.WriteMetadata(runtimelock.Metadata{
		PID:         os.Getpid(),
		ListenAddr:  u.Host,
		BasePath:    "/",
		TokenPath:   runtimelock.AuthTokenPath(dir),
		RequireAuth: token != "",
	}))
	require.NoError(t, os.WriteFile(runtimelock.AuthTokenPath(dir), []byte(token), 0o600))
	return cfgPath
}

func writeDaemonMetadataForServer(
	t *testing.T,
	handle *runtimelock.Handle,
	dir string,
	ts *httptest.Server,
	pid int,
	startedAt string,
) {
	t.Helper()
	u, err := url.Parse(ts.URL)
	require.NoError(t, err)
	require.NoError(t, handle.WriteMetadata(runtimelock.Metadata{
		PID:         pid,
		ListenAddr:  u.Host,
		StartedAt:   startedAt,
		BasePath:    "/",
		TokenPath:   runtimelock.AuthTokenPath(dir),
		RequireAuth: false,
	}))
}

func TestDaemonClientGetJSONSendsBearer(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer ts.Close()
	cfg := writeFakeDaemonFiles(t, ts, "sekrit")

	c := newDaemonClient(cfg, 5*time.Second)
	var out struct {
		OK bool `json:"ok"`
	}
	require.NoError(t, c.getJSON(t.Context(), "/api/v1/activity", nil, &out))
	assert.True(t, out.OK)
	assert.Equal(t, "Bearer sekrit", gotAuth)
}

func TestDaemonClientDaemonUnavailable(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, "data_dir = %q\n", dir), 0o600))

	c := newDaemonClient(cfgPath, time.Second)
	var out any
	err := c.getJSON(t.Context(), "/api/v1/activity", nil, &out)
	var derr *daemonError
	require.ErrorAs(err, &derr)
	assert.Equal("daemon_unavailable", derr.Kind)
	assert.NotContains(derr.Message, "sekrit")
	assert.NotContains(derr.Message, runtimelock.AuthTokenPath(dir))
}

func TestDaemonClientMapsProblems(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		switch r.URL.Path {
		case "/api/v1/nf":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":404,"code":"notFound","detail":"diff not available"}`))
		case "/api/v1/nf-item":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":404,"code":"pullNotFound","detail":"nope"}`))
		case "/api/v1/conflict":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"status":409,"code":"conflict","detail":"workflow state changed","details":{"current_status":"reviewing","expected_status":"new"}}`))
		case "/api/v1/auth":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":401,"code":"unauthorized","detail":"missing token"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"status":500,"code":"internal","detail":"unexpected path"}`))
		}
	}))
	defer ts.Close()
	cfg := writeFakeDaemonFiles(t, ts, "")
	c := newDaemonClient(cfg, 5*time.Second)

	var out any
	var derr *daemonError
	require.ErrorAs(c.getJSON(t.Context(), "/api/v1/nf", nil, &out), &derr)
	assert.Equal("not_found", derr.Kind)

	derr = nil
	require.ErrorAs(c.getJSON(t.Context(), "/api/v1/nf-item", nil, &out), &derr)
	assert.Equal("not_found", derr.Kind)

	derr = nil
	require.ErrorAs(c.putJSON(t.Context(), "/api/v1/conflict", map[string]any{}, &out), &derr)
	assert.Equal("conflict", derr.Kind)
	assert.Equal("reviewing", derr.Details["current_status"])
	assert.Equal("new", derr.Details["expected_status"])

	derr = nil
	require.ErrorAs(c.getJSON(t.Context(), "/api/v1/auth", nil, &out), &derr)
	assert.Equal("daemon_auth", derr.Kind)
}

func TestDaemonClientWorkflowProbeMapsMissingRouteToVersionMismatch(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"code":"notFound","detail":"route missing"}`))
	}))
	defer ts.Close()
	cfg := writeFakeDaemonFiles(t, ts, "")
	c := newDaemonClient(cfg, 5*time.Second)

	var derr *daemonError
	require.ErrorAs(c.ensureWorkflowStateSupported(t.Context()), &derr)
	assert.Equal("version_mismatch", derr.Kind)
	assert.Contains(derr.Message, "/workflow-state")

	derr = nil
	require.ErrorAs(c.ensureWorkflowStateSupported(t.Context()), &derr)
	assert.Equal("version_mismatch", derr.Kind)
	assert.Equal(1, requests)
}

func TestDaemonClientWorkflowProbeRechecksAfterDaemonIdentityChanges(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, "data_dir = %q\n", dir), 0o600))
	require.NoError(os.WriteFile(runtimelock.AuthTokenPath(dir), nil, 0o600))

	handle, err := runtimelock.Acquire(dir)
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(handle.Release())
	})

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"code":"notFound","detail":"route missing"}`))
	}))
	defer first.Close()
	secondCalls := 0
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer second.Close()

	writeDaemonMetadataForServer(t, handle, dir, first, 101, "2026-07-01T10:00:00Z")
	c := newDaemonClient(cfgPath, 5*time.Second)
	var derr *daemonError
	require.ErrorAs(c.ensureWorkflowStateSupported(t.Context()), &derr)
	assert.Equal("version_mismatch", derr.Kind)

	writeDaemonMetadataForServer(t, handle, dir, second, 202, "2026-07-01T10:01:00Z")
	require.NoError(c.discover())
	require.NoError(c.ensureWorkflowStateSupported(t.Context()))
	assert.Equal(1, secondCalls)
}

func TestDaemonClientWorkflowProbeRefreshesIdentityBeforeUsingCachedResult(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, "data_dir = %q\n", dir), 0o600))
	require.NoError(os.WriteFile(runtimelock.AuthTokenPath(dir), nil, 0o600))

	handle, err := runtimelock.Acquire(dir)
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(handle.Release())
	})

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"code":"notFound","detail":"route missing"}`))
	}))
	defer first.Close()
	secondCalls := 0
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer second.Close()

	writeDaemonMetadataForServer(t, handle, dir, first, 101, "2026-07-01T10:00:00Z")
	c := newDaemonClient(cfgPath, 5*time.Second)
	var derr *daemonError
	require.ErrorAs(c.ensureWorkflowStateSupported(t.Context()), &derr)
	assert.Equal("version_mismatch", derr.Kind)

	writeDaemonMetadataForServer(t, handle, dir, second, 202, "2026-07-01T10:01:00Z")
	require.NoError(c.ensureWorkflowStateSupported(t.Context()))
	assert.Equal(1, secondCalls)
}

func TestDaemonClientPutRefreshesDiscoveryAfterUnavailableWithoutReplaying(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(cfgPath, fmt.Appendf(nil, "data_dir = %q\n", dir), 0o600))
	require.NoError(os.WriteFile(runtimelock.AuthTokenPath(dir), nil, 0o600))

	handle, err := runtimelock.Acquire(dir)
	require.NoError(err)
	t.Cleanup(func() {
		require.NoError(handle.Release())
	})

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	writeDaemonMetadataForServer(t, handle, dir, first, 101, "2026-07-01T10:00:00Z")
	c := newDaemonClient(cfgPath, 5*time.Second)
	var out any
	require.NoError(c.getJSON(t.Context(), "/api/v1/ping", nil, &out))
	first.Close()

	secondCalls := 0
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer second.Close()
	writeDaemonMetadataForServer(t, handle, dir, second, 202, "2026-07-01T10:01:00Z")

	err = c.putJSON(t.Context(), "/api/v1/workflow-state/pr/gh/acme/widget/1", map[string]any{"status": "reviewing"}, &out)
	require.Error(err)
	var derr *daemonError
	require.ErrorAs(err, &derr)
	assert.Equal("daemon_unavailable", derr.Kind)
	assert.Equal(0, secondCalls)

	require.NoError(c.putJSON(t.Context(), "/api/v1/workflow-state/pr/gh/acme/widget/1", map[string]any{"status": "reviewing"}, &out))
	assert.Equal(1, secondCalls)
}

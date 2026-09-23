package settingstest

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func newTestServer(t *testing.T) *server.Server {
	t.Helper()
	return server.New(serverfake.OpenTestDB(t), nil, nil, "/", nil, server.ServerOptions{})
}

func TestServeRejectsRebindingHost(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv := newTestServer(t)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(err)
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()
	t.Cleanup(func() {
		serverfake.GracefulShutdown(t, srv)
		err := <-errCh
		require.ErrorIs(err, http.ErrServerClosed)
	})

	validReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+"/healthz", nil)
	require.NoError(err)
	validResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(validReq)
	require.NoError(err)
	_, err = io.Copy(io.Discard, validResp.Body)
	require.NoError(err)
	require.NoError(validResp.Body.Close())
	assert.Equal(http.StatusOK, validResp.StatusCode)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+"/healthz", nil)
	require.NoError(err)
	req.Host = "evil.example:8091"

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(err)
	defer resp.Body.Close()

	require.Equal(http.StatusForbidden, resp.StatusCode)
	var body struct {
		Error string `json:"error"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(body.Error, "allowed_hosts")
	assert.Contains(body.Error, "trust_reverse_proxy")
}

func TestServeAllowsBoundLoopbackHost(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv := newTestServer(t)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("127.0.0.2 loopback alias unavailable: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()
	t.Cleanup(func() {
		serverfake.GracefulShutdown(t, srv)
		err := <-errCh
		require.ErrorIs(err, http.ErrServerClosed)
	})

	validReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+"/healthz", nil)
	require.NoError(err)
	validResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(validReq)
	require.NoError(err)
	_, err = io.Copy(io.Discard, validResp.Body)
	require.NoError(err)
	require.NoError(validResp.Body.Close())
	assert.Equal(http.StatusOK, validResp.StatusCode)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+"/healthz", nil)
	require.NoError(err)
	req.Host = "evil.example:8091"

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(err)
	assert.Equal(http.StatusForbidden, resp.StatusCode)
	_, err = io.Copy(io.Discard, resp.Body)
	require.NoError(err)
	require.NoError(resp.Body.Close())
}

func TestSSE_ReturnsEventStream(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s)
	defer ts.Close()

	respReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/v1/events", nil)
	require.NoError(t, err)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
}

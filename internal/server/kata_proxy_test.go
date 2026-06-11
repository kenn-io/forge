package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/kata"
)

const kataProxyTestDaemonHeaderName = "X-Middleman-Kata-Daemon"

func TestKataProxyRoutesDefaultDaemon(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var mu sync.Mutex
	var receivedPath, receivedQuery string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedPath = r.URL.Path
		receivedQuery = r.URL.RawQuery
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"instance":"home"}`))
	}))
	defer daemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/proxy/api/v1/instance?detail=1", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.JSONEq(`{"instance":"home"}`, rr.Body.String())
	assert.Contains(rr.Header().Values("Vary"), kataProxyTestDaemonHeaderName)
	mu.Lock()
	assert.Equal("/api/v1/instance", receivedPath)
	assert.Equal("detail=1", receivedQuery)
	mu.Unlock()
}

func TestKataProxyRoutesSelectedDaemonAndInjectsToken(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	stub := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal("Bearer "+id+"-secret", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(id))
		}))
	}
	homeDaemon := stub("home")
	defer homeDaemon.Close()
	workDaemon := stub("work")
	defer workDaemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
active_daemon = "home"

[[daemon]]
name = "home"
url = "`+homeDaemon.URL+`"
token = "home-secret"

[[daemon]]
name = "work"
url = "`+workDaemon.URL+`"
token = "work-secret"
`)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)
	req.Header.Set(kataProxyTestDaemonHeaderName, "work")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("work", strings.TrimSpace(rr.Body.String()))
	assert.Contains(rr.Header().Values("Vary"), kataProxyTestDaemonHeaderName)
}

func TestKataProxyDoesNotOverrideAuthorization(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var receivedAuthorization string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer daemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+daemon.URL+`"
token = "configured-secret"
`)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)
	req.Header.Set("Authorization", "Bearer caller-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("Bearer caller-secret", receivedAuthorization)
}

func TestKataProxyStripsBrowserAndSelectorHeaders(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var receivedOrigin, receivedSelector string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedOrigin = r.Header.Get("Origin")
		receivedSelector = r.Header.Get(kataProxyTestDaemonHeaderName)
		w.WriteHeader(http.StatusOK)
	}))
	defer daemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)
	req.Header.Set("Origin", "http://middleman.invalid")
	req.Header.Set(kataProxyTestDaemonHeaderName, "home")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Empty(receivedOrigin)
	assert.Empty(receivedSelector)
}

func TestKataProxyStreamsSSEAndForwardsCursorHeaders(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	type receivedHeaders struct {
		accept        string
		authorization string
		lastEventID   string
		origin        string
		selector      string
	}

	headers := make(chan receivedHeaders, 1)
	firstFrameWritten := make(chan struct{})
	releaseSecondFrame := make(chan struct{})
	var releaseSecondFrameOnce sync.Once
	releaseSecond := func() {
		releaseSecondFrameOnce.Do(func() {
			close(releaseSecondFrame)
		})
	}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- receivedHeaders{
			accept:        r.Header.Get("Accept"),
			authorization: r.Header.Get("Authorization"),
			lastEventID:   r.Header.Get("Last-Event-ID"),
			origin:        r.Header.Get("Origin"),
			selector:      r.Header.Get(kataProxyTestDaemonHeaderName),
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("id: 8\nevent: issue.updated\ndata: {\"event_id\":8}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(firstFrameWritten)
		<-releaseSecondFrame
		_, _ = w.Write([]byte("id: 9\nevent: issue.updated\ndata: {\"event_id\":9}\n\n"))
	}))

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+daemon.URL+`"
token = "stream-secret"
`)
	srv, _ := setupTestServer(t)
	middleman := httptest.NewServer(srv)
	t.Cleanup(daemon.Close)
	t.Cleanup(middleman.Close)
	t.Cleanup(releaseSecond)

	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		middleman.URL+"/api/v1/kata/proxy/api/v1/events/stream",
		http.NoBody,
	)
	require.NoError(err)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Last-Event-ID", "7")
	req.Header.Set("Origin", "http://middleman.invalid")
	req.Header.Set(kataProxyTestDaemonHeaderName, "home")

	resp, err := middleman.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	assert.Equal("text/event-stream", resp.Header.Get("Content-Type"))

	select {
	case got := <-headers:
		assert.Equal("text/event-stream", got.accept)
		assert.Equal("Bearer stream-secret", got.authorization)
		assert.Equal("7", got.lastEventID)
		assert.Empty(got.origin)
		assert.Empty(got.selector)
	case <-time.After(time.Second):
		require.Fail("timed out waiting for proxied stream request")
	}
	select {
	case <-firstFrameWritten:
	case <-time.After(time.Second):
		require.Fail("timed out waiting for upstream stream frame")
	}

	scanner := bufio.NewScanner(resp.Body)
	firstFrame := readSSEFrameWithin(t, scanner, time.Second, releaseSecond)
	assert.Equal("8", firstFrame.ID)
	assert.Equal("issue.updated", firstFrame.Event)

	releaseSecond()
	secondFrame := readSSEFrameWithin(t, scanner, time.Second, releaseSecond)
	assert.Equal("9", secondFrame.ID)
	assert.Equal("issue.updated", secondFrame.Event)
}

func TestKataProxyReusesProxyForResolvedDaemon(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)
	daemon := kata.Daemon{ID: "home", URL: "http://127.0.0.1:65535", Token: "secret"}

	first, err := srv.kataProxyForDaemon(daemon)
	require.NoError(err)
	second, err := srv.kataProxyForDaemon(daemon)
	require.NoError(err)

	assert.Equal(first.handler, second.handler)
	assert.Len(srv.kataProxyCache, 1)
}

func TestKataProxyHTTPDaemonUsesOwnedTransport(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("owned"))
	}))
	defer daemon.Close()

	originalTransport := http.DefaultTransport
	http.DefaultTransport = poisonDefaultTransport{}
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
	})

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("owned", strings.TrimSpace(rr.Body.String()))
}

func TestKataProxyHTTPDaemonIgnoresMutatedDefaultHTTPTransport(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("owned"))
	}))
	defer daemon.Close()

	originalTransport := http.DefaultTransport
	http.DefaultTransport = &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, fmt.Errorf("poison default transport dialed")
		},
	}
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
	})

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("owned", strings.TrimSpace(rr.Body.String()))
}

func TestKataProxyUnknownDaemonSelectionReturnsProblem(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer daemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)
	req.Header.Set(kataProxyTestDaemonHeaderName, "missing")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Contains(rr.Header().Values("Vary"), kataProxyTestDaemonHeaderName)
	assert.Contains(rr.Body.String(), "unknown")
}

func TestKataProxyRejectsUnsetTokenEnvBeforeForwarding(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var reached bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer daemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("MIDDLEMAN_KATA_PROXY_MISSING_TOKEN", "")
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+daemon.URL+`"
token_env = "MIDDLEMAN_KATA_PROXY_MISSING_TOKEN"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeBadRequest, problem.Code)
	assert.Contains(problem.Detail, "token_env")
	assert.Contains(problem.Detail, "MIDDLEMAN_KATA_PROXY_MISSING_TOKEN")
	assert.False(reached)
}

func TestKataProxyRejectsInvalidCatalogBeforeForwarding(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var reached bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer daemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+daemon.URL+`"

[[daemon]]
name = "home"
local = true
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeBadRequest, problem.Code)
	assert.Contains(problem.Detail, "duplicate")
	assert.Contains(problem.Detail, "home")
	assert.False(reached)
}

func TestKataProxyNoConfiguredDaemonReturnsServiceUnavailable(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	t.Setenv("KATA_HOME", t.TempDir())
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)

	require.Equal(http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), "serviceUnavailable")
}

func TestKataProxyUnreachableDaemonReturnsBadGateway(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(err)
	addr := listener.Addr().String()
	require.NoError(listener.Close())

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "http://`+addr+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)

	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), "upstreamError")
}

func TestKataProxyLocalDaemonResolvesRuntimeAfterServerStart(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("late-local"))
	}))
	defer daemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	writeKataProxyCatalog(t, home, `
active_daemon = "local"

[[daemon]]
name = "local"
local = true
`)
	srv, _ := setupTestServer(t)

	call := func() *httptest.ResponseRecorder {
		return doJSON(t, srv, http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)
	}

	rr := call()
	require.Equal(http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), "serviceUnavailable")

	runtimeFile := writeKataProxyRuntimeRecord(t, daemon.URL)
	rr = call()
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("late-local", strings.TrimSpace(rr.Body.String()))

	require.NoError(os.Remove(runtimeFile))
	rr = call()
	require.Equal(http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), "serviceUnavailable")

	writeKataProxyRuntimeRecord(t, daemon.URL)
	rr = call()
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("late-local", strings.TrimSpace(rr.Body.String()))
}

func TestKataProxyLocalDaemonRejectsNonLocalRuntimeTargets(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	for _, target := range []string{"http://203.0.113.10:8080", "https://kata.example.com"} {
		t.Run(target, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("KATA_HOME", home)
			t.Setenv("KATA_DB", "")
			writeKataProxyCatalog(t, home, `
active_daemon = "local"

[[daemon]]
name = "local"
local = true
`)
			writeKataProxyRuntimeRecord(t, target)
			srv, _ := setupTestServer(t)

			rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)

			require.Equal(http.StatusServiceUnavailable, rr.Code, rr.Body.String())
			assert.Contains(rr.Body.String(), "serviceUnavailable")
		})
	}
}

func TestKataProxyForwardsViaUnixSocket(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	t.Setenv("TMPDIR", "/tmp") // Keep Unix socket paths below macOS' length limit.
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "proxy.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(err)

	upstream := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal("/api/v1/instance", r.URL.Path)
			assert.Equal("Bearer unix-secret", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"instance":"unix"}`))
		}),
	}
	upstreamDone := make(chan struct{})
	go func() {
		_ = upstream.Serve(listener)
		close(upstreamDone)
	}()
	t.Cleanup(func() {
		require.NoError(upstream.Close())
		<-upstreamDone
	})

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "unix://`+socketPath+`"
token = "unix-secret"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.JSONEq(`{"instance":"unix"}`, rr.Body.String())
}

func TestKataProxyForwardsNonJSONMutationWithSameOriginFetchSite(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var receivedContentType, receivedBody string
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		receivedContentType = r.Header.Get("Content-Type")
		receivedBody = string(body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	}))
	defer daemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/kata/proxy/api/v1/files", strings.NewReader("raw markdown"))
	req.Header.Set("Content-Type", "text/markdown")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())
	assert.Equal("text/markdown", receivedContentType)
	assert.Equal("raw markdown", receivedBody)
	assert.Equal("created", strings.TrimSpace(rr.Body.String()))
}

func TestKataProxyRejectsNonJSONMutationWithoutFetchSiteProof(t *testing.T) {
	require := require.New(t)

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer daemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/kata/proxy/api/v1/files", strings.NewReader("raw markdown"))
	req.Header.Set("Content-Type", "text/markdown")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusUnsupportedMediaType, rr.Code, rr.Body.String())
}

func TestKataProxyRejectsCrossSiteMutationBeforeForwarding(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var reached bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer daemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/kata/proxy/api/v1/files", strings.NewReader(`{"ok":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusForbidden, rr.Code, rr.Body.String())
	assert.False(reached)
}

func TestKataProxyDoesNotForwardTrace(t *testing.T) {
	assert := Assert.New(t)

	var reached bool
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer daemon.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+daemon.URL+`"
token = "secret"
`)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodTrace, "/api/v1/kata/proxy/api/v1/trace", strings.NewReader(`{"ok":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	assert.NotEqual(http.StatusOK, rr.Code)
	assert.False(reached)
}

func TestKataProxyHiddenFromOpenAPI(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	t.Setenv("KATA_HOME", t.TempDir())
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/openapi.json", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.NotContains(rr.Body.String(), "/kata/proxy")
}

func writeKataProxyCatalog(t *testing.T, home string, body string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600))
}

func writeKataProxyRuntimeRecord(t *testing.T, address string) string {
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
	path := filepath.Join(runtimeDir, fmt.Sprintf("daemon.%d.json", rec.PID))
	require.NoError(t, os.WriteFile(path, body, 0o600))
	return path
}

type poisonDefaultTransport struct{}

func (poisonDefaultTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("poison default transport used")
}

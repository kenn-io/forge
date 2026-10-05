package accesstest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/hostapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

type healthResponse struct {
	Status   string `json:"status"`
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Modified bool   `json:"modified"`
}

func TestSwitchHandlerSwapsDifferentHandlerTypes(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	switcher := hostapi.NewSwitchHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	firstRR := httptest.NewRecorder()
	switcher.ServeHTTP(firstRR, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	require.Equal(t, http.StatusAccepted, firstRR.Code)

	next := http.NewServeMux()
	next.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	switcher.Swap(next)

	secondRR := httptest.NewRecorder()
	switcher.ServeHTTP(secondRR, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	require.Equal(t, http.StatusNoContent, secondRR.Code)
}

func TestStartupHandlerAnswersOnlyLivenessWhileStarting(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	cfg := &config.Config{
		Host:     "127.0.0.1",
		Port:     8091,
		BasePath: "/",
	}
	handler := server.NewStartupHandler(
		cfg,
		server.ServerOptions{},
		serverfake.StaticListener{AddrValue: serverfake.StaticListenerAddr("127.0.0.1:8091")},
		server.BuildInfo{Version: "v1.2.3", Commit: strings.Repeat("a", 40)},
	)
	serve := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		req.Host = "127.0.0.1:8091"
		req.RemoteAddr = "127.0.0.1:1234"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	assert := assert.New(t)
	liveRR := serve("/livez")
	var live healthResponse
	require.NoError(t, json.Unmarshal(liveRR.Body.Bytes(), &live))
	assert.Equal(http.StatusOK, liveRR.Code)
	assert.Equal("v1.2.3", live.Version)
	assert.Len(live.Revision, 40)

	for _, path := range []string{"/", "/assets/index-DEADBEEF.js", "/api/v1/settings", "/mcp", "/healthz"} {
		rr := serve(path)
		assert.Equal(http.StatusServiceUnavailable, rr.Code, path)
		assert.Equal("application/problem+json", rr.Header().Get("Content-Type"), path)
		var problem httpapi.ProblemError
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &problem), path)
		assert.Equal(httpapi.CodeServiceUnavailable, problem.Code, path)
	}
}

func TestStartupHandlerUsesHostValidation(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	cfg := &config.Config{
		Host:     "127.0.0.1",
		Port:     8091,
		BasePath: "/",
	}
	handler := server.NewStartupHandler(
		cfg,
		server.ServerOptions{},
		serverfake.StaticListener{AddrValue: serverfake.StaticListenerAddr("127.0.0.1:8091")},
		server.BuildInfo{Version: "v1.2.3", Commit: strings.Repeat("a", 40)},
	)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Host = "attacker.example:8091"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
}

func TestStartupHandlerHonorsBasePath(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	cfg := &config.Config{
		Host:     "127.0.0.1",
		Port:     8091,
		BasePath: "/kenn-forge/",
	}
	handler := server.NewStartupHandler(
		cfg,
		server.ServerOptions{},
		serverfake.StaticListener{AddrValue: serverfake.StaticListenerAddr("127.0.0.1:8091")},
		server.BuildInfo{Version: "v1.2.3", Commit: strings.Repeat("a", 40)},
	)
	serve := func(path string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		req.Host = "127.0.0.1:8091"
		req.RemoteAddr = "127.0.0.1:1234"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr.Code
	}

	assert := assert.New(t)
	assert.Equal(http.StatusOK, serve("/kenn-forge/livez"))
	assert.Equal(http.StatusOK, serve("/livez"))
	assert.Equal(http.StatusServiceUnavailable, serve("/kenn-forge/"))
	assert.Equal(http.StatusServiceUnavailable, serve("/kenn-forge/mcp"))
	assert.Equal(http.StatusServiceUnavailable, serve("/kenn-forge/api/v1/settings"))
}

func TestStartupHandlerSwapsToFullServerOverHTTP(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port

	cfg := &config.Config{
		Host:           "127.0.0.1",
		Port:           port,
		BasePath:       "/",
		SyncInterval:   "5m",
		GitHubTokenEnv: "KENN_FORGE_GITHUB_TOKEN_UNSET_FOR_STARTUP_TEST",
		Activity: config.Activity{
			ViewMode:  "threaded",
			TimeRange: "7d",
		},
	}

	switcher := hostapi.NewSwitchHandler(server.NewStartupHandler(
		cfg,
		server.ServerOptions{},
		ln,
		server.BuildInfo{},
	))
	httpSrv := &http.Server{Handler: switcher}
	errCh := make(chan error, 1)
	go func() {
		if serveErr := httpSrv.Serve(ln); !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	var fullServer *server.Server
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if fullServer != nil {
			require.NoError(t, fullServer.Shutdown(shutdownCtx))
		} else {
			require.NoError(t, httpSrv.Shutdown(shutdownCtx))
		}
		select {
		case serveErr := <-errCh:
			require.NoError(t, serveErr)
		default:
		}
	})

	client := &http.Client{Timeout: 2 * time.Second}
	baseURL := "http://" + ln.Addr().String()

	rootStatus, _, _ := getHTTPBody(t, client, baseURL+"/")
	assert := assert.New(t)
	assert.Equal(http.StatusServiceUnavailable, rootStatus)

	apiStatus, apiHeader, apiBody := getHTTPBody(
		t, client, baseURL+"/api/v1/sync/status",
	)
	assert.Equal(http.StatusServiceUnavailable, apiStatus)
	assert.Equal("application/problem+json", apiHeader.Get("Content-Type"))
	assert.Contains(apiBody, `"reason":"starting"`)

	database := dbtest.Open(t)
	mock := &serverfake.MockGH{}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, nil, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	fullServer = server.New(
		database, syncer, frontend, "/", cfg,
		server.ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
	fullServer.AttachHTTPServer(httpSrv, ln)
	switcher.Swap(fullServer)

	readyStatus, readyHeader, readyBody := getHTTPBody(
		t, client, baseURL+"/api/v1/sync/status",
	)
	assert.Equal(http.StatusOK, readyStatus)
	assert.True(
		strings.HasPrefix(readyHeader.Get("Content-Type"), "application/json"),
		readyHeader.Get("Content-Type"),
	)
	assert.Contains(readyBody, `"running":`)

	readyRootStatus, _, readyRootBody := getHTTPBody(t, client, baseURL+"/")
	assert.Equal(http.StatusOK, readyRootStatus)
	assert.Contains(readyRootBody, "app")
	assert.Contains(readyRootBody, `window.__BASE_PATH__="/"`)
}

func getHTTPBody(
	t *testing.T,
	client *http.Client,
	url string,
) (int, http.Header, string) {
	t.Helper()
	resp, err := client.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, resp.Header, string(body)
}

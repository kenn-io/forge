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

func TestStartupHandlerServesSPAWhileAPIUnavailable(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
		"assets/index-DEADBEEF.js": &fstest.MapFile{
			Data: []byte(`console.log("bundle");`),
		},
	}
	cfg := &config.Config{
		Host:     "127.0.0.1",
		Port:     8091,
		BasePath: "/",
	}
	handler := server.NewStartupHandler(
		frontend,
		cfg,
		server.ServerOptions{},
		serverfake.StaticListener{AddrValue: serverfake.StaticListenerAddr("127.0.0.1:8091")},
		server.BuildInfo{Version: "v1.2.3", Commit: strings.Repeat("a", 40)},
	)

	rootReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	rootReq.Host = "127.0.0.1:8091"
	rootReq.RemoteAddr = "127.0.0.1:1234"
	rootRR := httptest.NewRecorder()
	handler.ServeHTTP(rootRR, rootReq)

	assert := assert.New(t)
	assert.Equal(http.StatusOK, rootRR.Code)
	assert.Contains(rootRR.Body.String(), `<body>app</body>`)
	assert.Contains(rootRR.Body.String(), `window.__BASE_PATH__="/"`)
	assert.NotContains(rootRR.Body.String(), "kenn-forge is starting")

	liveReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/livez", nil)
	liveReq.Host = "127.0.0.1:8091"
	liveReq.RemoteAddr = "127.0.0.1:1234"
	liveRR := httptest.NewRecorder()
	handler.ServeHTTP(liveRR, liveReq)
	var live healthResponse
	require.NoError(t, json.Unmarshal(liveRR.Body.Bytes(), &live))
	assert.Equal(http.StatusOK, liveRR.Code)
	assert.Equal("v1.2.3", live.Version)
	assert.Len(live.Revision, 40)

	apiReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/settings", nil)
	apiReq.Host = "127.0.0.1:8091"
	apiReq.RemoteAddr = "127.0.0.1:1234"
	apiRR := httptest.NewRecorder()
	handler.ServeHTTP(apiRR, apiReq)

	assert.Equal(http.StatusServiceUnavailable, apiRR.Code)
	assert.Equal("application/problem+json", apiRR.Header().Get("Content-Type"))
	var problem httpapi.ProblemError
	require.NoError(t, json.Unmarshal(apiRR.Body.Bytes(), &problem))
	assert.Equal(httpapi.CodeServiceUnavailable, problem.Code)

	assetReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/assets/index-DEADBEEF.js", nil)
	assetReq.Host = "127.0.0.1:8091"
	assetReq.RemoteAddr = "127.0.0.1:1234"
	assetRR := httptest.NewRecorder()
	handler.ServeHTTP(assetRR, assetReq)

	assert.Equal(http.StatusOK, assetRR.Code)
	assert.Equal("public, max-age=31536000, immutable", assetRR.Header().Get("Cache-Control"))
}

func TestStartupHandlerUsesHostValidation(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}
	cfg := &config.Config{
		Host:     "127.0.0.1",
		Port:     8091,
		BasePath: "/",
	}
	handler := server.NewStartupHandler(
		frontend,
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
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head><script src="/assets/index.js"></script></head><body>app</body></html>`),
		},
	}
	cfg := &config.Config{
		Host:     "127.0.0.1",
		Port:     8091,
		BasePath: "/kenn-forge/",
	}
	handler := server.NewStartupHandler(
		frontend,
		cfg,
		server.ServerOptions{},
		serverfake.StaticListener{AddrValue: serverfake.StaticListenerAddr("127.0.0.1:8091")},
		server.BuildInfo{Version: "v1.2.3", Commit: strings.Repeat("a", 40)},
	)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/kenn-forge/", nil)
	req.Host = "127.0.0.1:8091"
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert := assert.New(t)
	assert.Equal(http.StatusOK, rr.Code)
	assert.Contains(rr.Body.String(), `<body>app</body>`)
	assert.Contains(rr.Body.String(), `window.__BASE_PATH__="/kenn-forge/"`)
	assert.Contains(rr.Body.String(), `src="/kenn-forge/assets/index.js"`)
	assert.NotContains(rr.Body.String(), "kenn-forge is starting")

	healthReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/kenn-forge/healthz", nil)
	healthReq.Host = "127.0.0.1:8091"
	healthReq.RemoteAddr = "127.0.0.1:1234"
	healthRR := httptest.NewRecorder()
	handler.ServeHTTP(healthRR, healthReq)

	assert.Equal(http.StatusServiceUnavailable, healthRR.Code)

	apiReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/kenn-forge/api/v1/settings", nil)
	apiReq.Host = "127.0.0.1:8091"
	apiReq.RemoteAddr = "127.0.0.1:1234"
	apiRR := httptest.NewRecorder()
	handler.ServeHTTP(apiRR, apiReq)

	assert.Equal(http.StatusServiceUnavailable, apiRR.Code)

	bareReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/settings", nil)
	bareReq.Host = "127.0.0.1:8091"
	bareReq.RemoteAddr = "127.0.0.1:1234"
	bareRR := httptest.NewRecorder()
	handler.ServeHTTP(bareRR, bareReq)

	assert.Equal(http.StatusNotFound, bareRR.Code)
}

func TestStartupHandlerSwapsToFullServerOverHTTP(t *testing.T) {
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
		frontend,
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
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
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

	rootStatus, _, rootBody := getHTTPBody(t, client, baseURL+"/")
	assert := assert.New(t)
	assert.Equal(http.StatusOK, rootStatus)
	assert.Contains(rootBody, `<body>app</body>`)
	assert.Contains(rootBody, `window.__BASE_PATH__="/"`)
	assert.NotContains(rootBody, "kenn-forge is starting")

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

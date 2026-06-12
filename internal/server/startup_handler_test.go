package server

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

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestSwitchHandlerSwapsDifferentHandlerTypes(t *testing.T) {
	switcher := NewSwitchHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	firstRR := httptest.NewRecorder()
	switcher.ServeHTTP(firstRR, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusAccepted, firstRR.Code)

	next := http.NewServeMux()
	next.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	switcher.Swap(next)

	secondRR := httptest.NewRecorder()
	switcher.ServeHTTP(secondRR, httptest.NewRequest(http.MethodGet, "/", nil))
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
	handler := NewStartupHandler(
		frontend,
		cfg,
		ServerOptions{},
		staticListener{addr: staticListenerAddr("127.0.0.1:8091")},
	)

	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootReq.Host = "127.0.0.1:8091"
	rootReq.RemoteAddr = "127.0.0.1:1234"
	rootRR := httptest.NewRecorder()
	handler.ServeHTTP(rootRR, rootReq)

	assert := Assert.New(t)
	assert.Equal(http.StatusOK, rootRR.Code)
	assert.Contains(rootRR.Body.String(), `<body>app</body>`)
	assert.Contains(rootRR.Body.String(), `window.__BASE_PATH__="/"`)
	assert.NotContains(rootRR.Body.String(), "middleman is starting")

	apiReq := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	apiReq.Host = "127.0.0.1:8091"
	apiReq.RemoteAddr = "127.0.0.1:1234"
	apiRR := httptest.NewRecorder()
	handler.ServeHTTP(apiRR, apiReq)

	assert.Equal(http.StatusServiceUnavailable, apiRR.Code)
	assert.Equal("application/problem+json", apiRR.Header().Get("Content-Type"))
	var problem ProblemError
	require.NoError(t, json.Unmarshal(apiRR.Body.Bytes(), &problem))
	assert.Equal(CodeServiceUnavailable, problem.Code)

	assetReq := httptest.NewRequest(http.MethodGet, "/assets/index-DEADBEEF.js", nil)
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
	handler := NewStartupHandler(
		frontend,
		cfg,
		ServerOptions{},
		staticListener{addr: staticListenerAddr("127.0.0.1:8091")},
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "attacker.example:8091"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	Assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
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
		BasePath: "/middleman/",
	}
	handler := NewStartupHandler(
		frontend,
		cfg,
		ServerOptions{},
		staticListener{addr: staticListenerAddr("127.0.0.1:8091")},
	)

	req := httptest.NewRequest(http.MethodGet, "/middleman/", nil)
	req.Host = "127.0.0.1:8091"
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert := Assert.New(t)
	assert.Equal(http.StatusOK, rr.Code)
	assert.Contains(rr.Body.String(), `<body>app</body>`)
	assert.Contains(rr.Body.String(), `window.__BASE_PATH__="/middleman/"`)
	assert.Contains(rr.Body.String(), `src="/middleman/assets/index.js"`)
	assert.NotContains(rr.Body.String(), "middleman is starting")

	healthReq := httptest.NewRequest(http.MethodGet, "/middleman/healthz", nil)
	healthReq.Host = "127.0.0.1:8091"
	healthReq.RemoteAddr = "127.0.0.1:1234"
	healthRR := httptest.NewRecorder()
	handler.ServeHTTP(healthRR, healthReq)

	assert.Equal(http.StatusServiceUnavailable, healthRR.Code)

	apiReq := httptest.NewRequest(http.MethodGet, "/middleman/api/v1/settings", nil)
	apiReq.Host = "127.0.0.1:8091"
	apiReq.RemoteAddr = "127.0.0.1:1234"
	apiRR := httptest.NewRecorder()
	handler.ServeHTTP(apiRR, apiReq)

	assert.Equal(http.StatusServiceUnavailable, apiRR.Code)

	bareReq := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port

	cfg := &config.Config{
		Host:           "127.0.0.1",
		Port:           port,
		BasePath:       "/",
		SyncInterval:   "5m",
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN_UNSET_FOR_STARTUP_TEST",
		Activity: config.Activity{
			ViewMode:  "threaded",
			TimeRange: "7d",
		},
	}

	switcher := NewSwitchHandler(NewStartupHandler(
		frontend,
		cfg,
		ServerOptions{},
		ln,
	))
	httpSrv := &http.Server{Handler: switcher}
	errCh := make(chan error, 1)
	go func() {
		if serveErr := httpSrv.Serve(ln); !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	var fullServer *Server
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
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
	assert := Assert.New(t)
	assert.Equal(http.StatusOK, rootStatus)
	assert.Contains(rootBody, `<body>app</body>`)
	assert.Contains(rootBody, `window.__BASE_PATH__="/"`)
	assert.NotContains(rootBody, "middleman is starting")

	apiStatus, apiHeader, apiBody := getHTTPBody(
		t, client, baseURL+"/api/v1/sync/status",
	)
	assert.Equal(http.StatusServiceUnavailable, apiStatus)
	assert.Equal("application/problem+json", apiHeader.Get("Content-Type"))
	assert.Contains(apiBody, `"reason":"starting"`)

	database := dbtest.Open(t)
	mock := &mockGH{}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, nil, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	fullServer = New(
		database, syncer, frontend, "/", cfg,
		ServerOptions{HostCheckAllowLoopbackAnyPort: true},
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

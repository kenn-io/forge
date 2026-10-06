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
	"go.kenn.io/forge/internal/server/authapi"
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

func newStartupSwitch(t *testing.T, basePath string) *hostapi.SwitchHandler {
	t.Helper()
	return hostapi.NewStartupSwitch(server.NewStartupHandler(
		&config.Config{Host: "127.0.0.1", Port: 8091, BasePath: basePath},
		server.ServerOptions{},
		serverfake.StaticListener{AddrValue: serverfake.StaticListenerAddr("127.0.0.1:8091")},
		server.BuildInfo{Version: "v1.2.3", Commit: strings.Repeat("a", 40)},
	))
}

func serveStartup(
	ctx context.Context, handler http.Handler, path, host string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	req.Host = host
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestStartupSwitchAnswersProbes(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	tests := []struct {
		basePath string
		path     string
		live     bool
	}{
		{basePath: "/", path: "/livez", live: true},
		{basePath: "/", path: "/healthz"},
		{basePath: "/kenn-forge/", path: "/kenn-forge/livez", live: true},
		{basePath: "/kenn-forge/", path: "/kenn-forge/healthz"},
		// Base paths that overlap the root probes must not hide them.
		{basePath: "/live/", path: "/livez", live: true},
		{basePath: "/live/", path: "/live/livez", live: true},
		{basePath: "/livez/", path: "/livez", live: true},
		{basePath: "/livez/", path: "/livez/livez", live: true},
	}
	for _, test := range tests {
		t.Run(test.basePath+" "+test.path, func(t *testing.T) {
			rr := serveStartup(t.Context(), newStartupSwitch(t, test.basePath), test.path, "127.0.0.1:8091")
			if test.live {
				require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
				var live healthResponse
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &live))
				assert.Equal(t, "ok", live.Status)
				assert.Equal(t, "v1.2.3", live.Version)
				return
			}
			require.Equal(t, http.StatusServiceUnavailable, rr.Code)
			var problem httpapi.ProblemError
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &problem))
			assert.Equal(t, httpapi.CodeServiceUnavailable, problem.Code)
		})
	}
}

func TestStartupSwitchHoldsOtherRequestsUntilFullServer(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	switcher := newStartupSwitch(t, "/")

	// A request abandoned before readiness gets no startup answer.
	abandoned, cancel := context.WithCancel(t.Context())
	cancel()
	rr := serveStartup(abandoned, switcher, "/mcp", "127.0.0.1:8091")
	assert.Empty(t, rr.Body.String())
	assert.Empty(t, rr.Header())

	held := make(chan *httptest.ResponseRecorder, 1)
	go func() { held <- serveStartup(t.Context(), switcher, "/api/v1/settings", "127.0.0.1:8091") }()
	switcher.Swap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	assert.Equal(t, http.StatusNoContent, (<-held).Code)
	assert.Equal(t, http.StatusNoContent, serveStartup(t.Context(), switcher, "/livez", "127.0.0.1:8091").Code)
}

func TestStartupHandlerUsesHostValidation(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	rr := serveStartup(t.Context(), newStartupSwitch(t, "/"), "/livez", "attacker.example:8091")

	assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
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

	access := authapi.DaemonAccessOptions{Token: "startup-secret", RequireAPIAuth: true}
	switcher := hostapi.NewStartupSwitch(server.NewStartupHandler(
		cfg,
		server.ServerOptions{DaemonAccess: access},
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

	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	baseURL := "http://" + ln.Addr().String()

	liveStatus, _, _ := getHTTPBody(t, client, baseURL+"/livez")
	assert := assert.New(t)
	assert.Equal(http.StatusOK, liveStatus)

	// A tokenized page load during startup waits for the full server, which
	// runs the token-to-cookie bootstrap.
	type response struct {
		resp *http.Response
		err  error
	}
	held := make(chan response, 1)
	go func() {
		bootstrapRequest, err := http.NewRequestWithContext(
			t.Context(), http.MethodGet, baseURL+"/?auth_token=startup-secret", nil,
		)
		if err != nil {
			held <- response{err: err}
			return
		}
		resp, err := client.Do(bootstrapRequest)
		held <- response{resp: resp, err: err}
	}()

	database := dbtest.Open(t)
	mock := &serverfake.MockGH{}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, nil, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	fullServer = server.New(
		database, syncer, frontend, "/", cfg,
		server.ServerOptions{DaemonAccess: access, HostCheckAllowLoopbackAnyPort: true},
	)
	fullServer.AttachHTTPServer(httpSrv, ln)
	switcher.Swap(fullServer)

	bootstrap := <-held
	require.NoError(t, bootstrap.err)
	defer bootstrap.resp.Body.Close()
	assert.Equal(http.StatusSeeOther, bootstrap.resp.StatusCode)
	var authCookie *http.Cookie
	for _, cookie := range bootstrap.resp.Cookies() {
		if cookie.Name == authapi.AuthCookieName {
			authCookie = cookie
		}
	}
	require.NotNil(t, authCookie)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/v1/sync/status", nil)
	require.NoError(t, err)
	request.AddCookie(authCookie)
	ready, err := client.Do(request)
	require.NoError(t, err)
	defer ready.Body.Close()
	assert.Equal(http.StatusOK, ready.StatusCode)
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

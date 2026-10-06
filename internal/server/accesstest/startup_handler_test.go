package accesstest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"testing/synctest"
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
	return newStartupSwitchWithOptions(basePath, server.ServerOptions{}, 0)
}

func newStartupSwitchWithOptions(
	basePath string, options server.ServerOptions, readTimeout time.Duration,
) *hostapi.SwitchHandler {
	return hostapi.NewStartupSwitch(server.NewStartupHandler(
		&config.Config{Host: "127.0.0.1", Port: 8091, BasePath: basePath},
		options,
		serverfake.StaticListener{AddrValue: serverfake.StaticListenerAddr("127.0.0.1:8091")},
		server.BuildInfo{Version: "v1.2.3", Commit: strings.Repeat("a", 40)},
	), readTimeout)
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

func TestStartupSwitchHoldsRequestsUntilFullServer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		switcher := newStartupSwitch(t, "/")

		// A request abandoned before readiness gets no startup answer.
		abandoned, cancel := context.WithCancel(t.Context())
		cancel()
		rr := serveStartup(abandoned, switcher, "/mcp", "127.0.0.1:8091")
		assert.Empty(t, rr.Body.String())
		assert.Empty(t, rr.Header())

		held := make(chan *httptest.ResponseRecorder, 1)
		go func() { held <- serveStartup(t.Context(), switcher, "/api/v1/settings", "127.0.0.1:8091") }()
		synctest.Wait()
		select {
		case <-held:
			require.Fail(t, "request answered before the full server was installed")
		default:
		}

		switcher.Swap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		assert.Equal(t, http.StatusNoContent, (<-held).Code)
		assert.Equal(t, http.StatusNoContent, serveStartup(t.Context(), switcher, "/livez", "127.0.0.1:8091").Code)
	})
}

// A token link opened during startup must reach the full server, which sets
// the auth cookie that later API calls need.
func TestStartupSwitchHandsHeldTokenLinkToFullServer(t *testing.T) {
	access := authapi.DaemonAccessOptions{Token: "startup-secret", RequireAPIAuth: true}
	database := dbtest.Open(t)
	fullServer := server.New(
		database, nil, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(`<body>app</body>`)}}, "/",
		&config.Config{Host: "127.0.0.1", Port: 8091, BasePath: "/"},
		server.ServerOptions{DaemonAccess: access},
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, fullServer) })

	var authCookie *http.Cookie
	synctest.Test(t, func(t *testing.T) {
		switcher := newStartupSwitchWithOptions("/", server.ServerOptions{DaemonAccess: access}, 0)
		held := make(chan *httptest.ResponseRecorder, 1)
		go func() { held <- serveStartup(t.Context(), switcher, "/?auth_token=startup-secret", "127.0.0.1:8091") }()
		synctest.Wait()
		select {
		case <-held:
			require.Fail(t, "token link answered before the full server was installed")
		default:
		}

		switcher.Swap(fullServer)
		bootstrap := <-held
		require.Equal(t, http.StatusSeeOther, bootstrap.Code, bootstrap.Body.String())
		for _, cookie := range bootstrap.Result().Cookies() {
			if cookie.Name == authapi.AuthCookieName {
				authCookie = cookie
			}
		}
	})
	require.NotNil(t, authCookie)

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/sync/status", nil)
	request.Host = "127.0.0.1:8091"
	request.RemoteAddr = "127.0.0.1:1234"
	request.AddCookie(authCookie)
	rr := httptest.NewRecorder()
	fullServer.ServeHTTP(rr, request)
	assert.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
}

// pipeListener serves in-memory connections so synctest controls every wait,
// including the connection's read deadlines.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return serverfake.StaticListenerAddr("127.0.0.1:8091") }

// Waiting for startup must not spend the request's body read budget.
func TestStartupSwitchHeldRequestKeepsBodyReadBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const readTimeout = time.Second
		switcher := newStartupSwitchWithOptions("/", server.ServerOptions{}, readTimeout)
		listener := &pipeListener{conns: make(chan net.Conn, 1), done: make(chan struct{})}
		httpSrv := &http.Server{Handler: switcher, ReadTimeout: readTimeout}
		serverConn, clientConn := net.Pipe()
		listener.conns <- serverConn
		go func() { _ = httpSrv.Serve(listener) }()

		body := bytes.Repeat([]byte("x"), 64<<10)
		statuses := make(chan int, 1)
		go func() {
			request, err := http.NewRequestWithContext(
				t.Context(), http.MethodPost, "http://127.0.0.1:8091/api/v1/upload", bytes.NewReader(body),
			)
			if err == nil {
				err = request.Write(clientConn)
			}
			if err != nil {
				statuses <- 0
				return
			}
			response, err := http.ReadResponse(bufio.NewReader(clientConn), request)
			if err != nil {
				statuses <- 0
				return
			}
			_ = response.Body.Close()
			statuses <- response.StatusCode
		}()
		synctest.Wait()
		time.Sleep(2 * readTimeout)

		received := make(chan int, 1)
		switcher.Swap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				received <- -1
				return
			}
			received <- len(data)
			w.WriteHeader(http.StatusNoContent)
		}))

		assert.Equal(t, len(body), <-received)
		assert.Equal(t, http.StatusNoContent, <-statuses)
		require.NoError(t, clientConn.Close())
		require.NoError(t, httpSrv.Close())
	})
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

	switcher := hostapi.NewStartupSwitch(server.NewStartupHandler(
		cfg,
		server.ServerOptions{},
		ln,
		server.BuildInfo{},
	), 0)
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

	readyStatus, _, readyBody := getHTTPBody(t, client, baseURL+"/api/v1/sync/status")
	assert.Equal(http.StatusOK, readyStatus)
	assert.Contains(readyBody, `"running":`)

	rootStatus, _, rootBody := getHTTPBody(t, client, baseURL+"/")
	assert.Equal(http.StatusOK, rootStatus)
	assert.Contains(rootBody, `window.__BASE_PATH__="/"`)
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

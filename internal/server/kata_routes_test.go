package server

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type kataDaemonWire struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Default bool   `json:"default"`
	Auth    string `json:"auth"`
	Health  string `json:"health"`
	Hint    string `json:"hint,omitempty"`
}

type kataDaemonRosterWire struct {
	Daemons []kataDaemonWire `json:"daemons"`
	Source  string           `json:"source,omitempty"`
}

func TestKataDaemonsEndpointEmptyWhenCatalogAbsent(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	t.Setenv("KATA_HOME", t.TempDir())
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/daemons", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	raw := rr.Body.String()
	var body kataDaemonRosterWire
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Empty(body.Source)
	assert.Empty(body.Daemons)
	assert.Contains(raw, `"daemons":[]`)
	assert.NotContains(raw, `"source"`)
}

func TestKataDaemonsEndpointIgnoresMiddlemanConfigAndLegacyEnvCatalogSources(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var probes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	t.Setenv("KATA_HOME", t.TempDir())
	t.Setenv("KATA_URL", upstream.URL)
	t.Setenv("KATA_TOKEN", "legacy-secret")
	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[daemon]]
name = "middleman-owned"
url = "`+upstream.URL+`"
`, &mockGH{})

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/daemons", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	raw := rr.Body.String()
	var body kataDaemonRosterWire
	require.NoError(json.NewDecoder(bytes.NewReader([]byte(raw))).Decode(&body))
	assert.Empty(body.Source)
	assert.Empty(body.Daemons)
	assert.Zero(probes.Load())
	assert.NotContains(raw, "middleman-owned")
	assert.NotContains(raw, "legacy-secret")
}

func TestKataDaemonsEndpointReportsHealthAndRedactsSecrets(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	connected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v1/instance", r.URL.Path)
		assert.Equal("Bearer prod-secret", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer connected.Close()
	authRequired := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer authRequired.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_PROD_TOKEN", "prod-secret")
	writeKataServerCatalog(t, home, `
active_daemon = "prod"

[[daemon]]
name = "prod"
url = "`+connected.URL+`/secret/path?token=leak"
token_env = "KATA_PROD_TOKEN"

[[daemon]]
name = "work"
url = "`+authRequired.URL+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/daemons", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var body kataDaemonRosterWire
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Daemons, 2)

	assert.Equal("kata catalog", body.Source)
	assert.Equal("prod", body.Daemons[0].ID)
	assert.True(body.Daemons[0].Default)
	assert.Equal("token", body.Daemons[0].Auth)
	assert.Equal("connected", body.Daemons[0].Health)
	assert.NotContains(body.Daemons[0].URL, "secret")
	assert.NotContains(rr.Body.String(), "prod-secret")
	assert.NotContains(rr.Body.String(), "token=leak")

	assert.Equal("work", body.Daemons[1].ID)
	assert.False(body.Daemons[1].Default)
	assert.Equal("none", body.Daemons[1].Auth)
	assert.Equal("auth_required", body.Daemons[1].Health)
}

func TestKataDaemonsEndpointRejectsUnsetTokenEnv(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("MIDDLEMAN_KATA_MISSING_TOKEN", "")
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "work"
url = "https://kata.example.com"
token_env = "MIDDLEMAN_KATA_MISSING_TOKEN"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/daemons", nil)

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeBadRequest, problem.Code)
	assert.Contains(problem.Detail, "token_env")
	assert.Contains(problem.Detail, "MIDDLEMAN_KATA_MISSING_TOKEN")
}

func TestKataDaemonsEndpointRejectsInvalidCatalog(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "work"
url = "https://kata.example.com"

[[daemon]]
name = "work"
local = true
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/daemons", nil)

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeBadRequest, problem.Code)
	assert.Contains(problem.Detail, "duplicate")
	assert.Contains(problem.Detail, "work")
}

func TestKataDaemonsEndpointReportsDownLocalWithHint(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	t.Setenv("KATA_DB", "")
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "local"
local = true
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/daemons", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var body kataDaemonRosterWire
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Daemons, 1)
	assert.True(body.Daemons[0].Default)
	assert.Equal("down", body.Daemons[0].Health)
	assert.Contains(body.Daemons[0].Hint, "kata daemon start")
}

func TestKataDaemonsEndpointRedactsMalformedTargetErrors(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "bad"
url = "http://user:s3cr3t@%zz?token=leak"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/daemons", nil)

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.NotContains(rr.Body.String(), "s3cr3t")
	assert.NotContains(rr.Body.String(), "user:")
	assert.NotContains(rr.Body.String(), "token=leak")
	assert.Contains(rr.Body.String(), "invalid url")
}

func TestKataDaemonsEndpointReportsAuthKindAndMetadata(t *testing.T) {
	assert := Assert.New(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
active_daemon = "home"

[[daemon]]
name = "home"
url = "`+upstream.URL+`"
token = "tok"

[[daemon]]
name = "work"
url = "`+upstream.URL+`"
`)
	srv, _ := setupTestServer(t)

	got := requestKataDaemonsByID(t, srv)

	assert.Equal("token", got["home"].Auth)
	assert.Equal("none", got["work"].Auth)
	assert.True(got["home"].Default)
	assert.False(got["work"].Default)
	assert.Equal(upstream.URL, got["home"].URL)
}

func TestKataDaemonsEndpointReportsUnreachableAsDown(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(err)
	addr := listener.Addr().String()
	require.NoError(listener.Close())

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "gone"
url = "http://`+addr+`"
`)
	srv, _ := setupTestServer(t)

	got := requestKataDaemonsByID(t, srv)

	assert.Equal("down", got["gone"].Health)
}

func TestKataDaemonsEndpointHealthOverUnixSocket(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	t.Setenv("TMPDIR", "/tmp") // Keep Unix socket paths below macOS' length limit.
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "h.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(err)
	upstream := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
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
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "u"
url = "unix://`+socketPath+`"
`)
	srv, _ := setupTestServer(t)

	got := requestKataDaemonsByID(t, srv)

	assert.Equal("connected", got["u"].Health)
}

func TestKataDaemonsEndpointCachesHealthWithinTTL(t *testing.T) {
	assert := Assert.New(t)

	var probes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+upstream.URL+`"
`)
	srv, _ := setupTestServer(t)

	requestKataDaemons(t, srv)
	requestKataDaemons(t, srv)

	assert.Equal(int32(1), probes.Load())
}

func TestKataDaemonsEndpointHealthOverTrailingSlashURL(t *testing.T) {
	assert := Assert.New(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instance" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+upstream.URL+`/"
`)
	srv, _ := setupTestServer(t)

	got := requestKataDaemonsByID(t, srv)

	assert.Equal("connected", got["home"].Health)
}

func TestKataDaemonsEndpointPreservesConfigOrder(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
active_daemon = "alpha"

[[daemon]]
name = "zeta"
url = "`+upstream.URL+`"

[[daemon]]
name = "alpha"
url = "`+upstream.URL+`"

[[daemon]]
name = "mid"
url = "`+upstream.URL+`"
`)
	srv, _ := setupTestServer(t)

	for range 5 {
		list := requestKataDaemons(t, srv)
		require.Len(list, 3)
		assert.Equal("zeta", list[0].ID)
		assert.Equal("alpha", list[1].ID)
		assert.Equal("mid", list[2].ID)
	}
}

func TestKataDaemonsEndpointReportsEffectiveDefaultForLoneDaemon(t *testing.T) {
	assert := Assert.New(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "only"
url = "`+upstream.URL+`"
`)
	srv, _ := setupTestServer(t)

	got := requestKataDaemonsByID(t, srv)

	assert.True(got["only"].Default)
}

func TestKataDaemonsEndpointRedactsDaemonURLCredentials(t *testing.T) {
	assert := Assert.New(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	hostport := upstream.URL[len("http://"):]
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "home"
url = "http://user:s3cr3t@`+hostport+`/kata/pathsecret?access_token=leak"
`)
	srv, _ := setupTestServer(t)

	got := requestKataDaemonsByID(t, srv)["home"].URL

	assert.Equal("http://"+hostport, got)
	for _, secret := range []string{"s3cr3t", "user:", "access_token", "leak", "pathsecret"} {
		assert.NotContains(got, secret)
	}
}

func TestKataDaemonsEndpointDoesNotFollowProbeRedirects(t *testing.T) {
	assert := Assert.New(t)

	var followed atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/instance" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		followed.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+upstream.URL+`"
`)
	srv, _ := setupTestServer(t)

	got := requestKataDaemonsByID(t, srv)

	assert.Equal("down", got["home"].Health)
	assert.False(followed.Load())
}

func TestKataDaemonsEndpointCoalescesConcurrentProbes(t *testing.T) {
	assert := Assert.New(t)

	var probes atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "home"
url = "`+upstream.URL+`"
`)
	srv, _ := setupTestServer(t)

	const n = 5
	start := make(chan struct{})
	statuses := make(chan int, n)
	var launched, done sync.WaitGroup
	launched.Add(n)
	done.Add(n)
	for range n {
		go func() {
			defer done.Done()
			launched.Done()
			<-start
			req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/daemons", nil)
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			statuses <- rr.Code
		}()
	}
	launched.Wait()
	close(start)

	<-entered
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) && probes.Load() == 1 {
		time.Sleep(time.Millisecond)
	}
	got := probes.Load()
	close(release)
	done.Wait()
	close(statuses)

	assert.Equal(int32(1), got)
	for status := range statuses {
		assert.Equal(http.StatusOK, status)
	}
}

func requestKataDaemonsByID(t *testing.T, srv *Server) map[string]kataDaemonWire {
	t.Helper()

	list := requestKataDaemons(t, srv)
	out := make(map[string]kataDaemonWire, len(list))
	for _, daemon := range list {
		out[daemon.ID] = daemon
	}
	return out
}

func requestKataDaemons(t *testing.T, srv *Server) []kataDaemonWire {
	t.Helper()

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/daemons", nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var body kataDaemonRosterWire
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	return body.Daemons
}

func writeKataServerCatalog(t *testing.T, home string, body string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600))
}

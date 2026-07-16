package server

import (
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type trackedKataUnixServer struct {
	target          string
	liveConnections atomic.Int64
	maxConnections  atomic.Int64
}

func startTrackedKataUnixServer(t *testing.T, handler http.Handler) *trackedKataUnixServer {
	t.Helper()
	t.Setenv("TMPDIR", "/tmp") // Keep Unix socket paths below macOS' length limit.
	socketPath := filepath.Join(t.TempDir(), "kata.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	tracked := &trackedKataUnixServer{target: "unix://" + socketPath}
	server := &http.Server{
		Handler: handler,
		ConnState: func(_ net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				live := tracked.liveConnections.Add(1)
				for {
					observed := tracked.maxConnections.Load()
					if live <= observed || tracked.maxConnections.CompareAndSwap(observed, live) {
						break
					}
				}
			case http.StateHijacked, http.StateClosed:
				tracked.liveConnections.Add(-1)
			}
		},
	}
	done := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(done)
	}()
	t.Cleanup(func() {
		require.NoError(t, server.Close())
		<-done
	})
	return tracked
}

func (s *trackedKataUnixServer) requireConnectionsDrained(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		return s.liveConnections.Load() == 0
	}, time.Second, 10*time.Millisecond, "Unix connections remained open: %d", s.liveConnections.Load())
}

func TestKataTaskDetailClosesUnixConnections(t *testing.T) {
	require := require.New(t)

	var started atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	upstream := startTrackedKataUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if started.Add(1) == 2 {
			releaseOnce.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/issues/issue-example-1":
			_, _ = w.Write([]byte(`{"issue":{"uid":"issue-example-1","project_uid":"project-example","project_name":"Example Project","short_id":"task-1","title":"Example task"},"comments":[],"labels":[],"links":[]}`))
		case "/api/v1/projects":
			_, _ = w.Write([]byte(`{"projects":[{"uid":"project-example","name":"Example Project"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "desktop"
url = "`+upstream.target+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/tasks/issue-example-1", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	require.GreaterOrEqual(upstream.maxConnections.Load(), int64(2))
	upstream.requireConnectionsDrained(t)
}

func TestKataProjectMappingsClosesUnixConnections(t *testing.T) {
	require := require.New(t)

	upstream := startTrackedKataUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/projects" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"projects":[{"uid":"project-example","name":"Example Project"}]}`))
	}))

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "desktop"
url = "`+upstream.target+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/project-mappings", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	require.GreaterOrEqual(upstream.maxConnections.Load(), int64(1))
	upstream.requireConnectionsDrained(t)
}

func TestKataProxyRetainsUnixConnectionReuse(t *testing.T) {
	require := require.New(t)

	upstream := startTrackedKataUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instance" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"instance":"example"}`))
	}))

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "desktop"
url = "`+upstream.target+`"
`)
	srv, _ := setupTestServer(t)

	for range 2 {
		rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/proxy/api/v1/instance", nil)
		require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	}

	require.Equal(int64(1), upstream.maxConnections.Load(), "cached proxy should reuse its Unix connection")
	require.Equal(int64(1), upstream.liveConnections.Load(), "cached proxy should keep its Unix connection idle")
}

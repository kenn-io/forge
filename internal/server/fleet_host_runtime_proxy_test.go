package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
)

// TestFleetHostRuntimeSessionProxiesToPeer proves the host-targeted
// host-level runtime session routes (launch, stop, attach-spec) forward to
// the configured HTTP peer's local runtime API with the body intact, so
// host-scoped sessions are reachable across the fleet like worktree and
// workspace sessions.
func TestFleetHostRuntimeSessionProxiesToPeer(t *testing.T) {
	type captured struct {
		method string
		path   string
		body   string
	}
	var got []captured
	peer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			got = append(got, captured{
				method: r.Method,
				path:   r.URL.Path,
				body:   string(body),
			})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"ok":true}`))
		},
	))
	defer peer.Close()

	s := &Server{cfg: &config.Config{
		Fleet: config.Fleet{
			Enabled: true,
			Key:     "hub",
			Peers: []config.FleetPeer{
				{Key: "member", BaseURL: peer.URL},
			},
		},
	}}
	api := newFleetTestAPI()
	s.registerFleetRoutes(api)

	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantMethod string
		wantPath   string
	}{
		{
			name:       "clone project",
			method:     http.MethodPost,
			path:       "/fleet/hosts/member/projects/clone",
			body:       `{"url":"https://example.com/r.git","path":"~/clones/r"}`,
			wantMethod: http.MethodPost,
			wantPath:   "/api/v1/projects/clone",
		},
		{
			name:       "launch host runtime session",
			method:     http.MethodPost,
			path:       "/fleet/hosts/member/runtime/sessions",
			body:       `{"command":["vim"],"session_key":"console:member"}`,
			wantMethod: http.MethodPost,
			wantPath:   "/api/v1/runtime/sessions",
		},
		{
			name:       "stop host runtime session",
			method:     http.MethodDelete,
			path:       "/fleet/hosts/member/runtime/sessions/console%3Amember",
			wantMethod: http.MethodDelete,
			wantPath:   "/api/v1/runtime/sessions/console:member",
		},
		{
			name:       "host runtime session attach spec",
			method:     http.MethodGet,
			path:       "/fleet/hosts/member/runtime/sessions/console%3Amember/attach-spec",
			wantMethod: http.MethodGet,
			wantPath:   "/api/v1/runtime/sessions/console:member/attach-spec",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := Assert.New(t)
			got = nil
			req := httptest.NewRequest(
				tc.method, tc.path, strings.NewReader(tc.body),
			)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			api.Adapter().ServeHTTP(rr, req)

			require.Equal(http.StatusCreated, rr.Code, rr.Body.String())
			require.Len(got, 1, "peer must receive exactly one request")
			assert.Equal(tc.wantMethod, got[0].method)
			assert.Equal(tc.wantPath, got[0].path)
			if tc.body == "" {
				assert.Empty(got[0].body)
			} else {
				assert.JSONEq(tc.body, got[0].body)
			}
		})
	}
}

// TestFleetFilesystemProxiesToPeer proves the host-targeted filesystem
// discovery routes forward to the peer's local filesystem API with the
// query string intact — the browsing client has no access to the remote
// filesystem of its own.
func TestFleetFilesystemProxiesToPeer(t *testing.T) {
	type captured struct {
		path  string
		query string
	}
	var got []captured
	peer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			got = append(got, captured{
				path:  r.URL.Path,
				query: r.URL.RawQuery,
			})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		},
	))
	defer peer.Close()

	s := &Server{cfg: &config.Config{
		Fleet: config.Fleet{
			Enabled: true,
			Key:     "hub",
			Peers: []config.FleetPeer{
				{Key: "member", BaseURL: peer.URL},
			},
		},
	}}
	api := newFleetTestAPI()
	s.registerFleetRoutes(api)

	cases := []struct {
		name      string
		path      string
		wantPath  string
		wantQuery string
	}{
		{
			name:      "complete filesystem path",
			path:      "/fleet/hosts/member/filesystem/complete?path=%2Fsrv%2Fpro",
			wantPath:  "/api/v1/filesystem/complete",
			wantQuery: "path=%2Fsrv%2Fpro",
		},
		{
			name:      "validate filesystem repo",
			path:      "/fleet/hosts/member/filesystem/validate-repo?path=%2Fsrv%2Fapp",
			wantPath:  "/api/v1/filesystem/validate-repo",
			wantQuery: "path=%2Fsrv%2Fapp",
		},
		{
			name:     "list project branches",
			path:     "/fleet/hosts/member/projects/prj_1/branches",
			wantPath: "/api/v1/projects/prj_1/branches",
		},
		{
			name:     "inspect project worktree",
			path:     "/fleet/hosts/member/projects/prj_1/worktrees/wtr_9/inspect",
			wantPath: "/api/v1/projects/prj_1/worktrees/wtr_9/inspect",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := Assert.New(t)
			got = nil
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rr := httptest.NewRecorder()
			api.Adapter().ServeHTTP(rr, req)

			require.Equal(http.StatusOK, rr.Code, rr.Body.String())
			require.Len(got, 1, "peer must receive exactly one request")
			assert.Equal(tc.wantPath, got[0].path)
			assert.Equal(tc.wantQuery, got[0].query)
		})
	}
}

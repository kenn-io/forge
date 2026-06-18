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

// TestFleetWorktreeLifecycleProxiesToPeer proves the host-targeted worktree
// lifecycle and metadata routes (create, from-merge-request, remove,
// session-backend, linked-issues, refresh-stats) forward to the configured
// HTTP peer's local project worktree API with the body intact.
func TestFleetWorktreeLifecycleProxiesToPeer(t *testing.T) {
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
			name:       "create worktree",
			method:     http.MethodPost,
			path:       "/fleet/hosts/member/projects/prj_1/worktrees",
			body:       `{"branch":"feature/x"}`,
			wantMethod: http.MethodPost,
			wantPath:   "/api/v1/projects/prj_1/worktrees",
		},
		{
			name:       "create worktree from merge request",
			method:     http.MethodPost,
			path:       "/fleet/hosts/member/projects/prj_1/worktrees/from-merge-request",
			body:       `{"number":42}`,
			wantMethod: http.MethodPost,
			wantPath:   "/api/v1/projects/prj_1/worktrees/from-merge-request",
		},
		{
			name:       "remove worktree",
			method:     http.MethodPost,
			path:       "/fleet/hosts/member/projects/prj_1/worktrees/wtr_9/delete",
			body:       `{"removeFromDisk":true}`,
			wantMethod: http.MethodPost,
			wantPath:   "/api/v1/projects/prj_1/worktrees/wtr_9/delete",
		},
		{
			name:       "set worktree session backend",
			method:     http.MethodPut,
			path:       "/fleet/hosts/member/projects/prj_1/worktrees/wtr_9/session-backend",
			body:       `{"session_backend":"remoteTmux"}`,
			wantMethod: http.MethodPut,
			wantPath:   "/api/v1/projects/prj_1/worktrees/wtr_9/session-backend",
		},
		{
			name:       "set worktree linked issues",
			method:     http.MethodPut,
			path:       "/fleet/hosts/member/projects/prj_1/worktrees/wtr_9/linked-issues",
			body:       `{"linked_issue_numbers":[7,12]}`,
			wantMethod: http.MethodPut,
			wantPath:   "/api/v1/projects/prj_1/worktrees/wtr_9/linked-issues",
		},
		{
			name:       "refresh worktree stats",
			method:     http.MethodPost,
			path:       "/fleet/hosts/member/projects/prj_1/worktrees/wtr_9/refresh-stats",
			wantMethod: http.MethodPost,
			wantPath:   "/api/v1/projects/prj_1/worktrees/wtr_9/refresh-stats",
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

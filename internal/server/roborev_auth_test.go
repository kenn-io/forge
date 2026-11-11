package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/serverfake"
)

func TestRoborevBindingSurvivesConfigReload(t *testing.T) {
	assert := assert.New(t)
	key := strings.Repeat("ab", 32)
	t.Setenv("KENN_FORGE_ROBOREV_AUTH_KEY", key)
	var calls atomic.Int64
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal("Bearer "+key, r.Header.Get("Authorization"))
		if r.URL.Path == "/api/status" {
			_, _ = io.WriteString(w, `{"version":"bound"}`)
		} else {
			_, _ = io.WriteString(w, `{"repos":[],"total_count":0}`)
		}
	}))
	defer daemon.Close()
	srv, _, _, _ := setupTestServerWithConfigContent(t,
		validReloadConfig+fmt.Sprintf("\n[roborev]\nendpoint=%q\n", daemon.URL), &serverfake.MockGH{})
	client := srv.roborevapi.Client
	t.Setenv("KENN_FORGE_ROBOREV_AUTH_KEY", strings.Repeat("cd", 32))
	srv.cfgPath = filepath.Join(t.TempDir(), "reload.toml")
	writeConfigToml(t, srv.cfgPath, validReloadConfig+"\n[roborev]\nendpoint=\"http://127.0.0.1:1\"\n")
	event := srv.configreload.ApplyConfigChange(t.Context())
	require.True(t, event.Valid, event.Error)
	assert.Same(client, srv.roborevapi.Client)
	for _, path := range []string{
		"/api/v1/roborev/status", "/api/v1/roborev/configured-repositories", "/api/roborev/api/jobs",
	} {
		rr := testutil.DoJSON(t, srv, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		if path == "/api/v1/roborev/status" {
			assert.Contains(rr.Body.String(), `"version":"bound"`)
		}
	}
	assert.Equal(int64(3), calls.Load())
}

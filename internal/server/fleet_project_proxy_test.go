package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
)

// hubServer builds a Server whose fleet self key is "hub" (via
// config), with no peers configured — exercising the self and
// unknown-host branches of the project write dispatch.
func hubServer() *Server {
	cfg := &config.Config{}
	cfg.Fleet.Key = "hub"
	return &Server{cfg: cfg}
}

func TestServeFleetProjectWrite_SelfRoutesToLocalHandler(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	s := hubServer()
	var localPath string
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localPath = r.URL.Path
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"prj_local"}`))
	})

	r := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/hosts/hub/projects",
		strings.NewReader(`{"local_path":"/local/repo"}`))
	r.SetPathValue("host_key", "hub")
	w := httptest.NewRecorder()

	s.serveFleetProjectWrite(w, r, "/api/v1/projects")

	assert.Equal("/api/v1/projects", localPath, "self routes to the local project handler")
	require.Equal(http.StatusCreated, w.Code)
	assert.JSONEq(`{"id":"prj_local"}`, w.Body.String())
}

func TestServeFleetProjectWrite_UnknownHostIs404(t *testing.T) {
	s := hubServer()

	r := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/hosts/spoke/projects",
		strings.NewReader(`{"local_path":"/x"}`))
	r.SetPathValue("host_key", "spoke")
	w := httptest.NewRecorder()

	s.serveFleetProjectWrite(w, r, "/api/v1/projects")

	assert.Equal(t, http.StatusNotFound, w.Code,
		"a host that is neither local nor a configured peer is unreachable")
}

func TestFleetProjectIntakeSelfRoutePersistsProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := assert.New(t)

	srv, database := setupTestServer(t)
	setTestFleetConfig(srv, func(cfg *config.Config) {
		cfg.Fleet.Enabled = true
		cfg.Fleet.Key = "hub"
	})

	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoDir))
	expectedRoot, err := filepath.EvalSymlinks(repoDir)
	require.NoError(err)

	validatePath := "/api/v1/fleet/hosts/hub/filesystem/validate-repo?path=" +
		url.QueryEscape(repoDir)
	resp := httpDo(t, ts, http.MethodGet, validatePath, nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var validation struct {
		IsValid  bool   `json:"is_valid"`
		RootPath string `json:"root_path"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&validation))
	resp.Body.Close()
	require.True(validation.IsValid, "fleet validation should accept the repo")
	assert.Equal(expectedRoot, validation.RootPath)

	registerBody := mustMarshal(t, map[string]any{
		"local_path": validation.RootPath,
	})
	resp = httpDo(
		t, ts, http.MethodPost,
		"/api/v1/fleet/hosts/hub/projects",
		registerBody,
	)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var created struct {
		ID        string `json:"id"`
		LocalPath string `json:"local_path"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&created))
	resp.Body.Close()
	require.NotEmpty(created.ID)
	assert.Equal(expectedRoot, created.LocalPath)

	project, err := database.GetProjectByID(t.Context(), created.ID)
	require.NoError(err)
	assert.Equal(expectedRoot, project.LocalPath)

	resp = httpDo(t, ts, http.MethodGet, "/api/v1/projects", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var listed struct {
		Projects []struct {
			ID        string `json:"id"`
			LocalPath string `json:"local_path"`
		} `json:"projects"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&listed))
	resp.Body.Close()
	require.Len(listed.Projects, 1)
	assert.Equal(created.ID, listed.Projects[0].ID)
	assert.Equal(expectedRoot, listed.Projects[0].LocalPath)
}

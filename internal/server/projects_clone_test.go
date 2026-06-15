package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/procutil"
)

// TestCloneProject covers POST /api/v1/projects/clone: clone a repository
// URL to a local path and register the checkout as a project in one
// operation, owned by the host that stores it.
func TestCloneProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	srv, database := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	source := initLifecycleRouteRepo(t)
	runGit(t, source, "branch", "feat/clone")
	dest := filepath.Join(t.TempDir(), "cloned")

	body := mustMarshal(t, map[string]any{
		"url":  source,
		"path": dest,
	})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects/clone", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var created struct {
		ID        string `json:"id"`
		LocalPath string `json:"local_path"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&created))
	resp.Body.Close()
	require.NotEmpty(created.ID)
	assert.Equal(dest, created.LocalPath)

	// The checkout exists on disk and the project is registered with
	// its root worktree discovered.
	_, err := os.Stat(filepath.Join(dest, ".git"))
	require.NoError(err)
	project, err := database.GetProjectByID(t.Context(), created.ID)
	require.NoError(err)
	assert.Equal(dest, project.LocalPath)
	rows := listWorktreeRows(t, ts, created.ID)
	require.NotEmpty(rows, "discovery registers the root checkout")

	// Cloning onto an existing destination is refused.
	resp = httpDo(t, ts, http.MethodPost, "/api/v1/projects/clone", body)
	assert.Equal(http.StatusConflict, resp.StatusCode)
	resp.Body.Close()
}

// TestCloneProjectBranchAndHomePath covers the branch option and
// home-relative destination expansion: fleet clients send "~/..." paths
// because only the executing host knows its home.
func TestCloneProjectBranchAndHomePath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	source := initLifecycleRouteRepo(t)
	runGit(t, source, "branch", "feat/clone")

	body := mustMarshal(t, map[string]any{
		"url":    source,
		"path":   "~/clones/widget",
		"branch": "feat/clone",
	})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects/clone", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var created struct {
		LocalPath string `json:"local_path"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&created))
	resp.Body.Close()
	assert.Equal(filepath.Join(fakeHome, "clones", "widget"), created.LocalPath)

	out, err := procutil.Command(
		"git", "-C", created.LocalPath,
		"rev-parse", "--abbrev-ref", "HEAD",
	).Output()
	require.NoError(err)
	assert.Equal("feat/clone", string(out[:len(out)-1]))
}

// TestCloneProjectFailureCleansOwnedDestination pins the rollback
// contract: a failed clone removes the destination directory this
// request reserved, so an immediate retry reaches git again instead of
// tripping destinationExists over a leftover partial checkout.
func TestCloneProjectFailureCleansOwnedDestination(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	dest := filepath.Join(t.TempDir(), "clones", "broken")
	body := mustMarshal(t, map[string]any{
		// A file URL that does not exist: git fails after the
		// destination has been reserved.
		"url":  "file://" + filepath.Join(t.TempDir(), "no-such-repo.git"),
		"path": dest,
	})

	for attempt := 1; attempt <= 2; attempt++ {
		resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects/clone", body)
		var problem struct {
			Code string `json:"code"`
		}
		require.NoError(json.NewDecoder(resp.Body).Decode(&problem))
		resp.Body.Close()
		require.Equal(http.StatusBadRequest, resp.StatusCode,
			"attempt %d must fail at git, not at destination reservation", attempt)
		assert.NotEqual("destinationExists", problem.Code,
			"attempt %d must not trip over a leftover partial checkout", attempt)
		_, statErr := os.Stat(dest)
		assert.True(os.IsNotExist(statErr),
			"attempt %d must remove the reserved destination", attempt)
	}
}

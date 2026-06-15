package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

// TestW1SliceAGate is the falsifiable capability gate from the convergence
// plan: it exercises the generic project + worktree registry plus
// launch-target discovery against a path with no `gh` context and an
// unrecognizable remote, and finishes by asserting neutral operation IDs in
// the live OpenAPI document. If this test passes, the W1 milestone is
// unblocked on the Middleman side.
func TestW1SliceAGate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	require := require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoDir))

	// 1) Register a project from a path with no `gh` context and no
	//    parseable remote. The response must include a server-assigned
	//    project_id and must omit platform_identity.
	registerBody := mustMarshal(t, map[string]any{
		"local_path":   repoDir,
		"display_name": "no-remote-repo",
	})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", registerBody)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var registered map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&registered))
	resp.Body.Close()
	projectID, _ := registered["id"].(string)
	require.NotEmpty(projectID)
	assert.True(strings.HasPrefix(projectID, "prj_"))
	assert.NotContains(registered, "platform_identity",
		"platform_identity must be absent when no remote is parseable")
	assert.Equal("no-remote-repo", registered["display_name"])
	assert.NotContains(registered, "host",
		"host column was speculative; the response must not include it")

	// 2) GET /projects must list the registered project.
	resp = httpDo(t, ts, http.MethodGet, "/api/v1/projects", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var listed struct {
		Projects []map[string]any `json:"projects"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&listed))
	resp.Body.Close()
	require.Len(listed.Projects, 1)
	assert.Equal(projectID, listed.Projects[0]["id"])
	assert.NotContains(listed.Projects[0], "platform_identity")

	// 3) GET /projects/{project_id} must round-trip the record with
	//    platform_identity still absent.
	resp = httpDo(t, ts, http.MethodGet, "/api/v1/projects/"+projectID, nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var fetched map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&fetched))
	resp.Body.Close()
	assert.Equal(projectID, fetched["id"])
	assert.NotContains(fetched, "platform_identity")

	// 4) Register a worktree the daemon already created on disk.
	//    Middleman just persists the metadata - the path validity
	//    contract is the daemon's, not Middleman's.
	worktreePath := filepath.Join(t.TempDir(), "wt-feature-x")
	wtBody := mustMarshal(t, map[string]any{
		"branch": "feature-x",
		"path":   worktreePath,
	})
	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", wtBody,
	)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var worktree map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&worktree))
	resp.Body.Close()
	worktreeID, _ := worktree["id"].(string)
	require.NotEmpty(worktreeID)
	assert.True(strings.HasPrefix(worktreeID, "wtr_"))
	assert.Equal(projectID, worktree["project_id"])
	assert.Equal("feature-x", worktree["branch"])
	assert.Equal(worktreePath, worktree["path"])

	// 5) Listing the project's worktrees must return the new record.
	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var wtList struct {
		Worktrees []map[string]any `json:"worktrees"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&wtList))
	resp.Body.Close()
	require.Len(wtList.Worktrees, 2,
		"root checkout row plus the registered worktree")
	registeredRow := worktreeRowByBranch(wtList.Worktrees, "feature-x")
	require.NotNil(registeredRow, "registered worktree is listed")
	assert.Equal(worktreeID, registeredRow["id"])
	assert.NotNil(
		worktreeRowByPathBase(wtList.Worktrees, filepath.Base(repoDir)),
		"the project root checkout has a registry row")

	// 6) Launch-target discovery must include plain_shell with
	//    available: true. Configured-agent presence depends on PATH;
	//    only plain_shell is required.
	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/launch-targets", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var ltList struct {
		LaunchTargets []map[string]any `json:"launch_targets"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&ltList))
	resp.Body.Close()
	require.NotEmpty(ltList.LaunchTargets)

	var plainShell map[string]any
	for _, target := range ltList.LaunchTargets {
		if target["key"] == "plain_shell" {
			plainShell = target
			break
		}
	}
	require.NotNil(plainShell, "plain_shell must be present")
	assert.Equal(true, plainShell["available"])
	assert.Equal("plain_shell", plainShell["kind"])

	// 7) The live OpenAPI document must register the gate's operation
	//    IDs and must not bake PR/MR/issue terms into them - the
	//    generic registry must be a generic registry.
	resp = httpDo(t, ts, http.MethodGet, "/api/v1/openapi.json", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&doc))
	resp.Body.Close()

	expectedOps := map[string]string{
		"POST /projects":                                        "register-project",
		"GET /projects":                                         "list-projects",
		"GET /projects/{project_id}":                            "get-project",
		"POST /projects/{project_id}/worktrees":                 "register-worktree",
		"DELETE /projects/{project_id}/worktrees/{worktree_id}": "delete-worktree",
		"GET /projects/{project_id}/worktrees":                  "list-worktrees",
		"GET /projects/{project_id}/launch-targets":             "list-launch-targets",
	}
	for spec, wantID := range expectedOps {
		method, path, _ := strings.Cut(spec, " ")
		gotPath, ok := doc.Paths[path]
		require.Truef(ok, "OpenAPI doc missing path %q", path)
		gotOp, ok := gotPath[strings.ToLower(method)]
		require.Truef(ok, "OpenAPI doc missing %s on %s", method, path)
		assert.Equalf(wantID, gotOp.OperationID,
			"unexpected operation id for %s %s", method, path)
	}

	// 8) Negative: no operation ID on a generic project route may
	//    contain "pull-request", "issue", or "mr" terms. This is the
	//    "generic, not a PR fork" assertion from the convergence plan.
	for path, methods := range doc.Paths {
		if !strings.HasPrefix(path, "/projects") {
			continue
		}
		for method, op := range methods {
			id := op.OperationID
			for _, banned := range []string{"pull-request", "pullrequest", "pr-", "issue", "mr-"} {
				assert.NotContainsf(id, banned,
					"op id %q on %s %s contains banned term %q",
					id, method, path, banned)
			}
		}
	}
}

func TestRegisterProject_RejectsMissingPath(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := mustMarshal(t, map[string]any{"local_path": ""})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(http.StatusBadRequest, resp.StatusCode)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	assert.Contains(string(payload), "local_path")
}

func TestRegisterProject_PreservesExplicitProviderIdentity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	srv, database := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoDir))

	body := mustMarshal(t, map[string]any{
		"local_path": repoDir,
		"platform_identity": map[string]string{
			"platform":      "gitlab",
			"platform_host": "git.example.com",
			"owner":         "platform",
			"name":          "runner",
		},
	})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	defer resp.Body.Close()

	var registered struct {
		ID               string `json:"id"`
		PlatformIdentity struct {
			Platform     string `json:"platform"`
			PlatformHost string `json:"platform_host"`
			Owner        string `json:"owner"`
			Name         string `json:"name"`
		} `json:"platform_identity"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&registered))
	assert.Equal("gitlab", registered.PlatformIdentity.Platform)
	assert.Equal("git.example.com", registered.PlatformIdentity.PlatformHost)

	project, err := database.GetProjectByID(t.Context(), registered.ID)
	require.NoError(err)
	require.NotNil(project.PlatformIdentity)
	assert.Equal(&db.PlatformIdentity{
		Platform: "gitlab",
		Host:     "git.example.com",
		Owner:    "platform",
		Name:     "runner",
	}, project.PlatformIdentity)
}

func TestRegisterProject_UsesConfiguredProviderForRemoteIdentity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[platforms]]
type = "gitlab"
host = "code.example.com"
`, &mockGH{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "-q")
	runGit(t, repoDir, "remote", "add", "origin", "git@code.example.com:group/subgroup/project.git")

	body := mustMarshal(t, map[string]any{"local_path": repoDir})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	defer resp.Body.Close()

	var registered map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&registered))
	identity, ok := registered["platform_identity"].(map[string]any)
	require.True(ok, "platform_identity must be present")
	assert.Equal("gitlab", identity["platform"])
	assert.Equal("code.example.com", identity["platform_host"])
	assert.Equal("group/subgroup", identity["owner"])
	assert.Equal("project", identity["name"])
}

func TestRegisterProject_UsesDefaultPlatformHostForRemoteIdentity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	srv, _, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
default_platform_host = "ghe.example.com"
host = "127.0.0.1"
port = 8091
`, &mockGH{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "-q")
	runGit(t, repoDir, "remote", "add", "origin", "git@ghe.example.com:acme/widget.git")

	body := mustMarshal(t, map[string]any{"local_path": repoDir})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	defer resp.Body.Close()

	var registered map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&registered))
	identity, ok := registered["platform_identity"].(map[string]any)
	require.True(ok, "platform_identity must be present")
	assert.Equal("github", identity["platform"])
	assert.Equal("ghe.example.com", identity["platform_host"])
	assert.Equal("acme", identity["owner"])
	assert.Equal("widget", identity["name"])
}

func TestRegisterProject_RejectsNonexistentPath(t *testing.T) {
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := mustMarshal(t, map[string]any{
		"local_path": "/this/path/should/never/exist",
	})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

func TestRegisterProject_DuplicatePathReturns409(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoDir))

	body := mustMarshal(t, map[string]any{"local_path": repoDir})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(http.StatusConflict, resp.StatusCode)
	resp.Body.Close()
}

func TestRegisterProject_AcceptsCallerProvidedIdentity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoDir))

	// Even though the repo has no remote, the caller can provide
	// platform_identity directly. Caller-provided wins, and the handler
	// upserts a middleman_repos row to give the project a stable FK
	// target - no sync subscription is created (sync is driven by TOML
	// config, not by middleman_repos rows).
	body := mustMarshal(t, map[string]any{
		"local_path": repoDir,
		"platform_identity": map[string]string{
			"platform":      "github",
			"platform_host": "github.com",
			"owner":         "acme",
			"name":          "widget",
		},
	})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var got map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&got))
	resp.Body.Close()
	identity, _ := got["platform_identity"].(map[string]any)
	require.NotNil(identity)
	assert.Equal("github", identity["platform"])
	assert.Equal("github.com", identity["platform_host"])
	assert.NotContains(identity, "host")
	assert.Equal("acme", identity["owner"])
	assert.Equal("widget", identity["name"])

	// Re-fetching reads the identity off the joined middleman_repos
	// row - confirms the FK linkage is what the response is built from
	// (not a stale duplicate copy on middleman_projects).
	projectID, _ := got["id"].(string)
	require.NotEmpty(projectID)
	resp = httpDo(t, ts, http.MethodGet, "/api/v1/projects/"+projectID, nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	var fetched map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&fetched))
	resp.Body.Close()
	identity2, _ := fetched["platform_identity"].(map[string]any)
	require.NotNil(identity2)
	assert.Equal("github", identity2["platform"])
	assert.Equal("github.com", identity2["platform_host"])
	assert.NotContains(identity2, "host")
	assert.Equal("acme", identity2["owner"])
	assert.Equal("widget", identity2["name"])
}

// TestRegisterProject_DoesNotSubscribeRepoToSync pins the load-bearing
// invariant that registering a project does NOT subscribe the linked
// repo to sync. registerProject calls db.UpsertRepo to give the project
// a stable middleman_repos FK target, but UpsertRepo is pure DDL and
// must not touch the syncer's in-memory tracked-repos list - sync
// subscription is driven exclusively by the user's TOML config and
// SetRepos.
//
// If a future refactor accidentally couples UpsertRepo (or the
// project-registration path) to sync, this test fails and flags the
// regression: an embedder could otherwise quietly add unwanted repos
// to the sync set just by registering a project.
func TestRegisterProject_DoesNotSubscribeRepoToSync(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	srv, database := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	before := srv.syncer.TrackedRepos()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoDir))

	body := mustMarshal(t, map[string]any{
		"local_path": repoDir,
		"platform_identity": map[string]string{
			"platform":      "github",
			"platform_host": "github.com",
			"owner":         "stranger",
			"name":          "not-in-toml",
		},
	})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	after := srv.syncer.TrackedRepos()
	assert.Equal(before, after,
		"registering a project must not change the syncer's tracked-repos set")

	// Sanity check the upsert side-effect: the middleman_repos row must
	// exist (so the project's FK target is real), even though sync is
	// not subscribed. Confirms the equality assertion above is not
	// passing simply because UpsertRepo silently no-op'd.
	var count int
	err := database.ReadDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM middleman_repos
		   WHERE platform_host = ? AND owner = ? AND name = ?`,
		"github.com", "stranger", "not-in-toml",
	).Scan(&count)
	require.NoError(err)
	assert.Equal(1, count,
		"UpsertRepo must persist the middleman_repos FK target row")
}

func TestGetProject_NotFoundReturns404(t *testing.T) {
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet, "/api/v1/projects/prj_nope", nil)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

// TestRegisterWorktree_SamePathSameProjectConverges asserts that re-registering
// a worktree at a path the same project already owns is idempotent rather than a
// conflict: the row keeps its id and refreshes its branch. This lets explicit
// registration converge with a row the background discovery pass created.
func TestRegisterWorktree_SamePathSameProjectConverges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoDir))
	projectID := registerProjectForTest(t, ts, repoDir)

	wtPath := filepath.Join(t.TempDir(), "wt-1")
	firstID := registerWorktreeForTest(t, ts, projectID, "feature-x", wtPath, http.StatusCreated)
	require.NotEmpty(firstID)

	adopted := mustMarshal(t, map[string]any{"branch": "feature-y", "path": wtPath})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", adopted,
	)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var second map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&second))
	resp.Body.Close()
	require.Equal(firstID, second["id"])
	require.Equal("feature-y", second["branch"])
}

// TestSetWorktreeSessionBackendRoute covers the wave-2 write-through target:
// PUT .../session-backend persists the override, the worktree list reflects it,
// and a worktree id under the valid project that does not exist is a 404.
func TestSetWorktreeSessionBackendRoute(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoDir))
	projectID := registerProjectForTest(t, ts, repoDir)
	wtPath := filepath.Join(t.TempDir(), "wt-feat")
	worktreeID := registerWorktreeForTest(
		t, ts, projectID, "feat", wtPath, http.StatusCreated,
	)

	body := mustMarshal(t, map[string]any{"session_backend": "localTmux"})
	resp := httpDo(t, ts, http.MethodPut,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/session-backend",
		body,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var updated map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&updated))
	resp.Body.Close()
	require.Equal("localTmux", updated["session_backend"])

	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var wtList struct {
		Worktrees []map[string]any `json:"worktrees"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&wtList))
	resp.Body.Close()
	require.Len(wtList.Worktrees, 2, "root checkout row plus the worktree")
	featRow := worktreeRowByBranch(wtList.Worktrees, "feat")
	require.NotNil(featRow)
	require.Equal("localTmux", featRow["session_backend"])

	resp = httpDo(t, ts, http.MethodPut,
		"/api/v1/projects/"+projectID+"/worktrees/wtr_missing/session-backend",
		body,
	)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	// A value outside the canonical vocabulary must be rejected, not
	// persisted into worktree rows and fleet snapshots.
	body = mustMarshal(t, map[string]any{"session_backend": "carrierPigeon"})
	resp = httpDo(t, ts, http.MethodPut,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/session-backend",
		body,
	)
	require.Equal(http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()

	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NoError(json.NewDecoder(resp.Body).Decode(&wtList))
	resp.Body.Close()
	require.Len(wtList.Worktrees, 2)
	featRow = worktreeRowByBranch(wtList.Worktrees, "feat")
	require.NotNil(featRow)
	require.Equal("localTmux", featRow["session_backend"],
		"a rejected value must not overwrite the stored override")

	// Explicit null still clears the override.
	body = mustMarshal(t, map[string]any{"session_backend": nil})
	resp = httpDo(t, ts, http.MethodPut,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/session-backend",
		body,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NoError(json.NewDecoder(resp.Body).Decode(&updated))
	resp.Body.Close()
	require.Empty(updated["session_backend"])
}

func TestDeleteWorktreeRoute(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoDir))
	projectID := registerProjectForTest(t, ts, repoDir)
	wtPath := filepath.Join(t.TempDir(), "wt-feat")
	worktreeID := registerWorktreeForTest(
		t, ts, projectID, "feat", wtPath, http.StatusCreated,
	)

	resp := httpDo(t, ts, http.MethodDelete,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID, nil,
	)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var wtList struct {
		Worktrees []map[string]any `json:"worktrees"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&wtList))
	resp.Body.Close()
	require.Len(wtList.Worktrees, 1, "only the root checkout row remains")
	require.Nil(worktreeRowByBranch(wtList.Worktrees, "feat"),
		"the worktree is gone from the project")

	// The owning project survives the worktree delete.
	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID, nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Deleting an unknown worktree id is a 404.
	resp = httpDo(t, ts, http.MethodDelete,
		"/api/v1/projects/"+projectID+"/worktrees/wtr_missing", nil,
	)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

// TestRegisterWorktree_SamePathDifferentProjectReturns409 keeps a genuine
// cross-project path collision a conflict; convergence only applies within the
// owning project.
func TestRegisterWorktree_SamePathDifferentProjectReturns409(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoA := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoA))
	repoB := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoB))
	projectA := registerProjectForTest(t, ts, repoA)
	projectB := registerProjectForTest(t, ts, repoB)

	wtPath := filepath.Join(t.TempDir(), "shared-wt")
	registerWorktreeForTest(t, ts, projectA, "feature-x", wtPath, http.StatusCreated)

	conflict := mustMarshal(t, map[string]any{"branch": "feature-x", "path": wtPath})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectB+"/worktrees", conflict,
	)
	require.Equal(http.StatusConflict, resp.StatusCode)
	resp.Body.Close()
}

// registerProjectForTest registers a project at localPath and returns its id.
func registerProjectForTest(t *testing.T, ts *httptest.Server, localPath string) string {
	t.Helper()
	body := mustMarshal(t, map[string]any{"local_path": localPath})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var registered map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&registered))
	resp.Body.Close()
	id, _ := registered["id"].(string)
	require.NotEmpty(t, id)
	return id
}

// registerWorktreeForTest registers a worktree, asserts the status, and returns
// the worktree id from a successful response (empty otherwise).
func registerWorktreeForTest(
	t *testing.T, ts *httptest.Server, projectID, branch, path string, wantStatus int,
) string {
	t.Helper()
	body := mustMarshal(t, map[string]any{"branch": branch, "path": path})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", body,
	)
	require.Equal(t, wantStatus, resp.StatusCode)
	defer resp.Body.Close()
	if wantStatus != http.StatusCreated {
		return ""
	}
	var created map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	id, _ := created["id"].(string)
	return id
}

func TestListLaunchTargets_NotFoundReturns404(t *testing.T) {
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/prj_nope/launch-targets", nil,
	)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

func TestProjectWorktreeRuntimeShellLifecycle(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var runtimeBody struct {
		LaunchTargets []map[string]any `json:"launch_targets"`
		Sessions      []map[string]any `json:"sessions"`
		ShellSession  *map[string]any  `json:"shell_session,omitempty"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&runtimeBody))
	resp.Body.Close()
	assert.NotEmpty(runtimeBody.LaunchTargets)
	assert.Empty(runtimeBody.Sessions)
	assert.Nil(runtimeBody.ShellSession)

	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/shell", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var shell map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&shell))
	resp.Body.Close()
	shellKey, _ := shell["key"].(string)
	require.NotEmpty(shellKey)
	assert.Equal(projectID, shell["project_id"])
	assert.Equal(worktreeID, shell["worktree_id"])
	assert.Equal("plain_shell", shell["target_key"])
	assert.NotContains(shell, "workspace_id")

	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NoError(json.NewDecoder(resp.Body).Decode(&runtimeBody))
	resp.Body.Close()
	require.NotNil(runtimeBody.ShellSession)
	assert.Equal(shellKey, (*runtimeBody.ShellSession)["key"])

	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+
			"/runtime/sessions/"+shellKey+"/attach-spec",
		nil,
	)
	require.Equal(http.StatusBadRequest, resp.StatusCode)
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	resp.Body.Close()
	assert.Contains(string(payload), "badRequest")
	assert.Contains(string(payload), "not tmux-backed")

	resp = httpDo(t, ts, http.MethodDelete,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/sessions/"+shellKey,
		nil,
	)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()
}

func TestProjectWorktreeRuntimeLaunchTargetLifecycle(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := mustMarshal(t, map[string]any{"target_key": "helper"})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/sessions", body,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var session map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&session))
	resp.Body.Close()
	sessionKey, _ := session["key"].(string)
	require.NotEmpty(sessionKey)
	assert.Equal(projectID, session["project_id"])
	assert.Equal(worktreeID, session["worktree_id"])
	assert.Equal("helper", session["target_key"])
	assert.Equal("agent", session["kind"])
	assert.NotContains(session, "workspace_id")

	// Agent launches are never singletons: a second launch of the same target
	// starts a distinct session. Only plain_shell is reused, via /runtime/shell.
	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/sessions", body,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var second map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&second))
	resp.Body.Close()
	secondKey, _ := second["key"].(string)
	require.NotEmpty(secondKey)
	assert.NotEqual(sessionKey, secondKey)

	resp = httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var runtimeBody struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&runtimeBody))
	resp.Body.Close()
	require.Len(runtimeBody.Sessions, 2)
	listedKeys := make([]string, 0, len(runtimeBody.Sessions))
	for _, s := range runtimeBody.Sessions {
		key, _ := s["key"].(string)
		listedKeys = append(listedKeys, key)
	}
	assert.ElementsMatch([]string{sessionKey, secondKey}, listedKeys)

	for _, key := range []string{sessionKey, secondKey} {
		resp = httpDo(t, ts, http.MethodDelete,
			"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/sessions/"+key,
			nil,
		)
		require.Equal(http.StatusNoContent, resp.StatusCode)
		resp.Body.Close()
	}
}

func TestProjectWorktreeRuntimeRejectsPlainShellOnSessionsRoute(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := mustMarshal(t, map[string]any{"target_key": "plain_shell"})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime/sessions", body,
	)
	require.Equal(http.StatusBadRequest, resp.StatusCode)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	assert.Contains(string(payload), "badRequest")
	assert.Contains(string(payload), "runtime/shell")
}

func TestProjectWorktreeRuntimeRejectsMismatchedProject(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	other := createRuntimeTestProject(t, srv.db, t.TempDir())
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+other.ID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	assert.Contains(string(payload), "notFound")
	assert.NotContains(string(payload), projectID)
}

func TestProjectWorktreeRuntimeAttachSpecUsesStoredTmuxSession(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	tmuxScript := writeProjectRuntimeTmuxProbe(t, "project-runtime-live", 0, "")
	srv.cfg.Tmux.Command = []string{tmuxScript, "--socket", "runtime"}
	sessionKey := worktreeID + "_helper"
	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(
		context.Background(),
		&db.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  sessionKey,
			SessionName: "project-runtime-live",
			TargetKey:   "helper",
			CreatedAt:   time.Now().UTC(),
		},
	))

	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+
			"/runtime/sessions/"+sessionKey+"/attach-spec",
		nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	defer resp.Body.Close()
	var spec runtimeAttachSpecResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&spec))
	assert.Equal(1, spec.Version)
	assert.Equal("tmux", spec.Kind)
	assert.Equal(sessionKey, spec.SessionKey)
	assert.Equal("helper", spec.TargetKey)
	assert.Equal("project-runtime-live", spec.TmuxSession)
	assert.Equal(
		[]string{tmuxScript, "--socket", "runtime", "attach-session", "-t", "project-runtime-live"},
		spec.Command,
	)
	assert.True(spec.RequiresLocalHost)
}

func TestProjectWorktreeRuntimeAttachSpecRejectsMissingTmuxSession(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	srv.cfg.Tmux.Command = []string{
		writeProjectRuntimeTmuxProbe(t, "project-runtime-live", 1, "can't find session"),
	}
	sessionKey := worktreeID + "_helper"
	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(
		context.Background(),
		&db.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  sessionKey,
			SessionName: "project-runtime-live",
			TargetKey:   "helper",
			CreatedAt:   time.Now().UTC(),
		},
	))

	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+
			"/runtime/sessions/"+sessionKey+"/attach-spec",
		nil,
	)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	assert.Contains(string(payload), "notFound")
}

func TestProjectWorktreeRuntimeAttachSpecRejectsNonOwnedSession(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+
			"/runtime/sessions/missing-session/attach-spec",
		nil,
	)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	assert.Contains(string(payload), "notFound")
}

func TestProjectWorktreeRuntimeStopFallsBackToStoredTmuxSession(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, projectID, worktreeID := setupProjectWorktreeRuntimeTest(t)
	record := filepath.Join(t.TempDir(), "tmux-record")
	tmuxScript := writeProjectRuntimeTmuxRecorder(t)
	t.Setenv("TMUX_RECORD", record)
	srv.cfg.Tmux.Command = []string{tmuxScript}

	targetKey := "helper"
	sessionName := "project-runtime-stored"
	sessionKey := worktreeID + "_helper"
	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(
		context.Background(),
		&db.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  sessionKey,
			SessionName: sessionName,
			TargetKey:   targetKey,
			CreatedAt:   time.Now().UTC(),
		},
	))

	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+"/runtime", nil,
	)
	require.Equal(http.StatusOK, resp.StatusCode)
	var runtimeBody struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&runtimeBody))
	resp.Body.Close()
	require.Len(runtimeBody.Sessions, 1)
	assert.Equal(sessionKey, runtimeBody.Sessions[0]["key"])
	assert.Equal(targetKey, runtimeBody.Sessions[0]["target_key"])

	resp = httpDo(t, ts, http.MethodDelete,
		"/api/v1/projects/"+projectID+"/worktrees/"+worktreeID+
			"/runtime/sessions/"+sessionKey,
		nil,
	)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	stored, err := srv.db.ListProjectWorktreeTmuxSessions(
		context.Background(), worktreeID,
	)
	require.NoError(err)
	assert.Empty(stored)
	recorded, err := os.ReadFile(record)
	require.NoError(err)
	assert.Contains(string(recorded), "kill-session -t "+sessionName)
}

func TestProjectWorktreeRuntimeExitForgetsStoredTmuxSession(t *testing.T) {
	require := require.New(t)

	srv, _, worktreeID := setupProjectWorktreeRuntimeTest(t)
	scope := projectWorktreeRuntimeScope(worktreeID)
	targetKey := "helper"
	sessionName := "project-runtime-exited"
	sessionKey := worktreeID + "_helper"
	createdAt := time.Now().UTC()
	require.NoError(srv.db.UpsertProjectWorktreeTmuxSession(
		context.Background(),
		&db.ProjectWorktreeTmuxSession{
			WorktreeID:  worktreeID,
			SessionKey:  sessionKey,
			SessionName: sessionName,
			TargetKey:   targetKey,
			CreatedAt:   createdAt,
		},
	))

	srv.handleRuntimeSessionExit(localruntime.SessionInfo{
		Key:         sessionKey,
		WorkspaceID: scope,
		TargetKey:   targetKey,
		TmuxSession: sessionName,
		CreatedAt:   createdAt,
	})

	require.Eventually(func() bool {
		stored, err := srv.db.ListProjectWorktreeTmuxSessions(
			context.Background(), worktreeID,
		)
		return err == nil && len(stored) == 0
	}, time.Second, 10*time.Millisecond)
}

// TestRegisterProject_RejectsPartialPlatformIdentity pins the contract that
// platform_identity is all-or-nothing. Two paths reject it:
//   - Missing field: Huma's JSON Schema validator returns 422 (the
//     platformIdentityPayload struct fields are non-pointer and
//     non-omitempty, so all three are required).
//   - Whitespace-only field: passes the schema validator but fails the
//     handler's TrimSpace check and returns 400. This is the embedder-
//     facing failure mode for "I sent the field but the value is junk".
func TestRegisterProject_RejectsPartialPlatformIdentity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoDir))

	missingFieldBody := mustMarshal(t, map[string]any{
		"local_path": repoDir,
		"platform_identity": map[string]string{
			"platform":      "github",
			"platform_host": "github.com",
			"owner":         "acme",
			// missing "name" — Huma's schema rejects with 422
		},
	})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", missingFieldBody)
	require.Equal(http.StatusUnprocessableEntity, resp.StatusCode)
	resp.Body.Close()

	whitespaceBody := mustMarshal(t, map[string]any{
		"local_path": repoDir,
		"platform_identity": map[string]string{
			"platform":      "github",
			"platform_host": "github.com",
			"owner":         "acme",
			"name":          "   ",
		},
	})
	resp = httpDo(t, ts, http.MethodPost, "/api/v1/projects", whitespaceBody)
	require.Equal(http.StatusBadRequest, resp.StatusCode)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	assert.Contains(string(payload), "platform_identity")
}

// TestRegisterProject_RejectsPathThatIsAFile guards against an embedder
// passing a regular file as local_path (e.g. a config file or symlink the
// host resolved to the wrong target). The handler must reject before
// recording the row.
func TestRegisterProject_RejectsPathThatIsAFile(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "not-a-dir.txt")
	require.NoError(os.WriteFile(filePath, []byte(""), 0o600))

	body := mustMarshal(t, map[string]any{"local_path": filePath})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(http.StatusBadRequest, resp.StatusCode)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(err)
	assert.Contains(string(payload), "not a directory")
}

// TestRegisterWorktree_RejectsBlankFields covers the required worktree
// fields under both Huma schema validation (missing branch → 422) and the
// handler's checks (missing/whitespace path without create_on_disk and
// whitespace branch → 400). Both contracts are embedder-facing; pinning
// both guards against either layer regressing. Path is schema-optional
// because create_on_disk derives it; without create_on_disk the handler
// still requires it.
func TestRegisterWorktree_RejectsBlankFields(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoDir))

	regBody := mustMarshal(t, map[string]any{"local_path": repoDir})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", regBody)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var registered map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&registered))
	resp.Body.Close()
	projectID, _ := registered["id"].(string)

	cases := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantBody   string
	}{
		{
			name:       "missing branch returns 422 from schema",
			body:       map[string]any{"path": "/tmp/whatever"},
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "missing path without create_on_disk returns 400 from handler",
			body:       map[string]any{"branch": "feature-x"},
			wantStatus: http.StatusBadRequest,
			wantBody:   "path",
		},
		{
			name:       "whitespace branch returns 400 from handler",
			body:       map[string]any{"branch": "   ", "path": "/tmp/whatever"},
			wantStatus: http.StatusBadRequest,
			wantBody:   "branch",
		},
		{
			name:       "whitespace path returns 400 from handler",
			body:       map[string]any{"branch": "feature-x", "path": "   "},
			wantStatus: http.StatusBadRequest,
			wantBody:   "path",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := mustMarshal(t, tc.body)
			resp := httpDo(t, ts, http.MethodPost,
				"/api/v1/projects/"+projectID+"/worktrees", body,
			)
			defer resp.Body.Close()
			require.Equal(tc.wantStatus, resp.StatusCode)
			if tc.wantBody != "" {
				payload, err := io.ReadAll(resp.Body)
				require.NoError(err)
				assert.Contains(string(payload), tc.wantBody)
			}
		})
	}
}

// TestRegisterWorktree_NotFoundReturns404 pins the failure mode an embedder
// hits if the project_id is wrong or the project was deleted between
// register-project and register-worktree.
func TestRegisterWorktree_NotFoundReturns404(t *testing.T) {
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	body := mustMarshal(t, map[string]any{
		"branch": "feature-x",
		"path":   "/tmp/wt-1",
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/prj_nope/worktrees", body,
	)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

// TestListProjects_ReturnsEmptyArrayNotNull pins that the JSON response
// always emits an empty array when no projects are registered. An embedder
// iterating the response with `for (const p of resp.projects)` would crash
// on null but works on []. The Go side initializes the slice non-nil; this
// test catches a regression that lets the empty case marshal to null.
func TestListProjects_ReturnsEmptyArrayNotNull(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := httpDo(t, ts, http.MethodGet, "/api/v1/projects", nil)
	require.Equal(http.StatusOK, resp.StatusCode)
	defer resp.Body.Close()
	var listed map[string]json.RawMessage
	require.NoError(json.NewDecoder(resp.Body).Decode(&listed))
	raw, ok := listed["projects"]
	require.True(ok, "response must include a projects key")
	assert.Equal("[]", string(raw),
		"empty list must serialize as [] for embedder iteration safety")
}

func setupProjectWorktreeRuntimeTest(t *testing.T) (*Server, string, string) {
	t.Helper()
	cfgContent := `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[tmux]
agent_sessions = false

[[agents]]
key = "helper"
label = "Helper"
command = ["/bin/sh", "-c", "sleep 60"]
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgContent), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	// Force tmux unavailable so runtime sessions start through the in-process
	// pty owner deterministically, regardless of whether the test host has
	// tmux installed (a real tmux would otherwise back the plain shell).
	cfg.Tmux.Command = []string{filepath.Join(dir, "missing-tmux")}
	database := dbtest.Open(t)
	mock := &mockGH{}
	clients := map[string]ghclient.Client{"github.com": mock}
	resolved := ghclient.ResolveConfiguredRepos(t.Context(), clients, cfg.Repos)
	syncer := ghclient.NewSyncer(
		clients, database, nil, resolved.Expanded, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		ServerOptions{
			WorktreeDir:                   filepath.Join(dir, "managed-worktrees"),
			PtyOwnerInProcess:             true,
			HostCheckAllowLoopbackAnyPort: true,
		},
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	project := createRuntimeTestProject(t, database, t.TempDir())
	worktreePath := t.TempDir()
	worktree, err := database.CreateProjectWorktree(context.Background(), db.CreateProjectWorktreeInput{
		ProjectID: project.ID,
		Branch:    "runtime",
		Path:      worktreePath,
	})
	require.NoError(t, err)
	return srv, project.ID, worktree.ID
}

func createRuntimeTestProject(t *testing.T, database *db.DB, localPath string) *db.Project {
	t.Helper()
	project, err := database.CreateProject(context.Background(), db.CreateProjectInput{
		DisplayName: "runtime-project",
		LocalPath:   localPath,
	})
	require.NoError(t, err)
	return project
}

func writeProjectRuntimeTmuxRecorder(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\n' "$*" >> "$TMUX_RECORD"` + "\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return script
}

func writeProjectRuntimeTmuxProbe(
	t *testing.T,
	expectedSession string,
	exitCode int,
	stderr string,
) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-tmux")
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--socket\" ]; then shift 2; fi\n" +
		"if [ \"$1\" != \"has-session\" ] || [ \"$2\" != \"-t\" ] || [ \"$3\" != \"" + expectedSession + "\" ]; then\n" +
		"  echo unexpected tmux argv: \"$@\" >&2\n" +
		"  exit 2\n" +
		"fi\n"
	if stderr != "" {
		body += "echo " + shellQuoteTest(stderr) + " >&2\n"
	}
	body += "exit " + strconv.Itoa(exitCode) + "\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return script
}

func shellQuoteTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestInitLocalOnlyGitRepoIgnoresInheritedGitEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	host := t.TempDir()
	initCmd := gitcmd.New().Command(t.Context(), "", "init", "-q", "-b", "main", host)
	require.NoError(initCmd.Run(), "seed host repo")

	hostConfig := filepath.Join(host, ".git", "config")
	before, err := os.ReadFile(hostConfig)
	require.NoError(err)

	target := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(host, ".git"))
	t.Setenv("GIT_WORK_TREE", target)

	require.NoError(initLocalOnlyGitRepo(t.Context(), target))

	after, err := os.ReadFile(hostConfig)
	require.NoError(err)
	assert.Equal(string(before), string(after),
		"git init helper must not write core.worktree to inherited host config")
	assert.FileExists(filepath.Join(target, ".git", "config"))
}

// initLocalOnlyGitRepo runs `git init` in dir without configuring any remote,
// matching the no-`gh` Add Existing path.
func initLocalOnlyGitRepo(ctx context.Context, dir string) error {
	cmd := gitcmd.New().Command(ctx, dir, "init", "-q")
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return out
}

func httpDo(t *testing.T, ts *httptest.Server, method, path string, body []byte) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, ts.URL+path, bodyReader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	} else if method == http.MethodPost || method == http.MethodDelete ||
		method == http.MethodPut || method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	return resp
}

// TestDeleteProjectRouteRemovesProject proves DELETE /api/v1/projects/{id}
// unregisters a project: it returns 204, a follow-up GET reports 404, and a
// second delete reports 404 rather than a misleading success. This is the route
// half of the host write-through for /api/project/remove.
func TestDeleteProjectRouteRemovesProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(t.Context(), repoDir))

	registerBody := mustMarshal(t, map[string]any{
		"local_path":   repoDir,
		"display_name": "doomed",
	})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", registerBody)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var registered struct {
		ID string `json:"id"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&registered))
	resp.Body.Close()
	require.NotEmpty(registered.ID)

	resp = httpDo(t, ts, http.MethodDelete, "/api/v1/projects/"+registered.ID, nil)
	require.Equal(http.StatusNoContent, resp.StatusCode, "delete returns 204 No Content")
	resp.Body.Close()

	resp = httpDo(t, ts, http.MethodGet, "/api/v1/projects/"+registered.ID, nil)
	require.Equal(http.StatusNotFound, resp.StatusCode, "the project is gone after delete")
	resp.Body.Close()

	resp = httpDo(t, ts, http.MethodDelete, "/api/v1/projects/"+registered.ID, nil)
	require.Equal(http.StatusNotFound, resp.StatusCode,
		"deleting an already-gone project is 404, not 500")
	resp.Body.Close()
}

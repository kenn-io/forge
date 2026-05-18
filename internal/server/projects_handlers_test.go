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
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/db"
	"github.com/wesm/middleman/internal/gitenv"
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
	require.NoError(initLocalOnlyGitRepo(repoDir))

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
	require.Len(wtList.Worktrees, 1)
	assert.Equal(worktreeID, wtList.Worktrees[0]["id"])

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
		"POST /projects":                            "register-project",
		"GET /projects":                             "list-projects",
		"GET /projects/{project_id}":                "get-project",
		"POST /projects/{project_id}/worktrees":     "register-worktree",
		"GET /projects/{project_id}/worktrees":      "list-worktrees",
		"GET /projects/{project_id}/launch-targets": "list-launch-targets",
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
	require.NoError(initLocalOnlyGitRepo(repoDir))

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
	require.NoError(initLocalOnlyGitRepo(repoDir))

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
	require.NoError(initLocalOnlyGitRepo(repoDir))

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
	require.NoError(initLocalOnlyGitRepo(repoDir))

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

func TestRegisterWorktree_DuplicatePathReturns409(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repoDir := t.TempDir()
	require.NoError(initLocalOnlyGitRepo(repoDir))

	body := mustMarshal(t, map[string]any{"local_path": repoDir})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var registered map[string]any
	require.NoError(json.NewDecoder(resp.Body).Decode(&registered))
	resp.Body.Close()
	projectID, _ := registered["id"].(string)

	wtPath := filepath.Join(t.TempDir(), "wt-1")
	wtBody := mustMarshal(t, map[string]any{
		"branch": "feature-x",
		"path":   wtPath,
	})
	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", wtBody,
	)
	require.Equal(http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", wtBody,
	)
	require.Equal(http.StatusConflict, resp.StatusCode)
	resp.Body.Close()
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
	require.NoError(initLocalOnlyGitRepo(repoDir))

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

// TestRegisterWorktree_RejectsBlankFields covers the two required worktree
// fields under both Huma schema validation (missing fields → 422) and the
// handler's TrimSpace check (whitespace-only fields → 400). Both contracts
// are embedder-facing; pinning both guards against either layer regressing.
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
	require.NoError(initLocalOnlyGitRepo(repoDir))

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
			name:       "missing path returns 422 from schema",
			body:       map[string]any{"branch": "feature-x"},
			wantStatus: http.StatusUnprocessableEntity,
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

func TestInitLocalOnlyGitRepoIgnoresInheritedGitEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	require := require.New(t)
	assert := Assert.New(t)

	host := t.TempDir()
	initCmd := exec.Command("git", "init", "-q", "-b", "main", host)
	initCmd.Env = gitenv.StripAll(os.Environ())
	require.NoError(initCmd.Run(), "seed host repo")

	hostConfig := filepath.Join(host, ".git", "config")
	before, err := os.ReadFile(hostConfig)
	require.NoError(err)

	target := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(host, ".git"))
	t.Setenv("GIT_WORK_TREE", target)

	require.NoError(initLocalOnlyGitRepo(target))

	after, err := os.ReadFile(hostConfig)
	require.NoError(err)
	assert.Equal(string(before), string(after),
		"git init helper must not write core.worktree to inherited host config")
	assert.FileExists(filepath.Join(target, ".git", "config"))
}

// initLocalOnlyGitRepo runs `git init` in dir without configuring any remote,
// matching the no-`gh` Add Existing path.
func initLocalOnlyGitRepo(dir string) error {
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	cmd.Env = gitenv.StripAll(os.Environ())
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
	}
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	return resp
}

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	Require "github.com/stretchr/testify/require"

	gitenv "go.kenn.io/kit/git/env"
	"go.kenn.io/middleman/internal/procutil"
)

func lifecycleRouteGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := procutil.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitenv.StripAll(os.Environ())
	out, err := cmd.CombinedOutput()
	Require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// initLifecycleRouteRepo creates a git repo with one commit on "main".
func initLifecycleRouteRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := filepath.Join(t.TempDir(), "repo")
	Require.NoError(t, os.MkdirAll(dir, 0o755))
	lifecycleRouteGit(t, dir, "init", "-q", "-b", "main")
	lifecycleRouteGit(t, dir, "config", "user.email", "t@e.st")
	lifecycleRouteGit(t, dir, "config", "user.name", "Tester")
	lifecycleRouteGit(t, dir, "config", "commit.gpgsign", "false")
	lifecycleRouteGit(t, dir, "commit", "--allow-empty", "-m", "initial")
	return dir
}

func decodeProblemCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	Require.NoError(t, json.NewDecoder(resp.Body).Decode(&problem))
	return problem.Code
}

func listWorktreeRows(t *testing.T, ts *httptest.Server, projectID string) []map[string]any {
	t.Helper()
	resp := httpDo(t, ts, http.MethodGet,
		"/api/v1/projects/"+projectID+"/worktrees", nil)
	Require.Equal(t, http.StatusOK, resp.StatusCode)
	defer resp.Body.Close()
	var out struct {
		Worktrees []map[string]any `json:"worktrees"`
	}
	Require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.Worktrees
}

// worktreeRowByBranch returns the first listed row checked out on branch, or
// nil when absent. List order is created_at,id, so rows are selected by
// identity instead of position.
func worktreeRowByBranch(rows []map[string]any, branch string) map[string]any {
	for _, row := range rows {
		if row["branch"] == branch {
			return row
		}
	}
	return nil
}

// worktreeRowByPathBase returns the first listed row whose path ends in base,
// or nil when absent. Root rows are matched this way because the stored
// project path may be symlink-resolved (e.g. a /private prefix on macOS)
// relative to the temp dir the test created.
func worktreeRowByPathBase(rows []map[string]any, base string) map[string]any {
	for _, row := range rows {
		if p, ok := row["path"].(string); ok && filepath.Base(p) == base {
			return row
		}
	}
	return nil
}

// TestWorktreeCreateOnDiskRoute covers the materializing register: with
// create_on_disk the route performs the git work (derived destination, new
// branch) and registers the result, so the response identity matches what
// is on disk.
func TestWorktreeCreateOnDiskRoute(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	projectID := registerProjectForTest(t, ts, repo)

	body := mustMarshal(t, map[string]any{
		"branch":         "feat/api",
		"create_on_disk": true,
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var created struct {
		ID     string `json:"id"`
		Branch string `json:"branch"`
		Path   string `json:"path"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&created))
	resp.Body.Close()

	assert.Equal("feat/api", created.Branch)
	assert.NotEmpty(created.ID)
	info, statErr := os.Stat(created.Path)
	require.NoError(statErr, "worktree directory exists on disk")
	assert.True(info.IsDir())
	assert.Equal("feat/api",
		lifecycleRouteGit(t, created.Path, "rev-parse", "--abbrev-ref", "HEAD"))

	rows := listWorktreeRows(t, ts, projectID)
	require.Len(rows, 2, "root checkout row plus the created worktree")
	linked := worktreeRowByBranch(rows, "feat/api")
	require.NotNil(linked, "created worktree is registered")
	assert.Equal(created.Path, linked["path"])
	root := worktreeRowByPathBase(rows, filepath.Base(repo))
	require.NotNil(root, "the project root checkout has a registry row")
	assert.Equal("main", root["branch"])
}

// TestWorktreeCreateOnDiskBranchInUse covers the distinct problem code for
// attaching a branch that is already checked out (the primary checkout).
func TestWorktreeCreateOnDiskBranchInUse(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	projectID := registerProjectForTest(t, ts, repo)

	body := mustMarshal(t, map[string]any{
		"branch":         "main",
		"create_on_disk": true,
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", body)
	require.Equal(http.StatusConflict, resp.StatusCode)
	assert.Equal("branchInUse", decodeProblemCode(t, resp))
	resp.Body.Close()
	rows := listWorktreeRows(t, ts, projectID)
	require.Len(rows, 1, "failed create registers nothing beyond the root row")
	assert.NotNil(worktreeRowByPathBase(rows, filepath.Base(repo)),
		"the surviving row is the root checkout")
}

// TestWorktreeCreateOnDiskHookFailure covers setup-hook failure: the
// response carries the hookFailed code with script detail, and the git work
// is rolled back so nothing is registered and a retry is possible.
func TestWorktreeCreateOnDiskHookFailure(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	require.NoError(os.WriteFile(
		filepath.Join(repo, "setup.sh"),
		[]byte("#!/bin/sh\necho nope >&2\nexit 7\n"), 0o755,
	))
	projectID := registerProjectForTest(t, ts, repo)

	dest := filepath.Join(t.TempDir(), "wt")
	body := mustMarshal(t, map[string]any{
		"branch":         "feature",
		"path":           dest,
		"create_on_disk": true,
		"setup_script":   "setup.sh",
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", body)
	require.Equal(http.StatusUnprocessableEntity, resp.StatusCode)
	var problem struct {
		Code    string `json:"code"`
		Details struct {
			ExitCode int    `json:"exitCode"`
			Stderr   string `json:"stderr"`
		} `json:"details"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&problem))
	resp.Body.Close()
	assert.Equal("hookFailed", problem.Code)
	assert.Equal(7, problem.Details.ExitCode)
	assert.Contains(problem.Details.Stderr, "nope")

	_, statErr := os.Stat(dest)
	assert.True(os.IsNotExist(statErr), "failed hook rolls the worktree back")
	rows := listWorktreeRows(t, ts, projectID)
	require.Len(rows, 1, "failed hook registers nothing beyond the root row")
	assert.Nil(worktreeRowByBranch(rows, "feature"))
}

// TestWorktreeCreateOnDiskRollsBackWhenRowConflicts: when the git work
// succeeds but the registry insert hits a path conflict, the route rolls
// the git work back so the conflicting state is not made worse.
func TestWorktreeCreateOnDiskRollsBackWhenRowConflicts(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	projectID := registerProjectForTest(t, ts, repo)

	// Another project's registry row holds the path while the directory
	// does not exist, so the on-disk create passes its stat check and
	// fails on insert (same-project path collisions adopt instead).
	otherRepo := initLifecycleRouteRepo(t)
	otherProjectID := registerProjectForTest(t, ts, otherRepo)
	dest := filepath.Join(t.TempDir(), "wt")
	registerWorktreeForTest(
		t, ts, otherProjectID, "other", dest, http.StatusCreated)

	body := mustMarshal(t, map[string]any{
		"branch":         "feature",
		"path":           dest,
		"create_on_disk": true,
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", body)
	require.Equal(http.StatusConflict, resp.StatusCode)
	assert.Equal("destinationExists", decodeProblemCode(t, resp))
	resp.Body.Close()

	_, statErr := os.Stat(dest)
	assert.True(os.IsNotExist(statErr),
		"git work is rolled back when the registry insert conflicts")
}

// TestWorktreeDeleteFromDiskRoute covers the materializing delete: worktree
// directory removed, branch deleted, registry row dropped.
func TestWorktreeDeleteFromDiskRoute(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	projectID := registerProjectForTest(t, ts, repo)

	body := mustMarshal(t, map[string]any{
		"branch":         "feature",
		"create_on_disk": true,
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var created struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&created))
	resp.Body.Close()

	deleteBody := mustMarshal(t, map[string]any{
		"remove_from_disk": true,
		"remove_branch":    true,
	})
	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+created.ID+"/delete",
		deleteBody)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	_, statErr := os.Stat(created.Path)
	assert.True(os.IsNotExist(statErr))
	cmd := procutil.Command(
		"git", "show-ref", "--verify", "--quiet", "refs/heads/feature")
	cmd.Dir = repo
	cmd.Env = gitenv.StripAll(os.Environ())
	require.Error(cmd.Run(), "branch deleted with remove_branch")
	rows := listWorktreeRows(t, ts, projectID)
	require.Len(rows, 1, "only the root checkout row remains")
	assert.Nil(worktreeRowByBranch(rows, "feature"),
		"the deleted worktree row is gone")
}

// TestWorktreeDeleteFromDiskRefusesDirtyWithoutForce: dirty worktrees are
// kept (registry row and disk both intact) unless force is set.
func TestWorktreeDeleteFromDiskRefusesDirtyWithoutForce(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	projectID := registerProjectForTest(t, ts, repo)

	body := mustMarshal(t, map[string]any{
		"branch":         "feature",
		"create_on_disk": true,
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var created struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&created))
	resp.Body.Close()

	require.NoError(os.WriteFile(
		filepath.Join(created.Path, "scratch.txt"), []byte("x\n"), 0o644))

	deleteBody := mustMarshal(t, map[string]any{"remove_from_disk": true})
	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+created.ID+"/delete",
		deleteBody)
	require.Equal(http.StatusConflict, resp.StatusCode)
	assert.Equal("worktreeDirty", decodeProblemCode(t, resp))
	resp.Body.Close()
	_, statErr := os.Stat(created.Path)
	require.NoError(statErr, "dirty worktree kept without force")
	rows := listWorktreeRows(t, ts, projectID)
	require.Len(rows, 2, "root checkout row plus the kept worktree")
	require.NotNil(worktreeRowByBranch(rows, "feature"),
		"dirty worktree row kept without force")

	forceBody := mustMarshal(t, map[string]any{
		"remove_from_disk": true, "force": true,
	})
	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+created.ID+"/delete",
		forceBody)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()
	_, statErr = os.Stat(created.Path)
	assert.True(os.IsNotExist(statErr), "force removes the dirty worktree")
	rows = listWorktreeRows(t, ts, projectID)
	assert.Len(rows, 1, "only the root checkout row remains")
	assert.Nil(worktreeRowByBranch(rows, "feature"))
}

// TestWorktreeDeleteFromDiskRefusesDefaultBranch: a worktree on the
// project's default branch is protected from disk-removing deletes.
func TestWorktreeDeleteFromDiskRefusesDefaultBranch(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	registerBody := mustMarshal(t, map[string]any{
		"local_path":     repo,
		"default_branch": "main",
	})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", registerBody)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var project struct {
		ID string `json:"id"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&project))
	resp.Body.Close()

	// Protection fires before any disk work, so the row's path need not
	// exist.
	worktreeID := registerWorktreeForTest(
		t, ts, project.ID, "main",
		filepath.Join(t.TempDir(), "missing"), http.StatusCreated)

	deleteBody := mustMarshal(t, map[string]any{"remove_from_disk": true})
	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+project.ID+"/worktrees/"+worktreeID+"/delete",
		deleteBody)
	require.Equal(http.StatusConflict, resp.StatusCode)
	assert.Equal("branchProtected", decodeProblemCode(t, resp))
	resp.Body.Close()
	rows := listWorktreeRows(t, ts, project.ID)
	require.Len(rows, 2, "root checkout row plus the protected worktree")
	require.NotNil(worktreeRowByPathBase(rows, "missing"),
		"protected worktree row kept")
}

// TestWorktreeDeleteFromDiskRunsTeardownHook: the teardown script runs in
// the worktree before removal; its failure aborts the delete entirely.
func TestWorktreeDeleteFromDiskRunsTeardownHook(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	outFile := filepath.Join(t.TempDir(), "teardown.out")
	require.NoError(os.WriteFile(
		filepath.Join(repo, "teardown.sh"),
		[]byte("#!/bin/sh\necho \"$MIDDLEMAN_BRANCH\" > "+outFile+"\n"),
		0o755,
	))
	projectID := registerProjectForTest(t, ts, repo)

	body := mustMarshal(t, map[string]any{
		"branch":         "feature",
		"create_on_disk": true,
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&created))
	resp.Body.Close()

	deleteBody := mustMarshal(t, map[string]any{
		"remove_from_disk": true,
		"teardown_script":  "teardown.sh",
	})
	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+created.ID+"/delete",
		deleteBody)
	require.Equal(http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	recorded, err := os.ReadFile(outFile)
	require.NoError(err, "teardown hook ran before removal")
	assert.Equal("feature", strings.TrimSpace(string(recorded)))
}

// TestWorktreeDeleteFromDiskAbortsOnTeardownFailure: a failing teardown
// keeps both the disk worktree and the registry row.
func TestWorktreeDeleteFromDiskAbortsOnTeardownFailure(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	require.NoError(os.WriteFile(
		filepath.Join(repo, "teardown.sh"),
		[]byte("#!/bin/sh\nexit 9\n"), 0o755,
	))
	projectID := registerProjectForTest(t, ts, repo)

	body := mustMarshal(t, map[string]any{
		"branch":         "feature",
		"create_on_disk": true,
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var created struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&created))
	resp.Body.Close()

	deleteBody := mustMarshal(t, map[string]any{
		"remove_from_disk": true,
		"teardown_script":  "teardown.sh",
	})
	resp = httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/"+created.ID+"/delete",
		deleteBody)
	require.Equal(http.StatusUnprocessableEntity, resp.StatusCode)
	assert.Equal("hookFailed", decodeProblemCode(t, resp))
	resp.Body.Close()

	_, statErr := os.Stat(created.Path)
	require.NoError(statErr, "failed teardown keeps the worktree on disk")
	rows := listWorktreeRows(t, ts, projectID)
	require.Len(rows, 2, "root checkout row plus the kept worktree")
	require.NotNil(worktreeRowByBranch(rows, "feature"),
		"failed teardown keeps the registry row")
}

// TestWorktreeRegisterWithoutCreateOnDiskUnchanged pins the legacy
// registry-only contract: path is required and no git work happens.
func TestWorktreeRegisterWithoutCreateOnDiskUnchanged(t *testing.T) {
	require := Require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	projectID := registerProjectForTest(t, ts, repo)

	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees",
		mustMarshal(t, map[string]any{"branch": "feature"}))
	require.Equal(http.StatusBadRequest, resp.StatusCode,
		"registry-only register still requires a path")
	resp.Body.Close()

	dest := filepath.Join(t.TempDir(), "never-created")
	registerWorktreeForTest(t, ts, projectID, "feature", dest, http.StatusCreated)
	_, statErr := os.Stat(dest)
	require.True(os.IsNotExist(statErr),
		"registry-only register performs no git work")
}

// TestWorktreeCreateOnDiskSameBranchConcurrent races two create_on_disk
// requests for the same new branch on one project and pins the API
// contract under concurrent submission: exactly one 201, the loser a
// clean conflict, and no torn registry state. The serialization itself
// comes from git's own branch/worktree collision atomicity (there is no
// in-process lock on this path), so the race is best-effort — a start
// barrier maximizes overlap, but a sequential interleaving also
// satisfies (and must satisfy) the same contract.
func TestWorktreeCreateOnDiskSameBranchConcurrent(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	projectID := registerProjectForTest(t, ts, repo)

	body := mustMarshal(t, map[string]any{
		"branch":         "feat/race",
		"create_on_disk": true,
	})

	// Workers avoid t/require: a failed assertion inside a goroutine
	// exits it via Goexit before the channel send, deadlocking the
	// parent. Errors travel through the outcome instead.
	type outcome struct {
		status int
		code   string
		err    error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			req, err := http.NewRequest(http.MethodPost,
				ts.URL+"/api/v1/projects/"+projectID+"/worktrees",
				bytes.NewReader(body))
			if err != nil {
				results <- outcome{err: err}
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := ts.Client().Do(req)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			out := outcome{status: resp.StatusCode}
			if resp.StatusCode != http.StatusCreated {
				var problem struct {
					Code string `json:"code"`
				}
				_ = json.NewDecoder(resp.Body).Decode(&problem)
				out.code = problem.Code
			}
			resp.Body.Close()
			results <- out
		}()
	}
	ready.Wait()
	close(start)
	first := <-results
	second := <-results
	require.NoError(first.err)
	require.NoError(second.err)

	statuses := []int{first.status, second.status}
	assert.Contains(statuses, http.StatusCreated,
		"exactly one create must win")
	assert.Contains(statuses, http.StatusConflict,
		"the loser must get a clean conflict")
	for _, out := range []outcome{first, second} {
		if out.status == http.StatusConflict {
			assert.Contains(
				[]string{"branchInUse", "branchConflict", "destinationExists"},
				out.code,
			)
		}
	}

	rows := listWorktreeRows(t, ts, projectID)
	require.Len(rows, 2, "root row plus exactly one created worktree")
	assert.NotNil(worktreeRowByBranch(rows, "feat/race"))
}

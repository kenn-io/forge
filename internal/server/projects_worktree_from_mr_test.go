package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	Assert "github.com/stretchr/testify/assert"
	Require "github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
)

// registerIdentifiedProject registers localPath as a project carrying the
// acme/widget GitHub identity and returns its id.
func registerIdentifiedProject(
	t *testing.T, ts *httptest.Server, localPath string,
) string {
	t.Helper()
	body := mustMarshal(t, map[string]any{
		"local_path": localPath,
		"platform_identity": map[string]any{
			"platform":      "github",
			"platform_host": "github.com",
			"owner":         "acme",
			"name":          "widget",
		},
	})
	resp := httpDo(t, ts, http.MethodPost, "/api/v1/projects", body)
	Require.Equal(t, http.StatusCreated, resp.StatusCode)
	defer resp.Body.Close()
	var registered struct {
		ID string `json:"id"`
	}
	Require.NoError(t, json.NewDecoder(resp.Body).Decode(&registered))
	return registered.ID
}

func seedMergeRequest(
	t *testing.T, database *db.DB, number int, headBranch, cloneURL string,
) {
	t.Helper()
	ctx := t.Context()
	repoID, err := database.UpsertRepo(
		ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	Require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:           repoID,
		PlatformID:       int64(90000 + number),
		Number:           number,
		URL:              "https://github.com/acme/widget/pull/42",
		Title:            "Add feature",
		Author:           "octocat",
		State:            "open",
		IsDraft:          true,
		HeadBranch:       headBranch,
		BaseBranch:       "main",
		HeadRepoCloneURL: cloneURL,
		CreatedAt:        now,
		UpdatedAt:        now,
		LastActivityAt:   now,
	})
	Require.NoError(t, err)
}

// TestCreateWorktreeFromMergeRequestRoute covers the happy path for a
// same-repo merge request: the head branch is fetched from the project's
// origin, materialized as a new worktree, and registered.
func TestCreateWorktreeFromMergeRequestRoute(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	srv, database := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// origin carries the merge request head branch; clone is the project.
	origin := initLifecycleRouteRepo(t)
	clone := filepath.Join(t.TempDir(), "clone")
	lifecycleRouteGit(t, filepath.Dir(origin), "clone", "-q", origin, clone)
	lifecycleRouteGit(t, origin, "checkout", "-q", "-b", "feature-x")
	lifecycleRouteGit(t, origin, "commit", "--allow-empty", "-m", "pr work")
	headSHA := lifecycleRouteGit(t, origin, "rev-parse", "feature-x")
	lifecycleRouteGit(t, origin, "checkout", "-q", "main")

	projectID := registerIdentifiedProject(t, ts, clone)
	// The MR head repo is the project repo itself (same-repo scenario).
	seedMergeRequest(t, database, 42, "feature-x",
		"https://github.com/acme/widget.git")

	dest := filepath.Join(t.TempDir(), "wt")
	body := mustMarshal(t, map[string]any{
		"number": 42,
		"branch": "pr-42",
		"path":   dest,
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/from-merge-request", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var created struct {
		ID           string `json:"id"`
		Branch       string `json:"branch"`
		Path         string `json:"path"`
		MergeRequest struct {
			Number  int    `json:"number"`
			URL     string `json:"url"`
			State   string `json:"state"`
			Title   string `json:"title"`
			IsDraft bool   `json:"is_draft"`
		} `json:"merge_request"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&created))
	resp.Body.Close()

	assert.Equal("pr-42", created.Branch)
	assert.Equal(dest, created.Path)
	assert.Equal(42, created.MergeRequest.Number,
		"the response echoes which merge request was materialized")
	assert.Equal("https://github.com/acme/widget/pull/42",
		created.MergeRequest.URL)
	assert.Equal("open", created.MergeRequest.State)
	assert.Equal("Add feature", created.MergeRequest.Title)
	assert.True(created.MergeRequest.IsDraft,
		"draft state must survive the import response so callers"+
			" can fold it into their own state vocabulary")
	assert.Equal(headSHA,
		lifecycleRouteGit(t, dest, "rev-parse", "HEAD"),
		"worktree starts at the merge request head")
	rows := listWorktreeRows(t, ts, projectID)
	require.Len(rows, 2, "root checkout row plus the imported worktree")
	require.NotNil(worktreeRowByBranch(rows, "pr-42"),
		"imported worktree is registered")
}

// TestCreateWorktreeFromMergeRequestRouteUnknownNumber: an unsynced merge
// request is a 404 with the pullNotFound code, and nothing touches disk.
func TestCreateWorktreeFromMergeRequestRouteUnknownNumber(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	projectID := registerIdentifiedProject(t, ts, repo)

	dest := filepath.Join(t.TempDir(), "wt")
	body := mustMarshal(t, map[string]any{
		"number": 99,
		"branch": "pr-99",
		"path":   dest,
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/from-merge-request", body)
	require.Equal(http.StatusNotFound, resp.StatusCode)
	assert.Equal("pullNotFound", decodeProblemCode(t, resp))
	resp.Body.Close()
	_, statErr := os.Stat(dest)
	assert.True(os.IsNotExist(statErr))
}

// TestCreateWorktreeFromMergeRequestRouteNoIdentity: a local-only project
// cannot resolve merge requests.
func TestCreateWorktreeFromMergeRequestRouteNoIdentity(t *testing.T) {
	require := Require.New(t)

	srv, _ := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	repo := initLifecycleRouteRepo(t)
	projectID := registerProjectForTest(t, ts, repo)

	body := mustMarshal(t, map[string]any{
		"number": 1,
		"branch": "pr-1",
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/from-merge-request", body)
	require.Equal(http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// TestCreateWorktreeFromMergeRequestRouteSyncsOnDemand: a merge request
// the registry has not synced yet is fetched from the provider during
// import instead of failing with pullNotFound, so a caller (e.g. a fleet
// hub proxying into this host) does not need a separate sync step.
func TestCreateWorktreeFromMergeRequestRouteSyncsOnDemand(t *testing.T) {
	require := Require.New(t)
	assert := Assert.New(t)

	origin := initLifecycleRouteRepo(t)
	clone := filepath.Join(t.TempDir(), "clone")
	lifecycleRouteGit(t, filepath.Dir(origin), "clone", "-q", origin, clone)
	lifecycleRouteGit(t, origin, "checkout", "-q", "-b", "feature-y")
	lifecycleRouteGit(t, origin, "commit", "--allow-empty", "-m", "pr work")
	headSHA := lifecycleRouteGit(t, origin, "rev-parse", "feature-y")
	lifecycleRouteGit(t, origin, "checkout", "-q", "main")

	now := time.Now()
	mock := &mockGH{
		getPullRequestFn: func(
			_ context.Context, _, _ string, number int,
		) (*gh.PullRequest, error) {
			require.Equal(43, number)
			prID := int64(9043)
			nodeID := "PR_kwDO9043"
			title := "on-demand sync"
			state := "open"
			url := "https://github.com/acme/widget/pull/43"
			author := "ada"
			headRef := "feature-y"
			baseRef := "main"
			cloneURL := "https://github.com/acme/widget.git"
			fullName := "acme/widget"
			return &gh.PullRequest{
				ID:        &prID,
				NodeID:    &nodeID,
				Number:    &number,
				HTMLURL:   &url,
				Title:     &title,
				State:     &state,
				User:      &gh.User{Login: &author},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
					Repo: &gh.Repository{
						CloneURL: &cloneURL,
						FullName: &fullName,
					},
				},
				Base: &gh.PullRequestBranch{Ref: &baseRef},
			}, nil
		},
	}
	srv, _ := setupTestServerWithMock(t, mock)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	projectID := registerIdentifiedProject(t, ts, clone)

	dest := filepath.Join(t.TempDir(), "wt")
	body := mustMarshal(t, map[string]any{
		"number": 43,
		"branch": "pr-43",
		"path":   dest,
	})
	resp := httpDo(t, ts, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees/from-merge-request", body)
	require.Equal(http.StatusCreated, resp.StatusCode)
	var created struct {
		Branch string `json:"branch"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&created))
	resp.Body.Close()
	assert.Equal("pr-43", created.Branch)
	assert.Equal(headSHA,
		lifecycleRouteGit(t, dest, "rev-parse", "HEAD"),
		"worktree starts at the merge request head")
}

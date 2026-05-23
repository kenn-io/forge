package server

import (
	"bufio"
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
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
)

func TestWorkspacePushedHeadObserverE2ERefreshesPRDetailAndSSE(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := openTestDB(t)
	var newHead string
	newTitle := "Updated after push"
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, owner, name string, number int) (*gh.PullRequest, error) {
			require.Equal("acme", owner)
			require.Equal("widget", name)
			require.Equal(1, number)
			state := "open"
			body := "updated body"
			url := "https://github.com/acme/widget/pull/1"
			now := gh.Timestamp{Time: time.Date(2026, 5, 20, 14, 15, 0, 0, time.UTC)}
			return &gh.PullRequest{
				ID:        new(int64(1001)),
				Number:    new(1),
				Title:     &newTitle,
				Body:      &body,
				State:     &state,
				HTMLURL:   &url,
				User:      &gh.User{Login: new("octocat")},
				CreatedAt: &now,
				UpdatedAt: &now,
				Head:      &gh.PullRequestBranch{Ref: new("feature"), SHA: &newHead},
				Base:      &gh.PullRequestBranch{Ref: new("main"), SHA: new("base-sha")},
			}, nil
		},
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		[]ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{WorktreeDir: t.TempDir()})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	worktreePath, oldHead := setupPushedHeadE2EWorktree(t)
	repoID := seedPushedHeadE2ERepoAndPR(t, database, oldHead)
	insertPushedHeadE2EWorkspace(t, database, worktreePath)
	newHead = pushNewPushedHeadE2ECommit(t, worktreePath)

	httpServer := httptest.NewServer(srv)
	defer httpServer.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/v1/events", nil)
	require.NoError(err)
	resp, err := httpServer.Client().Do(req)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	scanner := bufio.NewScanner(resp.Body)

	srv.runWorkspacePushedHeadObserverPass(ctx)
	changed := readSSEFrameWithin(t, scanner, 5*time.Second, nil)
	queued := readSSEFrameWithin(t, scanner, 5*time.Second, nil)
	refreshed := readSSEFrameWithin(t, scanner, 5*time.Second, nil)
	assert.Equal("workspace_pushed_head_changed", changed.Event)
	assert.Equal("workspace_pr_refresh_queued", queued.Event)
	assert.Equal("pr_detail_refreshed", refreshed.Event)

	var changedPayload workspacePushedHeadChangedPayload
	require.NoError(json.Unmarshal([]byte(changed.Data), &changedPayload))
	assert.Equal("ws-pr", changedPayload.WorkspaceID)
	assert.Equal("github", changedPayload.Provider)
	assert.Equal("github.com", changedPayload.PlatformHost)
	assert.Equal("acme/widget", changedPayload.RepoPath)
	assert.Equal(oldHead, changedPayload.OldSHA)
	assert.Equal(newHead, changedPayload.NewSHA)

	pullResp, err := client.HTTP.GetPullWithResponse(ctx, "gh", "acme", "widget", 1)
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode())
	require.NotNil(pullResp.JSON200)
	assert.Equal(newTitle, pullResp.JSON200.MergeRequest.Title)
	assert.True(pullResp.JSON200.DetailLoaded)

	stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal(newHead, stored.PlatformHeadSHA)
}

func seedPushedHeadE2ERepoAndPR(t *testing.T, database *db.DB, oldHead string) int64 {
	t.Helper()
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      1001,
		Number:          1,
		URL:             "https://github.com/acme/widget/pull/1",
		Title:           "Old title",
		Author:          "octocat",
		State:           db.MergeRequestStateOpen,
		Body:            "old body",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: oldHead,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(t, err)
	return repoID
}

func setupPushedHeadE2EWorktree(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	worktree := filepath.Join(dir, "worktree")
	runGit(t, dir, "init", "--bare", "--initial-branch=main", remote)
	runGit(t, dir, "clone", remote, worktree)
	runGit(t, worktree, "config", "user.email", "test@test.com")
	runGit(t, worktree, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, worktree, "add", ".")
	runGit(t, worktree, "commit", "-m", "base commit")
	runGit(t, worktree, "push", "origin", "main")
	runGit(t, worktree, "checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("first\n"), 0o644))
	runGit(t, worktree, "add", ".")
	runGit(t, worktree, "commit", "-m", "feature commit")
	runGit(t, worktree, "push", "-u", "origin", "feature")
	return worktree, testGitSHA(t, worktree, "refs/remotes/origin/feature")
}

func pushNewPushedHeadE2ECommit(t *testing.T, worktree string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("second\n"), 0o644))
	runGit(t, worktree, "add", ".")
	runGit(t, worktree, "commit", "-m", "second feature commit")
	runGit(t, worktree, "push", "origin", "feature")
	return testGitSHA(t, worktree, "refs/remotes/origin/feature")
}

func insertPushedHeadE2EWorkspace(t *testing.T, database *db.DB, worktreePath string) {
	t.Helper()
	require.NoError(t, database.InsertWorkspace(t.Context(), &db.Workspace{
		ID:              "ws-pr",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      1,
		GitHeadRef:      "feature",
		WorkspaceBranch: "feature",
		WorktreePath:    worktreePath,
		TmuxSession:     "middleman-ws-pr",
		Status:          "ready",
	}))
}

package runtimetest

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/creack/pty"
	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/testtmux"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m, func() int { return gitsafe.RunIsolatedMain(m) }))
}

func requirePTYAvailable(t *testing.T) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable in this test environment: %v", err)
	}
	_ = ptmx.Close()
	_ = tty.Close()
}

func TestAPIListItemsIncludeWorkspaceRefs(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()

	serverfake.SeedPR(t, database, "acme", "widget", 1)
	serverfake.SeedIssue(t, database, "acme", "widget", 2, "open")
	serverfake.SeedWorkspace(
		t, database, "ws-pr-1", "acme", "widget",
		db.WorkspaceItemTypePullRequest, 1,
	)
	serverfake.SeedWorkspace(
		t, database, "ws-issue-2", "acme", "widget",
		db.WorkspaceItemTypeIssue, 2,
	)
	client := servertest.SetupTestClient(t, srv)

	pulls, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, pulls.StatusCode)
	require.NotNil(pulls.JSON200)
	require.Len(*pulls.JSON200, 1)
	require.NotNil((*pulls.JSON200)[0].Workspace)
	assert.Equal("ws-pr-1", (*pulls.JSON200)[0].Workspace.ID)
	assert.Equal("ready", (*pulls.JSON200)[0].Workspace.Status)

	issues, err := client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, issues.StatusCode)
	require.NotNil(issues.JSON200)
	require.Len(*issues.JSON200, 1)
	require.NotNil((*issues.JSON200)[0].Workspace)
	assert.Equal("ws-issue-2", (*issues.JSON200)[0].Workspace.ID)
	assert.Equal("ready", (*issues.JSON200)[0].Workspace.Status)

	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	activity, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since}})
	require.NoError(err)
	require.Equal(http.StatusOK, activity.StatusCode)
	require.NotNil(activity.JSON200)
	require.NotNil(activity.JSON200.Items)

	workspaceByItem := make(map[string]string)
	for _, item := range activity.JSON200.Items {
		if item.Workspace == nil {
			continue
		}
		workspaceByItem[item.ItemType+":"+strconv.FormatInt(item.ItemNumber, 10)] = item.Workspace.ID
	}
	assert.Equal("ws-pr-1", workspaceByItem["pr:1"])
	assert.Equal("ws-issue-2", workspaceByItem["issue:2"])
}

func TestAPIReadyForReviewReclassifiesWorkspaceHeadRepo(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	const forkURL = "https://github.com/contributor/widget.git"

	mock := &serverfake.MockGH{
		MarkReadyForReviewFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			title := "Ready PR"
			state := "open"
			url := "https://github.com/acme/widget/pull/1"
			author := "octocat"
			draft := false
			now := gh.Timestamp{Time: time.Now().UTC().Add(time.Minute)}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				Draft:     &draft,
				CreatedAt: &now,
				UpdatedAt: &now,
				User:      &gh.User{Login: &author},
				Head: &gh.PullRequestBranch{
					Ref:  new("feature"),
					Repo: &gh.Repository{CloneURL: new(forkURL)},
				},
				Base: &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	require.NoError(database.InsertWorkspace(t.Context(), &db.Workspace{
		ID:           "readyfork0000001",
		Platform:     "github",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature",
		WorktreePath: filepath.Join(t.TempDir(), "workspace"),
		TmuxSession:  "kenn-forge-readyfork0000001",
		Status:       "ready",
	}))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	stored, err := database.GetWorkspace(t.Context(), "readyfork0000001")
	require.NoError(err)
	require.NotNil(stored)
	require.NotNil(stored.MRHeadRepo)
	require.Equal(forkURL, *stored.MRHeadRepo)
}

func TestServerTestCleanupDoesNotInvokeDefaultTmux(t *testing.T) {
	require := require.New(t)
	var owner *testtmux.Owner
	if testtmux.Supported() {
		var err error
		owner, err = testtmux.New()
		require.NoError(err)
		t.Cleanup(func() { _ = owner.Cleanup() })
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux-called")
	tmuxPath := filepath.Join(dir, "tmux")
	require.NoError(os.WriteFile(
		tmuxPath,
		[]byte("#!/bin/sh\n: > \"$TMUX_CALLED\"\nexit 1\n"),
		0o755,
	))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_CALLED", logPath)

	require.NoError(serverfake.CleanupServerTestTmux(owner))
	_, err := os.Stat(logPath)
	require.ErrorIs(err, os.ErrNotExist)
}

func TestWorkspacePRDetailPlatformHost(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)

	// Seed same owner/name on different hosts to test ambiguity.
	serverfake.SeedPROnHost(
		t, database,
		"github.com", "acme", "widget", 10,
	)
	serverfake.SeedPROnHost(
		t, database,
		"ghe.example.com", "acme", "widget", 20,
	)

	mock := &serverfake.MockGH{}
	repos := []ghclient.RepoRef{
		{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		},
		{
			Owner: "acme", Name: "widget",
			PlatformHost: "ghe.example.com",
		},
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com":      mock,
			"ghe.example.com": mock,
		},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(
		database, syncer, nil, "/", nil, server.ServerOptions{},
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)
	ctx := t.Context()

	// PR on github.com
	r1, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.Equal(http.StatusOK, r1.StatusCode)
	require.NotNil(r1.JSON200)
	assert.Equal("github.com", r1.JSON200.PlatformHost)

	// PR on ghe.example.com (same owner/name, different number)
	r2, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "ghe.example.com", Provider: "gh", Owner: "acme", Name: "widget", Number: int64(20)}})
	require.NoError(err)
	require.Equal(http.StatusOK, r2.StatusCode)
	require.NotNil(r2.JSON200)
	assert.Equal("ghe.example.com", r2.JSON200.PlatformHost)
}

package workspacetest

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/apiclient"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/workspace"
)

func setupLifecycleWorkspaceServer(t *testing.T) (*apiclient.Client, *db.DB, string, string) {
	t.Helper()
	fixture := setupWorkspaceServerFixture(t, nil)
	return fixture.client, fixture.database, fixture.bare, fixture.remote
}

// TestWorkspaceForceDeleteToleratesCorruptWorktreeGitfileE2E verifies a
// workspace whose worktree .git gitfile was left empty by an
// interrupted "git worktree add" (the daemon canceling background
// setup at shutdown) can still be force-deleted through the API. Git
// rejects such a worktree with "invalid gitfile format", which the
// delete path's worktree-ownership probe surfaced as a 500 before the
// fix — leaving the workspace permanently undeletable.
func TestWorkspaceForceDeleteToleratesCorruptWorktreeGitfileE2E(t *testing.T) {
	t.Parallel()
	acquireWorkspaceGitSlot(t)

	require := require.New(t)
	assert := assert.New(t)

	client, database, _, _ := setupLifecycleWorkspaceServer(t)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	gitfile := filepath.Join(ws.WorktreePath, ".git")
	require.FileExists(gitfile)
	// Truncate the worktree's .git gitfile to reproduce the corrupt
	// state an interrupted "git worktree add" leaves behind.
	require.NoError(os.Truncate(gitfile, 0))

	force := true
	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, ws.Id, &generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(
		http.StatusNoContent, delResp.StatusCode(), string(delResp.Body),
	)

	got, err := database.GetWorkspace(ctx, ws.Id)
	require.NoError(err)
	assert.Nil(got)
}

func TestWorkspaceCreateSameRepoHeadCloneURLTracksOriginBranchE2E(t *testing.T) {
	t.Parallel()
	acquireWorkspaceGitSlot(t)

	require := require.New(t)
	assert := assert.New(t)

	client, database, clonePath, remotePath := setupLifecycleWorkspaceServer(t)
	ctx := t.Context()

	headSHA := testGitSHA(t, remotePath, "refs/heads/feature")
	runGit(t, remotePath, "update-ref", "refs/pull/2/head", headSHA)
	runGit(t, clonePath, "update-ref", "refs/pull/2/head", headSHA)
	seedPROnHost(t, database, "github.com", "acme", "widget", 2)

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			Provider:     "github",
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     2,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)

	ws := waitForWorkspaceReady(t, ctx, client, createResp.JSON202.Id)
	stored, err := database.GetWorkspace(ctx, ws.Id)
	require.NoError(err)
	require.NotNil(stored)
	assert.Nil(stored.MRHeadRepo)
	assert.Empty(stored.WorkspaceBranch)
	assert.Equal("feature", workspaceGitOutput(t, ws.WorktreePath, "branch", "--show-current"))
	assert.Equal(headSHA, testGitSHA(t, ws.WorktreePath, "HEAD"))
	assert.Equal(
		"origin/feature",
		workspaceGitOutput(
			t, ws.WorktreePath,
			"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}",
		),
	)
	assert.Equal(
		"refs/heads/feature",
		workspaceGitOutput(
			t, ws.WorktreePath,
			"config", "--get", "branch.feature.merge",
		),
	)
}

func TestWorkspaceRetryLegacyUnknownHeadRepoLeavesBranchUntrackedE2E(t *testing.T) {
	t.Parallel()
	acquireWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)
	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := t.Context()

	headSHA := testGitSHA(t, fixture.remote, "refs/heads/feature")
	runGit(t, fixture.remote, "update-ref", "refs/pull/2/head", headSHA)
	seedPRWithoutHeadRepo(t, fixture.database, "github.com", "acme", "widget", 2)
	errMessage := "retry legacy workspace"
	const workspaceID = "legacy-unknown-head-repo"
	require.NoError(fixture.database.InsertWorkspace(ctx, &db.Workspace{
		ID:              workspaceID,
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      2,
		GitHeadRef:      "feature",
		MRHeadRepo:      nil,
		WorkspaceBranch: "__middleman_unknown__",
		WorktreePath:    filepath.Join(t.TempDir(), workspaceID),
		TmuxSession:     "middleman-" + workspaceID,
		Status:          "error",
		ErrorMessage:    &errMessage,
	}))

	retryResp, err := fixture.client.HTTP.RetryWorkspaceWithResponse(ctx, workspaceID)
	require.NoError(err)
	require.Equal(http.StatusAccepted, retryResp.StatusCode())

	ready := waitForWorkspaceReady(t, ctx, fixture.client, workspaceID)
	assert.Equal(headSHA, testGitSHA(t, ready.WorktreePath, "HEAD"))
	branch := workspaceGitOutput(t, ready.WorktreePath, "branch", "--show-current")
	remoteOut, remoteErrOut, upstreamErr := gitcmd.New().Run(
		ctx, ready.WorktreePath, nil,
		"config", "--get", "branch."+branch+".remote",
	)
	mergeOut, _, _ := gitcmd.New().Run(
		ctx, ready.WorktreePath, nil,
		"config", "--get", "branch."+branch+".merge",
	)
	assert.Error(
		upstreamErr,
		"legacy unknown workspace must remain untracked after retry; branch=%q remote=%q merge=%q stderr=%q",
		branch, strings.TrimSpace(string(remoteOut)), strings.TrimSpace(string(mergeOut)), strings.TrimSpace(string(remoteErrOut)),
	)
}

func TestWorkspaceDeletePreservesUserCreatedBranch(t *testing.T) {
	t.Parallel()
	acquireWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)

	client, _, clonePath, _ := setupLifecycleWorkspaceServer(t)
	ctx := t.Context()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			Provider:     "github",
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)

	ws := waitForWorkspaceReady(t, ctx, client, createResp.JSON202.Id)
	runGit(t, ws.WorktreePath, "checkout", "-b", "user-scratch")
	scratchSHA := testGitSHA(t, ws.WorktreePath, "HEAD")

	force := true
	deleteResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, createResp.JSON202.Id,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, deleteResp.StatusCode())

	assert.Equal(
		scratchSHA,
		testGitSHA(t, clonePath, "refs/heads/user-scratch"),
	)
}

func TestWorkspaceDeleteDoesNotCleanupReplacementCloneE2E(t *testing.T) {
	t.Parallel()
	acquireWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)

	client, database, _, remotePath := setupLifecycleWorkspaceServer(t)
	ctx := t.Context()
	const branch = "middleman/pr-42"
	replacementClone := filepath.Join(t.TempDir(), "replacement-clone")
	runGit(t, filepath.Dir(replacementClone), "clone", remotePath, replacementClone)
	runGit(
		t, replacementClone, "remote", "set-url", "origin",
		"https://github.com/acme/widget.git",
	)
	runGit(t, replacementClone, "branch", branch, "HEAD")
	branchSHA := testGitSHA(t, replacementClone, "refs/heads/"+branch)
	wsID := "ws-replacement-clone"
	require.NoError(database.InsertWorkspace(ctx, &workspace.Workspace{
		ID:              wsID,
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature",
		WorkspaceBranch: branch,
		WorktreePath:    replacementClone,
		TerminalBackend: workspace.TerminalBackendTmux,
		Status:          "ready",
	}))

	force := true
	deleteResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, wsID, &generated.DeleteWorkspaceParams{Force: &force},
	)

	require.NoError(err)
	require.Equal(http.StatusNoContent, deleteResp.StatusCode())
	assert.DirExists(replacementClone)
	assert.Equal(branchSHA, testGitSHA(t, replacementClone, "refs/heads/"+branch))
	got, err := database.GetWorkspace(ctx, wsID)
	require.NoError(err)
	assert.Nil(got)
}

func TestWorkspaceCreatePreservesExistingLocalPreferredBranch(t *testing.T) {
	t.Parallel()
	acquireWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)

	client, _, clonePath, remotePath := setupLifecycleWorkspaceServer(t)
	ctx := t.Context()

	privateClone := filepath.Join(t.TempDir(), "private-clone")
	runGit(t, filepath.Dir(privateClone), "clone", clonePath, privateClone)
	runGit(t, privateClone, "config", "user.email", "test@test.com")
	runGit(t, privateClone, "config", "user.name", "Test")
	runGit(t, privateClone, "checkout", "feature")

	require.NoError(os.WriteFile(
		filepath.Join(privateClone, "private.txt"),
		[]byte("private\n"), 0o644,
	))
	runGit(t, privateClone, "add", "private.txt")
	runGit(t, privateClone, "commit", "-m", "private commit")
	privateSHA := testGitSHA(t, privateClone, "HEAD")
	runGit(t, privateClone, "push", clonePath, "HEAD:feature")

	originSHA := testGitSHA(t, remotePath, "refs/heads/feature")
	assert.NotEqual(originSHA, privateSHA)
	assert.Equal(privateSHA, testGitSHA(t, clonePath, "refs/heads/feature"))

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			Provider:     "github",
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)

	ws := waitForWorkspaceReady(t, ctx, client, createResp.JSON202.Id)
	assert.Equal(
		"middleman/pr-1",
		workspaceGitOutput(t, ws.WorktreePath, "branch", "--show-current"),
	)
	assert.Equal(originSHA, testGitSHA(t, ws.WorktreePath, "HEAD"))
	assert.Equal(privateSHA, testGitSHA(t, clonePath, "refs/heads/feature"))

	force := true
	deleteResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, createResp.JSON202.Id,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, deleteResp.StatusCode())

	assert.Equal(privateSHA, testGitSHA(t, clonePath, "refs/heads/feature"))
}

func TestWorkspaceDeleteLegacySyntheticBranchAllowsRecreate(t *testing.T) {
	t.Parallel()
	acquireWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)

	client, database, clonePath, remotePath := setupLifecycleWorkspaceServer(t)
	ctx := t.Context()

	privateClone := filepath.Join(t.TempDir(), "legacy-private-clone")
	runGit(t, filepath.Dir(privateClone), "clone", clonePath, privateClone)
	runGit(t, privateClone, "config", "user.email", "test@test.com")
	runGit(t, privateClone, "config", "user.name", "Test")
	runGit(t, privateClone, "checkout", "feature")
	require.NoError(os.WriteFile(
		filepath.Join(privateClone, "legacy-private.txt"),
		[]byte("legacy private\n"), 0o644,
	))
	runGit(t, privateClone, "add", "legacy-private.txt")
	runGit(t, privateClone, "commit", "-m", "legacy private commit")
	privateSHA := testGitSHA(t, privateClone, "HEAD")
	runGit(t, privateClone, "push", clonePath, "HEAD:feature")
	originSHA := testGitSHA(t, remotePath, "refs/heads/feature")
	assert.NotEqual(originSHA, privateSHA)

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			Provider:     "github",
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)

	ws := waitForWorkspaceReady(t, ctx, client, createResp.JSON202.Id)
	assert.Equal(
		"middleman/pr-1",
		workspaceGitOutput(t, ws.WorktreePath, "branch", "--show-current"),
	)

	_, err = database.WriteDB().ExecContext(ctx, `
		UPDATE middleman_workspaces
		SET workspace_branch = '__middleman_unknown__'
		WHERE id = ?`,
		createResp.JSON202.Id,
	)
	require.NoError(err)

	force := true
	deleteResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, createResp.JSON202.Id,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, deleteResp.StatusCode())

	runGit(t, clonePath, "fetch", "--prune", "origin")

	recreateResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			Provider:     "github",
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, recreateResp.StatusCode())
	require.NotNil(recreateResp.JSON202)

	recreated := waitForWorkspaceReady(t, ctx, client, recreateResp.JSON202.Id)
	assert.Equal(
		"middleman/pr-1",
		workspaceGitOutput(t, recreated.WorktreePath, "branch", "--show-current"),
	)
	assert.Equal(originSHA, testGitSHA(t, recreated.WorktreePath, "HEAD"))
}

func TestWorkspaceDeleteDirty(t *testing.T) {
	t.Parallel()
	acquireWorkspaceGitSlot(t)

	require := require.New(t)
	assert := assert.New(t)

	client, database, _, _ := setupLifecycleWorkspaceServer(t)
	ctx := t.Context()

	// Create workspace.
	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			Provider:     "github",
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	ready := waitForWorkspaceReady(t, ctx, client, wsID)
	wsPath := ready.WorktreePath

	// Write a dirty file into the worktree.
	require.NoError(os.WriteFile(
		filepath.Join(wsPath, "dirty.txt"),
		[]byte("uncommitted\n"), 0o644,
	))

	// DELETE without force -> 409.
	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, wsID, &generated.DeleteWorkspaceParams{},
	)
	require.NoError(err)
	assert.Equal(http.StatusConflict, delResp.StatusCode())

	// DELETE with force -> 204.
	force := true
	delResp2, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, wsID,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	assert.Equal(http.StatusNoContent, delResp2.StatusCode())

	// Verify deleted.
	getResp, err := client.HTTP.GetWorkspaceWithResponse(
		ctx, wsID,
	)
	require.NoError(err)
	assert.Equal(http.StatusNotFound, getResp.StatusCode())

	// --- Second scenario: corrupt/missing worktree ---
	// Seed a second PR and create a workspace for it.
	seedPROnHost(t, database, "github.com", "acme", "widget", 2)
	create2, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			Provider:     "github",
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     2,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, create2.StatusCode())
	ws2ID := create2.JSON202.Id

	ready2 := waitForWorkspaceReady(t, ctx, client, ws2ID)
	ws2Path := ready2.WorktreePath

	// Nuke the worktree directory to simulate corruption.
	require.NoError(os.RemoveAll(ws2Path))

	// DELETE without force → 409 (dirty check fails on missing dir).
	del3, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, ws2ID, &generated.DeleteWorkspaceParams{},
	)
	require.NoError(err)
	assert.Equal(http.StatusConflict, del3.StatusCode())

	// DELETE with force → 204.
	del4, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, ws2ID,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	assert.Equal(http.StatusNoContent, del4.StatusCode())

	// Verify deleted.
	get2, err := client.HTTP.GetWorkspaceWithResponse(ctx, ws2ID)
	require.NoError(err)
	assert.Equal(http.StatusNotFound, get2.StatusCode())
}

func TestWorkspaceForceDeleteToleratesMissingWorktreeCommonDirE2E(
	t *testing.T,
) {
	require := require.New(t)
	assert := assert.New(t)

	client, database, _, _ := setupLifecycleWorkspaceServer(t)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)
	installGitCommonDirReadFailure(t)

	force := true
	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, ws.Id, &generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(
		http.StatusNoContent, delResp.StatusCode(), string(delResp.Body),
	)

	got, err := database.GetWorkspace(ctx, ws.Id)
	require.NoError(err)
	assert.Nil(got)
}

func installGitCommonDirReadFailure(t *testing.T) {
	t.Helper()

	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	wrapperDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(wrapperDir, "git"), []byte(`#!/bin/sh
set -eu
case " $* " in
	*" rev-parse --path-format=absolute --git-common-dir "*)
		echo "fatal: failed to read worktrees/pr-1/commondir: Success" >&2
		exit 128
		;;
esac
exec "$MIDDLEMAN_TEST_REAL_GIT" "$@"
`), 0o755))
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MIDDLEMAN_TEST_REAL_GIT", realGit)
}

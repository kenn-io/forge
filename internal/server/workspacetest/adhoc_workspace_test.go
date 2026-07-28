package workspacetest

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient/generated"
)

// Starting new work needs no provider item: a tracked repository plus an
// optional branch name is enough to get a materialized worktree.
func TestCreateAdHocWorkspaceMaterializesRequestedBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	branch := "spike/rate-limits"

	resp, err := fixture.client.HTTP.CreateRepoWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget",
		generated.CreateRepoWorkspaceJSONRequestBody{Branch: &branch},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode(), string(resp.Body))
	require.NotNil(resp.JSON202)
	require.NotNil(resp.JSON202.Created)
	assert.True(*resp.JSON202.Created)
	assert.Equal("adhoc", resp.JSON202.ItemType)
	assert.EqualValues(0, resp.JSON202.ItemNumber)
	assert.Equal(branch, resp.JSON202.GitHeadRef)

	ready := waitForWorkspaceReady(t, t.Context(), fixture.client, resp.JSON202.Id)
	assert.Equal(branch, ready.GitHeadRef)
	assert.Nil(ready.MrTitle)

	head := testGitSHA(t, ready.WorktreePath, "HEAD")
	assert.Equal(testGitSHA(t, fixture.remote, "refs/heads/main"), head,
		"ad-hoc workspaces branch from the repository default branch")
	checkedOut, err := os.ReadFile(filepath.Join(ready.WorktreePath, "base.txt"))
	require.NoError(err)
	assert.Equal("base\n", string(checkedOut))
}

func TestCreateAdHocWorkspaceGeneratesBranchWhenOmitted(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)

	resp, err := fixture.client.HTTP.CreateRepoWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget",
		generated.CreateRepoWorkspaceJSONRequestBody{},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode(), string(resp.Body))
	require.NotNil(resp.JSON202)
	assert.True(strings.HasPrefix(resp.JSON202.GitHeadRef, "middleman/work-"),
		"generated branch %q should carry the work prefix", resp.JSON202.GitHeadRef)

	ready := waitForWorkspaceReady(t, t.Context(), fixture.client, resp.JSON202.Id)
	assert.Equal(resp.JSON202.GitHeadRef, ready.GitHeadRef)
}

// A repeat request for the same branch reopens the workspace that already owns
// it rather than creating a second worktree.
func TestCreateAdHocWorkspaceReusesWorkspaceForSameBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	branch := "spike/rate-limits"
	body := generated.CreateRepoWorkspaceJSONRequestBody{Branch: &branch}

	first, err := fixture.client.HTTP.CreateRepoWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget", body,
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, first.StatusCode(), string(first.Body))
	require.NotNil(first.JSON202)

	second, err := fixture.client.HTTP.CreateRepoWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget", body,
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, second.StatusCode(), string(second.Body))
	require.NotNil(second.JSON202)
	assert.Equal(first.JSON202.Id, second.JSON202.Id)
	assert.Nil(second.JSON202.Created)

	listResp, err := fixture.client.HTTP.ListWorkspacesWithResponse(t.Context())
	require.NoError(err)
	require.NotNil(listResp.JSON200)
	adhoc := 0
	for _, ws := range *listResp.JSON200.Workspaces {
		if ws.ItemType == "adhoc" {
			adhoc++
		}
	}
	assert.Equal(1, adhoc)
}

func TestCreateAdHocWorkspaceRejectsInvalidBranch(t *testing.T) {
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	branch := "bad branch"

	resp, err := fixture.client.HTTP.CreateRepoWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget",
		generated.CreateRepoWorkspaceJSONRequestBody{Branch: &branch},
	)
	require.NoError(err)
	require.Equal(http.StatusBadRequest, resp.StatusCode(), string(resp.Body))
}

func TestCreateAdHocWorkspaceRejectsUntrackedRepo(t *testing.T) {
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	branch := "spike/thing"

	resp, err := fixture.client.HTTP.CreateRepoWorkspaceWithResponse(
		t.Context(), "gh", "acme", "unknown",
		generated.CreateRepoWorkspaceJSONRequestBody{Branch: &branch},
	)
	require.NoError(err)
	require.Equal(http.StatusNotFound, resp.StatusCode(), string(resp.Body))
}

// An existing local branch must surface the typed conflict envelope with a
// suggested alternative so the caller can retry without guessing.
func TestCreateAdHocWorkspaceExistingBranchReturnsTypedConflict(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	branch := "spike/rate-limits"
	mainSHA := testGitSHA(t, fixture.remote, "refs/heads/main")
	runGit(t, fixture.bare, "update-ref", "refs/heads/"+branch, mainSHA)

	resp, err := fixture.client.HTTP.CreateRepoWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget",
		generated.CreateRepoWorkspaceJSONRequestBody{Branch: &branch},
	)
	require.NoError(err)
	require.Equal(http.StatusConflict, resp.StatusCode(), string(resp.Body))

	problem := resp.ApplicationproblemJSONDefault
	require.NotNil(problem)
	require.NotNil(problem.Type)
	assert.Equal("urn:middleman:error:workspace-branch-conflict", *problem.Type)
	assert.Equal(generated.BranchConflict, problem.Code)
	require.NotNil(problem.Details)
	details := *problem.Details
	assert.Equal(branch, details["branch"])
	assert.Equal(branch+"-2", details["suggestedBranch"])
}

func TestCreateAdHocWorkspaceReusesExistingBranchWhenAsked(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	branch := "spike/rate-limits"
	mainSHA := testGitSHA(t, fixture.remote, "refs/heads/main")
	runGit(t, fixture.bare, "update-ref", "refs/heads/"+branch, mainSHA)
	reuse := true

	resp, err := fixture.client.HTTP.CreateRepoWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget",
		generated.CreateRepoWorkspaceJSONRequestBody{
			Branch: &branch, ReuseExistingBranch: &reuse,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode(), string(resp.Body))
	require.NotNil(resp.JSON202)
	assert.Nil(resp.JSON202.Created)

	ready := waitForWorkspaceReady(t, t.Context(), fixture.client, resp.JSON202.Id)
	assert.Equal(branch, ready.GitHeadRef)
	checkedOut := workspaceGitOutput(
		t, ready.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD",
	)
	assert.Equal(branch, checkedOut)
}

func TestCreateAdHocWorkspaceReuseMissingBranchReportsCreated(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	branch := "spike/rate-limits"
	reuse := true

	resp, err := fixture.client.HTTP.CreateRepoWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget",
		generated.CreateRepoWorkspaceJSONRequestBody{
			Branch: &branch, ReuseExistingBranch: &reuse,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode(), string(resp.Body))
	require.NotNil(resp.JSON202)
	require.NotNil(resp.JSON202.Created)
	assert.True(*resp.JSON202.Created)
}

// Reuse is the one case where work does not start at origin/HEAD: the existing
// branch is adopted at its own tip, however far that has diverged.
func TestCreateAdHocWorkspaceReuseStartsFromDivergedBranchTip(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	// The fixture's "feature" branch carries a commit that main does not.
	branch := "feature"
	featureSHA := testGitSHA(t, fixture.remote, "refs/heads/"+branch)
	mainSHA := testGitSHA(t, fixture.remote, "refs/heads/main")
	require.NotEqual(mainSHA, featureSHA)
	reuse := true

	resp, err := fixture.client.HTTP.CreateRepoWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget",
		generated.CreateRepoWorkspaceJSONRequestBody{
			Branch: &branch, ReuseExistingBranch: &reuse,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode(), string(resp.Body))
	require.NotNil(resp.JSON202)

	ready := waitForWorkspaceReady(t, t.Context(), fixture.client, resp.JSON202.Id)
	assert.Equal(branch, ready.GitHeadRef)
	assert.Equal(featureSHA, testGitSHA(t, ready.WorktreePath, "HEAD"),
		"reuse adopts the existing branch tip, not origin/HEAD")
	_, err = os.Stat(filepath.Join(ready.WorktreePath, "new.txt"))
	assert.NoError(err, "the diverged commit's file should be checked out")
}

// Renaming the branch from inside the worktree is a shell action middleman does
// not observe, so the workspace keeps its creation-time identity: the old name
// still resolves to it, and the new name cannot be turned into a second
// workspace while this worktree holds the branch.
func TestCreateAdHocWorkspaceAfterInWorktreeBranchRename(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	original := "spike/rate-limits"
	renamed := "spike/rate-limits-v2"

	created, err := fixture.client.HTTP.CreateRepoWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget",
		generated.CreateRepoWorkspaceJSONRequestBody{Branch: &original},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, created.StatusCode(), string(created.Body))
	require.NotNil(created.JSON202)
	ready := waitForWorkspaceReady(t, t.Context(), fixture.client, created.JSON202.Id)

	runGit(t, ready.WorktreePath, "branch", "-m", original, renamed)

	// Old name: still this workspace, because item_key is the creation-time
	// branch and is never rewritten.
	again, err := fixture.client.HTTP.CreateRepoWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget",
		generated.CreateRepoWorkspaceJSONRequestBody{Branch: &original},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, again.StatusCode(), string(again.Body))
	require.NotNil(again.JSON202)
	assert.Equal(created.JSON202.Id, again.JSON202.Id)

	// New name: the renamed branch exists locally, so this is the ordinary
	// branch conflict with a suggested alternative, not a second worktree on the
	// same branch.
	conflict, err := fixture.client.HTTP.CreateRepoWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget",
		generated.CreateRepoWorkspaceJSONRequestBody{Branch: &renamed},
	)
	require.NoError(err)
	require.Equal(http.StatusConflict, conflict.StatusCode(), string(conflict.Body))
	problem := conflict.ApplicationproblemJSONDefault
	require.NotNil(problem)
	assert.Equal(generated.BranchConflict, problem.Code)
	require.NotNil(problem.Details)
	assert.Equal(renamed+"-2", (*problem.Details)["suggestedBranch"])
}

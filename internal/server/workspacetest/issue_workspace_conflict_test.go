package workspacetest

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient/generated"
)

// TestIssueWorkspaceConflictExposesTyped409ThroughGeneratedClient is a
// black-box migration of TestWorkspaceCreateIssueBranchConflictReturnsTyped409
// (still in internal/server/api_test.go). The original asserts the same
// behavior using a package-local rawProblemDetail struct; this version
// decodes through generated.ErrorModel so a regression that drifts the
// 409 response shape away from the published OpenAPI contract fails this
// test.
//
// The migration is not a transport change — both shapes go through
// srv.ServeHTTP via the recorder transport. It is a package-boundary
// change: this file lives in package workspacetest, cannot reach into
// package server's unexported helpers, and uses the generated apiclient
// instead of the in-package doJSON helper. workspacetest/ is the natural
// home because setupWorkspaceServerFixture already wires up a real git
// remote, which apitest/ does not.
func TestIssueWorkspaceConflictExposesTyped409ThroughGeneratedClient(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)

	seedIssue(t, fixture.database, "acme", "widget", 7, "open")

	branch := "middleman/issue-7"

	// Pre-create the requested branch so the next workspace request hits
	// the typed conflict path regardless of the default branch style.
	mainSHA := testGitSHA(t, fixture.remote, "refs/heads/main")
	runGit(
		t,
		fixture.bare,
		"update-ref", "refs/heads/"+branch, mainSHA,
	)

	resp, err := fixture.client.HTTP.CreateIssueWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget", 7,
		generated.CreateIssueWorkspaceInputBody{GitHeadRef: &branch},
	)
	require.NoError(err)
	require.Equal(http.StatusConflict, resp.StatusCode(), string(resp.Body))

	problem := resp.ApplicationproblemJSONDefault
	require.NotNil(problem, "default error model must be populated for 409")

	require.NotNil(problem.Type)
	assert.Equal(
		"urn:middleman:error:issue-workspace-branch-conflict",
		*problem.Type,
	)
	require.NotNil(problem.Status)
	assert.EqualValues(http.StatusConflict, *problem.Status)
	require.NotNil(problem.Detail)
	assert.NotEmpty(*problem.Detail)

	require.NotNil(problem.Errors)
	locations := map[string]any{}
	for _, e := range *problem.Errors {
		if e.Location == nil {
			continue
		}
		locations[*e.Location] = e.Value
	}
	assert.Equal(branch, locations["body.git_head_ref"])
	assert.Equal(
		branch+"-2",
		locations["body.suggested_git_head_ref"],
	)
}

func TestIssueWorkspaceCreateIgnoresBrokenCallerCwdForBranchValidation(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	seedIssue(t, fixture.database, "acme", "widget", 23, "open")

	brokenCwd := t.TempDir()
	require.NoError(os.WriteFile(
		filepath.Join(brokenCwd, ".git"),
		[]byte("gitdir: /definitely/not/a/git/worktree\n"),
		0o644,
	))
	previousCwd, err := os.Getwd()
	require.NoError(err)
	require.NoError(os.Chdir(brokenCwd))
	t.Cleanup(func() {
		require.NoError(os.Chdir(previousCwd))
	})

	branch := "middleman/issue-23-federation-test"
	resp, err := fixture.client.HTTP.CreateIssueWorkspaceWithResponse(
		t.Context(), "gh", "acme", "widget", 23,
		generated.CreateIssueWorkspaceInputBody{GitHeadRef: &branch},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode(), string(resp.Body))
	require.NotNil(resp.JSON202)

	ready := waitForWorkspaceReady(
		t, t.Context(), fixture.client, resp.JSON202.Id,
	)
	assert.Equal(branch, ready.GitHeadRef)
	assert.Equal("ready", ready.Status)
	assert.FileExists(filepath.Join(ready.WorktreePath, "base.txt"))
}

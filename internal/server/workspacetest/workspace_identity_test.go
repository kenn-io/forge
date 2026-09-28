package workspacetest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil/gitfixture"
)

func TestItemWorkspaceCreationRejectsReplacedRepositoryIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := setupWorkspaceServerFixture(t, nil)
	current, _, err := fixture.database.ReconcileRepositoryObservation(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: "repo-replacement",
		Owner: "acme", Name: "widget",
	}, time.Now().UTC().Add(time.Hour))
	require.NoError(err)
	require.NotNil(current)
	require.NoError(fixture.database.UpdateRepoProviderMetadata(t.Context(), current.Repository.ID, db.RepoProviderMetadata{
		PlatformRepoID: "repo-replacement", CloneURL: "https://github.com/acme/widget.git", DefaultBranch: "main",
	}))
	bare, err := fixture.clones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(t.Context(), "repo-replacement"), "github", "github.com", "acme", "widget",
	)
	require.NoError(err)
	gitfixture.Run(t, t.TempDir(), "clone", "--bare", fixture.remote, bare)
	gitfixture.Run(t, bare, "remote", "set-url", "origin", "https://github.com/acme/widget.git")
	gitfixture.Run(t, bare, "config", "--add", "url."+fixture.remote+".insteadOf", "https://github.com/acme/widget.git")
	pull, err := fixture.database.GetMergeRequestByRepoIDAndNumber(t.Context(), fixture.repoID, 1)
	require.NoError(err)
	require.NotNil(pull)
	pull.ID, pull.RepoID = 0, current.Repository.ID
	_, err = fixture.database.UpsertMergeRequest(t.Context(), pull)
	require.NoError(err)
	now := time.Now().UTC()
	for _, number := range []int{7, 8} {
		_, err = fixture.database.UpsertIssue(t.Context(), &db.Issue{
			RepoID: current.Repository.ID, Number: number, PlatformID: int64(number),
			Title: "Replacement issue", State: "open", Author: "testuser",
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}
	existing, err := fixture.client.HTTP.CreateIssueWorkspaceWithResponse(t.Context(), &generated.CreateIssueWorkspaceRequestOptions{
		PathParams: &generated.CreateIssueWorkspacePath{Provider: "github", Owner: "acme", Name: "widget", Number: 7},
		Body:       &generated.CreateIssueWorkspaceInputBody{},
	})
	require.NoError(err)
	require.NotNil(existing.JSON202)
	waitForWorkspaceReady(t, t.Context(), fixture.client, existing.JSON202.ID)

	// The visible detail belongs to the displaced repository. Neither creation
	// nor early reuse of a replacement issue workspace may redirect that intent.
	for _, path := range []string{
		"/api/v1/workspaces",
		"/api/v1/fleet/hosts/self/workspaces",
		"/api/v1/issues/github/acme/widget/7/workspace",
		"/api/v1/issues/github/acme/widget/8/workspace",
		"/api/v1/host/github.com/issues/github/acme/widget/7/workspace",
		"/api/v1/host/github.com/issues/github/acme/widget/8/workspace",
		"/api/v1/fleet/hosts/self/issues/github/acme/widget/7/workspace",
		"/api/v1/fleet/hosts/self/host/github.com/issues/github/acme/widget/8/workspace",
	} {
		body := `{"platform_repo_id":"repo-acme-widget"}`
		if strings.HasSuffix(path, "/workspaces") {
			body = `{"provider":"github","platform_host":"github.com","owner":"acme","name":"widget","mr_number":1,"platform_repo_id":"repo-acme-widget"}`
		}
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://forge.test"+path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		fixture.server.ServeHTTP(response, request)
		assert.Equal(http.StatusNotFound, response.Code, "%s: %s", path, response.Body.String())
		assert.Contains(response.Body.String(), `"code":"repoNotFound"`)
	}
	workspaces, err := fixture.client.HTTP.ListWorkspacesWithResponse(t.Context())
	require.NoError(err)
	require.NotNil(workspaces.JSON200)
	assert.Len(workspaces.JSON200.Workspaces, 1, "rejected stale requests must not persist a workspace")

	for _, path := range []string{
		"/api/v1/issues/github/acme/widget/7/workspace",
		"/api/v1/host/github.com/issues/github/acme/widget/7/workspace",
	} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://forge.test"+path,
			strings.NewReader(`{"platform_repo_id":"repo-replacement"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		fixture.server.ServeHTTP(response, request)
		require.Equal(http.StatusAccepted, response.Code, response.Body.String())
		require.Contains(response.Body.String(), fmt.Sprintf(`"id":%q`, existing.JSON202.ID))
	}
}

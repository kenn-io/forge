package e2etest

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/db"
)

func TestWorkspaceAPIHidesRemovedAssociatedPullRequestE2E(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	ts, database := bootFleetServer(t, nil)

	repoIdentity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	repoIdentity.PlatformRepoID = "repo-acme-widget"
	repoID, err := database.UpsertRepo(ctx, repoIdentity)
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, PlatformID: 4200, Number: 42,
		URL: "https://github.com/acme/widget/pull/42", Title: "Removed pull",
		Author: "dev", State: "open", HeadBranch: "feature", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	associatedPR := 42
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "ws-adhoc", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: db.WorkspaceItemTypeAdHoc, ItemKey: db.AdHocWorkspaceItemKey("feature"),
		AssociatedPRNumber: &associatedPR,
		GitHeadRef:         "feature", WorktreePath: t.TempDir(), Status: "creating",
	}))
	_, err = database.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES (?, 'merge_request', 42, 'pull-42', ?, ?, 'removed_upstream')`,
		repoID, now, now,
	)
	require.NoError(err)
	summary, err := database.GetWorkspaceSummary(ctx, "ws-adhoc")
	require.NoError(err)
	require.NotNil(summary)
	require.False(summary.AssociatedPRVisible)

	client, err := apiclient.NewWithHTTPClient(ts.URL, ts.Client())
	require.NoError(err)
	list, err := client.HTTP.ListWorkspacesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, list.StatusCode(), string(list.Body))
	require.NotNil(list.JSON200)
	require.NotNil(list.JSON200.Workspaces)
	require.Len(*list.JSON200.Workspaces, 1)
	require.Equal("ws-adhoc", (*list.JSON200.Workspaces)[0].Id)
	require.Nil((*list.JSON200.Workspaces)[0].AssociatedPrNumber)

	detail, err := client.HTTP.GetWorkspaceWithResponse(ctx, "ws-adhoc")
	require.NoError(err)
	require.Equal(http.StatusOK, detail.StatusCode(), string(detail.Body))
	require.NotNil(detail.JSON200)
	require.Nil(detail.JSON200.AssociatedPrNumber)

	stored, err := database.GetWorkspace(ctx, "ws-adhoc")
	require.NoError(err)
	require.NotNil(stored)
	require.NotNil(stored.AssociatedPRNumber)
	require.Equal(42, *stored.AssociatedPRNumber)
}

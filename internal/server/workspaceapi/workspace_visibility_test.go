package workspaceapi

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestWorkspaceMergeTargetBranchRejectsRemovedPullRequest(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := t.Context()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	repoIdentity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	repoIdentity.PlatformRepoID = "repo-acme-widget"
	repoID, err := database.UpsertRepo(ctx, repoIdentity)
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, PlatformID: 7, Number: 7,
		URL: "https://github.com/acme/widget/pull/7", Title: "Removed pull",
		Author: "alice", State: "open", HeadBranch: "feature", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "ws-removed-pr", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 7,
		GitHeadRef: "feature", WorktreePath: t.TempDir(), Status: "ready",
	}))
	summary, err := database.GetWorkspaceSummary(ctx, "ws-removed-pr")
	require.NoError(err)
	require.NotNil(summary)
	branch, available, err := (&Handler{db: database}).workspaceMergeTargetBranch(
		ctx, summary,
	)
	require.NoError(err)
	require.True(available)
	require.Equal("main", branch)

	_, err = database.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES (?, 'merge_request', 7, 'pull-7', ?, ?, 'removed_upstream')`,
		repoID, now, now,
	)
	require.NoError(err)
	summary, err = database.GetWorkspaceSummary(ctx, "ws-removed-pr")
	require.NoError(err)
	require.NotNil(summary)

	branch, available, err = (&Handler{db: database}).workspaceMergeTargetBranch(
		ctx, summary,
	)

	require.NoError(err)
	require.False(available)
	require.Empty(branch)
}

package settingsservertest

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/itemapi"
)

func TestMCPBackendAppliesActivityItemTypesBeforeSafetyWindow(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	pullID := seedPR(t, database, "acme", "widget", 42)
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: pullID, EventType: "issue_comment", Author: "reviewer",
		Body: "review this", CreatedAt: base, DedupeKey: "mcp-item-filter-comment",
	}}))
	commits := make([]db.BranchCommit, itemapi.ActivitySafetyCap+1)
	for i := range commits {
		at := base.Add(time.Duration(i+1) * time.Millisecond)
		commits[i] = db.BranchCommit{
			RepoID: repo.ID, BranchName: "main", CommitSHA: fmt.Sprintf("%040x", i+1),
			AuthorName: "maintainer", AuthoredAt: at,
			CommitterName: "maintainer", CommittedAt: at,
			Subject: "repository activity", CreatedAt: at, UpdatedAt: at,
		}
	}
	require.NoError(database.UpsertBranchCommits(ctx, commits))

	page, err := srv.MCPBackend().ListActivity(ctx, mcpserver.ActivityQuery{
		Since: base.Add(-time.Minute).Format(time.RFC3339), ItemTypes: []string{"pr"},
	})

	require.NoError(err)
	require.NotEmpty(page.Items)
	for _, item := range page.Items {
		assert.Equal("pr", item.ItemType)
		assert.Equal(repo.PlatformRepoID, item.Repository.PlatformRepoID)
	}
	assert.False(page.Capped)
}

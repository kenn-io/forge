package archive

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"
)

func TestSnapshotReferencesFollowRepositoryScope(t *testing.T) {
	t.Parallel()
	for _, issueScope := range []string{"", "open"} {
		t.Run("issue_scope="+issueScope, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := dbtest.Open(t)
			now := archiveTestTime()
			first := archiveServiceRef(platform.KindGitHub, "github.test", "project-a")
			second := archiveServiceRef(platform.KindGitHub, "github.test", "project-b")
			firstID := archiveServiceSeedRepo(t, database, first)
			secondID := archiveServiceSeedRepo(t, database, second)
			provider := newArchiveServiceProvider(first.Platform, first.Host)
			registry, err := platform.NewRegistry(provider)
			require.NoError(err)
			service := newArchiveTestService(t, database, registry, []platform.RepoRef{first, second}, nil, now)
			_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: firstID, Number: 1, Title: "Open fix", State: db.MergeRequestStateOpen, CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now})
			require.NoError(err)
			for _, issue := range []db.Issue{
				{RepoID: firstID, PlatformID: 11, Number: 11, Title: "Local issue", State: "open", CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now},
				{RepoID: secondID, PlatformID: 22, Number: 22, Title: "Related closed issue", State: "closed", CreatedAt: now.Add(-30 * 24 * time.Hour), UpdatedAt: now},
			} {
				id, err := database.UpsertIssue(t.Context(), &issue)
				require.NoError(err)
				require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{IssueID: id, EventType: "cross_referenced", DedupeKey: "reference", CreatedAt: now, MetadataJSON: `{"source_type":"PullRequest","source_owner":"owner","source_repo":"project-a","source_number":1,"source_url":"https://github.test/owner/project-a/pull/1"}`}}))
			}
			options := SnapshotOptions{Start: now.Add(-time.Hour), End: now, IssueScope: issueScope, Repositories: []platform.RepoRef{first}}
			scoped, err := service.Snapshot(t.Context(), options)
			require.NoError(err)
			require.Len(scoped.Repositories, 1)
			require.Len(scoped.PullRequests, 1)
			require.Len(scoped.Issues, 1)
			assert.Equal(11, scoped.Issues[0].Number)
			assert.NotContains(scoped.PullRequests[0].Gaps, "unresolved_issue_reference")
			require.Len(scoped.Relations, 1)
			assert.Equal(scoped.Issues[0].ID, scoped.Relations[0].TargetID)

			tx, err := database.ReadDB().BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
			require.NoError(err)
			measurement, err := db.MeasureArchiveSnapshot(t.Context(), tx, []int64{firstID}, options.Start, options.End, issueScope == "open")
			require.NoError(err)
			require.NoError(tx.Rollback())
			assert.Equal(3, measurement.Records, "only the selected PR, issue, and reference consume the record budget")

			options.Repositories = []platform.RepoRef{first, second}
			expanded, err := service.Snapshot(t.Context(), options)
			require.NoError(err)
			require.Len(expanded.Issues, 2)
			require.Len(expanded.Relations, 2)
			assert.Equal("closed", expanded.Issues[1].State)
			assert.NotContains(expanded.PullRequests[0].Gaps, "unresolved_issue_reference")

			_, err = database.WriteDB().ExecContext(t.Context(), `INSERT INTO forge_archive_items (repo_id,item_type,item_number,provider_item_id,provider_created_at,provider_updated_at,lifecycle_state) VALUES (?, 'issue',11,'issue-11',?,?,'removed_upstream')`, firstID, now.Add(-24*time.Hour), now)
			require.NoError(err)
			options.Repositories = []platform.RepoRef{first}
			unavailable, err := service.Snapshot(t.Context(), options)
			require.NoError(err)
			require.Len(unavailable.PullRequests, 1)
			assert.Contains(unavailable.PullRequests[0].Gaps, "unresolved_issue_reference", "unavailable selected issues must still report missing evidence")
			assert.Empty(unavailable.Relations)
			assert.Empty(provider.calls, "snapshot reads stay local")
		})
	}
}

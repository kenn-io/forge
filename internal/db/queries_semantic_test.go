package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListSemanticSourcesSelectsVisibleProse(t *testing.T) {
	assert := assert.New(t)
	d := openTemplateTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "acme", "widgets")
	inactiveRepoID := insertTestRepo(t, d, "acme", "retired")

	pr := testMR(repoID, 1, withMRTitle("Speed up cache"))
	pr.Body = "Warm the cache on deploy."
	prID := insertTestMRWithOptions(t, d, pr)
	removedPRID := insertTestMR(t, d, repoID, 2, "Removed upstream", baseTime())
	insertTestMR(t, d, inactiveRepoID, 3, "Inactive repository", baseTime())

	issue := testIssue(repoID, 4, withIssueTitle("Login loops"))
	issue.Body = "Users bounce to login."
	issueID := insertTestIssueWithOptions(t, d, issue)

	require.NoError(t, d.UpsertMREvents(ctx, []MREvent{
		{MergeRequestID: prID, EventType: "issue_comment", Body: "Looks good", CreatedAt: baseTime(), DedupeKey: "c1"},
		{MergeRequestID: prID, EventType: "review", Body: "Approve with nits", CreatedAt: baseTime(), DedupeKey: "r1"},
		{MergeRequestID: prID, EventType: "review_comment", Body: "Rename this", CreatedAt: baseTime(), DedupeKey: "rc1"},
		{MergeRequestID: prID, EventType: "commit", Body: "fix: commit message", CreatedAt: baseTime(), DedupeKey: "k1"},
		{MergeRequestID: prID, EventType: "issue_comment", Body: "  \n ", CreatedAt: baseTime(), DedupeKey: "blank"},
		{MergeRequestID: removedPRID, EventType: "issue_comment", Body: "On a removed PR", CreatedAt: baseTime(), DedupeKey: "c2"},
	}))
	require.NoError(t, d.UpsertIssueEvents(ctx, []IssueEvent{
		{IssueID: issueID, EventType: "issue_comment", Body: "Same here", CreatedAt: baseTime(), DedupeKey: "ic1"},
		{IssueID: issueID, EventType: "closed", Body: "closed", CreatedAt: baseTime(), DedupeKey: "ic2"},
	}))
	_, err := d.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES (?, 'merge_request', 2, 'mr-2', ?, ?, 'removed_upstream')`,
		repoID, baseTime(), baseTime())
	require.NoError(t, err)
	_, err = d.WriteDB().ExecContext(ctx,
		`UPDATE forge_repos SET lifecycle_state = 'inactive' WHERE id = ?`, inactiveRepoID)
	require.NoError(t, err)

	pulls, err := d.ListSemanticSources(ctx, SemanticSourcePull, 0, 10)
	require.NoError(t, err)
	require.Len(t, pulls, 1, "removed and inactive pull requests are excluded")
	assert.Equal(SemanticSource{
		Kind: SemanticSourcePull, ID: prID, RepoID: repoID, ItemType: "pr", ItemNumber: 1,
		ItemState: "open", Title: "Speed up cache", Body: "Warm the cache on deploy.",
	}, pulls[0])

	issues, err := d.ListSemanticSources(ctx, SemanticSourceIssue, 0, 10)
	require.NoError(t, err)
	require.Len(t, issues, 1)
	assert.Equal("Login loops", issues[0].Title)

	comments, err := d.ListSemanticSources(ctx, SemanticSourcePullComment, 0, 10)
	require.NoError(t, err)
	bodies := make([]string, len(comments))
	for i, comment := range comments {
		bodies[i] = comment.Body
		assert.Equal(1, comment.ItemNumber, "comments carry their pull request")
		assert.Empty(comment.Title)
	}
	assert.Equal([]string{"Looks good", "Approve with nits", "Rename this"}, bodies,
		"commits, blank bodies, and removed items are excluded")

	issueComments, err := d.ListSemanticSources(ctx, SemanticSourceIssueComment, 0, 10)
	require.NoError(t, err)
	require.Len(t, issueComments, 1)
	assert.Equal("Same here", issueComments[0].Body)
	assert.Equal("issue", issueComments[0].ItemType)

	page, err := d.ListSemanticSources(ctx, SemanticSourcePullComment, comments[0].ID, 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(comments[1].ID, page[0].ID, "pages continue after the given ID")
}

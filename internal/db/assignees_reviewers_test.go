package db

import (
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedAssigneeTestMR(t *testing.T, d *DB, updatedAt time.Time, assigneesJSON, reviewersJSON string) (int64, int64) {
	t.Helper()
	ctx := t.Context()
	repoID, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	mrID, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID:         repoID,
		PlatformID:     1000,
		Number:         1,
		Title:          "PR",
		Author:         "octocat",
		State:          "open",
		CreatedAt:      updatedAt,
		UpdatedAt:      updatedAt,
		LastActivityAt: updatedAt,
		AssigneesJSON:  assigneesJSON,
		ReviewersJSON:  reviewersJSON,
	})
	require.NoError(t, err)
	return repoID, mrID
}

func TestUpsertMergeRequestPersistsAndParsesUserLists(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	repoID, _ := seedAssigneeTestMR(t, d, now, `["alice","bob"]`, `["carol"]`)

	mr, err := d.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal([]string{"alice", "bob"}, mr.Assignees)
	assert.Equal([]string{"carol"}, mr.RequestedReviewers)

	listed, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{RepoID: repoID})
	require.NoError(err)
	require.Len(listed, 1)
	assert.Equal([]string{"alice", "bob"}, listed[0].Assignees)
	assert.Equal([]string{"carol"}, listed[0].RequestedReviewers)
}

func TestUpsertMergeRequestPreservesUserListsWhenProviderOmitsThem(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	repoID, _ := seedAssigneeTestMR(t, d, now, `["alice"]`, `["carol"]`)

	// A later sync whose provider response did not carry the fields
	// (empty JSON columns) must keep the previously stored values.
	_, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID:         repoID,
		PlatformID:     1000,
		Number:         1,
		Title:          "PR updated",
		Author:         "octocat",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now.Add(time.Hour),
		LastActivityAt: now.Add(time.Hour),
	})
	require.NoError(err)

	mr, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("PR updated", mr.Title)
	assert.Equal([]string{"alice"}, mr.Assignees)
	assert.Equal([]string{"carol"}, mr.RequestedReviewers)

	// A sync carrying a provider-confirmed empty set must overwrite.
	_, err = d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID:         repoID,
		PlatformID:     1000,
		Number:         1,
		Title:          "PR updated",
		Author:         "octocat",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now.Add(2 * time.Hour),
		LastActivityAt: now.Add(2 * time.Hour),
		AssigneesJSON:  "[]",
		ReviewersJSON:  "[]",
	})
	require.NoError(err)

	mr, err = d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Empty(mr.Assignees)
	assert.Empty(mr.RequestedReviewers)
}

func TestUpdateMergeRequestUserListsPersistMutationResults(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	repoID, mrID := seedAssigneeTestMR(t, d, now, "", "")

	require.NoError(d.UpdateMergeRequestAssignees(ctx, repoID, mrID, []string{"alice"}))
	require.NoError(d.UpdateMergeRequestReviewers(ctx, repoID, mrID, []string{"bob"}))

	mr, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal([]string{"alice"}, mr.Assignees)
	assert.Equal([]string{"bob"}, mr.RequestedReviewers)

	require.NoError(d.UpdateMergeRequestAssignees(ctx, repoID, mrID, nil))
	require.NoError(d.UpdateMergeRequestReviewers(ctx, repoID, mrID, nil))
	mr, err = d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	assert.Empty(mr.Assignees)
	assert.Equal("[]", mr.AssigneesJSON)
	assert.Equal("[]", mr.ReviewersJSON)
}

func TestUpdateIssueAssigneesPersistsMutationResults(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	repoID, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	issueID, err := d.UpsertIssue(ctx, &Issue{
		RepoID:         repoID,
		PlatformID:     2000,
		Number:         7,
		Title:          "Issue",
		Author:         "octocat",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	require.NoError(d.UpdateIssueAssignees(ctx, repoID, issueID, []string{"dana"}))
	issue, err := d.GetIssueByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal([]string{"dana"}, issue.Assignees)
}

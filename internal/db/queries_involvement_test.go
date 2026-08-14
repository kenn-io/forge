package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListMergeRequestsInvolvingViewer(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	viewer := []RepoViewerLogin{{RepoID: repoID, Login: "Alice"}}

	authorID := insertTestMRWithOptions(t, d, testMR(repoID, 1, withMRAuthor("alice")))
	assignee := testMR(repoID, 2)
	assignee.AssigneesJSON = `["ALICE"]`
	assigneeID := insertTestMRWithOptions(t, d, assignee)
	reviewer := testMR(repoID, 3)
	reviewer.ReviewersJSON = `["aLiCe"]`
	reviewerID := insertTestMRWithOptions(t, d, reviewer)
	reviewedID := insertTestMRWithOptions(t, d, testMR(repoID, 4))
	commentedID := insertTestMRWithOptions(t, d, testMR(repoID, 5))
	commitOnlyID := insertTestMRWithOptions(t, d, testMR(repoID, 6))
	insertTestMRWithOptions(t, d, testMR(repoID, 7))
	notifiedID := insertTestMRWithOptions(t, d, testMR(repoID, 8))
	insertTestMRWithOptions(t, d, testMR(repoID, 9))

	require.NoError(d.UpsertMREvents(ctx, []MREvent{
		{MergeRequestID: reviewedID, EventType: "review", Author: "ALICE", CreatedAt: baseTime(), DedupeKey: "reviewed"},
		{MergeRequestID: commentedID, EventType: "issue_comment", Author: "alice", CreatedAt: baseTime(), DedupeKey: "commented"},
		{MergeRequestID: commitOnlyID, EventType: "commit", Author: "alice", CreatedAt: baseTime(), DedupeKey: "commit-only"},
	}))
	mention := notificationFixture("mention-involvement", "mention", baseTime())
	mention.RepoID = &repoID
	mention.ItemNumber = new(8)
	mention.Participating = false
	ciOnly := notificationFixture("ci-only", "ci_activity", baseTime())
	ciOnly.RepoID = &repoID
	ciOnly.ItemNumber = new(9)
	ciOnly.Participating = false
	require.NoError(d.UpsertNotifications(ctx, []Notification{mention, ciOnly}))

	got, err := d.ListMergeRequests(ctx, ListMergeRequestsOpts{ViewerLogins: viewer, State: "all"})
	require.NoError(err)
	assert.ElementsMatch([]int64{
		authorID, assigneeID, reviewerID, reviewedID, commentedID, notifiedID,
	}, mergeRequestIDs(got))

	limited, err := d.ListMergeRequests(ctx, ListMergeRequestsOpts{ViewerLogins: viewer, State: "all", Limit: 2})
	require.NoError(err)
	assert.Len(limited, 2, "the involvement predicate must run before pagination")

	workspaceKeys, err := d.ListInvolvedWorkspaceSubjectKeys(ctx, viewer, []WorkspaceSubjectKey{
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 1},
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 7},
	})
	require.NoError(err)
	assert.Equal(map[WorkspaceSubjectKey]struct{}{
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 1}: {},
	}, workspaceKeys)
}

func TestListIssuesInvolvingViewer(t *testing.T) {
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	viewer := []RepoViewerLogin{{RepoID: repoID, Login: "Alice"}}

	authorID := insertTestIssueWithOptions(t, d, testIssue(repoID, 1, withIssueAuthor("alice")))
	assignee := testIssue(repoID, 2)
	assignee.AssigneesJSON = `["ALICE"]`
	assigneeID := insertTestIssueWithOptions(t, d, assignee)
	commentedID := insertTestIssueWithOptions(t, d, testIssue(repoID, 3))
	insertTestIssueWithOptions(t, d, testIssue(repoID, 4))
	require.NoError(t, d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID: commentedID, EventType: "issue_comment", Author: "aLiCe",
		CreatedAt: baseTime(), DedupeKey: "commented",
	}}))

	got, err := d.ListIssues(ctx, ListIssuesOpts{ViewerLogins: viewer, State: "all"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{authorID, assigneeID, commentedID}, issueIDs(got))
}

func TestListActivityInvolvingViewerUsesSubjectInvolvement(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	viewer := []RepoViewerLogin{{RepoID: repoID, Login: "Alice"}}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	involvedID := insertTestMRWithOptions(t, d, testMR(repoID, 1,
		withMRAuthor("alice"), withMRActivity(now.Add(-time.Hour))))
	commitOnlyID := insertTestMRWithOptions(t, d, testMR(repoID, 2,
		withMRAuthor("other"), withMRActivity(now.Add(-time.Hour))))
	commentedID := insertTestIssueWithOptions(t, d, testIssue(repoID, 3,
		withIssueAuthor("other"), withIssueActivity(now.Add(-time.Hour))))
	insertTestIssueWithOptions(t, d, testIssue(repoID, 4,
		withIssueAuthor("other"), withIssueActivity(now.Add(-time.Hour))))

	require.NoError(d.UpsertMREvents(ctx, []MREvent{
		{MergeRequestID: involvedID, EventType: "commit", Author: "other", CreatedAt: now, DedupeKey: "involved-commit"},
		{MergeRequestID: commitOnlyID, EventType: "commit", Author: "ALICE", CreatedAt: now, DedupeKey: "viewer-commit-only"},
	}))
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID: commentedID, EventType: "issue_comment", Author: "ALICE",
		CreatedAt: now, DedupeKey: "viewer-comment",
	}}))

	got, err := d.ListActivity(ctx, ListActivityOpts{ViewerLogins: viewer, Limit: 100})
	require.NoError(err)
	gotKeys := make([]string, 0, len(got))
	for _, item := range got {
		gotKeys = append(gotKeys, fmt.Sprintf("%s:%s:%d", item.Source, item.ItemType, item.ItemNumber))
		assert.NotEmpty(item.ItemType, "repository-only activity is not involvement")
		assert.NotEqual(2, item.ItemNumber, "commit authorship alone is not involvement")
		assert.NotEqual(4, item.ItemNumber)
	}
	assert.Contains(gotKeys, "pr:pr:1")
	assert.Contains(gotKeys, "pre:pr:1", "all events on an involved subject remain visible")
	assert.Contains(gotKeys, "issue:issue:3")
	assert.Contains(gotKeys, "ise:issue:3")
}

func mergeRequestIDs(items []MergeRequest) []int64 {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func issueIDs(items []Issue) []int64 {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

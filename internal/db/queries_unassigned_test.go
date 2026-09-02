package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnassignedFiltersPullsIssuesAndActivityBeforeLimit(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	base := baseTime()

	unassignedPR := testMR(repoID, 1, withMRActivity(base))
	unassignedPR.AssigneesJSON = `[]`
	unassignedPRID := insertTestMRWithOptions(t, d, unassignedPR)
	assignedPR := testMR(repoID, 2, withMRActivity(base.Add(4*time.Minute)))
	assignedPR.AssigneesJSON = `["alice"]`
	assignedPRID := insertTestMRWithOptions(t, d, assignedPR)
	unknownAssigneesPRID := insertTestMRWithOptions(t, d, testMR(
		repoID, 5, withMRActivity(base.Add(8*time.Minute)),
	))

	unassignedIssueID := insertTestIssueWithOptions(t, d, testIssue(
		repoID, 3, withIssueActivity(base.Add(time.Minute)),
	))
	assignedIssue := testIssue(repoID, 4, withIssueActivity(base.Add(5*time.Minute)))
	assignedIssue.AssigneesJSON = `["bob"]`
	assignedIssueID := insertTestIssueWithOptions(t, d, assignedIssue)

	require.NoError(d.UpsertMREvents(ctx, []MREvent{
		{MergeRequestID: unassignedPRID, EventType: "review", Author: "reviewer", CreatedAt: base.Add(2 * time.Minute), DedupeKey: "unassigned-review"},
		{MergeRequestID: assignedPRID, EventType: "review", Author: "reviewer", CreatedAt: base.Add(6 * time.Minute), DedupeKey: "assigned-review"},
		{MergeRequestID: unknownAssigneesPRID, EventType: "review", Author: "reviewer", CreatedAt: base.Add(9 * time.Minute), DedupeKey: "unknown-assignees-review"},
	}))
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{
		{IssueID: unassignedIssueID, EventType: "issue_comment", Author: "commenter", CreatedAt: base.Add(3 * time.Minute), DedupeKey: "unassigned-comment"},
		{IssueID: assignedIssueID, EventType: "issue_comment", Author: "commenter", CreatedAt: base.Add(7 * time.Minute), DedupeKey: "assigned-comment"},
	}))

	pulls, err := d.ListMergeRequests(ctx, ListMergeRequestsOpts{State: "all", Unassigned: true, Limit: 1})
	require.NoError(err)
	require.Len(pulls, 1)
	assert.Equal(1, pulls[0].Number)

	issues, err := d.ListIssues(ctx, ListIssuesOpts{State: "all", Unassigned: true, Limit: 1})
	require.NoError(err)
	require.Len(issues, 1)
	assert.Equal(3, issues[0].Number)

	activity, err := d.ListActivity(ctx, ListActivityOpts{Unassigned: true, Limit: 1})
	require.NoError(err)
	require.Len(activity, 1)
	assert.Equal("issue:3", fmt.Sprintf("%s:%d", activity[0].ItemType, activity[0].ItemNumber))

	projection, err := d.ListCollapsedActivityProjection(ctx, ListActivityProjectionOpts{
		Unassigned:   true,
		Limit:        1,
		SubjectLimit: 1,
	})
	require.NoError(err)
	require.Len(projection.Subjects, 1)
	assert.Equal(WorkspaceSubjectKey{
		RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 3,
	}, projection.Subjects[0].Subject.Key)

	workspaceKeys, err := d.ListUnassignedWorkspaceSubjectKeys(ctx, []WorkspaceSubjectKey{
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 1},
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 2},
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 5},
		{RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 3},
		{RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 4},
	})
	require.NoError(err)
	assert.Equal(map[WorkspaceSubjectKey]struct{}{
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 1}: {},
		{RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 3}:       {},
	}, workspaceKeys)
}

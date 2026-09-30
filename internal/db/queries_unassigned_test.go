package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnassignedFiltersPullsIssuesAndActivityBeforeLimit(t *testing.T) {
	t.Parallel()

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

func TestListUnassignedWorkspaceSubjectKeysSupportsLargeSetsAndHidesRemovedItems(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	base := baseTime()

	live := testMR(repoID, 1, withMRActivity(base))
	live.AssigneesJSON = `[]`
	insertTestMRWithOptions(t, d, live)
	removed := testIssue(repoID, 2, withIssueActivity(base))
	removed.AssigneesJSON = `[]`
	insertTestIssueWithOptions(t, d, removed)
	_, err := d.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES (?, 'issue', 2, 'issue-2', ?, ?, 'removed_upstream')`,
		repoID, base, base,
	)
	require.NoError(err)

	candidates := make([]WorkspaceSubjectKey, 0, 11_001)
	for i := range 11_000 {
		candidates = append(candidates, WorkspaceSubjectKey{
			RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: i + 1,
		})
	}
	candidates = append(candidates, WorkspaceSubjectKey{
		RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 2,
	})

	got, err := d.ListUnassignedWorkspaceSubjectKeys(ctx, candidates)
	require.NoError(err)
	assert.Equal(map[WorkspaceSubjectKey]struct{}{
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 1}: {},
	}, got)
}

func TestInclusionFiltersMatchAnySelectedOption(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	viewer := []RepoViewerLogin{{RepoID: repoID, Login: "viewer"}}
	for number := 1; number <= 3; number++ {
		pr := testMR(repoID, number, withMRActivity(baseTime().Add(time.Duration(number)*time.Minute)))
		issue := testIssue(repoID, number, withIssueActivity(baseTime().Add(time.Duration(number)*time.Minute)))
		pr.AssigneesJSON, issue.AssigneesJSON = `["other"]`, `["other"]`
		if number == 1 {
			pr.Author, issue.Author = "viewer", "viewer"
		}
		if number == 2 {
			pr.AssigneesJSON, issue.AssigneesJSON = `[]`, `[]`
		}
		insertTestMRWithOptions(t, d, pr)
		insertTestIssueWithOptions(t, d, issue)
	}
	pulls, err := d.ListMergeRequests(ctx, ListMergeRequestsOpts{ViewerLogins: viewer, Unassigned: true, Limit: 2})
	require.NoError(t, err)
	require.Len(t, pulls, 2)
	assert.Equal(t, []int{2, 1}, []int{pulls[0].Number, pulls[1].Number})
	issues, err := d.ListIssues(ctx, ListIssuesOpts{ViewerLogins: viewer, Unassigned: true, Limit: 2})
	require.NoError(t, err)
	require.Len(t, issues, 2)
	assert.Equal(t, []int{2, 1}, []int{issues[0].Number, issues[1].Number})
	activity, err := d.ListActivity(ctx, ListActivityOpts{ViewerLogins: viewer, Unassigned: true, Limit: 4})
	require.NoError(t, err)
	require.Len(t, activity, 4)
	for _, item := range activity {
		assert.Contains(t, []int{1, 2}, item.ItemNumber)
	}
	projection, err := d.ListCollapsedActivityProjection(ctx, ListActivityProjectionOpts{ViewerLogins: viewer, Unassigned: true, Limit: 4, SubjectLimit: 4})
	require.NoError(t, err)
	require.Len(t, projection.Subjects, 4)
	for _, subject := range projection.Subjects {
		assert.Contains(t, []int{1, 2}, subject.Subject.Key.ItemNumber)
	}
}

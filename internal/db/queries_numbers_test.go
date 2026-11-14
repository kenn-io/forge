package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListItemsByNumbers(t *testing.T) { //nolint:tparallel // subtests share the seeded pull-request/issue database and number-filter fixture.
	t.Parallel()
	d := openTestDB(t)
	repo := insertTestRepo(t, d, "acme", "widget")
	other := insertTestRepo(t, d, "acme", "other")
	for _, repoID := range []int64{repo, other} {
		for n, state := range []string{"open", "closed", "open"} {
			mr := testMR(repoID, n+1)
			mr.State = MergeRequestState(state)
			mr.LastActivityAt = baseTime().Add(time.Duration(n) * time.Minute)
			_, err := d.UpsertMergeRequest(t.Context(), mr)
			require.NoError(t, err)
			issue := testIssue(repoID, n+1, withIssueActivity(mr.LastActivityAt))
			issue.State = state
			insertTestIssueWithOptions(t, d, issue)
		}
	}
	for _, tc := range []struct {
		name    string
		numbers []int64
		offset  int
		want    []int
	}{
		{"named across states", []int64{1, 2, 99}, 0, []int{2, 1}},
		{"empty", []int64{}, 0, []int{}},
		{"unrestricted", nil, 0, []int{3, 2, 1}},
		{"before pagination", []int64{1, 2, 99}, 1, []int{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filters := []RepoFilter{{PlatformHost: "github.com", RepoPath: "acme/widget"}}
			pulls, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{State: "all", RepoFilters: filters, Numbers: tc.numbers, Offset: tc.offset})
			require.NoError(t, err)
			issues, err := d.ListIssues(t.Context(), ListIssuesOpts{State: "all", RepoFilters: filters, Numbers: tc.numbers, Offset: tc.offset})
			require.NoError(t, err)
			pullNumbers, issueNumbers := []int{}, []int{}
			for _, row := range pulls {
				pullNumbers = append(pullNumbers, row.Number)
				require.Equal(t, repo, row.RepoID)
			}
			for _, row := range issues {
				issueNumbers = append(issueNumbers, row.Number)
				require.Equal(t, repo, row.RepoID)
			}
			assert.Equal(t, tc.want, pullNumbers)
			assert.Equal(t, tc.want, issueNumbers)
		})
	}
}

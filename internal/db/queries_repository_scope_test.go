package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestItemListsAllowedRepoIDs(t *testing.T) {
	database := openTestDB(t)
	tracked := insertTestRepo(t, database, "org-a", "tracked")
	retained := insertTestRepo(t, database, "org-a", "retained")
	for i, repoID := range []int64{tracked, tracked, retained} {
		activity := baseTime().Add(time.Duration(i) * time.Hour)
		insertTestMR(t, database, repoID, 41+i, "Example pull", activity)
		insertTestIssue(t, database, repoID, 41+i, "Example issue", activity)
	}
	// More IDs than SQLite allows bound variables in one statement.
	large := make([]int64, 0, 40_000)
	for id := range int64(40_000) {
		large = append(large, retained+1+id)
	}
	large = append(large, tracked)
	for _, test := range []struct {
		name    string
		allowed []int64
		filter  []RepoFilter
		offset  int
		want    []int
	}{
		{name: "unrestricted", want: []int{43}},
		{name: "empty", allowed: []int64{}, want: []int{}},
		{name: "tracked first page", allowed: []int64{tracked}, want: []int{42}},
		{name: "large tracked set", allowed: large, want: []int{42}},
		{name: "tracked second page", allowed: []int64{tracked}, offset: 1, want: []int{41}},
		{name: "matching filter", allowed: []int64{tracked}, filter: []RepoFilter{{Platform: "github", PlatformHost: "github.com", RepoPath: "org-a/tracked"}}, want: []int{42}},
		{name: "disjoint filter", allowed: []int64{tracked}, filter: []RepoFilter{{Platform: "github", PlatformHost: "github.com", RepoPath: "org-a/retained"}}, want: []int{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			require := require.New(t)
			pulls, err := database.ListMergeRequests(t.Context(), ListMergeRequestsOpts{
				AllowedRepoIDs: test.allowed, RepoFilters: test.filter, Limit: 1, Offset: test.offset,
			})
			require.NoError(err)
			pullNumbers := make([]int, 0, len(pulls))
			for _, pull := range pulls {
				pullNumbers = append(pullNumbers, pull.Number)
			}
			assert.Equal(t, test.want, pullNumbers)
			issues, err := database.ListIssues(t.Context(), ListIssuesOpts{
				AllowedRepoIDs: test.allowed, RepoFilters: test.filter, Limit: 1, Offset: test.offset,
			})
			require.NoError(err)
			issueNumbers := make([]int, 0, len(issues))
			for _, issue := range issues {
				issueNumbers = append(issueNumbers, issue.Number)
			}
			assert.Equal(t, test.want, issueNumbers)
		})
	}
}

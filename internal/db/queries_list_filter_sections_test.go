package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mutually exclusive filters match either; every other selected filter must also match.
func TestListMergeRequestsCombinesFilterSections(t *testing.T) { //nolint:tparallel // subtests share the seeded pull-request database and filter fixture.
	t.Parallel()
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "acme", "widget")
	for _, row := range []struct {
		number             int
		author, ci, review string
		draft, unassigned  bool
	}{
		{number: 1, author: "author-a", ci: "success"},
		{number: 2, author: "author-a", ci: "success", draft: true, unassigned: true},
		{number: 3, author: "author-a", ci: "failure", review: "APPROVED"},
		{number: 4, author: "author-b", ci: "success"},
	} {
		pr := testMR(repoID, row.number, withMRActivity(baseTime().Add(time.Duration(row.number)*time.Minute)))
		pr.Author, pr.CIStatus, pr.ReviewDecision, pr.IsDraft = row.author, row.ci, row.review, row.draft
		pr.AssigneesJSON = `["other"]`
		if row.unassigned {
			pr.AssigneesJSON = `[]`
		}
		insertTestMRWithOptions(t, d, pr)
	}
	for _, tc := range []struct {
		name       string
		opts       ListMergeRequestsOpts
		wantNumber []int
	}{
		{
			name:       "search, ready, and passing CI all hold before the limit",
			opts:       ListMergeRequestsOpts{Search: "author-a", Attributes: []string{"ready", "passed_ci"}, Limit: 1},
			wantNumber: []int{1},
		},
		{
			name:       "draft or ready",
			opts:       ListMergeRequestsOpts{Attributes: []string{"draft", "ready"}},
			wantNumber: []int{4, 3, 2, 1},
		},
		{
			name:       "passing or failing CI",
			opts:       ListMergeRequestsOpts{Attributes: []string{"passed_ci", "failed_ci"}},
			wantNumber: []int{4, 3, 2, 1},
		},
		{
			name:       "ready and approved",
			opts:       ListMergeRequestsOpts{Attributes: []string{"ready", "approved"}},
			wantNumber: []int{3},
		},
		{
			name:       "involves me and ready",
			opts:       ListMergeRequestsOpts{ViewerLogins: []RepoViewerLogin{{RepoID: repoID, Login: "author-a"}}, Attributes: []string{"ready"}},
			wantNumber: []int{3, 1},
		},
		{
			name: "involves me, unassigned, and passing CI",
			opts: ListMergeRequestsOpts{
				ViewerLogins: []RepoViewerLogin{{RepoID: repoID, Login: "author-a"}}, Unassigned: true,
				Attributes: []string{"passed_ci"},
			},
			wantNumber: []int{2},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := d.ListMergeRequests(t.Context(), tc.opts)
			require.NoError(t, err)
			numbers := []int{}
			for _, row := range rows {
				numbers = append(numbers, row.Number)
			}
			assert.Equal(t, tc.wantNumber, numbers)
		})
	}
}

func TestListIssuesCombinesFilterSections(t *testing.T) { //nolint:tparallel // subtests share the seeded issue database and reference/involvement fixture.
	t.Parallel()
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	for _, row := range []struct {
		number                 int
		author                 string
		referenced, unassigned bool
	}{
		{number: 1, author: "viewer", referenced: true},
		{number: 2, author: "viewer", unassigned: true},
		{number: 3, author: "other", referenced: true, unassigned: true},
		{number: 4, author: "viewer", referenced: true, unassigned: true},
	} {
		issue := testIssue(repoID, row.number, withIssueActivity(baseTime().Add(time.Duration(row.number)*time.Minute)))
		issue.Author, issue.AssigneesJSON = row.author, `["other"]`
		if row.unassigned {
			issue.AssigneesJSON = `[]`
		}
		id := insertTestIssueWithOptions(t, d, issue)
		if row.referenced {
			require.NoError(t, d.UpsertIssueEvents(ctx, []IssueEvent{{
				IssueID:      id,
				EventType:    "cross_referenced",
				MetadataJSON: `{"source_type":"PullRequest","source_owner":"acme","source_repo":"client","source_number":42,"source_url":"https://github.com/acme/client/pull/42"}`,
				CreatedAt:    baseTime(),
				DedupeKey:    fmt.Sprintf("cross-reference-%d", row.number),
			}}))
		}
	}
	viewer := []RepoViewerLogin{{RepoID: repoID, Login: "viewer"}}
	for _, tc := range []struct {
		name       string
		opts       ListIssuesOpts
		wantNumber []int
	}{
		{
			name:       "involves me and referenced by a PR",
			opts:       ListIssuesOpts{ViewerLogins: viewer, ReferencedByPR: true},
			wantNumber: []int{4, 1},
		},
		{
			name:       "involves me, unassigned, and referenced by a PR",
			opts:       ListIssuesOpts{ViewerLogins: viewer, Unassigned: true, ReferencedByPR: true},
			wantNumber: []int{4},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := d.ListIssues(ctx, tc.opts)
			require.NoError(t, err)
			numbers := []int{}
			for _, row := range rows {
				numbers = append(numbers, row.Number)
			}
			assert.Equal(t, tc.wantNumber, numbers)
		})
	}
}

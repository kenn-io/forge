package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mergeRequestPlanQueryer struct {
	queryer
	t      *testing.T
	ro     *sql.DB
	index  string
	reject string
}

func (q mergeRequestPlanQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	plan := strings.Join(explainQueryPlan(q.t, q.ro, query, args...), "\n")
	if q.index == "" {
		assert.Contains(q.t, plan, "SCAN p USING INDEX idx_mr_activity")
	} else {
		assert.Contains(q.t, plan, "SEARCH p USING INDEX "+q.index+" ")
	}
	reject := q.reject
	if reject == "" {
		reject = "TEMP B-TREE FOR ORDER BY"
	}
	assert.NotContains(q.t, plan, reject)
	return q.queryer.QueryContext(ctx, query, args...)
}

func TestListMergeRequestsCreatedQueryPlan(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "history")
	insertTestRepo(t, d, "acme", "other")
	for number := 1; number <= 1000; number++ {
		mr := testMR(repoID, number)
		mr.State = "closed"
		if number <= 30 {
			mr.State = "open"
		}
		mr.HeadBranch = "feature/shared"
		if number == 20 {
			mr.HeadBranch = "feature/20"
		}
		mr.CreatedAt = baseTime().Add(time.Duration(number) * time.Minute)
		mr.LastActivityAt = mr.CreatedAt
		_, err := d.UpsertMergeRequest(ctx, mr)
		require.NoError(t, err)
	}
	for _, stats := range []string{"without_stats", "with_stats"} {
		if stats == "with_stats" {
			require.NoError(t, d.Optimize(ctx))
		}
		t.Run(stats, func(t *testing.T) {
			for _, tc := range []struct {
				state, index string
			}{
				{"open", "idx_mr_repo_state_created"},
				{"closed", "idx_mr_repo_created"},
				{"all", "idx_mr_repo_created"},
			} {
				t.Run(tc.state, func(t *testing.T) {
					q := mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB(), index: tc.index}
					opts := ListMergeRequestsOpts{RepoID: repoID, State: tc.state, ProviderState: true, ByCreated: true, Limit: 30}
					_, err := d.listMergeRequests(t.Context(), q, opts)
					require.NoError(t, err)
					opts.HeadBranch = "feature/20"
					q.index = "idx_mr_repo_head"
					_, err = d.listMergeRequests(t.Context(), q, opts)
					require.NoError(t, err)
				})
			}
		})
	}
}

func TestListMergeRequestsPageQueryPlan(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	ctx := t.Context()
	large := insertTestRepo(t, d, "acme", "large")
	small := insertTestRepo(t, d, "acme", "small")
	for number := 1; number <= 300; number++ {
		repoID := large
		if number <= 10 {
			repoID = small
		}
		mr := testMR(repoID, number)
		mr.State = "merged"
		mr.LastActivityAt = baseTime().Add(time.Duration(number/2) * time.Minute)
		if number == 300 {
			mr.State = "open"
			mr.IsLocked = true
		}
		_, err := d.UpsertMergeRequest(ctx, mr)
		require.NoError(t, err)
	}
	require.NoError(t, d.Optimize(ctx))
	for _, state := range []string{"closed", "all"} {
		t.Run(state, func(t *testing.T) {
			assert := assert.New(t)
			mrs, err := d.listMergeRequests(t.Context(), mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB()}, ListMergeRequestsOpts{State: state, Limit: 200, AllowedRepoIDs: []int64{large, small}})
			require.NoError(t, err)
			require.Len(t, mrs, 200)
			assert.Equal(300, mrs[0].Number)
			assert.Equal(101, mrs[199].Number)
		})
		t.Run(state+"/large", func(t *testing.T) {
			mrs, err := d.listMergeRequests(t.Context(), mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB(), index: "idx_mr_repo_activity"}, ListMergeRequestsOpts{
				State: state, Limit: 200, AllowedRepoIDs: []int64{large, small},
				RepoFilters: []RepoFilter{{Platform: "github", PlatformHost: "github.com", RepoPath: "acme/large"}},
			})
			require.NoError(t, err)
			require.Len(t, mrs, 200)
			assert.Equal(t, 300, mrs[0].Number)
			assert.Equal(t, 101, mrs[len(mrs)-1].Number)
		})
	}
	insertMerged := func(t *testing.T, d *DB, repoID int64, numbers ...int) {
		for _, number := range numbers {
			insertTestMRWithOptions(t, d, testMR(repoID, number, withMRState("merged"), withMRActivity(baseTime().Add(time.Duration(number)*time.Minute))))
		}
	}
	t.Run("one repository", func(t *testing.T) {
		d := openTestDB(t)
		repo := insertTestRepo(t, d, "acme", "only")
		for number := 1; number <= 50; number++ {
			insertMerged(t, d, repo, number)
		}
		require.NoError(t, d.Optimize(t.Context()))
		for _, state := range []string{"closed", "all"} {
			mrs, err := d.listMergeRequests(t.Context(), mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB(), index: "idx_mr_repo_activity"}, ListMergeRequestsOpts{
				State: state, Limit: 20, AllowedRepoIDs: []int64{repo},
				RepoFilters: []RepoFilter{{Platform: "github", PlatformHost: "github.com", RepoPath: "acme/only"}},
			})
			require.NoError(t, err)
			require.Len(t, mrs, 20)
			assert.Equal(t, 50, mrs[0].Number)
			assert.Equal(t, 31, mrs[19].Number)
		}
	})
	t.Run("sparse tracked repositories", func(t *testing.T) {
		d := openTestDB(t)
		tracked := []int64{insertTestRepo(t, d, "acme", "small")}
		for i := range 50 {
			tracked = append(tracked, insertTestRepo(t, d, "acme", fmt.Sprintf("empty-%d", i)))
		}
		history := insertTestRepo(t, d, "old", "history")
		for number := 1; number <= 310; number++ {
			repoID := history
			if number <= 10 {
				repoID = tracked[0]
			}
			insertMerged(t, d, repoID, number)
		}
		require.NoError(t, d.Optimize(t.Context()))
		// Untracked history is newer, so a global activity scan walks all of it.
		mrs, err := d.listMergeRequests(t.Context(), mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB(), index: "idx_mr_repo_activity", reject: "SCAN p USING INDEX idx_mr_activity"}, ListMergeRequestsOpts{
			State: "all", Limit: 50, AllowedRepoIDs: tracked,
		})
		require.NoError(t, err)
		require.Len(t, mrs, 10)
		assert.Equal(t, 10, mrs[0].Number)
		assert.Equal(t, 1, mrs[9].Number)
	})
}

type numbersPlanQueryer struct {
	queryer
	t  *testing.T
	ro *sql.DB
}

func (q numbersPlanQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	plan := strings.Join(explainQueryPlan(q.t, q.ro, query, args...), "\n")
	q.t.Log(plan)
	for _, alias := range []string{"p", "i"} {
		if strings.Contains(query, " "+alias+"\n") {
			assert.Contains(q.t, plan, "SEARCH "+alias+" USING ")
			assert.NotContains(q.t, plan, "SCAN "+alias+" ")
			for line := range strings.SplitSeq(plan, "\n") {
				if strings.HasPrefix(line, "SEARCH "+alias+" ") {
					assert.Contains(q.t, line, "(repo_id=? AND number=?)")
				}
			}
		}
	}
	return q.queryer.QueryContext(ctx, query, args...)
}

func TestListItemsByNumbersQueryPlan(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	repo := insertTestRepo(t, d, "acme", "large")
	for number := 1; number <= 300; number++ {
		insertTestMR(t, d, repo, number, "pull", baseTime().Add(time.Duration(number)*time.Minute))
		insertTestIssue(t, d, repo, number, "issue", baseTime().Add(time.Duration(number)*time.Minute))
	}
	var stats int
	err := d.ReadDB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sqlite_stat1 WHERE tbl IN ('forge_merge_requests', 'forge_issues')").Scan(&stats)
	require.NoError(t, err)
	require.Zero(t, stats)
	filters := []RepoFilter{{Platform: "github", PlatformHost: "github.com", RepoPath: "acme/large"}}
	numbers := []int64{1, 2}
	t.Run("pulls", func(t *testing.T) {
		rows, err := d.listMergeRequests(t.Context(), numbersPlanQueryer{d.roStmts, t, d.ReadDB()}, ListMergeRequestsOpts{
			State: "all", RepoFilters: filters, Numbers: numbers, Limit: 200,
		})
		require.NoError(t, err)
		require.Len(t, rows, 2)
		assert.Equal(t, 2, rows[0].Number)
		assert.Equal(t, 1, rows[1].Number)
	})
	t.Run("issues", func(t *testing.T) {
		rows, err := d.listIssues(t.Context(), numbersPlanQueryer{d.roStmts, t, d.ReadDB()}, ListIssuesOpts{
			State: "all", RepoFilters: filters, Numbers: numbers, Limit: 200,
		})
		require.NoError(t, err)
		require.Len(t, rows, 2)
		assert.Equal(t, 2, rows[0].Number)
		assert.Equal(t, 1, rows[1].Number)
	})
	t.Run("workflow", func(t *testing.T) {
		rows, next, err := d.listItemWorkflowStates(t.Context(), numbersPlanQueryer{d.roStmts, t, d.ReadDB()}, ListWorkflowStatesOpts{
			RepoFilters: filters, Numbers: numbers, IncludeClosed: true, Limit: 200,
		})
		require.NoError(t, err)
		require.Len(t, rows, 4)
		assert.Empty(t, next)
		assert.Equal(t, []int{2, 2, 1, 1}, []int{rows[0].Number, rows[1].Number, rows[2].Number, rows[3].Number})
	})
}

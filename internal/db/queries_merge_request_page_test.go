package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mergeRequestPlanQueryer struct {
	queryer
	t     *testing.T
	ro    *sql.DB
	index string
}

func (q mergeRequestPlanQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	plan := strings.Join(explainQueryPlan(q.t, q.ro, query, args...), "\n")
	if q.index == "" {
		assert.Contains(q.t, plan, "SCAN p USING INDEX idx_mr_activity")
	} else {
		assert.Contains(q.t, plan, "SEARCH p USING INDEX "+q.index+" ")
	}
	assert.NotContains(q.t, plan, "TEMP B-TREE FOR ORDER BY")
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
}

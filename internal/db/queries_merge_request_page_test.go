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
		} else if number <= 60 {
			// Exercise both ways a provider PR can count as merged.
			if number%2 == 0 {
				mr.State = "merged"
			} else {
				mr.MergedAt = new(baseTime())
			}
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
				first, last  int
			}{
				{"open", "idx_mr_repo_open_created", 30, 1},
				{"merged", "idx_mr_repo_merged_created", 60, 31},
				{"closed", "idx_mr_repo_created", 1000, 971},
				{"all", "idx_mr_repo_created", 1000, 971},
			} {
				t.Run(tc.state, func(t *testing.T) {
					q := mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB(), index: tc.index}
					opts := ListMergeRequestsOpts{RepoID: repoID, State: tc.state, ProviderState: true, ByCreated: true, Limit: 30}
					mrs, err := d.listMergeRequests(t.Context(), q, opts)
					require.NoError(t, err)
					require.Len(t, mrs, 30)
					assert.Equal(t, tc.first, mrs[0].Number)
					assert.Equal(t, tc.last, mrs[29].Number)
					opts.HeadBranch = "feature/20"
					q.index = "idx_mr_repo_head"
					mrs, err = d.listMergeRequests(t.Context(), q, opts)
					require.NoError(t, err)
					if tc.state == "open" || tc.state == "all" {
						require.Len(t, mrs, 1)
						assert.Equal(t, 20, mrs[0].Number)
					} else {
						assert.Empty(t, mrs)
					}
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
	}
}

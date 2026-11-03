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
	t            *testing.T
	ro           *sql.DB
	repoFiltered bool
}

func (q mergeRequestPlanQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	plan := strings.Join(explainQueryPlan(q.t, q.ro, query, args...), "\n")
	if q.repoFiltered {
		assert.NotContains(q.t, plan, "SCAN p USING INDEX idx_mr_activity", "a small repo must not scan the full history")
		assert.Contains(q.t, plan, "SEARCH p USING INDEX idx_mr_repo_state_activity (repo_id=?)")
	} else {
		assert.Contains(q.t, plan, "SCAN p USING INDEX idx_mr_activity")
		assert.NotContains(q.t, plan, "TEMP B-TREE FOR ORDER BY")
	}
	return q.queryer.QueryContext(ctx, query, args...)
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
			for i := 1; i < len(mrs); i++ {
				assert.Equal(mrs[i-1].Number-1, mrs[i].Number, "activity ties must sort by descending ID")
			}
		})
	}
	mrs, err := d.listMergeRequests(ctx, mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB(), repoFiltered: true}, ListMergeRequestsOpts{State: "closed", Limit: 200, AllowedRepoIDs: []int64{large, small}, RepoFilters: []RepoFilter{{RepoOwner: "acme", RepoName: "small"}}})
	require.NoError(t, err)
	require.Len(t, mrs, 10)
	assert.Equal(t, 10, mrs[0].Number)
	assert.Equal(t, 1, mrs[9].Number)
}

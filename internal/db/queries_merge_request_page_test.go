package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func activityPageProbeQueries(d *DB) []string {
	d.roStmts.mu.Lock()
	defer d.roStmts.mu.Unlock()
	var queries []string
	for query := range d.roStmts.entries {
		if strings.HasPrefix(query, "WITH recent AS") {
			queries = append(queries, query)
		}
	}
	return queries
}

func seedRecentSearchPage(t *testing.T, d *DB) []int64 {
	t.Helper()
	var repos []int64
	for n := range 60 {
		repos = append(repos, insertTestRepo(t, d, "acme", fmt.Sprintf("repo-%d", n)))
	}
	for _, table := range []string{"forge_merge_requests", "forge_issues"} {
		execPageFixtureNumbers(t, d, 6000, fmt.Sprintf(`
			INSERT INTO %s (repo_id, platform_id, number, title, author, state, created_at, updated_at, last_activity_at)
			SELECT ?1 + (n %% 60), n, n, 'change', CASE WHEN n %% 50 = 0 THEN 'user-a' ELSE 'other' END,
			       'closed', '2024-01-15', '2024-01-15', datetime('2024-01-15', '+' || n || ' minutes') FROM numbers`, table), repos[0])
	}
	require.NoError(t, d.Optimize(t.Context()))
	return repos
}

func TestListMergeRequestsAttributePageSkipsProbe(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	repo := insertTestRepo(t, d, "acme", "tracked")
	insertTestMR(t, d, repo, 1, "approved", baseTime())
	_, err := d.WriteDB().ExecContext(t.Context(), "UPDATE forge_merge_requests SET state = 'closed', review_decision = 'APPROVED'")
	require.NoError(t, err)
	_, err = d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{
		State: "all", Limit: 50, AllowedRepoIDs: []int64{repo}, Attributes: []string{"approved"},
	})
	require.NoError(t, err)
	assert.Empty(t, activityPageProbeQueries(d), "attribute pages already have an activity plan")
}

// execPageFixtureNumbers supplies numbers 1 through count to a fixture insert.
func execPageFixtureNumbers(t *testing.T, d *DB, count int, insert string, args ...any) {
	t.Helper()
	_, err := d.WriteDB().ExecContext(t.Context(), fmt.Sprintf(`
		WITH RECURSIVE numbers(n) AS (VALUES(1) UNION ALL SELECT n + 1 FROM numbers WHERE n < %d)
		%s`, count, insert), args...)
	require.NoError(t, err)
}

func TestListActivityIndexMigration(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.db")
	raw := migrateToVersionForTest(t, path, 94)
	require.NoError(t, raw.Close())
	for _, version := range []uint{95, 94, 95} {
		raw = migrateToVersionForTest(t, path, version)
		if version == 95 {
			for _, index := range []string{"idx_mr_activity", "idx_issues_activity"} {
				var count int
				require.NoError(t, raw.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_index_xinfo(?) WHERE key = 1", index).Scan(&count))
				assert.Equal(t, 3, count, index)
			}
		} else {
			assertIndexForTest(t, raw, "forge_merge_requests", "idx_mr_activity", []string{"last_activity_at", "id"}, false)
			var count int
			require.NoError(t, raw.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_issues_activity'").Scan(&count))
			assert.Zero(t, count)
		}
		require.NoError(t, raw.Close())
	}
}

func TestListPagesTrackedRepositoryPlans(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		tracked       int
		rowsPerRepo   int
		untrackedRows int
		activityWalk  bool
		search        string
		partial       bool
		offset        int
		limit         int
	}{
		{"newer_untracked/negative_offset/tracked=30", 30, 200, 60000, false, "", false, -51, 50},
		{"dense_history/tracked=27", 27, 2000, 0, true, "", false, 0, 50},
		{"recent_search/tracked=60", 60, 100, 0, true, "user-a", false, 0, 50},
		{"partial_page/tracked=51", 51, 10, 300, false, "", true, 0, 50},
		{"unlimited_issues/tracked=2", 2, 100, 6000, false, "", false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := openTestDB(t)
			var repos []int64
			var filters []RepoFilter
			if tc.search != "" {
				repos = seedRecentSearchPage(t, d)
				for repo := range tc.tracked {
					filters = append(filters, RepoFilter{Platform: "github", PlatformHost: "github.com", RepoPath: fmt.Sprintf("acme/repo-%d", repo)})
				}
			} else {
				for repo := range tc.tracked + 1 {
					if repo == tc.tracked && tc.untrackedRows == 0 {
						break
					}
					name := fmt.Sprintf("repo-%d", repo)
					repoID := insertTestRepo(t, d, "acme", name)
					count := tc.rowsPerRepo
					if repo < tc.tracked {
						repos = append(repos, repoID)
						filters = append(filters, RepoFilter{Platform: "github", PlatformHost: "github.com", RepoPath: "acme/" + name})
					} else {
						// This active repository is outside the allow-list, and all its rows are newer.
						count = tc.untrackedRows
					}
					if tc.partial && repo > 0 && repo < tc.tracked {
						continue
					}
					for _, table := range []string{"forge_merge_requests", "forge_issues"} {
						if (tc.partial && table == "forge_issues") || (tc.limit == 0 && table == "forge_merge_requests") {
							continue
						}
						execPageFixtureNumbers(t, d, count, fmt.Sprintf(`
							INSERT INTO %s (repo_id, platform_id, number, state, created_at, updated_at, last_activity_at)
							SELECT ?1, n, ?2 + n, 'closed', '2024-01-15 12:00:00', '2024-01-15 12:00:00',
							       datetime('2024-01-15', '+' || (?2 + n) || ' minutes') FROM numbers`, table), repoID, repo*tc.rowsPerRepo)
					}
				}
			}
			if tc.activityWalk {
				untracked := insertTestRepo(t, d, "acme", "untracked")
				for _, table := range []string{"forge_merge_requests", "forge_issues"} {
					// Interleave excluded rows, including matches for the search case.
					_, err := d.WriteDB().ExecContext(t.Context(), fmt.Sprintf(`
						INSERT INTO %s (repo_id, platform_id, number, title, author, state, created_at, updated_at, last_activity_at)
						SELECT ?, number, 1000000 + number, title, author, state, created_at, updated_at,
						       datetime(last_activity_at, '+1 second') FROM %s WHERE number %% 100 = 0`, table, table), untracked)
					require.NoError(t, err)
				}
			}
			count, first, last := 50, tc.tracked*tc.rowsPerRepo, tc.tracked*tc.rowsPerRepo-49
			if tc.limit == 0 {
				count, last = tc.tracked*tc.rowsPerRepo, 1
			}
			if tc.search != "" {
				first, last = 6000, 3550
			}
			if tc.partial {
				count, first, last = 10, 10, 1
			}
			require.NoError(t, d.Optimize(t.Context()))
			states := []string{"all", "closed"}
			if tc.partial || tc.limit == 0 {
				states = states[:1]
			}
			for _, state := range states {
				t.Run(state, func(t *testing.T) {
					if tc.limit > 0 {
						t.Run("merge_requests", func(t *testing.T) {
							q := mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB()}
							if !tc.activityWalk {
								q.reject = "SCAN p USING INDEX idx_mr_activity"
							}
							opts := ListMergeRequestsOpts{State: state, AllowedRepoIDs: repos, Search: tc.search, Limit: tc.limit, Offset: tc.offset}
							pulls, err := d.listMergeRequests(t.Context(), q, opts)
							require.NoError(t, err)
							require.Len(t, pulls, count)
							assert.Equal(t, first, pulls[0].Number)
							assert.Equal(t, last, pulls[count-1].Number)
							// Repository filters retain the old query without the global activity page.
							opts.AllowedRepoIDs, opts.RepoFilters = nil, filters
							baseline, err := d.ListMergeRequests(t.Context(), opts)
							require.NoError(t, err)
							assert.Equal(t, baseline, pulls)
						})
					}
					if !tc.partial {
						t.Run("issues", func(t *testing.T) {
							q := mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB()}
							if !tc.activityWalk {
								q.reject = "SCAN i USING INDEX idx_issues_activity"
							}
							opts := ListIssuesOpts{State: state, AllowedRepoIDs: repos, Search: tc.search, Limit: tc.limit, Offset: tc.offset}
							issues, err := d.listIssues(t.Context(), q, opts)
							require.NoError(t, err)
							require.Len(t, issues, count)
							assert.Equal(t, first, issues[0].Number)
							assert.Equal(t, last, issues[count-1].Number)
							opts.AllowedRepoIDs, opts.RepoFilters = nil, filters
							baseline, err := d.ListIssues(t.Context(), opts)
							require.NoError(t, err)
							assert.Equal(t, baseline, issues)
						})
					}
				})
			}
			if tc.activityWalk && tc.search == "" {
				queries := activityPageProbeQueries(d)
				require.Len(t, queries, 2)
				probeArgs := []any{50 * activityPageScanFactor}
				repoIDCondition("recent.repo_id", repos, &probeArgs)
				probeArgs = append(probeArgs, 50)
				for _, query := range queries {
					plan := strings.Join(explainQueryPlan(t, d.ReadDB(), query, probeArgs...), "\n")
					assert.Contains(t, plan, "CO-ROUTINE recent")
					assert.NotContains(t, plan, "MATERIALIZE")
					assert.NotContains(t, plan, "TEMP B-TREE")
				}
			}
		})
	}
}

type mergeRequestPlanQueryer struct {
	queryer
	t       *testing.T
	ro      *sql.DB
	index   string
	reject  string
	starred bool
}

func (q mergeRequestPlanQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	plan := strings.Join(explainQueryPlan(q.t, q.ro, query, args...), "\n")
	alias, activityIndex := "p", "idx_mr_activity"
	if strings.Contains(query, "forge_issues i") {
		alias, activityIndex = "i", "idx_issues_activity"
	}
	if q.starred {
		assert.Regexp(q.t, `^(SCAN|SEARCH) s\b`, plan)
		assert.Regexp(q.t, `SEARCH `+alias+` USING INDEX \S+ \(repo_id=\? AND number=\?\)`, plan)
	} else if q.index == "" && q.reject != "" {
		assert.Regexp(q.t, `SEARCH `+alias+` USING (COVERING )?INDEX \S+ \(repo_id=`, plan)
	} else if q.index == "" {
		assert.Contains(q.t, plan, "SCAN "+alias+" USING INDEX "+activityIndex)
	} else {
		assert.Contains(q.t, plan, "SEARCH "+alias+" USING INDEX "+q.index+" ")
	}
	reject := q.reject
	if reject == "" && q.starred {
		reject = "SCAN " + alias + " "
	} else if reject == "" {
		reject = "TEMP B-TREE FOR ORDER BY"
	}
	assert.NotContains(q.t, plan, reject)
	return q.queryer.QueryContext(ctx, query, args...)
}

func TestListStarredItemsQueryPlan(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	repo := insertTestRepo(t, d, "acme", "tracked")
	otherRepo := insertTestRepo(t, d, "acme", "other")
	for _, repoID := range []int64{repo, otherRepo} {
		for _, table := range []string{"forge_merge_requests", "forge_issues"} {
			state := "CASE n % 3 WHEN 1 THEN 'open' WHEN 2 THEN 'closed' ELSE 'merged' END"
			if table == "forge_issues" {
				state = "CASE n % 2 WHEN 1 THEN 'open' ELSE 'closed' END"
			}
			execPageFixtureNumbers(t, d, 300, fmt.Sprintf(`
				INSERT INTO %s (repo_id, platform_id, number, state, created_at, updated_at, last_activity_at)
				SELECT ?1, n, n, %s, '2024-01-15', '2024-01-15',
				       datetime('2024-01-15', '+' || (n / 2) || ' minutes') FROM numbers`, table, state), repoID)
		}
		for _, kind := range []string{"pr", "issue"} {
			for number := 1; number <= 6; number++ {
				require.NoError(t, d.SetStarred(t.Context(), kind, repoID, number))
			}
		}
	}
	require.NoError(t, d.Optimize(t.Context()))
	for _, tc := range []struct {
		kind, state string
		want        []int
	}{
		{"pr", "", []int{4, 1}},
		{"pr", "open", []int{4, 1}},
		{"pr", "closed", []int{6, 5, 3, 2}},
		{"pr", "merged", []int{6, 3}},
		{"pr", "all", []int{6, 5, 4, 3, 2, 1}},
		{"issue", "", []int{5, 3, 1}},
		{"issue", "open", []int{5, 3, 1}},
		{"issue", "closed", []int{6, 4, 2}},
		{"issue", "all", []int{6, 5, 4, 3, 2, 1}},
	} {
		t.Run(tc.kind+"/"+tc.state, func(t *testing.T) {
			assert := assert.New(t)
			q := mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB(), starred: true}
			var numbers []int
			if tc.kind == "pr" {
				rows, err := d.listMergeRequests(t.Context(), q, ListMergeRequestsOpts{
					State: tc.state, Starred: true, AllowedRepoIDs: []int64{repo}, Limit: 50,
				})
				require.NoError(t, err)
				for _, row := range rows {
					assert.Equal(repo, row.RepoID)
					assert.True(row.Starred)
					numbers = append(numbers, row.Number)
				}
			} else {
				rows, err := d.listIssues(t.Context(), q, ListIssuesOpts{
					State: tc.state, Starred: true, AllowedRepoIDs: []int64{repo}, Limit: 50,
				})
				require.NoError(t, err)
				for _, row := range rows {
					assert.Equal(repo, row.RepoID)
					assert.True(row.Starred)
					numbers = append(numbers, row.Number)
				}
			}
			assert.Equal(tc.want, numbers)
		})
	}
}

func TestListPagesManyRepositories(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	var repos []int64
	var filters []RepoFilter
	for repo := range 60 {
		repoID := insertTestRepo(t, d, "acme", fmt.Sprintf("repo-%d", repo))
		repos = append(repos, repoID)
		filters = append(filters, RepoFilter{Platform: "github", PlatformHost: "github.com", RepoPath: fmt.Sprintf("acme/repo-%d", repo)})
		execPageFixtureNumbers(t, d, 2000, `
			INSERT INTO forge_merge_requests (repo_id, platform_id, number, state, created_at, updated_at, last_activity_at)
			SELECT ?1, n, n, CASE WHEN n <= 5 THEN 'open' ELSE 'closed' END,
			       '2024-01-15 12:00:00', '2024-01-15 12:00:00', datetime('2024-01-15', '+' || (?2 + n) || ' minutes') FROM numbers`, repoID, repo*2000)
		count := 40
		if repo == 59 {
			count = 400 // The last tracked repository has more issue history.
		}
		execPageFixtureNumbers(t, d, count, `
			INSERT INTO forge_issues (repo_id, platform_id, number, state, assignees_json, created_at, updated_at, last_activity_at)
			SELECT ?1, n, ?2 + n, CASE WHEN n <= 5 THEN 'open' ELSE 'closed' END, '[]',
			       '2024-01-15 12:00:00', '2024-01-15 12:00:00', datetime('2024-01-15', '+' || ((?2 + n) / 2) || ' minutes') FROM numbers`, repoID, repo*40)
	}
	_, err := d.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_labels (repo_id, name, color, updated_at)
		SELECT r.id, 'other-' || n.value, 'abcdef', '2024-01-15 12:00:00' FROM forge_repos r CROSS JOIN json_each('[0,1,2,3,4,5,6,7,8,9]') n;
		INSERT INTO forge_merge_request_labels (merge_request_id, label_id)
		SELECT p.id, l.id FROM forge_merge_requests p JOIN forge_labels l ON l.repo_id = p.repo_id AND l.name = 'other-' || (p.number % 10) WHERE p.number % 3 = 0`)
	require.NoError(t, err)
	for number := 1; number <= 9; number++ {
		require.NoError(t, d.SetStarred(t.Context(), "pr", repos[0], number))
	}
	require.NoError(t, d.SetStarred(t.Context(), "pr", repos[59], 7))
	pull, err := d.GetMergeRequestByRepoIDAndNumber(t.Context(), repos[59], 7)
	require.NoError(t, err)
	require.NoError(t, d.ReplaceMergeRequestLabels(t.Context(), repos[59], pull.ID, []Label{{Name: "selected", Color: "abcdef"}}))
	_, err = d.WriteDB().ExecContext(t.Context(), "ANALYZE")
	require.NoError(t, err)
	require.NoError(t, d.Optimize(t.Context()))
	for _, tc := range []struct {
		table, filter       string
		limit, count, first int
		index               string
	}{
		{"issues", "repository", 1, 1, 40, "idx_issues_repo_created"},
		{"merge_requests", "open", 1, 1, 5, "idx_mr_repo_state_activity"},
		{"issues", "open", 1, 1, 2365, "idx_issues_repo_state_activity"},
		{"merge_requests", "starred", 50, 10, 7, ""},
		{"merge_requests", "label", 50, 1, 7, "sqlite_autoindex_forge_merge_requests_2"},
	} {
		t.Run(tc.table+"/"+tc.filter, func(t *testing.T) {
			assert := assert.New(t)
			pullOpts := ListMergeRequestsOpts{State: "all", Limit: tc.limit, AllowedRepoIDs: repos}
			issueOpts := ListIssuesOpts{State: "all", Limit: tc.limit, AllowedRepoIDs: repos}
			switch tc.filter {
			case "repository":
				pullOpts.RepoFilters, issueOpts.RepoFilters = filters[:1], filters[:1]
			case "open":
				pullOpts.State, issueOpts.State = "open", "open"
			case "starred":
				pullOpts.Starred, issueOpts.Starred = true, true
			case "label":
				pullOpts.Label = "selected"
			}
			q := mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB(), index: tc.index, starred: tc.filter == "starred"}
			if tc.table == "merge_requests" {
				if !q.starred {
					q.reject = "SCAN p USING INDEX idx_mr_activity"
				}
				pulls, err := d.listMergeRequests(t.Context(), q, pullOpts)
				require.NoError(t, err)
				require.Len(t, pulls, tc.count)
				assert.Equal(tc.first, pulls[0].Number)
				switch tc.filter {
				case "starred", "label":
					assert.Equal(pull.ID, pulls[0].ID)
				default:
					assert.Equal(repos[59], pulls[0].RepoID)
				}
			} else {
				q.reject = "SCAN i USING INDEX idx_issues_activity"
				issues, err := d.listIssues(t.Context(), q, issueOpts)
				require.NoError(t, err)
				require.Len(t, issues, tc.count)
				assert.Equal(tc.first, issues[0].Number)
			}
		})
	}
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
		mr.AssigneesJSON = `["other"]`
		if number == 299 || number == 297 || number == 295 {
			mr.ReviewDecision, mr.AssigneesJSON, mr.CIStatus = "APPROVED", `[]`, "success"
		}
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
		t.Run(state+"/attributes", func(t *testing.T) {
			mrs, err := d.listMergeRequests(t.Context(), mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB()}, ListMergeRequestsOpts{
				State: state, Unassigned: true, Limit: 2, Offset: 1, AllowedRepoIDs: []int64{large, small},
				Attributes: []string{"approved", "failed_ci", "passed_ci"},
			})
			require.NoError(t, err)
			require.Len(t, mrs, 2)
			assert.Equal(t, []int{297, 295}, []int{mrs[0].Number, mrs[1].Number})
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
		t.Run(state+"/large/attributes", func(t *testing.T) {
			mrs, err := d.listMergeRequests(t.Context(), mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB(), index: "idx_mr_repo_activity"}, ListMergeRequestsOpts{
				State: state, Limit: 2, Offset: 1, AllowedRepoIDs: []int64{large, small},
				RepoFilters: []RepoFilter{{Platform: "github", PlatformHost: "github.com", RepoPath: "acme/large"}},
				Attributes:  []string{"approved", "passed_ci"},
			})
			require.NoError(t, err)
			require.Len(t, mrs, 2)
			assert.Equal(t, []int{297, 295}, []int{mrs[0].Number, mrs[1].Number})
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
}

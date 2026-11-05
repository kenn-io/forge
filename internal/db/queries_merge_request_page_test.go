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

func TestListActivityIndexMigration(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.db")
	raw := migrateToVersionForTest(t, path, 93)
	require.NoError(t, raw.Close())
	d, err := OpenPreparedForTest(path)
	require.NoError(t, err)
	repo := insertTestRepo(t, d, "acme", "history")
	insertTestMR(t, d, repo, 1, "older", baseTime())
	insertTestMR(t, d, repo, 2, "newer", baseTime().Add(time.Hour))
	insertTestIssue(t, d, repo, 1, "older", baseTime())
	insertTestIssue(t, d, repo, 2, "newer", baseTime().Add(time.Hour))
	require.NoError(t, d.Close())
	for _, version := range []uint{94, 93, 94} {
		raw = migrateToVersionForTest(t, path, version)
		if version == 94 {
			assertIndexForTest(t, raw, "forge_issues", "idx_issues_activity", []string{"last_activity_at", "id", "repo_id"}, false)
		}
		definition := "CREATE INDEX idx_mr_activity ON forge_merge_requests(last_activity_at DESC, id DESC)"
		if version == 94 {
			definition = "CREATE INDEX idx_mr_activity ON forge_merge_requests(last_activity_at DESC, id DESC, +repo_id)"
		}
		var actualDefinition string
		require.NoError(t, raw.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_mr_activity'").Scan(&actualDefinition))
		assert.Equal(t, definition, actualDefinition)
		require.NoError(t, raw.Close())
		d, err = Open(path)
		require.NoError(t, err)
		issues, err := d.ListIssues(t.Context(), ListIssuesOpts{State: "all", AllowedRepoIDs: []int64{repo, repo + 1}, Limit: 2})
		require.NoError(t, err)
		require.Len(t, issues, 2)
		assert.Equal(t, []string{"newer", "older"}, []string{issues[0].Title, issues[1].Title})
		pulls, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{State: "all", AllowedRepoIDs: []int64{repo, repo + 1}, Limit: 2})
		require.NoError(t, err)
		require.Len(t, pulls, 2)
		assert.Equal(t, []string{"newer", "older"}, []string{pulls[0].Title, pulls[1].Title})
		require.NoError(t, d.Close())
	}
}

func TestListActivityIndexDefinitions(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	for _, tc := range []struct {
		name, index, table string
	}{
		{"merge_requests", "idx_mr_activity", "forge_merge_requests"},
		{"issues", "idx_issues_activity", "forge_issues"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var definition string
			require.NoError(t, d.ReadDB().QueryRowContext(t.Context(), "SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?", tc.index).Scan(&definition))
			assert.Equal(t, "CREATE INDEX "+tc.index+" ON "+tc.table+"(last_activity_at DESC, id DESC, +repo_id)", definition)
		})
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
	}{
		{"newer_untracked/tracked=30", 30, 200, 60000, false},
		{"dense_history/tracked=27", 27, 2000, 0, false},
		{"dense_history/tracked=28", 28, 2000, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := openTestDB(t)
			var repos []int64
			var filters []RepoFilter
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
				for _, table := range []string{"forge_merge_requests", "forge_issues"} {
					_, err := d.WriteDB().ExecContext(t.Context(), fmt.Sprintf(`
						WITH RECURSIVE numbers(n) AS (VALUES(1) UNION ALL SELECT n + 1 FROM numbers WHERE n < ?1)
						INSERT INTO %s (repo_id, platform_id, number, state, created_at, updated_at, last_activity_at)
						SELECT ?2, n, ?3 + n, 'closed', '2024-01-15 12:00:00', '2024-01-15 12:00:00',
						       datetime('2024-01-15', '+' || (?3 + n) || ' minutes') FROM numbers`, table), count, repoID, repo*tc.rowsPerRepo)
					require.NoError(t, err)
				}
			}
			require.NoError(t, d.Optimize(t.Context()))
			for _, state := range []string{"all", "closed"} {
				t.Run(state, func(t *testing.T) {
					t.Run("merge_requests", func(t *testing.T) {
						q := mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB()}
						if !tc.activityWalk {
							q.index, q.reject = "sqlite_autoindex_forge_merge_requests_2", "SCAN p USING INDEX idx_mr_activity"
						}
						opts := ListMergeRequestsOpts{State: state, AllowedRepoIDs: repos, Limit: 50}
						pulls, err := d.listMergeRequests(t.Context(), q, opts)
						require.NoError(t, err)
						require.Len(t, pulls, 50)
						assert.Equal(t, tc.tracked*tc.rowsPerRepo, pulls[0].Number)
						assert.Equal(t, tc.tracked*tc.rowsPerRepo-49, pulls[49].Number)
						// Repository filters retain the old query without the global activity page.
						opts.AllowedRepoIDs, opts.RepoFilters = nil, filters
						baseline, err := d.ListMergeRequests(t.Context(), opts)
						require.NoError(t, err)
						assert.Equal(t, baseline, pulls)
					})
					t.Run("issues", func(t *testing.T) {
						q := mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB()}
						if !tc.activityWalk {
							q.index, q.reject = "idx_issues_repo_created", "SCAN i USING INDEX idx_issues_activity"
							if tc.tracked == 27 && state == "closed" {
								q.index = "idx_issues_repo_state_activity"
							}
						}
						opts := ListIssuesOpts{State: state, AllowedRepoIDs: repos, Limit: 50}
						issues, err := d.listIssues(t.Context(), q, opts)
						require.NoError(t, err)
						require.Len(t, issues, 50)
						assert.Equal(t, tc.tracked*tc.rowsPerRepo, issues[0].Number)
						assert.Equal(t, tc.tracked*tc.rowsPerRepo-49, issues[49].Number)
						opts.AllowedRepoIDs, opts.RepoFilters = nil, filters
						baseline, err := d.ListIssues(t.Context(), opts)
						require.NoError(t, err)
						assert.Equal(t, baseline, issues)
					})
				})
			}
		})
	}
}

type mergeRequestPlanQueryer struct {
	queryer
	t      *testing.T
	ro     *sql.DB
	index  string
	reject string
}

func (q mergeRequestPlanQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	plan := strings.Join(explainQueryPlan(q.t, q.ro, query, args...), "\n")
	alias, activityIndex := "p", "idx_mr_activity"
	if strings.Contains(query, "FROM forge_issues i") {
		alias, activityIndex = "i", "idx_issues_activity"
	}
	if q.index == "" {
		assert.Contains(q.t, plan, "SCAN "+alias+" USING INDEX "+activityIndex)
	} else {
		assert.Contains(q.t, plan, "SEARCH "+alias+" USING INDEX "+q.index+" ")
	}
	reject := q.reject
	if reject == "" {
		reject = "TEMP B-TREE FOR ORDER BY"
	}
	assert.NotContains(q.t, plan, reject)
	return q.queryer.QueryContext(ctx, query, args...)
}

func TestListPagesFewTrackedRepositories(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	d := openTestDB(t)
	var tracked []int64
	for repo := range 60 {
		repoID := insertTestRepo(t, d, "acme", fmt.Sprintf("repo-%d", repo))
		count := 1000 // Active untracked history is newer than every tracked row.
		if repo < 3 {
			count = 200
			tracked = append(tracked, repoID)
		}
		for _, table := range []string{"forge_merge_requests", "forge_issues"} {
			_, err := d.WriteDB().ExecContext(t.Context(), fmt.Sprintf(`
				WITH RECURSIVE numbers(n) AS (VALUES(1) UNION ALL SELECT n + 1 FROM numbers WHERE n < ?1)
				INSERT INTO %s (repo_id, platform_id, number, state, created_at, updated_at, last_activity_at)
				SELECT ?2, n, ?3 + n, 'closed', '2024-01-15 12:00:00', '2024-01-15 12:00:00',
				       datetime('2024-01-15', '+' || (?3 + n) || ' minutes') FROM numbers`, table), count, repoID, repo*200)
			require.NoError(t, err)
		}
	}
	require.NoError(t, d.Optimize(t.Context()))
	pulls, err := d.listMergeRequests(t.Context(), mergeRequestPlanQueryer{
		queryer: d.roStmts, t: t, ro: d.ReadDB(), index: "sqlite_autoindex_forge_merge_requests_2", reject: "SCAN p",
	}, ListMergeRequestsOpts{State: "all", AllowedRepoIDs: tracked, Limit: 50})
	require.NoError(t, err)
	require.Len(t, pulls, 50)
	assert.Equal(600, pulls[0].Number)
	assert.Equal(551, pulls[49].Number)
	issues, err := d.listIssues(t.Context(), mergeRequestPlanQueryer{
		queryer: d.roStmts, t: t, ro: d.ReadDB(), index: "idx_issues_repo_created", reject: "SCAN i",
	}, ListIssuesOpts{State: "all", AllowedRepoIDs: tracked, Limit: 50})
	require.NoError(t, err)
	require.Len(t, issues, 50)
	assert.Equal(600, issues[0].Number)
	assert.Equal(551, issues[49].Number)
}

func TestListPagesManyRepositories(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	var repos []int64
	var filters []RepoFilter
	for repo := range 60 {
		repoID := insertTestRepo(t, d, "acme", fmt.Sprintf("repo-%d", repo))
		count := 40
		repos = append(repos, repoID)
		filters = append(filters, RepoFilter{Platform: "github", PlatformHost: "github.com", RepoPath: fmt.Sprintf("acme/repo-%d", repo)})
		if repo == 59 {
			count = 400 // Retained history from a repository removed from tracking.
		}
		for item := 1; item <= count; item++ {
			number := repo*40 + item
			activity := baseTime().Add(time.Duration(number/2) * time.Minute)
			state := "closed"
			if item <= 5 {
				state = "open"
			}
			mr := testMR(repoID, number, withMRState(MergeRequestState(state)), withMRActivity(activity))
			mr.AssigneesJSON = "[]"
			insertTestMRWithOptions(t, d, mr)
			issue := testIssue(repoID, number, withIssueActivity(activity))
			issue.State, issue.AssigneesJSON = state, "[]"
			insertTestIssueWithOptions(t, d, issue)
		}
	}
	for _, kind := range []string{"merge_request", "issue"} {
		_, err := d.WriteDB().ExecContext(t.Context(), `INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id, provider_created_at, provider_updated_at, lifecycle_state
		) VALUES (?, ?, 1, 'removed-1', ?, ?, 'removed_upstream')`, repos[0], kind, baseTime(), baseTime())
		require.NoError(t, err)
	}
	require.NoError(t, d.Optimize(t.Context()))
	for _, state := range []string{"closed", "all"} {
		for _, search := range []string{"", "author"} {
			for _, tracked := range []int{activityPageMinRepos, 59, 60} {
				for _, offset := range []int{0, 7} {
					for _, unassigned := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/search=%s/tracked=%d/offset=%d/unassigned=%t", state, search, tracked, offset, unassigned), func(t *testing.T) {
							assert := assert.New(t)
							limit, count, first := 50, 50, tracked*40-offset
							if tracked == 60 {
								first = 2760 - offset
							}
							q := mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB()}
							pullOpts := ListMergeRequestsOpts{State: state, Search: search, Limit: limit, Offset: offset, AllowedRepoIDs: repos[:tracked], RepoFilters: []RepoFilter{}, Unassigned: unassigned}
							pulls, err := d.listMergeRequests(t.Context(), q, pullOpts)
							require.NoError(t, err)
							require.Len(t, pulls, count)
							assert.Equal(first, pulls[0].Number)
							assert.Equal(first-1, pulls[1].Number)
							pullOpts.AllowedRepoIDs, pullOpts.RepoFilters = nil, filters[:tracked]
							baselinePulls, err := d.ListMergeRequests(t.Context(), pullOpts)
							require.NoError(t, err)
							assert.Equal(baselinePulls, pulls)
							issueOpts := ListIssuesOpts{State: state, Search: search, Limit: limit, Offset: offset, AllowedRepoIDs: repos[:tracked], RepoFilters: []RepoFilter{}, Unassigned: unassigned}
							issues, err := d.listIssues(t.Context(), q, issueOpts)
							require.NoError(t, err)
							require.Len(t, issues, count)
							assert.Equal(first, issues[0].Number)
							assert.Equal(first-1, issues[1].Number)
							issueOpts.AllowedRepoIDs, issueOpts.RepoFilters = nil, filters[:tracked]
							baselineIssues, err := d.ListIssues(t.Context(), issueOpts)
							require.NoError(t, err)
							assert.Equal(baselineIssues, issues)
						})
					}
				}
			}
		}
	}

	for _, kind := range []string{"pr", "issue"} {
		require.NoError(t, d.SetStarred(t.Context(), kind, repos[0], 7))
	}
	pull, err := d.GetMergeRequestByRepoIDAndNumber(t.Context(), repos[0], 7)
	require.NoError(t, err)
	require.NoError(t, d.ReplaceMergeRequestLabels(t.Context(), repos[0], pull.ID, []Label{{Name: "selected", Color: "abcdef"}}))
	for _, filter := range []string{"repository", "open", "starred", "label"} {
		t.Run(filter, func(t *testing.T) {
			pullOpts := ListMergeRequestsOpts{State: "all", Limit: 1, AllowedRepoIDs: repos}
			issueOpts := ListIssuesOpts{State: "all", Limit: 1, AllowedRepoIDs: repos}
			pullIndex, issueIndex, want := "idx_mr_repo_activity", "idx_issue_repo_activity", 40
			switch filter {
			case "repository":
				pullOpts.RepoFilters, issueOpts.RepoFilters = filters[:1], filters[:1]
			case "open":
				pullOpts.State, issueOpts.State = "open", "open"
				pullIndex, issueIndex, want = "idx_mr_repo_state_activity", "idx_issues_repo_state_activity", 2365
			case "starred":
				pullOpts.Starred, issueOpts.Starred, want = true, true, 7
				pullIndex, issueIndex = "sqlite_autoindex_forge_merge_requests_2", "idx_issues_repo_created"
			case "label":
				pullOpts.Label, want = "selected", 7
				pullIndex = "sqlite_autoindex_forge_merge_requests_2"
			}
			q := mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB(), index: pullIndex, reject: "SCAN p USING INDEX idx_mr_activity"}
			pulls, err := d.listMergeRequests(t.Context(), q, pullOpts)
			require.NoError(t, err)
			require.Len(t, pulls, 1)
			assert.Equal(t, want, pulls[0].Number)
			if filter != "label" {
				q.index, q.reject = issueIndex, "SCAN i USING INDEX idx_issues_activity"
				issues, err := d.listIssues(t.Context(), q, issueOpts)
				require.NoError(t, err)
				require.Len(t, issues, 1)
				assert.Equal(t, want, issues[0].Number)
			}
		})
	}
}

func TestListMergeRequestsSelectiveActivityQueryPlan(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	var repos []int64
	for repo := range 60 {
		repoID := insertTestRepo(t, d, "acme", fmt.Sprintf("repo-%d", repo))
		repos = append(repos, repoID)
	}
	for _, repoID := range repos {
		_, err := d.WriteDB().ExecContext(t.Context(), `
			WITH RECURSIVE numbers(n) AS (VALUES(1) UNION ALL SELECT n + 1 FROM numbers WHERE n < 2000)
			INSERT INTO forge_merge_requests (repo_id, platform_id, number, state, created_at, updated_at, last_activity_at)
			SELECT ?1, n, n, 'closed', '2024-01-15 12:00:00', '2024-01-15 12:00:00', datetime('2024-01-15', '+' || (?1 * 2000 + n) || ' minutes') FROM numbers`, repoID)
		require.NoError(t, err)
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
	for _, tracked := range []int{2, 60} {
		for _, filter := range []string{"starred", "label"} {
			t.Run(fmt.Sprintf("%s/tracked=%d", filter, tracked), func(t *testing.T) {
				opts := ListMergeRequestsOpts{State: "all", Limit: 50, AllowedRepoIDs: repos[60-tracked:]}
				if filter == "starred" {
					opts.Starred = true
				} else {
					opts.Label = "selected"
				}
				q := mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB(), index: "sqlite_autoindex_forge_merge_requests_2", reject: "SCAN p USING INDEX idx_mr_activity"}
				pulls, err := d.listMergeRequests(t.Context(), q, opts)
				require.NoError(t, err)
				count := 1
				if filter == "starred" && tracked == 60 {
					count = 10
				}
				require.Len(t, pulls, count)
				assert.Equal(t, pull.ID, pulls[0].ID)
			})
		}
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
			mr.ReviewDecision = "APPROVED"
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
				Attributes: []string{"approved", "draft", "ready", "merge_conflicts", "failed_ci", "passed_ci", "merge_queue"},
			})
			require.NoError(t, err)
			require.Len(t, mrs, 2)
			assert.Equal(t, []int{299, 297}, []int{mrs[0].Number, mrs[1].Number})
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
				Attributes:  []string{"approved", "ready"},
			})
			require.NoError(t, err)
			require.Len(t, mrs, 2)
			assert.Equal(t, []int{299, 297}, []int{mrs[0].Number, mrs[1].Number})
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
		// Walk newer untracked history too; the activity index carries repo_id to skip it before table lookups.
		mrs, err := d.listMergeRequests(t.Context(), mergeRequestPlanQueryer{queryer: d.roStmts, t: t, ro: d.ReadDB()}, ListMergeRequestsOpts{
			State: "all", Limit: 50, AllowedRepoIDs: tracked,
		})
		require.NoError(t, err)
		require.Len(t, mrs, 10)
		assert.Equal(t, 10, mrs[0].Number)
		assert.Equal(t, 1, mrs[9].Number)

		_, err = d.WriteDB().ExecContext(t.Context(), "UPDATE forge_merge_requests SET review_decision = 'APPROVED'")
		require.NoError(t, err)
		mrs, err = d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{
			State: "all", Limit: 50, AllowedRepoIDs: tracked, Attributes: []string{"approved"},
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

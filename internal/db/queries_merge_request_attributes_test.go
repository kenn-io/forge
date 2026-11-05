package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListMergeRequestsAttributesMatchResponseRules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, status, checks string
		failed, passed       bool
	}{
		{"aggregate failure", " Failure ", "", true, false},
		{"aggregate error", "ERROR", "[]", true, false},
		{"aggregate pending", "pending", "[]", false, false},
		{"empty checks", "success", "", false, true},
		{"null checks", "success", "null", false, true},
		{"array checks", "success", "[]", false, true},
		{"malformed checks", "success", "{", false, false},
		{"object checks", "success", "{}", false, false},
		{"string element", "success", `["x"]`, false, false},
		{"number element", "success", `[1]`, false, false},
		{"null element", "success", `[null]`, false, false},
		{"null and failed check", "success", `[null,{"status":"completed","conclusion":"failure"}]`, true, false},
		{"boolean element", "success", `[true]`, false, false},
		{"array element", "success", `[[]]`, false, false},
		{"mixed failed checks", "success", `[{"status":"completed","conclusion":"failure"},"x"]`, false, false},
		{"mixed passed checks", "", `[{"status":"completed","conclusion":"success"},1]`, false, false},
		{"aggregate failure with invalid checks", "failure", `["x"]`, true, false},
		{"pending failure", "success", `[{"status":"pending","conclusion":"failure"}]`, false, false},
		{"skipped neutral", "success", `[{"status":"completed","conclusion":"skipped"},{"status":"completed","conclusion":"neutral"}]`, false, true},
		{"no evidence", "", "[]", false, false},
		{"checks only", "", `[{"status":"completed","conclusion":"success"}]`, false, true},
		{"failed check", "success", `[{"status":"completed","conclusion":"failure"}]`, true, false},
		{"missing conclusion", "success", `[{"status":"completed"}]`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			repo := insertTestRepo(t, d, "acme", "ci")
			mr := testMR(repo, 1)
			mr.CIStatus, mr.CIChecksJSON = tc.status, tc.checks
			_, err := d.UpsertMergeRequest(t.Context(), mr)
			require.NoError(t, err)
			for _, attribute := range []string{"failed_ci", "passed_ci"} {
				rows, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{State: "all", Attributes: []string{attribute}})
				require.NoError(t, err)
				want := tc.failed
				if attribute == "passed_ci" {
					want = tc.passed
				}
				numbers := []int{}
				for _, row := range rows {
					numbers = append(numbers, row.Number)
				}
				expected := []int{}
				if want {
					expected = []int{1}
				}
				assert.Equal(t, expected, numbers, attribute)
			}
		})
	}
	t.Run("review draft and locked ready", func(t *testing.T) {
		d := openTestDB(t)
		repo := insertTestRepo(t, d, "acme", "review")
		for n := 1; n <= 4; n++ {
			mr := testMR(repo, n)
			mr.ReviewDecision = " approved "
			if n == 4 {
				mr.ReviewDecision = "changes_requested"
				mr.State = "merged"
			}
			mr.IsDraft = n == 2
			mr.IsLocked = n == 3
			_, err := d.UpsertMergeRequest(t.Context(), mr)
			require.NoError(t, err)
		}
		for _, tc := range []struct {
			attribute, state string
			want             []int
		}{
			{"approved", "all", []int{3, 2, 1}},
			{"draft", "all", []int{2}},
			{"ready", "all", []int{3, 1}},
		} {
			rows, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{State: tc.state, Attributes: []string{tc.attribute}})
			require.NoError(t, err)
			numbers := []int{}
			for _, row := range rows {
				numbers = append(numbers, row.Number)
			}
			assert.Equal(t, tc.want, numbers)
		}
	})
	t.Run("merge queue", func(t *testing.T) {
		d := openTestDB(t)
		repo := insertTestRepo(t, d, "acme", "queue")
		now := baseTime()
		for n := 1; n <= 6; n++ {
			mr := testMR(repo, n)
			if n == 5 {
				mr.State = "closed"
			}
			if n == 6 {
				mr.State = "merged"
			}
			_, err := d.UpsertMergeRequest(t.Context(), mr)
			require.NoError(t, err)
			if n == 4 {
				continue
			}
			state := MergeQueueState{Number: n, Queued: n == 1 || n >= 5, ObservedAt: now}
			if n == 2 {
				state.RemovalReason = "failed_checks"
				state.RemovedAt = &now
			}
			_, err = d.UpsertMergeQueueState(t.Context(), repo, state)
			require.NoError(t, err)
		}
		rows, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{State: "all", Attributes: []string{"merge_queue"}})
		require.NoError(t, err)
		require.Len(t, rows, 2)
		assert.Equal(t, []int{2, 1}, []int{rows[0].Number, rows[1].Number})
	})
	t.Run("narrowing with inclusion", func(t *testing.T) {
		d := openTestDB(t)
		repo := insertTestRepo(t, d, "acme", "filters")
		// 1-3 involve the viewer and fail CI; 4 only fails CI and 5 only involves the viewer.
		for n := 1; n <= 5; n++ {
			mr := testMR(repo, n)
			mr.AssigneesJSON = `["other"]`
			if n != 4 {
				mr.Author = "viewer"
			}
			if n != 5 {
				mr.CIStatus = "failure"
			}
			id, err := d.UpsertMergeRequest(t.Context(), mr)
			require.NoError(t, err)
			if n == 2 {
				require.NoError(t, d.SetKanbanState(t.Context(), id, "reviewing"))
			}
			if n == 3 {
				require.NoError(t, d.ReplaceMergeRequestLabels(t.Context(), repo, id, []Label{{PlatformID: 1, Name: "review", UpdatedAt: baseTime()}}))
			}
			if n == 1 {
				require.NoError(t, d.SetStarred(t.Context(), "pr", repo, n))
			}
		}
		for _, tc := range []struct {
			name, search, kanban, label string
			starred                     bool
			want                        []int
		}{
			{"both groups", "", "", "", false, []int{3, 2, 1}},
			{"search", "03", "", "", false, []int{3}},
			{"kanban", "", "new", "", false, []int{3, 1}},
			{"label", "", "", "review", false, []int{3}},
			{"starred", "", "", "", true, []int{1}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rows, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{State: "all", ViewerLogins: []RepoViewerLogin{{RepoID: repo, Login: "viewer"}}, Attributes: []string{"failed_ci"}, Search: tc.search, KanbanState: tc.kanban, Label: tc.label, Starred: tc.starred})
				require.NoError(t, err)
				numbers := []int{}
				for _, row := range rows {
					numbers = append(numbers, row.Number)
				}
				assert.Equal(t, tc.want, numbers)
			})
		}
	})
	t.Run("stack conflicts", func(t *testing.T) {
		for _, tc := range []struct {
			name, sql string
			want      []int
		}{
			{"open dirty blocker", "", []int{2, 1}},
			{"closed target", "UPDATE forge_merge_requests SET state = 'closed' WHERE number = 2", []int{1}},
			{"merged blocker", "UPDATE forge_merge_requests SET state = CASE number WHEN 1 THEN 'merged' ELSE 'open' END", []int{1}},
			{"removed blocker", `INSERT INTO forge_archive_items (repo_id, item_type, item_number, provider_item_id, provider_created_at, provider_updated_at, lifecycle_state) SELECT repo_id, 'merge_request', number, 'pull-1', created_at, updated_at, 'removed_upstream' FROM forge_merge_requests WHERE number = 1`, []int{}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				d := openTestDB(t)
				repo := insertTestRepo(t, d, "acme", "stack")
				blocker := insertTestMRWithBranches(t, d, repo, 1, "a", "main", "open", withMRMergeableState("dirty"))
				target := insertTestMRWithBranches(t, d, repo, 2, "b", "a", "open")
				stack, err := d.UpsertStack(t.Context(), repo, 1, "feature", false)
				require.NoError(t, err)
				require.NoError(t, d.ReplaceStackMembers(t.Context(), stack, []StackMember{{MergeRequestID: blocker, Position: 1}, {MergeRequestID: target, Position: 2}}))
				if tc.sql != "" {
					_, err = d.WriteDB().ExecContext(t.Context(), tc.sql)
					require.NoError(t, err)
				}
				rows, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{State: "all", Attributes: []string{"merge_conflicts"}})
				require.NoError(t, err)
				numbers := []int{}
				for _, row := range rows {
					numbers = append(numbers, row.Number)
				}
				assert.Equal(t, tc.want, numbers)
			})
		}
	})
}

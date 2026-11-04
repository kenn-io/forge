package db

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertTestMRWithBranches(
	t *testing.T,
	d *DB,
	repoID int64,
	number int,
	head, base string,
	state MergeRequestState,
	opts ...testMROpt,
) int64 {
	t.Helper()
	allOpts := []testMROpt{withMRTitle("PR " + head), withMRBranches(head, base), withMRState(state)}
	allOpts = append(allOpts, opts...)
	return insertTestMRWithOptions(t, d, testMR(repoID, number, allOpts...))
}

func TestListOpenPRsForStackDetection(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "org", "repo")

	// open PR — included
	insertTestMRWithBranches(t, d, repoID, 1, "feature/a", "main", MergeRequestStateOpen)
	// merged PR — excluded
	insertTestMRWithBranches(t, d, repoID, 2, "feature/b", "feature/a", MergeRequestStateMerged)
	// closed PR — excluded
	insertTestMRWithBranches(t, d, repoID, 3, "feature/c", "main", MergeRequestStateClosed)

	prs, err := d.ListOpenPRsForStackDetection(t.Context(), repoID)
	require.NoError(t, err)
	require.Len(t, prs, 1)
	assert.Equal(1, prs[0].Number)
}

func TestUpsertStackAndReplaceMembers(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "org", "repo")

	mrID1 := insertTestMRWithBranches(t, d, repoID, 1, "feature/a", "main", "open")
	mrID2 := insertTestMRWithBranches(t, d, repoID, 2, "feature/b", "feature/a", "open")

	// Create stack (keyed by repo_id + base_number)
	stackID, err := d.UpsertStack(ctx, repoID, 1, "feature", false)
	require.NoError(err)
	assert.Positive(stackID)

	// Idempotent upsert returns same ID
	stackID2, err := d.UpsertStack(ctx, repoID, 1, "feature", false)
	require.NoError(err)
	assert.Equal(stackID, stackID2)

	// Replace members
	members := []StackMember{
		{StackID: stackID, MergeRequestID: mrID1, Position: 1},
		{StackID: stackID, MergeRequestID: mrID2, Position: 2},
	}
	err = d.ReplaceStackMembers(ctx, stackID, members)
	require.NoError(err)

	// Verify via ListStacksWithMembers
	stacks, memberMap, err := d.ListStacksWithMembers(ctx, "")
	require.NoError(err)
	assert.Len(stacks, 1)
	assert.Equal("feature", stacks[0].Name)
	assert.Equal("org", stacks[0].RepoOwner)
	assert.Equal("repo", stacks[0].RepoName)
	assert.Len(memberMap[stackID], 2)
	assert.Equal(1, memberMap[stackID][0].Position)
	assert.Equal(2, memberMap[stackID][1].Position)
}

func TestStackMembersRenumberAfterRemovedMembersAreFiltered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		removedNumber int
		wantNumbers   []int
	}{
		{name: "first member", removedNumber: 1, wantNumbers: []int{2, 3}},
		{name: "middle member", removedNumber: 2, wantNumbers: []int{1, 3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			d := openTestDB(t)
			ctx := t.Context()
			repoID := insertTestRepo(t, d, "org", "repo")

			memberIDs := make([]int64, 0, 3)
			for number := 1; number <= 3; number++ {
				memberIDs = append(memberIDs, insertTestMRWithBranches(
					t, d, repoID, number,
					fmt.Sprintf("feature/%d", number), "main", MergeRequestStateOpen,
				))
			}
			stackID, err := d.UpsertStack(ctx, repoID, 1, "feature", false)
			require.NoError(err)
			require.NoError(d.ReplaceStackMembers(ctx, stackID, []StackMember{
				{MergeRequestID: memberIDs[0], Position: 1},
				{MergeRequestID: memberIDs[1], Position: 2},
				{MergeRequestID: memberIDs[2], Position: 3},
			}))

			now := time.Now().UTC().Truncate(time.Second)
			_, err = d.WriteDB().ExecContext(ctx, `
				INSERT INTO forge_archive_items (
					repo_id, item_type, item_number, provider_item_id,
					provider_created_at, provider_updated_at, lifecycle_state
				) VALUES (?, 'merge_request', ?, ?, ?, ?, 'removed_upstream')`,
				repoID, tt.removedNumber,
				fmt.Sprintf("pull-%d", tt.removedNumber), now, now,
			)
			require.NoError(err)

			_, memberMap, err := d.ListStacksWithMembers(ctx, "")
			require.NoError(err)
			require.Len(memberMap[stackID], 2)
			for i, member := range memberMap[stackID] {
				require.Equal(tt.wantNumbers[i], member.Number)
				require.Equal(i+1, member.Position)
			}

			stack, members, err := d.GetStackForPRByRepoID(
				ctx, repoID, tt.wantNumbers[1],
			)
			require.NoError(err)
			require.NotNil(stack)
			require.Len(members, 2)
			for i, member := range members {
				require.Equal(tt.wantNumbers[i], member.Number)
				require.Equal(i+1, member.Position)
			}
		})
	}
}

func TestStackMembersIncludeMergeableState(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "org", "repo")

	mrID1 := insertTestMRWithBranches(
		t, d, repoID, 1, "feature/a", "main", "open", withMRMergeableState("dirty"),
	)
	mrID2 := insertTestMRWithBranches(t, d, repoID, 2, "feature/b", "feature/a", "open")
	stackID, err := d.UpsertStack(ctx, repoID, 1, "feature", false)
	require.NoError(err)
	err = d.ReplaceStackMembers(ctx, stackID, []StackMember{
		{StackID: stackID, MergeRequestID: mrID1, Position: 1},
		{StackID: stackID, MergeRequestID: mrID2, Position: 2},
	})
	require.NoError(err)

	_, members, err := d.GetStackForPRByRepoID(ctx, repoID, 2)
	require.NoError(err)
	require.Len(members, 2)
	assert.Equal("dirty", members[0].MergeableState)
	assert.Empty(members[1].MergeableState)
}

func TestListMRsBlockedByStackConflicts(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "org", "repo")

	mrID1 := insertTestMRWithBranches(
		t, d, repoID, 1, "feature/a", "main", "open", withMRMergeableState("dirty"),
	)
	mrID2 := insertTestMRWithBranches(t, d, repoID, 2, "feature/b", "feature/a", "open")
	mrID3 := insertTestMRWithBranches(t, d, repoID, 3, "feature/c", "feature/b", "open")
	stackID, err := d.UpsertStack(ctx, repoID, 1, "feature", false)
	require.NoError(err)
	err = d.ReplaceStackMembers(ctx, stackID, []StackMember{
		{StackID: stackID, MergeRequestID: mrID1, Position: 1},
		{StackID: stackID, MergeRequestID: mrID2, Position: 2},
		{StackID: stackID, MergeRequestID: mrID3, Position: 3},
	})
	require.NoError(err)

	blocked, err := d.ListMRsBlockedByStackConflicts(ctx, []int64{mrID1, mrID2, mrID3})
	require.NoError(err)
	assert.False(blocked[mrID1])
	assert.True(blocked[mrID2])
	assert.True(blocked[mrID3])

	large := make([]int64, 0, 70_000)
	for id := int64(1_000_000); id < 1_070_000; id++ {
		large = append(large, id)
	}
	large = append(large, mrID1, mrID2, mrID3)
	blocked, err = d.ListMRsBlockedByStackConflicts(ctx, large)
	require.NoError(err)
	assert.Len(blocked, 2)
	assert.True(blocked[mrID2])
	assert.True(blocked[mrID3])

	_, err = d.WriteDB().ExecContext(ctx, "UPDATE forge_merge_requests SET state = 'closed' WHERE id = ?", mrID2)
	require.NoError(err)
	blocked, err = d.ListMRsBlockedByStackConflicts(ctx, []int64{mrID2, mrID3})
	require.NoError(err)
	assert.Equal(map[int64]bool{mrID3: true}, blocked)
	_, err = d.WriteDB().ExecContext(ctx, "UPDATE forge_merge_requests SET state = 'open' WHERE id = ?", mrID2)
	require.NoError(err)

	_, err = d.WriteDB().ExecContext(ctx, "UPDATE forge_merge_requests SET state = 'merged' WHERE id = ?", mrID1)
	require.NoError(err)
	blocked, err = d.ListMRsBlockedByStackConflicts(ctx, []int64{mrID2, mrID3})
	require.NoError(err)
	assert.Empty(blocked)
	_, err = d.WriteDB().ExecContext(ctx, "UPDATE forge_merge_requests SET state = 'open' WHERE id = ?", mrID1)
	require.NoError(err)

	_, err = d.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_archive_items (repo_id, item_type, item_number, provider_item_id, provider_created_at, provider_updated_at, lifecycle_state)
		VALUES (?, 'merge_request', 2, 'pull-2', ?, ?, 'removed_upstream')`, repoID, baseTime(), baseTime())
	require.NoError(err)
	blocked, err = d.ListMRsBlockedByStackConflicts(ctx, []int64{mrID2, mrID3})
	require.NoError(err)
	assert.Equal(map[int64]bool{mrID3: true}, blocked)

	_, err = d.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_archive_items (repo_id, item_type, item_number, provider_item_id, provider_created_at, provider_updated_at, lifecycle_state)
		VALUES (?, 'merge_request', 1, 'pull-1', ?, ?, 'removed_upstream')`, repoID, baseTime(), baseTime())
	require.NoError(err)
	blocked, err = d.ListMRsBlockedByStackConflicts(ctx, []int64{mrID2, mrID3})
	require.NoError(err)
	assert.Empty(blocked)
}

func TestListStackPlacementsForMRs(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "org", "repo")

	mrID1 := insertTestMRWithBranches(t, d, repoID, 1, "feature/a", "main", "merged")
	mrID2 := insertTestMRWithBranches(t, d, repoID, 2, "feature/b", "feature/a", "open")
	mrID3 := insertTestMRWithBranches(t, d, repoID, 3, "feature/c", "feature/b", "open")
	mrID4 := insertTestMRWithBranches(t, d, repoID, 4, "feature/d", "feature/c", "open")
	soloID := insertTestMRWithBranches(t, d, repoID, 9, "solo", "main", "open")
	stackID, err := d.UpsertStack(ctx, repoID, 1, "feature", false)
	require.NoError(err)
	err = d.ReplaceStackMembers(ctx, stackID, []StackMember{
		{StackID: stackID, MergeRequestID: mrID1, Position: 1},
		{StackID: stackID, MergeRequestID: mrID2, Position: 2},
		{StackID: stackID, MergeRequestID: mrID3, Position: 3},
		{StackID: stackID, MergeRequestID: mrID4, Position: 4},
	})
	require.NoError(err)

	placements, err := d.ListStackPlacementsForMRs(ctx, []int64{mrID2, mrID4, soloID})
	require.NoError(err)
	assert.Equal(StackPlacement{StackID: stackID, Position: 2, Size: 4}, placements[mrID2])
	assert.Equal(StackPlacement{StackID: stackID, Position: 4, Size: 4}, placements[mrID4])
	_, requested := placements[mrID1]
	assert.False(requested, "unrequested members must not be returned")
	_, inStack := placements[soloID]
	assert.False(inStack, "merge requests outside a stack must not be returned")

	// Hiding a member removed upstream renumbers the visible members.
	_, err = d.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES (?, 'merge_request', 2, 'pull-2', ?, ?, 'removed_upstream')`,
		repoID, baseTime(), baseTime(),
	)
	require.NoError(err)

	placements, err = d.ListStackPlacementsForMRs(ctx, []int64{mrID2, mrID3, mrID4})
	require.NoError(err)
	_, hidden := placements[mrID2]
	assert.False(hidden, "hidden members must not be returned")
	assert.Equal(StackPlacement{StackID: stackID, Position: 2, Size: 3}, placements[mrID3])
	assert.Equal(StackPlacement{StackID: stackID, Position: 3, Size: 3}, placements[mrID4])

	empty, err := d.ListStackPlacementsForMRs(ctx, nil)
	require.NoError(err)
	assert.Empty(empty)

	// The pull list has no size cap, so the lookup must survive an ID set far
	// beyond SQLite's bind-parameter ceiling (32,766 variables).
	large := make([]int64, 0, 70_000)
	for id := int64(1_000_000); id < 1_070_000; id++ {
		large = append(large, id)
	}
	large = append(large, mrID3, mrID4)
	placements, err = d.ListStackPlacementsForMRs(ctx, large)
	require.NoError(err)
	assert.Len(placements, 2)
	assert.Equal(StackPlacement{StackID: stackID, Position: 2, Size: 3}, placements[mrID3])
}

func TestDeleteStaleStacks(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "org", "repo")

	id1, err := d.UpsertStack(ctx, repoID, 1, "keep", false)
	require.NoError(err)
	_, err = d.UpsertStack(ctx, repoID, 2, "delete-me", false)
	require.NoError(err)

	err = d.DeleteStaleStacks(ctx, repoID, []int64{id1})
	require.NoError(err)

	// Verify directly that "delete-me" is gone.
	var count int
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM forge_stacks WHERE repo_id = ?`, repoID,
	).Scan(&count)
	require.NoError(err)
	assert.Equal(1, count) // only "keep" remains
}

func TestListStacksWithMembers_MalformedFilter(t *testing.T) {
	t.Parallel()

	d := openTestDB(t)
	ctx := t.Context()

	for _, bad := range []string{"noslash", "/bar", "foo/", "/", "foo/bar/baz"} {
		_, _, err := d.ListStacksWithMembers(ctx, bad)
		require.Error(t, err, "filter=%q should fail", bad)
	}
	// Empty string is valid (no filter).
	_, _, err := d.ListStacksWithMembers(ctx, "")
	require.NoError(t, err)
}

func TestReplaceStackMembersReassignsAcrossStacks(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "org", "repo")

	mrID := insertTestMRWithBranches(t, d, repoID, 1, "feature/a", "main", "open")

	// Put PR in stackA.
	stackA, err := d.UpsertStack(ctx, repoID, 1, "stackA", false)
	require.NoError(err)
	err = d.ReplaceStackMembers(ctx, stackA, []StackMember{
		{StackID: stackA, MergeRequestID: mrID, Position: 1},
	})
	require.NoError(err)

	// Reassigning same PR to stackB should succeed by evicting from stackA.
	stackB, err := d.UpsertStack(ctx, repoID, 2, "stackB", false)
	require.NoError(err)
	err = d.ReplaceStackMembers(ctx, stackB, []StackMember{
		{StackID: stackB, MergeRequestID: mrID, Position: 1},
	})
	require.NoError(err)

	// Only one membership row remains, now in stackB.
	var count int
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM forge_stack_members WHERE merge_request_id = ?`,
		mrID,
	).Scan(&count)
	require.NoError(err)
	assert.Equal(1, count)

	var gotStack int64
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT stack_id FROM forge_stack_members WHERE merge_request_id = ?`,
		mrID,
	).Scan(&gotStack)
	require.NoError(err)
	assert.Equal(stackB, gotStack)
}

func TestGetStackForPR(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "org", "repo")

	mrID1 := insertTestMRWithBranches(t, d, repoID, 10, "feature/a", "main", "open")
	mrID2 := insertTestMRWithBranches(t, d, repoID, 11, "feature/b", "feature/a", "open")

	stackID, err := d.UpsertStack(ctx, repoID, 10, "feature", false)
	require.NoError(err)
	err = d.ReplaceStackMembers(ctx, stackID, []StackMember{
		{StackID: stackID, MergeRequestID: mrID1, Position: 1},
		{StackID: stackID, MergeRequestID: mrID2, Position: 2},
	})
	require.NoError(err)

	// Found
	stack, members, err := d.GetStackForPR(ctx, "github", "github.com", "org", "repo", 10)
	require.NoError(err)
	require.NotNil(stack)
	assert.Equal("feature", stack.Name)
	assert.Len(members, 2)

	// Not found
	stack2, _, err := d.GetStackForPR(ctx, "github", "github.com", "org", "repo", 999)
	require.NoError(err)
	assert.Nil(stack2)
}

func TestStackDetectionReaders(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "org", "repo")
	otherID := insertTestRepo(t, d, "org", "other")
	const clone = "https://github.com/org/repo.git"
	for _, row := range []struct {
		repo            int64
		number          int
		head, base, url string
		state           MergeRequestState
	}{
		{repoID, 1, "a", "main", clone, MergeRequestStateOpen},
		{repoID, 2, "a", "main", "https://GitHub.com/org/repo.git", MergeRequestStateMerged},
		{repoID, 3, "a", "main", clone, MergeRequestStateClosed},
		{repoID, 4, "a", "main", "https://github.com/fork/repo.git", MergeRequestStateMerged},
		{repoID, 5, "b", "other", clone, MergeRequestStateMerged},
		{repoID, 6, "a", "main", clone, MergeRequestStateMerged},
		{otherID, 7, "a", "main", clone, MergeRequestStateMerged},
	} {
		insertTestMRWithBranches(t, d, row.repo, row.number, row.head, row.base, row.state,
			func(mr *MergeRequest) { mr.HeadRepoCloneURL = row.url })
	}
	_, err := d.WriteDB().ExecContext(ctx, `INSERT INTO forge_archive_items (
		repo_id, item_type, item_number, provider_item_id, provider_created_at, provider_updated_at, lifecycle_state
	) VALUES (?, 'merge_request', 6, 'pull-6', ?, ?, 'removed_upstream')`, repoID, baseTime(), baseTime())
	require.NoError(t, err)
	for _, tc := range []struct {
		name           string
		urls, branches []string
		want           []int
	}{
		{"head group", []string{clone}, []string{"a"}, []int{1, 2}},
		{"mixed-case spelling", []string{"HTTPS://GitHub.com/org/repo.git"}, []string{"a"}, []int{1, 2}},
		{"two branches", []string{clone}, []string{"a", "b"}, []int{1, 2, 5}},
		{"fork", []string{"https://github.com/fork/repo.git"}, []string{"a"}, []int{4}},
		{"empty urls", nil, []string{"a"}, nil},
		{"empty branches", []string{clone}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := d.ListPRsForStackDetectionByHeads(ctx, repoID, tc.urls, tc.branches)
			require.NoError(t, err)
			var numbers []int
			for _, row := range rows {
				numbers = append(numbers, row.Number)
			}
			assert.Equal(t, tc.want, numbers)
		})
	}
	for _, tc := range []struct {
		base  string
		after int
		want  []int
	}{
		{"main", 0, []int{2}},
		{"main", 2, []int{4}},
		{"main", 4, nil},
		{"other", 0, []int{5}},
		{"missing", 0, nil},
	} {
		rows, err := d.ListMergedPRsForStackDetectionByBase(ctx, repoID, tc.base, tc.after)
		require.NoError(t, err)
		var numbers []int
		for _, row := range rows {
			numbers = append(numbers, row.Number)
		}
		assert.Equal(t, tc.want, numbers)
	}
}

func TestStackDetectionQueryPlans(t *testing.T) {
	t.Parallel()
	for _, optimize := range []bool{false, true} {
		d := openTestDB(t)
		repoID := insertTestRepo(t, d, "org", "repo")
		for number := 1; number <= 300; number++ {
			insertTestMRWithBranches(t, d, repoID, number, fmt.Sprintf("branch/%d", number), fmt.Sprintf("base/%d", number), MergeRequestStateOpen,
				func(mr *MergeRequest) { mr.HeadRepoCloneURL = "https://github.com/org/repo.git" })
		}
		if optimize {
			require.NoError(t, d.Optimize(t.Context()))
		}
		// History can arrive after startup analysis, before statistics refresh.
		_, err := d.WriteDB().ExecContext(t.Context(), `
			WITH RECURSIVE history(number) AS (
				VALUES(301)
				UNION ALL
				SELECT number + 1 FROM history WHERE number < 30000
			)
			INSERT INTO forge_merge_requests (
				repo_id, platform_id, number, title, author, state, head_branch, base_branch,
				created_at, updated_at, last_activity_at
			)
			SELECT ?, ? + number, number, 'Merged PR', 'author', 'merged', 'branch/' || number, 'main', ?, ?, ?
			FROM history`, repoID, repoID*10000, baseTime(), baseTime(), baseTime())
		require.NoError(t, err)
		for _, tc := range []struct {
			filter, order, index, hint string
			args                       []any
		}{
			{`AND state = 'open'`, "", "state=?", "INDEXED BY idx_mr_repo_state_created", []any{repoID}},
			{stackHeadsFilter, "ORDER BY number", "idx_mr_repo_head_repo", "INDEXED BY idx_mr_repo_head_repo", []any{repoID, `["https://github.com/org/repo.git"]`, `["branch/1"]`}},
			{stackMergedBaseFilter, "ORDER BY number LIMIT 1", "idx_mr_repo_base_state", "", []any{repoID, "base/1", 0}},
		} {
			plan := explainQueryPlan(t, d.ReadDB(), fmt.Sprintf(stackDetectionSQL, tc.hint)+tc.filter+" "+tc.order, tc.args...)
			assert.Contains(t, strings.Join(plan, "\n"), tc.index, "optimize=%v", optimize)
		}
	}
}

func TestStackDetectionMergedChildSkipsEmptyHeads(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "org", "repo")
	for number := 1; number <= 1000; number++ {
		insertTestMRWithBranches(t, d, repoID, number, fmt.Sprintf("deleted/%d", number), "develop", MergeRequestStateMerged)
	}
	insertTestMRWithBranches(t, d, repoID, 1001, "feature", "develop", MergeRequestStateMerged,
		func(mr *MergeRequest) { mr.HeadRepoCloneURL = "https://github.com/org/repo.git" })
	queries := 0
	for after := 0; ; {
		queries++
		candidates, err := d.ListMergedPRsForStackDetectionByBase(t.Context(), repoID, "develop", after)
		require.NoError(t, err)
		require.Len(t, candidates, 1)
		candidate := candidates[0]
		if candidate.HeadRepoCloneURL != "" {
			assert.Equal(t, 1001, candidate.Number)
			break
		}
		after = candidate.Number
	}
	assert.Equal(t, 1, queries, "deleted forks must not require a query per skipped child")
}

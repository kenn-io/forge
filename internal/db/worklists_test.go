package db

import (
	"cmp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertWorklistTestPull(t *testing.T, d *DB, repoID int64, number int, state MergeRequestState) {
	t.Helper()
	mr := testMR(repoID, number, withMRState(state))
	mr.PlatformHeadSHA = "aaa111"
	mr.CommentCount = 2
	mr.CIStatus = "success"
	mr.ReviewDecision = "REVIEW_REQUIRED"
	insertTestMRWithOptions(t, d, mr)
}

func TestPutWorklistCreatesListWithRecordedItemState(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widgets")
	insertWorklistTestPull(t, d, repoID, 7, MergeRequestStateOpen)
	insertTestIssue(t, d, repoID, 9, "Crash on start", baseTime())
	writtenAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	result, err := d.PutWorklist(ctx, PutWorklistParams{
		Title: "Ready to merge", Recipe: "open PRs with green CI", CreatedBy: "agent-a",
		WorkspaceID: "ws-1", Now: writtenAt,
		Items: []WorklistItemParams{
			{RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 7, Score: new(8), Section: "Ready", Note: "small fix"},
			{RepoID: repoID, ItemType: ItemTypeIssue, ItemNumber: 9, Section: "Triage", Caveat: "needs repro"},
		},
	})

	require.NoError(err)
	assert.True(result.Created)
	assert.Equal(2, result.ItemCount)
	detail, err := d.GetWorklist(ctx, result.ID, nil)
	require.NoError(err)
	assert.Equal("Ready to merge", detail.Title)
	assert.Equal("open PRs with green CI", detail.Recipe)
	assert.Equal("agent-a", detail.CreatedBy)
	assert.Equal("ws-1", detail.WorkspaceID)
	assert.Equal(writtenAt, detail.CreatedAt)
	assert.Equal(writtenAt, detail.UpdatedAt)
	assert.Nil(detail.DismissedAt)
	require.Len(detail.Items, 2)

	pull := detail.Items[0]
	assert.Equal(0, pull.Position)
	assert.Equal(ItemTypePR, pull.ItemType)
	assert.Equal(7, pull.ItemNumber)
	assert.Equal(new(8), pull.Score)
	assert.Equal("Ready", pull.Section)
	assert.Equal("small fix", pull.Note)
	assert.Equal("PR 7", pull.Title)
	assert.Equal("https://github.com/example/repo/pull/7", pull.URL)
	assert.Equal(writtenAt, pull.WrittenAt)
	assert.Equal(WorklistItemState{
		State: "open", HeadSHA: "aaa111", CommentCount: 2, CIStatus: "success",
		ReviewDecision: "REVIEW_REQUIRED", LastActivityAt: new(baseTime()),
	}, pull.Snapshot)
	require.NotNil(pull.Live)
	assert.Equal(pull.Snapshot, *pull.Live)
	assert.Equal(WorklistFresh, pull.Freshness)
	assert.Equal(WorklistChanges{}, pull.Changes)

	issue := detail.Items[1]
	assert.Equal(1, issue.Position)
	assert.Nil(issue.Score)
	assert.Equal("needs repro", issue.Caveat)
	assert.Equal("Crash on start", issue.Title)
	assert.Equal(WorklistItemState{State: "open", LastActivityAt: new(baseTime())}, issue.Snapshot)
	assert.Equal(WorklistFresh, issue.Freshness)
}

func TestPutWorklistReplacesItemsAndResetsRecordedState(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widgets")
	insertWorklistTestPull(t, d, repoID, 7, MergeRequestStateOpen)
	insertWorklistTestPull(t, d, repoID, 8, MergeRequestStateOpen)
	created := time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC)
	first, err := d.PutWorklist(ctx, PutWorklistParams{
		Title: "First", Recipe: "r1", CreatedBy: "agent-a", Now: created,
		Items: []WorklistItemParams{{RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 7}},
	})
	require.NoError(err)
	require.NoError(d.SetWorklistDismissed(ctx, first.ID, true, created.Add(time.Minute)))
	_, err = d.WriteDB().ExecContext(ctx,
		`UPDATE forge_merge_requests SET comment_count = 6 WHERE repo_id = ? AND number IN (7, 8)`, repoID)
	require.NoError(err)
	stale, err := d.GetWorklist(ctx, first.ID, nil)
	require.NoError(err)
	require.Equal(WorklistChanged, stale.Items[0].Freshness)

	replaced := created.Add(time.Hour)
	result, err := d.PutWorklist(ctx, PutWorklistParams{
		ID: first.ID, Title: "Second", Recipe: "r2", CreatedBy: "agent-b", WorkspaceID: "ws-2", Now: replaced,
		Items: []WorklistItemParams{
			{RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 8},
			{RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 7},
		},
	})

	require.NoError(err)
	assert.False(result.Created)
	assert.Equal(first.ID, result.ID)
	detail, err := d.GetWorklist(ctx, first.ID, nil)
	require.NoError(err)
	assert.Equal("Second", detail.Title)
	assert.Equal("r2", detail.Recipe)
	assert.Equal("agent-b", detail.CreatedBy)
	assert.Equal("ws-2", detail.WorkspaceID)
	assert.Equal(created, detail.CreatedAt)
	assert.Equal(replaced, detail.UpdatedAt)
	assert.Nil(detail.DismissedAt, "an agent rewrite restores a dismissed list")
	require.Len(detail.Items, 2)
	assert.Equal([]int{8, 7}, []int{detail.Items[0].ItemNumber, detail.Items[1].ItemNumber})
	for _, item := range detail.Items {
		assert.Equal(6, item.Snapshot.CommentCount)
		assert.Equal(replaced, item.WrittenAt)
		assert.Equal(WorklistFresh, item.Freshness)
	}
}

func TestPutWorklistRejectsMissingWorklistAndUnknownItems(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()
	repoID := insertTestRepo(t, d, "acme", "widgets")
	insertWorklistTestPull(t, d, repoID, 7, MergeRequestStateOpen)
	insertTestIssue(t, d, repoID, 9, "removed issue", now)
	_, err := d.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES (?, 'issue', 9, 'issue-9', ?, ?, 'removed_upstream')`, repoID, now, now)
	require.NoError(t, err)
	existing, err := d.PutWorklist(ctx, PutWorklistParams{
		Title: "Keep", Now: now,
		Items: []WorklistItemParams{{RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 7, Note: "original"}},
	})
	require.NoError(t, err)

	tests := []struct {
		name      string
		id        int64
		items     []WorklistItemParams
		wantIndex int
		wantType  string
	}{
		{
			name: "pull number without a synced pull",
			id:   existing.ID,
			items: []WorklistItemParams{
				{RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 7},
				{RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 9},
			},
			wantIndex: 1, wantType: ItemTypePR,
		},
		{
			name:      "issue removed upstream",
			items:     []WorklistItemParams{{RepoID: repoID, ItemType: ItemTypeIssue, ItemNumber: 9}},
			wantIndex: 0, wantType: ItemTypeIssue,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			_, err := d.PutWorklist(ctx, PutWorklistParams{ID: tt.id, Title: "Replaced", Now: now, Items: tt.items})

			var notFound *WorklistItemNotFoundError
			require.ErrorAs(t, err, &notFound)
			assert.Equal(tt.wantIndex, notFound.Index)
			assert.Equal(tt.wantType, notFound.ItemType)
		})
	}

	lists, err := d.ListWorklists(ctx, true, nil)
	require.NoError(t, err)
	require.Len(t, lists, 1, "a failed put writes nothing")
	kept, err := d.GetWorklist(ctx, existing.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, "Keep", kept.Title)
	require.Len(t, kept.Items, 1)
	assert.Equal(t, "original", kept.Items[0].Note)

	_, err = d.PutWorklist(ctx, PutWorklistParams{ID: existing.ID + 100, Title: "Ghost", Now: now})
	require.ErrorIs(t, err, ErrWorklistNotFound)
}

func TestWorklistDismissalIsIdempotentAndHidesListByDefault(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()
	repoID := insertTestRepo(t, d, "acme", "widgets")
	insertWorklistTestPull(t, d, repoID, 7, MergeRequestStateOpen)
	older, err := d.PutWorklist(ctx, PutWorklistParams{
		Title: "Older", Now: now,
		Items: []WorklistItemParams{{RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 7}},
	})
	require.NoError(err)
	newer, err := d.PutWorklist(ctx, PutWorklistParams{
		Title: "Newer", Now: now.Add(time.Minute),
		Items: []WorklistItemParams{{RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 7}},
	})
	require.NoError(err)

	dismissedAt := now.Add(time.Hour)
	require.NoError(d.SetWorklistDismissed(ctx, older.ID, true, dismissedAt))
	require.NoError(d.SetWorklistDismissed(ctx, older.ID, true, dismissedAt.Add(time.Hour)))

	summary, err := d.GetWorklistSummary(ctx, older.ID, nil)
	require.NoError(err)
	assert.Equal(new(dismissedAt), summary.DismissedAt, "repeat dismissal keeps the first time")
	visible, err := d.ListWorklists(ctx, false, nil)
	require.NoError(err)
	require.Len(visible, 1)
	assert.Equal(newer.ID, visible[0].ID)
	all, err := d.ListWorklists(ctx, true, nil)
	require.NoError(err)
	assert.Equal([]int64{newer.ID, older.ID}, []int64{all[0].ID, all[1].ID})

	require.NoError(d.SetWorklistDismissed(ctx, older.ID, false, now))
	require.NoError(d.SetWorklistDismissed(ctx, older.ID, false, now))
	visible, err = d.ListWorklists(ctx, false, nil)
	require.NoError(err)
	assert.Len(visible, 2)
	assert.ErrorIs(d.SetWorklistDismissed(ctx, newer.ID+100, true, now), ErrWorklistNotFound)
}

func TestWorklistItemsFollowRepositoryDeletion(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	kept := insertTestRepo(t, d, "acme", "widgets")
	removed := insertTestRepo(t, d, "acme", "gadgets")
	insertWorklistTestPull(t, d, kept, 7, MergeRequestStateOpen)
	insertWorklistTestPull(t, d, removed, 3, MergeRequestStateOpen)
	put, err := d.PutWorklist(ctx, PutWorklistParams{Title: "Mixed", Now: baseTime(), Items: []WorklistItemParams{
		{RepoID: removed, ItemType: ItemTypePR, ItemNumber: 3},
		{RepoID: kept, ItemType: ItemTypePR, ItemNumber: 7},
	}})
	require.NoError(err)

	_, err = d.WriteDB().ExecContext(ctx, `DELETE FROM forge_repos WHERE id = ?`, removed)
	require.NoError(err)

	detail, err := d.GetWorklist(ctx, put.ID, nil)
	require.NoError(err)
	require.Len(detail.Items, 1)
	assert.Equal(t, kept, detail.Items[0].RepoID)
	summary, err := d.GetWorklistSummary(ctx, put.ID, nil)
	require.NoError(err)
	assert.Equal(t, 1, summary.ItemCount)
}

func TestWorklistItemsInUntrackedRepositoriesAreGone(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	tracked := insertTestRepo(t, d, "acme", "widgets")
	untracked := insertTestRepo(t, d, "acme", "gadgets")
	insertWorklistTestPull(t, d, tracked, 7, MergeRequestStateOpen)
	insertWorklistTestPull(t, d, untracked, 3, MergeRequestStateOpen)
	put, err := d.PutWorklist(ctx, PutWorklistParams{Title: "Mixed", Now: baseTime(), Items: []WorklistItemParams{
		{RepoID: untracked, ItemType: ItemTypePR, ItemNumber: 3},
		{RepoID: tracked, ItemType: ItemTypePR, ItemNumber: 7},
	}})
	require.NoError(err)

	detail, err := d.GetWorklist(ctx, put.ID, []int64{tracked})
	require.NoError(err)
	require.Len(detail.Items, 2)
	assert.Equal(WorklistGone, detail.Items[0].Freshness)
	assert.Nil(detail.Items[0].Live)
	assert.Equal(WorklistFresh, detail.Items[1].Freshness)
	summary, err := d.GetWorklistSummary(ctx, put.ID, []int64{tracked})
	require.NoError(err)
	assert.Equal(2, summary.ItemCount)
	assert.Equal(1, summary.GoneCount)
	listed, err := d.ListWorklists(ctx, false, []int64{})
	require.NoError(err)
	require.Len(listed, 1)
	assert.Equal(2, listed[0].GoneCount, "an empty tracked set tracks nothing")

	_, err = d.WriteDB().ExecContext(ctx, `UPDATE forge_repos SET lifecycle_state = 'inactive' WHERE id = ?`, tracked)
	require.NoError(err)
	summary, err = d.GetWorklistSummary(ctx, put.ID, nil)
	require.NoError(err)
	assert.Equal(1, summary.GoneCount, "an inactive repository is untracked")
	assert.Equal(0, summary.ChangedCount)
}

func TestWorklistFreshnessComparesRecordedAndLiveState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		itemType      string
		initialState  MergeRequestState
		mutate        string
		wantFreshness WorklistFreshness
		wantChanges   WorklistChanges
		wantLive      bool
	}{
		{name: "unchanged pull", itemType: ItemTypePR, wantFreshness: WorklistFresh, wantLive: true},
		{
			name: "new commits", itemType: ItemTypePR,
			mutate:        `UPDATE forge_merge_requests SET platform_head_sha = 'bbb222' WHERE repo_id = ? AND number = 7`,
			wantFreshness: WorklistChanged, wantChanges: WorklistChanges{NewCommits: true}, wantLive: true,
		},
		{
			name: "head no longer known", itemType: ItemTypePR,
			mutate:        `UPDATE forge_merge_requests SET platform_head_sha = '' WHERE repo_id = ? AND number = 7`,
			wantFreshness: WorklistFresh, wantLive: true,
		},
		{
			name: "new comments", itemType: ItemTypePR,
			mutate:        `UPDATE forge_merge_requests SET comment_count = 5 WHERE repo_id = ? AND number = 7`,
			wantFreshness: WorklistChanged, wantChanges: WorklistChanges{NewComments: 3}, wantLive: true,
		},
		{
			name: "fewer comments", itemType: ItemTypePR,
			mutate:        `UPDATE forge_merge_requests SET comment_count = 1 WHERE repo_id = ? AND number = 7`,
			wantFreshness: WorklistFresh, wantLive: true,
		},
		{
			name: "CI result", itemType: ItemTypePR,
			mutate:        `UPDATE forge_merge_requests SET ci_status = 'failure' WHERE repo_id = ? AND number = 7`,
			wantFreshness: WorklistChanged, wantChanges: WorklistChanges{CIFrom: "success", CITo: "failure"}, wantLive: true,
		},
		{
			name: "review decision", itemType: ItemTypePR,
			mutate:        `UPDATE forge_merge_requests SET review_decision = 'APPROVED' WHERE repo_id = ? AND number = 7`,
			wantFreshness: WorklistChanged, wantChanges: WorklistChanges{ReviewFrom: "REVIEW_REQUIRED", ReviewTo: "APPROVED"}, wantLive: true,
		},
		{
			name: "merged", itemType: ItemTypePR,
			mutate:        `UPDATE forge_merge_requests SET state = 'merged' WHERE repo_id = ? AND number = 7`,
			wantFreshness: WorklistGone, wantChanges: WorklistChanges{StateFrom: "open", StateTo: "merged"}, wantLive: true,
		},
		{
			name: "closed issue", itemType: ItemTypeIssue,
			mutate:        `UPDATE forge_issues SET state = 'closed' WHERE repo_id = ? AND number = 7`,
			wantFreshness: WorklistGone, wantChanges: WorklistChanges{StateFrom: "open", StateTo: "closed"}, wantLive: true,
		},
		{
			name: "reopened pull", itemType: ItemTypePR, initialState: MergeRequestStateClosed,
			mutate:        `UPDATE forge_merge_requests SET state = 'open' WHERE repo_id = ? AND number = 7`,
			wantFreshness: WorklistChanged, wantChanges: WorklistChanges{StateFrom: "closed", StateTo: "open"}, wantLive: true,
		},
		{
			name: "pull removed upstream", itemType: ItemTypePR,
			mutate: `INSERT INTO forge_archive_items (
				repo_id, item_type, item_number, provider_item_id,
				provider_created_at, provider_updated_at, lifecycle_state
			) VALUES (?, 'merge_request', 7, 'pull-7', '2024-01-15', '2024-01-15', 'removed_upstream')`,
			wantFreshness: WorklistGone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			require := require.New(t)
			d := openTestDB(t)
			ctx := t.Context()
			repoID := insertTestRepo(t, d, "acme", "widgets")
			if tt.itemType == ItemTypePR {
				insertWorklistTestPull(t, d, repoID, 7, cmp.Or(tt.initialState, MergeRequestStateOpen))
			} else {
				insertTestIssue(t, d, repoID, 7, "Issue 7", baseTime())
			}
			put, err := d.PutWorklist(ctx, PutWorklistParams{Title: "List", Now: baseTime(), Items: []WorklistItemParams{
				{RepoID: repoID, ItemType: tt.itemType, ItemNumber: 7},
			}})
			require.NoError(err)
			if tt.mutate != "" {
				_, err = d.WriteDB().ExecContext(ctx, tt.mutate, repoID)
				require.NoError(err)
			}

			detail, err := d.GetWorklist(ctx, put.ID, nil)

			require.NoError(err)
			require.Len(detail.Items, 1)
			item := detail.Items[0]
			assert.Equal(tt.wantFreshness, item.Freshness)
			assert.Equal(tt.wantChanges, item.Changes)
			assert.Equal(tt.wantLive, item.Live != nil)
			summary, err := d.GetWorklistSummary(ctx, put.ID, nil)
			require.NoError(err)
			assert.Equal(tt.wantFreshness == WorklistChanged, summary.ChangedCount == 1)
			assert.Equal(tt.wantFreshness == WorklistGone, summary.GoneCount == 1)
		})
	}
}

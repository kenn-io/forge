package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPRSizeHistoryTracksObservedRevisions(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "acme", "widget")
	mr := &MergeRequest{
		RepoID: repoID, Number: 1, Title: "Improve search", State: MergeRequestStateOpen,
		PlatformHeadSHA: "head-a", PlatformBaseSHA: "base-a", Additions: 100, Deletions: 20,
		AdditionsKnown: true, DeletionsKnown: true, UpdatedAt: time.Now().UTC(),
	}
	save := func() *MergeRequest {
		t.Helper()
		_, err := d.UpsertMergeRequest(t.Context(), mr)
		require.NoError(t, err)
		got, err := d.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 1)
		require.NoError(t, err)
		require.NotNil(t, got)
		return got
	}
	first := save()
	require.Len(t, first.DiffSizeHistory, 1)
	require.Equal(t, 100, first.DiffSizeHistory[0].Additions)
	require.False(t, first.DiffSizeHistory[0].ObservedAt.IsZero())
	require.Len(t, save().DiffSizeHistory, 1, "refreshes are not revisions")
	mr.PlatformHeadSHA, mr.Additions = "head-b", 200
	got := save()
	require.Len(t, got.DiffSizeHistory, 2)
	require.Equal(t, 200, got.DiffSizeHistory[1].Additions, "total PR diff, not accumulated churn")
	mr.Additions = 150
	got = save()
	require.Len(t, got.DiffSizeHistory, 2, "corrected totals replace the same revision")
	require.Equal(t, 150, got.DiffSizeHistory[1].Additions)
	mr.PlatformBaseSHA = "base-b"
	require.Len(t, save().DiffSizeHistory, 3, "base changes can alter total PR size")
	mr.PlatformHeadSHA, mr.AdditionsKnown = "unmeasured", false
	require.Len(t, save().DiffSizeHistory, 3, "omitted counts must never reuse the previous head's size")
	mr.AdditionsKnown, mr.PlatformHeadSHA = true, "stale"
	mr.UpdatedAt = mr.UpdatedAt.Add(-time.Hour)
	require.Len(t, save().DiffSizeHistory, 3, "rejected snapshots cannot add history")
	mr.UpdatedAt = time.Now().UTC()
	for i := range 40 {
		mr.PlatformHeadSHA, mr.Additions, mr.Deletions = fmt.Sprintf("revision-%d", i), i, 0
		got = save()
	}
	require.Len(t, got.DiffSizeHistory, 32)
	require.Equal(t, "revision-8", got.DiffSizeHistory[0].HeadSHA)
	require.Equal(t, "revision-39", got.DiffSizeHistory[31].HeadSHA)
}

func TestPRSizeHistoryDoesNotInventExistingHistory(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "acme", "empty")
	mr := &MergeRequest{RepoID: repoID, Number: 2, Title: "No metrics", State: MergeRequestStateOpen, UpdatedAt: time.Now().UTC()}
	_, err := d.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
	got, err := d.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 2)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got.DiffSizeHistory)
	mr.PlatformHeadSHA, mr.AdditionsKnown, mr.DeletionsKnown = "empty-diff", true, true
	_, err = d.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
	got, err = d.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 2)
	require.NoError(t, err)
	require.Len(t, got.DiffSizeHistory, 1, "explicit zero is a real point")
}

func TestPRSizeHistoryRestartsWhenBaseBranchChanges(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "acme", "stacked")
	mr := &MergeRequest{
		RepoID: repoID, Number: 3, Title: "Stacked change", State: MergeRequestStateOpen,
		BaseBranch: "parent-feature", PlatformHeadSHA: "head-a", PlatformBaseSHA: "parent-tip",
		Additions: 16000, Deletions: 6000, AdditionsKnown: true, DeletionsKnown: true, UpdatedAt: time.Now().UTC(),
	}
	save := func() []PRDiffSizePoint {
		t.Helper()
		_, err := d.UpsertMergeRequest(t.Context(), mr)
		require.NoError(t, err)
		got, err := d.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 3)
		require.NoError(t, err)
		require.NotNil(t, got)
		return got.DiffSizeHistory
	}
	require.Len(t, save(), 1)
	mr.BaseBranch, mr.PlatformBaseSHA, mr.Additions, mr.Deletions = "main", "main-tip", 1700, 400
	history := save()
	require.Len(t, history, 1, "sizes against the old base branch are not comparable")
	require.Equal(t, "main", history[0].BaseBranch)
	require.Equal(t, 1700, history[0].Additions)
	mr.PlatformHeadSHA, mr.Additions = "head-b", 1650
	require.Len(t, save(), 2, "revisions on the same base branch still form one series")
}

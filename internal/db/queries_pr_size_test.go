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
	require.Equal(t, first.DiffSizeHistory[0], got.DiffSizeHistory[0], "trimming must retain the true initial observation")
	require.Equal(t, "revision-9", got.DiffSizeHistory[1].HeadSHA)
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

func TestPRSizeHistoryRetainsZeroBaseline(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "acme", "zero-baseline")
	mr := &MergeRequest{
		RepoID: repoID, Number: 1, Title: "Build search", State: MergeRequestStateOpen,
		PlatformHeadSHA: "initial", AdditionsKnown: true, DeletionsKnown: true,
		UpdatedAt: time.Now().UTC(),
	}
	_, err := d.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
	for i := 1; i <= 40; i++ {
		mr.PlatformHeadSHA, mr.Additions = fmt.Sprintf("head-%d", i), i
		_, err = d.UpsertMergeRequest(t.Context(), mr)
		require.NoError(t, err)
	}
	got, err := d.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 1)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got.DiffSizeHistory, 32)
	require.Equal(t, "initial", got.DiffSizeHistory[0].HeadSHA)
	require.Zero(t, got.DiffSizeHistory[0].Additions)
	require.Zero(t, got.DiffSizeHistory[0].Deletions)
	require.Equal(t, "head-10", got.DiffSizeHistory[1].HeadSHA)
	require.Equal(t, "head-40", got.DiffSizeHistory[31].HeadSHA)
}

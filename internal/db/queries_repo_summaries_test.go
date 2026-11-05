package db

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListRepoSummariesIncludesOverviewSnapshot(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widgets")
	publishedAt := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	previousPublishedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	timelineUpdatedAt := time.Date(2026, 4, 10, 9, 0, 0, 0, time.UTC)
	commitsSince := 42

	err := d.UpsertRepoOverview(ctx, repoID, RepoOverview{
		LatestRelease: &RepoRelease{
			TagName:         "v2.8.1",
			Name:            "Version 2.8.1",
			URL:             "https://github.com/acme/widgets/releases/tag/v2.8.1",
			TargetCommitish: "main",
			Prerelease:      false,
			PublishedAt:     &publishedAt,
		},
		Releases: []RepoRelease{
			{
				TagName:         "v2.8.1",
				Name:            "Version 2.8.1",
				URL:             "https://github.com/acme/widgets/releases/tag/v2.8.1",
				TargetCommitish: "main",
				Prerelease:      false,
				PublishedAt:     &publishedAt,
			},
			{
				TagName:         "v2.7.0",
				Name:            "Version 2.7.0",
				URL:             "https://github.com/acme/widgets/releases/tag/v2.7.0",
				TargetCommitish: "main",
				Prerelease:      true,
				PublishedAt:     &previousPublishedAt,
			},
		},
		CommitsSinceRelease: &commitsSince,
		CommitTimeline: []RepoCommitTimelinePoint{{
			SHA:         "abc123",
			Message:     "Ship repo overview",
			CommittedAt: time.Date(2026, 4, 9, 8, 0, 0, 0, time.UTC),
		}},
		TimelineUpdatedAt: &timelineUpdatedAt,
	})
	require.NoError(err)

	summaries, err := d.ListRepoSummaries(ctx)
	require.NoError(err)
	require.Len(summaries, 1)
	overview := summaries[0].Overview
	require.NotNil(overview.LatestRelease)
	require.NotNil(overview.CommitsSinceRelease)
	require.NotNil(overview.TimelineUpdatedAt)
	require.Len(overview.CommitTimeline, 1)
	require.Len(overview.Releases, 2)

	assert.Equal("v2.8.1", overview.LatestRelease.TagName)
	assert.Equal("Version 2.8.1", overview.LatestRelease.Name)
	assert.Equal("https://github.com/acme/widgets/releases/tag/v2.8.1", overview.LatestRelease.URL)
	assert.Equal("main", overview.LatestRelease.TargetCommitish)
	assert.False(overview.LatestRelease.Prerelease)
	assert.Equal(publishedAt, *overview.LatestRelease.PublishedAt)
	assert.Equal("v2.7.0", overview.Releases[1].TagName)
	assert.True(overview.Releases[1].Prerelease)
	assert.Equal(previousPublishedAt, *overview.Releases[1].PublishedAt)
	assert.Equal(42, *overview.CommitsSinceRelease)
	assert.Equal("abc123", overview.CommitTimeline[0].SHA)
	assert.Equal("Ship repo overview", overview.CommitTimeline[0].Message)
	assert.Equal(timelineUpdatedAt, *overview.TimelineUpdatedAt)
}

func TestListRepoSummariesExcludesLockedPullRequestsFromOpenCounts(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "acme", "widgets")
	insertTestMR(t, d, repoID, 1, "open", baseTime())
	locked := testMR(repoID, 2)
	locked.IsDraft = true
	locked.IsLocked = true
	_, err := d.UpsertMergeRequest(t.Context(), locked)
	require.NoError(err)

	summaries, err := d.ListRepoSummaries(t.Context())
	require.NoError(err)
	require.Len(summaries, 1)
	assert.Equal(2, summaries[0].CachedPRCount)
	assert.Equal(1, summaries[0].OpenPRCount)
	assert.Zero(summaries[0].DraftPRCount)
}

func TestUpsertRepoOverviewClearsTimelineWhenReleaseChangesWithoutCloneData(t *testing.T) {
	t.Parallel()

	assert := assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widgets")
	oldPublishedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	newPublishedAt := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	timelineUpdatedAt := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	commitsSince := 12

	err := d.UpsertRepoOverview(ctx, repoID, RepoOverview{
		LatestRelease: &RepoRelease{
			TagName:     "v1.0.0",
			Name:        "Version 1.0.0",
			URL:         "https://github.com/acme/widgets/releases/tag/v1.0.0",
			PublishedAt: &oldPublishedAt,
		},
		CommitsSinceRelease: &commitsSince,
		CommitTimeline: []RepoCommitTimelinePoint{{
			SHA:         "abc123",
			Message:     "Old release commit",
			CommittedAt: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC),
		}},
		TimelineUpdatedAt: &timelineUpdatedAt,
	})
	require.NoError(err)

	err = d.UpsertRepoOverview(ctx, repoID, RepoOverview{
		LatestRelease: &RepoRelease{
			TagName:     "v2.0.0",
			Name:        "Version 2.0.0",
			URL:         "https://github.com/acme/widgets/releases/tag/v2.0.0",
			PublishedAt: &newPublishedAt,
		},
	})
	require.NoError(err)

	summaries, err := d.ListRepoSummaries(ctx)
	require.NoError(err)
	require.Len(summaries, 1)
	overview := summaries[0].Overview
	require.NotNil(overview.LatestRelease)

	assert.Equal("v2.0.0", overview.LatestRelease.TagName)
	assert.Nil(overview.CommitsSinceRelease)
	assert.Empty(overview.CommitTimeline)
	assert.Nil(overview.TimelineUpdatedAt)
}

func TestListRepoSummariesHidesRemovedUpstreamItems(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoA := insertTestRepo(t, d, "example", "a")
	repoB := insertTestRepo(t, d, "example", "b")
	now := baseTime()
	for _, item := range []struct {
		repoID        int64
		number        int
		author        string
		draft, locked bool
		hours         int
	}{
		{repoA, 1, "alice", false, false, 1},
		{repoA, 2, "bob", true, false, 2},
		{repoA, 3, "carol", true, false, 3},
		{repoA, 4, "alice", true, false, 100},
		{repoA, 5, "", false, false, 4},
		{repoA, 6, "dana", false, false, 5},
		{repoB, 1, "alice", true, true, 100},
	} {
		mr := testMR(item.repoID, item.number, withMRAuthor(item.author), withMRActivity(now.Add(time.Duration(item.hours)*time.Hour)))
		mr.IsDraft, mr.IsLocked = item.draft, item.locked
		insertTestMRWithOptions(t, d, mr)
	}
	for _, item := range []struct {
		repoID int64
		number int
		author string
		hours  int
	}{
		{repoA, 1, "alice", 1},
		{repoA, 2, "bob", 2},
		{repoA, 3, "bob", 3},
		{repoA, 4, "carol", 100},
		{repoA, 5, "dana", 2},
		{repoB, 1, "alice", 100},
	} {
		insertTestIssueWithOptions(t, d, testIssue(item.repoID, item.number, withIssueAuthor(item.author), withIssueActivity(now.Add(time.Duration(item.hours)*time.Hour))))
	}
	for _, item := range []struct {
		repoID int64
		kind   ArchiveItemType
		number int
		state  string
	}{
		{repoA, "merge_request", 4, "removed_upstream"},
		{repoA, "issue", 4, "removed_upstream"},
		{repoA, "merge_request", 3, "inaccessible"},
		{repoB, "merge_request", 1, "removed_upstream"},
		{repoB, "issue", 1, "removed_upstream"},
	} {
		insertArchiveItemForTest(t, d, item.repoID, item.kind, item.number, now)
		_, err := d.execContext(t.Context(), `UPDATE forge_archive_items SET lifecycle_state = ?
			WHERE repo_id = ? AND item_type = ? AND item_number = ?`, item.state, item.repoID, item.kind, item.number)
		require.NoError(err)
	}

	// Three authors tie for third. Carol's removed newest item must not
	// displace Bob, whose visible activity is the most recent of the three.
	repoC := insertTestRepo(t, d, "example", "c")
	number := 0
	for _, author := range []struct {
		login string
		hours []int
	}{
		{"leader", []int{1, 2, 3, 4}},
		{"runner", []int{1, 2, 3}},
		{"alice", []int{1, 4}},
		{"bob", []int{1, 5}},
		{"carol", []int{1, 2, 100}},
	} {
		for _, hours := range author.hours {
			number++
			insertTestMRWithOptions(t, d, testMR(repoC, number, withMRAuthor(author.login), withMRActivity(now.Add(time.Duration(hours)*time.Hour))))
		}
	}
	insertArchiveItemForTest(t, d, repoC, ArchiveItemTypeMergeRequest, 14, now)
	_, err := d.execContext(t.Context(), `UPDATE forge_archive_items SET lifecycle_state = 'removed_upstream'
		WHERE repo_id = ? AND item_type = 'merge_request' AND item_number = 14`, repoC)
	require.NoError(err)

	summaries, err := d.ListRepoSummaries(t.Context())
	require.NoError(err)
	require.Len(summaries, 3)
	a, b := summaries[0], summaries[1]
	assert.Equal(5, a.CachedPRCount)
	assert.Equal(5, a.OpenPRCount)
	assert.Equal(2, a.DraftPRCount)
	assert.Equal(4, a.CachedIssueCount)
	assert.Equal(4, a.OpenIssueCount)
	require.NotNil(a.MostRecentActivityAt)
	assert.Equal(now.Add(5*time.Hour), *a.MostRecentActivityAt)
	assert.Equal([]RepoActivityAuthor{{Login: "bob", ItemCount: 3}, {Login: "dana", ItemCount: 2}, {Login: "alice", ItemCount: 2}}, a.ActiveAuthors)
	require.Len(a.RecentIssues, 3)
	assert.Equal([]int{3, 5, 2}, []int{a.RecentIssues[0].Number, a.RecentIssues[1].Number, a.RecentIssues[2].Number})
	assert.Zero(b.CachedPRCount)
	assert.Zero(b.OpenPRCount)
	assert.Zero(b.DraftPRCount)
	assert.Zero(b.CachedIssueCount)
	assert.Zero(b.OpenIssueCount)
	assert.Nil(b.MostRecentActivityAt)
	assert.Empty(b.ActiveAuthors)
	assert.Empty(b.RecentIssues)
	assert.Equal([]RepoActivityAuthor{{Login: "leader", ItemCount: 4}, {Login: "runner", ItemCount: 3}, {Login: "bob", ItemCount: 2}}, summaries[2].ActiveAuthors)
}

func TestRepoSummaryQueryPlans(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "example", "widgets")
	for number := 1; number <= 300; number++ {
		insertTestMR(t, d, repoID, number, "PR", baseTime())
		insertTestIssue(t, d, repoID, number, "Issue", baseTime())
	}
	for i := range 5 {
		other := insertTestRepo(t, d, "example", fmt.Sprintf("small-%d", i))
		insertTestMR(t, d, other, 1, "PR", baseTime())
		insertTestIssue(t, d, other, 1, "Issue", baseTime())
	}
	for _, item := range []struct{ table, kind string }{
		{"forge_merge_requests", "merge_request"},
		{"forge_issues", "issue"},
	} {
		_, err := d.execContext(t.Context(), `INSERT INTO forge_archive_items
			(repo_id, item_type, item_number, provider_item_id, provider_created_at, provider_updated_at, lifecycle_state)
			SELECT repo_id, ?, number, CAST(number AS TEXT), created_at, updated_at,
			CASE WHEN number = 300 THEN 'removed_upstream' ELSE 'active' END FROM `+item.table, item.kind)
		require.NoError(t, err)
	}
	require.NoError(t, d.Optimize(t.Context()))
	for _, query := range []struct {
		name, sql string
		indexes   []string
	}{
		{"stats", repoSummaryStatsQuery, []string{"idx_archive_items_removed_upstream", "idx_mr_repo_activity", "idx_issue_repo_activity", "idx_mr_open_flags"}},
		{"authors", repoSummaryAuthorsQuery, []string{"idx_mr_repo_author_activity", "idx_issue_repo_author_activity"}},
		{"issues", repoSummaryIssuesQuery, []string{"idx_issues_repo_state_activity"}},
	} {
		t.Run(query.name, func(t *testing.T) {
			assert := assert.New(t)
			details := explainQueryPlan(t, d.ReadDB(), query.sql)
			plan := strings.Join(details, "\n")
			for _, index := range query.indexes {
				assert.Contains(plan, index)
			}
		})
	}
}

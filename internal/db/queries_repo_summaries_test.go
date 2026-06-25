package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListRepoSummariesIncludesOverviewSnapshot(t *testing.T) {
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

func TestUpsertRepoOverviewClearsTimelineWhenReleaseChangesWithoutCloneData(t *testing.T) {
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

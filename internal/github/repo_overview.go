package github

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"time"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

const repoOverviewTimelineLimit = 30

func repoProviderHostFromDB(repo db.Repo) string {
	if repo.PlatformHost != "" {
		return repo.PlatformHost
	}
	kind := platform.Kind(repo.Platform)
	if kind == "" {
		kind = platform.KindGitHub
	}
	if host, ok := platform.DefaultHost(kind); ok {
		return host
	}
	return platform.DefaultGitHubHost
}

func (s *Syncer) syncRepoOverview(
	ctx context.Context,
	client Client,
	repo RepoRef,
	repoID int64,
	cloneFetchOK bool,
) {
	releases, err := client.ListReleases(ctx, repo.Owner, repo.Name, 10)
	if err != nil {
		logSyncFailure(ctx, slog.LevelWarn, "list repo releases failed", err,
			"repo", repo.Owner+"/"+repo.Name,
		)
		return
	}

	selectedReleases := displayReleases(releases, 3)
	overview := db.RepoOverview{}
	for _, release := range selectedReleases {
		overview.Releases = append(overview.Releases, repoReleaseFromGitHub(release))
	}
	if len(overview.Releases) > 0 {
		overview.LatestRelease = &overview.Releases[0]
	}

	var timelineTags []string
	var selectedTags []*gh.RepositoryTag
	if len(selectedReleases) == 0 {
		tags, err := client.ListTags(ctx, repo.Owner, repo.Name, 3)
		if err != nil {
			logSyncFailure(ctx, slog.LevelWarn, "list repo tags failed", err,
				"repo", repo.Owner+"/"+repo.Name,
			)
		} else {
			selectedTags = displayTags(tags, 3)
			for _, tag := range selectedTags {
				overview.Releases = append(overview.Releases, repoReleaseFromTag(
					repo.PlatformHost,
					repo.Owner,
					repo.Name,
					tag,
				))
			}
			if len(overview.Releases) > 0 {
				overview.LatestRelease = &overview.Releases[0]
			}
			for _, tag := range selectedTags {
				timelineTags = append(timelineTags, tag.GetName())
			}
		}
	} else {
		for _, release := range selectedReleases {
			timelineTags = append(timelineTags, release.GetTagName())
		}
	}

	s.addRepoOverviewTimeline(ctx, repo, cloneFetchOK, timelineTags, &overview)

	if err := s.db.UpsertRepoOverview(ctx, repoID, overview); err != nil {
		slog.Warn("store repo overview failed",
			"repo", repo.Owner+"/"+repo.Name, "err", err,
		)
	}
}

func (s *Syncer) syncProviderRepoOverview(
	ctx context.Context,
	repo RepoRef,
	repoID int64,
	cloneFetchOK bool,
) {
	releaseReader, err := s.releaseReaderFor(repo)
	if err != nil {
		if !errors.Is(err, platform.ErrUnsupportedCapability) {
			slog.Warn("resolve repo release reader failed",
				"repo", repo.Owner+"/"+repo.Name, "err", err,
			)
		}
		return
	}

	releases, err := releaseReader.ListReleases(ctx, platformRepoRef(repo))
	if err != nil {
		logSyncFailure(ctx, slog.LevelWarn, "list repo releases failed", err,
			"repo", repo.Owner+"/"+repo.Name,
		)
		return
	}

	selectedReleases := displayPlatformReleases(releases, 3)
	overview := db.RepoOverview{}
	for _, release := range selectedReleases {
		overview.Releases = append(overview.Releases, repoReleaseFromPlatform(release))
	}
	if len(overview.Releases) > 0 {
		overview.LatestRelease = &overview.Releases[0]
	}

	timelineTags := make([]string, 0, len(selectedReleases))
	if len(selectedReleases) == 0 {
		tagReader, tagErr := s.tagReaderFor(repo)
		if tagErr != nil {
			if !errors.Is(tagErr, platform.ErrUnsupportedCapability) {
				slog.Warn("resolve repo tag reader failed",
					"repo", repo.Owner+"/"+repo.Name, "err", tagErr,
				)
			}
		} else {
			tags, tagErr := tagReader.ListTags(ctx, platformRepoRef(repo))
			if tagErr != nil {
				logSyncFailure(ctx, slog.LevelWarn, "list repo tags failed", tagErr,
					"repo", repo.Owner+"/"+repo.Name,
				)
			} else {
				selectedTags := displayPlatformTags(tags, 3)
				for _, tag := range selectedTags {
					overview.Releases = append(overview.Releases, repoReleaseFromPlatformTag(tag))
					timelineTags = append(timelineTags, tag.Name)
				}
				if len(overview.Releases) > 0 {
					overview.LatestRelease = &overview.Releases[0]
				}
			}
		}
	} else {
		for _, release := range selectedReleases {
			timelineTags = append(timelineTags, release.TagName)
		}
	}

	s.addRepoOverviewTimeline(ctx, repo, cloneFetchOK, timelineTags, &overview)

	if err := s.db.UpsertRepoOverview(ctx, repoID, overview); err != nil {
		slog.Warn("store repo overview failed",
			"repo", repo.Owner+"/"+repo.Name, "err", err,
		)
	}
}

func (s *Syncer) addRepoOverviewTimeline(
	ctx context.Context,
	repo RepoRef,
	cloneFetchOK bool,
	tags []string,
	overview *db.RepoOverview,
) {
	if len(tags) == 0 || s.clones == nil || !cloneFetchOK {
		return
	}
	host := repo.PlatformHost
	if host == "" {
		host = "github.com"
	}
	latestTag := tags[0]
	count, _, countErr := s.clones.CommitTimelineSinceTag(
		ctx, string(repoPlatform(repo)), host, repo.Owner, repo.Name, latestTag, 1,
	)
	if countErr != nil {
		slog.Warn("count commits since latest version failed",
			"repo", repo.Owner+"/"+repo.Name,
			"tag", latestTag, "err", countErr,
		)
	} else {
		overview.CommitsSinceRelease = &count
	}

	timelineTag := tags[len(tags)-1]
	_, points, err := s.clones.CommitTimelineSinceTag(
		ctx, string(repoPlatform(repo)), host, repo.Owner, repo.Name,
		timelineTag, repoOverviewTimelineLimit,
	)
	if err != nil {
		slog.Warn("build repo commit timeline failed",
			"repo", repo.Owner+"/"+repo.Name,
			"tag", timelineTag, "err", err,
		)
		return
	}
	overview.CommitTimeline = make([]db.RepoCommitTimelinePoint, 0, len(points))
	for _, point := range points {
		overview.CommitTimeline = append(overview.CommitTimeline, db.RepoCommitTimelinePoint{
			SHA:         point.SHA,
			Message:     point.Message,
			CommittedAt: point.CommittedAt.UTC(),
		})
	}
	now := time.Now().UTC()
	overview.TimelineUpdatedAt = &now
}

func displayReleases(
	releases []*gh.RepositoryRelease,
	limit int,
) []*gh.RepositoryRelease {
	if limit < 1 {
		limit = 1
	}
	out := make([]*gh.RepositoryRelease, 0, limit)
	for _, release := range releases {
		if release == nil || release.GetDraft() || release.GetTagName() == "" {
			continue
		}
		out = append(out, release)
		if len(out) == limit {
			return out
		}
	}
	return out
}

func displayTags(
	tags []*gh.RepositoryTag,
	limit int,
) []*gh.RepositoryTag {
	if limit < 1 {
		limit = 1
	}
	out := make([]*gh.RepositoryTag, 0, limit)
	for _, tag := range tags {
		if tag == nil || tag.GetName() == "" {
			continue
		}
		out = append(out, tag)
		if len(out) == limit {
			return out
		}
	}
	return out
}

func displayPlatformReleases(releases []platform.Release, limit int) []platform.Release {
	if limit < 1 {
		limit = 1
	}
	out := make([]platform.Release, 0, limit)
	for _, release := range releases {
		if release.TagName == "" {
			continue
		}
		out = append(out, release)
		if len(out) == limit {
			return out
		}
	}
	return out
}

func displayPlatformTags(tags []platform.Tag, limit int) []platform.Tag {
	if limit < 1 {
		limit = 1
	}
	out := make([]platform.Tag, 0, limit)
	for _, tag := range tags {
		if tag.Name == "" {
			continue
		}
		out = append(out, tag)
		if len(out) == limit {
			return out
		}
	}
	return out
}

func repoReleaseFromGitHub(release *gh.RepositoryRelease) db.RepoRelease {
	out := db.RepoRelease{
		TagName:         release.GetTagName(),
		Name:            release.GetName(),
		URL:             release.GetHTMLURL(),
		TargetCommitish: release.GetTargetCommitish(),
		Prerelease:      release.GetPrerelease(),
	}
	publishedAt := release.GetPublishedAt().Time
	if !publishedAt.IsZero() {
		publishedAt = publishedAt.UTC()
		out.PublishedAt = &publishedAt
	}
	return out
}

func repoReleaseFromPlatform(release platform.Release) db.RepoRelease {
	out := db.RepoRelease{
		TagName:         release.TagName,
		Name:            release.Name,
		URL:             release.URL,
		TargetCommitish: release.TargetCommitish,
		Prerelease:      release.Prerelease,
	}
	if release.PublishedAt != nil && !release.PublishedAt.IsZero() {
		publishedAt := release.PublishedAt.UTC()
		out.PublishedAt = &publishedAt
	}
	return out
}

func repoReleaseFromTag(platformHost, owner, repo string, tag *gh.RepositoryTag) db.RepoRelease {
	host := platformHost
	if host == "" {
		host = "github.com"
	}
	tagName := tag.GetName()
	return db.RepoRelease{
		TagName:         tagName,
		Name:            tagName,
		URL:             "https://" + host + "/" + owner + "/" + repo + "/tree/" + url.PathEscape(tagName),
		TargetCommitish: tag.GetCommit().GetSHA(),
	}
}

func repoReleaseFromPlatformTag(tag platform.Tag) db.RepoRelease {
	return db.RepoRelease{
		TagName:         tag.Name,
		Name:            tag.Name,
		URL:             tag.URL,
		TargetCommitish: tag.SHA,
	}
}

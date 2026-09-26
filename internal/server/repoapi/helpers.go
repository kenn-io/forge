package repoapi

import (
	"context"
	"fmt"

	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/spokeapi"
)

// filterConfiguredRepos returns only repos that are currently tracked.
func (s *Handlers) FilterConfiguredRepos(repos []db.Repo) []db.Repo {
	tracked := s.trackedConfiguredRepoSet()
	filtered := make([]db.Repo, 0, len(repos))
	for _, r := range repos {
		if _, ok := tracked[configuredDBRepoKey(r)]; ok {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func (s *Handlers) FilterConfiguredRepoSummaries(
	summaries []db.RepoSummary,
) []db.RepoSummary {
	tracked := s.trackedConfiguredRepoSet()
	filtered := make([]db.RepoSummary, 0, len(summaries))
	for _, summary := range summaries {
		repo := summary.Repo
		if _, ok := tracked[configuredDBRepoKey(repo)]; ok {
			filtered = append(filtered, summary)
		}
	}
	return filtered
}

// filterHiddenRepos removes repositories with a hidden-from-UI preference.
// It applies only to interactive repository catalogs (selectors, pickers);
// item feeds, direct routes, and the settings surface stay unfiltered.
func (s *Handlers) FilterHiddenRepos(
	ctx context.Context, repos []db.Repo,
) ([]db.Repo, error) {
	hiddenIDs, err := s.hiddenRepoIDSet(ctx)
	if err != nil {
		return nil, err
	}
	if len(hiddenIDs) == 0 {
		return repos, nil
	}
	filtered := make([]db.Repo, 0, len(repos))
	for _, repo := range repos {
		if _, ok := hiddenIDs[repo.ID]; ok {
			continue
		}
		filtered = append(filtered, repo)
	}
	return filtered, nil
}

func (s *Handlers) FilterHiddenRepoSummaries(
	ctx context.Context, summaries []db.RepoSummary,
) ([]db.RepoSummary, error) {
	hiddenIDs, err := s.hiddenRepoIDSet(ctx)
	if err != nil {
		return nil, err
	}
	if len(hiddenIDs) == 0 {
		return summaries, nil
	}
	filtered := make([]db.RepoSummary, 0, len(summaries))
	for _, summary := range summaries {
		if _, ok := hiddenIDs[summary.Repo.ID]; ok {
			continue
		}
		filtered = append(filtered, summary)
	}
	return filtered, nil
}

func (s *Handlers) hiddenRepoIDSet(
	ctx context.Context,
) (map[int64]struct{}, error) {
	if s.Db == nil {
		return nil, nil
	}
	hidden, err := s.Db.HiddenRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("list hidden repos: %w", err)
	}
	ids := make(map[int64]struct{}, len(hidden))
	for _, repo := range hidden {
		ids[repo.ID] = struct{}{}
	}
	return ids, nil
}

func (s *Handlers) trackedConfiguredRepoSet() map[string]struct{} {
	trackedRepos := (*s.Syncer).TrackedRepos()
	tracked := make(map[string]struct{}, len(trackedRepos))
	for _, repo := range trackedRepos {
		tracked[spokeapi.TrackedRepoKey(repo)] = struct{}{}
	}
	return tracked
}

func configuredDBRepoKey(repo db.Repo) string {
	return spokeapi.TrackedRepoKey(ghclient.RepoRef{
		Platform:     httpapi.ProviderKind(repo),
		PlatformHost: httpapi.ProviderHost(repo),
		Owner:        repo.Owner,
		Name:         repo.Name,
		RepoPath:     repo.RepoPath,
	})
}

func (s *Handlers) RepoResponse(repo db.Repo) itemapi.RepoResponse {
	return itemapi.RepoResponse{
		ID:                  repo.ID,
		Platform:            repo.Platform,
		PlatformHost:        repo.PlatformHost,
		PlatformRepoID:      repo.PlatformRepoID,
		Owner:               repo.Owner,
		Name:                repo.Name,
		LastSyncStartedAt:   repo.LastSyncStartedAt,
		LastSyncCompletedAt: repo.LastSyncCompletedAt,
		LastSyncError:       repo.LastSyncError,
		AllowSquashMerge:    repo.AllowSquashMerge,
		AllowMergeCommit:    repo.AllowMergeCommit,
		AllowRebaseMerge:    repo.AllowRebaseMerge,
		ViewerCanMerge:      repo.ViewerCanMerge,
		CreatedAt:           repo.CreatedAt,
		Capabilities:        s.RepoResolver.CapabilitiesForRepo(repo),
		Operations:          s.RepoOperations(repo),
	}
}

func (s *Handlers) IsConfiguredRepoTracked(repo db.Repo) bool {
	_, ok := s.trackedConfiguredRepoSet()[configuredDBRepoKey(repo)]
	return ok
}

func (s *Handlers) ToRepoSummaryResponse(
	summary db.RepoSummary,
	defaultPlatformHost string,
) itemapi.RepoSummaryResponse {
	resp := itemapi.RepoSummaryResponse{
		Repo:                s.RepoResolver.Ref(summary.Repo),
		PlatformHost:        summary.Repo.PlatformHost,
		DefaultPlatformHost: defaultPlatformHost,
		Owner:               summary.Repo.Owner,
		Name:                summary.Repo.Name,
		LastSyncError:       summary.Repo.LastSyncError,
		CachedPRCount:       summary.CachedPRCount,
		OpenPRCount:         summary.OpenPRCount,
		DraftPRCount:        summary.DraftPRCount,
		CachedIssueCount:    summary.CachedIssueCount,
		OpenIssueCount:      summary.OpenIssueCount,
		ActiveAuthors:       make([]itemapi.RepoSummaryAuthorResponse, 0, len(summary.ActiveAuthors)),
		RecentIssues:        make([]itemapi.RepoSummaryIssueResponse, 0, len(summary.RecentIssues)),
		Operations:          s.RepoOperations(summary.Repo),
	}
	if summary.Repo.LastSyncStartedAt != nil {
		resp.LastSyncStartedAt = itemapi.FormatUTCRFC3339(*summary.Repo.LastSyncStartedAt)
	}
	if summary.Repo.LastSyncCompletedAt != nil {
		resp.LastSyncCompletedAt = itemapi.FormatUTCRFC3339(*summary.Repo.LastSyncCompletedAt)
	}
	if summary.MostRecentActivityAt != nil {
		resp.MostRecentActivityAt = itemapi.FormatUTCRFC3339(*summary.MostRecentActivityAt)
	}
	if summary.Overview.LatestRelease != nil {
		release := summary.Overview.LatestRelease
		resp.LatestRelease = &itemapi.RepoSummaryReleaseResponse{
			TagName:         release.TagName,
			Name:            release.Name,
			URL:             release.URL,
			TargetCommitish: release.TargetCommitish,
			Prerelease:      release.Prerelease,
		}
		if release.PublishedAt != nil {
			resp.LatestRelease.PublishedAt = itemapi.FormatUTCRFC3339(*release.PublishedAt)
		}
	}
	resp.Releases = make([]itemapi.RepoSummaryReleaseResponse, 0, len(summary.Overview.Releases))
	for _, release := range summary.Overview.Releases {
		item := itemapi.RepoSummaryReleaseResponse{
			TagName:         release.TagName,
			Name:            release.Name,
			URL:             release.URL,
			TargetCommitish: release.TargetCommitish,
			Prerelease:      release.Prerelease,
		}
		if release.PublishedAt != nil {
			item.PublishedAt = itemapi.FormatUTCRFC3339(*release.PublishedAt)
		}
		resp.Releases = append(resp.Releases, item)
	}
	resp.CommitsSinceRelease = summary.Overview.CommitsSinceRelease
	resp.CommitTimeline = make(
		[]itemapi.RepoSummaryCommitPointResponse,
		0,
		len(summary.Overview.CommitTimeline),
	)
	for _, point := range summary.Overview.CommitTimeline {
		resp.CommitTimeline = append(resp.CommitTimeline, itemapi.RepoSummaryCommitPointResponse{
			SHA:         point.SHA,
			Message:     point.Message,
			CommittedAt: itemapi.FormatUTCRFC3339(point.CommittedAt),
		})
	}
	if summary.Overview.TimelineUpdatedAt != nil {
		resp.TimelineUpdatedAt = itemapi.FormatUTCRFC3339(*summary.Overview.TimelineUpdatedAt)
	}
	for _, author := range summary.ActiveAuthors {
		resp.ActiveAuthors = append(resp.ActiveAuthors, itemapi.RepoSummaryAuthorResponse{
			Login:     author.Login,
			ItemCount: author.ItemCount,
		})
	}
	for _, issue := range summary.RecentIssues {
		resp.RecentIssues = append(resp.RecentIssues, itemapi.RepoSummaryIssueResponse{
			Number:         issue.Number,
			Title:          issue.Title,
			Author:         issue.Author,
			State:          issue.State,
			URL:            issue.URL,
			LastActivityAt: itemapi.FormatUTCRFC3339(issue.LastActivityAt),
		})
	}
	return resp
}

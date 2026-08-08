package server

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

type starredRequest struct {
	ItemType     string `json:"item_type"`
	Provider     string `json:"provider"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Number       int    `json:"number"`
	PlatformHost string `json:"platform_host"`
}

func workspaceLookupKey(
	provider, host, owner, name, itemType string,
	number int,
) string {
	if provider == "" {
		provider = string(platform.KindGitHub)
	}
	if host == "" {
		if defaultHost, ok := platform.DefaultHost(platform.Kind(provider)); ok {
			host = defaultHost
		}
	}
	return strings.ToLower(provider) + "\x00" +
		strings.ToLower(host) + "\x00" +
		strings.ToLower(owner) + "\x00" +
		strings.ToLower(name) + "\x00" +
		itemType + "\x00" +
		fmt.Sprint(number)
}

func (s *Server) buildWorkspaceRefLookup(
	ctx context.Context,
) (map[string]workspaceapi.WorkspaceRef, error) {
	workspaces, err := s.db.ListWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	lookup := make(map[string]workspaceapi.WorkspaceRef, len(workspaces))
	for _, ws := range workspaces {
		key := workspaceLookupKey(
			ws.Platform, ws.PlatformHost, ws.RepoOwner, ws.RepoName,
			ws.ItemType, ws.ItemNumber,
		)
		if _, exists := lookup[key]; exists {
			continue
		}
		lookup[key] = workspaceapi.WorkspaceRef{ID: ws.ID, Status: ws.Status}
	}
	return lookup, nil
}

func workspaceItemTypeFromActivity(itemType string) string {
	switch itemType {
	case "pr":
		return db.WorkspaceItemTypePullRequest
	case "issue":
		return db.WorkspaceItemTypeIssue
	default:
		return ""
	}
}

func workspaceRefForActivityItem(
	lookup map[string]workspaceapi.WorkspaceRef,
	item db.ActivityItem,
) *workspaceapi.WorkspaceRef {
	workspaceItemType := workspaceItemTypeFromActivity(item.ItemType)
	if workspaceItemType == "" {
		return nil
	}
	ref, ok := lookup[workspaceLookupKey(
		item.Platform, item.PlatformHost, item.RepoOwner, item.RepoName,
		workspaceItemType, item.ItemNumber,
	)]
	if !ok {
		return nil
	}
	return &ref
}

// filterConfiguredRepos returns only repos that are currently tracked.
func (s *Server) filterConfiguredRepos(repos []db.Repo) []db.Repo {
	tracked := s.trackedConfiguredRepoSet()
	filtered := make([]db.Repo, 0, len(repos))
	for _, r := range repos {
		if _, ok := tracked[configuredDBRepoKey(r)]; ok {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func (s *Server) filterInteractiveRepos(repos []db.Repo) []db.Repo {
	visible := s.trackedInteractiveRepoSet()
	filtered := make([]db.Repo, 0, len(repos))
	for _, repo := range repos {
		if _, ok := visible[configuredDBRepoKey(repo)]; ok {
			filtered = append(filtered, repo)
		}
	}
	return filtered
}

func (s *Server) trackedInteractiveRepoSet() map[string]struct{} {
	s.cfgMu.Lock()
	configured := slices.Clone(s.cfg.Repos)
	s.cfgMu.Unlock()

	trackedRepos := s.syncer.TrackedRepos()
	visible := make(map[string]struct{}, len(trackedRepos))
	for _, repo := range trackedRepos {
		hidden := false
		for _, raw := range configured {
			if raw.HideFromUI && repoMatchesConfig(repo, raw) {
				hidden = true
				break
			}
		}
		if !hidden {
			visible[trackedRepoKey(repo)] = struct{}{}
		}
	}
	return visible
}

func (s *Server) filterConfiguredRepoSummaries(
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

func (s *Server) trackedConfiguredRepoSet() map[string]struct{} {
	trackedRepos := s.syncer.TrackedRepos()
	tracked := make(map[string]struct{}, len(trackedRepos))
	for _, repo := range trackedRepos {
		tracked[trackedRepoKey(repo)] = struct{}{}
	}
	return tracked
}

func configuredDBRepoKey(repo db.Repo) string {
	return trackedRepoKey(ghclient.RepoRef{
		Platform:     httpapi.ProviderKind(repo),
		PlatformHost: httpapi.ProviderHost(repo),
		Owner:        repo.Owner,
		Name:         repo.Name,
		RepoPath:     repo.RepoPath,
	})
}

func (s *Server) repoResponse(repo db.Repo) repoResponse {
	return repoResponse{
		ID:                  repo.ID,
		Platform:            repo.Platform,
		PlatformHost:        repo.PlatformHost,
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
		Capabilities:        s.repoResolver.CapabilitiesForRepo(repo),
		Operations:          s.repoOperations(repo),
	}
}

func (s *Server) isConfiguredRepoTracked(repo db.Repo) bool {
	_, ok := s.trackedConfiguredRepoSet()[configuredDBRepoKey(repo)]
	return ok
}

// parseRepoFilter splits the shared repo query parameter used by pull, issue,
// and activity list endpoints. Repository filters must be provider-qualified as
// provider|platform_host/repo_path. Repo paths can contain slashes, so hosted
// filters keep everything after the host together as repoPath.
func parseRepoFilter(repo string) (provider, platformHost, owner, name, repoPath string) {
	repo = strings.Trim(repo, "/ ")
	if providerPart, hostedPath, ok := strings.Cut(repo, "|"); ok {
		provider := strings.ToLower(strings.TrimSpace(providerPart))
		if _, ok := platform.MetadataFor(platform.Kind(provider)); !ok {
			return "", "", "", "", ""
		}
		parts := strings.Split(strings.Trim(hostedPath, "/ "), "/")
		if len(parts) < 2 {
			return "", "", "", "", ""
		}
		return provider, parts[0], "", "", strings.Join(parts[1:], "/")
	}
	return "", "", "", "", ""
}

func parseRepoFilters(repo string) []db.RepoFilter {
	parts := strings.Split(repo, ",")
	filters := make([]db.RepoFilter, 0, len(parts))
	for _, part := range parts {
		provider, platformHost, owner, name, repoPath := parseRepoFilter(part)
		if repoPath != "" {
			filters = append(filters, db.RepoFilter{
				Platform:     provider,
				PlatformHost: platformHost,
				RepoPath:     repoPath,
			})
		} else if owner != "" {
			filters = append(filters, db.RepoFilter{
				Platform:     provider,
				PlatformHost: platformHost,
				RepoOwner:    owner,
				RepoName:     name,
			})
		}
	}
	return filters
}

func hasInvalidRepoFilter(repo string) bool {
	for part := range strings.SplitSeq(repo, ",") {
		part = strings.Trim(part, "/ ")
		if part == "" {
			continue
		}
		_, _, owner, _, repoPath := parseRepoFilter(part)
		if owner == "" && repoPath == "" {
			return true
		}
	}
	return false
}

func validateStarredRequest(body starredRequest) bool {
	return body.ItemType == "pr" || body.ItemType == "issue"
}

// formatUTCRFC3339 is the server's API boundary formatter for timestamps.
// Handlers pass absolute instants through this helper so JSON always leaves
// kenn-forge as explicit UTC RFC3339, regardless of how a test or caller
// constructed the original time.Time.
func formatUTCRFC3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func (s *Server) toRepoSummaryResponse(
	summary db.RepoSummary,
	defaultPlatformHost string,
) repoSummaryResponse {
	resp := repoSummaryResponse{
		Repo:                s.repoResolver.Ref(summary.Repo),
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
		ActiveAuthors:       make([]repoSummaryAuthorResponse, 0, len(summary.ActiveAuthors)),
		RecentIssues:        make([]repoSummaryIssueResponse, 0, len(summary.RecentIssues)),
		Operations:          s.repoOperations(summary.Repo),
	}
	if summary.Repo.LastSyncStartedAt != nil {
		resp.LastSyncStartedAt = formatUTCRFC3339(*summary.Repo.LastSyncStartedAt)
	}
	if summary.Repo.LastSyncCompletedAt != nil {
		resp.LastSyncCompletedAt = formatUTCRFC3339(*summary.Repo.LastSyncCompletedAt)
	}
	if summary.MostRecentActivityAt != nil {
		resp.MostRecentActivityAt = formatUTCRFC3339(*summary.MostRecentActivityAt)
	}
	if summary.Overview.LatestRelease != nil {
		release := summary.Overview.LatestRelease
		resp.LatestRelease = &repoSummaryReleaseResponse{
			TagName:         release.TagName,
			Name:            release.Name,
			URL:             release.URL,
			TargetCommitish: release.TargetCommitish,
			Prerelease:      release.Prerelease,
		}
		if release.PublishedAt != nil {
			resp.LatestRelease.PublishedAt = formatUTCRFC3339(*release.PublishedAt)
		}
	}
	resp.Releases = make([]repoSummaryReleaseResponse, 0, len(summary.Overview.Releases))
	for _, release := range summary.Overview.Releases {
		item := repoSummaryReleaseResponse{
			TagName:         release.TagName,
			Name:            release.Name,
			URL:             release.URL,
			TargetCommitish: release.TargetCommitish,
			Prerelease:      release.Prerelease,
		}
		if release.PublishedAt != nil {
			item.PublishedAt = formatUTCRFC3339(*release.PublishedAt)
		}
		resp.Releases = append(resp.Releases, item)
	}
	resp.CommitsSinceRelease = summary.Overview.CommitsSinceRelease
	resp.CommitTimeline = make(
		[]repoSummaryCommitPointResponse,
		0,
		len(summary.Overview.CommitTimeline),
	)
	for _, point := range summary.Overview.CommitTimeline {
		resp.CommitTimeline = append(resp.CommitTimeline, repoSummaryCommitPointResponse{
			SHA:         point.SHA,
			Message:     point.Message,
			CommittedAt: formatUTCRFC3339(point.CommittedAt),
		})
	}
	if summary.Overview.TimelineUpdatedAt != nil {
		resp.TimelineUpdatedAt = formatUTCRFC3339(*summary.Overview.TimelineUpdatedAt)
	}
	for _, author := range summary.ActiveAuthors {
		resp.ActiveAuthors = append(resp.ActiveAuthors, repoSummaryAuthorResponse{
			Login:     author.Login,
			ItemCount: author.ItemCount,
		})
	}
	for _, issue := range summary.RecentIssues {
		resp.RecentIssues = append(resp.RecentIssues, repoSummaryIssueResponse{
			Number:         issue.Number,
			Title:          issue.Title,
			Author:         issue.Author,
			State:          issue.State,
			URL:            issue.URL,
			LastActivityAt: formatUTCRFC3339(issue.LastActivityAt),
		})
	}
	return resp
}

// toWorktreeLinkResponses converts DB links to API responses.
// Returns an empty non-nil slice when input is nil.

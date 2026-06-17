package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
)

type repoNumberPathRef struct {
	owner        string
	name         string
	number       int
	platformHost string
}

type starredRequest struct {
	ItemType     string `json:"item_type"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Number       int    `json:"number"`
	PlatformHost string `json:"platform_host,omitempty"`
}

var errRepoNotFound = errors.New("repo not found")

// buildRepoLookup materializes a repo-id keyed map used to annotate list
// responses with owner/name information.
func buildRepoLookup(repos []db.Repo) map[int64]db.Repo {
	lookup := make(map[int64]db.Repo, len(repos))
	for _, repo := range repos {
		lookup[repo.ID] = repo
	}
	return lookup
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
) (map[string]workspaceRef, error) {
	workspaces, err := s.db.ListWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	lookup := make(map[string]workspaceRef, len(workspaces))
	for _, ws := range workspaces {
		key := workspaceLookupKey(
			ws.Platform, ws.PlatformHost, ws.RepoOwner, ws.RepoName,
			ws.ItemType, ws.ItemNumber,
		)
		if _, exists := lookup[key]; exists {
			continue
		}
		lookup[key] = workspaceRef{ID: ws.ID, Status: ws.Status}
	}
	return lookup, nil
}

func workspaceRefForRepoItem(
	lookup map[string]workspaceRef,
	repo db.Repo,
	itemType string,
	number int,
) *workspaceRef {
	ref, ok := lookup[workspaceLookupKey(
		repo.Platform, repoProviderHost(repo), repo.Owner, repo.Name,
		itemType, number,
	)]
	if !ok {
		return nil
	}
	return &ref
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
	lookup map[string]workspaceRef,
	item db.ActivityItem,
) *workspaceRef {
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

// lookupRepoMap fetches repos and returns an ID-keyed map. When config is
// available, only currently tracked repos are included so that removed repos
// are filtered out of list responses.
func (s *Server) lookupRepoMap(ctx context.Context) (map[int64]db.Repo, error) {
	repos, err := s.db.ListRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	if s.cfg != nil {
		repos = s.filterConfiguredRepos(repos)
	}
	return buildRepoLookup(repos), nil
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

// lookupRepo resolves a repository from owner/name and optional host inputs.
func (s *Server) lookupRepo(
	ctx context.Context,
	owner, name, platformHost string,
) (*db.Repo, error) {
	var (
		repo *db.Repo
		err  error
	)
	if platformHost != "" {
		repo, err = s.db.GetRepoByHostOwnerName(
			ctx, platformHost, owner, name,
		)
	} else {
		repo, err = s.db.GetRepoByOwnerName(ctx, owner, name)
	}
	if err != nil {
		return nil, fmt.Errorf("get repo: %w", err)
	}
	if repo == nil {
		return nil, errRepoNotFound
	}
	return repo, nil
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
		Platform:     repoProviderKind(repo),
		PlatformHost: repoProviderHost(repo),
		Owner:        repo.Owner,
		Name:         repo.Name,
		RepoPath:     repo.RepoPath,
	})
}

func (s *Server) isConfiguredRepoTracked(repo db.Repo) bool {
	_, ok := s.trackedConfiguredRepoSet()[configuredDBRepoKey(repo)]
	return ok
}

// lookupRepoID resolves a repository from owner/name inputs and returns a
// stable not-found error for handlers that need repo identity only.
func (s *Server) lookupRepoID(ctx context.Context, owner, name string) (int64, error) {
	repo, err := s.lookupRepo(ctx, owner, name, "")
	if err != nil {
		return 0, err
	}
	return repo.ID, nil
}

func (s *Server) lookupRepoIDOnHost(
	ctx context.Context,
	owner, name, platformHost string,
) (int64, error) {
	repo, err := s.lookupRepo(ctx, owner, name, platformHost)
	if err != nil {
		return 0, err
	}
	return repo.ID, nil
}

// lookupMRID resolves the internal MR id from the common route tuple.
func (s *Server) lookupMRID(ctx context.Context, ref repoNumberPathRef) (int64, error) {
	if ref.platformHost != "" {
		repoID, err := s.lookupRepoIDOnHost(
			ctx, ref.owner, ref.name, ref.platformHost,
		)
		if err != nil {
			return 0, err
		}
		mr, err := s.db.GetMergeRequestByRepoIDAndNumber(
			ctx, repoID, ref.number,
		)
		if err != nil {
			return 0, err
		}
		if mr == nil {
			return 0, fmt.Errorf(
				"pull request %s/%s#%d not found",
				ref.owner, ref.name, ref.number,
			)
		}
		return mr.ID, nil
	}
	return s.db.GetMRIDByRepoAndNumber(ctx, ref.owner, ref.name, ref.number)
}

// lookupIssueID resolves the internal issue id from the common route tuple.
func (s *Server) lookupIssueID(ctx context.Context, ref repoNumberPathRef) (int64, error) {
	if ref.platformHost == "" {
		return s.db.GetIssueIDByRepoAndNumber(
			ctx, ref.owner, ref.name, ref.number,
		)
	}
	repoID, err := s.lookupRepoIDOnHost(
		ctx, ref.owner, ref.name, ref.platformHost,
	)
	if err != nil {
		return 0, err
	}
	issue, err := s.db.GetIssueByRepoIDAndNumber(
		ctx, repoID, ref.number,
	)
	if err != nil {
		return 0, err
	}
	if issue == nil {
		return 0, fmt.Errorf(
			"issue %s/%s#%d not found", ref.owner, ref.name, ref.number,
		)
	}
	return issue.ID, nil
}

// parseRepoFilter splits the shared repo query parameter used by pull, issue,
// and activity list endpoints. The accepted wire forms are owner/name,
// platform_host/repo_path, and provider|platform_host/repo_path. Slash-qualified
// provider labels such as provider/platform_host/repo_path are display-only and
// intentionally parse as host-qualified values to keep nested repo paths
// unambiguous. Repo paths can contain slashes, so hosted filters keep everything
// after the host together as repoPath.
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

	parts := strings.Split(repo, "/")
	switch len(parts) {
	case 2:
		return "", "", parts[0], parts[1], ""
	default:
		if len(parts) >= 3 {
			return "", parts[0], "", "", strings.Join(parts[1:], "/")
		}
		return "", "", "", "", ""
	}
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

func validateStarredRequest(body starredRequest) bool {
	return body.ItemType == "pr" || body.ItemType == "issue"
}

// formatUTCRFC3339 is the server's API boundary formatter for timestamps.
// Handlers pass absolute instants through this helper so JSON always leaves
// middleman as explicit UTC RFC3339, regardless of how a test or caller
// constructed the original time.Time.
func formatUTCRFC3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func (s *Server) toRepoSummaryResponse(
	summary db.RepoSummary,
	defaultPlatformHost string,
) repoSummaryResponse {
	resp := repoSummaryResponse{
		Repo:                s.repoRefFromRepo(summary.Repo),
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
func toWorktreeLinkResponses(
	links []db.WorktreeLink,
) []worktreeLinkResponse {
	out := make([]worktreeLinkResponse, len(links))
	for i, l := range links {
		out[i] = worktreeLinkResponse{
			WorktreeKey:    l.WorktreeKey,
			WorktreePath:   l.WorktreePath,
			WorktreeBranch: l.WorktreeBranch,
		}
	}
	return out
}

// indexWorktreeLinksByMR groups worktree link responses by
// merge request ID.
func indexWorktreeLinksByMR(
	links []db.WorktreeLink,
) map[int64][]worktreeLinkResponse {
	m := make(map[int64][]worktreeLinkResponse)
	for _, l := range links {
		m[l.MergeRequestID] = append(
			m[l.MergeRequestID],
			worktreeLinkResponse{
				WorktreeKey:    l.WorktreeKey,
				WorktreePath:   l.WorktreePath,
				WorktreeBranch: l.WorktreeBranch,
			},
		)
	}
	return m
}

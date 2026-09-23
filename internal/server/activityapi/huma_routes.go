package activityapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/gitclone"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/platform"
)

type statusOnlyOutput = httpapi.OKStatusOutput

type starredInput struct {
	Body itemapi.StarredRequest
}

type unsetStarredInput struct {
	ItemType     string `query:"item_type" enum:"pr,issue" required:"true"`
	Provider     string `query:"provider" required:"true"`
	PlatformHost string `query:"platform_host" required:"true"`
	Owner        string `query:"owner" required:"true"`
	Name         string `query:"name" required:"true"`
	Number       int    `query:"number" required:"true"`
}

type GetRepoCommitDiffInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	SHA          string `path:"sha"`
	Whitespace   string `query:"whitespace"`
}

type GetRepoCommitDiffHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	SHA          string `path:"sha"`
	Whitespace   string `query:"whitespace"`
}

type GetRepoCommitDiffOutput = httpapi.BodyOutput[itemapi.DiffResponse]

type listReposOutput = httpapi.BodyOutput[[]itemapi.RepoResponse]

type listRepoSummariesOutput = httpapi.BodyOutput[[]itemapi.RepoSummaryResponse]

type syncStatusOutput = httpapi.BodyOutput[*ghclient.SyncStatus]

type listActivityThreadEventsInput struct {
	Provider          string   `query:"provider"`
	PlatformHost      string   `query:"platform_host"`
	PlatformRepoID    string   `query:"platform_repo_id"`
	ItemType          string   `query:"item_type" enum:"pr,issue"`
	ItemNumber        int      `query:"item_number" minimum:"1"`
	Types             []string `query:"types"`
	Search            string   `query:"search"`
	Unassigned        bool     `query:"unassigned" doc:"Only include activity for pull requests and issues with no assignees."`
	Since             string   `query:"since"`
	Before            string   `query:"before"`
	AtOrBefore        string   `query:"at_or_before"`
	Limit             int      `query:"limit" minimum:"10" maximum:"250" default:"100"`
	HideClosedMerged  bool     `query:"hide_closed_merged"`
	HideBots          bool     `query:"hide_bots"`
	HideDefaultBranch bool     `query:"hide_default_branch"`
}

type triggerSyncInput struct {
	PriorityRepos []string `query:"priority_repo" doc:"Optional repository filters to sync first. Accepts repeated provider|platform_host/repo_path values or comma-separated values."`
	OnlyRepos     []string `query:"only_repo" doc:"Optional repository filters to sync exclusively. Accepts repeated provider|platform_host/repo_path values or comma-separated values."`
}

type listActivityOutput = httpapi.BodyOutput[itemapi.ActivityResponse]

type listActivityAuthorsOutput = httpapi.BodyOutput[itemapi.ActivityAuthorsResponse]

func ApiConfig(basePath string) huma.Config {
	config := huma.DefaultConfig("kenn-forge API", "0.1.0")
	config.OpenAPIPath = "/openapi"
	config.DocsPath = "/docs"
	config.SchemasPath = "/schemas"
	config.Servers = []*huma.Server{{
		URL: strings.TrimSuffix(basePath, "/") + "/api/v1",
	}}
	return config
}

func (s *Handlers) GetRepoCommitDiff(
	ctx context.Context,
	input *GetRepoCommitDiffInput,
) (*GetRepoCommitDiffOutput, error) {
	if s.Clones == nil {
		return nil, httpapi.ServiceUnavailable("diff view not available: clone manager not configured")
	}

	repo, err := s.RepoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}

	host := httpapi.ProviderHost(*repo)
	ctx = gitclone.WithRepositoryIdentity(ctx, repo.PlatformRepoID)
	if !isFullGitObjectID(input.SHA) {
		return nil, httpapi.Validation("path.sha", "commit SHA must be a full object ID")
	}

	sha, err := s.Clones.ResolveCommit(ctx, string(httpapi.ProviderKind(*repo)), host, repo.Owner, repo.Name, input.SHA)
	if err != nil {
		if errors.Is(err, gitclone.ErrNotFound) {
			return nil, httpapi.NotFound(httpapi.CodeNotFound, "diff not available: referenced commit not found", nil)
		}
		slog.Error("failed to resolve repo commit", "owner", input.Owner, "name", input.Name, "sha", input.SHA, "err", err)
		return nil, httpapi.Upstream("failed to compute diff", "", "")
	}

	parent, err := s.Clones.ParentOf(ctx, string(httpapi.ProviderKind(*repo)), host, repo.Owner, repo.Name, sha)
	if err != nil {
		if errors.Is(err, gitclone.ErrNotFound) {
			return nil, httpapi.NotFound(httpapi.CodeNotFound, "diff not available: referenced commit not found", nil)
		}
		slog.Error("failed to resolve commit parent", "owner", input.Owner, "name", input.Name, "sha", sha, "err", err)
		return nil, httpapi.Upstream("failed to compute diff", "", "")
	}

	hideWhitespace := input.Whitespace == "hide"
	result, err := s.Clones.Diff(ctx, string(httpapi.ProviderKind(*repo)), host, repo.Owner, repo.Name, parent, sha, hideWhitespace)
	if err != nil {
		if errors.Is(err, gitclone.ErrNotFound) {
			return nil, httpapi.NotFound(httpapi.CodeNotFound, "diff not available: referenced commit not found", nil)
		}
		slog.Error("failed to compute repo commit diff", "owner", input.Owner, "name", input.Name, "sha", sha, "err", err)
		return nil, httpapi.Upstream("failed to compute diff", "", "")
	}

	return &GetRepoCommitDiffOutput{Body: itemapi.DiffResponse{
		Stale:               false,
		WhitespaceOnlyCount: result.WhitespaceOnlyCount,
		Files:               result.Files,
	}}, nil
}

func (s *Handlers) GetRepoCommitDiffOnHost(
	ctx context.Context,
	input *GetRepoCommitDiffHostInput,
) (*GetRepoCommitDiffOutput, error) {
	next := GetRepoCommitDiffInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		SHA:          input.SHA,
		Whitespace:   input.Whitespace,
	}
	return s.GetRepoCommitDiff(ctx, &next)
}

func isFullGitObjectID(value string) bool {
	switch len(value) {
	case 40, 64:
	default:
		return false
	}
	for _, c := range value {
		if c >= '0' && c <= '9' {
			continue
		}
		if c >= 'a' && c <= 'f' {
			continue
		}
		if c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

func (s *Handlers) SetStarred(ctx context.Context, input *starredInput) (*statusOnlyOutput, error) {
	repoID, err := s.lookupStarredRepoID(ctx, input.Body)
	if err != nil {
		return nil, err
	}
	if err := s.Db.SetStarred(ctx, input.Body.ItemType, repoID, input.Body.Number); err != nil {
		return nil, httpapi.Internal("set starred failed")
	}
	return &statusOnlyOutput{Status: http.StatusOK}, nil
}

func (s *Handlers) UnsetStarred(ctx context.Context, input *unsetStarredInput) (*statusOnlyOutput, error) {
	request := itemapi.StarredRequest{
		ItemType:     input.ItemType,
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
	}
	repoID, err := s.lookupStarredRepoID(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := s.Db.UnsetStarred(ctx, request.ItemType, repoID, request.Number); err != nil {
		return nil, httpapi.Internal("unset starred failed")
	}
	return &statusOnlyOutput{Status: http.StatusOK}, nil
}

func (s *Handlers) GetRepo(ctx context.Context, input *itemapi.GetRepoInput) (*itemapi.GetRepoOutput, error) {
	repo, err := s.RepoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	return &itemapi.GetRepoOutput{Body: s.RepoResponse(*repo)}, nil
}

func (s *Handlers) ListRepos(ctx context.Context, _ *struct{}) (*listReposOutput, error) {
	repos, err := s.Db.ListRepos(ctx)
	if err != nil {
		return nil, httpapi.Internal("list repos failed")
	}
	if repos == nil {
		repos = []db.Repo{}
	}
	if (*s.Cfg) != nil {
		repos = s.FilterConfiguredRepos(repos)
	}
	repos, err = s.FilterHiddenRepos(ctx, repos)
	if err != nil {
		return nil, httpapi.Internal(err.Error())
	}

	out := make([]itemapi.RepoResponse, 0, len(repos))
	for _, repo := range repos {
		out = append(out, s.RepoResponse(repo))
	}

	return &listReposOutput{Body: out}, nil
}

func (s *Handlers) ListRepoSummaries(
	ctx context.Context, _ *struct{},
) (*listRepoSummariesOutput, error) {
	rows, err := s.ListRepoSummariesService(ctx)
	if err != nil {
		return nil, err
	}
	return &listRepoSummariesOutput{Body: rows}, nil
}

func (s *Handlers) ListRepoSummariesService(ctx context.Context) ([]itemapi.RepoSummaryResponse, error) {
	output, err := s.listRepoSummariesRouteCore(ctx)
	if err != nil {
		return nil, err
	}
	return output.Body, nil
}

func (s *Handlers) listRepoSummariesRouteCore(
	ctx context.Context,
) (*listRepoSummariesOutput, error) {
	summaries, err := s.Db.ListRepoSummaries(ctx)
	if err != nil {
		return nil, httpapi.Internal("list repo summaries failed")
	}
	if (*s.Cfg) != nil {
		summaries = s.FilterConfiguredRepoSummaries(summaries)
	}
	summaries, err = s.FilterHiddenRepoSummaries(ctx, summaries)
	if err != nil {
		return nil, httpapi.Internal(err.Error())
	}

	defaultPlatformHost := s.DefaultPlatformHost()
	out := make([]itemapi.RepoSummaryResponse, 0, len(summaries))
	for _, summary := range summaries {
		out = append(out, s.ToRepoSummaryResponse(
			summary, defaultPlatformHost,
		))
	}

	return &listRepoSummariesOutput{Body: out}, nil
}

func (s *Handlers) TriggerSync(
	ctx context.Context,
	input *triggerSyncInput,
) (*itemapi.AcceptedOutput, error) {
	if err := s.RequireSync(); err != nil {
		return nil, err
	}
	if len(input.OnlyRepos) > 0 {
		repos, err := s.onlyReposFromFilter(input.OnlyRepos)
		if err != nil {
			return nil, httpapi.Validation("query.only_repo", err.Error())
		}
		if !(*s.Syncer).TriggerRunForRepos(context.WithoutCancel(ctx), repos) {
			return nil, httpapi.ServiceUnavailable("syncer is stopping")
		}
		return &itemapi.AcceptedOutput{Status: http.StatusAccepted}, nil
	}
	if !(*s.Syncer).TriggerRunWithPriority(
		context.WithoutCancel(ctx),
		s.priorityReposFromFilter(input.PriorityRepos),
	) {
		return nil, httpapi.ServiceUnavailable("syncer is stopping")
	}
	if s.NotificationsEnabled() {
		s.RunBackground(func(bgCtx context.Context) {
			if err := (*s.Syncer).RunNotificationSync(bgCtx); err != nil {
				slog.Warn("notification sync failed", "err", err)
			}
		})
	}
	return &itemapi.AcceptedOutput{Status: http.StatusAccepted}, nil
}

func (s *Handlers) RequireSync() error {
	if (*s.Syncer) == nil {
		return httpapi.ServiceUnavailable("syncer not configured")
	}
	if !(*s.Syncer).SyncEnabled() {
		return httpapi.ServiceUnavailable(platform.ErrSyncDisabled.Error())
	}
	return nil
}

func (s *Handlers) onlyReposFromFilter(filters []string) ([]ghclient.RepoRef, error) {
	values := SplitRepoFilterValues(filters)
	if len(values) == 0 {
		return nil, fmt.Errorf("repository must match a configured provider|platform_host/repo_path")
	}

	tracked := (*s.Syncer).TrackedRepos()
	out := make([]ghclient.RepoRef, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		repo, ok := MatchPriorityRepo(value, tracked)
		if !ok {
			return nil, fmt.Errorf("repository %q must match a configured provider|platform_host/repo_path", value)
		}
		key := priorityRepoIdentity(repo)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, repo)
	}
	return out, nil
}

func (s *Handlers) priorityReposFromFilter(filters []string) []ghclient.RepoRef {
	values := SplitRepoFilterValues(filters)
	if len(values) == 0 {
		return nil
	}

	tracked := (*s.Syncer).TrackedRepos()
	out := make([]ghclient.RepoRef, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if repo, ok := MatchPriorityRepo(value, tracked); ok {
			key := priorityRepoIdentity(repo)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, repo)
		}
	}
	return out
}

func SplitRepoFilterValues(filters []string) []string {
	values := make([]string, 0, len(filters))
	for _, filter := range filters {
		for value := range strings.SplitSeq(filter, ",") {
			value = strings.Trim(value, "/ ")
			if value != "" {
				values = append(values, value)
			}
		}
	}
	return values
}

func MatchPriorityRepo(
	filter string,
	tracked []ghclient.RepoRef,
) (ghclient.RepoRef, bool) {
	if provider, value, ok := strings.Cut(filter, "|"); ok {
		provider = strings.TrimSpace(provider)
		value = strings.Trim(value, "/ ")
		parts := strings.Split(value, "/")
		if provider == "" || len(parts) < 3 {
			return ghclient.RepoRef{}, false
		}
		kind, err := platform.NormalizeKind(provider)
		if err != nil {
			return ghclient.RepoRef{}, false
		}

		host := parts[0]
		path := strings.Join(parts[1:], "/")
		for _, repo := range tracked {
			if repoPlatformForPriority(repo) == kind &&
				strings.EqualFold(repoHostForPriority(repo), host) &&
				strings.EqualFold(repoPathForPriority(repo), path) {
				return repo, true
			}
		}
		return ghclient.RepoRef{}, false
	}
	return ghclient.RepoRef{}, false
}

func priorityRepoIdentity(repo ghclient.RepoRef) string {
	return strings.ToLower(
		string(repoPlatformForPriority(repo)) + "/" +
			repoHostForPriority(repo) + "/" +
			repoPathForPriority(repo),
	)
}

func repoPathForPriority(repo ghclient.RepoRef) string {
	path := strings.Trim(repo.RepoPath, "/ ")
	if path != "" {
		return path
	}
	return strings.Trim(repo.Owner, "/ ") + "/" + strings.Trim(repo.Name, "/ ")
}

func repoHostForPriority(repo ghclient.RepoRef) string {
	host := strings.TrimSpace(repo.PlatformHost)
	if host != "" {
		return strings.ToLower(host)
	}
	kind := repoPlatformForPriority(repo)
	if defaultHost, ok := platform.DefaultHost(kind); ok {
		return defaultHost
	}
	return platform.DefaultGitHubHost
}

func repoPlatformForPriority(repo ghclient.RepoRef) platform.Kind {
	if repo.Platform != "" {
		return repo.Platform
	}
	return platform.KindGitHub
}

func (s *Handlers) SyncStatus(_ context.Context, _ *struct{}) (*syncStatusOutput, error) {
	if (*s.Syncer) == nil {
		return &syncStatusOutput{Body: &ghclient.SyncStatus{}}, nil
	}
	return &syncStatusOutput{Body: (*s.Syncer).Status()}, nil
}

func (s *Handlers) SyncPRCI(ctx context.Context, input *itemapi.RepoNumberInput) (*itemapi.SyncPRCIOutput, error) {
	repo, err := s.RepoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}

	mr, err := s.Db.GetVisibleMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get pull request: " + err.Error())
	}
	if mr == nil {
		return nil, httpapi.NotFound(httpapi.CodePullNotFound, "pull request not found", nil)
	}
	warnings, err := (*s.Syncer).RefreshMRCIStatusOnProvider(
		ctx,
		ghclient.RepoRef{
			Platform:           httpapi.ProviderKind(*repo),
			Owner:              repo.Owner,
			Name:               repo.Name,
			PlatformHost:       httpapi.ProviderHost(*repo),
			RepoPath:           repo.RepoPath,
			PlatformExternalID: repo.PlatformRepoID,
			WebURL:             repo.WebURL,
			CloneURL:           repo.CloneURL,
			DefaultBranch:      repo.DefaultBranch,
		},
		repo.ID,
		input.Number,
		mr.PlatformHeadSHA,
	)
	if err != nil {
		return nil, httpapi.ProviderCallProblemWithDetail(
			err,
			string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo),
			"refresh PR CI: "+err.Error(),
		)
	}

	mr, err = s.Db.GetVisibleMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get pull request: " + err.Error())
	}
	if mr == nil {
		return nil, httpapi.NotFound(httpapi.CodePullNotFound, "pull request not found after CI refresh", nil)
	}
	body, err := (*s.PullAPI).BuildDetail(ctx, mr)
	if err != nil {
		return nil, err
	}
	body.Warnings = append(body.Warnings, warnings...)
	return &itemapi.SyncPRCIOutput{Body: body}, nil
}

func (s *Handlers) EnqueuePRSync(ctx context.Context, input *itemapi.RepoNumberInput) (*itemapi.AcceptedOutput, error) {
	repo, err := s.RepoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	removed, err := s.Db.IsArchiveItemRemovedUpstream(
		ctx, repo.ID, db.ArchiveItemTypeMergeRequest, input.Number,
	)
	if err != nil {
		return nil, httpapi.Internal("check pull request visibility: " + err.Error())
	}
	if removed {
		return nil, httpapi.NotFound(
			httpapi.CodePullNotFound, "pull request not found", nil,
		)
	}
	kind := httpapi.ProviderKind(*repo)
	host := httpapi.ProviderHost(*repo)
	key := "pr:" + string(kind) + ":" + host + ":" + repo.RepoPath +
		"#" + strconv.Itoa(input.Number)
	s.EnqueueDetailSyncOrRerun(
		key,
		[]any{
			"type", "pr",
			"provider", string(kind),
			"platform_host", host,
			"repo_path", repo.RepoPath,
			"owner", repo.Owner,
			"name", repo.Name,
			"number", input.Number,
		},
		func(ctx context.Context) error {
			removed, err := s.Db.IsArchiveItemRemovedUpstream(
				ctx, repo.ID, db.ArchiveItemTypeMergeRequest, input.Number,
			)
			if err != nil {
				return fmt.Errorf("check pull request visibility: %w", err)
			}
			if removed {
				return nil
			}
			return (*s.Syncer).SyncMROnProvider(
				ctx, kind, host, repo.Owner, repo.Name, input.Number,
			)
		},
	)
	return &itemapi.AcceptedOutput{Status: http.StatusAccepted}, nil
}

func (s *Handlers) SyncIssue(ctx context.Context, input *itemapi.IssueRepoNumberInput) (*itemapi.SyncIssueOutput, error) {
	repo, err := s.RepoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	removed, err := s.Db.IsArchiveItemRemovedUpstream(
		ctx, repo.ID, db.ArchiveItemTypeIssue, input.Number,
	)
	if err != nil {
		return nil, httpapi.Internal("check issue visibility: " + err.Error())
	}
	if removed {
		return nil, httpapi.NotFound(
			httpapi.CodeIssueNotFound, "issue not found", nil,
		)
	}
	err = (*s.Syncer).SyncIssueOnProvider(
		ctx, httpapi.ProviderKind(*repo), httpapi.ProviderHost(*repo),
		repo.Owner, repo.Name, input.Number,
	)
	if err != nil {
		if strings.Contains(err.Error(), "is not tracked") {
			return nil, httpapi.Forbidden(err.Error(), nil)
		}
		return nil, httpapi.ProviderCallProblemWithDetail(
			err,
			string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo),
			"sync issue: "+err.Error(),
		)
	}

	issue, err := s.Db.GetVisibleIssueByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get issue: " + err.Error())
	}
	if issue == nil {
		return nil, httpapi.NotFound(httpapi.CodeIssueNotFound, "issue not found after sync", nil)
	}

	syncIssueResp, err := (*s.IssueAPI).BuildDetail(ctx, repo, issue)
	if err != nil {
		return nil, err
	}
	return &itemapi.SyncIssueOutput{Body: syncIssueResp}, nil
}

func (s *Handlers) EnqueueIssueSync(ctx context.Context, input *itemapi.IssueRepoNumberInput) (*itemapi.AcceptedOutput, error) {
	repo, err := s.RepoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	removed, err := s.Db.IsArchiveItemRemovedUpstream(
		ctx, repo.ID, db.ArchiveItemTypeIssue, input.Number,
	)
	if err != nil {
		return nil, httpapi.Internal("check issue visibility: " + err.Error())
	}
	if removed {
		return nil, httpapi.NotFound(
			httpapi.CodeIssueNotFound, "issue not found", nil,
		)
	}
	kind := httpapi.ProviderKind(*repo)
	host := httpapi.ProviderHost(*repo)
	key := "issue:" + string(kind) + ":" + host + ":" + repo.RepoPath +
		"#" + strconv.Itoa(input.Number)
	s.EnqueueDetailSync(
		key,
		[]any{
			"type", "issue",
			"provider", string(kind),
			"platform_host", host,
			"repo_path", repo.RepoPath,
			"owner", repo.Owner,
			"name", repo.Name,
			"number", input.Number,
		},
		func(ctx context.Context) error {
			removed, err := s.Db.IsArchiveItemRemovedUpstream(
				ctx, repo.ID, db.ArchiveItemTypeIssue, input.Number,
			)
			if err != nil {
				return fmt.Errorf("check issue visibility: %w", err)
			}
			if removed {
				return nil
			}
			return (*s.Syncer).SyncIssueOnProvider(
				ctx, kind, host, repo.Owner, repo.Name, input.Number,
			)
		},
	)
	return &itemapi.AcceptedOutput{Status: http.StatusAccepted}, nil
}

func (s *Handlers) ListActivity(ctx context.Context, input *itemapi.ListActivityInput) (*listActivityOutput, error) {
	body, err := s.ListActivityService(ctx, input)
	if err != nil {
		return nil, err
	}
	return &listActivityOutput{Body: body}, nil
}

func (s *Handlers) ListActivityService(
	ctx context.Context, input *itemapi.ListActivityInput,
) (itemapi.ActivityResponse, error) {
	if (*s.ProviderSource) != nil {
		response, err := (*s.ProviderSource).ListActivity(ctx, input)
		if err != nil {
			return itemapi.ActivityResponse{}, err
		}
		return s.overlayLocalActivityWorkspaces(ctx, input, response)
	}
	output, err := s.listActivityRouteCore(ctx, input)
	if err != nil {
		return itemapi.ActivityResponse{}, err
	}
	if _, federationRequest := federationauth.PrincipalFromContext(ctx); federationRequest {
		return providerActivityResponse(output.Body), nil
	}
	if (*s.FleetAPI) != nil {
		workspaces, err := (*s.FleetAPI).ActivityWorkspaces(ctx)
		if err != nil {
			slog.Warn("list fleet activity workspaces failed", "err", err)
		} else {
			itemapi.OverlayFleetActivityWorkspaces(&output.Body, workspaces)
		}
	}
	return output.Body, nil
}

func providerActivityResponse(response itemapi.ActivityResponse) itemapi.ActivityResponse {
	response.Items = append([]itemapi.ActivityItemResponse(nil), response.Items...)
	for i := range response.Items {
		response.Items[i].Workspace = nil
	}
	response.ItemActivity = append([]itemapi.ActivitySubjectResponse(nil), response.ItemActivity...)
	for i := range response.ItemActivity {
		response.ItemActivity[i].Workspace = nil
	}
	response.WorkspaceActivity = []itemapi.WorkspaceActivitySubjectResponse{}
	return response
}

func (s *Handlers) overlayLocalActivityWorkspaces(
	ctx context.Context, input *itemapi.ListActivityInput, response itemapi.ActivityResponse,
) (itemapi.ActivityResponse, error) {
	if (*s.WorkspaceAPI) == nil {
		return providerActivityResponse(response), nil
	}
	snapshot, err := (*s.WorkspaceAPI).WorkspaceSubjectSnapshot(ctx)
	if err != nil {
		return itemapi.ActivityResponse{}, httpapi.Internal("list workspace activity failed")
	}
	return s.OverlayLocalActivityWorkspaceSnapshot(ctx, input, response, snapshot)
}

func (s *Handlers) OverlayLocalActivityWorkspaceSnapshot(
	ctx context.Context,
	input *itemapi.ListActivityInput,
	response itemapi.ActivityResponse,
	snapshot workspaceapi.WorkspaceSubjectSnapshot,
) (itemapi.ActivityResponse, error) {
	response = providerActivityResponse(response)
	if input.Unassigned {
		if err := s.retainUnassignedWorkspaceSubjects(ctx, &snapshot); err != nil {
			slog.Error("list unassigned workspace subjects failed", "err", err)
			return itemapi.ActivityResponse{}, httpapi.Internal("list workspace activity failed")
		}
	}
	repositories, err := s.WorkspaceActivityRepositoryIdentities(ctx, snapshot)
	if err != nil {
		return itemapi.ActivityResponse{}, httpapi.Internal("list workspace activity failed")
	}
	overlays := activityWorkspaceOverlays(snapshot, repositories)
	for i := range response.Items {
		if workspace, ok := overlays[itemapi.ActivityItemIdentity(response.Items[i])]; ok {
			copy := workspace
			response.Items[i].Workspace = &copy
		}
	}
	for i := range response.ItemActivity {
		if workspace, ok := overlays[itemapi.ActivitySubjectIdentity(response.ItemActivity[i])]; ok {
			copy := workspace
			response.ItemActivity[i].Workspace = &copy
		}
	}
	if input.Projection != "events" && !input.InvolvesMe {
		opts, err := localWorkspaceActivityOptions(input, (*s.Now)())
		if err != nil {
			return itemapi.ActivityResponse{}, err
		}
		response.WorkspaceActivity = s.WorkspaceActivityResponse(
			input, opts, snapshot, nil, response.UseWorkspaceActivityForRecency,
		)
	}
	return response, nil
}

func localWorkspaceActivityOptions(input *itemapi.ListActivityInput, now time.Time) (db.ListActivityOpts, error) {
	opts := db.ListActivityOpts{
		RepoFilters: itemapi.ParseRepoFilters(input.Repo),
		ItemTypes:   input.ItemTypes,
		Search:      strings.ToLower(strings.TrimSpace(input.Search)),
		Author:      strings.TrimSpace(input.Author),
		Unassigned:  input.Unassigned,
	}
	if input.Since == "" {
		defaultSince := now.UTC().AddDate(0, 0, -7)
		opts.Since = &defaultSince
		return opts, nil
	}
	since, err := time.Parse(time.RFC3339, input.Since)
	if err != nil {
		return db.ListActivityOpts{}, httpapi.Validation("query.since", "invalid since: "+err.Error())
	}
	opts.Since = &since
	return opts, nil
}

func activityWorkspaceOverlays(
	snapshot workspaceapi.WorkspaceSubjectSnapshot,
	repositories map[int64]providerplane.RepositoryIdentity,
) map[providerplane.ItemIdentity]workspaceapi.WorkspaceRef {
	overlays := make(map[providerplane.ItemIdentity]workspaceapi.WorkspaceRef)
	for key, activity := range snapshot.Subjects {
		itemType := ""
		workspace := activity.Workspace
		switch key.ItemType {
		case db.WorkspaceItemTypePullRequest:
			itemType = "pr"
		case db.WorkspaceItemTypeIssue:
			itemType = "issue"
			var ok bool
			workspace, ok = snapshot.OwnReferences[key]
			if !ok {
				continue
			}
		default:
			continue
		}
		identity := providerplane.ItemIdentity{
			Repository: providerplane.RepositoryIdentity{
				Provider:       activity.Subject.Platform,
				PlatformHost:   activity.Subject.PlatformHost,
				PlatformRepoID: activity.Subject.PlatformRepoID,
			},
			ItemType: itemType, ItemNumber: key.ItemNumber,
		}.Canonical()
		if identity.Valid() {
			overlays[identity] = workspace
		}
	}
	for key, workspace := range snapshot.OwnReferences {
		if _, ok := snapshot.Subjects[key]; ok {
			continue
		}
		itemType := ""
		switch key.ItemType {
		case db.WorkspaceItemTypePullRequest:
			itemType = "pr"
		case db.WorkspaceItemTypeIssue:
			itemType = "issue"
		default:
			continue
		}
		identity := providerplane.ItemIdentity{
			Repository: repositories[key.RepoID],
			ItemType:   itemType,
			ItemNumber: key.ItemNumber,
		}.Canonical()
		if identity.Valid() {
			overlays[identity] = workspace
		}
	}
	return overlays
}

func (s *Handlers) listActivityRouteCore(ctx context.Context, input *itemapi.ListActivityInput) (*listActivityOutput, error) {
	if itemapi.HasInvalidRepoFilter(input.Repo) {
		return nil, httpapi.Validation("query.repo", "repo filter must be provider|platform_host/repo_path")
	}

	opts := db.ListActivityOpts{
		Repo:        input.Repo,
		RepoFilters: itemapi.ParseRepoFilters(input.Repo),
		Types:       input.Types,
		ItemTypes:   input.ItemTypes,
		Search:      strings.ToLower(strings.TrimSpace(input.Search)),
		Author:      strings.TrimSpace(input.Author),
		Unassigned:  input.Unassigned,
		// Notifications are always on; this only drops notification rows in
		// SQL when no config is loaded (the nil-config safety guard), so the
		// safety-cap window is filled by real activity, not stale notifications.
		ExcludeNotifications: !s.NotificationsEnabled(),
		HideClosedMerged:     input.HideClosedMerged,
		HideBots:             input.HideBots,
		HideDefaultBranch:    input.HideDefaultBranch,
	}

	projection := input.Projection
	if projection == "" {
		projection = "full"
	}
	pageLimit := itemapi.ActivitySafetyCap
	if projection == "events" || (projection == "collapsed" && input.Limit > 0) {
		pageLimit = input.Limit
		if pageLimit == 0 {
			pageLimit = 500
		}
	}
	opts.Limit = pageLimit + 1
	if input.InvolvesMe {
		viewerLogins, err := s.ResolveAuthenticatedViewerLogins(ctx, opts.RepoFilters)
		if err != nil {
			return nil, err
		}
		opts.ViewerLogins = viewerLogins
	}

	if input.Since != "" {
		t, err := time.Parse(time.RFC3339, input.Since)
		if err != nil {
			return nil, httpapi.Validation("query.since", "invalid since: "+err.Error())
		}
		opts.Since = &t
	} else {
		defaultSince := (*s.Now)().UTC().AddDate(0, 0, -7)
		opts.Since = &defaultSince
	}

	if input.After != "" {
		t, source, sourceID, err := db.DecodeCursor(input.After)
		if err != nil {
			return nil, httpapi.Validation("query.after", "invalid after cursor: "+err.Error())
		}
		opts.AfterTime = &t
		opts.AfterSource = source
		opts.AfterSourceID = sourceID
	}
	if input.Before != "" {
		t, source, sourceID, err := db.DecodeCursor(input.Before)
		if err != nil {
			return nil, httpapi.Validation("query.before", "invalid before cursor: "+err.Error())
		}
		opts.BeforeTime = &t
		opts.BeforeSource = source
		opts.BeforeSourceID = sourceID
	}
	if input.AtOrBefore != "" {
		t, source, sourceID, err := db.DecodeCursor(input.AtOrBefore)
		if err != nil {
			return nil, httpapi.Validation("query.at_or_before", "invalid upper-bound cursor: "+err.Error())
		}
		opts.AtOrBeforeTime = &t
		opts.AtOrBeforeSource = source
		opts.AtOrBeforeSourceID = sourceID
	}

	releaseReconciliation, err := s.Db.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		slog.Error("lock activity repository snapshot failed", "err", err)
		return nil, httpapi.Internal("list activity failed")
	}
	defer releaseReconciliation()
	if (*s.Cfg) != nil {
		opts.AllowedRepoIDs, err = s.trackedActivityRepoIDsUnderRepositoryReconciliationRead(ctx)
		if err != nil {
			return nil, httpapi.Internal("load tracked activity repos failed")
		}
	}
	if input.ParentPlatformRepoID != "" {
		repository, lookupErr := s.Db.GetRepositoryByProviderIDUnderRepositoryReconciliationRead(
			ctx, input.ParentProvider, input.ParentPlatformHost, input.ParentPlatformRepoID,
		)
		if lookupErr != nil {
			return nil, httpapi.Internal("resolve activity thread repository failed")
		}
		if repository == nil || repository.Lifecycle != db.RepositoryLifecycleActive ||
			!activityRepoIDAllowed(repository.Repository.ID, opts.AllowedRepoIDs) {
			opts.AllowedRepoIDs = []int64{}
		} else {
			opts.ParentRepoID = repository.Repository.ID
			opts.ParentItemType = input.ParentItemType
			opts.ParentItemNumber = input.ParentItemNumber
		}
	}

	var items []db.ActivityItem
	var itemActivity []db.ActivitySubject
	eventCursor := input.After
	if projection == "collapsed" {
		collapsed, projectionErr := s.Db.ListCollapsedActivityProjection(ctx, db.ListActivityProjectionOpts{
			ListActivityOpts: opts,
			SubjectLimit:     pageLimit + 1,
			SearchEventLimit: itemapi.ActivitySafetyCap + 1,
		})
		if projectionErr != nil {
			slog.Error("list collapsed activity failed", "err", projectionErr)
			return nil, httpapi.Internal("list activity failed")
		}
		items = collapsed.DirectRows
		itemActivity = collapsed.Subjects
		eventCursor = collapsed.EventCursor
	} else {
		items, err = s.Db.ListActivity(ctx, opts)
		if err != nil {
			slog.Error("list activity failed", "err", err)
			return nil, httpapi.Internal("list activity failed")
		}
		if len(items) > 0 {
			eventCursor = db.EncodeCursor(items[0].CreatedAt, items[0].Source, items[0].SourceID)
		}
	}
	workspaceEventItems := items
	hasFullWorkspaceEventItems := projection != "events" &&
		opts.Search != "" && (opts.AfterTime != nil || projection == "collapsed")
	if hasFullWorkspaceEventItems {
		workspaceOpts := opts
		workspaceOpts.AfterTime = nil
		workspaceOpts.AfterSource = ""
		workspaceOpts.AfterSourceID = 0
		workspaceOpts.Limit = itemapi.ActivitySafetyCap + 1
		workspaceEventItems, err = s.Db.ListActivity(ctx, workspaceOpts)
		if err != nil {
			slog.Error("list activity search subjects failed", "err", err)
			return nil, httpapi.Internal("list activity failed")
		}
	}
	// Search-matched parents are derived from the bounded event read, so an
	// event page that overflowed can hide parents whose only matches fell off
	// it; report that as parent truncation rather than a complete snapshot.
	searchMatchesTruncated := opts.Search != "" && len(workspaceEventItems) > itemapi.ActivitySafetyCap
	var searchMatchedSubjectKeys []db.WorkspaceSubjectKey
	if opts.Search != "" {
		searchMatchedSubjectKeys = make([]db.WorkspaceSubjectKey, 0, len(workspaceEventItems))
		for _, item := range workspaceEventItems {
			if item.ItemType != "pr" && item.ItemType != "issue" {
				continue
			}
			searchMatchedSubjectKeys = append(searchMatchedSubjectKeys, db.WorkspaceSubjectKey{
				RepoID: item.RepoID, ItemType: item.ItemType, ItemNumber: item.ItemNumber,
			})
		}
	}
	if projection == "full" {
		itemActivity, err = s.Db.ListActivitySubjects(ctx, db.ListActivitySubjectsOpts{
			Repo:           opts.Repo,
			RepoFilters:    opts.RepoFilters,
			AllowedRepoIDs: opts.AllowedRepoIDs,
			ItemTypes:      opts.ItemTypes,
			ExcludeNotificationRecency: opts.ExcludeNotifications ||
				(len(opts.Types) > 0 && !slices.Contains(opts.Types, "notification")),
			Search:                   opts.Search,
			SearchMatchedSubjectKeys: searchMatchedSubjectKeys,
			Author:                   opts.Author,
			Unassigned:               opts.Unassigned,
			ViewerLogins:             opts.ViewerLogins,
			HideClosedMerged:         opts.HideClosedMerged,
			HideBots:                 opts.HideBots,
			Limit:                    itemapi.ActivitySafetyCap + 1,
			Since:                    opts.Since,
		})
		if err != nil {
			slog.Error("list activity subjects failed", "err", err)
			return nil, httpapi.Internal("list activity failed")
		}
	}
	if (*s.ActivityAfterItemsForTest) != nil {
		(*s.ActivityAfterItemsForTest)()
	}
	workspaceSnapshot, err := (*s.WorkspaceAPI).WorkspaceSubjectSnapshotUnderRepositoryReconciliationRead(ctx)
	if err != nil {
		slog.Error("list workspace activity failed", "err", err)
		return nil, httpapi.Internal("list workspace activity failed")
	}
	if opts.Unassigned {
		if err := s.retainUnassignedWorkspaceSubjects(ctx, &workspaceSnapshot); err != nil {
			slog.Error("list unassigned workspace subjects failed", "err", err)
			return nil, httpapi.Internal("list workspace activity failed")
		}
	}
	if opts.ViewerLogins != nil {
		workspaceSubjectKeys := workspaceSnapshotSubjectKeys(workspaceSnapshot)
		involvedSubjects, err := s.Db.ListInvolvedWorkspaceSubjectKeys(
			ctx, opts.ViewerLogins, workspaceSubjectKeys,
		)
		if err != nil {
			slog.Error("list involved workspace subjects failed", "err", err)
			return nil, httpapi.Internal("list workspace activity failed")
		}
		for key := range workspaceSnapshot.Subjects {
			if _, ok := involvedSubjects[key]; !ok {
				delete(workspaceSnapshot.Subjects, key)
			}
		}
		for key := range workspaceSnapshot.OwnReferences {
			if _, ok := involvedSubjects[key]; !ok {
				delete(workspaceSnapshot.OwnReferences, key)
			}
		}
	}

	eventsCapped := len(items) > pageLimit
	itemActivityLimit := itemapi.ActivitySafetyCap
	if projection == "collapsed" {
		itemActivityLimit = pageLimit
	}
	itemActivityCapped := len(itemActivity) > itemActivityLimit || searchMatchesTruncated
	if len(items) > pageLimit {
		items = items[:pageLimit]
	}
	nextCursor := ""
	if projection == "events" && len(items) == pageLimit && len(items) > 0 {
		last := items[len(items)-1]
		nextCursor = db.EncodeCursor(last.CreatedAt, last.Source, last.SourceID)
	}
	if len(itemActivity) > itemActivityLimit {
		itemActivity = itemActivity[:itemActivityLimit]
	}
	if hasFullWorkspaceEventItems {
		if len(workspaceEventItems) > itemapi.ActivitySafetyCap {
			workspaceEventItems = workspaceEventItems[:itemapi.ActivitySafetyCap]
		}
	} else {
		workspaceEventItems = items
	}

	out := make([]itemapi.ActivityItemResponse, len(items))
	for i, it := range items {
		item := itemapi.ActivityItemResponse{
			ID:           it.Source + ":" + strconv.FormatInt(it.SourceID, 10),
			Cursor:       db.EncodeCursor(it.CreatedAt, it.Source, it.SourceID),
			ActivityType: it.ActivityType,
			Repo: itemapi.ActivityRepoRef(s.RepoResolver.Ref(db.Repo{
				Platform: it.Platform, PlatformHost: it.PlatformHost,
				PlatformRepoID: it.PlatformRepoID, Owner: it.RepoOwner,
				Name: it.RepoName, RepoPath: it.RepoPath,
			})),
			PlatformHost: it.PlatformHost,
			RepoOwner:    it.RepoOwner,
			RepoName:     it.RepoName,
			ItemType:     it.ItemType,
			ItemNumber:   it.ItemNumber,
			ItemTitle:    it.ItemTitle,
			ItemURL:      it.ItemURL,
			ItemState:    it.ItemState,
			Workspace:    itemapi.WorkspaceRefForActivityItem(workspaceSnapshot, it),
			Author:       it.Author,
			ItemAuthor:   it.ItemAuthor,
			CreatedAt:    itemapi.FormatUTCRFC3339(it.CreatedAt),
			BodyPreview:  it.BodyPreview,
		}
		if it.ItemLastActivityAt != nil {
			item.ItemLastActivityAt = itemapi.FormatUTCRFC3339(*it.ItemLastActivityAt)
		}
		item.BranchName = it.BranchName
		item.CommitSHA = it.CommitSHA
		item.BeforeSHA = it.BeforeSHA
		item.AfterSHA = it.AfterSHA
		item.AuthorName = it.AuthorName
		item.AuthorEmail = it.AuthorEmail
		item.CommitterName = it.CommitterName
		item.CommitterEmail = it.CommitterEmail
		if it.AuthoredAt != nil {
			item.AuthoredAt = itemapi.FormatUTCRFC3339(*it.AuthoredAt)
		}
		if it.CommittedAt != nil {
			item.CommittedAt = itemapi.FormatUTCRFC3339(*it.CommittedAt)
		}
		item.ActivityURL = it.ActivityURL
		if item.ActivityURL == "" {
			item.ActivityURL = branchActivityURL(it)
		}
		item.SubjectState = it.SubjectState
		out[i] = item
	}

	itemActivityOut := make([]itemapi.ActivitySubjectResponse, len(itemActivity))
	for i, activity := range itemActivity {
		subject := activity.Subject
		workspaceSubjectKey := subject.Key
		workspaceSubjectKey.ItemType = itemapi.WorkspaceItemTypeFromActivity(subject.Key.ItemType)
		itemActivityOut[i] = itemapi.ActivitySubjectResponse{
			Repo: itemapi.ActivityRepoRef(s.RepoResolver.Ref(db.Repo{
				Platform: subject.Platform, PlatformHost: subject.PlatformHost,
				PlatformRepoID: subject.PlatformRepoID, Owner: subject.RepoOwner,
				Name: subject.RepoName, RepoPath: subject.RepoPath,
			})),
			PlatformHost:        subject.PlatformHost,
			RepoOwner:           subject.RepoOwner,
			RepoName:            subject.RepoName,
			ItemType:            subject.Key.ItemType,
			ItemNumber:          subject.Key.ItemNumber,
			ItemTitle:           subject.Title,
			ItemURL:             subject.URL,
			ItemState:           subject.State,
			ItemAuthor:          subject.Author,
			Workspace:           itemapi.WorkspaceRefForActivitySubjectKey(workspaceSnapshot, workspaceSubjectKey),
			ActivityAt:          itemapi.FormatUTCRFC3339(activity.ActivityAt),
			EventLedgerRevision: activity.EventLedgerRevision,
		}
	}
	if projection == "events" {
		itemActivityOut = []itemapi.ActivitySubjectResponse{}
	}

	useWorkspaceActivityForRecency := s.useWorkspaceActivityForRecency()
	workspaceActivity := s.WorkspaceActivityResponse(
		input, opts, workspaceSnapshot, workspaceEventItems,
		useWorkspaceActivityForRecency,
	)
	if projection == "events" {
		workspaceActivity = []itemapi.WorkspaceActivitySubjectResponse{}
	}
	return &listActivityOutput{
		Body: itemapi.ActivityResponse{
			Items:                          out,
			ItemActivity:                   itemActivityOut,
			WorkspaceActivity:              workspaceActivity,
			UseWorkspaceActivityForRecency: useWorkspaceActivityForRecency,
			Capped:                         eventsCapped,
			ItemActivityCapped:             itemActivityCapped,
			EventCursor:                    eventCursor,
			NextCursor:                     nextCursor,
		},
	}, nil
}

func workspaceSnapshotSubjectKeys(
	snapshot workspaceapi.WorkspaceSubjectSnapshot,
) []db.WorkspaceSubjectKey {
	keys := make([]db.WorkspaceSubjectKey, 0, len(snapshot.Subjects)+len(snapshot.OwnReferences))
	for key := range snapshot.Subjects {
		keys = append(keys, key)
	}
	for key := range snapshot.OwnReferences {
		keys = append(keys, key)
	}
	return keys
}

func (s *Handlers) retainUnassignedWorkspaceSubjects(
	ctx context.Context, snapshot *workspaceapi.WorkspaceSubjectSnapshot,
) error {
	if (*s.ProviderSource) != nil {
		repositories, err := s.WorkspaceActivityRepositoryIdentities(ctx, *snapshot)
		if err != nil {
			return err
		}
		identitiesByKey, candidates := spokeapi.WorkspaceActivitySubjectIdentities(*snapshot, repositories)
		unassigned, err := (*s.ProviderSource).FilterUnassignedActivitySubjects(ctx, candidates)
		if err != nil {
			return err
		}
		spokeapi.RetainActivitySubjectsByIdentity(snapshot, identitiesByKey, unassigned)
		return nil
	}
	unassignedSubjects, err := s.Db.ListUnassignedWorkspaceSubjectKeys(
		ctx, workspaceSnapshotSubjectKeys(*snapshot),
	)
	if err != nil {
		return err
	}
	for key := range snapshot.Subjects {
		if _, ok := unassignedSubjects[key]; !ok {
			delete(snapshot.Subjects, key)
		}
	}
	for key := range snapshot.OwnReferences {
		if _, ok := unassignedSubjects[key]; !ok {
			delete(snapshot.OwnReferences, key)
		}
	}
	return nil
}

func activityRepoIDAllowed(repoID int64, allowed []int64) bool {
	if allowed == nil {
		return true
	}
	return slices.Contains(allowed, repoID)
}

func (s *Handlers) ListActivityThreadEvents(
	ctx context.Context,
	input *listActivityThreadEventsInput,
) (*listActivityOutput, error) {
	if strings.TrimSpace(input.Provider) == "" {
		return nil, httpapi.Validation("query.provider", "provider is required")
	}
	if strings.TrimSpace(input.PlatformHost) == "" {
		return nil, httpapi.Validation("query.platform_host", "platform host is required")
	}
	if strings.TrimSpace(input.PlatformRepoID) == "" {
		return nil, httpapi.Validation("query.platform_repo_id", "platform repository id is required")
	}
	if input.ItemType != "pr" && input.ItemType != "issue" {
		return nil, httpapi.Validation("query.item_type", "item type must be pr or issue")
	}
	if input.ItemNumber <= 0 {
		return nil, httpapi.Validation("query.item_number", "item number must be positive")
	}
	return s.ListActivity(ctx, &itemapi.ListActivityInput{
		Types:                input.Types,
		Search:               input.Search,
		Unassigned:           input.Unassigned,
		Since:                input.Since,
		Before:               input.Before,
		AtOrBefore:           input.AtOrBefore,
		Projection:           "events",
		Limit:                input.Limit,
		HideClosedMerged:     input.HideClosedMerged,
		HideBots:             input.HideBots,
		HideDefaultBranch:    input.HideDefaultBranch,
		ParentProvider:       input.Provider,
		ParentPlatformHost:   input.PlatformHost,
		ParentPlatformRepoID: input.PlatformRepoID,
		ParentItemType:       input.ItemType,
		ParentItemNumber:     input.ItemNumber,
	})
}

func (s *Handlers) ListActivityAuthors(
	ctx context.Context, input *itemapi.ListActivityAuthorsInput,
) (*listActivityAuthorsOutput, error) {
	if itemapi.HasInvalidRepoFilter(input.Repo) {
		return nil, httpapi.Validation("query.repo", "repo filter must be provider|platform_host/repo_path")
	}

	opts := db.ListActivityAuthorsOpts{
		RepoFilters:          itemapi.ParseRepoFilters(input.Repo),
		ExcludeNotifications: !s.NotificationsEnabled(),
	}
	if input.Since != "" {
		t, err := time.Parse(time.RFC3339, input.Since)
		if err != nil {
			return nil, httpapi.Validation("query.since", "invalid since: "+err.Error())
		}
		opts.Since = &t
	} else {
		defaultSince := (*s.Now)().UTC().AddDate(0, 0, -7)
		opts.Since = &defaultSince
	}
	if (*s.ProviderSource) != nil {
		response, err := (*s.ProviderSource).ListActivityAuthors(ctx, input)
		if err != nil {
			return nil, err
		}
		workspaceSnapshot, err := (*s.WorkspaceAPI).WorkspaceSubjectSnapshot(ctx)
		if err != nil {
			slog.Error("list workspace activity authors failed", "err", err)
			return nil, httpapi.Internal("list activity authors failed")
		}
		response.Authors = s.ActivityAuthorsWithWorkspace(
			response.Authors, workspaceSnapshot, opts,
			response.UseWorkspaceActivityForRecency,
		)
		return &listActivityAuthorsOutput{Body: response}, nil
	}
	releaseReconciliation, err := s.Db.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		slog.Error("lock activity author repository snapshot failed", "err", err)
		return nil, httpapi.Internal("list activity authors failed")
	}
	defer releaseReconciliation()
	opts.AllowedRepoIDs, err = s.trackedActivityRepoIDsUnderRepositoryReconciliationRead(ctx)
	if err != nil {
		return nil, httpapi.Internal("load tracked activity repos failed")
	}

	authors, err := s.Db.ListActivityAuthors(ctx, opts)
	if err != nil {
		slog.Error("list activity authors failed", "err", err)
		return nil, httpapi.Internal("list activity authors failed")
	}
	if _, federationRequest := federationauth.PrincipalFromContext(ctx); federationRequest {
		return &listActivityAuthorsOutput{
			Body: itemapi.ActivityAuthorsResponse{
				Authors:                        authors,
				UseWorkspaceActivityForRecency: s.useWorkspaceActivityForRecency(),
			},
		}, nil
	}
	workspaceSnapshot, err := (*s.WorkspaceAPI).WorkspaceSubjectSnapshotUnderRepositoryReconciliationRead(ctx)
	if err != nil {
		slog.Error("list workspace activity authors failed", "err", err)
		return nil, httpapi.Internal("list activity authors failed")
	}
	useWorkspaceActivityForRecency := s.useWorkspaceActivityForRecency()
	authors = s.ActivityAuthorsWithWorkspace(
		authors, workspaceSnapshot, opts, useWorkspaceActivityForRecency,
	)
	return &listActivityAuthorsOutput{
		Body: itemapi.ActivityAuthorsResponse{
			Authors:                        authors,
			UseWorkspaceActivityForRecency: useWorkspaceActivityForRecency,
		},
	}, nil
}

func (s *Handlers) ActivityAuthorsWithWorkspace(
	providerAuthors []string,
	snapshot workspaceapi.WorkspaceSubjectSnapshot,
	opts db.ListActivityAuthorsOpts,
	useWorkspaceActivityForRecency bool,
) []string {
	if !useWorkspaceActivityForRecency {
		return providerAuthors
	}
	return itemapi.MergeWorkspaceActivityAuthors(providerAuthors, snapshot, opts)
}

func (s *Handlers) useWorkspaceActivityForRecency() bool {
	s.CfgMu.Lock()
	defer s.CfgMu.Unlock()
	return (*s.Cfg) != nil && (*s.Cfg).Activity.UseWorkspaceActivityForRecency
}

func (s *Handlers) trackedActivityRepoIDsUnderRepositoryReconciliationRead(
	ctx context.Context,
) ([]int64, error) {
	repoIDs := make([]int64, 0)
	if (*s.Syncer) == nil {
		return repoIDs, nil
	}
	tracked := (*s.Syncer).TrackedRepos()
	repoIDs = make([]int64, 0, len(tracked))
	seen := make(map[int64]struct{}, len(tracked))
	for _, repo := range tracked {
		repoID := repo.RepoID
		if repoID == 0 {
			resolvedID, found, err := s.Db.ResolveRepositoryIDUnderRepositoryReconciliationRead(
				ctx, db.RepoIdentity{
					Platform:       string(repo.Platform),
					PlatformHost:   repo.PlatformHost,
					PlatformRepoID: repo.PlatformExternalID,
					Owner:          repo.Owner,
					Name:           repo.Name,
					RepoPath:       repo.RepoPath,
				},
			)
			if err != nil {
				return nil, err
			}
			if !found {
				continue
			}
			repoID = resolvedID
		}
		if repoID <= 0 {
			continue
		}
		if _, ok := seen[repoID]; ok {
			continue
		}
		seen[repoID] = struct{}{}
		repoIDs = append(repoIDs, repoID)
	}
	return repoIDs, nil
}

func branchActivityURL(it db.ActivityItem) string {
	if it.CommitSHA == "" && (it.BeforeSHA == "" || it.AfterSHA == "") {
		return ""
	}
	kind := platform.Kind(it.Platform)
	meta, ok := platform.MetadataFor(kind)
	if !ok {
		return ""
	}
	host, ok := platform.HostOrDefault(meta.Kind, it.PlatformHost)
	if !ok || host == "" {
		return ""
	}
	repoPath := escapedRepoPath(it.RepoOwner, it.RepoName)
	switch meta.Kind {
	case platform.KindGitHub, platform.KindForgejo, platform.KindGitea:
		if it.CommitSHA == "" {
			return "https://" + host + "/" + repoPath + "/compare/" +
				url.PathEscape(it.BeforeSHA) + "..." + url.PathEscape(it.AfterSHA)
		}
		return "https://" + host + "/" + repoPath + "/commit/" + url.PathEscape(it.CommitSHA)
	case platform.KindGitLab:
		if it.CommitSHA == "" {
			return "https://" + host + "/" + repoPath + "/-/compare/" +
				url.PathEscape(it.BeforeSHA) + "..." + url.PathEscape(it.AfterSHA)
		}
		return "https://" + host + "/" + repoPath + "/-/commit/" + url.PathEscape(it.CommitSHA)
	default:
		return ""
	}
}

func escapedRepoPath(owner, name string) string {
	parts := strings.Split(strings.Trim(owner+"/"+name, "/"), "/")
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			escaped = append(escaped, url.PathEscape(part))
		}
	}
	return strings.Join(escaped, "/")
}

func (s *Handlers) lookupStarredRepoID(ctx context.Context, body itemapi.StarredRequest) (int64, error) {
	if !itemapi.ValidateStarredRequest(body) {
		return 0, httpapi.Validation("body.item_type",
			"item_type must be 'pr' or 'issue'", "pr", "issue")
	}
	if strings.TrimSpace(body.Provider) == "" {
		return 0, httpapi.Validation("body.provider", "provider is required")
	}

	repo, err := s.RepoResolver.LookupRoute(
		ctx, body.Provider, body.PlatformHost, body.Owner, body.Name,
	)
	if err != nil {
		if errors.Is(err, httpapi.ErrRepoNotFound) {
			return 0, httpapi.NotFound(httpapi.CodeRepoNotFound, err.Error(), nil)
		}
		return 0, httpapi.ProviderRouteLookupError(err)
	}

	return repo.ID, nil
}

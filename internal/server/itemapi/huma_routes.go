package itemapi

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v91/github"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/ratelimit"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/platform"
)

type RepoNumberInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
}

type IssueRepoNumberInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
}

type ResolveItemInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	ItemType     string `query:"item_type" enum:"pr,issue" doc:"Optional item type hint for providers whose issues and merge requests have separate number spaces."`
}

type GetRepoInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
}

type GetRepoOutput = httpapi.BodyOutput[RepoResponse]

type CommentAutocompleteInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Trigger      string `query:"trigger"`
	Q            string `query:"q"`
	Limit        int    `query:"limit"`
	// ItemType and ItemNumber identify the item the comment is written on so
	// users already participating in it rank first among @ suggestions.
	ItemType   string `query:"item_type" enum:"pr,issue" doc:"Optional item the comment targets; requires item_number."`
	ItemNumber int64  `query:"item_number" doc:"Optional item number the comment targets; requires item_type."`
}

type CommentAutocompleteOutput = httpapi.BodyOutput[CommentAutocompleteResponse]

type RoborevConfiguredRepositoriesOutput = httpapi.BodyOutput[RoborevConfiguredRepositoriesResponse]

type AcceptedOutput = httpapi.AcceptedStatusOutput

type SyncPROutput = httpapi.BodyOutput[pullapi.MergeRequestDetailResponse]

type SyncPRCIOutput = httpapi.BodyOutput[pullapi.MergeRequestDetailResponse]

type SyncIssueOutput = httpapi.BodyOutput[issueapi.IssueDetailResponse]

type ResolveItemOutput = httpapi.BodyOutput[resolveItemResponse]

type RateLimitsOutput = httpapi.BodyOutput[RateLimitsResponse]

type StreamEventsInput struct {
	WorkspaceID string `query:"workspace_id" doc:"Optional selected local workspace to prewarm and validate while this stream is connected"`
}

type ListActivityInput struct {
	Repo                 string   `query:"repo" doc:"Repository filter. Accepts provider|platform_host/repo_path, with comma-separated values for multiple repositories."`
	Types                []string `query:"types"`
	ItemTypes            []string `query:"item_types" doc:"Item scopes included before limiting activity results: pr, issue, or repo."`
	Search               string   `query:"search"`
	Author               string   `query:"author" doc:"Exact, case-insensitive pull request or issue author filter."`
	InvolvesMe           bool     `query:"involves_me" doc:"Only include activity for pull requests and issues involving the authenticated viewer."`
	Unassigned           bool     `query:"unassigned" doc:"Only include activity for pull requests and issues with no assignees."`
	After                string   `query:"after"`
	Before               string   `query:"before"`
	AtOrBefore           string   `query:"at_or_before"`
	Since                string   `query:"since"`
	Projection           string   `query:"projection" enum:"full,collapsed,events" default:"full"`
	Limit                int      `query:"limit" minimum:"10" maximum:"500"`
	HideClosedMerged     bool     `query:"hide_closed_merged"`
	HideBots             bool     `query:"hide_bots"`
	HideDefaultBranch    bool     `query:"hide_default_branch"`
	ParentProvider       string
	ParentPlatformHost   string
	ParentPlatformRepoID string
	ParentItemType       string
	ParentItemNumber     int
}

type ListActivityAuthorsInput struct {
	Repo  string `query:"repo" doc:"Repository filter. Accepts provider|platform_host/repo_path, with comma-separated values for multiple repositories."`
	Since string `query:"since"`
}

type ListNotificationsInput struct {
	State  string   `query:"state"`
	Reason []string `query:"reason"`
	Type   []string `query:"type"`
	Repo   string   `query:"repo"`
	Search string   `query:"q"`
	Sort   string   `query:"sort"`
	Limit  int      `query:"limit"`
	Offset int      `query:"offset"`
}

type ListNotificationsOutput struct {
	Body NotificationsResponse
}

type NotificationBulkInput struct {
	Body struct {
		IDs      []int64 `json:"ids"`
		MarkRead *bool   `json:"mark_read,omitempty"`
	}
}

type NotificationBulkOutput struct {
	Body NotificationBulkResponse
}

func (s *Handlers) GetCommentAutocomplete(
	ctx context.Context,
	input *CommentAutocompleteInput,
) (*CommentAutocompleteOutput, error) {
	repo, err := s.RepoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 25 {
		limit = 25
	}

	var currentItem *db.CommentAutocompleteItem
	switch {
	case input.ItemType != "" && input.ItemNumber > 0:
		kind := "issue"
		if input.ItemType == "pr" {
			kind = "pull"
		}
		currentItem = &db.CommentAutocompleteItem{Kind: kind, Number: input.ItemNumber}
	case input.ItemType != "" || input.ItemNumber != 0:
		return nil, httpapi.Validation(
			"query.item_number",
			"item_type and item_number must be provided together",
		)
	}

	switch input.Trigger {
	case "@":
		users, err := s.Db.ListCommentAutocompleteUsers(
			ctx,
			repo.Platform,
			repo.PlatformHost,
			input.Owner,
			input.Name,
			input.Q,
			currentItem,
			limit,
		)
		if err != nil {
			return nil, httpapi.Internal("list comment autocomplete users failed")
		}
		return &CommentAutocompleteOutput{Body: CommentAutocompleteResponse{Users: users}}, nil
	case "#", "!":
		itemKind := ""
		if httpapi.ProviderKind(*repo) == platform.KindGitLab {
			itemKind = "issue"
			if input.Trigger == "!" {
				itemKind = "pull"
			}
		} else if input.Trigger == "!" {
			return nil, httpapi.Validation(
				"query.trigger",
				"trigger ! is only supported for GitLab merge requests",
				"@",
				"#",
			)
		}
		references, err := s.Db.ListCommentAutocompleteReferences(
			ctx,
			repo.Platform,
			repo.PlatformHost,
			input.Owner,
			input.Name,
			input.Q,
			itemKind,
			limit,
		)
		if err != nil {
			return nil, httpapi.Internal("list comment autocomplete references failed")
		}
		return &CommentAutocompleteOutput{Body: CommentAutocompleteResponse{References: references}}, nil
	default:
		return nil, httpapi.Validation("query.trigger", "trigger must be @, #, or GitLab !", "@", "#", "!")
	}
}

func RateLimitStatusKey(rt *ratelimit.RateTracker) string {
	return RateLimitStatusKeyFor(rt.Provider(), rt.PlatformHost(), rt.Principal())
}

// rateLimitStatusKeyFor names one principal's entry in the rate-limit
// response. Tracker-derived rows and registry-derived pools must agree on it
// or the same principal would appear twice.
func RateLimitStatusKeyFor(providerName, host, principal string) string {
	return ghclient.RateStatusKey(providerName, host, principal)
}

func RatePrincipalLabel(providerName, principal string) string {
	if providerName != string(platform.KindGitHub) || principal == "host" {
		return "Host credential"
	}
	if id, ok := strings.CutPrefix(principal, "installation:"); ok {
		return "GitHub App installation " + id
	}
	if id, ok := strings.CutPrefix(principal, "user:"); ok {
		return "GitHub user " + id
	}
	return principal
}

func (s *Handlers) SyncPR(ctx context.Context, input *RepoNumberInput) (*SyncPROutput, error) {
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
	// SyncMR distinguishes a non-fatal diff failure from a hard sync failure
	// via DiffSyncError. The PR row, timeline, and CI status are all current
	// in either case, so degrade gracefully: keep the response, but report
	// the diff problem as a warning so the UI can explain why the diff view
	// is stale or empty.
	var diffErr *ghclient.DiffSyncError
	syncErr := (*s.Syncer).SyncMROnProvider(
		ctx, httpapi.ProviderKind(*repo), httpapi.ProviderHost(*repo),
		repo.Owner, repo.Name, input.Number,
	)
	if syncErr != nil && !errors.As(syncErr, &diffErr) {
		if strings.Contains(syncErr.Error(), "is not tracked") {
			return nil, httpapi.Forbidden(syncErr.Error(), nil)
		}
		return nil, httpapi.ProviderCallProblemWithDetail(
			syncErr,
			string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo),
			"sync PR: "+syncErr.Error(),
		)
	}

	mr, err := s.Db.GetVisibleMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get pull request: " + err.Error())
	}
	if mr == nil {
		return nil, httpapi.NotFound(httpapi.CodePullNotFound, "pull request not found after sync", nil)
	}

	body, err := (*s.PullAPI).BuildDetail(ctx, mr)
	if err != nil {
		return nil, err
	}

	if diffErr != nil {
		slog.Warn("diff sync failed during sync PR",
			"owner", input.Owner,
			"name", input.Name,
			"number", input.Number,
			"code", diffErr.Code,
			"err", diffErr.Err,
		)
		// Replace inferred warnings with the explicit error, which is
		// more specific than the row-state-based diffWarnings.
		body.Warnings = []string{diffErr.UserMessage()}
	}

	return &SyncPROutput{Body: body}, nil
}

func ActivityItemIdentity(item ActivityItemResponse) providerplane.ItemIdentity {
	return providerplane.ItemIdentity{
		Repository: providerplane.RepositoryIdentity{
			Provider: item.Repo.Provider, PlatformHost: item.Repo.PlatformHost,
			PlatformRepoID: item.Repo.PlatformRepoID,
		},
		ItemType: item.ItemType, ItemNumber: item.ItemNumber,
	}.Canonical()
}

func ActivitySubjectIdentity(item ActivitySubjectResponse) providerplane.ItemIdentity {
	return providerplane.ItemIdentity{
		Repository: providerplane.RepositoryIdentity{
			Provider: item.Repo.Provider, PlatformHost: item.Repo.PlatformHost,
			PlatformRepoID: item.Repo.PlatformRepoID,
		},
		ItemType: item.ItemType, ItemNumber: item.ItemNumber,
	}.Canonical()
}

func ActivityRepoRef(repo httpapi.RepoRefResponse) ActivityRepoRefResponse {
	return ActivityRepoRefResponse{
		Provider:       repo.Provider,
		PlatformHost:   repo.PlatformHost,
		PlatformRepoID: repo.PlatformRepoID,
		RepoPath:       repo.RepoPath,
		Owner:          repo.Owner,
		Name:           repo.Name,
	}
}

func (s *Handlers) WorkspaceActivityResponse(
	input *ListActivityInput,
	opts db.ListActivityOpts,
	snapshot workspaceapi.WorkspaceSubjectSnapshot,
	providerItems []db.ActivityItem,
	useWorkspaceActivityForRecency bool,
) []WorkspaceActivitySubjectResponse {
	if !useWorkspaceActivityForRecency {
		return []WorkspaceActivitySubjectResponse{}
	}
	itemTypes := make(map[string]struct{}, len(input.ItemTypes))
	for _, itemType := range input.ItemTypes {
		itemTypes[strings.ToLower(strings.TrimSpace(itemType))] = struct{}{}
	}
	allowedRepoIDs := make(map[int64]struct{}, len(opts.AllowedRepoIDs))
	for _, repoID := range opts.AllowedRepoIDs {
		allowedRepoIDs[repoID] = struct{}{}
	}
	matchedSubjects := make(map[db.WorkspaceSubjectKey]struct{})
	if opts.Search != "" {
		for _, item := range providerItems {
			itemType := WorkspaceItemTypeFromActivity(item.ItemType)
			if itemType == "" {
				continue
			}
			matchedSubjects[db.WorkspaceSubjectKey{
				RepoID: item.RepoID, ItemType: itemType, ItemNumber: item.ItemNumber,
			}] = struct{}{}
		}
	}
	result := make([]WorkspaceActivitySubjectResponse, 0, len(snapshot.Subjects))
	for key, activity := range snapshot.Subjects {
		if activity.ActivityAt == nil || (opts.Since != nil && activity.ActivityAt.Before(*opts.Since)) {
			continue
		}
		wireType := "issue"
		if key.ItemType == db.WorkspaceItemTypePullRequest {
			wireType = "pr"
		}
		if len(itemTypes) > 0 {
			if _, ok := itemTypes[wireType]; !ok {
				continue
			}
		}
		subject := activity.Subject
		if opts.AllowedRepoIDs != nil {
			if _, ok := allowedRepoIDs[key.RepoID]; !ok {
				continue
			}
		}
		if !workspaceSubjectMatchesRepoFilters(subject, opts.RepoFilters) {
			continue
		}
		_, matchedProviderEvent := matchedSubjects[key]
		if opts.Author != "" && !strings.EqualFold(subject.Author, opts.Author) {
			continue
		}
		if opts.Search != "" {
			haystack := strings.ToLower(strings.Join([]string{
				subject.Title, subject.Author, subject.RepoOwner + "/" + subject.RepoName,
				subject.RepoPath, "#" + strconv.Itoa(key.ItemNumber),
			}, " "))
			if !matchedProviderEvent && !strings.Contains(haystack, opts.Search) {
				continue
			}
		}
		ref := activity.Workspace
		result = append(result, WorkspaceActivitySubjectResponse{
			Repo: ActivityRepoRef(s.RepoResolver.Ref(db.Repo{
				Platform: subject.Platform, PlatformHost: subject.PlatformHost,
				PlatformRepoID: subject.PlatformRepoID, Owner: subject.RepoOwner,
				Name: subject.RepoName, RepoPath: subject.RepoPath,
			})),
			PlatformHost: subject.PlatformHost, RepoOwner: subject.RepoOwner, RepoName: subject.RepoName,
			ItemType: wireType, ItemNumber: key.ItemNumber, ItemTitle: subject.Title,
			ItemURL: subject.URL, ItemState: subject.State, ItemAuthor: subject.Author,
			Workspace: &ref, ActivityAt: FormatUTCRFC3339(*activity.ActivityAt),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ActivityAt != result[j].ActivityAt {
			return result[i].ActivityAt > result[j].ActivityAt
		}
		left := result[i].Repo.RepoPath + result[i].ItemType + strconv.Itoa(result[i].ItemNumber)
		right := result[j].Repo.RepoPath + result[j].ItemType + strconv.Itoa(result[j].ItemNumber)
		return left < right
	})
	return result
}

func workspaceSubjectMatchesRepoFilters(
	subject db.WorkspaceSubjectMetadata,
	filters []db.RepoFilter,
) bool {
	if len(filters) == 0 {
		return true
	}
	for _, filter := range filters {
		if !strings.EqualFold(filter.Platform, subject.Platform) ||
			!strings.EqualFold(filter.PlatformHost, subject.PlatformHost) {
			continue
		}
		if filter.RepoPath != "" && strings.EqualFold(filter.RepoPath, subject.RepoPath) {
			return true
		}
		if filter.RepoOwner != "" && filter.RepoName != "" &&
			strings.EqualFold(filter.RepoOwner, subject.RepoOwner) &&
			strings.EqualFold(filter.RepoName, subject.RepoName) {
			return true
		}
	}
	return false
}

type workspaceActivityAuthorCandidate struct {
	author     string
	activityAt time.Time
}

func MergeWorkspaceActivityAuthors(
	providerAuthors []string,
	snapshot workspaceapi.WorkspaceSubjectSnapshot,
	opts db.ListActivityAuthorsOpts,
) []string {
	authors := make([]string, 0, len(providerAuthors)+len(snapshot.Subjects))
	seen := make(map[string]struct{}, len(providerAuthors)+len(snapshot.Subjects))
	for _, author := range providerAuthors {
		key := strings.ToLower(strings.TrimSpace(author))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		authors = append(authors, author)
	}

	allowedRepoIDs := make(map[int64]struct{}, len(opts.AllowedRepoIDs))
	for _, repoID := range opts.AllowedRepoIDs {
		allowedRepoIDs[repoID] = struct{}{}
	}
	workspaceCandidates := make(map[string]workspaceActivityAuthorCandidate)
	for key, activity := range snapshot.Subjects {
		if activity.ActivityAt == nil || (opts.Since != nil && activity.ActivityAt.Before(*opts.Since)) {
			continue
		}
		if opts.AllowedRepoIDs != nil {
			if _, ok := allowedRepoIDs[key.RepoID]; !ok {
				continue
			}
		}
		if !workspaceSubjectMatchesRepoFilters(activity.Subject, opts.RepoFilters) {
			continue
		}
		author := strings.TrimSpace(activity.Subject.Author)
		authorKey := strings.ToLower(author)
		if authorKey == "" {
			continue
		}
		if _, ok := seen[authorKey]; ok {
			continue
		}
		candidate, ok := workspaceCandidates[authorKey]
		if !ok || activity.ActivityAt.After(candidate.activityAt) ||
			(activity.ActivityAt.Equal(candidate.activityAt) && author < candidate.author) {
			workspaceCandidates[authorKey] = workspaceActivityAuthorCandidate{
				author: author, activityAt: *activity.ActivityAt,
			}
		}
	}

	orderedWorkspaceCandidates := make([]workspaceActivityAuthorCandidate, 0, len(workspaceCandidates))
	for _, candidate := range workspaceCandidates {
		orderedWorkspaceCandidates = append(orderedWorkspaceCandidates, candidate)
	}
	sort.Slice(orderedWorkspaceCandidates, func(i, j int) bool {
		left, right := orderedWorkspaceCandidates[i], orderedWorkspaceCandidates[j]
		if !left.activityAt.Equal(right.activityAt) {
			return left.activityAt.After(right.activityAt)
		}
		leftKey, rightKey := strings.ToLower(left.author), strings.ToLower(right.author)
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return left.author < right.author
	})
	for _, candidate := range orderedWorkspaceCandidates {
		authors = append(authors, candidate.author)
	}
	return authors
}

func (s *Handlers) ResolveItem(
	ctx context.Context, input *ResolveItemInput,
) (*ResolveItemOutput, error) {
	number := input.Number
	requestedItemType := input.ItemType
	if requestedItemType != "" &&
		requestedItemType != "pr" &&
		requestedItemType != "issue" {
		return nil, httpapi.Validation("query.item_type",
			"item_type must be 'pr' or 'issue'", "pr", "issue")
	}
	repo, err := s.RepoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if errors.Is(err, httpapi.ErrRepoNotFound) {
		return &ResolveItemOutput{
			Body: resolveItemResponse{
				Number:      number,
				RepoTracked: false,
			},
		}, nil
	}
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	providerKind := httpapi.ProviderKind(*repo)
	providerHost := httpapi.ProviderHost(*repo)
	itemTypeHint := requestedItemType
	if providerKind != platform.KindGitLab {
		itemTypeHint = ""
	}
	if !s.IsConfiguredRepoTracked(*repo) {
		return &ResolveItemOutput{
			Body: resolveItemResponse{
				Number:      number,
				RepoTracked: false,
			},
		}, nil
	}
	var (
		itemType string
		found    bool
	)
	if itemTypeHint != "" {
		itemType, found, err = s.Db.ResolveItemNumberOfType(
			ctx, repo.ID, number, itemTypeHint,
		)
	} else {
		itemType, found, err = s.Db.ResolveItemNumber(ctx, repo.ID, number)
	}
	if err != nil {
		return nil, httpapi.Internal("resolve item: " + err.Error())
	}
	if found {
		return &ResolveItemOutput{
			Body: resolveItemResponse{
				ItemType:    itemType,
				Number:      number,
				RepoTracked: true,
			},
		}, nil
	}
	archiveTypes := []db.ArchiveItemType{
		db.ArchiveItemTypeMergeRequest,
		db.ArchiveItemTypeIssue,
	}
	switch itemTypeHint {
	case "pr":
		archiveTypes = []db.ArchiveItemType{db.ArchiveItemTypeMergeRequest}
	case "issue":
		archiveTypes = []db.ArchiveItemType{db.ArchiveItemTypeIssue}
	}
	for _, archiveType := range archiveTypes {
		removed, removedErr := s.Db.IsArchiveItemRemovedUpstream(
			ctx, repo.ID, archiveType, number,
		)
		if removedErr != nil {
			return nil, httpapi.Internal("resolve item: " + removedErr.Error())
		}
		if removed {
			return nil, httpapi.NotFound(httpapi.CodeNotFound, "item not found", nil)
		}
	}

	if providerKind == platform.KindGitLab && itemTypeHint != "" {
		var syncErr error
		switch itemTypeHint {
		case "pr":
			syncErr = (*s.Syncer).SyncMROnProvider(
				ctx, providerKind, providerHost, repo.Owner, repo.Name, number,
			)
		case "issue":
			syncErr = (*s.Syncer).SyncIssueOnProvider(
				ctx, providerKind, providerHost, repo.Owner, repo.Name, number,
			)
		}
		var diffErr *ghclient.DiffSyncError
		if syncErr != nil && !errors.As(syncErr, &diffErr) {
			if strings.Contains(syncErr.Error(), "is not tracked") {
				return nil, httpapi.Forbidden(syncErr.Error(), nil)
			}
			return nil, httpapi.ProviderCallProblemWithDetail(
				syncErr, string(providerKind), providerHost,
				"resolve item: "+syncErr.Error(),
			)
		}
		itemType, found, err = s.Db.ResolveItemNumberOfType(
			ctx, repo.ID, number, itemTypeHint,
		)
		if err != nil {
			return nil, httpapi.Internal("resolve item: " + err.Error())
		}
		if !found {
			return nil, httpapi.NotFound(httpapi.CodeNotFound, "item not found", nil)
		}
		if diffErr != nil {
			slog.Warn("resolve item: diff sync failed but PR row was synced",
				"owner", repo.Owner,
				"name", repo.Name,
				"number", number,
				"err", syncErr,
			)
		}
		return &ResolveItemOutput{
			Body: resolveItemResponse{
				ItemType:    itemType,
				Number:      number,
				RepoTracked: true,
			},
		}, nil
	}

	if providerKind != platform.KindGitHub {
		return nil, httpapi.NotFound(httpapi.CodeNotFound, "item not found", nil)
	}

	itemType, err = (*s.Syncer).SyncItemByNumber(
		ctx, repo.Owner, repo.Name, number,
	)
	if err == nil && itemTypeHint != "" && itemType != itemTypeHint {
		return nil, httpapi.NotFound(httpapi.CodeNotFound, "item not found", nil)
	}
	// A DiffSyncError means the PR row was upserted but the diff
	// computation failed. Resolution doesn't need diff data, so treat
	// the result as success here. The resolve response has no warnings
	// field, so the staleness reaches the client when they navigate to
	// the PR detail page: getPull infers the warning from the persisted
	// row state via diffWarnings.
	var diffErr *ghclient.DiffSyncError
	if err != nil && !errors.As(err, &diffErr) {
		// Classified lookup outcomes (removed, inaccessible, moved with
		// its destination) arrive as platform errors; map them to their
		// typed problems instead of collapsing into an internal error.
		if _, ok := errors.AsType[*platform.Error](err); ok {
			return nil, httpapi.MapPlatformError(err)
		}
		if ghErr, ok := errors.AsType[*gh.ErrorResponse](err); ok {
			if ghErr.Response != nil &&
				ghErr.Response.StatusCode == 404 {
				return nil, httpapi.NotFound(httpapi.CodeNotFound,
					"item not found: "+err.Error(), nil)
			}
			return nil, httpapi.Upstream(
				"GitHub API error: "+err.Error(),
				string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo),
			)
		}
		return nil, httpapi.Internal("resolve item: " + err.Error())
	}
	if diffErr != nil {
		slog.Warn("resolve item: diff sync failed but PR row was synced",
			"owner", repo.Owner,
			"name", repo.Name,
			"number", number,
			"err", err,
		)
	}
	resolvedType, visible, resolveErr := s.Db.ResolveItemNumber(ctx, repo.ID, number)
	if resolveErr != nil {
		return nil, httpapi.Internal("resolve item: " + resolveErr.Error())
	}
	if !visible {
		return nil, httpapi.NotFound(httpapi.CodeNotFound, "item not found", nil)
	}
	itemType = resolvedType

	return &ResolveItemOutput{
		Body: resolveItemResponse{
			ItemType:    itemType,
			Number:      number,
			RepoTracked: true,
		},
	}, nil
}

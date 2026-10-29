package mcpserver

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type itemRefInput struct {
	Type                    string           `json:"type" jsonschema:"item type: pr or issue"`
	Provider                string           `json:"provider,omitempty" jsonschema:"provider kind: github, gitlab, forgejo, gitea, or bitbucket; required unless set in repository"`
	PlatformHost            string           `json:"platform_host,omitempty" jsonschema:"provider host from Forge reads, such as github.com or a self-hosted instance; defaults to the provider's public host (github.com, gitlab.com, codeberg.org, gitea.com, bitbucket.org); required for self-hosted instances and Bitbucket Data Center"`
	PlatformRepoID          int64            `json:"platform_repo_id,omitempty" jsonschema:"provider's integer repository ID from kenn_forge_list_repos or any Forge item; when omitted, Forge resolves it from provider, host, owner, and name"`
	BitbucketRepositoryUUID string           `json:"bitbucket_repository_uuid,omitempty" jsonschema:"Bitbucket Cloud repository UUID from kenn_forge_list_repos; set instead of platform_repo_id for Bitbucket Cloud repositories"`
	Owner                   string           `json:"owner,omitempty" jsonschema:"repository owner or namespace; required unless set in repository"`
	Name                    string           `json:"name,omitempty" jsonschema:"repository name; required unless set in repository"`
	Number                  int              `json:"number" jsonschema:"PR or issue number"`
	Repository              *repoFilterInput `json:"repository,omitempty" jsonschema:"repository object exactly as Forge listings return it, such as a workspace target's repository; use instead of, or together with, the flat repository fields"`
}

type getItemContextInput struct {
	Item             itemRefInput `json:"item"`
	EventLimit       int          `json:"event_limit,omitempty"`
	IncludeEvents    *bool        `json:"include_events,omitempty"`
	IncludeChecks    *bool        `json:"include_checks,omitempty"`
	IncludeWorkspace *bool        `json:"include_workspace,omitempty"`
	IncludeStack     *bool        `json:"include_stack,omitempty"`
}

type contextEvent struct {
	Type        string `json:"type"`
	Author      string `json:"author,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	Summary     string `json:"summary,omitempty"`
	BodyPreview string `json:"body_preview,omitempty"`
}

type contextCheck struct {
	Name            string `json:"name"`
	Status          string `json:"status"`
	Conclusion      string `json:"conclusion"`
	URL             string `json:"url"`
	App             string `json:"app"`
	DurationSeconds *int64 `json:"duration_seconds,omitempty"`
}

type getItemContextOutput struct {
	Labels         []string           `json:"labels,omitempty" jsonschema:"cached PR label names"`
	Item           itemRef            `json:"item"`
	PullStatus     *contextPullStatus `json:"pull_status,omitempty"`
	Body           string             `json:"body,omitempty"`
	Events         []contextEvent     `json:"events,omitempty"`
	EventsHasMore  *bool              `json:"events_has_more,omitempty" jsonschema:"true when more cached events exist than returned; omitted when events are not requested; request a larger event_limit up to 100 if needed"`
	Checks         []contextCheck     `json:"checks,omitempty"`
	Workspace      *WorkspaceRef      `json:"workspace,omitempty"`
	Stack          candidateStack     `json:"stack,omitzero"`
	Workflow       candidateWorkflow  `json:"workflow"`
	Cache          candidateCache     `json:"cache"`
	LastActivityAt string             `json:"last_activity_at,omitempty"`
}

type contextPullStatus struct {
	MergeableState string `json:"mergeable_state" jsonschema:"cached mergeability: dirty indicates a conflict on this PR or an ancestor in its stack; unknown means no conclusion"`
	ReviewDecision string `json:"review_decision"`
	CIStatus       string `json:"ci_status"`
	HeadSHA        string `json:"head_sha"`
}

type listByWorkflowInput struct {
	States        []string        `json:"states,omitempty"`
	ItemTypes     []string        `json:"item_types,omitempty"`
	Repo          repoFilterInput `json:"repo,omitzero"`
	IncludeClosed bool            `json:"include_closed,omitempty"`
	Limit         int             `json:"limit,omitempty"`
	Cursor        string          `json:"cursor,omitempty"`
}

type workflowListItem struct {
	Item           itemRef           `json:"item"`
	LastActivityAt string            `json:"last_activity_at,omitempty"`
	Workflow       candidateWorkflow `json:"workflow"`
}

type listByWorkflowOutput struct {
	Items      []workflowListItem `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

// itemIdentityHint tells agents how single-item tools identify an item.
const itemIdentityHint = " Identify the item by type and number plus provider, owner, and name, as flat fields or as a Forge listing's repository object in item.repository."

func (s *Server) registerItemTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_list_pull_contexts",
		Description: "Read a page of open PR contexts for one repository in a single call. " +
			"Prefer this for merge-readiness scans or work across all PRs. Includes cached mergeability, " +
			"CI checks, review decision, stack placement, workspace and freshness without per-PR detail reads. " +
			"Set include_events for review-comment excerpts and full stack health; this reads cached details internally. " +
			"Follow next_offset until absent. Cached evidence does not certify branch protection or merge permission.",
	}, wrapTool(s.listPullContexts))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_get_item_context",
		Description: "Return cached detail for a selected PR or issue without triggering sync. " +
			"Use include flags and event_limit to keep model context focused." + itemIdentityHint,
	}, wrapTool(s.getItemContext))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_list_items_by_workflow_state",
		Description: "List cached PRs and issues by kenn-forge-local workflow state. " +
			"Use this to inspect items already marked reviewing, waiting, or awaiting merge.",
	}, wrapTool(s.listItemsByWorkflowState))
}

func (s *Server) getItemContext(ctx context.Context, in getItemContextInput) (getItemContextOutput, error) {
	item, err := s.itemIdentity(ctx, in.Item, "item.")
	if err != nil {
		return getItemContextOutput{}, err
	}
	if item.Type == "issue" {
		return s.getIssueContext(ctx, item, in)
	}
	return s.getPullContext(ctx, item, in)
}

func (s *Server) getPullContext(
	ctx context.Context, item ItemIdentity, in getItemContextInput,
) (getItemContextOutput, error) {
	detail, err := s.backend.GetPull(ctx, item)
	if err != nil {
		return getItemContextOutput{}, err
	}
	if detail.Pull == nil {
		return getItemContextOutput{}, errors.New("pull detail missing pull")
	}
	pull := *detail.Pull
	out := pullContext(detail, in)
	workflowKey := pull.itemKey()
	workflows, err := s.workflowStatesForKeys(ctx, map[candidateKey]bool{workflowKey: true})
	if err != nil {
		return getItemContextOutput{}, err
	}
	out.Workflow = workflowForCandidate(workflowKey, workflows, pull.WorkflowStatus)
	return out, nil
}

func pullContext(detail PullDetail, in getItemContextInput) getItemContextOutput {
	pull := *detail.Pull
	out := getItemContextOutput{
		Item: pull.itemRef(), Body: pull.Body, Labels: pull.Labels,
		PullStatus: &contextPullStatus{
			MergeableState: firstNonEmpty(pull.MergeableState, "unknown"),
			ReviewDecision: pull.ReviewDecision, CIStatus: pull.CIStatus, HeadSHA: pull.HeadSHA,
		},
		Workflow:       candidateWorkflow{Status: workflowStatusOrNew(pull.WorkflowStatus)},
		Cache:          candidateCache{DetailLoaded: detail.DetailLoaded, DetailFetchedAt: detail.DetailFetchedAt},
		LastActivityAt: formatMCPTime(pull.LastActivityAt),
	}
	if boolDefault(in.IncludeEvents, true) {
		out.Events = contextEvents(detail.Events, clampLimit(in.EventLimit, 30, 100))
		out.EventsHasMore = new(len(detail.Events) > len(out.Events))
	}
	if boolDefault(in.IncludeChecks, true) {
		out.Checks = contextChecks(detail.Checks)
	}
	if boolDefault(in.IncludeWorkspace, true) {
		out.Workspace = detail.Workspace
	}
	if boolDefault(in.IncludeStack, true) && detail.Stack != nil {
		out.Stack = candidateStack{
			Present:  true,
			Position: detail.Stack.Position,
			Size:     detail.Stack.Size,
			Health:   detail.Stack.Health,
		}
	}
	return out
}

func (s *Server) getIssueContext(
	ctx context.Context, item ItemIdentity, in getItemContextInput,
) (getItemContextOutput, error) {
	detail, err := s.backend.GetIssue(ctx, item)
	if err != nil {
		return getItemContextOutput{}, err
	}
	if detail.Issue == nil {
		return getItemContextOutput{}, errors.New("issue detail missing issue")
	}
	issue := *detail.Issue
	workflow := candidateWorkflow{Status: workflowStatusOrNew(issue.WorkflowStatus)}
	if detail.Workflow != nil {
		workflow = candidateWorkflowFromState(*detail.Workflow)
	}
	out := getItemContextOutput{
		Item:           issue.itemRef(),
		Body:           issue.Body,
		Workflow:       workflow,
		Cache:          candidateCache{DetailLoaded: detail.DetailLoaded, DetailFetchedAt: detail.DetailFetchedAt},
		LastActivityAt: formatMCPTime(issue.LastActivityAt),
	}
	if boolDefault(in.IncludeEvents, true) {
		out.Events = contextEvents(detail.Events, clampLimit(in.EventLimit, 30, 100))
		out.EventsHasMore = new(len(detail.Events) > len(out.Events))
	}
	if boolDefault(in.IncludeWorkspace, true) {
		out.Workspace = detail.Workspace
	}
	return out, nil
}

func (s *Server) listItemsByWorkflowState(
	ctx context.Context,
	in listByWorkflowInput,
) (listByWorkflowOutput, error) {
	if _, err := workflowStateSet(in.States); err != nil {
		return listByWorkflowOutput{}, err
	}
	if _, _, err := itemTypeSelection(in.ItemTypes); err != nil {
		return listByWorkflowOutput{}, err
	}
	repo, err := s.repositoryFilter(ctx, in.Repo, "repo.")
	if err != nil {
		return listByWorkflowOutput{}, err
	}
	resp, err := s.backend.ListWorkflowStates(ctx, WorkflowQuery{
		Repository: repo, States: in.States, ItemTypes: in.ItemTypes,
		IncludeClosed: in.IncludeClosed, Limit: in.Limit, Cursor: in.Cursor,
	})
	if err != nil {
		return listByWorkflowOutput{}, err
	}
	out := listByWorkflowOutput{
		Items:      make([]workflowListItem, 0, len(resp.Items)),
		NextCursor: resp.NextCursor,
	}
	for _, row := range resp.Items {
		out.Items = append(out.Items, workflowListItem{
			Item:           row.itemRef(),
			LastActivityAt: row.LastActivityAt,
			Workflow:       candidateWorkflowFromState(row.Workflow),
		})
	}
	return out, nil
}

// repository merges the flat repository fields with the nested repository
// object listings return, so a listing row can pass straight through. A
// field set in both places must agree.
func (ref itemRefInput) repository(field string) (repoFilterInput, error) {
	flat := repoFilterInput{
		Provider: ref.Provider, PlatformHost: ref.PlatformHost,
		PlatformRepoID: ref.PlatformRepoID, BitbucketRepositoryUUID: ref.BitbucketRepositoryUUID,
		Owner: ref.Owner, Name: ref.Name,
	}
	if ref.Repository == nil {
		return flat, nil
	}
	nested := *ref.Repository
	var err error
	merge := func(name string, flatValue, nestedValue string, fold bool) string {
		flatValue, nestedValue = strings.TrimSpace(flatValue), strings.TrimSpace(nestedValue)
		switch {
		case err != nil || nestedValue == "":
			return flatValue
		case flatValue == "" || flatValue == nestedValue || fold && strings.EqualFold(flatValue, nestedValue):
			return nestedValue
		}
		err = FieldError(field+name, field+name+" conflicts with "+field+"repository."+name)
		return ""
	}
	merged := repoFilterInput{
		Provider:                merge("provider", flat.Provider, nested.Provider, true),
		PlatformHost:            merge("platform_host", flat.PlatformHost, nested.PlatformHost, true),
		BitbucketRepositoryUUID: merge("bitbucket_repository_uuid", flat.BitbucketRepositoryUUID, nested.BitbucketRepositoryUUID, true),
		Owner:                   merge("owner", flat.Owner, nested.Owner, false),
		Name:                    merge("name", flat.Name, nested.Name, false),
		RepoPath:                nested.RepoPath,
		PlatformRepoID:          flat.PlatformRepoID,
	}
	if err != nil {
		return repoFilterInput{}, err
	}
	switch {
	case nested.PlatformRepoID == 0:
	case flat.PlatformRepoID == 0:
		merged.PlatformRepoID = nested.PlatformRepoID
	case flat.PlatformRepoID != nested.PlatformRepoID:
		return repoFilterInput{}, FieldError(field+"platform_repo_id",
			field+"platform_repo_id conflicts with "+field+"repository.platform_repo_id")
	}
	return merged, nil
}

// normalize validates an item reference without resolving its repository
// key; Server.itemIdentity resolves a missing key from the route.
func (ref itemRefInput) normalize(field string) (ItemIdentity, error) {
	itemType := strings.ToLower(strings.TrimSpace(ref.Type))
	if itemType != "pr" && itemType != "issue" {
		return ItemIdentity{}, FieldError(field+"type", field+"type must be pr or issue")
	}
	input, err := ref.repository(field)
	if err != nil {
		return ItemIdentity{}, err
	}
	repo, err := input.normalize(field)
	if err != nil {
		return ItemIdentity{}, err
	}
	if ref.Number <= 0 {
		return ItemIdentity{}, FieldError(field+"number", field+"number must be greater than zero")
	}
	return ItemIdentity{
		Type: itemType, Provider: repo.Provider, PlatformHost: repo.PlatformHost, RepoKey: repo.Key,
		Owner: repo.Owner, Name: repo.Name, Number: ref.Number,
	}, nil
}

// itemIdentity validates an item reference from tool input and resolves its
// repository key. Tool handlers call it once where the input enters.
func (s *Server) itemIdentity(ctx context.Context, ref itemRefInput, field string) (ItemIdentity, error) {
	item, err := ref.normalize(field)
	if err != nil {
		return ItemIdentity{}, err
	}
	return s.resolveItemKey(ctx, item)
}

// resolveItemKey fills a missing repository key on a normalized item.
func (s *Server) resolveItemKey(ctx context.Context, item ItemIdentity) (ItemIdentity, error) {
	repo, err := s.resolveRepositoryKey(ctx, item.Repository())
	if err != nil {
		return ItemIdentity{}, err
	}
	item.Provider, item.PlatformHost, item.RepoKey = repo.Provider, repo.PlatformHost, repo.Key
	item.Owner, item.Name = repo.Owner, repo.Name
	return item, nil
}

// repositoryFilter resolves an optional repository filter; an empty filter
// matches every repository.
func (s *Server) repositoryFilter(ctx context.Context, r repoFilterInput, field string) (RepositoryIdentity, error) {
	if r.isZero() {
		return RepositoryIdentity{}, nil
	}
	repo, err := r.normalize(field)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	return s.resolveRepositoryKey(ctx, repo)
}

// resolveRepositoryKey fills a missing provider ID from the route. The
// provider ID names a repository and owner/name are only its last known
// route, so a supplied ID always wins; a route-only reference resolves here,
// once, to the repository that currently occupies the route, and every
// response carries that repository's ID for later calls.
func (s *Server) resolveRepositoryKey(ctx context.Context, repo RepositoryIdentity) (RepositoryIdentity, error) {
	if !repo.Key.IsZero() {
		return repo, nil
	}
	resolved, err := s.backend.ResolveRepositoryRoute(ctx, repo)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	if resolved.Key.IsZero() {
		return RepositoryIdentity{}, errors.New("repository route resolved without a provider ID")
	}
	return resolved, nil
}

func contextEvents(events []DetailEvent, limit int) []contextEvent {
	sort.Slice(events, func(i, j int) bool {
		if !events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return events[i].CreatedAt.After(events[j].CreatedAt)
		}
		return events[i].EventType < events[j].EventType
	})
	if len(events) > limit {
		events = events[:limit]
	}
	out := make([]contextEvent, 0, len(events))
	for _, event := range events {
		out = append(out, contextEvent{
			Type:        event.EventType,
			Author:      event.Author,
			CreatedAt:   formatMCPTime(event.CreatedAt),
			Summary:     event.Summary,
			BodyPreview: truncateBytes(event.Body, 500),
		})
	}
	return out
}

func (row WorkflowItem) itemRef() itemRef {
	ref := candidateKey{
		provider: row.Identity.Provider, platformHost: row.Identity.PlatformHost,
		repoKey: row.Identity.RepoKey, repoPath: repositoryPath(row.Repository),
		owner: row.Identity.Owner, name: row.Identity.Name,
		itemType: row.Identity.Type, number: row.Identity.Number,
	}.itemRef()
	ref.Title, ref.URL, ref.State, ref.Author, ref.IsDraft = row.Title, row.URL, row.State, row.Author, row.IsDraft
	return ref
}

func candidateWorkflowFromState(state WorkflowState) candidateWorkflow {
	return candidateWorkflow{
		Status: workflowStatusOrNew(state.Status), UpdatedAt: state.UpdatedAt,
		UpdatedSource: state.UpdatedSource, UpdatedActor: state.UpdatedActor,
		UpdatedReason: state.UpdatedReason,
	}
}

func contextChecks(checks []Check) []contextCheck {
	out := make([]contextCheck, 0, len(checks))
	for _, check := range checks {
		out = append(out, contextCheck(check))
	}
	return out
}

func boolDefault(value *bool, def bool) bool {
	if value == nil {
		return def
	}
	return *value
}

func truncateBytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	end := 0
	for i, r := range value {
		size := utf8.RuneLen(r)
		if r == utf8.RuneError {
			_, size = utf8.DecodeRuneInString(value[i:])
		}
		if i+size > limit {
			break
		}
		end = i + size
	}
	return value[:end]
}

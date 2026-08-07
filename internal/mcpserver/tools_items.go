package mcpserver

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type itemRefInput struct {
	Type         string `json:"type" jsonschema:"item type: pr or issue"`
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host,omitempty"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Number       int    `json:"number"`
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
	Item           itemRef             `json:"item"`
	Body           string              `json:"body,omitempty"`
	Events         []contextEvent      `json:"events,omitempty"`
	Checks         []contextCheck      `json:"checks,omitempty"`
	Workspace      *daemonWorkspaceRef `json:"workspace,omitempty"`
	Stack          candidateStack      `json:"stack,omitzero"`
	Workflow       candidateWorkflow   `json:"workflow"`
	Cache          candidateCache      `json:"cache"`
	LastActivityAt string              `json:"last_activity_at,omitempty"`
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

type daemonPullDetail struct {
	MergeRequest    *daemonPullBody     `json:"merge_request"`
	Events          []daemonDetailEvent `json:"events"`
	Repo            daemonRepoRef       `json:"repo"`
	PlatformHost    string              `json:"platform_host"`
	RepoOwner       string              `json:"repo_owner"`
	RepoName        string              `json:"repo_name"`
	DetailLoaded    bool                `json:"detail_loaded"`
	DetailFetchedAt string              `json:"detail_fetched_at"`
	Workspace       *daemonWorkspaceRef `json:"workspace"`
	Stack           *daemonStackContext `json:"stack"`
	Checks          []contextCheck      `json:"checks"`
}

type daemonPullBody struct {
	Number         int       `json:"Number"`
	Title          string    `json:"Title"`
	State          string    `json:"State"`
	Author         string    `json:"Author"`
	URL            string    `json:"URL"`
	IsDraft        bool      `json:"IsDraft"`
	Body           string    `json:"Body"`
	KanbanStatus   string    `json:"KanbanStatus"`
	LastActivityAt time.Time `json:"LastActivityAt"`
}

type daemonIssueDetail struct {
	Issue           *daemonIssueBody    `json:"issue"`
	Events          []daemonDetailEvent `json:"events"`
	Repo            daemonRepoRef       `json:"repo"`
	PlatformHost    string              `json:"platform_host"`
	RepoOwner       string              `json:"repo_owner"`
	RepoName        string              `json:"repo_name"`
	DetailLoaded    bool                `json:"detail_loaded"`
	DetailFetchedAt string              `json:"detail_fetched_at"`
	Workspace       *daemonWorkspaceRef `json:"workspace"`
	Workflow        *candidateWorkflow  `json:"workflow"`
}

type daemonIssueBody struct {
	Number         int       `json:"Number"`
	Title          string    `json:"Title"`
	State          string    `json:"State"`
	Author         string    `json:"Author"`
	URL            string    `json:"URL"`
	Body           string    `json:"Body"`
	WorkflowStatus string    `json:"WorkflowStatus"`
	LastActivityAt time.Time `json:"LastActivityAt"`
}

type daemonDetailEvent struct {
	EventType string    `json:"EventType"`
	Author    string    `json:"Author"`
	Summary   string    `json:"Summary"`
	Body      string    `json:"Body"`
	CreatedAt time.Time `json:"CreatedAt"`
}

type daemonWorkflowStateResponse struct {
	Items      []daemonWorkflowStateItem `json:"items"`
	NextCursor string                    `json:"next_cursor"`
}

type daemonWorkflowStateItem struct {
	Provider       string            `json:"provider"`
	PlatformHost   string            `json:"platform_host"`
	Owner          string            `json:"owner"`
	Name           string            `json:"name"`
	RepoPath       string            `json:"repo_path"`
	ItemType       string            `json:"item_type"`
	Number         int               `json:"number"`
	Title          string            `json:"title"`
	State          string            `json:"state"`
	URL            string            `json:"url"`
	Author         string            `json:"author"`
	IsDraft        bool              `json:"is_draft"`
	LastActivityAt string            `json:"last_activity_at"`
	Workflow       candidateWorkflow `json:"workflow"`
}

func (s *Server) registerItemTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_get_item_context",
		Description: "Return cached detail for a selected PR or issue without triggering sync. " +
			"Use include flags and event_limit to keep model context focused.",
	}, wrapTool(s.getItemContext))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_list_items_by_workflow_state",
		Description: "List cached PRs and issues by kenn-forge-local workflow state. " +
			"Use this to inspect items already marked reviewing, waiting, or awaiting merge.",
	}, wrapTool(s.listItemsByWorkflowState))
}

func (s *Server) getItemContext(ctx context.Context, in getItemContextInput) (getItemContextOutput, error) {
	if err := validateItemRef(in.Item); err != nil {
		return getItemContextOutput{}, err
	}
	switch in.Item.Type {
	case "pr":
		return s.getPullContext(ctx, in)
	case "issue":
		return s.getIssueContext(ctx, in)
	default:
		return getItemContextOutput{}, fmt.Errorf("item.type must be pr or issue")
	}
}

func (s *Server) getPullContext(ctx context.Context, in getItemContextInput) (getItemContextOutput, error) {
	var detail daemonPullDetail
	if err := s.daemon.getJSON(ctx, itemPath("pulls", in.Item), nil, &detail); err != nil {
		return getItemContextOutput{}, err
	}
	if detail.MergeRequest == nil {
		return getItemContextOutput{}, fmt.Errorf("daemon pull detail missing merge_request")
	}
	pull := daemonPull{
		Number:          detail.MergeRequest.Number,
		Title:           detail.MergeRequest.Title,
		State:           detail.MergeRequest.State,
		Author:          detail.MergeRequest.Author,
		URL:             detail.MergeRequest.URL,
		IsDraft:         detail.MergeRequest.IsDraft,
		KanbanStatus:    detail.MergeRequest.KanbanStatus,
		LastActivityAt:  detail.MergeRequest.LastActivityAt,
		Repo:            detail.Repo,
		PlatformHost:    detail.PlatformHost,
		RepoOwner:       detail.RepoOwner,
		RepoName:        detail.RepoName,
		Workspace:       detail.Workspace,
		DetailLoaded:    detail.DetailLoaded,
		DetailFetchedAt: detail.DetailFetchedAt,
	}
	out := getItemContextOutput{
		Item:           pull.itemRef(),
		Body:           detail.MergeRequest.Body,
		Cache:          candidateCache{DetailLoaded: detail.DetailLoaded, DetailFetchedAt: detail.DetailFetchedAt},
		LastActivityAt: formatMCPTime(detail.MergeRequest.LastActivityAt),
	}
	workflowKey := candidateKeyFromItem(out.Item)
	workflows, err := s.workflowStatesForKeys(ctx, map[candidateKey]bool{workflowKey: true})
	if err != nil {
		return getItemContextOutput{}, err
	}
	out.Workflow = workflowForCandidate(workflowKey, workflows, detail.MergeRequest.KanbanStatus)
	if boolDefault(in.IncludeEvents, true) {
		out.Events = contextEvents(detail.Events, clampLimit(in.EventLimit, 30, 100))
	}
	if boolDefault(in.IncludeChecks, true) {
		out.Checks = detail.Checks
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
	return out, nil
}

func (s *Server) getIssueContext(ctx context.Context, in getItemContextInput) (getItemContextOutput, error) {
	var detail daemonIssueDetail
	if err := s.daemon.getJSON(ctx, itemPath("issues", in.Item), nil, &detail); err != nil {
		return getItemContextOutput{}, err
	}
	if detail.Issue == nil {
		return getItemContextOutput{}, fmt.Errorf("daemon issue detail missing issue")
	}
	issue := daemonIssue{
		Number:          detail.Issue.Number,
		Title:           detail.Issue.Title,
		State:           detail.Issue.State,
		Author:          detail.Issue.Author,
		URL:             detail.Issue.URL,
		WorkflowStatus:  detail.Issue.WorkflowStatus,
		LastActivityAt:  detail.Issue.LastActivityAt,
		Repo:            detail.Repo,
		PlatformHost:    detail.PlatformHost,
		RepoOwner:       detail.RepoOwner,
		RepoName:        detail.RepoName,
		Workspace:       detail.Workspace,
		DetailLoaded:    detail.DetailLoaded,
		DetailFetchedAt: detail.DetailFetchedAt,
	}
	workflow := candidateWorkflow{Status: workflowStatusOrNew(detail.Issue.WorkflowStatus)}
	if detail.Workflow != nil {
		workflow = *detail.Workflow
		workflow.Status = workflowStatusOrNew(workflow.Status)
	}
	out := getItemContextOutput{
		Item:           issue.itemRef(),
		Body:           detail.Issue.Body,
		Workflow:       workflow,
		Cache:          candidateCache{DetailLoaded: detail.DetailLoaded, DetailFetchedAt: detail.DetailFetchedAt},
		LastActivityAt: formatMCPTime(detail.Issue.LastActivityAt),
	}
	if boolDefault(in.IncludeEvents, true) {
		out.Events = contextEvents(detail.Events, clampLimit(in.EventLimit, 30, 100))
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
	repo, err := in.Repo.queryValue()
	if err != nil {
		return listByWorkflowOutput{}, err
	}
	query := url.Values{}
	for _, state := range in.States {
		query.Add("state", state)
	}
	for _, itemType := range in.ItemTypes {
		query.Add("item_type", itemType)
	}
	if repo != "" {
		query.Set("repo", repo)
	}
	if in.IncludeClosed {
		query.Set("include_closed", "true")
	}
	if in.Limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", in.Limit))
	}
	if in.Cursor != "" {
		query.Set("cursor", in.Cursor)
	}
	var resp daemonWorkflowStateResponse
	if err := s.getWorkflowStateJSON(ctx, query, &resp); err != nil {
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
			Workflow:       row.Workflow,
		})
	}
	return out, nil
}

func validateItemRef(ref itemRefInput) error {
	if ref.Type != "pr" && ref.Type != "issue" {
		return fmt.Errorf("item.type must be pr or issue")
	}
	if ref.Provider == "" {
		return fmt.Errorf("item.provider is required")
	}
	if ref.Owner == "" {
		return fmt.Errorf("item.owner is required")
	}
	if ref.Name == "" {
		return fmt.Errorf("item.name is required")
	}
	if ref.Number <= 0 {
		return fmt.Errorf("item.number must be greater than zero")
	}
	return nil
}

func itemPath(kind string, ref itemRefInput) string {
	if ref.PlatformHost != "" {
		return fmt.Sprintf(
			"/api/v1/host/%s/%s/%s/%s/%s/%d",
			seg(ref.PlatformHost),
			kind,
			seg(ref.Provider),
			seg(ref.Owner),
			seg(ref.Name),
			ref.Number,
		)
	}
	return fmt.Sprintf(
		"/api/v1/%s/%s/%s/%s/%d",
		kind,
		seg(ref.Provider),
		seg(ref.Owner),
		seg(ref.Name),
		ref.Number,
	)
}

func contextEvents(events []daemonDetailEvent, limit int) []contextEvent {
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

func (row daemonWorkflowStateItem) itemRef() itemRef {
	return itemRef{
		Type:         row.ItemType,
		Provider:     row.Provider,
		PlatformHost: row.PlatformHost,
		Owner:        row.Owner,
		Name:         row.Name,
		RepoPath:     row.RepoPath,
		Number:       row.Number,
		Title:        row.Title,
		URL:          row.URL,
		State:        row.State,
		Author:       row.Author,
		IsDraft:      row.IsDraft,
	}
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

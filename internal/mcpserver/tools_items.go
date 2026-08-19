package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type itemRefInput struct {
	Type           string `json:"type" jsonschema:"item type: pr or issue"`
	Provider       string `json:"provider"`
	PlatformHost   string `json:"platform_host,omitempty"`
	PlatformRepoID string `json:"platform_repo_id" jsonschema:"stable provider-verified repository id"`
	Owner          string `json:"owner"`
	Name           string `json:"name"`
	Number         int    `json:"number"`
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
	Item           itemRef           `json:"item"`
	Body           string            `json:"body,omitempty"`
	Events         []contextEvent    `json:"events,omitempty"`
	Checks         []contextCheck    `json:"checks,omitempty"`
	Workspace      *WorkspaceRef     `json:"workspace,omitempty"`
	Stack          candidateStack    `json:"stack,omitzero"`
	Workflow       candidateWorkflow `json:"workflow"`
	Cache          candidateCache    `json:"cache"`
	LastActivityAt string            `json:"last_activity_at,omitempty"`
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
	detail, err := s.backend.GetPull(ctx, itemIdentity(in.Item))
	if err != nil {
		return getItemContextOutput{}, err
	}
	if detail.Pull == nil {
		return getItemContextOutput{}, fmt.Errorf("pull detail missing pull")
	}
	pull := *detail.Pull
	out := getItemContextOutput{
		Item:           pull.itemRef(),
		Body:           pull.Body,
		Cache:          candidateCache{DetailLoaded: detail.DetailLoaded, DetailFetchedAt: detail.DetailFetchedAt},
		LastActivityAt: formatMCPTime(pull.LastActivityAt),
	}
	workflowKey := candidateKeyFromItem(out.Item)
	workflows, err := s.workflowStatesForKeys(ctx, map[candidateKey]bool{workflowKey: true})
	if err != nil {
		return getItemContextOutput{}, err
	}
	out.Workflow = workflowForCandidate(workflowKey, workflows, pull.WorkflowStatus)
	if boolDefault(in.IncludeEvents, true) {
		out.Events = contextEvents(detail.Events, clampLimit(in.EventLimit, 30, 100))
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
	return out, nil
}

func (s *Server) getIssueContext(ctx context.Context, in getItemContextInput) (getItemContextOutput, error) {
	detail, err := s.backend.GetIssue(ctx, itemIdentity(in.Item))
	if err != nil {
		return getItemContextOutput{}, err
	}
	if detail.Issue == nil {
		return getItemContextOutput{}, fmt.Errorf("issue detail missing issue")
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
	repo, err := in.Repo.repositoryIdentity()
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

func validateItemRef(ref itemRefInput) error {
	if ref.Type != "pr" && ref.Type != "issue" {
		return fmt.Errorf("item.type must be pr or issue")
	}
	if ref.Provider == "" {
		return fmt.Errorf("item.provider is required")
	}
	if ref.PlatformRepoID == "" {
		return fmt.Errorf("item.platform_repo_id is required")
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
	return itemRef{
		Type: row.Identity.Type, Provider: row.Identity.Provider,
		PlatformHost: row.Identity.PlatformHost, PlatformRepoID: row.Identity.PlatformRepoID,
		Owner: row.Identity.Owner,
		Name:  row.Identity.Name, RepoPath: repositoryPath(row.Repository),
		Number: row.Identity.Number, Title: row.Title, URL: row.URL,
		State: row.State, Author: row.Author, IsDraft: row.IsDraft,
	}
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

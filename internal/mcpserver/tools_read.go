package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listReposInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"maximum repositories to return"`
}

type repoRow struct {
	Provider            string `json:"provider"`
	PlatformHost        string `json:"platform_host"`
	PlatformRepoID      string `json:"platform_repo_id"`
	Owner               string `json:"owner"`
	Name                string `json:"name"`
	RepoPath            string `json:"repo_path"`
	OpenPRCount         int    `json:"open_pr_count"`
	OpenIssueCount      int    `json:"open_issue_count"`
	LastSyncCompletedAt string `json:"last_sync_completed_at,omitempty"`
	LastSyncError       string `json:"last_sync_error,omitempty"`
}

type listReposOutput struct {
	Repos []repoRow `json:"repos"`
}

type listActivityInput struct {
	Since  string          `json:"since,omitempty" jsonschema:"RFC3339 timestamp or duration such as 24h; default 24h"`
	Repo   repoFilterInput `json:"repo,omitzero"`
	Types  []string        `json:"types,omitempty"`
	Search string          `json:"search,omitempty"`
	Limit  int             `json:"limit,omitempty"`
	After  string          `json:"after,omitempty" jsonschema:"opaque Forge activity cursor"`
}

type activityRow struct {
	ID           string        `json:"id"`
	Cursor       string        `json:"cursor"`
	ActivityType string        `json:"activity_type"`
	Item         itemRef       `json:"item"`
	Workspace    *WorkspaceRef `json:"workspace,omitempty"`
	Author       string        `json:"author"`
	ItemAuthor   string        `json:"item_author,omitempty"`
	CreatedAt    string        `json:"created_at"`
	BodyPreview  string        `json:"body_preview,omitempty"`
	ActivityURL  string        `json:"activity_url,omitempty"`
}

type listActivityOutput struct {
	Items  []activityRow `json:"items"`
	Capped bool          `json:"capped"`
}

type searchItemsInput struct {
	Query     string          `json:"query"`
	ItemTypes []string        `json:"item_types,omitempty" jsonschema:"item types to include: pr, issue"`
	Repo      repoFilterInput `json:"repo,omitzero"`
	State     string          `json:"state,omitempty" jsonschema:"open, closed, merged, or all; default open"`
	Limit     int             `json:"limit,omitempty"`
}

type searchResult struct {
	Item           itemRef `json:"item"`
	WorkflowStatus string  `json:"workflow_status"`
	LastActivityAt string  `json:"last_activity_at"`
}

type searchItemsOutput struct {
	Results []searchResult `json:"results"`
	Capped  bool           `json:"capped"`
}

func (s *Server) registerReadTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_list_repos",
		Description: "List repositories tracked by the running Kenn Forge daemon. " +
			"Call this first to discover valid repo filters and sync freshness.",
	}, wrapTool(s.listRepos))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_list_activity",
		Description: "Return compact recent cached activity rows from kenn-forge. " +
			"Use this for feed inspection; it does not force provider refreshes.",
	}, wrapTool(s.listActivity))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_search_items",
		Description: "Search cached PRs and issues by text, including quiet items absent from recent activity. " +
			"Call kenn_forge_list_repos first to discover valid repo filters and sync freshness.",
	}, wrapTool(s.searchItems))
}

const toolErrorMetaKey = "io.kenn.forge/error"

type toolErrorEvidence struct {
	Kind      string         `json:"kind"`
	Code      string         `json:"code,omitempty"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Ambiguous bool           `json:"ambiguous"`
	Details   map[string]any `json:"details,omitempty"`
}

type toolErrorOutput struct {
	Error toolErrorEvidence `json:"error"`
}

func wrapTool[In, Out any](
	fn func(context.Context, In) (Out, error),
) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := fn(ctx, in)
		if err == nil {
			return nil, out, nil
		}
		payload := toolErrorOutput{Error: toolErrorEvidence{
			Kind: "tool_error", Message: err.Error(),
		}}
		if backendErr, ok := errors.AsType[*Error](err); ok {
			payload.Error = toolErrorEvidence{
				Kind: backendErr.Kind, Code: backendErr.Code, Message: backendErr.Message,
				Retryable: backendErr.Retryable, Ambiguous: backendErr.Ambiguous,
				Details: backendErr.Details,
			}
		}
		content, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return nil, out, fmt.Errorf("marshal MCP tool error: %w", marshalErr)
		}
		result := &mcp.CallToolResult{
			Meta: mcp.Meta{toolErrorMetaKey: payload.Error},
			Content: []mcp.Content{&mcp.TextContent{
				Text: string(content),
			}},
		}
		result.SetError(err)
		return result, out, nil
	}
}

func (s *Server) listRepos(ctx context.Context, in listReposInput) (listReposOutput, error) {
	rows, err := s.backend.ListRepositories(ctx)
	if err != nil {
		return listReposOutput{}, err
	}
	limit := in.Limit
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := listReposOutput{Repos: make([]repoRow, 0, len(rows))}
	for _, row := range rows {
		repo := row.Repository
		out.Repos = append(out.Repos, repoRow{
			Provider:            repo.Provider,
			PlatformHost:        repo.PlatformHost,
			PlatformRepoID:      repo.PlatformRepoID,
			Owner:               repo.Owner,
			Name:                repo.Name,
			RepoPath:            repositoryPath(repo),
			OpenPRCount:         row.OpenPRCount,
			OpenIssueCount:      row.OpenIssueCount,
			LastSyncCompletedAt: row.LastSyncCompletedAt,
			LastSyncError:       row.LastSyncError,
		})
	}
	return out, nil
}

func (s *Server) listActivity(ctx context.Context, in listActivityInput) (listActivityOutput, error) {
	limit := clampLimit(in.Limit, 50, 200)
	repo, err := in.Repo.repositoryIdentity()
	if err != nil {
		return listActivityOutput{}, err
	}
	resp, err := s.backend.ListActivity(ctx, ActivityQuery{
		Since:      sinceToRFC3339(firstNonEmpty(in.Since, "24h")),
		Repository: repo, ActivityTypes: slices.Clone(in.Types),
		Search: in.Search, After: in.After,
	})
	if err != nil {
		return listActivityOutput{}, err
	}
	capped := resp.Capped || len(resp.Items) > limit
	if len(resp.Items) > limit {
		resp.Items = resp.Items[:limit]
	}
	out := listActivityOutput{Items: make([]activityRow, 0, len(resp.Items)), Capped: capped}
	for _, item := range resp.Items {
		out.Items = append(out.Items, activityRow{
			ID:           item.ID,
			Cursor:       item.Cursor,
			ActivityType: item.ActivityType,
			Item:         item.itemRef(),
			Workspace:    item.Workspace,
			Author:       item.Author,
			ItemAuthor:   item.ItemAuthor,
			CreatedAt:    item.CreatedAt,
			BodyPreview:  item.BodyPreview,
			ActivityURL:  item.ActivityURL,
		})
	}
	return out, nil
}

func (s *Server) searchItems(ctx context.Context, in searchItemsInput) (searchItemsOutput, error) {
	if strings.TrimSpace(in.Query) == "" {
		return searchItemsOutput{}, fmt.Errorf("query is required")
	}
	state := strings.TrimSpace(in.State)
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "merged" && state != "all" {
		return searchItemsOutput{}, fmt.Errorf("state must be open, closed, merged, or all")
	}
	limit := clampLimit(in.Limit, 25, 100)
	repo, err := in.Repo.repositoryIdentity()
	if err != nil {
		return searchItemsOutput{}, err
	}
	includePR, includeIssue, err := itemTypeSelection(in.ItemTypes)
	if err != nil {
		return searchItemsOutput{}, err
	}

	results := make([]searchResult, 0, limit)
	sourceCapped := false
	if includePR {
		pulls, capped, err := s.searchPulls(ctx, in.Query, state, repo, limit)
		if err != nil {
			return searchItemsOutput{}, err
		}
		sourceCapped = sourceCapped || capped
		results = append(results, pulls...)
	}
	if includeIssue && state != "merged" {
		issues, capped, err := s.searchIssues(ctx, in.Query, state, repo, limit)
		if err != nil {
			return searchItemsOutput{}, err
		}
		sourceCapped = sourceCapped || capped
		results = append(results, issues...)
	}
	sortSearchResults(results)
	capped := sourceCapped || len(results) > limit
	if capped {
		results = results[:limit]
	}
	return searchItemsOutput{Results: results, Capped: capped}, nil
}

func (s *Server) searchPulls(
	ctx context.Context,
	text string,
	state string,
	repo RepositoryIdentity,
	limit int,
) ([]searchResult, bool, error) {
	pageSize := limit + 1
	offset := 0
	out := make([]searchResult, 0, pageSize)
	for len(out) <= limit {
		rows, err := s.fetchPullSearchPage(ctx, text, state, repo, pageSize, offset)
		if err != nil {
			return nil, false, err
		}
		for _, row := range rows {
			if state == "merged" && row.State != "merged" {
				continue
			}
			if state == "closed" && row.State != "closed" {
				continue
			}
			out = append(out, searchResult{
				Item:           row.itemRef(),
				WorkflowStatus: workflowStatusOrNew(row.WorkflowStatus),
				LastActivityAt: formatMCPTime(row.LastActivityAt),
			})
			if len(out) > limit {
				return out, true, nil
			}
		}
		if len(rows) < pageSize {
			return out, false, nil
		}
		offset += len(rows)
	}
	return out, len(out) > limit, nil
}

func (s *Server) fetchPullSearchPage(
	ctx context.Context,
	text string,
	state string,
	repo RepositoryIdentity,
	limit int,
	offset int,
) ([]Pull, error) {
	queryState := state
	if state == "merged" {
		queryState = "all"
	}
	return s.backend.ListPulls(ctx, ItemListQuery{
		Repository: repo, State: queryState, Text: text, Limit: limit, Offset: offset,
	})
}

func (s *Server) searchIssues(
	ctx context.Context,
	text string,
	state string,
	repo RepositoryIdentity,
	limit int,
) ([]searchResult, bool, error) {
	rows, err := s.backend.ListIssues(ctx, ItemListQuery{
		Repository: repo, State: state, Text: text, Limit: limit + 1,
	})
	if err != nil {
		return nil, false, err
	}
	out := make([]searchResult, 0, len(rows))
	for _, row := range rows {
		out = append(out, searchResult{
			Item:           row.itemRef(),
			WorkflowStatus: workflowStatusOrNew(row.WorkflowStatus),
			LastActivityAt: formatMCPTime(row.LastActivityAt),
		})
	}
	return out, len(out) > limit, nil
}

func itemTypeSelection(values []string) (bool, bool, error) {
	if len(values) == 0 {
		return true, true, nil
	}
	var includePR, includeIssue bool
	for _, value := range values {
		switch value {
		case "pr":
			includePR = true
		case "issue":
			includeIssue = true
		default:
			return false, false, fmt.Errorf("item_types must contain only pr or issue")
		}
	}
	return includePR, includeIssue, nil
}

func sortSearchResults(results []searchResult) {
	sort.Slice(results, func(i, j int) bool {
		leftTime, leftErr := time.Parse(time.RFC3339, results[i].LastActivityAt)
		rightTime, rightErr := time.Parse(time.RFC3339, results[j].LastActivityAt)
		if leftErr == nil && rightErr == nil && !leftTime.Equal(rightTime) {
			return leftTime.After(rightTime)
		}
		if results[i].LastActivityAt != results[j].LastActivityAt {
			return results[i].LastActivityAt > results[j].LastActivityAt
		}
		return itemSortKey(results[i].Item) < itemSortKey(results[j].Item)
	})
}

func itemSortKey(item itemRef) string {
	return strings.Join([]string{
		item.Provider,
		item.PlatformHost,
		item.PlatformRepoID,
		item.RepoPath,
		item.Type,
		fmt.Sprintf("%08d", item.Number),
	}, "\x1f")
}

func sinceToRFC3339(raw string) string {
	if d, err := time.ParseDuration(raw); err == nil {
		return time.Now().UTC().Add(-d).Format(time.RFC3339)
	}
	return raw
}

func clampLimit(value int, def int, max int) int {
	if value <= 0 {
		return def
	}
	if value > max {
		return max
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

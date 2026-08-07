package mcpserver

import (
	"context"
	"fmt"
	"net/url"
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
	After  string          `json:"after,omitempty" jsonschema:"opaque daemon activity cursor"`
}

type activityRow struct {
	ID           string              `json:"id"`
	Cursor       string              `json:"cursor"`
	ActivityType string              `json:"activity_type"`
	Item         itemRef             `json:"item"`
	Workspace    *daemonWorkspaceRef `json:"workspace,omitempty"`
	Author       string              `json:"author"`
	ItemAuthor   string              `json:"item_author,omitempty"`
	CreatedAt    string              `json:"created_at"`
	BodyPreview  string              `json:"body_preview,omitempty"`
	ActivityURL  string              `json:"activity_url,omitempty"`
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

func wrapTool[In, Out any](
	fn func(context.Context, In) (Out, error),
) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := fn(ctx, in)
		return nil, out, err
	}
}

func (s *Server) listRepos(ctx context.Context, in listReposInput) (listReposOutput, error) {
	var rows []daemonRepoSummary
	if err := s.daemon.getJSON(ctx, "/api/v1/repos/summary", nil, &rows); err != nil {
		return listReposOutput{}, err
	}
	limit := in.Limit
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := listReposOutput{Repos: make([]repoRow, 0, len(rows))}
	for _, row := range rows {
		repo := row.Repo
		owner := firstNonEmpty(repo.Owner, row.Owner)
		name := firstNonEmpty(repo.Name, row.Name)
		host := firstNonEmpty(repo.PlatformHost, row.PlatformHost)
		out.Repos = append(out.Repos, repoRow{
			Provider:            repo.Provider,
			PlatformHost:        host,
			Owner:               owner,
			Name:                name,
			RepoPath:            repoPathOrFallback(repo, owner, name),
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
	repo, err := in.Repo.queryValue()
	if err != nil {
		return listActivityOutput{}, err
	}
	query := url.Values{}
	query.Set("since", sinceToRFC3339(firstNonEmpty(in.Since, "24h")))
	if repo != "" {
		query.Set("repo", repo)
	}
	for _, typ := range in.Types {
		query.Add("types", typ)
	}
	if in.Search != "" {
		query.Set("search", in.Search)
	}
	if in.After != "" {
		query.Set("after", in.After)
	}
	var resp daemonActivityResponse
	if err := s.daemon.getJSON(ctx, "/api/v1/activity", query, &resp); err != nil {
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
	repo, err := in.Repo.queryValue()
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
	repo string,
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
				WorkflowStatus: workflowStatusOrNew(row.KanbanStatus),
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
	repo string,
	limit int,
	offset int,
) ([]daemonPull, error) {
	query := url.Values{}
	query.Set("q", text)
	query.Set("state", state)
	if state == "merged" {
		query.Set("state", "all")
	}
	query.Set("limit", fmt.Sprintf("%d", limit))
	if offset > 0 {
		query.Set("offset", fmt.Sprintf("%d", offset))
	}
	if repo != "" {
		query.Set("repo", repo)
	}
	var rows []daemonPull
	if err := s.daemon.getJSON(ctx, "/api/v1/pulls", query, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *Server) searchIssues(
	ctx context.Context,
	text string,
	state string,
	repo string,
	limit int,
) ([]searchResult, bool, error) {
	query := url.Values{}
	query.Set("q", text)
	query.Set("state", state)
	query.Set("limit", fmt.Sprintf("%d", limit+1))
	if repo != "" {
		query.Set("repo", repo)
	}
	var rows []daemonIssue
	if err := s.daemon.getJSON(ctx, "/api/v1/issues", query, &rows); err != nil {
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

func (p daemonPull) itemRef() itemRef {
	repo := p.Repo
	owner := firstNonEmpty(repo.Owner, p.RepoOwner)
	name := firstNonEmpty(repo.Name, p.RepoName)
	return itemRef{
		Type:         "pr",
		Provider:     repo.Provider,
		PlatformHost: firstNonEmpty(repo.PlatformHost, p.PlatformHost),
		Owner:        owner,
		Name:         name,
		RepoPath:     repoPathOrFallback(repo, owner, name),
		Number:       p.Number,
		Title:        p.Title,
		URL:          p.URL,
		State:        p.State,
		Author:       p.Author,
		IsDraft:      p.IsDraft,
	}
}

func (i daemonIssue) itemRef() itemRef {
	repo := i.Repo
	owner := firstNonEmpty(repo.Owner, i.RepoOwner)
	name := firstNonEmpty(repo.Name, i.RepoName)
	return itemRef{
		Type:         "issue",
		Provider:     repo.Provider,
		PlatformHost: firstNonEmpty(repo.PlatformHost, i.PlatformHost),
		Owner:        owner,
		Name:         name,
		RepoPath:     repoPathOrFallback(repo, owner, name),
		Number:       i.Number,
		Title:        i.Title,
		URL:          i.URL,
		State:        i.State,
		Author:       i.Author,
	}
}

func (a daemonActivityItem) itemRef() itemRef {
	repo := a.Repo
	owner := firstNonEmpty(repo.Owner, a.RepoOwner)
	name := firstNonEmpty(repo.Name, a.RepoName)
	return itemRef{
		Type:         a.ItemType,
		Provider:     repo.Provider,
		PlatformHost: firstNonEmpty(repo.PlatformHost, a.PlatformHost),
		Owner:        owner,
		Name:         name,
		RepoPath:     repoPathOrFallback(repo, owner, name),
		Number:       a.ItemNumber,
		Title:        a.ItemTitle,
		URL:          a.ItemURL,
		State:        a.ItemState,
		Author:       a.ItemAuthor,
	}
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

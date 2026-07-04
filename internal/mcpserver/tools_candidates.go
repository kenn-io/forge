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

type findCandidatesInput struct {
	Since                 string          `json:"since,omitempty"`
	Repo                  repoFilterInput `json:"repo,omitempty"`
	ItemTypes             []string        `json:"item_types,omitempty"`
	WorkflowStates        []string        `json:"workflow_states,omitempty"`
	ExcludeWorkflowStates []string        `json:"exclude_workflow_states,omitempty"`
	IncludeDrafts         bool            `json:"include_drafts,omitempty"`
	IncludeClosed         bool            `json:"include_closed,omitempty"`
	Limit                 int             `json:"limit,omitempty"`
	ActivityTypes         []string        `json:"activity_types,omitempty"`
}

type candidate struct {
	Item      itemRef            `json:"item"`
	Workflow  candidateWorkflow  `json:"workflow"`
	Activity  candidateActivity  `json:"activity"`
	Workspace candidateWorkspace `json:"workspace"`
	Stack     candidateStack     `json:"stack"`
	Cache     candidateCache     `json:"cache"`
}

type candidateWorkflow struct {
	Status        string `json:"status"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	UpdatedSource string `json:"updated_source,omitempty"`
	UpdatedActor  string `json:"updated_actor,omitempty"`
	UpdatedReason string `json:"updated_reason,omitempty"`
}

type candidateActivity struct {
	LatestAt   string   `json:"latest_at"`
	EventCount int      `json:"event_count"`
	Types      []string `json:"types"`
	Actors     []string `json:"actors"`
	Reasons    []string `json:"reasons"`
}

type candidateWorkspace struct {
	Exists bool   `json:"exists"`
	ID     string `json:"id,omitempty"`
}

type candidateStack struct {
	Present  bool   `json:"present"`
	Position int    `json:"position,omitempty"`
	Size     int    `json:"size,omitempty"`
	Health   string `json:"health,omitempty"`
}

type candidateCache struct {
	DetailLoaded    bool   `json:"detail_loaded"`
	DetailFetchedAt string `json:"detail_fetched_at,omitempty"`
}

type findCandidatesOutput struct {
	Candidates []candidate `json:"candidates"`
	Capped     bool        `json:"capped"`
}

type candidateKey struct {
	provider     string
	platformHost string
	repoPath     string
	owner        string
	name         string
	itemType     string
	number       int
}

type candidateRepoKey struct {
	provider     string
	platformHost string
	repoPath     string
	owner        string
	name         string
}

type candidateGroup struct {
	key         candidateKey
	item        itemRef
	activity    candidateActivity
	typesSeen   map[string]bool
	actorsSeen  map[string]bool
	reasonsSeen map[string]bool
}

type daemonStackContext struct {
	Position int    `json:"position"`
	Size     int    `json:"size"`
	Health   string `json:"health"`
}

func (s *Server) registerCandidateTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "middleman_find_review_candidates",
		Description: "Find recently active cached PRs and issues that look worth reviewing, " +
			"with local workflow, workspace, stack, and cache context.",
	}, wrapTool(s.findReviewCandidates))
}

func (s *Server) findReviewCandidates(ctx context.Context, in findCandidatesInput) (findCandidatesOutput, error) {
	limit := clampLimit(in.Limit, 25, 100)
	repo, err := in.Repo.queryValue()
	if err != nil {
		return findCandidatesOutput{}, err
	}
	includePR, includeIssue, err := itemTypeSelection(in.ItemTypes)
	if err != nil {
		return findCandidatesOutput{}, err
	}
	includeWorkflow, err := workflowStateSet(in.WorkflowStates)
	if err != nil {
		return findCandidatesOutput{}, err
	}
	excludeWorkflow, err := workflowStateSet(in.ExcludeWorkflowStates)
	if err != nil {
		return findCandidatesOutput{}, err
	}

	query := url.Values{}
	query.Set("since", sinceToRFC3339(firstNonEmpty(in.Since, "24h")))
	if repo != "" {
		query.Set("repo", repo)
	}
	for _, typ := range in.ActivityTypes {
		query.Add("types", typ)
	}
	var resp daemonActivityResponse
	if err := s.daemon.getJSON(ctx, "/api/v1/activity", query, &resp); err != nil {
		return findCandidatesOutput{}, err
	}

	groups := map[candidateKey]*candidateGroup{}
	repos := map[candidateRepoKey]candidateRepoKey{}
	for _, row := range resp.Items {
		if row.ItemNumber == 0 {
			continue
		}
		if row.ItemType != "pr" && row.ItemType != "issue" {
			continue
		}
		if row.ItemType == "pr" && !includePR {
			continue
		}
		if row.ItemType == "issue" && !includeIssue {
			continue
		}
		item := row.itemRef()
		key := candidateKeyFromItem(item)
		group := groups[key]
		if group == nil {
			group = &candidateGroup{
				key:         key,
				item:        item,
				typesSeen:   map[string]bool{},
				actorsSeen:  map[string]bool{},
				reasonsSeen: map[string]bool{},
			}
			groups[key] = group
			repoKey := candidateRepoKey{
				provider:     key.provider,
				platformHost: key.platformHost,
				repoPath:     key.repoPath,
				owner:        key.owner,
				name:         key.name,
			}
			repos[repoKey] = repoKey
		}
		group.addActivity(row)
	}

	pulls, issues, err := s.fetchCandidateItems(ctx, repos)
	if err != nil {
		return findCandidatesOutput{}, err
	}

	candidates := make([]candidate, 0, len(groups))
	for _, group := range groups {
		cand, ok := s.buildCandidate(ctx, group, pulls, issues, in.IncludeClosed, in.IncludeDrafts)
		if !ok {
			continue
		}
		status := cand.Workflow.Status
		if len(includeWorkflow) > 0 && !includeWorkflow[status] {
			continue
		}
		if excludeWorkflow[status] {
			continue
		}
		candidates = append(candidates, cand)
	}
	sortCandidates(candidates)
	capped := resp.Capped || len(candidates) > limit
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return findCandidatesOutput{Candidates: candidates, Capped: capped}, nil
}

func (g *candidateGroup) addActivity(row daemonActivityItem) {
	if timeStringAfter(row.CreatedAt, g.activity.LatestAt) {
		g.activity.LatestAt = row.CreatedAt
	}
	g.activity.EventCount++
	if row.ActivityType != "" && !g.typesSeen[row.ActivityType] {
		g.typesSeen[row.ActivityType] = true
		g.activity.Types = append(g.activity.Types, row.ActivityType)
	}
	if row.Author != "" && !g.actorsSeen[row.Author] && len(g.activity.Actors) < 5 {
		g.actorsSeen[row.Author] = true
		g.activity.Actors = append(g.activity.Actors, row.Author)
	}
	reason := reasonForActivity(row.ActivityType, row.Author)
	if reason != "" && !g.reasonsSeen[reason] && len(g.activity.Reasons) < 5 {
		g.reasonsSeen[reason] = true
		g.activity.Reasons = append(g.activity.Reasons, reason)
	}
}

func (s *Server) fetchCandidateItems(
	ctx context.Context,
	repos map[candidateRepoKey]candidateRepoKey,
) (map[candidateKey]daemonPull, map[candidateKey]daemonIssue, error) {
	pulls := map[candidateKey]daemonPull{}
	issues := map[candidateKey]daemonIssue{}
	for _, repo := range repos {
		filter, err := repo.repoFilter().queryValue()
		if err != nil {
			return nil, nil, err
		}
		query := url.Values{}
		query.Set("repo", filter)
		query.Set("state", "all")
		query.Set("limit", "200")
		var pullRows []daemonPull
		if err := s.daemon.getJSON(ctx, "/api/v1/pulls", query, &pullRows); err != nil {
			return nil, nil, err
		}
		for _, row := range pullRows {
			pulls[candidateKeyFromItem(row.itemRef())] = row
		}
		var issueRows []daemonIssue
		if err := s.daemon.getJSON(ctx, "/api/v1/issues", query, &issueRows); err != nil {
			return nil, nil, err
		}
		for _, row := range issueRows {
			issues[candidateKeyFromItem(row.itemRef())] = row
		}
	}
	return pulls, issues, nil
}

func (s *Server) buildCandidate(
	ctx context.Context,
	group *candidateGroup,
	pulls map[candidateKey]daemonPull,
	issues map[candidateKey]daemonIssue,
	includeClosed bool,
	includeDrafts bool,
) (candidate, bool) {
	switch group.key.itemType {
	case "pr":
		row, ok := pulls[group.key]
		if !ok {
			return candidate{}, false
		}
		if !includeClosed && row.State != "open" {
			return candidate{}, false
		}
		if !includeDrafts && row.IsDraft {
			return candidate{}, false
		}
		item := row.itemRef()
		workspace := workspaceFromRef(row.Workspace)
		return candidate{
			Item:      item,
			Workflow:  candidateWorkflow{Status: workflowStatusOrNew(row.KanbanStatus)},
			Activity:  group.activity,
			Workspace: workspace,
			Stack:     s.stackForCandidate(ctx, item),
			Cache: candidateCache{
				DetailLoaded:    row.DetailLoaded,
				DetailFetchedAt: row.DetailFetchedAt,
			},
		}, true
	case "issue":
		row, ok := issues[group.key]
		if !ok {
			return candidate{}, false
		}
		if !includeClosed && row.State == "closed" {
			return candidate{}, false
		}
		item := row.itemRef()
		return candidate{
			Item:      item,
			Workflow:  candidateWorkflow{Status: workflowStatusOrNew(row.WorkflowStatus)},
			Activity:  group.activity,
			Workspace: workspaceFromRef(row.Workspace),
			Stack:     candidateStack{},
			Cache: candidateCache{
				DetailLoaded:    row.DetailLoaded,
				DetailFetchedAt: row.DetailFetchedAt,
			},
		}, true
	default:
		return candidate{}, false
	}
}

func (s *Server) stackForCandidate(ctx context.Context, item itemRef) candidateStack {
	var stack daemonStackContext
	if err := s.daemon.getJSON(ctx, stackPath(item), nil, &stack); err != nil {
		return candidateStack{}
	}
	return candidateStack{
		Present:  true,
		Position: stack.Position,
		Size:     stack.Size,
		Health:   stack.Health,
	}
}

func stackPath(item itemRef) string {
	return fmt.Sprintf(
		"/api/v1/host/%s/pulls/%s/%s/%s/%d/stack",
		seg(item.PlatformHost),
		seg(item.Provider),
		seg(item.Owner),
		seg(item.Name),
		item.Number,
	)
}

func (k candidateRepoKey) repoFilter() repoFilterInput {
	return repoFilterInput{
		Provider:     k.provider,
		PlatformHost: k.platformHost,
		RepoPath:     k.repoPath,
		Owner:        k.owner,
		Name:         k.name,
	}
}

func candidateKeyFromItem(item itemRef) candidateKey {
	return candidateKey{
		provider:     item.Provider,
		platformHost: item.PlatformHost,
		repoPath:     item.RepoPath,
		owner:        item.Owner,
		name:         item.Name,
		itemType:     item.Type,
		number:       item.Number,
	}
}

func workflowStateSet(values []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch value {
		case "new", "reviewing", "waiting", "awaiting_merge":
			out[value] = true
		default:
			return nil, fmt.Errorf("workflow states must contain only new, reviewing, waiting, or awaiting_merge")
		}
	}
	return out, nil
}

func workspaceFromRef(ref *daemonWorkspaceRef) candidateWorkspace {
	if ref == nil || ref.ID == "" {
		return candidateWorkspace{}
	}
	return candidateWorkspace{Exists: true, ID: ref.ID}
}

func reasonForActivity(activityType, author string) string {
	action := activityType
	switch activityType {
	case "comment":
		action = "commented"
	case "commit":
		action = "pushed commits"
	case "review":
		action = "reviewed"
	case "force_push":
		action = "force pushed"
	case "pr_opened", "issue_opened", "new", "new_pr", "new_issue":
		action = "opened"
	}
	if author == "" {
		return action
	}
	if action == activityType {
		return author + ": " + activityType
	}
	return author + " " + action
}

func sortCandidates(candidates []candidate) {
	sort.Slice(candidates, func(i, j int) bool {
		left := candidates[i].Activity.LatestAt
		right := candidates[j].Activity.LatestAt
		if timeStringAfter(left, right) {
			return true
		}
		if timeStringAfter(right, left) {
			return false
		}
		return itemSortKey(candidates[i].Item) < itemSortKey(candidates[j].Item)
	})
}

func timeStringAfter(left, right string) bool {
	if left == "" {
		return false
	}
	if right == "" {
		return true
	}
	leftTime, leftErr := time.Parse(time.RFC3339, left)
	rightTime, rightErr := time.Parse(time.RFC3339, right)
	if leftErr == nil && rightErr == nil {
		return leftTime.After(rightTime)
	}
	return left > right
}

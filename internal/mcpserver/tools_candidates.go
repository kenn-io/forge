package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.kenn.io/forge/platform"
)

type findCandidatesInput struct {
	Since         string          `json:"since,omitempty"`
	Repo          repoFilterInput `json:"repo,omitzero"`
	ItemTypes     []string        `json:"item_types,omitempty"`
	IncludeDrafts bool            `json:"include_drafts,omitempty"`
	IncludeClosed bool            `json:"include_closed,omitempty"`
	Limit         int             `json:"limit,omitempty"`
	ActivityTypes []string        `json:"activity_types,omitempty"`
}

type candidate struct {
	Item      itemRef            `json:"item"`
	Activity  candidateActivity  `json:"activity"`
	Workspace candidateWorkspace `json:"workspace"`
	Stack     candidateStack     `json:"stack"`
	Cache     candidateCache     `json:"cache"`
	// key identifies and orders the item; it is not part of the tool output.
	key candidateKey
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
	Hints      []resultHint `json:"hints,omitempty"`
	Candidates []candidate  `json:"candidates"`
	Capped     bool         `json:"capped"`
}

type candidateExclusions struct {
	draft  bool
	closed bool
}

type candidateKey struct {
	provider     string
	platformHost string
	repoKey      platform.RepositoryKey
	repoPath     string
	owner        string
	name         string
	itemType     string
	number       int
}

type candidateRepoKey struct {
	provider     string
	platformHost string
	repoKey      platform.RepositoryKey
	repoPath     string
	owner        string
	name         string
}

type candidateGroup struct {
	key         candidateKey
	activity    candidateActivity
	typesSeen   map[string]bool
	actorsSeen  map[string]bool
	reasonsSeen map[string]bool
}

func (s *Server) registerCandidateTools() {
	addTool(s.mcp, &mcp.Tool{
		Name:        "kenn_forge_find_review_candidates",
		Description: "Find recently active PRs and issues worth reviewing, with workspace and stack state.",
	}, wrapTool(s.findReviewCandidates))
}

func (s *Server) findReviewCandidates(ctx context.Context, in findCandidatesInput) (findCandidatesOutput, error) {
	limit, hints := clampLimitHint("limit", in.Limit, 25, 100)
	repo, err := s.repositoryFilter(ctx, in.Repo, "repo.")
	if err != nil {
		return findCandidatesOutput{}, err
	}
	includePR, includeIssue, err := itemTypeSelection(in.ItemTypes)
	if err != nil {
		return findCandidatesOutput{}, err
	}

	resp, err := s.backend.ListActivity(ctx, ActivityQuery{
		Since:      sinceToRFC3339(firstNonEmpty(in.Since, "24h")),
		Repository: repo, ActivityTypes: slices.Clone(in.ActivityTypes),
		ItemTypes: slices.Clone(in.ItemTypes),
	})
	if err != nil {
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
		key := row.itemKey()
		group := groups[key]
		if group == nil {
			group = &candidateGroup{
				key:         key,
				typesSeen:   map[string]bool{},
				actorsSeen:  map[string]bool{},
				reasonsSeen: map[string]bool{},
			}
			groups[key] = group
			repoKey := candidateRepoKey{
				provider:     key.provider,
				platformHost: key.platformHost,
				repoKey:      key.repoKey,
				repoPath:     key.repoPath,
				owner:        key.owner,
				name:         key.name,
			}
			repos[repoKey] = repoKey
		}
		group.addActivity(row)
	}

	pulls, issues, err := s.fetchCandidateItems(ctx, repos, candidateKeys(groups))
	if err != nil {
		return findCandidatesOutput{}, err
	}

	candidates := make([]candidate, 0, len(groups))
	capped := resp.Capped
	drafts, closed := 0, 0
	for _, group := range sortedCandidateGroups(groups) {
		cand, ok, excluded := s.buildCandidate(group, pulls, issues, in.IncludeClosed, in.IncludeDrafts)
		if excluded.draft {
			drafts++
		}
		if excluded.closed {
			closed++
		}
		if !ok {
			continue
		}
		candidates = append(candidates, cand)
		if len(candidates) > limit {
			capped = true
			break
		}
	}
	sortCandidates(candidates)
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for i := range candidates {
		if err := s.enrichCandidateStack(ctx, &candidates[i]); err != nil {
			return findCandidatesOutput{}, err
		}
	}
	out := findCandidatesOutput{Candidates: candidates, Capped: capped}
	if len(candidates) == 0 {
		qualifier := ""
		if resp.Capped {
			qualifier = "At least "
		}
		retry := map[string]any{}
		if drafts > 0 {
			retry["include_drafts"] = true
		}
		if closed > 0 {
			retry["include_closed"] = true
		}
		if drafts > 0 {
			out.Hints = append(out.Hints, resultHint{Code: "excluded_drafts", Message: fmt.Sprintf("%s%d draft PRs were left out", qualifier, drafts), RetryWith: retry})
		}
		if closed > 0 {
			out.Hints = append(out.Hints, resultHint{Code: "excluded_closed", Message: fmt.Sprintf("%s%d closed or merged items were left out", qualifier, closed), RetryWith: retry})
		}
	}
	out.Hints = append(out.Hints, hints...)
	return out, nil
}

func (g *candidateGroup) addActivity(row ActivityItem) {
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
	needed map[candidateKey]bool,
) (map[candidateKey]Pull, map[candidateKey]Issue, error) {
	pulls := map[candidateKey]Pull{}
	issues := map[candidateKey]Issue{}
	for _, repo := range repos {
		filter := repo.repositoryIdentity()
		repoPulls, repoIssues := neededForRepo(needed, repo)
		if len(repoPulls) > 0 {
			if err := s.fetchCandidatePullPages(ctx, filter, repoPulls, pulls); err != nil {
				return nil, nil, err
			}
		}
		if len(repoIssues) > 0 {
			if err := s.fetchCandidateIssuePages(ctx, filter, repoIssues, issues); err != nil {
				return nil, nil, err
			}
		}
	}
	return pulls, issues, nil
}

func (s *Server) fetchCandidatePullPages(
	ctx context.Context,
	filter RepositoryIdentity,
	numbers []int64,
	out map[candidateKey]Pull,
) error {
	const pageSize = 200
	for chunk := range slices.Chunk(numbers, pageSize) {
		rows, err := s.backend.ListPulls(ctx, ItemListQuery{
			Repository: filter, State: "all", Limit: pageSize, Numbers: chunk,
		})
		if err != nil {
			return err
		}
		for _, row := range rows {
			key := row.itemKey()
			out[key] = row
		}
	}
	return nil
}

func (s *Server) fetchCandidateIssuePages(
	ctx context.Context,
	filter RepositoryIdentity,
	numbers []int64,
	out map[candidateKey]Issue,
) error {
	const pageSize = 200
	for chunk := range slices.Chunk(numbers, pageSize) {
		issueRows, err := s.backend.ListIssues(ctx, ItemListQuery{
			Repository: filter, State: "all", Limit: pageSize, Numbers: chunk,
		})
		if err != nil {
			return err
		}
		for _, row := range issueRows {
			key := row.itemKey()
			out[key] = row
		}
	}
	return nil
}

func candidateKeys(groups map[candidateKey]*candidateGroup) map[candidateKey]bool {
	needed := make(map[candidateKey]bool, len(groups))
	for key := range groups {
		needed[key] = true
	}
	return needed
}

func neededForRepo(
	needed map[candidateKey]bool,
	repo candidateRepoKey,
) ([]int64, []int64) {
	var pulls, issues []int64
	for key := range needed {
		if key.provider != repo.provider ||
			key.platformHost != repo.platformHost ||
			key.repoKey != repo.repoKey ||
			key.repoPath != repo.repoPath ||
			key.owner != repo.owner ||
			key.name != repo.name {
			continue
		}
		switch key.itemType {
		case "pr":
			pulls = append(pulls, int64(key.number))
		case "issue":
			issues = append(issues, int64(key.number))
		}
	}
	return pulls, issues
}

func (s *Server) buildCandidate(
	group *candidateGroup,
	pulls map[candidateKey]Pull,
	issues map[candidateKey]Issue,
	includeClosed bool,
	includeDrafts bool,
) (candidate, bool, candidateExclusions) {
	switch group.key.itemType {
	case "pr":
		row, ok := pulls[group.key]
		if !ok {
			return candidate{}, false, candidateExclusions{}
		}
		excluded := candidateExclusions{draft: !includeDrafts && row.IsDraft, closed: !includeClosed && row.State != "open"}
		if excluded.draft || excluded.closed {
			return candidate{}, false, excluded
		}
		workspace := workspaceFromRef(row.Workspace)
		return candidate{
			Item:      row.itemRef(),
			key:       group.key,
			Activity:  group.activity,
			Workspace: workspace,
			Cache: candidateCache{
				DetailLoaded:    row.DetailLoaded,
				DetailFetchedAt: row.DetailFetchedAt,
			},
		}, true, candidateExclusions{}
	case "issue":
		row, ok := issues[group.key]
		if !ok {
			return candidate{}, false, candidateExclusions{}
		}
		if !includeClosed && row.State == "closed" {
			return candidate{}, false, candidateExclusions{closed: true}
		}
		return candidate{
			Item:      row.itemRef(),
			key:       group.key,
			Activity:  group.activity,
			Workspace: workspaceFromRef(row.Workspace),
			Stack:     candidateStack{},
			Cache: candidateCache{
				DetailLoaded:    row.DetailLoaded,
				DetailFetchedAt: row.DetailFetchedAt,
			},
		}, true, candidateExclusions{}
	default:
		return candidate{}, false, candidateExclusions{}
	}
}

func (s *Server) enrichCandidateStack(ctx context.Context, cand *candidate) error {
	if cand == nil || cand.Item.Type != "pr" {
		return nil
	}
	stack, err := s.stackForCandidate(ctx, cand.key.itemIdentity())
	if err != nil {
		return err
	}
	cand.Stack = stack
	return nil
}

func (s *Server) stackForCandidate(ctx context.Context, item ItemIdentity) (candidateStack, error) {
	stack, err := s.backend.GetPullStack(ctx, item)
	if err != nil {
		if backendErr, ok := errors.AsType[*Error](err); ok && isStackAbsentError(backendErr) {
			return candidateStack{}, nil
		}
		return candidateStack{}, err
	}
	return candidateStack{
		Present:  true,
		Position: stack.Position,
		Size:     stack.Size,
		Health:   stack.Health,
	}, nil
}

func (k candidateRepoKey) repositoryIdentity() RepositoryIdentity {
	return RepositoryIdentity{
		Provider: k.provider, PlatformHost: k.platformHost,
		Key: k.repoKey, RepoPath: k.repoPath,
		Owner: k.owner, Name: k.name,
	}
}

func workspaceFromRef(ref *WorkspaceRef) candidateWorkspace {
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
		return candidates[i].key.sortKey() < candidates[j].key.sortKey()
	})
}

func sortedCandidateGroups(groups map[candidateKey]*candidateGroup) []*candidateGroup {
	out := make([]*candidateGroup, 0, len(groups))
	for _, group := range groups {
		out = append(out, group)
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i].activity.LatestAt
		right := out[j].activity.LatestAt
		if timeStringAfter(left, right) {
			return true
		}
		if timeStringAfter(right, left) {
			return false
		}
		return out[i].key.sortKey() < out[j].key.sortKey()
	})
	return out
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

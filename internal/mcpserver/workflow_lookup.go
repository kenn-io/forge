package mcpserver

import (
	"context"
	"maps"
	"net/url"
	"sort"
)

type workflowLookupGroup struct {
	repo     candidateRepoKey
	itemType string
}

func (s *Server) workflowStatesForKeys(
	ctx context.Context,
	keys map[candidateKey]bool,
) (map[candidateKey]candidateWorkflow, error) {
	groups := map[workflowLookupGroup]map[candidateKey]bool{}
	for key := range keys {
		if key.itemType != "pr" && key.itemType != "issue" {
			continue
		}
		group := workflowLookupGroup{
			repo: candidateRepoKey{
				provider:     key.provider,
				platformHost: key.platformHost,
				repoPath:     key.repoPath,
				owner:        key.owner,
				name:         key.name,
			},
			itemType: key.itemType,
		}
		if groups[group] == nil {
			groups[group] = map[candidateKey]bool{}
		}
		groups[group][key] = true
	}

	out := map[candidateKey]candidateWorkflow{}
	for _, group := range sortedWorkflowLookupGroups(groups) {
		filter, err := group.repo.repoFilter().queryValue()
		if err != nil {
			return nil, err
		}
		remaining := copyCandidateKeySet(groups[group])
		cursor := ""
		for {
			query := workflowLookupQuery(filter, group.itemType, cursor)
			var resp daemonWorkflowStateResponse
			if err := s.getWorkflowStateJSON(ctx, query, &resp); err != nil {
				return nil, err
			}
			for _, row := range resp.Items {
				key := workflowRowKey(row, group)
				if !remaining[key] {
					continue
				}
				workflow := row.Workflow
				workflow.Status = workflowStatusOrNew(workflow.Status)
				out[key] = workflow
				delete(remaining, key)
			}
			if len(remaining) == 0 || resp.NextCursor == "" {
				break
			}
			cursor = resp.NextCursor
		}
	}
	return out, nil
}

func (s *Server) getWorkflowStateJSON(ctx context.Context, query url.Values, out any) error {
	if err := s.daemon.ensureWorkflowStateSupported(ctx); err != nil {
		return err
	}
	return s.daemon.getJSON(ctx, "/api/v1/workflow-state", query, out)
}

func workflowLookupQuery(repo string, itemType string, cursor string) url.Values {
	query := url.Values{}
	query.Set("repo", repo)
	query.Add("item_type", itemType)
	query.Set("include_closed", "true")
	query.Set("limit", "200")
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	return query
}

func workflowRowKey(row daemonWorkflowStateItem, group workflowLookupGroup) candidateKey {
	provider := firstNonEmpty(row.Provider, group.repo.provider)
	platformHost := firstNonEmpty(row.PlatformHost, group.repo.platformHost)
	repoPath := firstNonEmpty(row.RepoPath, group.repo.repoPath)
	owner := firstNonEmpty(row.Owner, group.repo.owner)
	name := firstNonEmpty(row.Name, group.repo.name)
	itemType := firstNonEmpty(row.ItemType, group.itemType)
	return candidateKey{
		provider:     provider,
		platformHost: platformHost,
		repoPath:     repoPath,
		owner:        owner,
		name:         name,
		itemType:     itemType,
		number:       row.Number,
	}
}

func workflowForCandidate(
	key candidateKey,
	workflows map[candidateKey]candidateWorkflow,
	fallbackStatus string,
) candidateWorkflow {
	if workflow, ok := workflows[key]; ok {
		workflow.Status = workflowStatusOrNew(workflow.Status)
		return workflow
	}
	return candidateWorkflow{Status: workflowStatusOrNew(fallbackStatus)}
}

func copyCandidateKeySet(in map[candidateKey]bool) map[candidateKey]bool {
	out := make(map[candidateKey]bool, len(in))
	maps.Copy(out, in)
	return out
}

func sortedWorkflowLookupGroups(groups map[workflowLookupGroup]map[candidateKey]bool) []workflowLookupGroup {
	out := make([]workflowLookupGroup, 0, len(groups))
	for group := range groups {
		out = append(out, group)
	}
	sort.Slice(out, func(i, j int) bool {
		left := workflowLookupGroupSortKey(out[i])
		right := workflowLookupGroupSortKey(out[j])
		return left < right
	})
	return out
}

func workflowLookupGroupSortKey(group workflowLookupGroup) string {
	return group.repo.provider + "\x00" +
		group.repo.platformHost + "\x00" +
		group.repo.repoPath + "\x00" +
		group.repo.owner + "\x00" +
		group.repo.name + "\x00" +
		group.itemType
}

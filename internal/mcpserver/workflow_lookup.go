package mcpserver

import (
	"cmp"
	"context"
	"slices"
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
				repoKey:      key.repoKey,
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
		filter := group.repo.repositoryIdentity()
		numbers := make([]int64, 0, len(groups[group]))
		for key := range groups[group] {
			numbers = append(numbers, int64(key.number))
		}
		for chunk := range slices.Chunk(numbers, 200) {
			query := WorkflowQuery{
				Repository: filter, ItemTypes: []string{group.itemType}, IncludeClosed: true,
				Numbers: chunk, Limit: 200,
			}
			resp, err := s.backend.ListWorkflowStates(ctx, query)
			if err != nil {
				return nil, err
			}
			for _, row := range resp.Items {
				key := workflowRowKey(row, group)
				if !groups[group][key] {
					continue
				}
				out[key] = candidateWorkflowFromState(row.Workflow)
			}
		}
	}
	return out, nil
}

func workflowRowKey(row WorkflowItem, group workflowLookupGroup) candidateKey {
	provider := firstNonEmpty(row.Identity.Provider, row.Repository.Provider, group.repo.provider)
	platformHost := firstNonEmpty(row.Identity.PlatformHost, row.Repository.PlatformHost, group.repo.platformHost)
	repoKey := cmp.Or(row.Identity.RepoKey, row.Repository.Key, group.repo.repoKey)
	repoPath := firstNonEmpty(row.Repository.RepoPath, group.repo.repoPath)
	owner := firstNonEmpty(row.Identity.Owner, row.Repository.Owner, group.repo.owner)
	name := firstNonEmpty(row.Identity.Name, row.Repository.Name, group.repo.name)
	itemType := firstNonEmpty(row.Identity.Type, group.itemType)
	return candidateKey{
		provider: provider, platformHost: platformHost, repoKey: repoKey,
		repoPath: repoPath, owner: owner, name: name,
		itemType: itemType, number: row.Identity.Number,
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
		group.repo.repoKey.String() + "\x00" +
		group.repo.repoPath + "\x00" +
		group.repo.owner + "\x00" +
		group.repo.name + "\x00" +
		group.itemType
}

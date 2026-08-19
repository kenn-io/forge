package mcpserver

import (
	"context"
	"maps"
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
				provider:       key.provider,
				platformHost:   key.platformHost,
				platformRepoID: key.platformRepoID,
				repoPath:       key.repoPath,
				owner:          key.owner,
				name:           key.name,
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
		remaining := copyCandidateKeySet(groups[group])
		cursor := ""
		for {
			query := workflowLookupQuery(filter, group.itemType, cursor)
			resp, err := s.backend.ListWorkflowStates(ctx, query)
			if err != nil {
				return nil, err
			}
			for _, row := range resp.Items {
				key := workflowRowKey(row, group)
				if !remaining[key] {
					continue
				}
				out[key] = candidateWorkflowFromState(row.Workflow)
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

func workflowLookupQuery(repo RepositoryIdentity, itemType string, cursor string) WorkflowQuery {
	return WorkflowQuery{
		Repository: repo, ItemTypes: []string{itemType}, IncludeClosed: true,
		Limit: 200, Cursor: cursor,
	}
}

func workflowRowKey(row WorkflowItem, group workflowLookupGroup) candidateKey {
	provider := firstNonEmpty(row.Identity.Provider, row.Repository.Provider, group.repo.provider)
	platformHost := firstNonEmpty(row.Identity.PlatformHost, row.Repository.PlatformHost, group.repo.platformHost)
	platformRepoID := firstNonEmpty(
		row.Identity.PlatformRepoID, row.Repository.PlatformRepoID, group.repo.platformRepoID,
	)
	repoPath := firstNonEmpty(row.Repository.RepoPath, group.repo.repoPath)
	owner := firstNonEmpty(row.Identity.Owner, row.Repository.Owner, group.repo.owner)
	name := firstNonEmpty(row.Identity.Name, row.Repository.Name, group.repo.name)
	itemType := firstNonEmpty(row.Identity.Type, group.itemType)
	return candidateKey{
		provider: provider, platformHost: platformHost, platformRepoID: platformRepoID,
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
		group.repo.platformRepoID + "\x00" +
		group.repo.repoPath + "\x00" +
		group.repo.owner + "\x00" +
		group.repo.name + "\x00" +
		group.itemType
}

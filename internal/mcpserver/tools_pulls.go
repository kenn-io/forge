package mcpserver

import (
	"context"
	"fmt"
)

type listPullContextsInput struct {
	Label         string          `json:"label,omitempty" jsonschema:"only PRs with this exact case-sensitive label name; applied before pagination"`
	Repo          repoFilterInput `json:"repo" jsonschema:"repository identity from kenn_forge_list_repos; required"`
	Limit         int             `json:"limit,omitempty" jsonschema:"PRs per page; default 25, maximum 100"`
	Offset        int             `json:"offset,omitempty" jsonschema:"start at zero, then use next_offset with the same repository"`
	IncludeEvents bool            `json:"include_events,omitempty" jsonschema:"include cached review and activity excerpts plus full stack health; default false"`
	EventLimit    int             `json:"event_limit,omitempty" jsonschema:"events per PR when include_events is true; default 5, maximum 100"`
	IncludeBody   bool            `json:"include_body,omitempty" jsonschema:"include PR descriptions; default false"`
}

type listPullContextsOutput struct {
	Items      []getItemContextOutput `json:"items"`
	NextOffset *int                   `json:"next_offset,omitempty"`
}

func (s *Server) listPullContexts(ctx context.Context, in listPullContextsInput) (listPullContextsOutput, error) {
	repo, err := in.Repo.repositoryIdentity()
	if err != nil {
		return listPullContextsOutput{}, err
	}
	if repo.Provider == "" {
		return listPullContextsOutput{}, fmt.Errorf("repo is required")
	}
	if in.Offset < 0 {
		return listPullContextsOutput{}, fmt.Errorf("offset must not be negative")
	}
	limit := clampLimit(in.Limit, 25, 100)
	pulls, err := s.backend.ListPulls(ctx, ItemListQuery{
		Repository: repo, State: "open", Label: in.Label, Limit: limit + 1, Offset: in.Offset,
	})
	if err != nil {
		return listPullContextsOutput{}, err
	}
	out := listPullContextsOutput{Items: make([]getItemContextOutput, 0, min(len(pulls), limit))}
	if len(pulls) > limit {
		out.NextOffset = new(in.Offset + limit)
		pulls = pulls[:limit]
	}
	for _, pull := range pulls {
		detail := PullDetail{
			Pull: &pull, Checks: pull.Checks, Stack: pull.Stack, Workspace: pull.Workspace,
			DetailLoaded: pull.DetailLoaded, DetailFetchedAt: pull.DetailFetchedAt,
		}
		if in.IncludeEvents {
			detail, err = s.backend.GetPull(ctx, itemIdentityFromRef(pull.itemRef()))
			if err != nil {
				return listPullContextsOutput{}, fmt.Errorf("PR %d context: %w", pull.Number, err)
			}
			if detail.Pull == nil {
				return listPullContextsOutput{}, fmt.Errorf("PR %d detail missing pull", pull.Number)
			}
		}
		item := pullContext(detail, getItemContextInput{
			IncludeEvents: new(in.IncludeEvents), EventLimit: clampLimit(in.EventLimit, 5, 100),
		})
		if !in.IncludeBody {
			item.Body = ""
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

package mcpserver

import (
	"context"
	"errors"
	"fmt"
)

type listPullContextsInput struct {
	Label         string          `json:"label,omitempty" jsonschema:"only PRs with this exact case-sensitive label name; applied before pagination"`
	Repo          repoFilterInput `json:"repo,omitzero" jsonschema:"repository identity from kenn_forge_list_repos; required unless merge_queue is true"`
	MergeQueue    bool            `json:"merge_queue,omitempty" jsonschema:"only PRs in a merge queue, or removed from one and not queued again; applied before pagination"`
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
	repo, err := s.repositoryFilter(ctx, in.Repo, "repo.")
	if err != nil {
		return listPullContextsOutput{}, err
	}
	if repo.Provider == "" && !in.MergeQueue {
		return listPullContextsOutput{}, FieldError("repo", "repo is required unless merge_queue is true")
	}
	if in.Offset < 0 {
		return listPullContextsOutput{}, errors.New("offset must not be negative")
	}
	limit := clampLimit(in.Limit, 25, 100)
	pulls, err := s.backend.ListPulls(ctx, ItemListQuery{
		Repository: repo, State: "open", Label: in.Label, MergeQueue: in.MergeQueue, Limit: limit + 1, Offset: in.Offset,
	})
	if err != nil {
		return listPullContextsOutput{}, err
	}
	out := listPullContextsOutput{Items: make([]getItemContextOutput, 0, min(len(pulls), limit))}
	if len(pulls) > limit {
		out.NextOffset = new(in.Offset + limit)
		pulls = pulls[:limit]
	}
	var details []PullDetail
	if in.IncludeEvents {
		items := make([]ItemIdentity, 0, len(pulls))
		for _, pull := range pulls {
			items = append(items, pull.itemKey().itemIdentity())
		}
		details, err = s.backend.GetPulls(ctx, items)
		if err != nil {
			return listPullContextsOutput{}, err
		}
		if len(details) != len(pulls) {
			return listPullContextsOutput{}, fmt.Errorf("PR contexts: got %d details for %d pulls", len(details), len(pulls))
		}
	}
	for i, pull := range pulls {
		detail := PullDetail{
			Pull: &pull, Checks: pull.Checks, Stack: pull.Stack, Workspace: pull.Workspace,
			DetailLoaded: pull.DetailLoaded, DetailFetchedAt: pull.DetailFetchedAt,
		}
		if i < len(details) {
			detail = details[i]
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

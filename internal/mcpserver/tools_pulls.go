package mcpserver

import (
	"context"
	"errors"
	"fmt"
)

type listPullContextsInput struct {
	Label         string          `json:"label,omitempty" jsonschema:"exact label"`
	Repo          repoFilterInput `json:"repo,omitzero" jsonschema:"required unless merge_queue"`
	MergeQueue    bool            `json:"merge_queue,omitempty" jsonschema:"only PRs in, or removed from, a merge queue"`
	Limit         int             `json:"limit,omitempty" jsonschema:"default 25, max 100"`
	Offset        int             `json:"offset,omitempty" jsonschema:"next_offset from the previous page"`
	IncludeEvents bool            `json:"include_events,omitempty" jsonschema:"review excerpts and stack health"`
	EventLimit    int             `json:"event_limit,omitempty" jsonschema:"default 5, max 100"`
	IncludeBody   bool            `json:"include_body,omitempty"`
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
	for _, pull := range pulls {
		detail := PullDetail{
			Pull: &pull, Checks: pull.Checks, Stack: pull.Stack, Workspace: pull.Workspace,
			DetailLoaded: pull.DetailLoaded, DetailFetchedAt: pull.DetailFetchedAt,
		}
		if in.IncludeEvents {
			detail, err = s.backend.GetPull(ctx, pull.itemKey().itemIdentity())
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

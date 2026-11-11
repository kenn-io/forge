package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

type listPullContextsInput struct {
	Label         string          `json:"label,omitempty" jsonschema:"exact label"`
	Repo          repoFilterInput `json:"repo,omitzero" jsonschema:"required unless merge_queue"`
	MergeQueue    bool            `json:"merge_queue,omitempty" jsonschema:"only PRs in, or removed from, a merge queue"`
	Roborev       string          `json:"roborev,omitempty" jsonschema:"findings or clean: only PRs whose newest roborev comment on the current head reports findings, or reports none"`
	Limit         int             `json:"limit,omitempty" jsonschema:"default 25, max 100"`
	Offset        int             `json:"offset,omitempty" jsonschema:"next_offset from the previous page"`
	IncludeEvents bool            `json:"include_events,omitempty" jsonschema:"review excerpts and stack health"`
	EventLimit    int             `json:"event_limit,omitempty" jsonschema:"default 5, max 100"`
	IncludeChecks bool            `json:"include_checks,omitempty" jsonschema:"include every check; default omits completed success, neutral, and skipped checks"`
	IncludeBody   bool            `json:"include_body,omitempty"`
}

type listPullContextsOutput struct {
	Hints      []resultHint           `json:"hints,omitempty"`
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
	if in.Roborev != "" && in.Roborev != "findings" && in.Roborev != "clean" {
		return listPullContextsOutput{}, FieldError("roborev", "roborev must be findings or clean")
	}
	if in.Offset < 0 {
		return listPullContextsOutput{}, errors.New("offset must not be negative")
	}
	limit, hints := clampLimitHint("limit", in.Limit, 25, 100)
	pulls, err := s.backend.ListPulls(ctx, ItemListQuery{
		Repository: repo, State: "open", Label: in.Label, MergeQueue: in.MergeQueue, Roborev: in.Roborev, Limit: limit + 1, Offset: in.Offset,
	})
	if err != nil {
		return listPullContextsOutput{}, err
	}
	out := listPullContextsOutput{Items: make([]getItemContextOutput, 0, min(len(pulls), limit)), Hints: hints}
	if len(pulls) > limit {
		out.NextOffset = new(in.Offset + limit)
		pulls = pulls[:limit]
	}
	var details []PullDetail
	var eventLimit int
	if in.IncludeEvents {
		eventLimit, hints = clampLimitHint("event_limit", in.EventLimit, 5, 100)
		out.Hints = append(out.Hints, hints...)
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
			IncludeEvents: new(in.IncludeEvents), EventLimit: eventLimit,
		})
		if !in.IncludeChecks {
			item.Checks = slices.DeleteFunc(item.Checks, func(check contextCheck) bool {
				return check.Status == "completed" && (check.Conclusion == "success" || check.Conclusion == "neutral" || check.Conclusion == "skipped")
			})
		}
		if !in.IncludeBody {
			item.Body = ""
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

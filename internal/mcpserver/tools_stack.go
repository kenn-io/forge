package mcpserver

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type getStackContextInput struct {
	Item itemRefInput `json:"item"`
}

type stackMemberOut struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	State          string `json:"state"`
	IsDraft        bool   `json:"is_draft"`
	WorkflowStatus string `json:"workflow_status"`
	IsRequested    bool   `json:"is_requested"`
	Position       int    `json:"position"`
}

type getStackContextOutput struct {
	Present bool             `json:"present"`
	Health  string           `json:"health,omitempty"`
	Members []stackMemberOut `json:"members,omitempty"`
}

func (s *Server) registerStackTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "kenn_forge_get_stack_context",
		Description: "Return ordered stack context for a PR, including local workflow status for each member.",
	}, wrapTool(s.getStackContext))
}

func (s *Server) getStackContext(
	ctx context.Context,
	in getStackContextInput,
) (getStackContextOutput, error) {
	if err := validateItemRef(in.Item); err != nil {
		return getStackContextOutput{}, err
	}
	if in.Item.Type != "pr" {
		return getStackContextOutput{}, &Error{
			Kind:    "invalid_request",
			Message: "stack context is only available for prs",
		}
	}

	stack, err := s.backend.GetPullStack(ctx, itemIdentity(in.Item))
	if err != nil {
		var backendErr *Error
		if errors.As(err, &backendErr) && isStackAbsentError(backendErr) {
			return getStackContextOutput{Present: false}, nil
		}
		return getStackContextOutput{}, err
	}

	sort.Slice(stack.Members, func(i, j int) bool {
		if stack.Members[i].Position != stack.Members[j].Position {
			return stack.Members[i].Position < stack.Members[j].Position
		}
		return stack.Members[i].Number < stack.Members[j].Number
	})
	wanted := make(map[int]bool, len(stack.Members))
	for _, member := range stack.Members {
		wanted[member.Number] = true
	}
	statuses, err := s.stackWorkflowStatuses(ctx, in.Item, wanted)
	if err != nil {
		return getStackContextOutput{}, err
	}

	out := getStackContextOutput{
		Present: true,
		Health:  stack.Health,
		Members: make([]stackMemberOut, 0, len(stack.Members)),
	}
	for _, member := range stack.Members {
		out.Members = append(out.Members, stackMemberOut{
			Number:         member.Number,
			Title:          member.Title,
			State:          member.State,
			IsDraft:        member.IsDraft,
			WorkflowStatus: workflowStatusOrNew(statuses[member.Number]),
			IsRequested:    member.Number == in.Item.Number,
			Position:       member.Position,
		})
	}
	return out, nil
}

func isStackAbsentError(err *Error) bool {
	if err == nil || err.Kind != "not_found" {
		return false
	}
	if err.Code != "notFound" && err.Code != "not_found" {
		return false
	}
	message := strings.ToLower(err.Message)
	return strings.Contains(message, "not part of a stack") || strings.Contains(message, "not stacked")
}

func (s *Server) stackWorkflowStatuses(
	ctx context.Context,
	ref itemRefInput,
	wanted map[int]bool,
) (map[int]string, error) {
	filter, err := (repoFilterInput{
		Provider: ref.Provider, PlatformHost: ref.PlatformHost,
		PlatformRepoID: ref.PlatformRepoID,
		Owner:          ref.Owner, Name: ref.Name,
	}).repositoryIdentity()
	if err != nil {
		return nil, err
	}
	query := WorkflowQuery{
		Repository: filter, ItemTypes: []string{"pr"}, IncludeClosed: true, Limit: 200,
	}
	statuses := map[int]string{}
	for {
		resp, err := s.backend.ListWorkflowStates(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, item := range resp.Items {
			statuses[item.Identity.Number] = workflowStatusOrNew(item.Workflow.Status)
			delete(wanted, item.Identity.Number)
		}
		if len(wanted) == 0 || resp.NextCursor == "" {
			return statuses, nil
		}
		query.Cursor = resp.NextCursor
	}
}

package mcpserver

import (
	"context"
	"errors"
	"net/url"
	"sort"

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

type daemonFullStackContext struct {
	Health  string              `json:"health"`
	Members []daemonStackMember `json:"members"`
}

type daemonStackMember struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Position int    `json:"position"`
	IsDraft  bool   `json:"is_draft"`
}

func (s *Server) registerStackTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "middleman_get_stack_context",
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
		return getStackContextOutput{}, &daemonError{
			Kind:    "invalid_request",
			Message: "stack context is only available for prs",
		}
	}

	var stack daemonFullStackContext
	if err := s.daemon.getJSON(ctx, itemPath("pulls", in.Item)+"/stack", nil, &stack); err != nil {
		var derr *daemonError
		if errors.As(err, &derr) && derr.Kind == "not_found" {
			return getStackContextOutput{Present: false}, nil
		}
		return getStackContextOutput{}, err
	}

	statuses, err := s.stackWorkflowStatuses(ctx, in.Item)
	if err != nil {
		return getStackContextOutput{}, err
	}
	sort.Slice(stack.Members, func(i, j int) bool {
		if stack.Members[i].Position != stack.Members[j].Position {
			return stack.Members[i].Position < stack.Members[j].Position
		}
		return stack.Members[i].Number < stack.Members[j].Number
	})

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

func (s *Server) stackWorkflowStatuses(
	ctx context.Context,
	ref itemRefInput,
) (map[int]string, error) {
	filter, err := (repoFilterInput{
		Provider:     ref.Provider,
		PlatformHost: ref.PlatformHost,
		Owner:        ref.Owner,
		Name:         ref.Name,
	}).queryValue()
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("repo", filter)
	query.Add("item_type", "pr")
	query.Set("include_closed", "true")
	query.Set("limit", "200")
	var resp daemonWorkflowStateResponse
	if err := s.daemon.getJSON(ctx, "/api/v1/workflow-state", query, &resp); err != nil {
		return nil, err
	}
	statuses := make(map[int]string, len(resp.Items))
	for _, item := range resp.Items {
		statuses[item.Number] = workflowStatusOrNew(item.Workflow.Status)
	}
	return statuses, nil
}

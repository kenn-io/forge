package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type setWorkflowInput struct {
	Item           itemRefInput `json:"item"`
	Status         string       `json:"status"`
	ExpectedStatus string       `json:"expected_status,omitempty"`
	Force          bool         `json:"force,omitempty"`
	Reason         string       `json:"reason,omitempty"`
	Actor          string       `json:"actor,omitempty"`
}

type setWorkflowOutput struct {
	PreviousStatus string `json:"previous_status"`
	Status         string `json:"status"`
	UpdatedAt      string `json:"updated_at"`
	UpdatedSource  string `json:"updated_source"`
	UpdatedActor   string `json:"updated_actor,omitempty"`
	UpdatedReason  string `json:"updated_reason,omitempty"`
}

func (s *Server) registerWorkflowTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_set_item_workflow_state",
		Description: "Set kenn-forge-local workflow state for one cached PR or issue. " +
			"This is the only MCP write tool and never calls provider APIs.",
	}, wrapTool(s.setItemWorkflowState))
}

func (s *Server) setItemWorkflowState(
	ctx context.Context,
	in setWorkflowInput,
) (setWorkflowOutput, error) {
	if err := validateItemRef(in.Item); err != nil {
		return setWorkflowOutput{}, err
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		return setWorkflowOutput{}, fmt.Errorf("status is required")
	}
	if _, err := workflowStateSet([]string{status}); err != nil {
		return setWorkflowOutput{}, err
	}
	expected := strings.TrimSpace(in.ExpectedStatus)
	if expected != "" {
		if _, err := workflowStateSet([]string{expected}); err != nil {
			return setWorkflowOutput{}, fmt.Errorf("expected_status: %w", err)
		}
	}
	if expected == "" && !in.Force {
		return setWorkflowOutput{}, &Error{
			Kind:    "invalid_request",
			Message: "expected_status is required unless force is true",
		}
	}
	if expected != "" && in.Force {
		return setWorkflowOutput{}, &Error{
			Kind:    "invalid_request",
			Message: "force cannot be true when expected_status is provided",
		}
	}
	mutation, err := s.backend.SetWorkflowState(ctx, itemIdentity(in.Item), WorkflowUpdate{
		Status: status, ExpectedStatus: expected, Force: in.Force, Source: "mcp",
		Actor: in.Actor, Reason: in.Reason,
	})
	if err != nil {
		return setWorkflowOutput{}, err
	}
	return setWorkflowOutput{
		PreviousStatus: mutation.PreviousStatus,
		Status:         mutation.State.Status,
		UpdatedAt:      mutation.State.UpdatedAt,
		UpdatedSource:  mutation.State.UpdatedSource,
		UpdatedActor:   mutation.State.UpdatedActor,
		UpdatedReason:  mutation.State.UpdatedReason,
	}, nil
}

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
		Name: "middleman_set_item_workflow_state",
		Description: "Set middleman-local workflow state for one cached PR or issue. " +
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
	if err := s.daemon.ensureWorkflowStateSupported(ctx); err != nil {
		return setWorkflowOutput{}, err
	}

	body := map[string]string{
		"status": status,
		"source": "mcp",
	}
	if expected != "" {
		body["expected_status"] = expected
	}
	if in.Actor != "" {
		body["actor"] = in.Actor
	}
	if in.Reason != "" {
		body["reason"] = in.Reason
	}

	var out setWorkflowOutput
	if err := s.daemon.putJSON(ctx, workflowPath(in.Item), body, &out); err != nil {
		return setWorkflowOutput{}, err
	}
	return out, nil
}

func workflowPath(ref itemRefInput) string {
	if ref.PlatformHost != "" {
		return fmt.Sprintf(
			"/api/v1/host/%s/workflow-state/%s/%s/%s/%s/%d",
			seg(ref.PlatformHost),
			seg(ref.Type),
			seg(ref.Provider),
			seg(ref.Owner),
			seg(ref.Name),
			ref.Number,
		)
	}
	return fmt.Sprintf(
		"/api/v1/workflow-state/%s/%s/%s/%s/%d",
		seg(ref.Type),
		seg(ref.Provider),
		seg(ref.Owner),
		seg(ref.Name),
		ref.Number,
	)
}

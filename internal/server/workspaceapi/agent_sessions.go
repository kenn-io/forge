package workspaceapi

import (
	"context"
	"slices"
	"strings"
	"time"

	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.kenn.io/kit/agenthook"
)

type listWorkspaceAgentSessionsInput struct {
	ID string `path:"id"`
}

type agentInitialMessageStatusResponse struct {
	Agent        string     `json:"agent"`
	SessionID    string     `json:"session_id"`
	State        string     `json:"state"`
	MessageBytes int        `json:"message_bytes"`
	ReservedAt   time.Time  `json:"reserved_at"`
	DeliveredAt  *time.Time `json:"delivered_at,omitempty"`
}

type workspaceAgentSessionResponse struct {
	Agent             string                             `json:"agent"`
	SessionID         string                             `json:"session_id"`
	RuntimeSessionKey string                             `json:"runtime_session_key"`
	TargetKey         string                             `json:"target_key"`
	State             agentactivity.State                `json:"state"`
	UpdatedAt         time.Time                          `json:"updated_at"`
	InitialMessage    *agentInitialMessageStatusResponse `json:"initial_message,omitempty"`
}

type listWorkspaceAgentSessionsOutput struct {
	Body struct {
		Sessions []workspaceAgentSessionResponse `json:"sessions"`
	}
}

func (s *Handler) listWorkspaceAgentSessions(
	ctx context.Context,
	input *listWorkspaceAgentSessionsInput,
) (*listWorkspaceAgentSessionsOutput, error) {
	if s.workspaces == nil || s.runtime == nil || s.agentActivity == nil || s.db == nil {
		return nil, httpapi.ServiceUnavailable("workspace agent sessions not configured")
	}

	summary, err := s.workspaces.GetSummary(ctx, input.ID)
	if err != nil {
		return nil, httpapi.Internal("get workspace failed")
	}
	if summary == nil {
		return nil, httpapi.NotFound(
			httpapi.CodeWorkspaceNotFound, "workspace not found", nil,
		)
	}

	liveByKey := make(map[string]localruntime.SessionInfo)
	for _, session := range s.runtime.ListSessions(summary.ID) {
		if session.Kind != localruntime.LaunchTargetAgent ||
			(session.Status != localruntime.SessionStatusStarting &&
				session.Status != localruntime.SessionStatusRunning) {
			continue
		}
		liveByKey[session.Key] = session
	}
	liveKeys := make([]string, 0, len(liveByKey))
	for key := range liveByKey {
		liveKeys = append(liveKeys, key)
	}

	output := &listWorkspaceAgentSessionsOutput{}
	output.Body.Sessions = make([]workspaceAgentSessionResponse, 0)
	for _, report := range s.agentActivity.LiveReportsForWorkspace(
		summary.WorktreePath, liveKeys,
	) {
		agent, parseErr := agenthook.ParseAgent(report.Agent)
		if parseErr != nil {
			continue
		}
		live, ok := liveByKey[report.RuntimeSessionKey]
		if !ok {
			continue
		}
		response := workspaceAgentSessionResponse{
			Agent:             string(agent),
			SessionID:         report.SessionID,
			RuntimeSessionKey: report.RuntimeSessionKey,
			TargetKey:         live.TargetKey,
			State:             report.State,
			UpdatedAt:         report.UpdatedAt.UTC(),
		}
		if attempt, found := s.initialMessageAttempt(
			summary.ID, report.RuntimeSessionKey,
		); found && attempt.Agent == string(agent) && attempt.SessionID == report.SessionID {
			response.InitialMessage = initialMessageStatusResponse(attempt)
		}
		output.Body.Sessions = append(output.Body.Sessions, response)
	}

	slices.SortFunc(output.Body.Sessions, func(a, b workspaceAgentSessionResponse) int {
		if order := b.UpdatedAt.Compare(a.UpdatedAt); order != 0 {
			return order
		}
		if order := strings.Compare(a.Agent, b.Agent); order != 0 {
			return order
		}
		if order := strings.Compare(a.SessionID, b.SessionID); order != 0 {
			return order
		}
		return strings.Compare(a.RuntimeSessionKey, b.RuntimeSessionKey)
	})
	return output, nil
}

func initialMessageStatusResponse(attempt initialMessageAttempt) *agentInitialMessageStatusResponse {
	response := &agentInitialMessageStatusResponse{
		Agent:        attempt.Agent,
		SessionID:    attempt.SessionID,
		State:        attempt.State,
		MessageBytes: len(attempt.Message),
		ReservedAt:   attempt.ReservedAt.UTC(),
	}
	if attempt.DeliveredAt != nil {
		deliveredAt := attempt.DeliveredAt.UTC()
		response.DeliveredAt = &deliveredAt
	}
	return response
}

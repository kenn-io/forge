package workspaceapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

type listWorkspaceACPSessionsInput struct {
	ID        string `path:"id"`
	TargetKey string `query:"target_key" required:"true" doc:"ACP agent target whose sessions to list."`
}

// WorkspaceACPSessionResponse is one session an ACP agent keeps for the
// workspace's worktree.
type WorkspaceACPSessionResponse struct {
	SessionID string     `json:"session_id" doc:"The agent's session ID; pass it as acp_session_id to resume the session in a new chat."`
	Title     string     `json:"title,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty" doc:"When the agent last updated the session, if it says."`
}

// WorkspaceACPSessionsResponse lists the sessions a chat can resume.
type WorkspaceACPSessionsResponse struct {
	Sessions []WorkspaceACPSessionResponse `json:"sessions"`
}

type listWorkspaceACPSessionsOutput struct {
	Body WorkspaceACPSessionsResponse
}

// listWorkspaceACPSessions lists the sessions an ACP agent keeps for the
// workspace's worktree, from any client, that no chat of the workspace holds.
func (s *Handler) listWorkspaceACPSessions(
	ctx context.Context,
	input *listWorkspaceACPSessionsInput,
) (*listWorkspaceACPSessionsOutput, error) {
	summary, err := s.getReadyRuntimeWorkspace(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	targetKey := strings.TrimSpace(input.TargetKey)
	if s.runtime == nil {
		return nil, httpapi.ServiceUnavailable("workspace runtime not configured")
	}
	sessions, err := s.runtime.ListACPSessions(ctx, summary.ID, summary.WorktreePath, targetKey)
	if err != nil {
		return nil, acpSessionsError(err)
	}
	output := &listWorkspaceACPSessionsOutput{}
	output.Body.Sessions = make([]WorkspaceACPSessionResponse, 0, len(sessions))
	for _, session := range sessions {
		response := WorkspaceACPSessionResponse{SessionID: session.SessionID, Title: session.Title}
		if !session.UpdatedAt.IsZero() {
			updated := session.UpdatedAt.UTC()
			response.UpdatedAt = &updated
		}
		output.Body.Sessions = append(output.Body.Sessions, response)
	}
	return output, nil
}

func acpSessionsError(err error) error {
	if errors.Is(err, localruntime.ErrACPCannotListSessions) {
		return httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}
	return workspaceRuntimeLaunchError(err)
}

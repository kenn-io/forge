package workspaceapi

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

const (
	defaultAgentHandoffTimeout      = 5 * time.Minute
	defaultAgentHandoffPollInterval = 500 * time.Millisecond
)

// WorkspaceAgentHandoffRequest launches one agent runtime in an existing
// workspace and delivers one initial message to it. Quick actions from the
// pull request and issue headers use this after ordinary workspace creation.
type WorkspaceAgentHandoffRequest struct {
	WorkspaceID string
	TargetKey   string
	Message     string
}

// WorkspaceAgentHandoffResult is the launched runtime plus the initial message
// delivery status.
type WorkspaceAgentHandoffResult struct {
	Session        localruntime.SessionInfo
	InitialMessage InitialMessageResult
}

type launchWorkspaceAgentHandoffInput struct {
	ID   string `path:"id"`
	Body struct {
		TargetKey string `json:"target_key"`
		Message   string `json:"message"`
	}
}

type workspaceAgentHandoffResponse struct {
	Session        localruntime.SessionInfo          `json:"session"`
	InitialMessage agentInitialMessageStatusResponse `json:"initial_message"`
}

type workspaceAgentHandoffOutput struct {
	Body workspaceAgentHandoffResponse
}

func (s *Handler) launchWorkspaceAgentHandoff(
	ctx context.Context,
	input *launchWorkspaceAgentHandoffInput,
) (*workspaceAgentHandoffOutput, error) {
	result, err := s.LaunchWorkspaceAgentHandoffService(ctx, WorkspaceAgentHandoffRequest{
		WorkspaceID: input.ID, TargetKey: input.Body.TargetKey, Message: input.Body.Message,
	})
	if err != nil {
		return nil, err
	}
	return &workspaceAgentHandoffOutput{Body: workspaceAgentHandoffResponse{
		Session:        result.Session,
		InitialMessage: *initialMessageResultResponse(result.InitialMessage),
	}}, nil
}

// LaunchWorkspaceAgentHandoffService waits for the workspace to become ready,
// launches the agent target, and delivers the message as the runtime's initial
// prompt. The work runs against the handler lifecycle rather than the request:
// a client that navigates away mid-handoff must not strand a launched agent
// without its prompt. Validation happens up front so a bad request fails
// before any waiting starts.
func (s *Handler) LaunchWorkspaceAgentHandoffService(
	ctx context.Context, req WorkspaceAgentHandoffRequest,
) (WorkspaceAgentHandoffResult, error) {
	if s.workspaces == nil || s.runtime == nil {
		return WorkspaceAgentHandoffResult{}, httpapi.ServiceUnavailable("workspace runtime not configured")
	}
	targetKey := strings.ToLower(strings.TrimSpace(req.TargetKey))
	if targetKey == "" {
		return WorkspaceAgentHandoffResult{}, httpapi.Validation("body.target_key", "target_key is required")
	}
	if !workspaceRuntimeTargetIsAgent(s.runtime, targetKey) {
		return WorkspaceAgentHandoffResult{}, httpapi.Validation(
			"body.target_key", "target_key is not an available agent launch target",
		)
	}
	message, _, err := normalizeInitialAgentMessage(req.Message)
	if err != nil {
		return WorkspaceAgentHandoffResult{}, httpapi.Validation("body.message", err.Error())
	}
	if _, err := s.getRuntimeWorkspace(ctx, req.WorkspaceID); err != nil {
		return WorkspaceAgentHandoffResult{}, err
	}

	handoffCtx, cancel := context.WithTimeout(s.agentHandoffParentContext(), s.agentHandoffTimeoutOrDefault())
	defer cancel()

	if err := s.waitForWorkspaceReady(handoffCtx, req.WorkspaceID); err != nil {
		return WorkspaceAgentHandoffResult{}, err
	}
	session, err := s.launchWorkspaceRuntimeService(handoffCtx, req.WorkspaceID, targetKey, "workflow")
	if err != nil {
		return WorkspaceAgentHandoffResult{}, err
	}
	status, err := s.deliverInitialMessage(handoffCtx, InitialMessageRequest{
		WorkspaceID: req.WorkspaceID, RuntimeSessionKey: session.Key,
		TargetKey: targetKey, Message: message,
	})
	if err != nil {
		slog.Warn("agent handoff initial message failed",
			"workspace_id", req.WorkspaceID, "session_key", session.Key,
			"target_key", targetKey, "err", err)
		return WorkspaceAgentHandoffResult{Session: session, InitialMessage: status}, err
	}
	return WorkspaceAgentHandoffResult{Session: session, InitialMessage: status}, nil
}

func (s *Handler) waitForWorkspaceReady(ctx context.Context, workspaceID string) error {
	for {
		summary, err := s.getRuntimeWorkspace(ctx, workspaceID)
		if err != nil {
			return err
		}
		switch summary.Status {
		case "ready":
			return nil
		case "error":
			detail := "workspace setup failed"
			if summary.ErrorMessage != nil && strings.TrimSpace(*summary.ErrorMessage) != "" {
				detail += ": " + strings.TrimSpace(*summary.ErrorMessage)
			}
			return httpapi.Conflict(httpapi.CodeConflict, detail, nil)
		}
		if err := s.waitAgentHandoffPoll(ctx, "workspace to become ready"); err != nil {
			return err
		}
	}
}

// deliverInitialMessage retries only the typed "input mode not ready" signal:
// the agent has not yet enabled bracketed paste, so nothing was written and a
// later attempt on the same runtime is safe. Every other error is final.
func (s *Handler) deliverInitialMessage(
	ctx context.Context, req InitialMessageRequest,
) (InitialMessageResult, error) {
	for {
		status, err := s.SubmitInitialMessageService(ctx, req)
		if !errors.Is(err, ErrInitialMessageInputModeNotReady) {
			return status, err
		}
		if err := s.waitAgentHandoffPoll(ctx, "agent input to become ready"); err != nil {
			return status, err
		}
	}
}

func (s *Handler) waitAgentHandoffPoll(ctx context.Context, waitingFor string) error {
	interval := s.agentHandoffPollInterval
	if interval <= 0 {
		interval = defaultAgentHandoffPollInterval
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return httpapi.ServiceUnavailable("agent handoff timed out waiting for " + waitingFor)
		}
		return httpapi.ServiceUnavailable("agent handoff canceled while waiting for " + waitingFor)
	}
}

func (s *Handler) agentHandoffParentContext() context.Context {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.lifecycleCtx != nil {
		return s.lifecycleCtx
	}
	return context.Background()
}

func (s *Handler) agentHandoffTimeoutOrDefault() time.Duration {
	if s.agentHandoffTimeout > 0 {
		return s.agentHandoffTimeout
	}
	return defaultAgentHandoffTimeout
}

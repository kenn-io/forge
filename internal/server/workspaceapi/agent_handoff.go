package workspaceapi

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
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
// prompt. The work runs against the handoff lifecycle rather than the request:
// a client that navigates away mid-handoff must not strand a launched agent
// without its prompt, while daemon shutdown cancels every waiting handoff
// before the HTTP drain (CancelAgentHandoffs). Validation happens up front so
// a bad request fails before any waiting starts.
//
// Once the agent is launched, a delivery failure is reported as a problem that
// carries the live session key and the last known message state, so callers
// can tell "the agent is running without its prompt" apart from "nothing
// started". The session is left running: it may already hold the prompt when
// delivery is uncertain, and stopping it would destroy the evidence.
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

	handoffCtx, cancel := context.WithTimeout(s.agentHandoffContext(), s.agentHandoffTimeoutOrDefault())
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
			"target_key", targetKey, "state", status.State, "err", err)
		return WorkspaceAgentHandoffResult{Session: session, InitialMessage: status},
			postLaunchHandoffProblem(err, session, status)
	}
	return WorkspaceAgentHandoffResult{Session: session, InitialMessage: status}, nil
}

// postLaunchHandoffProblem annotates a delivery failure with the runtime that
// is now live so the client can report a launched-but-promptless agent
// instead of a failed start.
func postLaunchHandoffProblem(
	err error, session localruntime.SessionInfo, status InitialMessageResult,
) error {
	var problem *httpapi.ProblemError
	if !errors.As(err, &problem) {
		problem = httpapi.NewProblem(
			http.StatusInternalServerError, httpapi.CodeInternalError, "submit initial message failed", nil,
		)
	}
	annotated := *problem
	annotated.Details = maps.Clone(problem.Details)
	if annotated.Details == nil {
		annotated.Details = make(map[string]any, 3)
	}
	state := status.State
	if state == "" {
		state = "not_delivered"
	}
	annotated.Details["session_key"] = session.Key
	annotated.Details["target_key"] = session.TargetKey
	annotated.Details["initial_message_state"] = state
	return &annotated
}

// CancelAgentHandoffs aborts every waiting agent handoff. The server calls it
// as the first step of shutdown: a handoff deliberately outlives its HTTP
// request, so without this the HTTP drain would wait on handoffs that only
// the later handler shutdown could cancel.
func (s *Handler) CancelAgentHandoffs() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	cancel := s.agentHandoffCancel
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
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
		return httpapi.ServiceUnavailable("agent handoff canceled by shutdown while waiting for " + waitingFor)
	}
}

func (s *Handler) agentHandoffContext() context.Context {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.agentHandoffCtx == nil {
		parent := s.lifecycleCtx
		if parent == nil {
			parent = context.Background()
		}
		s.agentHandoffCtx, s.agentHandoffCancel = context.WithCancel(parent)
	}
	return s.agentHandoffCtx
}

func (s *Handler) agentHandoffTimeoutOrDefault() time.Duration {
	if s.agentHandoffTimeout > 0 {
		return s.agentHandoffTimeout
	}
	return defaultAgentHandoffTimeout
}

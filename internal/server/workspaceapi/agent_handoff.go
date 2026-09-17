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
	"go.kenn.io/forge/internal/workspace/agenthandoff"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

const defaultAgentHandoffTimeout = 5 * time.Minute

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
		// A launch that failed because the handoff context ended is a
		// timeout or shutdown, not an internal error.
		if cause := context.Cause(handoffCtx); cause != nil {
			return WorkspaceAgentHandoffResult{}, handoffWaitError(cause, "agent to launch")
		}
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
// the later handler shutdown could cancel. When no handoff has run yet the
// shared context is created already canceled, so a request that arrives
// after this call cannot start a fresh wait.
func (s *Handler) CancelAgentHandoffs() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.agentHandoffContextLocked()
	s.agentHandoffCancel()
}

// handoffWaitError maps the shared polling errors onto this endpoint's
// problem contract; problems raised by the workspace lookup pass through.
func handoffWaitError(err error, waitingFor string) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return httpapi.ServiceUnavailable("agent handoff timed out waiting for " + waitingFor)
	case errors.Is(err, context.Canceled):
		return httpapi.ServiceUnavailable("agent handoff canceled by shutdown while waiting for " + waitingFor)
	case errors.Is(err, agenthandoff.ErrWorkspaceSetupFailed):
		return httpapi.Conflict(httpapi.CodeConflict, strings.ReplaceAll(err.Error(), "\n", ": "), nil)
	}
	return err
}

func (s *Handler) waitForWorkspaceReady(ctx context.Context, workspaceID string) error {
	err := s.agentHandoffPoller().WaitForWorkspace(ctx, func(ctx context.Context) (agenthandoff.WorkspaceState, error) {
		summary, err := s.getRuntimeWorkspace(ctx, workspaceID)
		if err != nil {
			// A lookup that failed because the handoff context ended is the
			// wait's outcome, not an internal error.
			if cause := context.Cause(ctx); cause != nil {
				return agenthandoff.WorkspaceState{}, cause
			}
			return agenthandoff.WorkspaceState{}, err
		}
		state := agenthandoff.WorkspaceState{Status: summary.Status}
		if summary.ErrorMessage != nil {
			state.ErrorMessage = *summary.ErrorMessage
		}
		return state, nil
	})
	if err != nil {
		return handoffWaitError(err, "workspace to become ready")
	}
	return nil
}

// deliverInitialMessage retries only the typed "input mode not ready" signal:
// the agent has not yet enabled bracketed paste, so nothing was written and a
// later attempt on the same runtime is safe. Every other error is final.
func (s *Handler) deliverInitialMessage(
	ctx context.Context, req InitialMessageRequest,
) (InitialMessageResult, error) {
	status, err := agenthandoff.Deliver(ctx, s.agentHandoffPoller(),
		func(ctx context.Context) (InitialMessageResult, error) {
			return s.SubmitInitialMessageService(ctx, req)
		},
		func(err error) bool { return errors.Is(err, ErrInitialMessageInputModeNotReady) },
	)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			err = cause
		}
		return status, handoffWaitError(err, "agent input to become ready")
	}
	return status, nil
}

func (s *Handler) agentHandoffPoller() agenthandoff.Poller {
	return agenthandoff.Poller{Interval: s.agentHandoffPollInterval}
}

func (s *Handler) agentHandoffContext() context.Context {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.agentHandoffContextLocked()
}

func (s *Handler) agentHandoffContextLocked() context.Context {
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

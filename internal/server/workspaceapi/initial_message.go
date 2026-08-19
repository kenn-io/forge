package workspaceapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.kenn.io/kit/agenthook"
)

const maxInitialAgentMessageBytes = 64 << 10

const (
	initialMessagePending   = "pending"
	initialMessageDelivered = "delivered"
	initialMessageUncertain = "uncertain"
)

var ErrInitialMessageInputModeNotReady = errors.New("agent terminal input mode is not ready")

type initialMessageKey struct {
	workspaceID       string
	runtimeSessionKey string
}

// initialMessageAttempt is deliberately process-local. The normalized prompt
// is retained only so retries against this daemon can be compared without
// exposing prompt content through the API. Daemon restart clears all attempts.
type initialMessageAttempt struct {
	Agent       string
	SessionID   string
	Message     string
	State       string
	ReservedAt  time.Time
	DeliveredAt *time.Time
}

type initialMessagePathInput struct {
	ID         string `path:"id"`
	SessionKey string `path:"session_key"`
}

type submitInitialMessageInput struct {
	ID         string `path:"id"`
	SessionKey string `path:"session_key"`
	Body       struct {
		Agent     string `json:"agent"`
		SessionID string `json:"session_id"`
		Message   string `json:"message"`
	}
}

type initialMessageOutput struct {
	Body agentInitialMessageStatusResponse
}

func normalizeInitialAgentMessage(message string) (string, int, error) {
	if !utf8.ValidString(message) {
		return "", 0, errors.New("message must be valid UTF-8")
	}
	message = strings.ReplaceAll(message, "\r\n", "\n")
	message = strings.ReplaceAll(message, "\r", "\n")
	if strings.TrimSpace(message) == "" {
		return "", 0, errors.New("message must not be blank")
	}
	for _, value := range message {
		if value != '\n' && !unicode.IsPrint(value) {
			return "", 0, fmt.Errorf("message contains unsafe control character U+%04X", value)
		}
	}
	messageBytes := len(message)
	if messageBytes > maxInitialAgentMessageBytes {
		return "", 0, errors.New("message must not exceed 64 KiB after line-ending normalization")
	}
	return message, messageBytes, nil
}

func (s *Handler) getInitialMessageStatus(
	ctx context.Context,
	input *initialMessagePathInput,
) (*initialMessageOutput, error) {
	result, err := s.GetInitialMessageService(ctx, input.ID, input.SessionKey)
	if err != nil {
		return nil, err
	}
	return &initialMessageOutput{Body: *initialMessageResultResponse(result)}, nil
}

func (s *Handler) submitInitialMessage(
	ctx context.Context,
	input *submitInitialMessageInput,
) (*initialMessageOutput, error) {
	result, err := s.SubmitInitialMessageService(ctx, InitialMessageRequest{
		WorkspaceID: input.ID, RuntimeSessionKey: input.SessionKey,
		Agent: input.Body.Agent, SessionID: input.Body.SessionID,
		Message: input.Body.Message,
	})
	if errors.Is(err, ErrInitialMessageInputModeNotReady) {
		return nil, httpapi.Validation("body.message", err.Error())
	}
	if err != nil {
		return nil, err
	}
	return &initialMessageOutput{Body: *initialMessageResultResponse(result)}, nil
}

func (s *Handler) GetInitialMessageService(
	_ context.Context, workspaceID, runtimeSessionKey string,
) (InitialMessageResult, error) {
	attempt, ok := s.initialMessageAttempt(workspaceID, runtimeSessionKey)
	if !ok {
		return InitialMessageResult{}, httpapi.NotFound(
			httpapi.CodeNotFound, "initial message status not found", nil,
		)
	}
	return initialMessageAttemptResult(attempt), nil
}

func (s *Handler) SubmitInitialMessageService(
	ctx context.Context, req InitialMessageRequest,
) (InitialMessageResult, error) {
	if s.workspaces == nil || s.runtime == nil || s.agentActivity == nil {
		return InitialMessageResult{}, httpapi.ServiceUnavailable("initial message delivery not configured")
	}
	agent, err := agenthook.ParseAgent(req.Agent)
	if err != nil {
		return InitialMessageResult{}, httpapi.Validation("body.agent", err.Error())
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return InitialMessageResult{}, httpapi.Validation("body.session_id", "session_id is required")
	}
	message, _, err := normalizeInitialAgentMessage(req.Message)
	if err != nil {
		return InitialMessageResult{}, httpapi.Validation("body.message", err.Error())
	}
	proposed := initialMessageAttempt{Agent: string(agent), SessionID: sessionID, Message: message}
	if existing, ok := s.initialMessageAttempt(req.WorkspaceID, req.RuntimeSessionKey); ok {
		return existingInitialMessageAttemptResult(existing, proposed)
	}
	summary, err := s.workspaces.GetSummary(ctx, req.WorkspaceID)
	if err != nil {
		return InitialMessageResult{}, httpapi.Internal("get workspace failed")
	}
	if summary == nil {
		return InitialMessageResult{}, httpapi.NotFound(
			httpapi.CodeWorkspaceNotFound, "workspace not found", nil,
		)
	}
	live := false
	for _, runtimeSession := range s.runtime.ListSessions(req.WorkspaceID) {
		if runtimeSession.Key == req.RuntimeSessionKey &&
			runtimeSession.Kind == localruntime.LaunchTargetAgent &&
			(runtimeSession.Status == localruntime.SessionStatusStarting ||
				runtimeSession.Status == localruntime.SessionStatusRunning) {
			live = true
			break
		}
	}
	if !live {
		return InitialMessageResult{}, httpapi.Conflict(
			httpapi.CodeConflict, "agent runtime session is not live", nil,
		)
	}
	matchedReport := false
	for _, report := range s.agentActivity.LiveReportsForWorkspace(
		summary.WorktreePath, []string{req.RuntimeSessionKey},
	) {
		if report.Agent == string(agent) && report.SessionID == sessionID {
			matchedReport = true
			break
		}
	}
	if !matchedReport {
		return InitialMessageResult{}, httpapi.Conflict(
			httpapi.CodeConflict, "coding session does not match the live agent runtime", nil,
		)
	}
	attempt, reserved := s.reserveInitialMessageAttempt(req.WorkspaceID, req.RuntimeSessionKey, proposed)
	if !reserved {
		return existingInitialMessageAttemptResult(attempt, proposed)
	}
	if err := s.runtime.SubmitInitialMessage(ctx, req.WorkspaceID, req.RuntimeSessionKey, message); err != nil {
		return s.handleInitialMessageSubmitError(
			req.WorkspaceID, req.RuntimeSessionKey, proposed, err,
		)
	}
	delivered := s.finishInitialMessageAttempt(
		req.WorkspaceID, req.RuntimeSessionKey, initialMessageDelivered,
	)
	return initialMessageAttemptResult(delivered), nil
}

func (s *Handler) handleInitialMessageSubmitError(
	workspaceID string,
	runtimeSessionKey string,
	proposed initialMessageAttempt,
	err error,
) (InitialMessageResult, error) {
	if errors.Is(err, localruntime.ErrBracketedPasteInactive) {
		s.releaseInitialMessageAttempt(workspaceID, runtimeSessionKey, proposed)
		return InitialMessageResult{}, ErrInitialMessageInputModeNotReady
	}
	if errors.Is(err, localruntime.ErrInitialMessageNotWritten) {
		s.releaseInitialMessageAttempt(workspaceID, runtimeSessionKey, proposed)
		return InitialMessageResult{}, httpapi.Conflict(
			httpapi.CodeConflict, err.Error(), nil,
		)
	}
	uncertain := s.finishInitialMessageAttempt(
		workspaceID, runtimeSessionKey, initialMessageUncertain,
	)
	return initialMessageAttemptResult(uncertain), httpapi.Internal("submit initial message failed")
}

func existingInitialMessageAttemptResult(
	existing initialMessageAttempt,
	proposed initialMessageAttempt,
) (InitialMessageResult, error) {
	if existing.Agent != proposed.Agent ||
		existing.SessionID != proposed.SessionID ||
		existing.Message != proposed.Message {
		return initialMessageAttemptConflict(existing)
	}
	return initialMessageAttemptResult(existing), nil
}

func initialMessageAttemptConflict(
	existing initialMessageAttempt,
) (InitialMessageResult, error) {
	return InitialMessageResult{}, httpapi.Conflict(
		httpapi.CodeConflict,
		"an initial message attempt already exists for this runtime session",
		map[string]any{
			"agent":         existing.Agent,
			"session_id":    existing.SessionID,
			"message_bytes": len(existing.Message),
			"state":         existing.State,
		},
	)
}

func (s *Handler) initialMessageAttempt(
	workspaceID string,
	runtimeSessionKey string,
) (initialMessageAttempt, bool) {
	s.initialMessagesMu.Lock()
	defer s.initialMessagesMu.Unlock()
	attempt, ok := s.initialMessages[initialMessageKey{
		workspaceID: workspaceID, runtimeSessionKey: runtimeSessionKey,
	}]
	return attempt, ok
}

func (s *Handler) reserveInitialMessageAttempt(
	workspaceID string,
	runtimeSessionKey string,
	proposed initialMessageAttempt,
) (initialMessageAttempt, bool) {
	s.initialMessagesMu.Lock()
	defer s.initialMessagesMu.Unlock()
	if s.initialMessages == nil {
		s.initialMessages = make(map[initialMessageKey]initialMessageAttempt)
	}
	key := initialMessageKey{
		workspaceID: workspaceID, runtimeSessionKey: runtimeSessionKey,
	}
	if existing, ok := s.initialMessages[key]; ok {
		return existing, false
	}
	proposed.State = initialMessagePending
	proposed.ReservedAt = s.now().UTC()
	s.initialMessages[key] = proposed
	return proposed, true
}

func (s *Handler) releaseInitialMessageAttempt(
	workspaceID string,
	runtimeSessionKey string,
	proposed initialMessageAttempt,
) {
	s.initialMessagesMu.Lock()
	defer s.initialMessagesMu.Unlock()
	key := initialMessageKey{
		workspaceID: workspaceID, runtimeSessionKey: runtimeSessionKey,
	}
	existing, ok := s.initialMessages[key]
	if ok && existing.State == initialMessagePending &&
		existing.Agent == proposed.Agent && existing.SessionID == proposed.SessionID &&
		existing.Message == proposed.Message {
		delete(s.initialMessages, key)
	}
}

func (s *Handler) finishInitialMessageAttempt(
	workspaceID string,
	runtimeSessionKey string,
	state string,
) initialMessageAttempt {
	s.initialMessagesMu.Lock()
	defer s.initialMessagesMu.Unlock()
	key := initialMessageKey{
		workspaceID: workspaceID, runtimeSessionKey: runtimeSessionKey,
	}
	attempt := s.initialMessages[key]
	attempt.State = state
	if state == initialMessageDelivered {
		deliveredAt := s.now().UTC()
		attempt.DeliveredAt = &deliveredAt
	}
	s.initialMessages[key] = attempt
	return attempt
}

func (s *Handler) clearInitialMessageAttempts(workspaceID string) {
	s.initialMessagesMu.Lock()
	defer s.initialMessagesMu.Unlock()
	for key := range s.initialMessages {
		if key.workspaceID == workspaceID {
			delete(s.initialMessages, key)
		}
	}
}

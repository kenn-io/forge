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
	_ context.Context,
	input *initialMessagePathInput,
) (*initialMessageOutput, error) {
	attempt, ok := s.initialMessageAttempt(input.ID, input.SessionKey)
	if !ok {
		return nil, httpapi.NotFound(
			httpapi.CodeNotFound, "initial message status not found", nil,
		)
	}
	return &initialMessageOutput{Body: *initialMessageStatusResponse(attempt)}, nil
}

func (s *Handler) submitInitialMessage(
	ctx context.Context,
	input *submitInitialMessageInput,
) (*initialMessageOutput, error) {
	if s.workspaces == nil || s.runtime == nil || s.agentActivity == nil {
		return nil, httpapi.ServiceUnavailable("initial message delivery not configured")
	}
	agent, err := agenthook.ParseAgent(input.Body.Agent)
	if err != nil {
		return nil, httpapi.Validation("body.agent", err.Error())
	}
	sessionID := strings.TrimSpace(input.Body.SessionID)
	if sessionID == "" {
		return nil, httpapi.Validation("body.session_id", "session_id is required")
	}
	message, _, err := normalizeInitialAgentMessage(input.Body.Message)
	if err != nil {
		return nil, httpapi.Validation("body.message", err.Error())
	}
	proposed := initialMessageAttempt{
		Agent: string(agent), SessionID: sessionID, Message: message,
	}

	if existing, ok := s.initialMessageAttempt(input.ID, input.SessionKey); ok {
		return existingInitialMessageAttempt(existing, proposed)
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
	live := false
	for _, runtimeSession := range s.runtime.ListSessions(input.ID) {
		if runtimeSession.Key == input.SessionKey &&
			runtimeSession.Kind == localruntime.LaunchTargetAgent &&
			(runtimeSession.Status == localruntime.SessionStatusStarting ||
				runtimeSession.Status == localruntime.SessionStatusRunning) {
			live = true
			break
		}
	}
	if !live {
		return nil, httpapi.Conflict(
			httpapi.CodeConflict, "agent runtime session is not live", nil,
		)
	}
	matchedReport := false
	for _, report := range s.agentActivity.LiveReportsForWorkspace(
		summary.WorktreePath, []string{input.SessionKey},
	) {
		if report.Agent == string(agent) && report.SessionID == sessionID {
			matchedReport = true
			break
		}
	}
	if !matchedReport {
		return nil, httpapi.Conflict(
			httpapi.CodeConflict, "coding session does not match the live agent runtime", nil,
		)
	}

	attempt, reserved := s.reserveInitialMessageAttempt(input.ID, input.SessionKey, proposed)
	if !reserved {
		return existingInitialMessageAttempt(attempt, proposed)
	}
	if err := s.runtime.SubmitInitialMessage(input.ID, input.SessionKey, message); err != nil {
		if errors.Is(err, localruntime.ErrBracketedPasteInactive) {
			s.releaseInitialMessageAttempt(input.ID, input.SessionKey, proposed)
			return nil, httpapi.Validation("body.message", err.Error())
		}
		s.finishInitialMessageAttempt(input.ID, input.SessionKey, initialMessageUncertain)
		return nil, httpapi.Internal("submit initial message failed")
	}
	delivered := s.finishInitialMessageAttempt(
		input.ID, input.SessionKey, initialMessageDelivered,
	)
	return &initialMessageOutput{Body: *initialMessageStatusResponse(delivered)}, nil
}

func existingInitialMessageAttempt(
	existing initialMessageAttempt,
	proposed initialMessageAttempt,
) (*initialMessageOutput, error) {
	if existing.Agent != proposed.Agent ||
		existing.SessionID != proposed.SessionID ||
		existing.Message != proposed.Message {
		return initialMessageAttemptConflict(existing)
	}
	return &initialMessageOutput{Body: *initialMessageStatusResponse(existing)}, nil
}

func initialMessageAttemptConflict(
	existing initialMessageAttempt,
) (*initialMessageOutput, error) {
	return nil, httpapi.Conflict(
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

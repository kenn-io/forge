package workspaceapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.kenn.io/kit/agenthook"
)

const maxInitialAgentMessageBytes = 64 << 10

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
	Body agentInitialMessageReceiptResponse
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
		if unicode.IsControl(value) && !unicode.IsSpace(value) {
			return "", 0, fmt.Errorf("message contains unsafe control character U+%04X", value)
		}
	}
	messageBytes := len(message)
	if messageBytes > maxInitialAgentMessageBytes {
		return "", 0, errors.New("message must not exceed 64 KiB after line-ending normalization")
	}
	return message, messageBytes, nil
}

func (s *Handler) getInitialMessageReceipt(
	ctx context.Context,
	input *initialMessagePathInput,
) (*initialMessageOutput, error) {
	if s.db == nil {
		return nil, httpapi.ServiceUnavailable("database not configured")
	}
	receipt, err := s.db.GetAgentInitialMessageReceipt(ctx, input.ID, input.SessionKey)
	if err != nil {
		return nil, httpapi.Internal("get initial message receipt failed")
	}
	if receipt == nil {
		return nil, httpapi.NotFound(
			httpapi.CodeNotFound, "initial message receipt not found", nil,
		)
	}
	return &initialMessageOutput{Body: *receiptResponse(*receipt)}, nil
}

func (s *Handler) submitInitialMessage(
	ctx context.Context,
	input *submitInitialMessageInput,
) (*initialMessageOutput, error) {
	if s.db == nil || s.workspaces == nil || s.runtime == nil || s.agentActivity == nil {
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
	message, messageBytes, err := normalizeInitialAgentMessage(input.Body.Message)
	if err != nil {
		return nil, httpapi.Validation("body.message", err.Error())
	}
	proposed := db.AgentInitialMessageReceipt{
		WorkspaceID: input.ID, RuntimeSessionKey: input.SessionKey,
		Agent: string(agent), CodingSessionID: sessionID, MessageBytes: messageBytes,
	}

	existing, err := s.db.GetAgentInitialMessageReceipt(ctx, input.ID, input.SessionKey)
	if err != nil {
		return nil, httpapi.Internal("get initial message receipt failed")
	}
	if existing != nil {
		return existingInitialMessageReceipt(*existing, proposed)
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

	receipt, reserved, err := s.db.ReserveAgentInitialMessage(ctx, proposed)
	if err != nil {
		if errors.Is(err, db.ErrAgentInitialMessageReceiptConflict) {
			var conflict *db.AgentInitialMessageReceiptConflictError
			if errors.As(err, &conflict) {
				return initialMessageReceiptConflict(conflict.Existing)
			}
		}
		if errors.Is(err, db.ErrAgentInitialMessageRuntimeNotFound) {
			return nil, httpapi.Conflict(
				httpapi.CodeConflict, "agent runtime session is no longer recorded", nil,
			)
		}
		return nil, httpapi.Internal("reserve initial message receipt failed")
	}
	if !reserved {
		return &initialMessageOutput{Body: *receiptResponse(receipt)}, nil
	}

	if err := s.runtime.SubmitInitialMessage(input.ID, input.SessionKey, message); err != nil {
		if _, markErr := s.db.MarkAgentInitialMessageUncertain(
			ctx, input.ID, input.SessionKey,
		); markErr != nil {
			return nil, httpapi.Internal("submit initial message and record uncertain receipt failed")
		}
		if errors.Is(err, localruntime.ErrBracketedPasteInactive) {
			return nil, httpapi.Validation("body.message", err.Error())
		}
		return nil, httpapi.Internal("submit initial message failed")
	}
	delivered, err := s.db.MarkAgentInitialMessageDelivered(ctx, input.ID, input.SessionKey)
	if err != nil {
		return nil, httpapi.Internal("mark initial message delivered failed")
	}
	return &initialMessageOutput{Body: *receiptResponse(delivered)}, nil
}

func existingInitialMessageReceipt(
	existing db.AgentInitialMessageReceipt,
	proposed db.AgentInitialMessageReceipt,
) (*initialMessageOutput, error) {
	if existing.Agent != proposed.Agent ||
		existing.CodingSessionID != proposed.CodingSessionID ||
		existing.MessageBytes != proposed.MessageBytes {
		return initialMessageReceiptConflict(existing)
	}
	return &initialMessageOutput{Body: *receiptResponse(existing)}, nil
}

func initialMessageReceiptConflict(
	existing db.AgentInitialMessageReceipt,
) (*initialMessageOutput, error) {
	return nil, httpapi.Conflict(
		httpapi.CodeConflict,
		"an initial message attempt already exists for this runtime session",
		map[string]any{
			"agent":         existing.Agent,
			"session_id":    existing.CodingSessionID,
			"message_bytes": existing.MessageBytes,
			"state":         existing.State,
		},
	)
}

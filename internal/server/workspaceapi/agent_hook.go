package workspaceapi

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/kit/agenthook"
)

type receiveAgentHookInput struct {
	Agent             string                  `path:"agent"`
	RuntimeSessionKey string                  `header:"X-Kenn-Forge-Runtime-Session-Key"`
	Body              agentactivity.HookEvent `json:"body"`
}

type agentHookResponse struct {
	HookOutput *agentHookOutput `json:"hook_output,omitempty"`
}

type agentHookOutput struct {
	HookSpecificOutput agentHookSpecificOutput `json:"hookSpecificOutput"`
}

type agentHookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

type receiveAgentHookOutput struct {
	Body agentHookResponse
}

func (s *Handler) receiveAgentHook(
	ctx context.Context, input *receiveAgentHookInput,
) (*receiveAgentHookOutput, error) {
	integration, err := agenthook.ParseAgent(input.Agent)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	if err := s.agentActivity.HandleEvent(
		string(integration), input.Body, input.RuntimeSessionKey,
	); err != nil {
		slog.Warn("record agent hook activity", "err", err)
		if input.Body.HookEventName == "SessionEnd" {
			return nil, httpapi.Internal("record agent hook activity: " + err.Error())
		}
	}
	s.recordAgentConversation(ctx, string(integration), input.Body, input.RuntimeSessionKey)

	output := &receiveAgentHookOutput{}
	if integration != agenthook.AgentClaude ||
		input.Body.HookEventName != "SessionStart" || s.workspaces == nil {
		return output, nil
	}
	startContext, err := s.workspaces.RenderAgentContextForWorktree(ctx, input.Body.CWD)
	if err != nil {
		slog.Warn("render agent hook workspace context", "err", err)
		return output, nil
	}
	if strings.TrimSpace(startContext) == "" {
		return output, nil
	}
	output.Body.HookOutput = &agentHookOutput{
		HookSpecificOutput: agentHookSpecificOutput{
			HookEventName:     "SessionStart",
			AdditionalContext: startContext,
		},
	}
	return output, nil
}

// recordAgentConversation saves the conversation a launched agent reports on
// its runtime row. SessionEnd deletes the activity report, including when a
// host shutdown kills the agent, so recovery reads the conversation from the
// row instead. SessionEnd itself saves only why the conversation ended.
func (s *Handler) recordAgentConversation(
	ctx context.Context, agent string, hook agentactivity.HookEvent, runtimeSessionKey string,
) {
	sessionID := strings.TrimSpace(hook.SessionID)
	runtimeSessionKey = strings.TrimSpace(runtimeSessionKey)
	agent = strings.ToLower(agent)
	if s.db == nil || sessionID == "" || runtimeSessionKey == "" || hook.AgentID != "" {
		return
	}
	// An agent started inside a launched terminal inherits its runtime key but
	// may work in another worktree; only the terminal's own workspace counts.
	saved, ok, err := s.db.RuntimeSessionConversation(ctx, runtimeSessionKey)
	if err != nil {
		slog.Warn("read agent runtime conversation", "session_key", runtimeSessionKey, "err", err)
		return
	}
	if !ok || !agentactivity.SameWorkspace(hook.CWD, saved.WorktreePath) {
		return
	}
	if hook.HookEventName == "SessionEnd" {
		if _, err := s.db.RecordWorkspaceRuntimeConversationEnd(
			ctx, runtimeSessionKey, agent, sessionID, strings.TrimSpace(hook.Reason),
		); err != nil {
			slog.Warn("record agent conversation end", "session_key", runtimeSessionKey, "err", err)
		}
		return
	}
	if s.savedConversationHoldsRow(saved, agent, sessionID, runtimeSessionKey) {
		return
	}
	if _, err := s.db.RecordWorkspaceRuntimeConversation(
		ctx, runtimeSessionKey, agent, sessionID,
	); err != nil {
		slog.Warn("record agent conversation", "session_key", runtimeSessionKey, "err", err)
	}
}

// savedConversationHoldsRow reports whether a different conversation than
// the reporting one is saved on the row and keeps it, as when the terminal's
// agent runs another hooked agent in the same worktree. That agent inherits
// the runtime key, but the terminal's own conversation keeps its place until
// its SessionEnd, which /clear and /resume also send first. A conversation
// terminated by a signal keeps it until it reports again, so a nested agent
// that outlives it cannot take its place. Only a report newer than both the
// row's runtime and this daemon counts: a run before a restart, or hooks lost
// while no daemon listened, leave older ones. An agent without SessionEnd
// hooks never shows an end, so its newest conversation wins.
func (s *Handler) savedConversationHoldsRow(
	saved db.RuntimeConversation, agent, sessionID, runtimeSessionKey string,
) bool {
	if saved.ID == "" || (saved.Agent == agent && saved.ID == sessionID) {
		return false
	}
	if conversationTerminated(saved.EndReason) {
		return true
	}
	if s.agentActivity == nil || !reportsSessionEnd(saved.Agent) {
		return false
	}
	updatedAt, ok := s.agentActivity.ReportUpdatedAt(saved.Agent, saved.ID, runtimeSessionKey)
	return ok && !updatedAt.Before(saved.CreatedAt) && !updatedAt.Before(s.startedAt)
}

func reportsSessionEnd(agent string) bool {
	integration, err := agenthook.ParseAgent(agent)
	if err != nil {
		return false
	}
	profile, ok := agenthook.LookupProfile(integration)
	return ok && slices.Contains(profile.SupportedEvents, agenthook.EventSessionEnd)
}

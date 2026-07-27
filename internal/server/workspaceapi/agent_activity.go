package workspaceapi

import (
	"log/slog"
	"time"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

// withAgentActivity annotates a workspace response with the state agent hooks
// last reported. Every response path funnels through here so a row shows the
// same agent state whether it came from cached, refreshed, or live enrichment.
func (s *Handler) withAgentActivity(
	resp workspaceResponse,
	summary *db.WorkspaceSummary,
) workspaceResponse {
	if s.agentActivity == nil || s.runtime == nil || summary == nil {
		return resp
	}
	snapshot, ok := s.agentActivity.SnapshotForWorkspace(
		summary.WorktreePath, s.liveAgentSessionKeys(summary.ID),
	)
	if !ok {
		return resp
	}
	state := string(snapshot.State)
	updatedAt := snapshot.UpdatedAt.UTC().Format(time.RFC3339)
	resp.AgentState = &state
	resp.AgentStateUpdatedAt = &updatedAt
	return resp
}

// liveAgentSessionKeys lists the workspace's agent sessions that could still be
// reporting. Reports from any other session describe work that already ended.
func (s *Handler) liveAgentSessionKeys(workspaceID string) []string {
	var keys []string
	for _, session := range s.runtime.ListSessions(workspaceID) {
		if session.Kind != localruntime.LaunchTargetAgent {
			continue
		}
		if session.Status != localruntime.SessionStatusRunning &&
			session.Status != localruntime.SessionStatusStarting {
			continue
		}
		keys = append(keys, session.Key)
	}
	return keys
}

// removeAgentActivityRuntimeSession drops the reports a runtime session left
// behind, so a stopped agent stops overriding tmux activity immediately instead
// of waiting for the reports to expire.
func (h *Handler) removeAgentActivityRuntimeSession(sessionKey string) {
	if h == nil || h.agentActivity == nil || sessionKey == "" {
		return
	}
	if err := h.agentActivity.RemoveRuntimeSession(sessionKey); err != nil {
		slog.Warn("remove agent activity report",
			"session_key", sessionKey,
			"err", err)
	}
}

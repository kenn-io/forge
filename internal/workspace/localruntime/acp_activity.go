package localruntime

import (
	"log/slog"

	"go.kenn.io/forge/internal/agentactivity"
)

// ACPActivityAgent names activity reports written by ACP owners. ACP agents
// never run Forge hooks; their connection is the authoritative turn signal.
const ACPActivityAgent = "acp"

// activityLocked maps the chat to the activity states hook agents report.
// Pending questions outrank a running turn so the user sees what blocks it.
func (a *ACP) activityLocked() agentactivity.State {
	switch {
	case len(a.state.Permissions) > 0:
		return agentactivity.StateApproval
	case len(a.state.Elicitations) > 0:
		return agentactivity.StateInput
	case a.state.Busy:
		return agentactivity.StateWorking
	case a.turnCompleted:
		return agentactivity.StateDone
	default:
		return agentactivity.StateIdle
	}
}

// reportACPActivity records each activity transition until the agent exits,
// then removes the report. It runs in the durable owner so activity stays
// current while the daemon is stopped or the workspace is closed.
func reportACPActivity(store *agentactivity.Store, agent *ACP, runtimeSessionKey, cwd string) {
	// An owner that crashed left its report behind, possibly under a session
	// ID this owner no longer uses; it must not keep reporting for the runtime.
	if err := store.RemoveRuntimeSession(runtimeSessionKey); err != nil {
		slog.Warn("clear stale ACP activity", "session_key", runtimeSessionKey, "err", err)
	}
	changes, unsubscribe := agent.Subscribe()
	defer unsubscribe()
	var last agentactivity.State
	for {
		select {
		case <-changes:
			agent.mu.Lock()
			state := agent.activityLocked()
			agent.mu.Unlock()
			if state == last {
				continue
			}
			if err := store.Record(ACPActivityAgent, agent.sessionID, runtimeSessionKey, cwd, state); err != nil {
				slog.Warn("record ACP activity", "session_key", runtimeSessionKey, "err", err)
				continue
			}
			last = state
		case <-agent.Done():
			if err := store.Remove(ACPActivityAgent, agent.sessionID, runtimeSessionKey); err != nil {
				slog.Warn("remove ACP activity", "session_key", runtimeSessionKey, "err", err)
			}
			return
		}
	}
}

package localruntime

import acpsdk "github.com/coder/acp-go-sdk"

// An agent can keep working after its prompt turn ends: a command it sent to
// the background still runs, and when that command finishes the agent may
// start a turn on its own. ACP v1 has no turn for either, so the chat would
// read as idle while the agent works and could be parked under it.
//
// claude-agent-acp reports both on v1 without extensions. A backgrounded
// Bash call completes with its terminal still open and marks its result with
// claudeCode.toolResponse.backgroundTaskId; the terminal exits when the
// command ends. A turn it starts itself streams output while no prompt is
// open. Cost updates clear its activity heuristic but carry no terminal
// outcome for transcript completion.

// trackTerminalLocked follows the command terminal of tool call id through
// the Zed terminal conventions. The background marker can arrive before or
// after the result that leaves the terminal open, so it only marks a terminal
// that has not exited.
func (a *ACP) trackTerminalLocked(id string, meta map[string]any) {
	if info, _ := meta["terminal_info"].(map[string]any); info != nil {
		if _, open := a.terminals[id]; !open {
			if a.terminals == nil {
				a.terminals = make(map[string]bool)
			}
			a.terminals[id] = false
		}
	}
	if _, open := a.terminals[id]; open && backgroundTaskID(meta) != "" {
		a.terminals[id] = true
	}
	if _, exited := meta["terminal_exit"].(map[string]any); exited {
		delete(a.terminals, id)
	}
}

func backgroundTaskID(meta map[string]any) string {
	claude, _ := meta["claudeCode"].(map[string]any)
	response, _ := claude["toolResponse"].(map[string]any)
	id, _ := response["backgroundTaskId"].(string)
	return id
}

// agentOutputLocked notes output the agent sends. Output while no prompt turn
// runs is a turn the agent started itself. Only an agent that ends its turns
// with a cost report can end such a turn, so no other agent starts one.
func (a *ACP) agentOutputLocked() {
	if !a.state.Busy && a.reportsTurnCost && !a.agentTurn {
		a.agentTurn = true
		a.agentTurnStart = len(a.state.Messages)
		a.changedLocked()
	}
}

// usageLocked clears autonomous activity at a cost report. This heuristic
// supplies no terminal outcome, so transcript completion stays unknown.
// It reports whether the chat changed.
func (a *ACP) usageLocked(usage *acpsdk.SessionUsageUpdate) bool {
	if usage.Cost == nil {
		return false
	}
	a.reportsTurnCost = true
	if !a.agentTurn {
		return false
	}
	a.agentTurn = false
	a.turnCompleted = true
	return true
}

// backgroundLocked reports agent work outside a prompt turn: a backgrounded
// command still runs, or a turn the agent started itself does.
func (a *ACP) backgroundLocked() bool {
	if a.agentTurn {
		return true
	}
	for _, background := range a.terminals {
		if background {
			return true
		}
	}
	return false
}

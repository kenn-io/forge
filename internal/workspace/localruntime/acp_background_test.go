package localruntime

import (
	"fmt"
	"path/filepath"
	"testing"
	"testing/synctest"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
)

// The updates below follow what claude-agent-acp 0.94.0 sent a client with
// Forge's capabilities for a Bash call with run_in_background: the call
// completes with its terminal open, the turn ends, the terminal exits when
// the command does, and then Claude starts a turn of its own.

func acpActivity(agent *ACP) agentactivity.State {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return agent.activityLocked()
}

func acpParkable(agent *ACP) bool {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return agent.idleLocked()
}

func acpTurnCost(t *testing.T, agent *ACP) {
	t.Helper()
	acpUpdate(t, agent, acpsdk.SessionUpdate{UsageUpdate: &acpsdk.SessionUsageUpdate{Used: 1, Size: 10, Cost: &acpsdk.Cost{Amount: 0.5, Currency: "USD"}}})
}

func acpTerminalCall(t *testing.T, agent *ACP, id string) {
	t.Helper()
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
		ToolCallId: acpsdk.ToolCallId(id), Title: "Terminal", Kind: acpsdk.ToolKindExecute, Status: acpsdk.ToolCallStatusPending,
		Meta: map[string]any{"claudeCode": map[string]any{"toolName": "Bash"}, "terminal_info": map[string]any{"terminal_id": id}},
	}})
}

func acpBackgrounded(t *testing.T, agent *ACP, id string) {
	t.Helper()
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
		ToolCallId: acpsdk.ToolCallId(id),
		Meta:       map[string]any{"claudeCode": map[string]any{"toolName": "Bash", "toolResponse": map[string]any{"backgroundTaskId": "task-" + id}}},
	}})
}

func acpTerminalStatus(t *testing.T, agent *ACP, id string, exited bool) {
	t.Helper()
	completed := acpsdk.ToolCallStatusCompleted
	meta := map[string]any{"claudeCode": map[string]any{"toolName": "Bash"}}
	if exited {
		meta["terminal_exit"] = map[string]any{"terminal_id": id, "exit_code": nil, "signal": nil}
	}
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{ToolCallId: acpsdk.ToolCallId(id), Status: &completed, Meta: meta}})
}

func acpTerminalExit(t *testing.T, agent *ACP, id string) {
	t.Helper()
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
		ToolCallId: acpsdk.ToolCallId(id),
		Meta:       map[string]any{"terminal_exit": map[string]any{"terminal_id": id, "exit_code": nil, "signal": nil}},
	}})
}

func TestACPWorksWhileABackgroundCommandRunsAndTheTurnItStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		// A saved, loadable session is what lets an idle chat park.
		agent.loadSession = true
		agent.recordPath = filepath.Join(t.TempDir(), "chat.json")
		require.True(t, acpParkable(agent))
		require.NoError(t, agent.Prompt("run the tests in the background", ACPSender{Kind: ACPSenderUser}))
		acpTerminalCall(t, agent, "bg")
		acpBackgrounded(t, agent, "bg")
		acpTerminalStatus(t, agent, "bg", false)
		acpText(t, agent, "Waiting for the background job.")
		acpTurnCost(t, agent)
		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()

		state := publishedACPState(t, agent)
		assert.False(t, state.Busy, "the prompt turn ended")
		assert.True(t, state.Background, "the command still runs")
		assert.Equal(t, agentactivity.StateWorking, acpActivity(agent))
		assert.False(t, acpParkable(agent), "parking would stop the command")

		acpTerminalExit(t, agent, "bg")
		assert.False(t, publishedACPState(t, agent).Background)
		assert.Equal(t, agentactivity.StateDone, acpActivity(agent))
		assert.True(t, acpParkable(agent))

		// Claude answers the finished command in a turn it started itself.
		acpText(t, agent, "Job done.")
		assert.True(t, publishedACPState(t, agent).Background)
		assert.Equal(t, agentactivity.StateWorking, acpActivity(agent))
		assert.False(t, acpParkable(agent), "parking would stop the turn")
		acpTurnCost(t, agent)
		assert.False(t, publishedACPState(t, agent).Background)
		assert.Equal(t, agentactivity.StateDone, acpActivity(agent))
		assert.True(t, acpParkable(agent))
	})
}

// The updates below follow what codex-acp 2.3.0 sent Forge for a command that
// outlived its turn: the call stays in progress with its terminal open after
// end_turn, and completes with terminal_exit when the command exits.

func acpCodexCommand(t *testing.T, agent *ACP, id string) {
	t.Helper()
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
		ToolCallId: acpsdk.ToolCallId(id), Title: "make lint", Kind: acpsdk.ToolKindExecute, Status: acpsdk.ToolCallStatusInProgress,
		Meta: map[string]any{"terminal_info": map[string]any{"cwd": "/work", "terminal_id": id}},
	}})
}

func acpCodexCommandExit(t *testing.T, agent *ACP, id string) {
	t.Helper()
	completed := acpsdk.ToolCallStatusCompleted
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
		ToolCallId: acpsdk.ToolCallId(id), Status: &completed,
		Meta: map[string]any{"terminal_exit": map[string]any{"terminal_id": id, "exit_code": 0, "signal": nil}},
	}})
}

func TestACPWorksWhileACodexCommandOutlivesItsTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.loadSession = true
		agent.recordPath = filepath.Join(t.TempDir(), "chat.json")
		require.NoError(t, agent.Prompt("push the branch", ACPSender{Kind: ACPSenderUser}))
		acpCodexCommand(t, agent, "push")
		acpCodexCommand(t, agent, "quick")
		acpCodexCommandExit(t, agent, "quick")
		// A finished call whose terminal never reported an exit runs nothing.
		acpTerminalCall(t, agent, "finished")
		acpTerminalStatus(t, agent, "finished", false)
		acpText(t, agent, "The push is still running its checks.")
		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()

		state := publishedACPState(t, agent)
		assert.False(t, state.Busy, "the prompt turn ended")
		assert.True(t, state.Background, "the push still runs")
		assert.Equal(t, agentactivity.StateWorking, acpActivity(agent))
		assert.False(t, acpParkable(agent), "parking would stop the push")

		acpCodexCommandExit(t, agent, "push")
		assert.False(t, publishedACPState(t, agent).Background)
		assert.Equal(t, agentactivity.StateDone, acpActivity(agent))
		assert.True(t, acpParkable(agent))
	})
}

func TestACPCancelledTurnLeavesNothingInTheBackground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		require.NoError(t, agent.Prompt("push the branch", ACPSender{Kind: ACPSenderUser}))
		acpCodexCommand(t, agent, "push")
		peer.endTurn(acpsdk.StopReasonCancelled)
		synctest.Wait()

		assert.False(t, publishedACPState(t, agent).Background, "a cancelled command may never report its exit")
		assert.Equal(t, agentactivity.StateDone, acpActivity(agent))

		// The next turn that ends normally leaves the interrupted command alone.
		require.NoError(t, agent.Prompt("try again", ACPSender{Kind: ACPSenderUser}))
		acpText(t, agent, "Done.")
		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()

		assert.False(t, publishedACPState(t, agent).Background)
		assert.Equal(t, agentactivity.StateDone, acpActivity(agent))
	})
}

func TestACPStoppedTakeoverLeavesNothingInTheBackground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.state.SteeringSupported = true
		agent.reportsThreadStatus = true
		require.NoError(t, agent.Prompt("work", ACPSender{Kind: ACPSenderUser}))
		steered := make(chan error, 1)
		go func() {
			steered <- agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "steer"})
		}()
		synctest.Wait()
		peer.answer(acpSteeringMethod, `{"outcome":"startedNewTurn"}`)
		require.NoError(t, <-steered)
		synctest.Wait()
		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
		require.True(t, publishedACPState(t, agent).Busy, "the agent's own turn runs on")

		acpCodexCommand(t, agent, "push")
		require.NoError(t, agent.Command(ACPCommand{Type: "cancel"}))
		acpThreadStatus(t, agent, "idle")

		state := publishedACPState(t, agent)
		assert.False(t, state.Busy)
		assert.False(t, state.Background, "a stopped command may never report its exit")
		assert.Equal(t, agentactivity.StateDone, acpActivity(agent))
	})
}

// A stop pressed while a steer waits for its answer ends the prompt first;
// the takeover the agent then reports was stopped too.
func TestACPTakeoverAfterAStoppedPromptLeavesNothingInTheBackground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.state.SteeringSupported = true
		agent.reportsThreadStatus = true
		require.NoError(t, agent.Prompt("work", ACPSender{Kind: ACPSenderUser}))
		steered := make(chan error, 1)
		go func() {
			steered <- agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "steer"})
		}()
		synctest.Wait()
		require.NoError(t, agent.Command(ACPCommand{Type: "cancel"}))
		peer.endTurn(acpsdk.StopReasonCancelled)
		synctest.Wait()
		peer.answer(acpSteeringMethod, `{"outcome":"startedNewTurn"}`)
		require.NoError(t, <-steered)
		synctest.Wait()
		require.True(t, publishedACPState(t, agent).Busy, "the agent's own turn runs")

		acpCodexCommand(t, agent, "push")
		acpThreadStatus(t, agent, "idle")

		state := publishedACPState(t, agent)
		assert.False(t, state.Busy)
		assert.False(t, state.Background, "a stopped command may never report its exit")
		assert.Equal(t, agentactivity.StateDone, acpActivity(agent))
	})
}

func TestACPForegroundCommandsLeaveNothingInTheBackground(t *testing.T) {
	agent := newDetachedACP(t)
	acpTerminalCall(t, agent, "fg")
	acpTerminalStatus(t, agent, "fg", true)
	// A marker that arrives after the command exited marks nothing.
	acpTerminalCall(t, agent, "fast")
	acpTerminalStatus(t, agent, "fast", true)
	acpBackgrounded(t, agent, "fast")

	assert.False(t, publishedACPState(t, agent).Background)
	assert.Equal(t, agentactivity.StateIdle, acpActivity(agent))
}

// An agent that reports no turn cost could never end a turn it started, so
// late output from it must not leave the chat working.
func TestACPOutputBetweenTurnsNeedsATurnCostReport(t *testing.T) {
	agent := newDetachedACP(t)
	acpText(t, agent, "late output")

	assert.False(t, publishedACPState(t, agent).Background)
	assert.Equal(t, agentactivity.StateIdle, acpActivity(agent))
}

func TestACPAutonomousOutputStartsAnUnknownTurn(t *testing.T) {
	for _, reportsCost := range []bool{false, true} {
		t.Run(fmt.Sprintf("reports cost=%t", reportsCost), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				agent, peer := newScriptedACP(t)
				require.NoError(t, agent.Prompt("question", ACPSender{Kind: ACPSenderUser}))
				acpText(t, agent, "Prompt reply.")
				if reportsCost {
					acpTurnCost(t, agent)
				}
				peer.endTurn(acpsdk.StopReasonEndTurn)
				synctest.Wait()
				acpUpdate(t, agent, acpChunk("First autonomous ", ""))
				acpUpdate(t, agent, acpChunk("reply.", ""))
				if reportsCost {
					acpTurnCost(t, agent)
					acpUpdate(t, agent, acpChunk("Second autonomous reply.", ""))
					acpTurnCost(t, agent)
				}
				advance(2 * minTextDeliveryInterval)
				state := publishedACPState(t, agent)
				if reportsCost {
					require.Len(t, state.Messages, 4)
					assert.Equal(t, "Second autonomous reply.", state.Messages[3].Text)
					assert.Equal(t, &ACPTurn{}, state.Messages[3].Turn)
				} else {
					require.Len(t, state.Messages, 3)
				}
				assert.Equal(t, "Prompt reply.", state.Messages[1].Text)
				assert.Equal(t, "First autonomous reply.", state.Messages[2].Text)
				assert.Equal(t, &ACPTurn{}, state.Messages[2].Turn)
			})
		})
	}
}

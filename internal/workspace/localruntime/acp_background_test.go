package localruntime

import (
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

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

func TestACPAgentStartedRepliesStaySeparateWithUnknownOutcomes(t *testing.T) {
	for _, id := range []string{"", "reused"} {
		t.Run("message ID "+id, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				assert := assert.New(t)
				agent, peer := newScriptedACP(t)
				require.NoError(t, agent.Prompt("question", ACPSender{Kind: ACPSenderUser}))
				chunk := acpsdk.SessionUpdate{AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{MessageId: &id, Content: acpsdk.TextBlock("First reply.")}}
				acpUpdate(t, agent, chunk)
				peer.endTurn(acpsdk.StopReasonEndTurn)
				synctest.Wait()
				first := publishedACPState(t, agent).Messages[1]
				chunk.AgentMessageChunk.Content = acpsdk.TextBlock("Second reply")
				acpUpdate(t, agent, chunk)
				assert.Equal([]ACPMessage{publishedACPState(t, agent).Messages[0], first}, publishedACPState(t, agent).Messages)
				chunk.AgentMessageChunk.Content = acpsdk.TextBlock(" finished.")
				acpUpdate(t, agent, chunk)
				acpTurnCost(t, agent)
				time.Sleep(2 * minTextDeliveryInterval)
				synctest.Wait()
				state := publishedACPState(t, agent)
				require.Len(t, state.Messages, 3)
				assert.Equal(first, state.Messages[1])
				assert.Equal("Second reply finished.", state.Messages[2].Text)
				assert.Nil(state.Messages[2].TurnEnd)
			})
		})
	}
}

func TestACPAgentStartedReplyLeavesUnmarkedHistoryAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent := newDetachedACP(t)
		acpText(t, agent, "Old unmarked reply.")
		time.Sleep(minTextDeliveryInterval)
		synctest.Wait()
		old := publishedACPState(t, agent).Messages[0]
		acpTurnCost(t, agent)
		acpText(t, agent, "New reply.")
		acpTurnCost(t, agent)
		time.Sleep(minTextDeliveryInterval)
		synctest.Wait()
		state := publishedACPState(t, agent)
		require.Len(t, state.Messages, 2)
		assert.Equal(t, old, state.Messages[0])
		assert.Nil(t, state.Messages[1].TurnEnd)
	})
}

func TestACPPromptTakeoverPublishesHeldAutonomousOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		agent, peer := newScriptedACP(t)
		acpTurnCost(t, agent)
		acpText(t, agent, "Autonomous reply.")
		held, err := decodedACPUpdates(t, agent, 0)
		require.NoError(t, err)
		require.Empty(t, held.Messages)
		require.NoError(t, agent.Prompt("next question", ACPSender{Kind: ACPSenderUser}))
		acpText(t, agent, "Prompt reply.")
		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
		delivered, err := decodedACPUpdates(t, agent, held.MessageCursor)
		require.NoError(t, err)
		require.Len(t, delivered.Messages, 3)
		assert.Equal("Autonomous reply.", delivered.Messages[0].Text)
		assert.Equal("next question", delivered.Messages[1].Text)
		assert.Equal("Prompt reply.", delivered.Messages[2].Text)
		previous := ""
		for _, message := range delivered.Messages {
			assert.Equal(previous, message.PreviousID)
			previous = message.ID
		}
	})
}

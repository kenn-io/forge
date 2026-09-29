package localruntime

import (
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func acpChunk(text, messageID string) acpsdk.SessionUpdate {
	chunk := &acpsdk.SessionUpdateAgentMessageChunk{Content: acpsdk.TextBlock(text)}
	if messageID != "" {
		chunk.MessageId = &messageID
	}
	return acpsdk.SessionUpdate{AgentMessageChunk: chunk}
}

func TestACPStartsANewMessageWhenTheMessageIDChanges(t *testing.T) {
	agent := newDetachedACP(t)
	acpUpdate(t, agent, acpChunk("Finished the list.\n\n", "11111111-1111-4111-8111-111111111111"))
	acpUpdate(t, agent, acpChunk("STEERED\n\n", "22222222-2222-4222-8222-222222222222"))
	acpUpdate(t, agent, acpChunk("More.\n\n", "22222222-2222-4222-8222-222222222222"))
	agent.mu.Lock()
	agent.releaseHeldTextLocked()
	agent.mu.Unlock()

	assert.Equal(t, []string{"Finished the list.\n\n", "STEERED\n\nMore.\n\n"}, publishedTexts(t, agent))
}

// Gaining or losing a message ID is a change of ID, so it starts a new message.
func TestACPStartsANewMessageWhenAMessageIDAppearsOrDisappears(t *testing.T) {
	agent := newDetachedACP(t)
	acpUpdate(t, agent, acpChunk("Untagged.\n\n", ""))
	acpUpdate(t, agent, acpChunk("Tagged.\n\n", "33333333-3333-4333-8333-333333333333"))
	acpUpdate(t, agent, acpChunk("Untagged again.\n\n", ""))
	acpUpdate(t, agent, acpChunk("Still untagged.\n\n", ""))
	agent.mu.Lock()
	agent.releaseHeldTextLocked()
	agent.mu.Unlock()

	assert.Equal(t, []string{"Untagged.\n\n", "Tagged.\n\n", "Untagged again.\n\nStill untagged.\n\n"}, publishedTexts(t, agent))
}

func TestACPKeepsThoughtsImagesAndPlans(t *testing.T) {
	agent := newDetachedACP(t)
	acpUpdate(t, agent, acpsdk.SessionUpdate{AgentThoughtChunk: &acpsdk.SessionUpdateAgentThoughtChunk{Content: acpsdk.TextBlock("Considering the options.\n\n")}})
	acpUpdate(t, agent, acpChunk("Here is the chart:\n\n", ""))
	acpUpdate(t, agent, acpsdk.SessionUpdate{AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{Content: acpsdk.ImageBlock("aW1hZ2U=", "image/png")}})
	large := strings.Repeat("A", 3<<20)
	acpUpdate(t, agent, acpsdk.SessionUpdate{AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{Content: acpsdk.ImageBlock(large, "image/png")}})
	acpUpdate(t, agent, acpsdk.SessionUpdate{Plan: &acpsdk.SessionUpdatePlan{Entries: []acpsdk.PlanEntry{
		{Content: "Read the parser", Priority: acpsdk.PlanEntryPriorityHigh, Status: acpsdk.PlanEntryStatusCompleted},
		{Content: "Fix the bug", Priority: acpsdk.PlanEntryPriorityMedium, Status: acpsdk.PlanEntryStatusInProgress},
	}}})

	state := publishedACPState(t, agent)
	require.Len(t, state.Messages, 4)
	assert.Equal(t, "thought", state.Messages[0].Role, "reasoning stays apart from the reply")
	assert.Equal(t, "Considering the options.\n\n", state.Messages[0].Text)
	assert.Equal(t, "assistant", state.Messages[1].Role)
	assert.Equal(t, "Here is the chart:\n\n", state.Messages[1].Text)
	require.NotNil(t, state.Messages[2].Content)
	assert.Equal(t, ACPContent{Type: "image", MimeType: "image/png", Data: "aW1hZ2U="}, *state.Messages[2].Content)
	require.NotNil(t, state.Messages[3].Content)
	assert.Len(t, state.Messages[3].Content.Data, len(large), "large media is kept whole")
	assert.Equal(t, []ACPPlanEntry{
		{Content: "Read the parser", Priority: "high", Status: "completed"},
		{Content: "Fix the bug", Priority: "medium", Status: "in_progress"},
	}, state.Plan)

	acpUpdate(t, agent, acpsdk.SessionUpdate{Plan: &acpsdk.SessionUpdatePlan{Entries: []acpsdk.PlanEntry{}}})
	assert.Empty(t, publishedACPState(t, agent).Plan, "each plan update replaces the plan")
}

func TestACPKeepsToolCallDetails(t *testing.T) {
	agent := newDetachedACP(t)
	line := 12
	oldText := "before"
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
		ToolCallId: "edit", Title: "Edit parser.go", Kind: acpsdk.ToolKindEdit, Status: acpsdk.ToolCallStatusInProgress,
		Locations: []acpsdk.ToolCallLocation{{Path: "/work/parser.go", Line: &line}},
		RawInput:  map[string]any{"path": "/work/parser.go"},
		Content: []acpsdk.ToolCallContent{
			{Diff: &acpsdk.ToolCallContentDiff{Path: "/work/parser.go", OldText: &oldText, NewText: "after"}},
		},
	}})
	output := []acpsdk.ToolCallContent{{Content: &acpsdk.ToolCallContentContent{Content: acpsdk.TextBlock("1 file changed")}}}
	completed := acpsdk.ToolCallStatusCompleted
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
		ToolCallId: "edit", Status: &completed, Content: output, RawOutput: map[string]any{"ok": true},
	}})

	state := publishedACPState(t, agent)
	require.Len(t, state.Messages, 1)
	tool := state.Messages[0]
	assert.Equal(t, "edit", tool.Kind)
	assert.Equal(t, "completed", tool.Status)
	assert.Equal(t, []ACPToolLocation{{Path: "/work/parser.go", Line: &line}}, tool.Locations, "fields an update omits are kept")
	assert.Equal(t, []ACPToolContent{{Type: "content", Content: &ACPContent{Type: "text", Text: "1 file changed"}}}, tool.ToolContent, "an update's content replaces the collection")
	assert.JSONEq(t, `{"path":"/work/parser.go"}`, tool.RawInput)
	assert.JSONEq(t, `{"ok":true}`, tool.RawOutput)
}

// Codex streams command output in tool call metadata under the Zed terminal
// conventions; a later content update must not drop what already streamed.
func TestACPCollectsStreamedCommandOutput(t *testing.T) {
	agent := newDetachedACP(t)
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
		ToolCallId: "cmd", Title: "go test ./...", Kind: acpsdk.ToolKindExecute, Status: acpsdk.ToolCallStatusInProgress,
		Content: []acpsdk.ToolCallContent{{Terminal: &acpsdk.ToolCallContentTerminal{TerminalId: "cmd"}}},
	}})
	for _, data := range []string{"ok  pkg/a\n", "ok  pkg/b\n"} {
		acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
			ToolCallId: "cmd", Meta: map[string]any{"terminal_output_delta": map[string]any{"terminal_id": "cmd", "data": data}},
		}})
	}
	completed := acpsdk.ToolCallStatusCompleted
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
		ToolCallId: "cmd", Status: &completed,
		Content: []acpsdk.ToolCallContent{{Terminal: &acpsdk.ToolCallContentTerminal{TerminalId: "cmd"}}},
		Meta:    map[string]any{"terminal_exit": map[string]any{"terminal_id": "cmd", "exit_code": float64(0), "signal": nil}},
	}})

	state := publishedACPState(t, agent)
	require.Len(t, state.Messages, 1)
	require.Len(t, state.Messages[0].ToolContent, 1)
	terminal := state.Messages[0].ToolContent[0]
	assert.Equal(t, "ok  pkg/a\nok  pkg/b\n", terminal.Output)
	require.NotNil(t, terminal.ExitCode)
	assert.Equal(t, 0, *terminal.ExitCode)
}

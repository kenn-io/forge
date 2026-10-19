package localruntime

import (
	"encoding/json/v2"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
)

func TestACPPageInvalidationUsesTheReturnedOffset(t *testing.T) {
	for _, tc := range []struct {
		name        string
		after       int
		edited      int
		invalidated bool
	}{
		{"edit before requested page but inside recent window", 20, 15, true},
		{"edit after requested page but before recent window", 5, 7, false},
		{"edit at requested offset belongs to the page", 7, 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := newDetachedACP(t)
			for i := range 210 {
				acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
					ToolCallId: acpsdk.ToolCallId(fmt.Sprintf("tool-%d", i)), Title: "Tool", Status: acpsdk.ToolCallStatusInProgress,
				}})
			}
			require.Equal(t, 10, publishedACPState(t, agent).MessageOffset)
			completed := acpsdk.ToolCallStatusCompleted
			acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{
				ToolCallId: acpsdk.ToolCallId(fmt.Sprintf("tool-%d", tc.edited)), Status: &completed,
			}})
			data, err := agent.Page(tc.after, 5)
			require.NoError(t, err)
			page := decodePage(t, data)
			assert.Equal(t, tc.after, page.MessageOffset)
			if tc.invalidated {
				assert.Equal(t, page.MessageRevision, page.HistoryRevision)
			} else {
				assert.Zero(t, page.HistoryRevision)
			}
		})
	}
}

func TestACPByteSizedBatchesPreserveTheTranscript(t *testing.T) {
	assert := assert.New(t)
	messages := make([]ACPMessage, 12)
	for i := range messages {
		// JSON escaping and multibyte text must count toward the wire size.
		messages[i] = ACPMessage{Role: "tool", Text: fmt.Sprintf("tool %d", i), RawOutput: strings.Repeat("\"\n界", 20_000)}
	}
	agent := &ACP{state: ACPState{Messages: messages, Connected: true}}
	data, err := agent.Snapshot()
	require.NoError(t, err)
	state := decodePage(t, data)
	assert.Less(len(data), 270_000)
	assert.Positive(state.MessageOffset)
	assert.Equal(12, state.MessageCount)
	assert.Equal(messages[state.MessageOffset:], state.Messages)

	// Repeated backward pages reach every older message without gaps.
	loaded := state.Messages
	for before := state.MessageOffset; before > 0; {
		data, err = agent.History(before, 100)
		require.NoError(t, err)
		var frame map[string]ACPHistory
		require.NoError(t, json.Unmarshal(data, &frame))
		page := frame["history"]
		require.Less(t, page.Offset, before)
		assert.Less(len(data), 270_000)
		assert.Equal(before, page.Offset+len(page.Messages))
		loaded = append(page.Messages, loaded...)
		before = page.Offset
	}
	assert.Equal(messages, loaded)

	// Forward pages used by coordinators also make complete progress.
	loaded = nil
	for after := 0; after < len(messages); {
		data, err = agent.Page(after, 100)
		require.NoError(t, err)
		page := decodePage(t, data)
		require.NotEmpty(t, page.Messages)
		assert.Less(len(data), 270_000)
		assert.Equal(after, page.MessageOffset)
		loaded = append(loaded, page.Messages...)
		after += len(page.Messages)
	}
	assert.Equal(messages, loaded)
	assert.Equal(messages, agent.state.Messages)
}

func TestACPBatchKeepsAnOversizedMessageWhole(t *testing.T) {
	assert := assert.New(t)
	large := ACPMessage{Role: "tool", Text: "result", RawOutput: strings.Repeat("x", 2<<20)}
	agent := &ACP{state: ACPState{Messages: []ACPMessage{{Role: "user", Text: "question"}, large}}}
	state := publishedACPState(t, agent)
	assert.Equal(1, state.MessageOffset)
	assert.Equal([]ACPMessage{large}, state.Messages)
	data, err := agent.History(2, 100)
	require.NoError(t, err)
	var frame map[string]ACPHistory
	require.NoError(t, json.Unmarshal(data, &frame))
	assert.Equal(1, frame["history"].Offset)
	assert.Equal([]ACPMessage{large}, frame["history"].Messages)
	data, err = agent.Page(1, 100)
	require.NoError(t, err)
	assert.Equal([]ACPMessage{large}, decodePage(t, data).Messages)
}

func decodePage(t *testing.T, data []byte) ACPState {
	t.Helper()
	var state ACPState
	require.NoError(t, json.Unmarshal(data, &state))
	return state
}

// A page is the published state with the transcript messages after a cursor,
// so a coordinator can read everything it has not seen yet.
func TestACPPageReturnsMessagesAfterACursor(t *testing.T) {
	assert := assert.New(t)
	messages := make([]ACPMessage, 250)
	for i := range messages {
		messages[i] = ACPMessage{Role: "assistant", Text: fmt.Sprintf("m%d", i)}
	}
	agent := &ACP{state: ACPState{Messages: messages, Busy: true, RuntimeGeneration: "generation"}}

	page := func(after, limit int) ACPState {
		t.Helper()
		data, err := agent.Page(after, limit)
		require.NoError(t, err)
		return decodePage(t, data)
	}
	state := page(10, 5)
	assert.Equal(250, state.MessageCount)
	assert.Equal(10, state.MessageOffset)
	require.Len(t, state.Messages, 5)
	assert.Equal("m10", state.Messages[0].Text)
	assert.Equal("m14", state.Messages[4].Text)
	assert.True(state.Busy, "a page carries the rest of the published state")
	assert.Equal("generation", state.RuntimeGeneration)

	state = page(0, 0)
	require.Len(t, state.Messages, acpHistoryPage, "a page without a limit has the default size")
	assert.Equal("m0", state.Messages[0].Text)

	state = page(200, 500)
	assert.Equal(200, state.MessageOffset)
	require.Len(t, state.Messages, 50)
	assert.Equal("m249", state.Messages[49].Text)

	state = page(240, math.MaxInt)
	require.Len(t, state.Messages, 10, "a huge limit reads to the end")

	state = page(400, 10)
	assert.Equal(250, state.MessageOffset, "a cursor past the end reads nothing")
	assert.Empty(state.Messages)
	assert.Equal(250, state.MessageCount)
	assert.Len(agent.state.Messages, 250, "paging must not change the transcript")
}

// A page counts only published messages, like snapshots and history.
func TestACPPageCountsOnlyPublishedMessages(t *testing.T) {
	agent := &ACP{state: ACPState{Messages: []ACPMessage{{Role: "user", Text: "question"}, {Role: "assistant", Text: "held"}}}, heldBytes: len("held")}
	data, err := agent.Page(0, 10)
	require.NoError(t, err)
	state := decodePage(t, data)
	assert.Equal(t, 1, state.MessageCount)
	require.Len(t, state.Messages, 1)
	assert.Equal(t, "question", state.Messages[0].Text)
}

// Pages cross the owner RPC like snapshots do.
func TestACPPageReadsTheOwnersTranscript(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	dir := t.TempDir()
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
	executable, err := os.Executable()
	require.NoError(t, err)
	manager := newACPTestManager(t, Options{
		ACPSessionsDir: filepath.Join(dir, "acp"),
		Targets:        ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil),
	})
	info, err := manager.Launch(t.Context(), "workspace", dir, "chat")
	require.NoError(t, err)
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "remember", ID: "first"}))
	require.Eventually(t, func() bool {
		data, err := agent.Snapshot()
		var state ACPState
		return err == nil && json.Unmarshal(data, &state) == nil && !state.Busy && len(state.Messages) == 2
	}, 5*time.Second, 10*time.Millisecond)

	data, err := agent.Page(1, 10)
	require.NoError(t, err)
	state := decodePage(t, data)
	assert.Equal(t, 2, state.MessageCount)
	assert.Equal(t, 1, state.MessageOffset)
	require.Len(t, state.Messages, 1)
	assert.Equal(t, "Hello workspace", state.Messages[0].Text)
	assert.True(t, state.Connected)
}

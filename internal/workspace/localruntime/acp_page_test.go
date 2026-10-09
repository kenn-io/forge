package localruntime

import (
	"encoding/json/v2"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
)

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

	// An owner must survive any limit an internal caller passes.
	data, err = agent.Page(1, math.MaxInt)
	require.NoError(t, err)
	require.Len(t, decodePage(t, data).Messages, 1)
}

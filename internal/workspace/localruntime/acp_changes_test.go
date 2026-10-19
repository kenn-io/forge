package localruntime

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type acpUpdateReader interface {
	Updates(uint64) (ACPBatch, error)
}

func decodedACPUpdates(t *testing.T, agent acpUpdateReader, after uint64) (ACPChanges, error) {
	t.Helper()
	batch, err := agent.Updates(after)
	if err != nil {
		return ACPChanges{}, err
	}
	var frame struct {
		Updates ACPChanges `json:"updates"`
	}
	require.NoError(t, json.Unmarshal(batch.Data, &frame))
	require.Equal(t, batch.MessageCursor, frame.Updates.MessageCursor)
	require.Equal(t, batch.More, frame.Updates.More)
	return frame.Updates, nil
}

func TestACPUpdatesCoalesceChangesAcrossBatches(t *testing.T) {
	agent := &ACP{}
	for _, text := range []string{"first", "second", "third"} {
		agent.insertMessageLocked(len(agent.state.Messages), ACPMessage{Role: "tool", Text: text, RawOutput: strings.Repeat("x", 150_000)})
	}
	first, err := decodedACPUpdates(t, agent, 0)
	require.NoError(t, err)
	require.Len(t, first.Messages, 1)
	require.True(t, first.More)
	// An already delivered entry and a pending entry both change while the
	// client drains. The next batches must contain their latest versions.
	agent.state.Messages[0].Text = "first updated"
	agent.publishMessageLocked(0)
	agent.state.Messages[1].Text = "second intermediate"
	agent.publishMessageLocked(1)
	agent.state.Messages[1].Text = "second final"
	agent.publishMessageLocked(1)
	loaded := map[string]ACPMessage{first.Messages[0].ID: first.Messages[0]}
	cursor := first.MessageCursor
	for {
		batch, err := decodedACPUpdates(t, agent, cursor)
		require.NoError(t, err)
		require.Len(t, batch.Messages, 1)
		for _, message := range batch.Messages {
			loaded[message.ID] = message
		}
		cursor = batch.MessageCursor
		if !batch.More {
			break
		}
	}
	require.Len(t, loaded, 3)
	for _, expected := range agent.state.Messages {
		assert.Equal(t, expected, loaded[expected.ID])
	}
	batch, err := decodedACPUpdates(t, agent, cursor)
	require.NoError(t, err)
	assert.Empty(t, batch.Messages)
	assert.False(t, batch.More)
}

func TestACPRestoreKeepsIDsAndReplacesChangeIndex(t *testing.T) {
	agent := &ACP{}
	agent.insertMessageLocked(0, ACPMessage{Role: "user", Text: "startup"})
	saved := ACPState{Messages: []ACPMessage{
		{ID: "first", Version: 8, Role: "user", Text: "saved question"},
		{ID: "second", PreviousID: "first", Version: 10, Role: "assistant", Text: "saved answer"},
	}}
	agent.restoreTranscriptLocked(saved)
	agent.state.Messages[0].Text = "updated"
	agent.publishMessageLocked(0)
	changes, err := decodedACPUpdates(t, agent, 10)
	require.NoError(t, err)
	require.Len(t, changes.Messages, 1)
	assert.Equal(t, "first", changes.Messages[0].ID)
	assert.Equal(t, uint64(11), changes.Messages[0].Version)
	data, err := agent.History("second", 100)
	require.NoError(t, err)
	var frame map[string]ACPHistory
	require.NoError(t, json.Unmarshal(data, &frame))
	require.Len(t, frame["history"].Messages, 1)
	assert.Equal(t, "first", frame["history"].Messages[0].ID)
}

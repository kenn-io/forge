package localruntime

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func savedBytes(t *testing.T, recordPath string) int64 {
	t.Helper()
	var total int64
	for _, path := range []string{recordPath, acpJournalPath(recordPath)} {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		total += info.Size()
	}
	return total
}

func savedToolStatuses(t *testing.T, recordPath string) map[string]string {
	t.Helper()
	statuses := map[string]string{}
	for _, message := range readSavedSession(t, recordPath).State.Messages {
		if message.ToolCallID != "" {
			statuses[message.ToolCallID] = message.Status
		}
	}
	return statuses
}

// Saving a tool update in a long chat writes the update, not the transcript.
// A chat that had read large tool output used to rewrite all of it on every
// update and saturate the disk.
func TestACPSaveWritesWhatChangedNotTheTranscript(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.recordPath = filepath.Join(t.TempDir(), "session.json")
		require.NoError(t, agent.Prompt("work", ACPSender{Kind: ACPSenderUser}))
		acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
			ToolCallId: "big", Title: string(bytes.Repeat([]byte("x"), 4<<20)),
		}})
		// The journal now outgrows the snapshot, so this save folds it in.
		acpToolStatus(t, agent, "big", acpsdk.ToolCallStatusCompleted)
		before := savedBytes(t, agent.recordPath)

		for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
			acpToolCall(t, agent, id, acpsdk.ToolCallStatusInProgress)
			acpToolStatus(t, agent, id, acpsdk.ToolCallStatusCompleted)
		}
		data, err := os.ReadFile(agent.recordPath)
		require.NoError(t, err)
		var snapshot acpSavedSession
		require.NoError(t, json.Unmarshal(data, &snapshot))
		for _, message := range snapshot.State.Messages {
			assert.NotEqual(t, "h", message.ToolCallID, "small updates do not rewrite the four-megabyte snapshot")
		}
		assert.Less(t, savedBytes(t, agent.recordPath)-before, int64(64<<10),
			"sixteen small updates to a four-megabyte chat append kilobytes")

		statuses := savedToolStatuses(t, agent.recordPath)
		assert.Len(t, statuses, 9)
		assert.Equal(t, "completed", statuses["h"], "the latest version of each entry is saved")
		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
	})
}

// The saved transcript reads back exactly as the chat holds it, and the
// journal is folded into a new snapshot once it outgrows the old one.
func TestACPSavedChatReadsBackAfterJournalAndCompaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.recordPath = filepath.Join(t.TempDir(), "session.json")
		require.NoError(t, agent.Prompt("work", ACPSender{Kind: ACPSenderUser}))
		output := string(bytes.Repeat([]byte("y"), 300<<10))
		for _, id := range []string{"one", "two", "three", "four", "five"} {
			acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{ToolCallId: acpsdk.ToolCallId(id), Title: id + output}})
			acpToolStatus(t, agent, id, acpsdk.ToolCallStatusCompleted)
		}
		acpText(t, agent, "All done.")
		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()

		journal, err := os.Stat(acpJournalPath(agent.recordPath))
		if err == nil {
			assert.Less(t, journal.Size(), int64(1<<20), "a journal past its threshold was folded into a snapshot")
		} else {
			require.ErrorIs(t, err, os.ErrNotExist)
		}
		// Saving round-trips through JSON, which keeps no difference between
		// an empty and an absent list, so compare the encodings.
		agent.mu.Lock()
		want, err := json.Marshal(agent.state.Messages)
		agent.mu.Unlock()
		require.NoError(t, err)
		got, err := json.Marshal(readSavedSession(t, agent.recordPath).State.Messages)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(want, got), "the saved transcript reads back as the chat holds it")
	})
}

// An entry inserted before another reads back in its place, though the
// journal records it after the entry it precedes.
func TestACPJournalRestoresInsertedEntriesInPlace(t *testing.T) {
	saved := acpSavedSession{Base: "snapshot", State: ACPState{Messages: []ACPMessage{
		{ID: "a", Version: 1, Text: "a"},
		{ID: "b", PreviousID: "a", Version: 2, Text: "b"},
	}}}
	record := acpJournalRecord{Base: "snapshot", Messages: []ACPMessage{
		{ID: "b", PreviousID: "c", Version: 4, Text: "b"},
		{ID: "c", PreviousID: "a", Version: 3, Text: "c"},
	}}
	line, err := json.Marshal(record)
	require.NoError(t, err)
	applyACPJournal(&saved, append(line, '\n'))
	var order []string
	for _, message := range saved.State.Messages {
		order = append(order, message.ID)
	}
	assert.Equal(t, []string{"a", "c", "b"}, order)
}

// A crash can leave journal records of an older snapshot, when it struck
// between writing a snapshot and removing the journal, or a half-written last
// record. Neither changes the saved chat.
func TestACPJournalSkipsWhatACrashLeftBehind(t *testing.T) {
	saved := acpSavedSession{Base: "new", State: ACPState{Messages: []ACPMessage{{ID: "a", Version: 5, Text: "current"}}}}
	stale, err := json.Marshal(acpJournalRecord{Base: "old", Messages: []ACPMessage{{ID: "a", Version: 2, Text: "stale"}}})
	require.NoError(t, err)
	current, err := json.Marshal(acpJournalRecord{Base: "new", Messages: []ACPMessage{{ID: "b", PreviousID: "a", Version: 6, Text: "kept"}}})
	require.NoError(t, err)
	torn, err := json.Marshal(acpJournalRecord{Base: "new", Messages: []ACPMessage{{ID: "c", PreviousID: "b", Version: 7, Text: "torn"}}})
	require.NoError(t, err)
	journal := string(stale) + "\n" + string(current) + "\n" + string(torn[:len(torn)/2])

	applyACPJournal(&saved, []byte(journal))
	var texts []string
	for _, message := range saved.State.Messages {
		texts = append(texts, message.Text)
	}
	assert.Equal(t, []string{"current", "kept"}, texts)
}

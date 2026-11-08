package localruntime

import (
	"encoding/json/v2"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingChat stands in for a started chat and records its commands.
type recordingChat struct {
	ACPChat
	mu       sync.Mutex
	commands []ACPCommand
}

func (c *recordingChat) Command(command ACPCommand) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commands = append(c.commands, command)
	return nil
}

func dormantState(t *testing.T, chat *DormantACP) ACPState {
	t.Helper()
	data, err := chat.Snapshot()
	require.NoError(t, err)
	var state ACPState
	require.NoError(t, json.Unmarshal(data, &state))
	return state
}

// A dormant chat shows its saved transcript and queue as a reload would, but
// not requests its stopped agent can no longer answer.
func TestDormantACPShowsTheSavedChatAsAReloadWould(t *testing.T) {
	saved := ACPState{
		Messages:    []ACPMessage{{ID: "m1", Role: "assistant", Text: "Saved reply"}},
		Queue:       []ACPQueuedPrompt{{ID: "q1", Text: "next", Sender: ACPSender{Kind: ACPSenderUser}}},
		Permissions: []ACPPermission{{ID: "old-generation:1", Title: "Run tests"}},
		Busy:        true,
		Connected:   true,
	}
	chat := newDormantACP(saved, func() (ACPChat, error) { return nil, errors.New("not started") })

	state := dormantState(t, chat)
	assert.True(t, state.Dormant)
	assert.False(t, state.Connected)
	assert.False(t, state.Busy)
	assert.Empty(t, state.Permissions)
	require.Len(t, state.Messages, 1)
	assert.Equal(t, "Saved reply", state.Messages[0].Text)
	require.Len(t, state.Queue, 1)
	assert.True(t, state.QueuePaused, "a restored queue never drains on its own")
}

// Commands that need the agent start it once, even when they race, and then
// run on the started chat. Cancelling starts nothing.
func TestDormantACPStartsItsAgentOnceForCommands(t *testing.T) {
	live := &recordingChat{}
	var starts atomic.Int32
	release := make(chan struct{})
	chat := newDormantACP(ACPState{}, func() (ACPChat, error) {
		starts.Add(1)
		<-release
		return live, nil
	})

	require.NoError(t, chat.Command(ACPCommand{Type: "cancel"}))
	assert.Zero(t, starts.Load(), "cancelling a chat with no running agent started it")

	var commands sync.WaitGroup
	for _, id := range []string{"p1", "p2"} {
		commands.Go(func() {
			assert.NoError(t, chat.Command(ACPCommand{Type: "prompt", ID: id}))
		})
	}
	require.Eventually(t, func() bool { return dormantState(t, chat).Starting }, 2*time.Second, 5*time.Millisecond)
	close(release)
	commands.Wait()

	assert.Equal(t, int32(1), starts.Load())
	<-chat.Started()
	assert.Same(t, live, chat.Live())
	live.mu.Lock()
	defer live.mu.Unlock()
	require.Len(t, live.commands, 2)
	assert.ElementsMatch(t, []string{"p1", "p2"}, []string{live.commands[0].ID, live.commands[1].ID})
}

// A failed start is returned to the command, whose sender shows it beside the
// refused prompt, not raised again as a chat-wide error; the next command tries
// again.
func TestDormantACPReportsAFailedStartAndRetries(t *testing.T) {
	live := &recordingChat{}
	attempts := 0
	chat := newDormantACP(ACPState{}, func() (ACPChat, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("agent refused")
		}
		return live, nil
	})

	err := chat.Command(ACPCommand{Type: "prompt", ID: "p1"})
	require.ErrorContains(t, err, "agent refused")
	state := dormantState(t, chat)
	assert.False(t, state.Starting)
	assert.Empty(t, state.Error)

	require.NoError(t, chat.Command(ACPCommand{Type: "prompt", ID: "p1"}))
	assert.Equal(t, 2, attempts)
}

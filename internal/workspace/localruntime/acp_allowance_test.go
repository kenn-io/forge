package localruntime

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedPeer is the agent end of a detached chat. It records what the chat
// sends and answers prompts only when a test asks it to.
type scriptedPeer struct {
	t      *testing.T
	writer *io.PipeWriter
	mu     sync.Mutex
	sent   []byte
}

func (p *scriptedPeer) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, data...)
	return len(data), nil
}

func (*scriptedPeer) Close() error { return nil }

// messages returns the JSON-RPC messages the chat sent with this method.
func (p *scriptedPeer) messages(method string) []jsontext.Value {
	p.t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	var ids []jsontext.Value
	for line := range bytes.Lines(p.sent) {
		var message struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
		}
		require.NoError(p.t, json.Unmarshal(line, &message))
		if message.Method == method {
			ids = append(ids, message.ID)
		}
	}
	return ids
}

func (p *scriptedPeer) cancels() int { return len(p.messages(acpsdk.AgentMethodSessionCancel)) }

// answer responds to the newest request with this method.
func (p *scriptedPeer) answer(method, result string) {
	p.t.Helper()
	requests := p.messages(method)
	require.NotEmpty(p.t, requests)
	_, err := fmt.Fprintf(p.writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", requests[len(requests)-1], result)
	require.NoError(p.t, err)
}

// endTurn answers the newest prompt.
func (p *scriptedPeer) endTurn(stopReason acpsdk.StopReason) {
	p.t.Helper()
	p.answer(acpsdk.AgentMethodSessionPrompt, fmt.Sprintf(`{"stopReason":%q}`, stopReason))
}

func newScriptedACP(t *testing.T) (*ACP, *scriptedPeer) {
	t.Helper()
	peerReader, peerWriter := io.Pipe()
	peer := &scriptedPeer{t: t, writer: peerWriter}
	agent := &ACP{
		stdin: peer, done: make(chan struct{}), sessionID: "session",
		permissions:  make(map[string]chan acpsdk.RequestPermissionOutcome),
		elicitations: make(map[string]chan acpsdk.UnstableCreateElicitationResponse),
		subscribers:  make(map[chan struct{}]struct{}),
		state:        ACPState{Connected: true},
	}
	agent.client = acpsdk.NewClientSideConnection(agent, agent, peerReader)
	t.Cleanup(func() {
		_ = peerWriter.Close()
		_ = peerReader.Close()
		select {
		case <-agent.done:
		default:
			close(agent.done)
		}
	})
	return agent, peer
}

func acpToolCall(t *testing.T, agent *ACP, id string, status acpsdk.ToolCallStatus) {
	t.Helper()
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{ToolCallId: acpsdk.ToolCallId(id), Title: id, Status: status}})
}

func acpToolStatus(t *testing.T, agent *ACP, id string, status acpsdk.ToolCallStatus) {
	t.Helper()
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{ToolCallId: acpsdk.ToolCallId(id), Status: &status}})
}

// askPermission makes the agent ask to run toolCall and returns the request
// the chat shows. The request stays pending until it is answered or the
// turn ends.
func askPermission(t *testing.T, agent *ACP, toolCall string) ACPPermission {
	t.Helper()
	go func() {
		_, _ = agent.RequestPermission(t.Context(), acpsdk.RequestPermissionRequest{
			SessionId: "session",
			ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: acpsdk.ToolCallId(toolCall)},
			Options:   []acpsdk.PermissionOption{{OptionId: "allow", Name: "Allow", Kind: acpsdk.PermissionOptionKindAllowOnce}},
		})
	}()
	synctest.Wait()
	permissions := publishedACPState(t, agent).Permissions
	require.Len(t, permissions, 1)
	return permissions[0]
}

func runningTurn(t *testing.T, agent *ACP) ACPTurnRecord {
	t.Helper()
	turns := publishedACPState(t, agent).Turns
	require.NotEmpty(t, turns)
	return turns[len(turns)-1]
}

// advance moves the fake clock and lets the turn clock handle every tick.
func advance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

// Every test here runs in a synctest bubble, which fails when a goroutine is
// left behind; a turn clock that outlived its turn would fail the test.
func TestACPAllowanceCancelsAnExhaustedTurnOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.recordPath = filepath.Join(t.TempDir(), "session.json")
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "bounded", Generation: 1, AllowanceMillis: 2000}))

		advance(1500 * time.Millisecond)
		assert.Zero(t, peer.cancels())
		assert.False(t, runningTurn(t, agent).Exhausted)

		advance(time.Second)
		require.Equal(t, 1, peer.cancels())
		state := publishedACPState(t, agent)
		assert.True(t, state.Stopping, "exhaustion stops the turn like the cancel command")
		assert.True(t, state.QueuePaused)
		record := runningTurn(t, agent)
		assert.Equal(t, int64(2000), record.Allowance)
		assert.Equal(t, int64(2000), record.ActiveMillis)
		assert.True(t, record.Exhausted)
		assert.True(t, readSavedSession(t, agent.recordPath).State.Turns[0].Exhausted, "exhaustion is saved")

		advance(time.Minute)
		assert.Equal(t, 1, peer.cancels(), "an exhausted turn is cancelled once")

		peer.endTurn(acpsdk.StopReasonCancelled)
		synctest.Wait()
		state = publishedACPState(t, agent)
		assert.False(t, state.Busy)
		require.Len(t, state.Turns, 1)
		ended := state.Turns[0]
		assert.NotEmpty(t, ended.EndedAt)
		assert.Equal(t, string(acpsdk.StopReasonCancelled), ended.StopReason)
		assert.True(t, ended.Exhausted)
		assert.Equal(t, int64(62500), ended.ActiveMillis, "the turn counts until it ends")
		assert.Equal(t, ended, readSavedSession(t, agent.recordPath).State.Turns[0])
	})
}

func TestACPTurnWithoutAllowanceIsNeverCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		require.NoError(t, agent.Prompt("work"))

		advance(time.Hour)
		assert.Zero(t, peer.cancels())
		record := runningTurn(t, agent)
		assert.Equal(t, int64(time.Hour/time.Millisecond), record.ActiveMillis)
		assert.Zero(t, record.Allowance)
		assert.False(t, record.Exhausted)
		assert.True(t, publishedACPState(t, agent).Busy)

		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
		assert.False(t, publishedACPState(t, agent).Busy)
	})
}

// A turn waiting only for a person to answer is blocked, and blocked time is
// not active time.
func TestACPBlockedTurnStopsItsClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		// A tool call an earlier turn never finished is not this turn's work.
		acpToolCall(t, agent, "stale", acpsdk.ToolCallStatusInProgress)
		require.NoError(t, agent.Prompt("work"))
		assert.False(t, publishedACPState(t, agent).Blocked)

		advance(2500 * time.Millisecond)
		// Agents announce the tool call they ask permission to run.
		acpToolCall(t, agent, "edit", acpsdk.ToolCallStatusPending)
		permission := askPermission(t, agent, "edit")
		assert.True(t, publishedACPState(t, agent).Blocked)
		assert.Equal(t, int64(2500), runningTurn(t, agent).ActiveMillis)

		advance(10 * time.Second)
		assert.True(t, publishedACPState(t, agent).Blocked)
		assert.Equal(t, int64(2500), runningTurn(t, agent).ActiveMillis, "waiting for a person is not active time")

		require.NoError(t, agent.Command(ACPCommand{Type: "permission", ID: permission.ID, OptionID: "allow"}))
		synctest.Wait()
		assert.False(t, publishedACPState(t, agent).Blocked)
		advance(1700 * time.Millisecond)
		assert.Equal(t, int64(4000), runningTurn(t, agent).ActiveMillis, "the clock resumes after the answer")

		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
		state := publishedACPState(t, agent)
		assert.False(t, state.Blocked)
		assert.Equal(t, int64(4200), state.Turns[0].ActiveMillis)
	})
}

// A turn with other work running is not blocked by a question, and becomes
// blocked when that work finishes.
func TestACPRequestBesideRunningToolIsNotBlocked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		require.NoError(t, agent.Prompt("work"))
		acpToolCall(t, agent, "build", acpsdk.ToolCallStatusInProgress)
		acpToolCall(t, agent, "edit", acpsdk.ToolCallStatusPending)
		permission := askPermission(t, agent, "edit")
		assert.False(t, publishedACPState(t, agent).Blocked)

		advance(3 * time.Second)
		assert.Equal(t, int64(3000), runningTurn(t, agent).ActiveMillis)

		acpToolStatus(t, agent, "build", acpsdk.ToolCallStatusCompleted)
		assert.True(t, publishedACPState(t, agent).Blocked)

		require.NoError(t, agent.Command(ACPCommand{Type: "permission", ID: permission.ID, OptionID: "allow"}))
		synctest.Wait()
		assert.False(t, publishedACPState(t, agent).Blocked, "an allowed tool call is pending until it runs")
		acpToolStatus(t, agent, "edit", acpsdk.ToolCallStatusCompleted)

		go func() {
			_, _ = agent.UnstableCreateElicitation(t.Context(), acpsdk.UnstableCreateElicitationRequest{
				Form: &acpsdk.UnstableCreateElicitationForm{Message: "Pick one"},
			})
		}()
		synctest.Wait()
		assert.True(t, publishedACPState(t, agent).Blocked, "an elicitation blocks the turn too")

		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
		state := publishedACPState(t, agent)
		assert.False(t, state.Blocked)
		assert.Empty(t, state.Elicitations)
	})
}

// A subagent's tool call that asks for permission blocks the turn: the
// subagent tool call it runs under waits on the same answer.
func TestACPSubagentPermissionBlocksTheTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		require.NoError(t, agent.Prompt("work"))
		subagentToolCall := func(id, parent string, status acpsdk.ToolCallStatus) {
			claude := map[string]any{"toolName": "Read", "parentToolUseId": parent}
			if parent == "" {
				claude = map[string]any{"toolName": "Agent"}
			}
			acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
				ToolCallId: acpsdk.ToolCallId(id), Title: id, Status: status, Meta: map[string]any{"claudeCode": claude},
			}})
		}
		subagentToolCall("agent-1", "", acpsdk.ToolCallStatusInProgress)
		subagentToolCall("child-1", "agent-1", acpsdk.ToolCallStatusPending)

		advance(1500 * time.Millisecond)
		askPermission(t, agent, "child-1")
		assert.True(t, publishedACPState(t, agent).Blocked)
		advance(10 * time.Second)
		assert.Equal(t, int64(1500), runningTurn(t, agent).ActiveMillis, "waiting for a person is not active time")

		// Another tool call of the same subagent is still work.
		subagentToolCall("child-2", "agent-1", acpsdk.ToolCallStatusInProgress)
		assert.False(t, publishedACPState(t, agent).Blocked)

		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
	})
}

// A takeover gives the running turn to a person, so the supervisor's
// allowance can no longer cancel it.
func TestACPTakeoverDisarmsTheAllowance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.recordPath = filepath.Join(t.TempDir(), "session.json")
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "bounded", Generation: 1, AllowanceMillis: 2000}))

		advance(1500 * time.Millisecond)
		require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
		assert.Zero(t, readSavedSession(t, agent.recordPath).State.Turns[0].Allowance, "the disarmed turn is saved")
		advance(time.Minute)
		assert.Zero(t, peer.cancels())
		record := runningTurn(t, agent)
		assert.Zero(t, record.Allowance)
		assert.False(t, record.Exhausted)

		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
	})
}

// A turn the agent starts after a person steers the taken-over turn is the
// same turn, and the supervisor's allowance does not cancel it either.
func TestACPTakeoverDisarmsTheAllowanceOfASteerStartedTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.state.SteeringSupported = true
		agent.reportsThreadStatus = true
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "bounded", Generation: 1, AllowanceMillis: 2000}))
		advance(500 * time.Millisecond)
		require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))

		steered := make(chan error, 1)
		go func() {
			steered <- agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "steer"})
		}()
		synctest.Wait()
		peer.answer(acpSteeringMethod, `{"outcome":"startedNewTurn"}`)
		require.NoError(t, <-steered)
		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
		require.True(t, publishedACPState(t, agent).Busy, "the agent's own turn runs on")

		advance(time.Minute)
		assert.Zero(t, peer.cancels())
		state := publishedACPState(t, agent)
		require.Len(t, state.Turns, 1)
		assert.False(t, state.Turns[0].Exhausted)

		acpUpdate(t, agent, acpsdk.SessionUpdate{SessionInfoUpdate: &acpsdk.SessionSessionInfoUpdate{
			Meta: map[string]any{"codex": map[string]any{"threadStatus": map[string]any{"type": "idle"}}},
		}})
		synctest.Wait()
		assert.False(t, publishedACPState(t, agent).Busy)
	})
}

// A turn someone already stopped is not cancelled again when its allowance
// runs out, and it is not marked as exhausted.
func TestACPAllowanceSkipsAStoppedTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "bounded", Generation: 1, AllowanceMillis: 2000}))
		advance(500 * time.Millisecond)
		require.NoError(t, agent.Command(ACPCommand{Type: "cancel"}))

		advance(time.Minute)
		assert.Equal(t, 1, peer.cancels())
		assert.False(t, runningTurn(t, agent).Exhausted)

		peer.endTurn(acpsdk.StopReasonCancelled)
		synctest.Wait()
	})
}

// The clock stops counting once the agent process is gone.
func TestACPTurnClockStopsWhenTheAgentExits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, _ := newScriptedACP(t)
		require.NoError(t, agent.Prompt("work"))
		advance(1500 * time.Millisecond)
		close(agent.done)
		advance(10 * time.Second)
		assert.Equal(t, int64(1000), runningTurn(t, agent).ActiveMillis)
	})
}

// A turn's count starts at zero even if an earlier clock was still counting.
func TestACPNewTurnClockStartsFromZero(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, _ := newScriptedACP(t)
		agent.mu.Lock()
		agent.state.Busy = true
		agent.beginTurnRecordLocked("first", 0, 0)
		agent.mu.Unlock()
		advance(1500 * time.Millisecond)

		agent.mu.Lock()
		agent.beginTurnRecordLocked("second", 0, 0)
		agent.mu.Unlock()
		assert.Zero(t, runningTurn(t, agent).ActiveMillis)
		advance(1500 * time.Millisecond)
		assert.Equal(t, int64(1000), runningTurn(t, agent).ActiveMillis)

		agent.mu.Lock()
		agent.endTurnRecordLocked("end_turn", nil)
		agent.mu.Unlock()
	})
}

// The turn clock saves its progress every ten seconds, and the final count
// when the turn ends.
func TestACPTurnClockSavesPeriodically(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.recordPath = filepath.Join(t.TempDir(), "session.json")
		require.NoError(t, agent.Prompt("work"))
		saved := func() int64 { return readSavedSession(t, agent.recordPath).State.Turns[0].ActiveMillis }

		advance(9500 * time.Millisecond)
		assert.Equal(t, int64(9000), runningTurn(t, agent).ActiveMillis)
		assert.Zero(t, saved())

		advance(time.Second)
		assert.Equal(t, int64(10000), saved())

		advance(5 * time.Second)
		assert.Equal(t, int64(10000), saved())

		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
		assert.Equal(t, int64(15500), saved())
	})
}

func TestACPTurnClockUsesTheConfiguredTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.allowanceTick = 100 * time.Millisecond
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "bounded", Generation: 1, AllowanceMillis: 250}))

		advance(350 * time.Millisecond)
		assert.Equal(t, 1, peer.cancels())
		assert.Equal(t, int64(300), runningTurn(t, agent).ActiveMillis)

		peer.endTurn(acpsdk.StopReasonCancelled)
		synctest.Wait()
	})
}

func TestACPAllowanceIsOnlyForSupervisedPrompts(t *testing.T) {
	agent := newDetachedACP(t)
	require.ErrorContains(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "person", AllowanceMillis: 1000}),
		"only a supervisor's prompts may set an allowance")

	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
	require.ErrorContains(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "negative", Generation: 1, AllowanceMillis: -1}),
		"allowance must be positive")

	require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
	require.ErrorContains(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "person", AllowanceMillis: 1000}),
		"only a supervisor's prompts may set an allowance")

	state := publishedACPState(t, agent)
	assert.False(t, state.Busy)
	assert.Empty(t, state.Messages)
	assert.Empty(t, state.Turns)
}

// A permission request for the tool call it announced blocks the turn on the
// wire, and the answer unblocks it.
func TestACPFixturePermissionBlocksTheTurn(t *testing.T) {
	c := launchRequestChat(t)
	permission := c.askPermission("blocked")
	c.awaitState(func(s ACPState) bool { return s.Blocked })

	require.NoError(t, c.chat.Command(ACPCommand{Type: "permission", ID: permission.ID, OptionID: "allow"}))
	state := c.awaitIdle()
	assert.False(t, state.Blocked)
}

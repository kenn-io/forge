package localruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readSavedSession(t *testing.T, path string) acpSavedSession {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var saved acpSavedSession
	require.NoError(t, json.Unmarshal(data, &saved))
	return saved
}

// inProcessChat starts ACP agents on the stdio fixture in this process, so
// tests can reach the owner's internals.
type inProcessChat struct {
	t       *testing.T
	manager *Manager
	info    SessionInfo
	command []string
	cwd     string
}

func newInProcessChat(t *testing.T) *inProcessChat {
	t.Helper()
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	c := &inProcessChat{
		t:       t,
		manager: NewManager(Options{ACPSessionsDir: filepath.Join(t.TempDir(), "acp")}),
		info:    SessionInfo{Key: "chat", WorkspaceID: "workspace", TargetKey: "chat", Kind: LaunchTargetACP},
		command: []string{executable, "-test.run=^TestACPStdioHelper$"},
		cwd:     cwd,
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(c.recordPath()), 0o700))
	return c
}

func (c *inProcessChat) recordPath() string { return c.manager.acpSessionPath(c.info.Key) }

func (c *inProcessChat) start(saved *acpSavedSession) *ACP {
	c.t.Helper()
	agent, err := c.manager.startACP(c.t.Context(), c.info, c.command, c.cwd, nil, saved, nil)
	require.NoError(c.t, err)
	c.t.Cleanup(func() { _ = agent.Stop(context.Background()) })
	return agent
}

// An accepted prompt always runs. An owner that crashes after recording a
// prompt as sending, but before recording its message, leaves it for the
// replacement owner to queue again.
func TestACPPromptSendingAtCrashRunsAfterRestore(t *testing.T) {
	c := newInProcessChat(t)
	recordPath := c.recordPath()
	first := c.start(nil)
	var atCrash []byte
	var err error
	first.beforePromptWrite = func() {
		// session.json now holds what a crashed owner would leave behind.
		atCrash, err = os.ReadFile(recordPath)
		require.NoError(t, err)
		require.NoError(t, first.cmd.Process.Kill())
		<-first.Done()
	}
	require.NoError(t, first.Command(ACPCommand{Type: "prompt", Text: "kept", ID: "crash"}), "the prompt was accepted")
	kept := ACPQueuedPrompt{ID: "crash", Text: "kept", Sender: ACPSender{Kind: ACPSenderUser}}
	live := readSavedSession(t, recordPath).State
	assert.Nil(t, live.Sending, "a failed write is not in flight")
	assert.Equal(t, []ACPQueuedPrompt{kept}, live.Queue, "a prompt whose write failed waits at the head of the queue")
	assert.True(t, live.QueuePaused)

	var saved acpSavedSession
	require.NoError(t, json.Unmarshal(atCrash, &saved))
	require.Equal(t, &kept, saved.State.Sending)
	require.Empty(t, saved.State.Queue)
	second := c.start(&saved)

	state := publishedACPState(t, second)
	assert.Equal(t, []ACPQueuedPrompt{kept}, state.Queue)
	assert.True(t, state.QueuePaused, "a reloaded conversation never starts queued work on its own")
	assert.Nil(t, state.Sending)
	require.NoError(t, second.Command(ACPCommand{Type: "prompt", Text: "kept", ID: "crash"}), "a retry is the same submission")
	require.NoError(t, second.Command(ACPCommand{Type: "resume"}))
	require.Eventually(t, func() bool {
		state = publishedACPState(t, second)
		return !state.Busy && len(state.Queue) == 0 && len(state.Messages) > 0
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"crash"}, userSubmissions(state))
}

// A steer is saved when it is accepted, before the agent sees it. An owner
// that crashes before the agent took it leaves it at the head of the
// replacement's queue.
func TestACPSteerAcceptedBeforeACrashRunsAfterRestore(t *testing.T) {
	t.Setenv("KENN_FORGE_ACP_STEERING", "1")
	c := newInProcessChat(t)
	recordPath := c.recordPath()
	first := c.start(nil)
	require.NoError(t, first.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "turn"}))
	require.True(t, publishedACPState(t, first).SteeringSupported)
	atCrash := make(chan []byte, 1)
	first.beforePromptWrite = func() {
		data, _ := os.ReadFile(recordPath)
		atCrash <- data
		_ = first.cmd.Process.Kill()
		<-first.Done()
	}
	require.NoError(t, first.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "steer"}), "a steer is accepted at once")
	var saved acpSavedSession
	require.NoError(t, json.Unmarshal(<-atCrash, &saved))
	adjust := ACPQueuedPrompt{ID: "steer", Text: "adjust", Sender: ACPSender{Kind: ACPSenderUser}}
	require.Equal(t, []ACPQueuedPrompt{adjust}, saved.State.Steers, "the steer is saved before the agent sees it")
	require.Eventually(t, func() bool {
		state := publishedACPState(t, first)
		return !state.Steering && len(state.Queue) == 1
	}, 5*time.Second, 10*time.Millisecond, "a steer the exited agent never took is queued")

	second := c.start(&saved)
	state := publishedACPState(t, second)
	assert.Equal(t, []ACPQueuedPrompt{adjust}, state.Queue)
	assert.Empty(t, state.Steers)
	require.NoError(t, second.Command(ACPCommand{Type: "resume"}))
	require.Eventually(t, func() bool {
		state = publishedACPState(t, second)
		return !state.Busy && len(state.Queue) == 0 && slices.Contains(userSubmissions(state), "steer")
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"turn", "steer"}, userSubmissions(state), "it runs once, as the next prompt")
}

// A steer the agent took leaves the steers in the update that records it, so
// a later restore does not queue it again.
func TestACPSteerTheAgentTookLeavesTheSteers(t *testing.T) {
	t.Setenv("KENN_FORGE_ACP_STEERING", "1")
	c := launchRequestChat(t)
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "turn"}))
	require.True(t, c.state().SteeringSupported)

	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "steer"}))
	state := c.awaitState(func(state ACPState) bool { return slices.Contains(userSubmissions(state), "steer") })
	assert.False(t, state.Steering)
	assert.Empty(t, state.Steers)

	saved := readSavedSession(t, c.manager.acpSessionPath(c.info.Key))
	assert.Nil(t, saved.State.Sending)
	assert.Empty(t, saved.State.Steers)
	assert.Empty(t, saved.State.Queue)
}

// userSubmissions lists the submission IDs of the people's messages in the
// transcript, in order.
func userSubmissions(state ACPState) []string {
	var ids []string
	for _, message := range state.Messages {
		if message.Role == "user" {
			ids = append(ids, message.SubmissionID)
		}
	}
	return ids
}

// The sending record is written before the agent sees the prompt and cleared
// in the update that records the message.
func TestACPSendingIsPersistedBeforeTheWrite(t *testing.T) {
	agent := newDetachedACP(t)
	agent.recordPath = filepath.Join(t.TempDir(), "session.json")
	var beforeWrite acpSavedSession
	agent.beforePromptWrite = func() { beforeWrite = readSavedSession(t, agent.recordPath) }
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "question", ID: "submission"}))

	assert.Equal(t, &ACPQueuedPrompt{ID: "submission", Text: "question", Sender: ACPSender{Kind: ACPSenderUser}}, beforeWrite.State.Sending)
	assert.Empty(t, beforeWrite.State.Messages)
	after := readSavedSession(t, agent.recordPath)
	assert.Nil(t, after.State.Sending)
	require.Len(t, after.State.Messages, 1)
	assert.Equal(t, "submission", after.State.Messages[0].SubmissionID)
}

func TestACPPromptWithoutIDIsNeverSending(t *testing.T) {
	agent := newDetachedACP(t)
	agent.recordPath = filepath.Join(t.TempDir(), "session.json")
	agent.beforePromptWrite = func() {
		agent.mu.Lock()
		assert.Nil(t, agent.state.Sending)
		agent.mu.Unlock()
		assert.NoFileExists(t, agent.recordPath, "nothing is saved before the write")
	}
	require.NoError(t, agent.Prompt("question", ACPSender{Kind: ACPSenderUser}))

	state := publishedACPState(t, agent)
	require.Len(t, state.Turns, 1)
	assert.Empty(t, state.Turns[0].SubmissionID)
}

func TestACPPromptIsNotWrittenWhenSendingCannotBePersisted(t *testing.T) {
	agent := newDetachedACP(t)
	var written bytes.Buffer
	agent.stdin = testWriteCloser{Writer: &written}
	// A directory cannot be replaced by the session file.
	agent.recordPath = t.TempDir()
	agent.beforePromptWrite = func() { assert.Fail(t, "the prompt must not be written") }
	require.Error(t, agent.Command(ACPCommand{Type: "prompt", Text: "question", ID: "submission"}))

	assert.Empty(t, written.String())
	state := publishedACPState(t, agent)
	assert.False(t, state.Busy)
	assert.Nil(t, state.Sending)
	assert.Empty(t, state.Messages)
	assert.Empty(t, state.Turns)
}

func TestACPTurnRecordsFollowEachTurn(t *testing.T) {
	t.Setenv("KENN_FORGE_ACP_STEERING", "1")
	c := launchRequestChat(t)
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "hello", ID: "first"}))
	turns := c.awaitIdle().Turns
	require.Len(t, turns, 1)
	assert.Equal(t, "first", turns[0].SubmissionID)
	assert.Equal(t, "end_turn", turns[0].StopReason)
	assert.Empty(t, turns[0].Error)
	assert.NotEmpty(t, turns[0].StartedAt)
	assert.NotEmpty(t, turns[0].EndedAt)

	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "second"}))
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "steer"}))
	state := c.awaitState(func(state ACPState) bool { return slices.Contains(userSubmissions(state), "steer") })
	turns = state.Turns
	require.Len(t, turns, 2, "an injected steer joins the running turn")
	assert.Equal(t, "second", turns[1].SubmissionID)
	assert.Empty(t, turns[1].EndedAt)
	starts := map[string]int{}
	for i, message := range state.Messages {
		if message.Role == "user" {
			starts[message.SubmissionID] = state.MessageOffset + i
		}
	}
	require.NotNil(t, turns[0].FirstMessage)
	require.NotNil(t, turns[1].FirstMessage)
	assert.Equal(t, starts["first"], *turns[0].FirstMessage, "a turn begins at the prompt that started it")
	assert.Equal(t, starts["second"], *turns[1].FirstMessage)
	assert.Greater(t, starts["steer"], *turns[1].FirstMessage, "steering falls inside the running turn")
	require.NoError(t, c.chat.Command(ACPCommand{Type: "cancel"}))
	turns = c.awaitState(func(s ACPState) bool { return !s.Busy }).Turns
	require.Len(t, turns, 2)
	assert.Equal(t, "cancelled", turns[1].StopReason)
	assert.NotEmpty(t, turns[1].EndedAt)
}

// A turn the agent starts after a steer is part of the running turn, which
// ends only when the agent reports its thread idle.
func TestACPTurnRecordEndsWithTheAgentsOwnTurn(t *testing.T) {
	agent := newDetachedACP(t)
	agent.state.Busy = true
	agent.mu.Lock()
	agent.beginTurnRecordLocked(ACPQueuedPrompt{ID: "turn"}, 0)
	agent.mu.Unlock()
	agent.external = &acpExternalTurn{active: true}
	completed := make(chan acpTurnResult, 1)
	completed <- acpTurnResult{stopReason: acpsdk.StopReasonEndTurn}
	agent.finishTurn(completed)

	state := publishedACPState(t, agent)
	require.True(t, state.Busy, "the agent's own turn is still running")
	require.Len(t, state.Turns, 1)
	assert.Empty(t, state.Turns[0].EndedAt, "the prompt completed but the agent's turn did not")

	agent.mu.Lock()
	agent.threadStatusLocked(map[string]any{"codex": map[string]any{"threadStatus": map[string]any{"type": "idle"}}})
	agent.mu.Unlock()
	state = publishedACPState(t, agent)
	assert.False(t, state.Busy)
	require.Len(t, state.Turns, 1)
	assert.Equal(t, "end_turn", state.Turns[0].StopReason)
	assert.NotEmpty(t, state.Turns[0].EndedAt)
}

// A steer whose prompt completed before the agent answered starts a turn
// that no running record covers, so the steer gets one.
func TestACPSteerAfterThePromptEndedStartsARecord(t *testing.T) {
	agent := newDetachedACP(t)
	agent.state.SteeringSupported = true
	agent.reportsThreadStatus = true
	hostReader, hostWriter := io.Pipe()
	agent.stdin = hostWriter
	peerReader, peerWriter := io.Pipe()
	t.Cleanup(func() { _ = hostReader.Close(); _ = hostWriter.Close(); _ = peerReader.Close(); _ = peerWriter.Close() })
	agent.client = acpsdk.NewClientSideConnection(agent, agent, peerReader)
	go func() {
		line, _ := bufio.NewReader(hostReader).ReadBytes('\n')
		var request struct {
			ID jsontext.Value `json:"id"`
		}
		_ = json.Unmarshal(line, &request)
		// The prompt's turn ends before the agent answers the steer.
		agent.mu.Lock()
		agent.state.Busy = false
		agent.mu.Unlock()
		_, _ = fmt.Fprintf(peerWriter, `{"jsonrpc":"2.0","id":%s,"result":{"outcome":"startedNewTurn"}}`+"\n", request.ID)
	}()
	agent.mu.Lock()
	agent.state.Busy = true
	agent.acceptSteerLocked(ACPQueuedPrompt{Text: "takeover", ID: "steer", Sender: ACPSender{Kind: ACPSenderUser}})
	agent.mu.Unlock()

	var state ACPState
	require.Eventually(t, func() bool {
		state = publishedACPState(t, agent)
		return !state.Steering && len(state.Turns) == 1
	}, 5*time.Second, 10*time.Millisecond)
	require.True(t, state.Busy)
	require.Len(t, state.Turns, 1)
	assert.Equal(t, "steer", state.Turns[0].SubmissionID)
	assert.Empty(t, state.Turns[0].EndedAt)

	agent.mu.Lock()
	agent.threadStatusLocked(map[string]any{"codex": map[string]any{"threadStatus": map[string]any{"type": "idle"}}})
	agent.mu.Unlock()
	turns := publishedACPState(t, agent).Turns
	require.Len(t, turns, 1)
	assert.Equal(t, "end_turn", turns[0].StopReason)
	assert.NotEmpty(t, turns[0].EndedAt)
}

func TestACPUnqueuedSubmissionCannotRunLater(t *testing.T) {
	agent := newDetachedACP(t)
	agent.state.Busy = true
	queued := ACPCommand{Type: "prompt", Mode: "queue", Text: "later", ID: "queued"}
	require.NoError(t, agent.Command(queued))
	require.NoError(t, agent.Command(ACPCommand{Type: "unqueue", ID: "queued"}))
	require.ErrorContains(t, agent.Command(queued), "submission was withdrawn")

	state := publishedACPState(t, agent)
	assert.Empty(t, state.Queue)
	assert.Equal(t, []string{"queued"}, state.Withdrawn)

	restored := newDetachedACP(t)
	restored.restoreTranscriptLocked(state)
	require.ErrorContains(t, restored.Command(queued), "submission was withdrawn")
}

// A prompt that was sending when its owner stopped runs after a restore
// unless the transcript recorded it: a queued one stays at the head of the
// queue, and one sent at once goes back to it.
func TestACPRestoreQueuesThePromptThatWasSending(t *testing.T) {
	sending := ACPQueuedPrompt{ID: "sent", Text: "kept", Sender: ACPSender{Kind: ACPSenderUser}}
	later := ACPQueuedPrompt{ID: "later", Text: "later", Sender: ACPSender{Kind: ACPSenderUser}}
	for _, tc := range []struct {
		name     string
		saved    ACPState
		expected []ACPQueuedPrompt
	}{
		{name: "queued", saved: ACPState{Sending: &sending, Queue: []ACPQueuedPrompt{sending, later}}, expected: []ACPQueuedPrompt{sending, later}},
		{name: "sent at once", saved: ACPState{Sending: &sending, Queue: []ACPQueuedPrompt{later}}, expected: []ACPQueuedPrompt{sending, later}},
		{name: "recorded", saved: ACPState{Sending: &sending, Messages: []ACPMessage{{ID: "m", Role: "user", Text: "kept", SubmissionID: "sent"}}}},
		{name: "steers lead the queue", saved: ACPState{Steers: []ACPQueuedPrompt{sending}, Queue: []ACPQueuedPrompt{later}}, expected: []ACPQueuedPrompt{sending, later}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := newDetachedACP(t)
			agent.restoreTranscriptLocked(tc.saved)
			state := publishedACPState(t, agent)
			if tc.expected == nil {
				assert.Empty(t, state.Queue)
			} else {
				assert.Equal(t, tc.expected, state.Queue)
			}
			assert.Equal(t, len(tc.expected) > 0, state.QueuePaused, "a reloaded conversation never starts queued work on its own")
			assert.Nil(t, state.Sending)
			assert.Empty(t, state.Steers)
		})
	}
}

// An agent that exits during a turn no prompt response will end, such as one
// it started after a steer, still ends that turn's record.
func TestACPAgentExitEndsTheRunningTurnRecord(t *testing.T) {
	c := newInProcessChat(t)
	agent := c.start(nil)
	agent.mu.Lock()
	agent.state.Busy = true
	agent.beginTurnRecordLocked(ACPQueuedPrompt{ID: "steer"}, 0)
	agent.external = &acpExternalTurn{active: true, promptDone: true}
	agent.mu.Unlock()
	require.NoError(t, agent.cmd.Process.Kill())
	<-agent.Done()

	for _, state := range []ACPState{publishedACPState(t, agent), readSavedSession(t, c.recordPath()).State} {
		assert.False(t, state.Busy)
		require.Len(t, state.Turns, 1)
		assert.Equal(t, "agent process exited during the turn", state.Turns[0].Error)
		assert.NotEmpty(t, state.Turns[0].EndedAt)
	}
}

func TestACPRestoreEndsTheTurnTheAgentExitedDuring(t *testing.T) {
	agent := newDetachedACP(t)
	agent.restoreTranscriptLocked(ACPState{
		Withdrawn: []string{"withdrawn"},
		Turns: []ACPTurnRecord{
			{SubmissionID: "done", StartedAt: "2026-01-01T00:00:00Z", EndedAt: "2026-01-01T00:01:00Z", StopReason: "end_turn"},
			{SubmissionID: "running", StartedAt: "2026-01-01T00:02:00Z"},
		},
	})

	state := publishedACPState(t, agent)
	assert.Equal(t, []string{"withdrawn"}, state.Withdrawn)
	require.Len(t, state.Turns, 2)
	assert.Equal(t, ACPTurnRecord{SubmissionID: "done", Sender: ACPSender{Kind: ACPSenderUser}, StartedAt: "2026-01-01T00:00:00Z", EndedAt: "2026-01-01T00:01:00Z", StopReason: "end_turn"}, state.Turns[0])
	assert.Equal(t, "agent process exited during the turn", state.Turns[1].Error)
	assert.NotEmpty(t, state.Turns[1].EndedAt)
	assert.Empty(t, state.Turns[1].StopReason)
}

func TestACPTurnRecordsKeepTheNewestFifty(t *testing.T) {
	agent := newDetachedACP(t)
	agent.mu.Lock()
	for i := range acpTurnRecordLimit + 5 {
		agent.beginTurnRecordLocked(ACPQueuedPrompt{ID: "s" + strconv.Itoa(i)}, 0)
		agent.endTurnRecordLocked("end_turn", nil)
	}
	turns := agent.state.Turns
	agent.mu.Unlock()
	require.Len(t, turns, acpTurnRecordLimit)
	assert.Equal(t, "s5", turns[0].SubmissionID)
	assert.Equal(t, "s54", turns[len(turns)-1].SubmissionID)
}

// Once the agent has a prompt, its turn runs whatever happens to the save
// that follows. Reporting the save failure as a refusal would tell the sender
// that running work never started, so the send succeeds and the chat shows
// the error.
func TestACPPromptIsAcceptedWhenTheSaveAfterTheWriteFails(t *testing.T) {
	agent := newDetachedACP(t)
	// A directory cannot be replaced by the session file. A prompt without
	// an ID saves nothing before the write.
	agent.recordPath = t.TempDir()

	require.NoError(t, agent.Prompt("question", ACPSender{Kind: ACPSenderUser}))
	state := publishedACPState(t, agent)
	assert.True(t, state.Busy)
	require.Len(t, state.Messages, 1)
	assert.Contains(t, state.Error, "save chat after sending a prompt")
}

// A queued prompt drains even when the queue cannot be saved, and an
// unqueued one never runs. Neither change can be undone by a failed save, so
// neither is reported as refused.
func TestACPQueueChangesSucceedWhenTheSaveAfterThemFails(t *testing.T) {
	agent := newDetachedACP(t)
	require.NoError(t, agent.Prompt("running", ACPSender{Kind: ACPSenderUser}))
	agent.recordPath = t.TempDir()

	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "queue", Text: "later", ID: "queued"}))
	state := publishedACPState(t, agent)
	require.Len(t, state.Queue, 1)
	assert.Contains(t, state.Error, "save chat after queueing a prompt")
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "queue", Text: "later", ID: "queued"}),
		"a retry finds the queued prompt")

	require.NoError(t, agent.Command(ACPCommand{Type: "unqueue", ID: "queued"}))
	state = publishedACPState(t, agent)
	assert.Empty(t, state.Queue)
	assert.Equal(t, []string{"queued"}, state.Withdrawn)
	assert.Contains(t, state.Error, "save chat after unqueueing a prompt")
}

// drain reads the queue head and then sends it. A withdrawal in between
// removes the prompt, so the send it already read must not go out.
func TestACPQueuedPromptWithdrawnAfterDrainReadItIsNotSent(t *testing.T) {
	agent := newDetachedACP(t)
	var written bytes.Buffer
	agent.stdin = testWriteCloser{Writer: &written}
	agent.state.Queue = []ACPQueuedPrompt{{ID: "withdrawn", Text: "removed"}}
	read := agent.state.Queue[0]
	require.NoError(t, agent.Command(ACPCommand{Type: "unqueue", ID: "withdrawn"}))

	agent.turnMu.Lock()
	err := agent.startPromptLocked(read, true)
	agent.turnMu.Unlock()

	require.ErrorIs(t, err, errACPQueueChanged)
	assert.NotContains(t, written.String(), "removed")
	state := publishedACPState(t, agent)
	assert.False(t, state.Busy)
	assert.Nil(t, state.Sending)
}

// A queued prompt stays queued until its write returns. Withdrawing it while
// it is being sent is refused, so a retry of the submission reports it as
// sent rather than withdrawn.
func TestACPQueuedPromptCannotBeWithdrawnWhileItIsSent(t *testing.T) {
	agent := newDetachedACP(t)
	var written bytes.Buffer
	agent.stdin = testWriteCloser{Writer: &written}
	agent.state.Queue = []ACPQueuedPrompt{{ID: "queued", Text: "later"}}
	var unqueueErr error
	agent.beforePromptWrite = func() {
		unqueueErr = agent.Command(ACPCommand{Type: "unqueue", ID: "queued"})
	}

	agent.drain()

	require.ErrorIs(t, unqueueErr, errACPQueuedPromptSending)
	assert.Equal(t, "sending", ACPErrorCode(unqueueErr))
	assert.Contains(t, written.String(), `"text":"later"`)
	state := publishedACPState(t, agent)
	assert.NotContains(t, state.Withdrawn, "queued")
	assert.Empty(t, state.Queue)
	assert.True(t, state.Busy)
}

// queuedACP is an idle chat with paused queued prompts from a person.
func queuedACP(t *testing.T, ids ...string) (*ACP, *scriptedPeer) {
	t.Helper()
	agent, peer := newScriptedACP(t)
	agent.recordPath = filepath.Join(t.TempDir(), "session.json")
	agent.mu.Lock()
	for _, id := range ids {
		agent.state.Queue = append(agent.state.Queue, ACPQueuedPrompt{ID: id, Text: id, Sender: ACPSender{Kind: ACPSenderUser}})
	}
	agent.state.QueuePaused = true
	agent.mu.Unlock()
	return agent, peer
}

// A prompt moved ahead of the head drain read runs first: drain sends only
// the current head.
func TestACPQueuedPromptMovedBackAfterDrainReadItIsNotSent(t *testing.T) {
	agent, peer := queuedACP(t, "a", "b")
	agent.mu.Lock()
	agent.state.QueuePaused = false
	agent.mu.Unlock()
	head := publishedACPState(t, agent).Queue[0]
	require.NoError(t, agent.Command(ACPCommand{Type: "move", ID: "b", Index: 0}))
	agent.turnMu.Lock()
	err := agent.startPromptLocked(head, true)
	agent.turnMu.Unlock()
	require.ErrorIs(t, err, errACPQueueChanged)
	assert.Empty(t, peer.params(acpsdk.AgentMethodSessionPrompt), "the moved-back prompt was not sent")

	require.NoError(t, agent.Command(ACPCommand{Type: "resume"}))
	// Busy is set before the write; the message is recorded after it.
	require.Eventually(t, func() bool { return len(publishedACPState(t, agent).Messages) == 1 }, time.Second, time.Millisecond)
	assert.Equal(t, "b", publishedACPState(t, agent).Messages[0].SubmissionID, "the prompt moved ahead runs first")
	require.Len(t, peer.params(acpsdk.AgentMethodSessionPrompt), 1)
}

// A prompt being sent cannot be moved, and nothing moves ahead of it.
func TestACPQueuedPromptCannotBeMovedWhileItIsSent(t *testing.T) {
	agent, _ := queuedACP(t, "a", "b")
	agent.beforePromptWrite = func() {
		agent.beforePromptWrite = nil
		require.ErrorIs(t, agent.Command(ACPCommand{Type: "move", ID: "a", Index: 1}), errACPQueuedPromptSending)
		require.NoError(t, agent.Command(ACPCommand{Type: "move", ID: "b", Index: 0}))
		assert.Equal(t, []string{"a", "b"}, readQueueIDs(publishedACPState(t, agent).Queue))
	}
	agent.mu.Lock()
	agent.state.QueuePaused = false
	agent.mu.Unlock()
	agent.drain()

	state := publishedACPState(t, agent)
	require.Len(t, state.Messages, 1)
	assert.Equal(t, "a", state.Messages[0].SubmissionID)
	assert.Equal(t, []string{"b"}, readQueueIDs(state.Queue))
}

// A session saved before senders were recorded reopens with its queued
// prompts and turns as a person's, which clients accept, and its queue
// drains with that sender.
func TestACPRestoredQueueWithoutSendersIsAPersons(t *testing.T) {
	var saved acpSavedSession
	require.NoError(t, json.Unmarshal([]byte(`{"SessionID":"native","State":{
		"messages":[],"queue":[{"id":"old","text":"queued before senders"}],
		"turns":[{"submissionId":"earlier","startedAt":"2026-10-01T12:00:00Z","endedAt":"2026-10-01T12:01:00Z","activeMillis":60000}]}}`), &saved))
	agent, peer := newScriptedACP(t)
	agent.mu.Lock()
	agent.restoreTranscriptLocked(saved.State)
	agent.mu.Unlock()

	snapshot, err := agent.Snapshot()
	require.NoError(t, err)
	var published struct {
		Queue []struct {
			Sender ACPSender `json:"sender"`
		} `json:"queue"`
		Turns []struct {
			Sender ACPSender `json:"sender"`
		} `json:"turns"`
	}
	require.NoError(t, json.Unmarshal(snapshot, &published))
	person := ACPSender{Kind: ACPSenderUser}
	require.Len(t, published.Queue, 1)
	assert.Equal(t, person, published.Queue[0].Sender)
	require.Len(t, published.Turns, 1)
	assert.Equal(t, person, published.Turns[0].Sender)

	require.NoError(t, agent.Command(ACPCommand{Type: "resume"}))
	require.Eventually(t, func() bool { return len(publishedACPState(t, agent).Messages) == 1 }, time.Second, time.Millisecond)
	assert.Equal(t, &person, publishedACPState(t, agent).Messages[0].Sender)
	var request struct {
		Meta map[string]map[string]any `json:"_meta"`
	}
	require.NoError(t, json.Unmarshal(peer.params(acpsdk.AgentMethodSessionPrompt)[0], &request))
	assert.Equal(t, "user", request.Meta["forge"]["sender"])
}

// A Stop that lands after drain read the head pauses the queue, and the head
// is not sent.
func TestACPQueuedPromptStoppedAfterDrainReadItIsNotSent(t *testing.T) {
	agent, peer := queuedACP(t, "a")
	agent.mu.Lock()
	agent.state.QueuePaused = false
	agent.mu.Unlock()
	head := publishedACPState(t, agent).Queue[0]
	require.NoError(t, agent.Command(ACPCommand{Type: "cancel"}))
	agent.turnMu.Lock()
	err := agent.startPromptLocked(head, true)
	agent.turnMu.Unlock()
	require.ErrorIs(t, err, errACPQueueChanged)
	assert.Empty(t, peer.params(acpsdk.AgentMethodSessionPrompt))
	state := publishedACPState(t, agent)
	assert.True(t, state.QueuePaused)
	assert.Equal(t, []string{"a"}, readQueueIDs(state.Queue))
}

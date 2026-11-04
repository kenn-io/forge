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
	"testing/synctest"
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
	agent, err := c.manager.startACP(c.t.Context(), c.info, c.command, c.cwd, nil, nil, saved, nil)
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
	agent.insertMessageLocked(0, ACPMessage{Role: "user", Text: "question", SubmissionID: "turn"})
	agent.beginTurnRecordLocked(ACPQueuedPrompt{ID: "turn"}, 0)
	agent.mu.Unlock()
	agent.external = &acpExternalTurn{}
	completed := make(chan acpTurnResult, 1)
	completed <- acpTurnResult{stopReason: acpsdk.StopReasonEndTurn}
	agent.finishTurn(completed)

	state := publishedACPState(t, agent)
	require.True(t, state.Busy, "the agent's own turn is still running")
	assert.Equal(t, &ACPTurn{}, state.Messages[0].Turn)
	require.Len(t, state.Turns, 1)
	assert.Empty(t, state.Turns[0].EndedAt, "the prompt completed but the agent's turn did not")

	agent.mu.Lock()
	agent.threadStatusLocked(map[string]any{"codex": map[string]any{"threadStatus": map[string]any{"type": "idle"}}})
	agent.mu.Unlock()
	state = publishedACPState(t, agent)
	assert.False(t, state.Busy)
	require.Len(t, state.Turns, 1)
	assert.Equal(t, "end_turn", state.Turns[0].StopReason)
	assert.Equal(t, &ACPTurn{StopReason: "end_turn"}, state.Messages[0].Turn)
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

// A prompt that was sending when its owner stopped runs after a restore: a
// queued one stays at the head of the queue, and one sent at once goes back
// to it. The owner records its message and clears sending in one save.
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
		{name: "steers lead the queue", saved: ACPState{Steers: []ACPQueuedPrompt{sending}, Queue: []ACPQueuedPrompt{later}}, expected: []ACPQueuedPrompt{sending, later}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := newDetachedACP(t)
			agent.restoreTranscriptLocked(tc.saved)
			state := publishedACPState(t, agent)
			assert.Equal(t, tc.expected, state.Queue)
			assert.True(t, state.QueuePaused, "a reloaded conversation never starts queued work on its own")
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
	agent.insertMessageLocked(0, ACPMessage{Role: "user", Text: "question", SubmissionID: "steer"})
	agent.beginTurnRecordLocked(ACPQueuedPrompt{ID: "steer"}, 0)
	agent.external = &acpExternalTurn{promptDone: true}
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
		Messages:  []ACPMessage{{Role: "user", Text: "interrupted question", Turn: &ACPTurn{}}},
		Turns: []ACPTurnRecord{
			{SubmissionID: "done", StartedAt: "2026-01-01T00:00:00Z", EndedAt: "2026-01-01T00:01:00Z", StopReason: "end_turn"},
			{SubmissionID: "running", StartedAt: "2026-01-01T00:02:00Z", FirstMessage: new(0)},
		},
	})

	state := publishedACPState(t, agent)
	assert.Equal(t, []string{"withdrawn"}, state.Withdrawn)
	require.Len(t, state.Turns, 2)
	assert.Equal(t, ACPTurnRecord{SubmissionID: "done", Sender: ACPSender{Kind: ACPSenderUser}, StartedAt: "2026-01-01T00:00:00Z", EndedAt: "2026-01-01T00:01:00Z", StopReason: "end_turn"}, state.Turns[0])
	assert.Equal(t, "agent process exited during the turn", state.Turns[1].Error)
	assert.NotEmpty(t, state.Turns[1].EndedAt)
	assert.Empty(t, state.Turns[1].StopReason)
	assert.Equal(t, &ACPTurn{Error: acpExitedDuringTurn}, state.Messages[0].Turn)
}

func TestACPTurnRecordsKeepTheNewestFifty(t *testing.T) {
	agent := newDetachedACP(t)
	agent.mu.Lock()
	for i := range acpTurnRecordLimit + 5 {
		agent.insertMessageLocked(i, ACPMessage{Role: "user", Text: "question"})
		agent.beginTurnRecordLocked(ACPQueuedPrompt{ID: "s" + strconv.Itoa(i)}, i)
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

// A client resends a removal it cannot tell arrived. Repeating one that took
// effect succeeds; removing a prompt that left the queue unremoved does not.
func TestACPRepeatedWithdrawalSucceeds(t *testing.T) {
	agent := newDetachedACP(t)
	agent.state.Queue = []ACPQueuedPrompt{{ID: "queued", Text: "later"}}

	require.NoError(t, agent.Command(ACPCommand{Type: "unqueue", ID: "queued"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "unqueue", ID: "queued"}))
	assert.Equal(t, []string{"queued"}, publishedACPState(t, agent).Withdrawn)

	require.ErrorContains(t, agent.Command(ACPCommand{Type: "unqueue", ID: "sent"}), "no longer queued")
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

func TestACPTranscriptMarkersSurviveSavedSessionReload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.recordPath = filepath.Join(t.TempDir(), "session.json")
		for i := range 2 {
			require.NoError(t, agent.Command(ACPCommand{Type: "prompt", ID: "turn-" + strconv.Itoa(i), Text: "question"}))
			acpText(t, agent, "Completed reply.")
			peer.endTurn(acpsdk.StopReasonEndTurn)
			synctest.Wait()
		}
		saved := readSavedSession(t, agent.recordPath).State
		data, err := agent.Page(0, 200)
		require.NoError(t, err)
		var page ACPState
		require.NoError(t, json.Unmarshal(data, &page))
		reloaded, err := newDormantACP(saved, nil).Page(0, 200)
		require.NoError(t, err)
		var restored ACPState
		require.NoError(t, json.Unmarshal(reloaded, &restored))
		require.Len(t, page.Messages, 4)
		require.Len(t, restored.Messages, 4)
		assert.Equal(t, &ACPTurn{StopReason: "end_turn"}, restored.Messages[0].Turn)
		assert.Equal(t, &ACPTurn{StopReason: "end_turn"}, restored.Messages[2].Turn)
		assert.Equal(t, page.Messages, restored.Messages)
	})
}

func TestACPTakeoverKeepsItsAcceptedBoundary(t *testing.T) {
	for _, tc := range []struct {
		status      string
		promptFirst bool
		chunked     bool
		stopped     bool
	}{
		{status: "none"},
		{status: "before output"},
		{status: "before steering", stopped: true},
		{status: "before acceptance", promptFirst: true},
		{status: "before prompt response"},
		{status: "before steering", promptFirst: true, chunked: true},
	} {
		t.Run(fmt.Sprintf("status=%s/prompt response before acceptance=%t/stopped=%t", tc.status, tc.promptFirst, tc.stopped), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				agent, peer := newScriptedACP(t)
				agent.state.SteeringSupported = true
				require.NoError(t, agent.Prompt("question", ACPSender{Kind: ACPSenderUser}))
				if tc.status == "before steering" {
					acpThreadStatus(t, agent, "active")
				}
				acpText(t, agent, "Original reply.")
				require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "steer"}))
				synctest.Wait()
				if tc.chunked {
					acpUpdate(t, agent, acpChunk("Take", "takeover"))
				}
				if tc.promptFirst {
					peer.endTurn(acpsdk.StopReasonEndTurn)
					synctest.Wait()
				}
				if tc.chunked {
					acpUpdate(t, agent, acpChunk("over", "takeover"))
				}
				if tc.status == "before acceptance" {
					acpCompleteTakeoverBeforeAcceptance(t, agent, peer)
				} else {
					peer.answer(acpSteeringMethod, `{"outcome":"startedNewTurn"}`)
					synctest.Wait()
				}
				before := publishedACPState(t, agent)
				reply := "Takeover reply."
				if tc.status != "before acceptance" {
					if tc.status == "before prompt response" {
						acpThreadStatus(t, agent, "active")
					}
					if !tc.promptFirst {
						peer.endTurn(acpsdk.StopReasonEndTurn)
						synctest.Wait()
					}
					if tc.status == "before output" {
						acpThreadStatus(t, agent, "active")
					}
					if tc.chunked {
						acpUpdate(t, agent, acpChunk(".", "takeover"))
						reply = "Takeover."
					} else {
						acpText(t, agent, reply)
					}
					if tc.stopped {
						require.NoError(t, agent.Command(ACPCommand{Type: "cancel"}))
					}
					if tc.status != "none" {
						acpThreadStatus(t, agent, "idle")
					}
				}
				advance(minTextDeliveryInterval)
				state := publishedACPState(t, agent)
				require.Len(t, state.Messages, 4)
				if tc.status == "before acceptance" {
					assert.False(t, state.Busy)
					assert.False(t, state.Stopping)
				}
				first := &ACPTurn{StopReason: "end_turn"}
				if tc.stopped {
					assert.False(t, state.Busy)
					first = &ACPTurn{}
					require.Len(t, state.Turns, 1)
					assert.Empty(t, state.Turns[0].StopReason)
				}
				assert.Equal(t, first, state.Messages[0].Turn)
				assert.Equal(t, "Original reply.", state.Messages[1].Text)
				want := &ACPTurn{}
				if tc.status == "before prompt response" {
					want = nil
					updates, err := decodedACPUpdates(t, agent, before.MessageCursor)
					require.NoError(t, err)
					assert.Contains(t, updates.Messages, state.Messages[2], "the shared turn removes the obsolete marker in updates")
				} else if tc.stopped {
					want = nil
				} else if tc.status != "none" {
					want.StopReason = "end_turn"
				}
				assert.Equal(t, want, state.Messages[2].Turn)
				assert.Equal(t, reply, state.Messages[3].Text)
				assert.Nil(t, state.Messages[3].Turn)
			})
		})
	}
}

func acpCompleteTakeoverBeforeAcceptance(t *testing.T, agent *ACP, peer *scriptedPeer) {
	t.Helper()
	acpThreadStatus(t, agent, "active")
	acpText(t, agent, "Takeover reply.")
	acpThreadStatus(t, agent, "idle")
	peer.answer(acpSteeringMethod, `{"outcome":"startedNewTurn"}`)
	synctest.Wait()
}

func acpThreadStatus(t *testing.T, agent *ACP, kind string) {
	t.Helper()
	acpUpdate(t, agent, acpsdk.SessionUpdate{SessionInfoUpdate: &acpsdk.SessionSessionInfoUpdate{
		Meta: map[string]any{"codex": map[string]any{"threadStatus": map[string]any{"type": kind}}},
	}})
	synctest.Wait()
}

func TestACPLateInjectedSteerPreservesTheCompletedAdjustment(t *testing.T) {
	for _, tc := range []struct {
		reason          string
		outcome         string
		outputBeforeAck bool
	}{
		{reason: "end_turn", outcome: "injected", outputBeforeAck: true},
		{reason: "end_turn", outcome: "promptRequired", outputBeforeAck: true},
		{reason: "cancelled", outcome: "injected"},
	} {
		t.Run(fmt.Sprintf("stop reason=%s/outcome=%s/unsolicited output before acceptance=%t", tc.reason, tc.outcome, tc.outputBeforeAck), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				agent, peer := newScriptedACP(t)
				agent.state.SteeringSupported = true
				require.NoError(t, agent.Prompt("question", ACPSender{Kind: ACPSenderUser}))
				acpText(t, agent, "Original reply.")
				require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "steer"}))
				synctest.Wait()
				acpUpdate(t, agent, acpChunk("Adjusted reply.", "adjusted"))
				if tc.reason == "cancelled" {
					require.NoError(t, agent.Command(ACPCommand{Type: "cancel"}))
				}
				before := publishedACPState(t, agent)
				peer.endTurn(acpsdk.StopReason(tc.reason))
				synctest.Wait()
				if tc.outputBeforeAck {
					acpUpdate(t, agent, acpChunk("Unsolicited output.", "unsolicited"))
				}
				peer.answer(acpSteeringMethod, fmt.Sprintf(`{"outcome":%q}`, tc.outcome))
				synctest.Wait()
				if !tc.outputBeforeAck {
					acpUpdate(t, agent, acpChunk("Unsolicited output.", "unsolicited"))
				}
				advance(2 * minTextDeliveryInterval)
				state := publishedACPState(t, agent)
				require.Len(t, state.Messages, 5)
				assert.Equal(t, &ACPTurn{StopReason: tc.reason}, state.Messages[0].Turn)
				updates, err := decodedACPUpdates(t, agent, before.MessageCursor)
				require.NoError(t, err)
				assert.Contains(t, updates.Messages, state.Messages[0], "the completed outcome reaches the client")
				if tc.outcome == "promptRequired" {
					assert.Equal(t, "Adjusted reply.", state.Messages[2].Text)
					assert.Nil(t, state.Messages[2].Turn)
					assert.Equal(t, "Unsolicited output.", state.Messages[3].Text)
					assert.Equal(t, &ACPTurn{}, state.Messages[3].Turn)
					assert.Equal(t, "steer", state.Messages[4].SubmissionID)
					return
				}
				assert.Nil(t, state.Messages[2].Turn)
				assert.Equal(t, "Adjusted reply.", state.Messages[3].Text)
				assert.Nil(t, state.Messages[3].Turn)
				assert.Equal(t, "Unsolicited output.", state.Messages[4].Text)
				assert.Equal(t, &ACPTurn{}, state.Messages[4].Turn)
			})
		})
	}
}

func TestACPPendingTakeoverOwnsLaterSteeringAndOutput(t *testing.T) {
	for _, tc := range []struct {
		name            string
		secondOutcome   string
		promptBeforeAck bool
		statusBeforeAck string
	}{
		{"confirmed while prompt runs", "startedNewTurn", false, ""},
		{"injected after prompt completion", "injected", true, ""},
		{"idle before second acknowledgment", "startedNewTurn", true, "idle"},
		{"active before second acknowledgment", "startedNewTurn", true, "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				agent, peer := newScriptedACP(t)
				agent.state.SteeringSupported = true
				agent.loadSession = true
				agent.recordPath = filepath.Join(t.TempDir(), "session.json")
				require.NoError(t, agent.Prompt("question", ACPSender{Kind: ACPSenderUser}))
				acpText(t, agent, "Original reply.")
				require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "first adjustment", ID: "first"}))
				synctest.Wait()
				peer.answer(acpSteeringMethod, `{"outcome":"startedNewTurn"}`)
				synctest.Wait()
				require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "second adjustment", ID: "second"}))
				synctest.Wait()
				if tc.promptBeforeAck {
					peer.endTurn(acpsdk.StopReasonEndTurn)
					synctest.Wait()
					acpText(t, agent, "Takeover reply.")
				}
				if tc.statusBeforeAck != "" {
					acpThreadStatus(t, agent, tc.statusBeforeAck)
				}
				peer.answer(acpSteeringMethod, fmt.Sprintf(`{"outcome":%q}`, tc.secondOutcome))
				synctest.Wait()
				if tc.statusBeforeAck != "active" {
					acpThreadStatus(t, agent, "active")
				}
				if !tc.promptBeforeAck {
					peer.endTurn(acpsdk.StopReasonEndTurn)
					synctest.Wait()
					acpText(t, agent, "Takeover reply.")
				}
				acpThreadStatus(t, agent, "idle")
				state := publishedACPState(t, agent)
				require.Len(t, state.Messages, 5)
				assert.Equal(t, &ACPTurn{StopReason: "end_turn"}, state.Messages[0].Turn)
				assert.Equal(t, "Original reply.", state.Messages[1].Text)
				var firstTurn *ACPTurn
				if tc.promptBeforeAck {
					firstTurn = &ACPTurn{StopReason: "end_turn"}
				}
				assert.Equal(t, "first", state.Messages[2].SubmissionID)
				assert.Equal(t, firstTurn, state.Messages[2].Turn)
				assert.Equal(t, "second", state.Messages[3].SubmissionID)
				assert.Nil(t, state.Messages[3].Turn)
				assert.Equal(t, "Takeover reply.", state.Messages[4].Text)
				assert.Nil(t, state.Messages[4].Turn)
				assert.True(t, acpParkable(agent), "the settled takeover can park")
			})
		})
	}
}

func TestACPCompletedTakeoverWaitsForItsPromptBeforeDraining(t *testing.T) {
	for _, successor := range []string{"queued", "rejected steer"} {
		t.Run(successor, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				agent, peer := newScriptedACP(t)
				agent.state.SteeringSupported = true
				require.NoError(t, agent.Prompt("question", ACPSender{Kind: ACPSenderUser}))
				original := peer.messages(acpsdk.AgentMethodSessionPrompt)[0]
				acpText(t, agent, "Original reply.")
				require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "steer"}))
				if successor == "queued" {
					require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "queue", Text: "next question", ID: "next"}))
				}
				synctest.Wait()
				acpCompleteTakeoverBeforeAcceptance(t, agent, peer)
				if successor == "rejected steer" {
					require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "next question", ID: "next"}))
					synctest.Wait()
					peer.answer(acpSteeringMethod, `{"outcome":"promptRequired"}`)
					synctest.Wait()
				}
				assert.Len(t, peer.messages(acpsdk.AgentMethodSessionPrompt), 1, "the successor waits for the original prompt response")
				_, err := fmt.Fprintf(peer.writer, `{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", original)
				require.NoError(t, err)
				synctest.Wait()
				state := publishedACPState(t, agent)
				require.Len(t, peer.messages(acpsdk.AgentMethodSessionPrompt), 2)
				require.Len(t, state.Turns, 2)
				assert.True(t, state.Busy)
				assert.Equal(t, "next", state.Turns[1].SubmissionID)
				assert.Empty(t, state.Turns[1].EndedAt, "the earlier response cannot complete the successor")
				peer.endTurn(acpsdk.StopReasonEndTurn)
				synctest.Wait()
			})
		})
	}
}

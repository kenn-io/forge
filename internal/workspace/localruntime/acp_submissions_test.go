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
	"strconv"
	"testing"

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
	agent, err := c.manager.startACP(c.t.Context(), c.info, c.command, c.cwd, nil, saved)
	require.NoError(c.t, err)
	c.t.Cleanup(func() { _ = agent.Stop(context.Background()) })
	return agent
}

// An owner that crashes after recording a prompt as sending, but before the
// agent is known to have it, cannot say whether the agent ran it. The
// replacement owner reports that submission as uncertain instead of running
// it a second time.
func TestACPPromptSendingAtCrashIsUncertainAfterRestore(t *testing.T) {
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
	require.Error(t, first.Command(ACPCommand{Type: "prompt", Text: "lost", ID: "crash"}))
	assert.Nil(t, readSavedSession(t, recordPath).State.Sending, "a failed write is not in flight")

	var saved acpSavedSession
	require.NoError(t, json.Unmarshal(atCrash, &saved))
	require.Equal(t, &ACPQueuedPrompt{ID: "crash", Text: "lost"}, saved.State.Sending)
	second := c.start(&saved)

	state := publishedACPState(t, second)
	assert.Equal(t, []string{"crash"}, state.Uncertain)
	assert.Nil(t, state.Sending)
	assert.Empty(t, state.Messages)
	err = second.Command(ACPCommand{Type: "prompt", Text: "lost", ID: "crash"})
	require.ErrorIs(t, err, ErrACPSubmissionUncertain)
	assert.Equal(t, "uncertain", acpErrorCode(err))
	require.ErrorIs(t, acpCodeError("uncertain"), ErrACPSubmissionUncertain)
	assert.Empty(t, publishedACPState(t, second).Messages, "an uncertain submission never runs again")

	persisted := readSavedSession(t, recordPath)
	assert.Equal(t, []string{"crash"}, persisted.State.Uncertain, "the next restore still knows")
}

// The sending record is written before the agent sees the prompt and cleared
// in the update that records the message.
func TestACPSendingIsPersistedBeforeTheWrite(t *testing.T) {
	agent := newDetachedACP(t)
	agent.recordPath = filepath.Join(t.TempDir(), "session.json")
	var beforeWrite acpSavedSession
	agent.beforePromptWrite = func() { beforeWrite = readSavedSession(t, agent.recordPath) }
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "question", ID: "submission"}))

	assert.Equal(t, &ACPQueuedPrompt{ID: "submission", Text: "question"}, beforeWrite.State.Sending)
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
	require.NoError(t, agent.Prompt("question"))

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
	turns = c.state().Turns
	require.Len(t, turns, 2, "an injected steer joins the running turn")
	assert.Equal(t, "second", turns[1].SubmissionID)
	assert.Empty(t, turns[1].EndedAt)
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
	agent.beginTurnRecordLocked("turn", 0, 0)
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

// The same takeover through the fixture: one record, ended with the agent's turn.
func TestACPTakeoverTurnRecordThroughTheAgent(t *testing.T) {
	t.Setenv("KENN_FORGE_ACP_STEERING", "1")
	c := launchRequestChat(t)
	// An earlier turn shows the agent reports thread status.
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "hello", ID: "first"}))
	c.awaitIdle()
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "turn"}))
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "takeover", ID: "steer"}))

	turns := c.awaitState(func(s ACPState) bool { return !s.Busy }).Turns
	require.Len(t, turns, 2, "the steer joined the prompt's turn")
	assert.Equal(t, "turn", turns[1].SubmissionID)
	assert.Equal(t, "end_turn", turns[1].StopReason)
	assert.NotEmpty(t, turns[1].EndedAt)
}

// A late thread status claims the agent's turn after the prompt's turn has
// ended; that turn gets its own record.
func TestACPLateClaimedAgentTurnGetsARecord(t *testing.T) {
	t.Setenv("KENN_FORGE_ACP_STEERING", "1")
	t.Setenv("KENN_FORGE_ACP_LATE_THREAD_STATUS", "1")
	c := launchRequestChat(t)
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "turn"}))
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "takeover", ID: "steer"}))
	c.awaitState(func(s ACPState) bool { return !s.Busy })
	claimed := c.awaitState(func(s ACPState) bool { return s.Busy }).Turns
	require.Len(t, claimed, 2)
	assert.NotEmpty(t, claimed[0].EndedAt)
	assert.Empty(t, claimed[1].EndedAt)

	turns := c.awaitState(func(s ACPState) bool { return !s.Busy }).Turns
	require.Len(t, turns, 2)
	assert.Empty(t, turns[1].SubmissionID)
	assert.Equal(t, "end_turn", turns[1].StopReason)
	assert.NotEmpty(t, turns[1].EndedAt)
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
		_, _ = fmt.Fprintf(peerWriter, `{"jsonrpc":"2.0","id":%s,"result":{"outcome":"startedNewTurn"}}`+"\n", request.ID)
	}()
	agent.turnMu.Lock()
	require.NoError(t, agent.steerLocked("takeover", "steer", nil))
	agent.turnMu.Unlock()

	state := publishedACPState(t, agent)
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

// A queued prompt stays queued until its message is recorded, so a crash
// while sending it leaves it both sending and queued. Resuming the restored
// queue must not send it again.
func TestACPRestoredUncertainPromptLeavesTheQueue(t *testing.T) {
	agent := newDetachedACP(t)
	var written bytes.Buffer
	agent.stdin = testWriteCloser{Writer: &written}
	agent.restoreTranscriptLocked(ACPState{
		Sending: &ACPQueuedPrompt{ID: "sent", Text: "maybe ran"},
		Queue:   []ACPQueuedPrompt{{ID: "sent", Text: "maybe ran"}},
	})
	state := publishedACPState(t, agent)
	assert.Empty(t, state.Queue)
	assert.False(t, state.QueuePaused, "nothing is left to resume")
	assert.Equal(t, []string{"sent"}, state.Uncertain)

	require.NoError(t, agent.Command(ACPCommand{Type: "resume"}))
	agent.drain()
	assert.Empty(t, written.String(), "the uncertain prompt never reaches the agent")
	assert.Empty(t, publishedACPState(t, agent).Messages)
}

// drain skips a queued submission that must never run instead of sending it.
func TestACPDrainDropsSubmissionsThatMustNotRun(t *testing.T) {
	agent := newDetachedACP(t)
	agent.recordPath = filepath.Join(t.TempDir(), "session.json")
	var written bytes.Buffer
	agent.stdin = testWriteCloser{Writer: &written}
	agent.state.Uncertain = []string{"uncertain"}
	agent.state.Withdrawn = []string{"withdrawn"}
	agent.state.Queue = []ACPQueuedPrompt{
		{ID: "uncertain", Text: "maybe ran"},
		{ID: "withdrawn", Text: "removed"},
		{ID: "next", Text: "runs"},
	}
	agent.drain()

	assert.NotContains(t, written.String(), "maybe ran")
	assert.NotContains(t, written.String(), "removed")
	assert.Contains(t, written.String(), `"text":"runs"`)
	saved := readSavedSession(t, agent.recordPath)
	assert.Empty(t, saved.State.Queue)
	require.Len(t, saved.State.Messages, 1)
	assert.Equal(t, "next", saved.State.Messages[0].SubmissionID)
}

func TestACPRestoreDoesNotRepeatAnUncertainID(t *testing.T) {
	agent := newDetachedACP(t)
	agent.restoreTranscriptLocked(ACPState{Sending: &ACPQueuedPrompt{ID: "sent"}, Uncertain: []string{"sent"}})
	assert.Equal(t, []string{"sent"}, publishedACPState(t, agent).Uncertain)
}

// An agent that exits during a turn no prompt response will end, such as one
// it started after a steer, still ends that turn's record.
func TestACPAgentExitEndsTheRunningTurnRecord(t *testing.T) {
	c := newInProcessChat(t)
	agent := c.start(nil)
	agent.mu.Lock()
	agent.state.Busy = true
	agent.beginTurnRecordLocked("steer", 0, 0)
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
		Sending:   &ACPQueuedPrompt{ID: "second", Text: "in flight"},
		Uncertain: []string{"first"},
		Withdrawn: []string{"withdrawn"},
		Turns: []ACPTurnRecord{
			{SubmissionID: "done", StartedAt: "2026-01-01T00:00:00Z", EndedAt: "2026-01-01T00:01:00Z", StopReason: "end_turn"},
			{SubmissionID: "running", StartedAt: "2026-01-01T00:02:00Z"},
		},
	})

	state := publishedACPState(t, agent)
	assert.Nil(t, state.Sending)
	assert.Equal(t, []string{"first", "second"}, state.Uncertain)
	assert.Equal(t, []string{"withdrawn"}, state.Withdrawn)
	require.Len(t, state.Turns, 2)
	assert.Equal(t, ACPTurnRecord{SubmissionID: "done", StartedAt: "2026-01-01T00:00:00Z", EndedAt: "2026-01-01T00:01:00Z", StopReason: "end_turn"}, state.Turns[0])
	assert.Equal(t, "agent process exited during the turn", state.Turns[1].Error)
	assert.NotEmpty(t, state.Turns[1].EndedAt)
	assert.Empty(t, state.Turns[1].StopReason)
}

func TestACPTurnRecordsKeepTheNewestFifty(t *testing.T) {
	agent := newDetachedACP(t)
	agent.mu.Lock()
	for i := range acpTurnRecordLimit + 5 {
		agent.beginTurnRecordLocked("s"+strconv.Itoa(i), 0, 0)
		agent.endTurnRecordLocked("end_turn", nil)
	}
	turns := agent.state.Turns
	agent.mu.Unlock()
	require.Len(t, turns, acpTurnRecordLimit)
	assert.Equal(t, "s5", turns[0].SubmissionID)
	assert.Equal(t, "s54", turns[len(turns)-1].SubmissionID)
}

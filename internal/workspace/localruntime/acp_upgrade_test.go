package localruntime

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/ptyowner"
)

// This is the pre-protocol owner wire response, including the missing cursor
// and entry identity fields that caused the browser to reject the transcript.
type OldACPUpdate struct {
	Protocol int
	Revision uint64
	State    []byte
	Exited   bool
	ExitCode int
}
type OldACPOwner struct {
	listener net.Listener
	protocol int
	parkErr  error
	parks    atomic.Int32

	mu    sync.Mutex
	state []byte
}

func (o *OldACPOwner) Snapshot(_ struct{}, reply *OldACPUpdate) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	*reply = OldACPUpdate{Protocol: o.protocol, Revision: 1, State: o.state}
	return nil
}

func (o *OldACPOwner) Park(_ struct{}, _ *struct{}) error {
	o.parks.Add(1)
	if o.parkErr != nil {
		return o.parkErr
	}
	return o.listener.Close()
}

func (o *OldACPOwner) setState(state string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.state = []byte(state)
}

const (
	oldOwnerKey       = "workspace-chat"
	oldOwnerWorkspace = "workspace"
	idleSession       = `{"SessionID":"fixture-session","LoadSession":true,"State":{"messages":[{"role":"user","text":"remember"},{"role":"assistant","text":"Saved answer"}]}}`
	nonResumeSession  = `{"SessionID":"fixture-session","State":{"messages":[{"role":"user","text":"remember"}]}}`
)

// oldACPOwnerChat is a chat whose running owner speaks an older protocol, for
// a manager that starts real replacement owners against the ACP fixture agent.
type oldACPOwnerChat struct {
	manager  *Manager
	owner    *OldACPOwner
	restored RestoredRuntimeSession
}

func startOldACPOwner(t *testing.T, protocol int, state, saved string) *oldACPOwnerChat {
	t.Helper()
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	dir := t.TempDir()
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
	executable, err := os.Executable()
	require.NoError(t, err)
	command := []string{executable, "-test.run=^TestACPStdioHelper$"}
	manager := newACPTestManager(t, Options{ACPSessionsDir: filepath.Join(dir, "acp"), Targets: ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: command}}, nil, nil)})
	paths, err := ptyowner.NewSessionPaths(manager.acpSessionsDir, oldOwnerKey)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(paths.Dir, 0o700))
	cfg, err := json.Marshal(acpOwnerConfig{Info: SessionInfo{WorkspaceID: oldOwnerWorkspace, Key: oldOwnerKey}, Command: command, CWD: dir})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(paths.Dir, "config.json"), cfg, 0o600))
	require.NoError(t, os.WriteFile(manager.acpSessionPath(oldOwnerKey), []byte(saved), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Dir(paths.Socket), 0o700))
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", paths.Socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	old := &OldACPOwner{listener: listener, protocol: protocol, state: []byte(state)}
	server := rpc.NewServer()
	require.NoError(t, server.RegisterName("ACP", old))
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.ServeConn(conn)
		}
	}()
	return &oldACPOwnerChat{manager: manager, owner: old, restored: RestoredRuntimeSession{WorkspaceID: oldOwnerWorkspace, SessionKey: oldOwnerKey, TargetKey: "chat", Kind: LaunchTargetACP, CWD: dir}}
}

func (c *oldACPOwnerChat) open(t *testing.T) error {
	t.Helper()
	return c.manager.RestoreRuntimeSessions(t.Context(), []RestoredRuntimeSession{c.restored})
}

func (c *oldACPOwnerChat) state(t *testing.T) ACPState {
	t.Helper()
	chat, err := c.manager.ACP(oldOwnerWorkspace, oldOwnerKey)
	require.NoError(t, err)
	data, err := chat.Snapshot()
	require.NoError(t, err)
	var state ACPState
	require.NoError(t, json.Unmarshal(data, &state))
	return state
}

// sentSubmissions lists the submission IDs of the prompts the agent received.
func sentSubmissions(state ACPState) []string {
	sent := []string{}
	for _, message := range state.Messages {
		if message.SubmissionID != "" {
			sent = append(sent, message.SubmissionID)
		}
	}
	return sent
}

func TestRestoreIncompatibleACPOwner(t *testing.T) {
	const idle = `{"messages":[{"role":"user","text":"remember"}],"busy":false,"connected":true}`
	for _, tc := range []struct {
		name      string
		protocol  int
		saved     string
		state     string
		wantError string
	}{
		{name: "resumes saved conversation", protocol: 1, saved: idleSession, state: idle},
		{name: "keeps active turn", protocol: 1, saved: idleSession, state: `{"busy":true}`, wantError: "still working"},
		{name: "keeps configuring owner", protocol: 1, saved: idleSession, state: `{"configuring":true}`, wantError: "still working"},
		{name: "keeps nonresumable owner", protocol: 1, saved: nonResumeSession, state: idle, wantError: "cannot reload its session"},
		{name: "keeps nonresumable owner waiting on a person", protocol: 1, saved: nonResumeSession, state: `{"busy":true,"permissions":[{"title":"Edit file"}]}`, wantError: "cannot reload its session"},
		{name: "keeps newer owner", protocol: ACPOwnerProtocol + 1, saved: idleSession, state: `{"permissions":[{"title":"Edit file"}]}`, wantError: "newer Forge version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chat := startOldACPOwner(t, tc.protocol, tc.state, tc.saved)
			err := chat.open(t)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				assert.Zero(t, chat.owner.parks.Load())
				data, readErr := os.ReadFile(chat.manager.acpSessionPath(oldOwnerKey))
				require.NoError(t, readErr)
				assert.Equal(t, tc.saved, string(data))
				return
			}
			require.NoError(t, err)
			assert.EqualValues(t, 1, chat.owner.parks.Load())
			state := chat.state(t)
			require.Len(t, state.Messages, 2)
			assert.Equal(t, "remember", state.Messages[0].Text)
			assert.Equal(t, "Saved answer", state.Messages[1].Text)
			assert.NotEmpty(t, state.Messages[0].ID)
			assert.NotEmpty(t, state.LastMessageID)
			assert.Empty(t, state.Notices)
			calls, err := os.ReadFile(filepath.Join(chat.restored.CWD, "sessions"))
			require.NoError(t, err)
			assert.Equal(t, "session/load\n", string(calls), "must resume the native conversation, never create a new one")
		})
	}
}

// Opening a chat parked its pending requests away with the old agent: nobody
// could ever answer them through this daemon. The reload keeps the conversation
// and leaves one notice naming them.
func TestOpeningReplacesAnOwnerWaitingOnAPerson(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      string
		wantNotice string
	}{
		{
			name:       "permission",
			state:      `{"busy":true,"permissions":[{"id":"p-1","title":"Edit file"}]}`,
			wantNotice: `The Forge upgrade cancelled an unanswered question: "Edit file"`,
		},
		{
			name:       "permission without a title",
			state:      `{"busy":true,"permissions":[{"id":"p-1"}]}`,
			wantNotice: `The Forge upgrade cancelled an unanswered question: "Agent requests permission"`,
		},
		{
			name:       "elicitation",
			state:      `{"busy":true,"elicitations":[{"id":"e-1","message":"Pick one"}]}`,
			wantNotice: `The Forge upgrade cancelled an unanswered question: "Pick one"`,
		},
		{
			name:       "several requests share one notice",
			state:      `{"busy":true,"permissions":[{"id":"p-1","title":"Edit file"}],"elicitations":[{"id":"e-1","message":"Pick one"}]}`,
			wantNotice: `The Forge upgrade cancelled unanswered questions: "Edit file", "Pick one"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chat := startOldACPOwner(t, 1, tc.state, idleSession)

			require.NoError(t, chat.open(t))

			assert.EqualValues(t, 1, chat.owner.parks.Load())
			state := chat.state(t)
			assert.True(t, state.Connected)
			assert.False(t, state.Busy)
			assert.Empty(t, state.Permissions)
			assert.Empty(t, state.Elicitations)
			assert.Empty(t, state.Error)
			assert.Equal(t, []string{tc.wantNotice}, state.Notices)
			require.Len(t, state.Messages, 2)
			assert.Equal(t, "Saved answer", state.Messages[1].Text)
			calls, err := os.ReadFile(filepath.Join(chat.restored.CWD, "sessions"))
			require.NoError(t, err)
			assert.Equal(t, "session/load\n", string(calls))
		})
	}
}

// The fixture agent ends a "delegate" turn at once, so the queue drains one
// prompt per turn. Other text starts a turn that keeps running.
func TestOpeningDeliversQueuedPromptsInOrderWithoutDuplicates(t *testing.T) {
	const saved = `{"SessionID":"fixture-session","LoadSession":true,"State":{` +
		`"messages":[{"role":"user","text":"delegate","submissionId":"ran"}],` +
		`"queue":[{"id":"q-1","text":"delegate","sender":{"kind":"user"}},{"id":"q-2","text":"delegate","sender":{"kind":"user"}}],` +
		`"withdrawn":["removed"]}}`
	const oldState = `{"queuePaused":true,` +
		`"steers":[],` +
		`"queue":[{"id":"ran","text":"delegate"},{"id":"q-1","text":"delegate"},{"id":"q-2","text":"delegate"},{"id":"removed","text":"delegate"},{"id":"unsaved","text":"delegate"}]}`
	chat := startOldACPOwner(t, 1, oldState, saved)

	require.NoError(t, chat.open(t))

	require.Eventually(t, func() bool {
		state := chat.state(t)
		return !state.Busy && len(state.Queue) == 0
	}, 10*time.Second, 10*time.Millisecond)
	state := chat.state(t)
	assert.Equal(t, []string{"ran", "q-1", "q-2", "unsaved"}, sentSubmissions(state))
	assert.False(t, state.QueuePaused)
	assert.Empty(t, state.Error)
	assert.Empty(t, state.Notices)
}

// The old owner's published queue is the one people last saw, so it wins over
// a saved queue that missed later changes. Its prompts carry no sender, as an
// owner from before senders were recorded publishes them.
func TestOpeningFollowsTheQueueTheOldOwnerPublished(t *testing.T) {
	for _, tc := range []struct {
		name     string
		saved    string
		oldState string
		want     []string
	}{
		{
			name:     "an unsaved move",
			saved:    `"queue":[{"id":"q-1","text":"delegate","sender":{"kind":"user"}},{"id":"q-2","text":"delegate","sender":{"kind":"user"}}]`,
			oldState: `{"queue":[{"id":"q-2","text":"delegate"},{"id":"q-1","text":"delegate"}]}`,
			want:     []string{"q-2", "q-1"},
		},
		{
			name:  "an unsaved steer",
			saved: `"queue":[{"id":"q-1","text":"delegate","sender":{"kind":"user"}}]`,
			// An owner holds steers only while it steers a turn, so only a turn
			// waiting on a person lets it be replaced.
			oldState: `{"busy":true,"steering":true,"permissions":[{"id":"p-1","title":"Edit file"}],` +
				`"steers":[{"id":"s-1","text":"delegate"}],"queue":[{"id":"q-1","text":"delegate"}]}`,
			want: []string{"s-1", "q-1"},
		},
		{
			name:     "an unsaved removal",
			saved:    `"queue":[{"id":"gone","text":"delegate","sender":{"kind":"user"}},{"id":"q-1","text":"delegate","sender":{"kind":"user"}}]`,
			oldState: `{"queue":[{"id":"q-1","text":"delegate"}],"withdrawn":["gone"]}`,
			want:     []string{"q-1"},
		},
		{
			name:     "an unsaved removal of the only prompt",
			saved:    `"queue":[{"id":"gone","text":"delegate","sender":{"kind":"user"}}]`,
			oldState: `{"withdrawn":["gone"]}`,
			want:     []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chat := startOldACPOwner(t, 1, tc.oldState, `{"SessionID":"fixture-session","LoadSession":true,"State":{`+tc.saved+`}}`)

			require.NoError(t, chat.open(t))

			require.Eventually(t, func() bool {
				state := chat.state(t)
				return !state.Busy && len(state.Queue) == 0
			}, 10*time.Second, 10*time.Millisecond)
			state := chat.state(t)
			assert.Equal(t, tc.want, sentSubmissions(state))
			for _, turn := range state.Turns {
				assert.Equal(t, ACPSenderUser, turn.Sender.Kind, turn.SubmissionID)
			}
		})
	}
}

// A prompt the new agent cannot take stays visible with its reason, and later
// prompts wait behind it rather than running out of order.
func TestOpeningMarksAnUndeliverableQueuedPromptNotSent(t *testing.T) {
	const saved = `{"SessionID":"fixture-session","LoadSession":true,"State":{"queue":[` +
		`{"id":"image","text":"see this","images":[{"type":"image","mimeType":"image/png","data":"AAAA"}],"sender":{"kind":"user"}},` +
		`{"id":"after","text":"delegate","sender":{"kind":"user"}}]}}`
	const oldState = `{"queuePaused":true,"queue":[{"id":"image","text":"see this","images":[{"type":"image","mimeType":"image/png","data":"AAAA"}]},{"id":"after","text":"delegate"}]}`
	chat := startOldACPOwner(t, 1, oldState, saved)

	require.NoError(t, chat.open(t))

	require.Eventually(t, func() bool {
		queue := chat.state(t).Queue
		return len(queue) == 2 && queue[0].Refused != ""
	}, 10*time.Second, 10*time.Millisecond)
	state := chat.state(t)
	assert.Equal(t, "this agent does not accept image prompts", state.Queue[0].Refused)
	assert.Empty(t, state.Queue[1].Refused)
	assert.True(t, state.QueuePaused)
	assert.Empty(t, state.Error, "the prompt reports its own failure")
	assert.Empty(t, sentSubmissions(state))

	// Removing it, as Edit does, lets the rest run once a person resumes.
	agent, err := chat.manager.ACP(oldOwnerWorkspace, oldOwnerKey)
	require.NoError(t, err)
	require.NoError(t, agent.Command(ACPCommand{Type: "unqueue", ID: "image"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "resume"}))
	require.Eventually(t, func() bool { return len(sentSubmissions(chat.state(t))) == 1 }, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"after"}, sentSubmissions(chat.state(t)))
}

func TestOpeningRetriesAfterAReloadFails(t *testing.T) {
	const oldState = `{"busy":true,"permissions":[{"title":"Edit file"}],"queue":[{"id":"q-1","text":"delegate"}]}`
	const saved = `{"SessionID":"fixture-session","LoadSession":true,"State":{"queue":[{"id":"q-1","text":"delegate","sender":{"kind":"user"}}]}}`
	chat := startOldACPOwner(t, 1, oldState, saved)
	t.Setenv("KENN_FORGE_ACP_NO_LOAD", "1")

	err := chat.open(t)

	require.ErrorIs(t, err, ErrACPCannotReload)
	require.ErrorContains(t, err, "reload this chat after the Forge upgrade")
	assert.EqualValues(t, 1, chat.owner.parks.Load())
	data, readErr := os.ReadFile(chat.manager.acpSessionPath(oldOwnerKey))
	require.NoError(t, readErr)
	assert.Equal(t, saved, string(data), "the saved session waits for the next attempt")

	// The owner is gone, so the next open only reloads; what the old owner
	// left still arrives.
	t.Setenv("KENN_FORGE_ACP_NO_LOAD", "")
	require.NoError(t, chat.open(t))
	state := chat.state(t)
	assert.Equal(t, []string{`The Forge upgrade cancelled an unanswered question: "Edit file"`}, state.Notices)
	require.Eventually(t, func() bool { return len(sentSubmissions(chat.state(t))) == 1 }, 10*time.Second, 10*time.Millisecond)
}

func TestOpeningReportsAnOwnerThatWillNotPark(t *testing.T) {
	chat := startOldACPOwner(t, 1, `{"permissions":[{"title":"Edit file"}]}`, idleSession)
	chat.owner.parkErr = errors.New("agent would not stop")

	err := chat.open(t)

	require.ErrorContains(t, err, "stop the agent from the previous Forge version")
	require.ErrorContains(t, err, "agent would not stop")
}

// Each open retries, so a running turn recovers on the first open after it ends.
func TestOpeningReplacesABusyOwnerOnceItFinishes(t *testing.T) {
	chat := startOldACPOwner(t, 1, `{"busy":true,"queue":[{"id":"q-1","text":"delegate"}]}`, idleSession)
	require.ErrorContains(t, chat.open(t), "still working")
	require.Zero(t, chat.owner.parks.Load())

	chat.owner.setState(`{"queue":[{"id":"q-1","text":"delegate"}],"queuePaused":true}`)

	require.NoError(t, chat.open(t))
	assert.EqualValues(t, 1, chat.owner.parks.Load())
	require.Eventually(t, func() bool { return len(sentSubmissions(chat.state(t))) == 1 }, 10*time.Second, 10*time.Millisecond)
}

// The handoff file may hold the only copy of a prompt, so it stays until the
// queue that holds that prompt is saved, and an owner that cannot save it
// refuses to start rather than run with a handoff that would apply again.
func TestApplyOwnerUpgradeKeepsTheHandoffUntilItsPromptsAreSaved(t *testing.T) {
	dir := t.TempDir()
	handoff := filepath.Join(dir, acpOwnerUpgradeFile)
	data, err := json.Marshal(acpOwnerUpgrade{Queue: []ACPQueuedPrompt{{ID: "unsaved", Text: "next", Sender: ACPSender{Kind: ACPSenderUser}}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(handoff, data, 0o600))
	agent := newDetachedACP(t)
	agent.state.Connected = false

	agent.recordPath = filepath.Join(dir, "missing", "session.json")
	require.ErrorContains(t, agent.applyOwnerUpgrade(dir), "save this chat after the Forge upgrade")

	assert.FileExists(t, handoff)

	agent.recordPath = filepath.Join(dir, "session.json")
	require.NoError(t, agent.applyOwnerUpgrade(dir))

	assert.NoFileExists(t, handoff)
	data, err = os.ReadFile(agent.recordPath)
	require.NoError(t, err)
	var saved acpSavedSession
	require.NoError(t, json.Unmarshal(data, &saved))
	require.Len(t, saved.State.Queue, 1, "applying the handoff again must not queue the prompt twice")
	assert.Equal(t, "unsaved", saved.State.Queue[0].ID)
}

// An unexpected send failure shows a plain reason on the prompt.
func TestDrainRefusesAQueuedPromptWithAPlainReason(t *testing.T) {
	failedReader, failedWriter := io.Pipe()
	require.NoError(t, failedReader.Close())
	peerReader, peerWriter := io.Pipe()
	t.Cleanup(func() { _ = peerWriter.Close(); _ = peerReader.Close() })
	agent := &ACP{
		stdin: failedWriter, done: make(chan struct{}),
		subscribers: make(map[chan struct{}]struct{}),
		state:       ACPState{Connected: true, Queue: []ACPQueuedPrompt{{ID: "q-1", Text: "next", Sender: ACPSender{Kind: ACPSenderUser}}}},
	}
	agent.client = acpsdk.NewClientSideConnection(agent, agent, peerReader)

	agent.drain()

	state := publishedACPState(t, agent)
	require.Len(t, state.Queue, 1)
	assert.Equal(t, "could not send this message to the agent", state.Queue[0].Refused)
	assert.True(t, state.QueuePaused)
	assert.Empty(t, state.Error)
}

func TestReloadableChatDoesNotLoseNativeContext(t *testing.T) {
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_NO_LOAD", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	_, err = startACPSession(t.Context(), []string{executable, "-test.run=^TestACPStdioHelper$"}, t.TempDir(), nil, nil, nil, &acpSavedSession{SessionID: "fixture-session", LoadSession: true})
	require.ErrorIs(t, err, ErrACPCannotReload)
}

package localruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
)

const claudeTranscriptFixture = `{"type":"permission-mode","sessionId":"s1"}
{"type":"user","timestamp":"2026-01-02T03:04:05Z","message":{"role":"user","content":"Fix the login bug"}}
{"type":"user","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Injected context"}]}}
{"type":"assistant","timestamp":"2026-01-02T03:04:06Z","message":{"id":"msg1","role":"assistant","content":[{"type":"thinking","thinking":"Look at auth"},{"type":"text","text":"Checking the handler."},{"type":"tool_use","id":"call1","name":"Bash","input":{"command":"go test ./auth\nsecond line"}}]}}
{"type":"assistant","isSidechain":true,"message":{"id":"sub","role":"assistant","content":[{"type":"text","text":"Subagent text"}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call1","is_error":true,"content":"FAIL auth"}]}}
{"type":"user","message":{"role":"user","content":"<command-name>/review</command-name>\n<command-message>review</command-message>\n<command-args>the diff</command-args>"}}
{"type":"user","message":{"role":"user","content":"<local-command-stdout>done</local-command-stdout>"}}
`

const codexTranscriptFixture = `{"timestamp":"2026-01-02T03:04:05Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>injected</environment_context>"}]}}
{"timestamp":"2026-01-02T03:04:05Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"Text","text":"Fix the login bug"}]}}}
{"timestamp":"2026-01-02T03:04:06Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"Reasoning","id":"r1","summary_text":["Look at auth"]}}}
{"timestamp":"2026-01-02T03:04:07Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"c1","command":["bash","-lc","go test ./auth"],"aggregated_output":"FAIL auth","exit_code":1,"status":"failed"}}}
{"timestamp":"2026-01-02T03:04:08Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"McpToolCall","id":"m1","server":"forge","tool":"list_prs","arguments":{"state":"open"},"status":"completed","result":{"content":[]}}}}
{"timestamp":"2026-01-02T03:04:09Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"a1","content":[{"type":"Text","text":"Fixed."}]}}}
`

type mirrorFixture struct {
	mu         sync.Mutex
	status     ACPMirrorStatus
	delivered  []ACPCommand
	deliverErr error
	starts     int
	live       *recordingChat
}

func (f *mirrorFixture) setStatus(status ACPMirrorStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func newMirrorFixture(t *testing.T, agent, transcript string, status ACPMirrorStatus) (*Manager, *DormantACP, *mirrorFixture, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(transcript), 0o600))
	manager := newACPTestManager(t, Options{ACPSessionsDir: t.TempDir()})
	fixture := &mirrorFixture{status: status, live: &recordingChat{}}
	chat := manager.MirrorACP(ACPMirror{
		Key: "chat", Agent: agent, SessionID: "session-1", TranscriptPath: path,
		Status: func() ACPMirrorStatus {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			return fixture.status
		},
		Deliver: func(command ACPCommand) error {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			fixture.delivered = append(fixture.delivered, command)
			return fixture.deliverErr
		},
		Start: func() (ACPChat, error) {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			fixture.starts++
			return fixture.live, nil
		},
	})
	return manager, chat, fixture, path
}

func TestMirrorShowsWhatAClaudeSessionShowsItsUser(t *testing.T) {
	_, chat, _, _ := newMirrorFixture(t, "claude", claudeTranscriptFixture, ACPMirrorStatus{Running: true})
	messages := dormantState(t, chat).Messages
	require.Len(t, messages, 5)
	assert := assert.New(t)

	assert.Equal("user", messages[0].Role)
	assert.Equal("Fix the login bug", messages[0].Text)
	assert.Equal(ACPSenderUser, messages[0].Sender.Kind)
	assert.Equal("2026-01-02T03:04:05Z", messages[0].CreatedAt)

	assert.Equal("thought", messages[1].Role)
	assert.Equal("Look at auth", messages[1].Text)
	assert.Equal("assistant", messages[2].Role)
	assert.Equal("Checking the handler.", messages[2].Text)
	assert.Equal("msg1", messages[2].MessageID)

	tool := messages[3]
	assert.Equal("tool", tool.Role)
	assert.Equal("Bash go test ./auth", tool.Text)
	assert.Equal("execute", tool.Kind)
	assert.Equal("failed", tool.Status, "the tool's result updates its entry")
	require.Len(t, tool.ToolContent, 1)
	assert.Equal("FAIL auth", tool.ToolContent[0].Content.Text)

	assert.Equal("/review the diff", messages[4].Text, "a slash command reads as typed")
}

func TestMirrorShowsACodexSessionsConversationWithoutInjectedContext(t *testing.T) {
	_, chat, _, _ := newMirrorFixture(t, "codex", codexTranscriptFixture, ACPMirrorStatus{Running: true})
	messages := dormantState(t, chat).Messages
	require.Len(t, messages, 5)
	assert := assert.New(t)
	assert.Equal("user", messages[0].Role)
	assert.Equal("Fix the login bug", messages[0].Text)
	assert.Equal("thought", messages[1].Role)
	assert.Equal("go test ./auth", messages[2].Text)
	assert.Equal("failed", messages[2].Status)
	assert.Equal("FAIL auth", messages[2].ToolContent[0].Content.Text)
	assert.Equal("forge list_prs", messages[3].Text)
	assert.Equal("completed", messages[3].Status)
	assert.Equal("Fixed.", messages[4].Text)
}

func TestMirrorKeepsEntryIdentityAcrossReads(t *testing.T) {
	_, first, _, _ := newMirrorFixture(t, "claude", claudeTranscriptFixture, ACPMirrorStatus{Running: true})
	_, second, _, _ := newMirrorFixture(t, "claude", claudeTranscriptFixture, ACPMirrorStatus{Running: true})
	firstMessages, secondMessages := dormantState(t, first).Messages, dormantState(t, second).Messages
	require.Len(t, secondMessages, len(firstMessages))
	for i := range firstMessages {
		assert.Equal(t, firstMessages[i].ID, secondMessages[i].ID)
	}
}

// Agents append a record in more than one write, so a line is applied only once
// it is complete, and a watching client hears about it.
func TestMirrorAppliesAppendedLinesOnceTheyAreComplete(t *testing.T) {
	_, chat, _, path := newMirrorFixture(t, "claude", "", ACPMirrorStatus{Running: true})
	assert.Empty(t, dormantState(t, chat).Messages)
	changes, unsubscribe := chat.Subscribe()
	defer unsubscribe()
	<-changes

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer file.Close()
	line := `{"type":"user","message":{"role":"user","content":"typed in the other terminal"}}`
	_, err = file.WriteString(line[:20])
	require.NoError(t, err)
	chat.pollMirror()
	assert.Empty(t, dormantState(t, chat).Messages, "half a record is not a message")

	_, err = file.WriteString(line[20:] + "\n")
	require.NoError(t, err)
	chat.pollMirror()
	select {
	case <-changes:
	default:
		assert.Fail(t, "a new message did not notify watchers")
	}
	messages := dormantState(t, chat).Messages
	require.Len(t, messages, 1)
	assert.Equal(t, "typed in the other terminal", messages[0].Text)
}

func TestMirrorReadsNothingForAnAgentWithoutAReader(t *testing.T) {
	_, chat, _, _ := newMirrorFixture(t, "gemini", claudeTranscriptFixture, ACPMirrorStatus{Running: true})
	assert.Empty(t, dormantState(t, chat).Messages)
}

// While the agent runs, input goes to its terminal and nothing starts a second
// process on its session.
func TestRunningMirrorDeliversInputAndNeverStartsAnAgent(t *testing.T) {
	_, chat, fixture, _ := newMirrorFixture(t, "claude", "", ACPMirrorStatus{Running: true, Writable: true, Busy: true})
	state := dormantState(t, chat)
	assert.True(t, state.Busy)
	assert.False(t, state.ExternalReadOnly)

	require.NoError(t, chat.Command(ACPCommand{Type: "prompt", ID: "p1", Text: "also check logout"}))
	require.NoError(t, chat.Command(ACPCommand{Type: "cancel"}))

	assert.Zero(t, fixture.starts)
	require.Len(t, fixture.delivered, 2)
	assert.Equal(t, "also check logout", fixture.delivered[0].Text)
	assert.Equal(t, "cancel", fixture.delivered[1].Type)
}

func TestRunningMirrorRefusesWhatItCannotDeliver(t *testing.T) {
	refused := map[string]ACPCommand{
		"a setting":     {Type: "config", ID: "model", Value: "fast"},
		"an image":      {Type: "prompt", Text: "look", Images: []ACPContent{{Type: "image", Data: "x"}}},
		"a coordinator": {Type: "prompt", Text: "go", Generation: 3},
	}
	for name, command := range refused {
		t.Run(name, func(t *testing.T) {
			_, chat, fixture, _ := newMirrorFixture(t, "claude", "", ACPMirrorStatus{Running: true, Writable: true})
			require.Error(t, chat.Command(command))
			assert.Empty(t, fixture.delivered)
			assert.Zero(t, fixture.starts)
		})
	}
}

func TestDeliveryFailureReachesTheSender(t *testing.T) {
	_, chat, fixture, _ := newMirrorFixture(t, "claude", "", ACPMirrorStatus{Running: true, Writable: true})
	fixture.deliverErr = errors.New("agent_blocked")
	assert.ErrorContains(t, chat.Command(ACPCommand{Type: "prompt", Text: "hello"}), "agent_blocked")
}

// An agent in a terminal Forge cannot type into still streams, and takes no input.
func TestMirrorInAnUnreachableTerminalIsReadOnly(t *testing.T) {
	_, chat, fixture, _ := newMirrorFixture(t, "claude", claudeTranscriptFixture, ACPMirrorStatus{Running: true})
	state := dormantState(t, chat)
	assert.True(t, state.ExternalReadOnly)
	assert.NotEmpty(t, state.Messages)

	require.ErrorIs(t, chat.Command(ACPCommand{Type: "prompt", Text: "hello"}), ErrACPMirrorReadOnly)
	assert.Empty(t, fixture.delivered)
	assert.Zero(t, fixture.starts)
}

// Once the agent exits, the next prompt starts a Forge-run agent that loads the
// same session, and the chat keeps the transcript it showed.
func TestExitedMirrorHandsTheSessionToAForgeRunAgent(t *testing.T) {
	manager, chat, fixture, _ := newMirrorFixture(t, "claude", claudeTranscriptFixture, ACPMirrorStatus{Running: true, Writable: true})
	before := dormantState(t, chat).Messages
	fixture.setStatus(ACPMirrorStatus{})
	chat.pollMirror()
	state := dormantState(t, chat)
	assert.False(t, state.Busy)
	assert.False(t, state.ExternalReadOnly)
	assert.True(t, state.Dormant)

	require.NoError(t, chat.Command(ACPCommand{Type: "prompt", ID: "p1", Text: "continue here"}))

	assert.Equal(t, 1, fixture.starts)
	assert.Empty(t, fixture.delivered, "the exited agent's terminal gets nothing")
	require.Len(t, fixture.live.commands, 1)
	assert.Equal(t, "continue here", fixture.live.commands[0].Text)

	data, err := os.ReadFile(manager.acpSessionPath("chat"))
	require.NoError(t, err)
	var saved acpSavedSession
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, "session-1", saved.SessionID)
	assert.True(t, saved.LoadSession)
	require.Len(t, saved.State.Messages, len(before))
	assert.Equal(t, before[0].ID, saved.State.Messages[0].ID)
}

// A browser that lost the acknowledgment of a prompt resends it on reconnect.
// When the agent has the prompt but exited meanwhile, the resend is accepted
// and starts nothing, so the agent that continues the session never runs it again.
func TestExitedMirrorTakesADeliveredPromptWithoutStarting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(claudeTranscriptFixture), 0o600))
	manager := newACPTestManager(t, Options{ACPSessionsDir: t.TempDir()})
	starts := 0
	chat := manager.MirrorACP(ACPMirror{
		Key: "chat", Agent: "claude", SessionID: "session-1", TranscriptPath: path,
		Status:    func() ACPMirrorStatus { return ACPMirrorStatus{} },
		Start:     func() (ACPChat, error) { starts++; return &recordingChat{}, nil },
		Delivered: func(id string) bool { return id == "p1" },
	})

	require.NoError(t, chat.Command(ACPCommand{Type: "prompt", ID: "p1", Text: "typed into the pane"}))

	assert.Zero(t, starts)
	assert.Nil(t, chat.Live())
}

// startRealMirror returns a mirror of an exited agent whose handoff starts a
// real owner on the fixture agent, and the fixture's directory.
func startRealMirror(t *testing.T, agent, transcriptText string) (*DormantACP, string) {
	t.Helper()
	return startRealMirrorWithStatus(t, agent, transcriptText, ACPMirrorStatus{})
}

func startRealMirrorWithStatus(t *testing.T, agent, transcriptText string, status ACPMirrorStatus) (*DormantACP, string) {
	t.Helper()
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	dir := t.TempDir()
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
	executable, err := os.Executable()
	require.NoError(t, err)
	owner := &ptyowner.Client{Root: filepath.Join(t.TempDir(), "pty-owner"), InProcess: true}
	targetKey := agent + "-chat"
	manager := newACPTestManager(t, Options{
		ACPSessionsDir: filepath.Join(dir, "acp"), PtyOwnerRuntime: ptyownerruntime.New(owner, nil),
		Targets: ResolveLaunchTargets([]config.Agent{{Key: targetKey, Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil),
	})
	key, err := NewSessionKey("workspace")
	require.NoError(t, err)
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(transcript, []byte(transcriptText), 0o600))
	chat := manager.MirrorACP(ACPMirror{
		Key: key, Agent: agent, SessionID: "fixture-session", TranscriptPath: transcript,
		Status: func() ACPMirrorStatus { return status },
		Start: func() (ACPChat, error) {
			if err := manager.PrepareMirroredACP(t.Context(), targetKey, "workspace", key, dir); err != nil {
				return nil, err
			}
			err := manager.RestoreRuntimeSessions(t.Context(), []RestoredRuntimeSession{{
				WorkspaceID: "workspace", SessionKey: key, TargetKey: targetKey, Kind: LaunchTargetACP, CWD: dir, CreatedAt: time.Now().UTC(),
			}})
			if err != nil {
				return nil, err
			}
			return manager.ACP("workspace", key)
		},
	})
	t.Cleanup(func() { _ = manager.Stop(context.WithoutCancel(t.Context()), "workspace", key) })
	return chat, dir
}

// awaitIdleMessages waits for the started chat to finish its turn with count
// messages.
func awaitIdleMessages(t *testing.T, chat *DormantACP, count int) ACPState {
	t.Helper()
	live := chat.Live()
	require.NotNil(t, live)
	var state ACPState
	require.Eventually(t, func() bool {
		data, err := live.Snapshot()
		return err == nil && json.Unmarshal(data, &state) == nil && !state.Busy && len(state.Messages) == count
	}, 5*time.Second, 10*time.Millisecond)
	return state
}

// A mirrored chat never launched an agent, so the handoff runs on a real owner
// and agent: nothing else proves the owner finds what it needs to load the session.
func TestExitedMirrorLoadsItsSessionInARealAgent(t *testing.T) {
	assert := assert.New(t)
	chat, dir := startRealMirror(t, "claude", claudeTranscriptFixture)
	mirrored := dormantState(t, chat).Messages
	require.NotEmpty(t, mirrored)

	require.NoError(t, chat.Command(ACPCommand{Type: "prompt", ID: "p1", Text: "continue here"}))

	state := awaitIdleMessages(t, chat, len(mirrored)+2)
	log, err := os.ReadFile(filepath.Join(dir, "sessions"))
	require.NoError(t, err)
	assert.Equal("session/load\n", string(log), "the agent loads the session and opens no other")
	assert.Equal(mirrored[0].ID, state.Messages[0].ID)
	assert.Equal("continue here", state.Messages[len(mirrored)].Text)
	assert.NotContains(messageTexts(state), "restored answer", "the mirrored transcript already holds the history")
}

// When the chat read no transcript, the history the agent replays on load is the
// only record of the earlier conversation.
func TestExitedMirrorWithoutATranscriptShowsTheAgentsHistory(t *testing.T) {
	for name, agent := range map[string]string{
		"an agent Forge reads no transcript of": "gemini",
		"a transcript with nothing in it":       "claude",
	} {
		t.Run(name, func(t *testing.T) {
			chat, _ := startRealMirror(t, agent, "")
			require.Empty(t, dormantState(t, chat).Messages)

			require.NoError(t, chat.Command(ACPCommand{Type: "prompt", ID: "p1", Text: "continue here"}))

			state := awaitIdleMessages(t, chat, 4)
			assert.Equal(t, []string{"remember", "restored answer", "continue here"}, messageTexts(state)[:3])
			assert.Equal(t, []ACPContent{{Type: "image", MimeType: "image/png", Data: "iVBORw0KGgo="}}, state.Messages[0].Images,
				"the replayed prompt shows its image")
		})
	}
}

// The chat read no transcript, so it cannot tell whether the agent had a
// conversation. When the agent cannot find the session, a prompted agent's
// conversation would be lost, so the start fails; a never-prompted one has
// nothing to lose and starts a new session.
func TestExitedMirrorWithoutATranscriptStartsFreshOnlyIfNeverPrompted(t *testing.T) {
	t.Run("prompted", func(t *testing.T) {
		t.Setenv("KENN_FORGE_ACP_LOAD_NOT_FOUND", "1")
		chat, _ := startRealMirrorWithStatus(t, "gemini", "", ACPMirrorStatus{Prompted: true})
		require.ErrorContains(t, chat.Command(ACPCommand{Type: "prompt", ID: "p1", Text: "continue here"}), "Resource not found")
		assert.Nil(t, chat.Live())
	})
	t.Run("never prompted", func(t *testing.T) {
		t.Setenv("KENN_FORGE_ACP_LOAD_NOT_FOUND", "1")
		chat, _ := startRealMirrorWithStatus(t, "gemini", "", ACPMirrorStatus{})
		require.NoError(t, chat.Command(ACPCommand{Type: "prompt", ID: "p1", Text: "continue here"}))
		assert.NotNil(t, chat.Live())
	})
}

func messageTexts(state ACPState) []string {
	texts := make([]string, 0, len(state.Messages))
	for _, message := range state.Messages {
		texts = append(texts, message.Text)
	}
	return texts
}

func TestMirrorThatCannotStartKeepsShowingItsTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(claudeTranscriptFixture), 0o600))
	manager := newACPTestManager(t, Options{ACPSessionsDir: t.TempDir()})
	chat := manager.MirrorACP(ACPMirror{
		Key: "chat", Agent: "claude", SessionID: "session-1", TranscriptPath: path,
		Status: func() ACPMirrorStatus { return ACPMirrorStatus{} },
		Start:  func() (ACPChat, error) { return nil, errors.New("no chat agent") },
	})
	require.ErrorContains(t, chat.Command(ACPCommand{Type: "prompt", Text: "go"}), "no chat agent")
	assert.NotEmpty(t, dormantState(t, chat).Messages)
	assert.Nil(t, chat.Live())
}

// A transcript path a hook reports after the chat opened is read from then on.
func TestMirrorReadsATranscriptReportedAfterItOpened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(claudeTranscriptFixture), 0o600))
	manager := newACPTestManager(t, Options{ACPSessionsDir: t.TempDir()})
	var mu sync.Mutex
	reported := ""
	chat := manager.MirrorACP(ACPMirror{
		Key: "chat", Agent: "claude", SessionID: "session-1",
		Status: func() ACPMirrorStatus {
			mu.Lock()
			defer mu.Unlock()
			return ACPMirrorStatus{Running: true, TranscriptPath: reported}
		},
	})
	require.Empty(t, dormantState(t, chat).Messages)

	mu.Lock()
	reported = path
	mu.Unlock()
	chat.pollMirror()

	assert.Len(t, dormantState(t, chat).Messages, 5)
}

// Every open view of a session gets its own chat. Once one view starts the
// Forge-run agent, the others follow that agent instead of the transcript.
func TestMirrorFollowsAnAgentAnotherViewStarted(t *testing.T) {
	manager := newACPTestManager(t, Options{ACPSessionsDir: t.TempDir()})
	live := &recordingChat{}
	var running atomic.Bool
	chat := manager.MirrorACP(ACPMirror{
		Key: "chat", Agent: "claude", SessionID: "session-1",
		Status: func() ACPMirrorStatus { return ACPMirrorStatus{} },
		Start:  func() (ACPChat, error) { return nil, errors.New("a second agent started on the session") },
		Live: func() ACPChat {
			if running.Load() {
				return live
			}
			return nil
		},
	})
	following := make(chan struct{})
	go func() {
		defer close(following)
		chat.Follow(t.Context())
	}()

	running.Store(true)
	select {
	case <-chat.Started():
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the view never followed the started agent")
	}
	<-following
	assert.Same(t, live, chat.Live())
	require.NoError(t, chat.Command(ACPCommand{Type: "prompt", ID: "p1", Text: "continue"}))
	live.mu.Lock()
	defer live.mu.Unlock()
	assert.Len(t, live.commands, 1)
}

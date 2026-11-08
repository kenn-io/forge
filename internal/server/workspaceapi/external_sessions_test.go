package workspaceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/agentprocess"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/projects"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/testutil/workspacetest"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

type externalSessionFixture struct {
	database   *db.DB
	handler    *Handler
	mux        *http.ServeMux
	repoID     int64
	worktree   string
	head       string
	transcript string
}

const externalClaudeTranscript = `{"type":"user","timestamp":"2026-01-02T03:04:05Z","message":{"role":"user","content":"Open a pull request"}}
{"type":"assistant","timestamp":"2026-01-02T03:04:06Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"Opened it."}]}}
`

func newExternalSessionFixture(t *testing.T) *externalSessionFixture {
	t.Helper()
	require := require.New(t)
	database := dbtest.Open(t)
	repoID, err := reposeed.Seed(t.Context(), database, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: "widget",
	})
	require.NoError(err)

	repository := gitfixture.NewRepository(t, false)
	gitfixture.Run(t, repository.Dir, "checkout", "-b", "feature/login")
	repository.Write(t, "login.txt", "login\n")
	repository.Stage(t, "login.txt")
	gitfixture.Run(t, repository.Dir, "commit", "-m", "login")
	gitfixture.Run(t, repository.Dir, "remote", "add", "origin", "git@github.com:acme/widget.git")
	worktree, err := filepath.EvalSymlinks(repository.Dir)
	require.NoError(err)
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(os.WriteFile(transcript, []byte(externalClaudeTranscript), 0o600))

	resolver := stubLaunchSpecResolver{}
	manager := workspacetest.NewManager(t, database, t.TempDir())
	manager.SetLaunchSpecResolver(resolver)
	tmux := filepath.Join(t.TempDir(), "tmux")
	require.NoError(os.WriteFile(tmux, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	manager.SetTmuxCommand([]string{tmux})
	handler := New(Deps{
		DB: database, Workspaces: manager, LaunchSpecResolver: resolver,
		Runtime:            localruntime.NewManager(localruntime.Options{ACPSessionsDir: t.TempDir()}),
		AgentActivity:      agentactivity.NewStore(t.TempDir()),
		EnrichmentDisabled: true,
	})
	handler.ApplyConfig(ConfigSnapshot{KnownPlatformHosts: []projects.KnownPlatformHost{{Platform: "github", Host: "github.com"}}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		require.NoError(handler.Shutdown(ctx))
	})
	mux := http.NewServeMux()
	handler.Register(humago.NewWithPrefix(mux, "/api/v1", huma.DefaultConfig("workspace test", "1")))
	return &externalSessionFixture{
		database: database, handler: handler, mux: mux, repoID: repoID, worktree: worktree,
		head:       strings.TrimSpace(string(gitfixture.Run(t, repository.Dir, "rev-parse", "HEAD"))),
		transcript: transcript,
	}
}

func (f *externalSessionFixture) hook(t *testing.T, agent, session, event string, headers map[string]string) {
	t.Helper()
	f.hookWith(t, agent, session, event, nil, headers)
}

// hookWith posts a hook event whose body also carries fields; the hook ignores
// fields its event does not use.
func (f *externalSessionFixture) hookWith(t *testing.T, agent, session, event string, fields map[string]any, headers map[string]string) {
	t.Helper()
	payload := map[string]any{
		"session_id": session, "cwd": f.worktree, "hook_event_name": event, "transcript_path": f.transcript,
	}
	maps.Copy(payload, fields)
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/agent-hooks/"+agent, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	f.mux.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

func (f *externalSessionFixture) openPull(t *testing.T, branch, head string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := f.database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: f.repoID, PlatformID: 4200, Number: 42, URL: "https://github.com/acme/widget/pull/42",
		Title: "Fix login", Author: "alice", State: "open", HeadBranch: branch, BaseBranch: "main",
		HeadRepoCloneURL: "https://github.com/acme/widget.git", PlatformHeadSHA: head,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(t, err)
}

// pass runs one adoption pass, as though the session's recheck were due.
func (f *externalSessionFixture) pass(t *testing.T) {
	t.Helper()
	f.handler.runExternalAgentSessionPass(t.Context())
}

func (f *externalSessionFixture) adopted(t *testing.T) (*db.Workspace, []db.WorkspaceRuntimeSession) {
	t.Helper()
	workspaces, err := f.database.ListWorkspaces(t.Context())
	require.NoError(t, err)
	require.Len(t, workspaces, 1)
	// Setup writes the ready status before it lets a chat start, so wait for both.
	require.Eventually(t, func() bool {
		ws, err := f.database.GetWorkspace(t.Context(), workspaces[0].ID)
		if err != nil || ws == nil || ws.Status != "ready" {
			return false
		}
		f.handler.workspaceSetupMu.Lock()
		defer f.handler.workspaceSetupMu.Unlock()
		_, settingUp := f.handler.workspaceSetupDone[ws.ID]
		return !settingUp
	}, 5*time.Second, 10*time.Millisecond, "setup never finished")
	rows, err := f.database.ListWorkspaceRuntimeSessions(t.Context(), workspaces[0].ID)
	require.NoError(t, err)
	return &workspaces[0], rows
}

func runningAgentHeaders() map[string]string {
	agent := agentprocess.Current()
	return map[string]string{
		"X-Kenn-Forge-Agent-Pid":   strconv.Itoa(agent.PID),
		"X-Kenn-Forge-Agent-Start": agent.Start,
	}
}

func herdrHeaders(socket string) map[string]string {
	headers := runningAgentHeaders()
	headers["X-Kenn-Forge-Herdr-Pane"] = "w1:p1"
	headers["X-Kenn-Forge-Herdr-Socket"] = socket
	return headers
}

func chatState(t *testing.T, chat workspaceChat) localruntime.ACPState {
	t.Helper()
	data, err := chat.Snapshot()
	require.NoError(t, err)
	var state localruntime.ACPState
	require.NoError(t, json.Unmarshal(data, &state))
	return state
}

func TestExternalAgentGetsAWorkspaceOnceItsWorktreeHoldsAPullRequest(t *testing.T) {
	assert := assert.New(t)
	f := newExternalSessionFixture(t)

	f.hook(t, "claude", "session-1", "UserPromptSubmit", runningAgentHeaders())
	f.pass(t)
	workspaces, err := f.database.ListWorkspaces(t.Context())
	require.NoError(t, err)
	assert.Empty(workspaces, "no pull request, so no workspace yet")

	f.openPull(t, "feature/login", f.head)
	f.hook(t, "claude", "session-1", "PostToolUse", runningAgentHeaders())
	f.pass(t)

	ws, rows := f.adopted(t)
	assert.True(ws.ExternalWorktree)
	assert.Equal(f.worktree, ws.WorktreePath)
	assert.Equal(42, ws.ItemNumber)
	assert.Equal("feature/login", ws.WorkspaceBranch)
	require.Len(t, rows, 1)
	assert.Equal(string(localruntime.LaunchTargetACP), rows[0].Kind)

	chat, err := f.handler.openWorkspaceChat(t.Context(), ws.ID, rows[0].SessionKey)
	require.NoError(t, err)
	state := chatState(t, chat)
	require.Len(t, state.Messages, 2)
	assert.Equal("Open a pull request", state.Messages[0].Text)
	assert.Equal("Opened it.", state.Messages[1].Text)
}

// An agent can open its pull request and exit before Forge syncs it. The session
// is still adopted when the pull request arrives within an hour, and forgotten
// after that.
func TestAgentThatExitsBeforeItsPullRequestSyncsStillGetsAWorkspace(t *testing.T) {
	assert := assert.New(t)
	f := newExternalSessionFixture(t)
	f.hook(t, "claude", "session-1", "UserPromptSubmit", runningAgentHeaders())
	f.hook(t, "claude", "session-1", "SessionEnd", nil)
	f.hook(t, "claude", "session-2", "UserPromptSubmit", runningAgentHeaders())
	require.NoError(t, f.database.MarkExternalAgentSessionEnded(t.Context(), "claude", "session-2", time.Now().Add(-2*time.Hour)))

	f.openPull(t, "feature/login", f.head)
	f.pass(t)

	ws, rows := f.adopted(t)
	require.Len(t, rows, 1)
	adopted, err := f.database.GetExternalAgentSession(t.Context(), "claude", "session-1")
	require.NoError(t, err)
	require.NotNil(t, adopted)
	assert.Equal(ws.ID, adopted.WorkspaceID)
	stale, err := f.database.GetExternalAgentSession(t.Context(), "claude", "session-2")
	require.NoError(t, err)
	assert.Nil(stale, "a session that ended over an hour ago is forgotten")
}

// Forge can miss hooks, as while its daemon restarts, so an agent whose first
// hook Forge sees is its exit still gets a workspace.
func TestAgentFirstSeenAtItsExitStillGetsAWorkspace(t *testing.T) {
	f := newExternalSessionFixture(t)
	f.hook(t, "claude", "session-1", "SessionEnd", nil)
	f.openPull(t, "feature/login", f.head)
	f.pass(t)

	_, rows := f.adopted(t)
	assert.Len(t, rows, 1)
}

// The hook that made the session due for adoption may be the last one for a
// while, so the new chat starts from it. An idle notice that followed an
// approval request does not hide the request.
func TestAdoptedChatShowsTheAgentWorkingBeforeItsNextHook(t *testing.T) {
	for name, events := range map[string][]string{
		"working":                          {"PostToolUse"},
		"approval followed by idle notice": {"PermissionRequest", "Notification"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newExternalSessionFixture(t)
			f.openPull(t, "feature/login", f.head)
			for _, event := range events {
				f.hookWith(t, "claude", "session-1", event, map[string]any{"notification_type": "idle_prompt"}, runningAgentHeaders())
			}
			f.pass(t)
			ws, rows := f.adopted(t)

			chat, err := f.handler.openWorkspaceChat(t.Context(), ws.ID, rows[0].SessionKey)
			require.NoError(t, err)

			assert.True(t, chatState(t, chat).Busy)
		})
	}
}

// An idle notice after a turn whose Stop Forge missed means the turn is over.
func TestAdoptedChatShowsAnAgentIdleAfterAnIdleNotice(t *testing.T) {
	f := newExternalSessionFixture(t)
	f.openPull(t, "feature/login", f.head)
	f.hook(t, "claude", "session-1", "PostToolUse", runningAgentHeaders())
	f.hookWith(t, "claude", "session-1", "Notification", map[string]any{"notification_type": "idle_prompt"}, runningAgentHeaders())
	f.pass(t)
	ws, rows := f.adopted(t)

	chat, err := f.handler.openWorkspaceChat(t.Context(), ws.ID, rows[0].SessionKey)
	require.NoError(t, err)

	assert.False(t, chatState(t, chat).Busy)
}

// A hook that shows a turn happened marks the agent prompted even when Forge
// missed its prompt hook; startup and idle notices can come from an agent never
// prompted.
func TestHooksThatShowTheAgentWasPrompted(t *testing.T) {
	for event, want := range map[string]bool{
		"UserPromptSubmit": true, "PostToolUse": true, "Stop": true, "PermissionRequest": true,
		"SessionStart": false, "Notification": false,
	} {
		t.Run(event, func(t *testing.T) {
			f := newExternalSessionFixture(t)
			f.hookWith(t, "claude", "session-1", event, map[string]any{"notification_type": "idle_prompt"}, runningAgentHeaders())
			session, err := f.database.GetExternalAgentSession(t.Context(), "claude", "session-1")
			require.NoError(t, err)
			require.NotNil(t, session)
			assert.Equal(t, want, session.Prompted)
		})
	}
}

func TestAWorktreeChatFollowsOneRunningAgentAtATime(t *testing.T) {
	f := newExternalSessionFixture(t)
	f.openPull(t, "feature/login", f.head)
	f.hook(t, "claude", "first", "UserPromptSubmit", runningAgentHeaders())
	f.pass(t)
	f.adopted(t)

	f.hook(t, "claude", "nested", "UserPromptSubmit", runningAgentHeaders())
	f.pass(t)
	_, rows := f.adopted(t)
	assert.Len(t, rows, 1, "a second agent in the worktree got its own chat")

	f.hook(t, "claude", "first", "SessionEnd", nil)
	f.hook(t, "claude", "nested", "PostToolUse", runningAgentHeaders())
	f.pass(t)
	_, rows = f.adopted(t)
	assert.Len(t, rows, 2, "the next agent's chat appears once the first ends")
}

func TestTwoAgentsReportedBeforeOnePassShareOneChat(t *testing.T) {
	f := newExternalSessionFixture(t)
	f.openPull(t, "feature/login", f.head)
	f.hook(t, "claude", "session-1", "UserPromptSubmit", runningAgentHeaders())
	f.hook(t, "claude", "session-2", "UserPromptSubmit", runningAgentHeaders())

	f.pass(t)

	_, rows := f.adopted(t)
	assert.Len(t, rows, 1, "a worktree's chat follows one running agent")
}

// A worktree can move to another pull request's branch after its first agent
// exits. The next agent's conversation never appears under the first pull request.
func TestAgentOnAnotherPullRequestIsNotAddedToTheOldWorkspace(t *testing.T) {
	f := newExternalSessionFixture(t)
	f.openPull(t, "feature/login", f.head)
	f.hook(t, "claude", "session-1", "UserPromptSubmit", runningAgentHeaders())
	f.pass(t)
	f.adopted(t)
	f.hook(t, "claude", "session-1", "SessionEnd", nil)

	gitfixture.Run(t, f.worktree, "checkout", "-b", "feature/logout")
	require.NoError(t, os.WriteFile(filepath.Join(f.worktree, "logout.txt"), []byte("logout\n"), 0o600))
	gitfixture.Run(t, f.worktree, "add", "logout.txt")
	gitfixture.Run(t, f.worktree, "commit", "-m", "logout")
	head := strings.TrimSpace(string(gitfixture.Run(t, f.worktree, "rev-parse", "HEAD")))
	now := time.Now().UTC()
	_, err := f.database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: f.repoID, PlatformID: 4300, Number: 43, URL: "https://github.com/acme/widget/pull/43",
		Title: "Fix logout", Author: "alice", State: "open", HeadBranch: "feature/logout", BaseBranch: "main",
		HeadRepoCloneURL: "https://github.com/acme/widget.git", PlatformHeadSHA: head,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(t, err)
	f.hook(t, "claude", "session-2", "UserPromptSubmit", runningAgentHeaders())
	f.pass(t)

	_, rows := f.adopted(t)
	assert.Len(t, rows, 1, "the pull request 43 session joined pull request 42's workspace")
}

func TestExternalSessionsThatAreNotAdopted(t *testing.T) {
	t.Run("an agent Forge launched", func(t *testing.T) {
		f := newExternalSessionFixture(t)
		f.openPull(t, "feature/login", f.head)
		f.hook(t, "claude", "launched", "UserPromptSubmit", map[string]string{"X-Kenn-Forge-Runtime-Session-Key": "runtime-1"})
		f.pass(t)
		saved, err := f.database.GetExternalAgentSession(t.Context(), "claude", "launched")
		require.NoError(t, err)
		assert.Nil(t, saved)
	})
	t.Run("a worktree that already belongs to a Forge workspace", func(t *testing.T) {
		f := newExternalSessionFixture(t)
		f.openPull(t, "feature/login", f.head)
		require.NoError(t, f.database.InsertWorkspace(t.Context(), &db.Workspace{
			ID: "ws-forge", Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget",
			RepoID: f.repoID, ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 7, GitHeadRef: "feature/login",
			WorkspaceBranch: "feature/login", WorktreePath: f.worktree, TmuxSession: "forge-ws-forge", Status: "ready",
		}))
		f.hook(t, "claude", "session-1", "UserPromptSubmit", runningAgentHeaders())
		f.pass(t)
		rows, err := f.database.ListWorkspaceRuntimeSessions(t.Context(), "ws-forge")
		require.NoError(t, err)
		assert.Empty(t, rows)
		workspaces, err := f.database.ListWorkspaces(t.Context())
		require.NoError(t, err)
		assert.Len(t, workspaces, 1)
	})
}

func TestAgentThatDiedWithoutSessionEndIsSeenAsGone(t *testing.T) {
	assert := assert.New(t)
	f := newExternalSessionFixture(t)
	f.hook(t, "claude", "killed", "UserPromptSubmit", map[string]string{
		"X-Kenn-Forge-Agent-Pid": strconv.Itoa(1 << 30), "X-Kenn-Forge-Agent-Start": "1",
	})
	f.hook(t, "claude", "unknown-process", "UserPromptSubmit", nil)
	f.pass(t)

	killed, err := f.database.GetExternalAgentSession(t.Context(), "claude", "killed")
	require.NoError(t, err)
	require.NotNil(t, killed)
	assert.NotNil(killed.EndedAt)
	running, err := f.database.GetExternalAgentSession(t.Context(), "claude", "unknown-process")
	require.NoError(t, err)
	require.NotNil(t, running)
	assert.Nil(running.EndedAt, "without proof of exit the session may still run")
}

// A prompt sent from the chat reaches the agent's Herdr pane, addressed through
// the socket of the Herdr server that pane belongs to.
func TestChatPromptGoesToTheAgentsHerdrPane(t *testing.T) {
	assert := assert.New(t)
	f := newExternalSessionFixture(t)
	bin := t.TempDir()
	record := filepath.Join(bin, "record")
	script := "#!/bin/sh\n{ printf 'socket=%s\\n' \"$HERDR_SOCKET_PATH\"; for arg; do printf '%s\\n' \"$arg\"; done; } >> " + record + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	f.openPull(t, "feature/login", f.head)
	f.hook(t, "claude", "session-1", "UserPromptSubmit", herdrHeaders("/run/test/herdr.sock"))
	f.pass(t)
	ws, rows := f.adopted(t)
	f.hook(t, "claude", "session-1", "PostToolUse", herdrHeaders("/run/test/herdr.sock"))
	chat, err := f.handler.openWorkspaceChat(t.Context(), ws.ID, rows[0].SessionKey)
	require.NoError(t, err)

	state := chatState(t, chat)
	assert.True(state.Dormant)
	assert.False(state.ExternalReadOnly)
	assert.True(state.Busy, "the hook said the agent is working")

	require.NoError(t, chat.Command(localruntime.ACPCommand{Type: "prompt", ID: "p1", Text: "--also check logout"}))
	require.NoError(t, chat.Command(localruntime.ACPCommand{Type: "cancel"}))

	recorded, err := os.ReadFile(record)
	require.NoError(t, err)
	assert.Equal(strings.Join([]string{
		"socket=/run/test/herdr.sock", "agent", "prompt", "w1:p1", "--also check logout",
		"socket=/run/test/herdr.sock", "agent", "send-keys", "w1:p1", "esc", "",
	}, "\n"), string(recorded))
}

// A browser that reconnects resends the prompts it has no acknowledgment for,
// under their submission IDs, through a new chat. Each reaches the pane once.
func TestAResentPromptReachesTheAgentOnce(t *testing.T) {
	f := newExternalSessionFixture(t)
	bin := t.TempDir()
	record := filepath.Join(bin, "record")
	script := "#!/bin/sh\nfor arg; do printf '%s\\n' \"$arg\"; done >> " + record + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f.openPull(t, "feature/login", f.head)
	f.hook(t, "claude", "session-1", "UserPromptSubmit", herdrHeaders("/run/test/herdr.sock"))
	f.pass(t)
	ws, rows := f.adopted(t)

	for _, id := range []string{"p1", "p1", "p2"} {
		chat, err := f.handler.openWorkspaceChat(t.Context(), ws.ID, rows[0].SessionKey)
		require.NoError(t, err)
		require.NoError(t, chat.Command(localruntime.ACPCommand{Type: "prompt", ID: id, Text: "prompt " + id}))
	}

	recorded, err := os.ReadFile(record)
	require.NoError(t, err)
	assert.Equal(t, strings.Join([]string{
		"agent", "prompt", "w1:p1", "prompt p1",
		"agent", "prompt", "w1:p1", "prompt p2", "",
	}, "\n"), string(recorded))

	// After the agent exits, a resend reaches no agent that continues the session.
	f.hook(t, "claude", "session-1", "SessionEnd", nil)
	chat, err := f.handler.openWorkspaceChat(t.Context(), ws.ID, rows[0].SessionKey)
	require.NoError(t, err)
	require.NoError(t, chat.Command(localruntime.ACPCommand{Type: "prompt", ID: "p1", Text: "prompt p1"}))
	_, err = f.handler.runtime.ACP(ws.ID, rows[0].SessionKey)
	require.Error(t, err, "the resend started an agent to run the prompt again")
	session, err := f.database.GetExternalAgentSession(t.Context(), "claude", "session-1")
	require.NoError(t, err)
	assert.True(t, session.Prompted, "UserPromptSubmit records that the agent was prompted")
}

func TestChatStaysReadOnlyWithoutAHerdrPaneAndStartsNothingWhileAgentRuns(t *testing.T) {
	assert := assert.New(t)
	f := newExternalSessionFixture(t)
	f.openPull(t, "feature/login", f.head)
	f.hook(t, "claude", "session-1", "UserPromptSubmit", runningAgentHeaders())
	f.pass(t)
	ws, rows := f.adopted(t)
	key := rows[0].SessionKey

	chat, err := f.handler.openWorkspaceChat(t.Context(), ws.ID, key)
	require.NoError(t, err)
	assert.True(chatState(t, chat).ExternalReadOnly)
	require.ErrorIs(t, chat.Command(localruntime.ACPCommand{Type: "prompt", Text: "hello"}), localruntime.ErrACPMirrorReadOnly)
	_, err = f.handler.runtime.ACP(ws.ID, key)
	assert.Error(err, "no Forge-run agent started while the external agent runs")
}

func TestExitedExternalAgentNeedsAChatAgentToContinue(t *testing.T) {
	assert := assert.New(t)
	f := newExternalSessionFixture(t)
	f.openPull(t, "feature/login", f.head)
	f.hook(t, "claude", "session-1", "UserPromptSubmit", runningAgentHeaders())
	f.pass(t)
	ws, rows := f.adopted(t)
	f.hook(t, "claude", "session-1", "SessionEnd", nil)

	chat, err := f.handler.openWorkspaceChat(t.Context(), ws.ID, rows[0].SessionKey)
	require.NoError(t, err)
	state := chatState(t, chat)
	assert.False(state.ExternalReadOnly)
	assert.NotEmpty(state.Messages)
	assert.ErrorContains(chat.Command(localruntime.ACPCommand{Type: "prompt", Text: "continue"}), `add a chat agent in Settings whose key starts with "claude"`)
}

// This runtime has no way to run an owner, so the start stops there: past the
// point where it needs the chat agent's command, with the session still unclaimed.
func TestExitedExternalAgentContinuesInTheChatAgent(t *testing.T) {
	assert := assert.New(t)
	f := newExternalSessionFixture(t)
	f.handler.runtime.UpdateTargets([]localruntime.LaunchTarget{{
		Key: "claude-chat", Label: "Claude chat", Kind: localruntime.LaunchTargetACP, Command: []string{"claude-agent-acp"}, Available: true,
	}})
	f.openPull(t, "feature/login", f.head)
	f.hook(t, "claude", "session-1", "UserPromptSubmit", runningAgentHeaders())
	f.pass(t)
	ws, rows := f.adopted(t)
	f.hook(t, "claude", "session-1", "SessionEnd", nil)

	chat, err := f.handler.openWorkspaceChat(t.Context(), ws.ID, rows[0].SessionKey)
	require.NoError(t, err)
	err = chat.Command(localruntime.ACPCommand{Type: "prompt", Text: "continue"})

	require.ErrorContains(t, err, "ACP requires tmux or ptyowner")
	session, err := f.database.GetExternalAgentSession(t.Context(), "claude", "session-1")
	require.NoError(t, err)
	assert.Nil(session.HandedOffAt, "a failed start leaves the session to the next prompt")
}

func TestRefusedCommandToAnExternalAgentIsAConflict(t *testing.T) {
	assert := assert.New(t)
	f := newExternalSessionFixture(t)
	f.openPull(t, "feature/login", f.head)
	f.hook(t, "claude", "session-1", "UserPromptSubmit", runningAgentHeaders())
	f.pass(t)
	ws, rows := f.adopted(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/api/v1/workspaces/"+ws.ID+"/runtime/sessions/"+rows[0].SessionKey+"/chat/commands",
		strings.NewReader(`{"type":"prompt","text":"hello","id":"p1"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.mux.ServeHTTP(recorder, req)

	assert.Equal(http.StatusConflict, recorder.Code, recorder.Body.String())
	assert.Contains(recorder.Body.String(), `"reason":"external_agent"`)
}

func TestDeletingAnAdoptedWorkspaceKeepsTheWorktreeAndStaysDeleted(t *testing.T) {
	assert := assert.New(t)
	f := newExternalSessionFixture(t)
	f.openPull(t, "feature/login", f.head)
	f.hook(t, "claude", "session-1", "UserPromptSubmit", runningAgentHeaders())
	f.pass(t)
	ws, _ := f.adopted(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.worktree, "unsaved.txt"), []byte("wip\n"), 0o644))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/workspaces/"+ws.ID, nil)
	recorder := httptest.NewRecorder()
	f.mux.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())

	assert.FileExists(filepath.Join(f.worktree, "unsaved.txt"))
	assert.Equal("feature/login\n", string(gitfixture.Run(t, f.worktree, "branch", "--show-current")))

	f.hook(t, "claude", "session-1", "PostToolUse", runningAgentHeaders())
	f.pass(t)
	workspaces, err := f.database.ListWorkspaces(t.Context())
	require.NoError(t, err)
	assert.Empty(workspaces, "the session came back after its workspace was deleted")
}

// Herdr's argument handling and pane targeting are its own, so delivery runs
// against the real CLI on a private server.
func TestDeliveryReachesARealHerdrPane(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a Herdr server")
	}
	herdr, err := exec.LookPath("herdr")
	if err != nil {
		t.Skip("herdr is not installed")
	}
	// Herdr's socket path must fit a Unix socket address, which t.TempDir can exceed.
	root, err := os.MkdirTemp("/tmp", "herdr-test")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	agent := "#!/bin/sh\nwhile IFS= read -r line; do printf 'received:%s\\n' \"$line\"; done\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte(agent), 0o755))
	for _, name := range []string{"HERDR_PANE_ID", "HERDR_SESSION", "HERDR_ENV", "HERDR_TAB_ID", "HERDR_WORKSPACE_ID", "HERDR_BIN_PATH", "HERDR_SERVER_LAUNCHER"} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_RUNTIME_DIR", root)
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(root, "herdr.sock"))
	socket := os.Getenv("HERDR_SOCKET_PATH")

	server := procutil.CommandContext(context.WithoutCancel(t.Context()), herdr, "server")
	require.NoError(t, server.Start())
	t.Cleanup(func() {
		_ = exec.CommandContext(context.WithoutCancel(t.Context()), herdr, "server", "stop").Run()
		_ = server.Process.Kill()
		_ = server.Wait()
	})
	run := func(args ...string) string {
		output, err := exec.CommandContext(t.Context(), herdr, args...).CombinedOutput()
		require.NoError(t, err, string(output))
		return string(output)
	}
	require.Eventually(t, func() bool {
		return exec.CommandContext(t.Context(), herdr, "pane", "list").Run() == nil
	}, 10*time.Second, 50*time.Millisecond)
	run("workspace", "create", "--cwd", root)
	run("agent", "start", "demo", "--kind", "claude", "--pane", "w1:p1")

	f := &Handler{db: dbtest.Open(t)}
	session := db.ExternalAgentSession{Agent: "claude", SessionID: "s1", CWD: root, HerdrPaneID: "w1:p1", HerdrSocketPath: socket}
	require.NoError(t, f.db.RecordExternalAgentSession(t.Context(), session))

	for _, text := range []string{"plain words", "--wait", "-single dash"} {
		require.NoError(t, f.deliverToExternalAgent(t.Context(), session, localruntime.ACPCommand{Type: "prompt", Text: text}))
		require.Eventually(t, func() bool {
			return strings.Contains(run("pane", "read", "w1:p1"), "received:"+text)
		}, 10*time.Second, 50*time.Millisecond, text)
	}
	require.NoError(t, f.deliverToExternalAgent(t.Context(), session, localruntime.ACPCommand{Type: "cancel"}))

	missing := session
	missing.SessionID, missing.HerdrPaneID = "s2", "w9:p9"
	require.NoError(t, f.db.RecordExternalAgentSession(t.Context(), missing))
	assert.ErrorContains(t, f.deliverToExternalAgent(t.Context(), missing, localruntime.ACPCommand{Type: "prompt", Text: "hello"}), "herdr")
}

package localruntime

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/ptyowner"
	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
)

// newACPSessionsTestManager runs the ACP fixture as the "chat" target, logging
// the sessions it creates or loads under the returned directory.
func newACPSessionsTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	dir := t.TempDir()
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
	executable, err := os.Executable()
	require.NoError(t, err)
	owner := &ptyowner.Client{Root: filepath.Join(t.TempDir(), "pty-owner"), InProcess: true}
	options := Options{
		ACPSessionsDir:  filepath.Join(dir, "acp"),
		ParkedDir:       filepath.Join(dir, "parked"),
		PtyOwnerRuntime: ptyownerruntime.New(owner, nil),
		Targets:         ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil),
	}
	return newACPTestManager(t, options), dir
}

func listedSessionIDs(sessions []ACPAgentSession) []string {
	ids := make([]string, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, session.SessionID)
	}
	return ids
}

// A resumed session opens as a chat whose transcript is the agent's history,
// and it leaves the list while a chat holds it.
func TestACPResumesAListedSessionWithItsHistory(t *testing.T) {
	t.Setenv("KENN_FORGE_ACP_LIST", "1")
	manager, dir := newACPSessionsTestManager(t)
	assert := assert.New(t)

	sessions, err := manager.ListACPSessions(t.Context(), "workspace", dir, "chat")
	require.NoError(t, err)
	// Pages are followed past an empty first page; newest first, undated last.
	assert.Equal([]string{"newer-session", "fixture-session", "undated-session"}, listedSessionIDs(sessions))
	assert.Equal("Newer work", sessions[0].Title)
	assert.Equal(time.Date(2026, 11, 21, 9, 30, 0, 250_000_000, time.UTC), sessions[0].UpdatedAt)
	assert.True(sessions[2].UpdatedAt.IsZero())

	info, err := manager.ResumeACPSession(t.Context(), "workspace", dir, "chat", "fixture-session", nil)
	require.NoError(t, err)
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	state := publishedACPState(t, agent)
	require.Len(t, state.Messages, 2)
	assert.Equal("user", state.Messages[0].Role)
	assert.Equal("remember", state.Messages[0].Text)
	assert.Equal("assistant", state.Messages[1].Role)
	assert.Equal("restored answer", state.Messages[1].Text)
	// Loaded, never replaced by a new session.
	assert.Equal("session/load\n", acpSessionLog(t, dir))
	// The chat's own record now holds the transcript, so a later reload
	// restores it like any saved chat.
	saved := savedACPSession(t, manager, info.Key)
	assert.Equal("fixture-session", saved.SessionID)
	assert.False(saved.Import)
	assert.Len(saved.State.Messages, 2)

	sessions, err = manager.ListACPSessions(t.Context(), "workspace", dir, "chat")
	require.NoError(t, err)
	assert.Equal([]string{"newer-session", "undated-session"}, listedSessionIDs(sessions))
	_, err = manager.ResumeACPSession(t.Context(), "workspace", dir, "chat", "fixture-session", nil)
	require.ErrorIs(t, err, ErrACPSessionInUse)
}

// A session the agent no longer has fails the resume instead of opening a new
// session, and stays offered for another try.
func TestACPResumeOfAMissingSessionFails(t *testing.T) {
	t.Setenv("KENN_FORGE_ACP_LIST", "1")
	t.Setenv("KENN_FORGE_ACP_LOAD_NOT_FOUND", "1")
	manager, dir := newACPSessionsTestManager(t)

	_, err := manager.ResumeACPSession(t.Context(), "workspace", dir, "chat", "fixture-session", nil)
	require.Error(t, err)
	assert.Equal(t, "session/load\n", acpSessionLog(t, dir))
	assert.Empty(t, manager.ListSessions("workspace"))

	sessions, err := manager.ListACPSessions(t.Context(), "workspace", dir, "chat")
	require.NoError(t, err)
	assert.Contains(t, listedSessionIDs(sessions), "fixture-session")
}

func TestACPListSessionsNeedsAnAgentThatListsAndLoads(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{name: "no list", env: map[string]string{}},
		{name: "no load", env: map[string]string{"KENN_FORGE_ACP_LIST": "1", "KENN_FORGE_ACP_NO_LOAD": "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			manager, dir := newACPSessionsTestManager(t)
			_, err := manager.ListACPSessions(t.Context(), "workspace", dir, "chat")
			require.ErrorIs(t, err, ErrACPCannotListSessions)
		})
	}
}

func TestACPResumeNeedsAnACPTarget(t *testing.T) {
	manager := NewManager(Options{Targets: []LaunchTarget{{Key: "codex", Label: "Codex", Kind: LaunchTargetAgent, Command: []string{"codex"}, Available: true}}})
	_, err := manager.ResumeACPSession(t.Context(), "workspace", t.TempDir(), "codex", "fixture-session", nil)
	require.ErrorContains(t, err, "is not an ACP agent")
}

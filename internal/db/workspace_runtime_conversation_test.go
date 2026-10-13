package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Recovery resumes the conversation an agent last reported, so it must
// survive the runtime row being recorded again when the agent resumes.
func TestRecordWorkspaceRuntimeConversation(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID: "ws-conversation", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: WorkspaceItemTypeAdHoc,
		ItemKey: AdHocWorkspaceItemKey("work/conversation"), GitHeadRef: "work/conversation",
		WorkspaceBranch: "work/conversation", WorktreePath: t.TempDir(), Status: "ready",
	}))
	session := WorkspaceRuntimeSession{
		WorkspaceID: "ws-conversation", SessionKey: "runtime-a", TargetKey: "claude",
		Label: "Claude", Kind: "agent", Scope: "session", TmuxSession: "forge-a",
	}
	require.NoError(d.RecordWorkspaceRuntimeSession(ctx, &session))
	stored := func() WorkspaceRuntimeSession {
		rows, err := d.ListAllWorkspaceRuntimeSessions(ctx)
		require.NoError(err)
		require.Len(rows, 1)
		return rows[0]
	}
	assert.Empty(stored().ConversationID, "a new runtime has no conversation yet")

	updated, err := d.RecordWorkspaceRuntimeConversation(ctx, "runtime-a", "claude", "first")
	require.NoError(err)
	assert.True(updated)
	updated, err = d.RecordWorkspaceRuntimeConversation(ctx, "runtime-a", "claude", "first")
	require.NoError(err)
	assert.False(updated, "an unchanged conversation is not rewritten")
	assert.Equal("claude", stored().ConversationAgent)
	assert.Equal("first", stored().ConversationID)

	// Resuming records the runtime again under a new tmux session.
	session.TmuxSession = "forge-b"
	require.NoError(d.RecordWorkspaceRuntimeSession(ctx, &session))
	assert.Equal("forge-b", stored().TmuxSession)
	assert.Equal("first", stored().ConversationID, "recording the runtime keeps its conversation")

	// /clear starts a new conversation in the same terminal.
	updated, err = d.RecordWorkspaceRuntimeConversation(ctx, "runtime-a", "claude", "second")
	require.NoError(err)
	assert.True(updated)
	assert.Equal("second", stored().ConversationID)

	// SessionEnd saves why the saved conversation ended; another
	// conversation's end changes nothing.
	updated, err = d.RecordWorkspaceRuntimeConversationEnd(ctx, "runtime-a", "claude", "nested", "other")
	require.NoError(err)
	assert.False(updated)
	updated, err = d.RecordWorkspaceRuntimeConversationEnd(ctx, "runtime-a", "claude", "second", "other")
	require.NoError(err)
	assert.True(updated)
	assert.Equal("other", stored().ConversationEndReason)
	// A resumed conversation reports again, so it no longer has ended.
	updated, err = d.RecordWorkspaceRuntimeConversation(ctx, "runtime-a", "claude", "second")
	require.NoError(err)
	assert.True(updated)
	assert.Empty(stored().ConversationEndReason)

	updated, err = d.RecordWorkspaceRuntimeConversation(ctx, "runtime-missing", "claude", "first")
	require.NoError(err)
	assert.False(updated, "a hook from an unrecorded runtime changes nothing")
}

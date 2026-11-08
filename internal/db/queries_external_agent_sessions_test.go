package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExternalAgentSessionLifecycle(t *testing.T) {
	ctx := t.Context()
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)

	require.NoError(database.RecordExternalAgentSession(ctx, ExternalAgentSession{
		Agent: "claude", SessionID: "s1", CWD: "/work/one", TranscriptPath: "/t/s1.jsonl",
		ProcessID: 7, ProcessStart: "100", HerdrPaneID: "w1:p1", HerdrSocketPath: "/run/herdr.sock",
	}))
	saved, err := database.GetExternalAgentSession(ctx, "claude", "s1")
	require.NoError(err)
	require.NotNil(saved)
	assert.Equal("/t/s1.jsonl", saved.TranscriptPath)
	assert.False(saved.FirstSeenAt.IsZero())

	// A later event that omits fields keeps them, and moving panes follows the agent.
	require.NoError(database.RecordExternalAgentSession(ctx, ExternalAgentSession{
		Agent: "claude", SessionID: "s1", CWD: "/work/one/sub", HerdrPaneID: "w2:p3", HerdrSocketPath: "/run/herdr.sock",
	}))
	saved, err = database.GetExternalAgentSession(ctx, "claude", "s1")
	require.NoError(err)
	assert.Equal("/work/one/sub", saved.CWD)
	assert.Equal("/t/s1.jsonl", saved.TranscriptPath)
	assert.Equal(7, saved.ProcessID)
	assert.Equal("100", saved.ProcessStart)
	assert.Equal("w2:p3", saved.HerdrPaneID)

	// An ended session stops being listed; a report means it runs again.
	require.NoError(database.MarkExternalAgentSessionEnded(ctx, "claude", "s1", time.Now()))
	running, err := database.ListRunningExternalAgentSessions(ctx)
	require.NoError(err)
	assert.Empty(running)
	require.NoError(database.RecordExternalAgentSession(ctx, ExternalAgentSession{Agent: "claude", SessionID: "s1", CWD: "/work/one"}))
	running, err = database.ListRunningExternalAgentSessions(ctx)
	require.NoError(err)
	assert.Len(running, 1)

	// Once Forge holds the session, reports under its ID change nothing.
	require.NoError(database.AdoptExternalAgentSession(ctx, "claude", "s1", "ws-1", "key-1"))
	byKey, err := database.GetExternalAgentSessionByRuntimeKey(ctx, "key-1")
	require.NoError(err)
	require.NotNil(byKey)
	assert.Equal("s1", byKey.SessionID)
	require.NoError(database.MarkExternalAgentSessionHandedOff(ctx, "claude", "s1", time.Now()))
	require.NoError(database.RecordExternalAgentSession(ctx, ExternalAgentSession{Agent: "claude", SessionID: "s1", CWD: "/elsewhere"}))
	saved, err = database.GetExternalAgentSession(ctx, "claude", "s1")
	require.NoError(err)
	assert.Equal("/work/one", saved.CWD)
	assert.NotNil(saved.HandedOffAt)
	running, err = database.ListRunningExternalAgentSessions(ctx)
	require.NoError(err)
	assert.Empty(running)

	missing, err := database.GetExternalAgentSessionByRuntimeKey(ctx, "no-such-key")
	require.NoError(err)
	assert.Nil(missing)
}

func TestDeleteEndedExternalSessionsKeepsAdoptedRunningAndRecentOnes(t *testing.T) {
	ctx := t.Context()
	require := require.New(t)
	database := openTestDB(t)
	for _, id := range []string{"ended", "recent", "running", "adopted"} {
		require.NoError(database.RecordExternalAgentSession(ctx, ExternalAgentSession{Agent: "codex", SessionID: id, CWD: "/work"}))
	}
	cutoff := time.Now()
	require.NoError(database.AdoptExternalAgentSession(ctx, "codex", "adopted", "ws-1", "key-1"))
	require.NoError(database.MarkExternalAgentSessionEnded(ctx, "codex", "adopted", cutoff.Add(-time.Minute)))
	require.NoError(database.MarkExternalAgentSessionEnded(ctx, "codex", "ended", cutoff.Add(-time.Minute)))
	require.NoError(database.MarkExternalAgentSessionEnded(ctx, "codex", "recent", cutoff.Add(time.Minute)))

	recent, err := database.ListEndedUnadoptedExternalAgentSessions(ctx, cutoff)
	require.NoError(err)
	require.Len(recent, 1)
	assert.Equal(t, "recent", recent[0].SessionID)

	require.NoError(database.DeleteEndedUnadoptedExternalAgentSessions(ctx, cutoff))

	for id, kept := range map[string]bool{"ended": false, "recent": true, "running": true, "adopted": true} {
		saved, err := database.GetExternalAgentSession(ctx, "codex", id)
		require.NoError(err)
		assert.Equal(t, kept, saved != nil, id)
	}
}

package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/fleet"
)

func TestReconcileFleetTmuxLiveOwnedAndUnmanaged(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	createdAt := time.Date(2026, 5, 31, 9, 0, 0, 0, time.UTC)
	polledAt := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	owned := []fleetOwnedTmuxSession{
		{
			Name:             "middleman-main",
			WorktreeKey:      "worktree:/repo/main",
			SessionScopedKey: "session:ws-1:main",
			RuntimeKind:      "tmux",
			Status:           "running",
			Label:            "middleman-main",
			CreatedAt:        createdAt,
		},
		{
			Name:             "middleman-agent",
			WorktreeKey:      "worktree:/repo/main",
			SessionScopedKey: "session:session-agent",
			RuntimeKind:      "agent",
			Status:           "starting",
			Label:            "Codex",
			CreatedAt:        createdAt,
		},
	}
	raw := fleet.RawSnapshot{
		Sessions: rawSessionsForOwnedTmux(owned),
	}
	snap := fleetTmuxMonitorSnapshot{
		CurrentInventory: &fleetTmuxInventorySample{
			PolledAt:  polledAt,
			Succeeded: true,
			Sessions: map[string]fleetTmuxLiveSession{
				"middleman-main": {
					Name:        "middleman-main",
					WindowCount: 1,
					Windows: []fleet.TmuxWindowInfo{{
						ID: "@1", Index: 0, Name: "main",
					}},
				},
				"middleman-agent": {
					Name:        "middleman-agent",
					WindowCount: 1,
					Windows: []fleet.TmuxWindowInfo{{
						ID: "@2", Index: 0, Name: "agent",
					}},
				},
				"personal": {
					Name:        "personal",
					WindowCount: 2,
					Windows: []fleet.TmuxWindowInfo{{
						ID: "@3", Index: 0, Name: "private",
					}},
				},
			},
		},
		IncludeUnmanagedDetail: false,
	}

	reconcileFleetTmuxSnapshot(&raw, owned, snap)

	require.Len(raw.Host.TmuxSessions, 3)
	main := requireTmuxInfo(t, raw.Host.TmuxSessions, "middleman-main")
	assert.True(main.Managed)
	assert.Equal("worktree:/repo/main", main.WorktreeKey)
	assert.Equal("session:ws-1:main", main.SessionScopedKey)
	assert.Equal(1, main.WindowCount)
	require.Len(main.Windows, 1)
	assert.Equal("main", main.Windows[0].Name)

	personal := requireTmuxInfo(t, raw.Host.TmuxSessions, "personal")
	assert.False(personal.Managed)
	assert.Equal(2, personal.WindowCount)
	assert.Empty(personal.Windows, "unmanaged windows are redacted by default")

	for _, session := range raw.Sessions {
		info := requireTmuxInfoByScopedKey(t, raw.Host.TmuxSessions, session.ScopedKey)
		assert.Equal(session.WorktreeKey, info.WorktreeKey)
	}
	for _, info := range raw.Host.TmuxSessions {
		if !info.Managed {
			continue
		}
		requireRawSessionByScopedKey(t, raw.Sessions, info.SessionScopedKey)
	}

	agent := requireRawSessionByScopedKey(t, raw.Sessions, "session:session-agent")
	assert.Equal("starting", agent.Status, "starting runtime overlay is preserved while tmux is live")
}

func TestReconcileFleetTmuxDebouncesAbsentOwnedSession(t *testing.T) {
	createdAt := time.Date(2026, 5, 31, 9, 0, 0, 0, time.UTC)
	firstPoll := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	secondPoll := firstPoll.Add(4 * time.Second)
	owned := []fleetOwnedTmuxSession{{
		Name:             "missing-agent",
		WorktreeKey:      "worktree:/repo/main",
		SessionScopedKey: "session:session-agent",
		RuntimeKind:      "agent",
		Status:           "running",
		Label:            "Codex",
		CreatedAt:        createdAt,
	}}

	firstRaw := fleet.RawSnapshot{Sessions: rawSessionsForOwnedTmux(owned)}
	reconcileFleetTmuxSnapshot(&firstRaw, owned, fleetTmuxMonitorSnapshot{
		PreviousInventory: &fleetTmuxInventorySample{
			PolledAt:  firstPoll.Add(-4 * time.Second),
			Succeeded: true,
			Sessions: map[string]fleetTmuxLiveSession{
				"missing-agent": {Name: "missing-agent", WindowCount: 1},
			},
		},
		CurrentInventory: &fleetTmuxInventorySample{
			PolledAt:  firstPoll,
			Succeeded: true,
			Sessions:  map[string]fleetTmuxLiveSession{},
		},
	})
	assert.Equal(t, "running", firstRaw.Sessions[0].Status)

	secondRaw := fleet.RawSnapshot{Sessions: rawSessionsForOwnedTmux(owned)}
	reconcileFleetTmuxSnapshot(&secondRaw, owned, fleetTmuxMonitorSnapshot{
		PreviousInventory: &fleetTmuxInventorySample{
			PolledAt:  firstPoll,
			Succeeded: true,
			Sessions:  map[string]fleetTmuxLiveSession{},
		},
		CurrentInventory: &fleetTmuxInventorySample{
			PolledAt:  secondPoll,
			Succeeded: true,
			Sessions:  map[string]fleetTmuxLiveSession{},
		},
	})
	assert.Equal(t, "exited", secondRaw.Sessions[0].Status)

	newOwned := owned
	newOwned[0].CreatedAt = secondPoll.Add(time.Second)
	newRaw := fleet.RawSnapshot{Sessions: rawSessionsForOwnedTmux(newOwned)}
	reconcileFleetTmuxSnapshot(&newRaw, newOwned, fleetTmuxMonitorSnapshot{
		PreviousInventory: &fleetTmuxInventorySample{
			PolledAt:  firstPoll,
			Succeeded: true,
			Sessions:  map[string]fleetTmuxLiveSession{},
		},
		CurrentInventory: &fleetTmuxInventorySample{
			PolledAt:  secondPoll,
			Succeeded: true,
			Sessions:  map[string]fleetTmuxLiveSession{},
		},
	})
	assert.Equal(t, "running", newRaw.Sessions[0].Status)
}

func TestReconcileFleetTmuxMetricsBestEffort(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	createdAt := time.Date(2026, 5, 31, 9, 0, 0, 0, time.UTC)
	polledAt := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	lastOutput := polledAt.Add(-time.Minute)
	lastActive := polledAt
	owned := []fleetOwnedTmuxSession{{
		Name:             "agent",
		WorktreeKey:      "worktree:/repo/main",
		SessionScopedKey: "session:session-agent",
		RuntimeKind:      "agent",
		Status:           "running",
		Label:            "Codex",
		CreatedAt:        createdAt,
	}}
	raw := fleet.RawSnapshot{Sessions: rawSessionsForOwnedTmux(owned)}
	reconcileFleetTmuxSnapshot(&raw, owned, fleetTmuxMonitorSnapshot{
		CurrentInventory: &fleetTmuxInventorySample{
			PolledAt:  polledAt,
			Succeeded: true,
			Sessions: map[string]fleetTmuxLiveSession{
				"agent": {Name: "agent", WindowCount: 1},
			},
		},
		Metrics: &fleetTmuxMetricsSample{
			SampledAt: polledAt,
			Sessions: map[string]fleetTmuxSessionMetrics{
				"agent": {
					CPUPercent:     14,
					ResidentMB:     68,
					ProcessCount:   3,
					LastOutputAt:   &lastOutput,
					LastActiveAt:   &lastActive,
					ExecutableName: "codex",
				},
			},
		},
	})
	require.Len(raw.Sessions, 1)
	session := raw.Sessions[0]
	require.NotNil(session.CPUPercent)
	assert.InDelta(14.0, *session.CPUPercent, 0.0001)
	require.NotNil(session.ResidentMB)
	assert.Equal(68, *session.ResidentMB)
	require.NotNil(session.ProcessCount)
	assert.Equal(3, *session.ProcessCount)
	assert.Equal("codex", session.ExecutableName)
	require.NotNil(session.LastOutputAt)
	assert.Equal(lastOutput.Format(time.RFC3339), *session.LastOutputAt)
	require.NotNil(session.LastActiveAt)
	assert.Equal(lastActive.Format(time.RFC3339), *session.LastActiveAt)

	metricsFailed := fleet.RawSnapshot{Sessions: rawSessionsForOwnedTmux(owned)}
	reconcileFleetTmuxSnapshot(&metricsFailed, owned, fleetTmuxMonitorSnapshot{
		CurrentInventory: &fleetTmuxInventorySample{
			PolledAt:  polledAt,
			Succeeded: true,
			Sessions: map[string]fleetTmuxLiveSession{
				"agent": {Name: "agent", WindowCount: 1},
			},
		},
		Metrics: &fleetTmuxMetricsSample{SampledAt: polledAt, Error: "ps failed"},
	})
	assert.Len(metricsFailed.Host.TmuxSessions, 1)
	assert.Equal("running", metricsFailed.Sessions[0].Status)
	assert.Nil(metricsFailed.Sessions[0].CPUPercent)
}

func requireTmuxInfo(
	t *testing.T,
	infos []fleet.TmuxSessionInfo,
	name string,
) fleet.TmuxSessionInfo {
	t.Helper()
	for _, info := range infos {
		if info.Name == name {
			return info
		}
	}
	require.Fail(t, "tmux session not found", name)
	return fleet.TmuxSessionInfo{}
}

func requireTmuxInfoByScopedKey(
	t *testing.T,
	infos []fleet.TmuxSessionInfo,
	scopedKey string,
) fleet.TmuxSessionInfo {
	t.Helper()
	for _, info := range infos {
		if info.SessionScopedKey == scopedKey {
			return info
		}
	}
	require.Fail(t, "tmux session scoped key not found", scopedKey)
	return fleet.TmuxSessionInfo{}
}

func requireRawSessionByScopedKey(
	t *testing.T,
	sessions []fleet.RawSession,
	scopedKey string,
) fleet.RawSession {
	t.Helper()
	for _, session := range sessions {
		if session.ScopedKey == scopedKey {
			return session
		}
	}
	require.Fail(t, "raw session scoped key not found", scopedKey)
	return fleet.RawSession{}
}

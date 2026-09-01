package fleet

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNeutralAggregateContainsNoObserverProjection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	local := RawSnapshot{
		ProtocolVersion: 3,
		NodeID:          NodeID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		Host:            RawHost{Hostname: "hub", Platform: "linux"},
		Workspaces:      []RawWorkspace{{ID: "ws-a", Status: "ready"}},
	}
	memberRaw := RawSnapshot{
		ProtocolVersion: 3,
		NodeID:          NodeID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		Host:            RawHost{Hostname: "spoke-b", Platform: "linux"},
		Workspaces:      []RawWorkspace{{ID: "ws-b", Status: "ready"}},
	}
	aggregate := BuildNeutralAggregate(local, []PeerResult{{
		NodeID: memberRaw.NodeID, Name: "spoke-b", Reachable: true, Raw: &memberRaw,
	}})

	require.Len(aggregate.Hosts, 2)
	require.Len(aggregate.Workspaces, 2)
	raw, err := json.Marshal(aggregate)
	require.NoError(err)
	assert.NotContains(string(raw), "operationAvailability")
	assert.NotContains(string(raw), `"kind"`)
	assert.Equal(NodeID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), local.NodeID,
		"aggregate construction must not mutate its local input")
}

func TestBuildNeutralAggregateStampsNodeIDsAndRetainsDegradedMembers(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	localID := NodeID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	memberID := NodeID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	downID := NodeID("cccccccccccccccccccccccccccccccc")
	local := RawSnapshot{
		ProtocolVersion: 3, NodeID: localID,
		Host:     RawHost{Hostname: "studio"},
		Projects: []RawProject{{ScopedKey: "repo:/a", Name: "a"}},
	}
	member := RawSnapshot{
		ProtocolVersion: 3, NodeID: memberID,
		Host:     RawHost{Hostname: "mbp"},
		Projects: []RawProject{{ScopedKey: "repo:/b", Name: "b"}},
	}
	timeout := "timeout"
	aggregate := BuildNeutralAggregate(local, []PeerResult{
		{NodeID: memberID, Name: "mbp", Reachable: true, Raw: &member},
		{NodeID: downID, Name: "epyc", Reachable: false, Err: &timeout},
		{NodeID: memberID, Name: "duplicate", Reachable: true, Raw: &member},
	})

	assert.Empty(local.Projects[0].HostKey)
	assert.Empty(member.Projects[0].HostKey)
	require.Len(aggregate.Projects, 2)
	assert.Equal(string(localID), aggregate.Projects[0].HostKey)
	assert.Equal(string(memberID), aggregate.Projects[1].HostKey)
	require.Len(aggregate.Hosts, 4)
	assert.False(aggregate.Hosts[2].Reachable)
	assert.Equal("timeout", *aggregate.Hosts[2].Error)
	assert.False(aggregate.Hosts[3].Reachable)
	assert.Contains(*aggregate.Hosts[3].Error, "duplicate node ID")
}

func TestBuildNeutralAggregateRejectsSelfReportedNodeMismatch(t *testing.T) {
	assert := assert.New(t)
	localID := NodeID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	enrolledID := NodeID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	reportedID := NodeID("cccccccccccccccccccccccccccccccc")
	member := RawSnapshot{
		ProtocolVersion: 3, NodeID: reportedID,
		Projects: []RawProject{{ScopedKey: "repo:/member"}},
	}
	aggregate := BuildNeutralAggregate(
		RawSnapshot{ProtocolVersion: 3, NodeID: localID},
		[]PeerResult{{NodeID: enrolledID, Reachable: true, Raw: &member}},
	)

	require.Len(t, aggregate.Hosts, 2)
	assert.False(aggregate.Hosts[1].Reachable)
	assert.Contains(*aggregate.Hosts[1].Error, "does not match enrollment")
	assert.Empty(aggregate.Projects)
}

func TestBuildNeutralAggregatePreservesMemberTerminalFacts(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	localID := NodeID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	memberID := NodeID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	member := RawSnapshot{
		ProtocolVersion: 3, NodeID: memberID,
		Host: RawHost{
			Hostname: "mbp", Version: "9.9.9",
			TmuxLastPolledAt: "2026-05-31T10:00:00Z",
			TmuxProbeError:   "inventory failed", TmuxMetricsError: "ps failed",
			TmuxSessions: []TmuxSessionInfo{{Name: "w-1", Managed: true}},
		},
		Worktrees: []RawWorktree{{
			ScopedKey: "worktree:/b", SessionBackend: SessionBackendLocalPTY,
		}},
	}
	aggregate := BuildNeutralAggregate(
		RawSnapshot{ProtocolVersion: 3, NodeID: localID},
		[]PeerResult{{NodeID: memberID, Reachable: true, Raw: &member}},
	)

	require.Len(aggregate.Hosts, 2)
	host := aggregate.Hosts[1]
	assert.Equal("9.9.9", host.Version)
	assert.Equal("2026-05-31T10:00:00Z", host.TmuxLastPolledAt)
	assert.Equal("inventory failed", host.TmuxProbeError)
	assert.Equal("ps failed", host.TmuxMetricsError)
	require.Len(host.TmuxSessions, 1)
	require.Len(aggregate.Worktrees, 1)
	assert.Equal(SessionBackendLocalPTY, aggregate.Worktrees[0].SessionBackend)
	assert.Equal(string(memberID), aggregate.Worktrees[0].HostKey)
}

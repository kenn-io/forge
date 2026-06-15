package fleet

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeStampsKeysAndHandlesDegraded(t *testing.T) {
	local := RawSnapshot{Host: RawHost{Hostname: "studio"}, Projects: []RawProject{{ScopedKey: "repo:/a", Name: "a"}}}
	ok := PeerResult{Key: "mbp", Name: "mbp", BaseURL: "http://mbp:8091", Reachable: true, ObservedAt: "T",
		Raw: &RawSnapshot{Projects: []RawProject{{ScopedKey: "repo:/b", Name: "b"}}}}
	down := PeerResult{Key: "epyc", Name: "epyc", BaseURL: "http://epyc:8091", Reachable: false, ObservedAt: "T", Err: new("timeout")}
	dup := PeerResult{Key: "mbp", Name: "mbp2", BaseURL: "http://other:8091", Reachable: true, ObservedAt: "T",
		Raw: &RawSnapshot{Projects: []RawProject{{ScopedKey: "repo:/c", Name: "c"}}}}

	assert := assert.New(t)
	merged := Merge(local, "studio", []PeerResult{ok, down, dup})

	assert.Empty(local.Projects[0].HostKey, "Merge must not mutate local input")
	assert.Empty(ok.Raw.Projects[0].HostKey, "Merge must not mutate peer input")

	assert.Equal("studio", merged.Projects[0].HostKey, "local project hostKey")
	var sawB bool
	for _, p := range merged.Projects {
		if p.ScopedKey == "repo:/b" {
			assert.Equal("mbp", p.HostKey, "peer project hostKey")
			sawB = true
		}
		assert.NotEqual("repo:/c", p.ScopedKey, "duplicate-key peer entities must be dropped")
	}
	assert.True(sawB, "reachable peer entities must be merged")
	var down1, demoted int
	for _, h := range merged.RemoteHosts {
		if !h.Reachable {
			down1++
		}
		if h.Error != nil && *h.Error == "duplicate host key mbp" {
			demoted++
		}
	}
	assert.Equal(2, down1, "want 2 unreachable incl 1 demoted-dup")
	assert.Equal(1, demoted, "want 1 demoted-dup")
}

// TestMergeStampsPeerWorktreesAsRemoteTmux proves a reachable peer's
// worktrees are reframed as remote-tmux attaches from the hub's view,
// while the local worktree's own backend is left untouched.
func TestMergeStampsPeerWorktreesAsRemoteTmux(t *testing.T) {
	local := RawSnapshot{
		Host:      RawHost{Hostname: "studio"},
		Worktrees: []RawWorktree{{ScopedKey: "worktree:/a", Name: "a", SessionBackend: "localTmux"}},
	}
	peer := PeerResult{Key: "mbp", Name: "mbp", BaseURL: "http://mbp:8091", Reachable: true, ObservedAt: "T",
		Raw: &RawSnapshot{Worktrees: []RawWorktree{{ScopedKey: "worktree:/b", Name: "b", SessionBackend: "localPTY"}}}}

	assert := assert.New(t)
	merged := Merge(local, "studio", []PeerResult{peer})

	assert.Equal("localPTY", peer.Raw.Worktrees[0].SessionBackend, "Merge must not mutate peer input")
	var sawLocal, sawPeer bool
	for _, w := range merged.Worktrees {
		switch w.ScopedKey {
		case "worktree:/a":
			assert.Equal("studio", w.HostKey)
			assert.Equal("localTmux", w.SessionBackend, "local worktree backend is untouched")
			sawLocal = true
		case "worktree:/b":
			assert.Equal("mbp", w.HostKey)
			assert.Equal(SessionBackendRemoteTmux, w.SessionBackend, "peer worktree backend becomes remoteTmux")
			sawPeer = true
		}
	}
	assert.True(sawLocal, "local worktree must be present")
	assert.True(sawPeer, "peer worktree must be present")
}

// TestMergeFoldsPeerVersionAndTmux proves a reachable peer's host version
// and tmux inventory fold into its RawRemoteHost record, so the hub can
// surface a peer's version and live tmux sessions in the merged graph.
func TestMergeFoldsPeerVersionAndTmux(t *testing.T) {
	local := RawSnapshot{Host: RawHost{Hostname: "studio"}}
	peer := PeerResult{
		Key: "mbp", Name: "mbp", BaseURL: "http://mbp:8091", Reachable: true, ObservedAt: "T",
		Raw: &RawSnapshot{Host: RawHost{
			Hostname:         "mbp",
			Version:          "9.9.9",
			TmuxLastPolledAt: "2026-05-31T10:00:00Z",
			TmuxProbeError:   "inventory failed",
			TmuxMetricsError: "ps failed",
			TmuxSessions:     []TmuxSessionInfo{{Name: "w-1", Managed: true, WorktreeKey: "worktree:/x"}},
		}},
	}

	require := require.New(t)
	assert := assert.New(t)
	merged := Merge(local, "studio", []PeerResult{peer})

	var rh *RawRemoteHost
	for i := range merged.RemoteHosts {
		if merged.RemoteHosts[i].HostKey == "mbp" {
			rh = &merged.RemoteHosts[i]
		}
	}
	require.NotNil(rh, "reachable peer must yield a remote host record")
	assert.Equal("9.9.9", rh.Version, "peer host version must fold into the remote host record")
	assert.Equal("2026-05-31T10:00:00Z", rh.TmuxLastPolledAt, "peer tmux freshness must fold into the remote host record")
	assert.Equal("inventory failed", rh.TmuxProbeError, "peer tmux probe error must fold into the remote host record")
	assert.Equal("ps failed", rh.TmuxMetricsError, "peer tmux metrics error must fold into the remote host record")
	require.Len(rh.TmuxSessions, 1, "peer tmux inventory must fold into the remote host record")
	assert.Equal("w-1", rh.TmuxSessions[0].Name)
}

// TestMergeFoldsPeerSSHDestinationAndTransport proves a peer's SSH destination
// and preferred transport fold into its RawRemoteHost record for both reachable
// and unreachable peers. The SSH fleet transport supplies these so the merged remote host stays
// routable; an unreachable peer keeps its destination so a client can reconnect.
func TestMergeFoldsPeerSSHDestinationAndTransport(t *testing.T) {
	local := RawSnapshot{Host: RawHost{Hostname: "studio"}}
	dest := "rpi5-ssd"
	reachable := PeerResult{
		Key: "rpi5", Name: "rpi5", Reachable: true, ObservedAt: "T",
		SSHDestination: &dest, PreferredTransport: "ssh",
		Raw: &RawSnapshot{Host: RawHost{Hostname: "rpi5"}},
	}
	downDest := "epyc.local"
	unreachable := PeerResult{
		Key: "epyc", Name: "epyc", Reachable: false,
		SSHDestination: &downDest, PreferredTransport: "ssh",
	}

	require := require.New(t)
	assert := assert.New(t)
	merged := Merge(local, "studio", []PeerResult{reachable, unreachable})

	byKey := map[string]RawRemoteHost{}
	for _, rh := range merged.RemoteHosts {
		byKey[rh.HostKey] = rh
	}
	require.Contains(byKey, "rpi5")
	require.Contains(byKey, "epyc")
	require.NotNil(byKey["rpi5"].SSHDestination)
	assert.Equal("rpi5-ssd", *byKey["rpi5"].SSHDestination, "reachable peer ssh destination must fold in")
	assert.Equal("ssh", byKey["rpi5"].PreferredTransport)
	require.NotNil(byKey["epyc"].SSHDestination,
		"unreachable peer must keep its ssh destination so a client can reconnect")
	assert.Equal("epyc.local", *byKey["epyc"].SSHDestination)
	assert.Equal("ssh", byKey["epyc"].PreferredTransport)
}

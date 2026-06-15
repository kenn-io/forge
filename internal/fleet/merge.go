package fleet

// PeerResult is the hub's per-peer fetch outcome. Key is the configured
// peer key and is AUTHORITATIVE: it overwrites any host key a peer
// self-reports, so a misconfigured or hostile peer cannot impersonate
// another host in the merged graph.
type PeerResult struct {
	Key        string
	Name       string
	BaseURL    string
	Platform   string
	ObservedAt string
	Reachable  bool
	Raw        *RawSnapshot // nil when unreachable
	Err        *string
	// SSHDestination and PreferredTransport describe how to reach a peer
	// over SSH. The SSH fleet transport sets them so the merged remote
	// host stays routable; they fold in even for an unreachable peer so
	// a client can still reconnect.
	SSHDestination     *string
	PreferredTransport string
}

// Merge stamps host keys and folds peers into one raw graph. Pure: no
// network, no clock, and no mutation of inputs — local and each peer's
// Raw snapshot are left untouched (host keys are stamped onto copies).
// Local entities are stamped with selfKey; each reachable peer's
// entities are stamped (overwriting any self-reported key) with the
// configured peer key. Unreachable peers and peers whose key collides
// with an already-seen host become RawRemoteHost diagnostics with their
// entities dropped.
func Merge(local RawSnapshot, selfKey string, peers []PeerResult) RawSnapshot {
	out := local
	out.Projects = stampedProjects(local.Projects, selfKey)
	out.Worktrees = stampedWorktrees(local.Worktrees, selfKey)
	out.Sessions = stampedSessions(local.Sessions, selfKey)
	out.RemoteHosts = append([]RawRemoteHost(nil), local.RemoteHosts...)

	seen := map[string]bool{selfKey: true}
	for _, p := range peers {
		if !p.Reachable || p.Raw == nil {
			out.RemoteHosts = append(out.RemoteHosts, remoteHost(p, false))
			continue
		}
		if seen[p.Key] {
			dupErr := "duplicate host key " + p.Key
			rh := remoteHost(p, false)
			rh.Error = &dupErr
			out.RemoteHosts = append(out.RemoteHosts, rh)
			continue
		}
		seen[p.Key] = true
		out.Projects = append(out.Projects, stampedProjects(p.Raw.Projects, p.Key)...)
		out.Worktrees = append(out.Worktrees, stampedRemoteWorktrees(p.Raw.Worktrees, p.Key)...)
		out.Sessions = append(out.Sessions, stampedSessions(p.Raw.Sessions, p.Key)...)
		rh := remoteHost(p, true)
		rh.Generation = p.Raw.Generation
		rh.Capabilities = p.Raw.Capabilities
		rh.PlatformAuthenticated = p.Raw.PlatformAuthenticated
		rh.Version = p.Raw.Host.Version
		rh.TmuxLastPolledAt = p.Raw.Host.TmuxLastPolledAt
		rh.TmuxProbeError = p.Raw.Host.TmuxProbeError
		rh.TmuxMetricsError = p.Raw.Host.TmuxMetricsError
		rh.TmuxSessions = p.Raw.Host.TmuxSessions
		out.RemoteHosts = append(out.RemoteHosts, rh)
	}
	return out
}

func remoteHost(p PeerResult, reachable bool) RawRemoteHost {
	// fleet.peers[].name is optional: fall back to the hostname the peer
	// reports about itself, then to the peer key, so a nameless peer never
	// surfaces as an empty host name.
	name := p.Name
	if name == "" && p.Raw != nil {
		name = p.Raw.Host.Hostname
	}
	if name == "" {
		name = p.Key
	}
	return RawRemoteHost{
		HostKey:            p.Key,
		Name:               name,
		BaseURL:            p.BaseURL,
		Platform:           p.Platform,
		Reachable:          reachable,
		LastSeenAt:         p.ObservedAt,
		Error:              p.Err,
		SSHDestination:     p.SSHDestination,
		PreferredTransport: p.PreferredTransport,
	}
}

// stampedProjects returns a copy of ps with every HostKey set to key,
// leaving the input slice and its backing array untouched. The shallow
// copy is sufficient: only the HostKey string is rewritten; pointer
// fields are shared but never written through.
func stampedProjects(ps []RawProject, key string) []RawProject {
	out := make([]RawProject, len(ps))
	copy(out, ps)
	for i := range out {
		out[i].HostKey = key
	}
	return out
}

func stampedWorktrees(ws []RawWorktree, key string) []RawWorktree {
	out := make([]RawWorktree, len(ws))
	copy(out, ws)
	for i := range out {
		out[i].HostKey = key
	}
	return out
}

func stampedSessions(ss []RawSession, key string) []RawSession {
	out := make([]RawSession, len(ss))
	copy(out, ss)
	for i := range out {
		out[i].HostKey = key
	}
	return out
}

// stampedRemoteWorktrees copies ws with HostKey set to key and SessionBackend
// forced to remoteTmux. From the hub's view a peer worktree is reached over a
// remote tmux attach, regardless of the backend the peer reports for its own
// local sessions, so the hub-side backend is always remote.
func stampedRemoteWorktrees(ws []RawWorktree, key string) []RawWorktree {
	out := make([]RawWorktree, len(ws))
	copy(out, ws)
	for i := range out {
		out[i].HostKey = key
		out[i].SessionBackend = SessionBackendRemoteTmux
	}
	return out
}

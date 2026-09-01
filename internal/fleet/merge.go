package fleet

// PeerResult is the hub's fetch result for one enrolled member.
// NodeID comes from enrollment and remains authoritative over self-reported
// payload fields.
type PeerResult struct {
	NodeID     NodeID
	Name       string
	BaseURL    string
	Role       Role
	Platform   string
	ObservedAt string
	Reachable  bool
	Raw        *RawSnapshot
	Err        *string
}

// BuildNeutralAggregate combines local authority with direct member raw
// snapshots. It stamps every entity with its enrolled node ID and never adds
// observer-relative kind, transport, or operation availability.
func BuildNeutralAggregate(local RawSnapshot, members []PeerResult) NeutralSnapshot {
	out := NeutralSnapshot{
		ProtocolVersion:       local.ProtocolVersion,
		Generation:            local.Generation,
		PlatformAuthenticated: local.PlatformAuthenticated,
		Hosts:                 []NeutralHost{neutralLocalHost(local, RoleHub)},
		Projects:              stampedProjects(local.Projects, string(local.NodeID)),
		Worktrees:             stampedWorktrees(local.Worktrees, string(local.NodeID)),
		Sessions:              stampedSessions(local.Sessions, string(local.NodeID)),
		Workspaces:            stampedWorkspaces(local.Workspaces, string(local.NodeID)),
	}

	seen := map[NodeID]bool{local.NodeID: true}
	for _, member := range members {
		if seen[member.NodeID] {
			duplicate := member
			duplicate.Reachable = false
			message := "duplicate node ID " + string(member.NodeID)
			duplicate.Err = &message
			out.Hosts = append(out.Hosts, neutralMemberHost(duplicate))
			continue
		}
		seen[member.NodeID] = true
		if !member.Reachable || member.Raw == nil {
			out.Hosts = append(out.Hosts, neutralMemberHost(member))
			continue
		}
		if member.Raw.NodeID != member.NodeID {
			mismatch := member
			mismatch.Reachable = false
			message := "member snapshot node ID does not match enrollment"
			mismatch.Err = &message
			out.Hosts = append(out.Hosts, neutralMemberHost(mismatch))
			continue
		}
		out.Hosts = append(out.Hosts, neutralMemberHost(member))
		key := string(member.NodeID)
		out.Projects = append(out.Projects, stampedProjects(member.Raw.Projects, key)...)
		out.Worktrees = append(out.Worktrees, stampedWorktrees(member.Raw.Worktrees, key)...)
		out.Sessions = append(out.Sessions, stampedSessions(member.Raw.Sessions, key)...)
		out.Workspaces = append(out.Workspaces, stampedWorkspaces(member.Raw.Workspaces, key)...)
	}
	return out
}

func neutralLocalHost(raw RawSnapshot, role Role) NeutralHost {
	name := raw.Host.Hostname
	if name == "" {
		name = string(raw.NodeID)
	}
	return NeutralHost{
		NodeID: raw.NodeID, FederationRole: role, Name: name,
		Hostname: raw.Host.Hostname, BaseURL: raw.BaseURL,
		Platform:  raw.Host.Platform,
		Reachable: true, PlatformAuthenticated: raw.PlatformAuthenticated,
		Generation: raw.Generation, Version: raw.Host.Version,
		LastSeenAt:       raw.Host.LastSeenAt,
		TmuxLastPolledAt: raw.Host.TmuxLastPolledAt,
		TmuxProbeError:   raw.Host.TmuxProbeError,
		TmuxMetricsError: raw.Host.TmuxMetricsError,
		Capabilities:     raw.Capabilities,
		TmuxSessions:     append([]TmuxSessionInfo(nil), raw.Host.TmuxSessions...),
	}
}

func neutralMemberHost(member PeerResult) NeutralHost {
	name := member.Name
	if name == "" && member.Raw != nil {
		name = member.Raw.Host.Hostname
	}
	if name == "" {
		name = string(member.NodeID)
	}
	role := member.Role
	if role == "" {
		role = RoleSpoke
	}
	host := NeutralHost{
		NodeID: member.NodeID, FederationRole: role, Name: name, BaseURL: member.BaseURL,
		Platform: member.Platform, Reachable: member.Reachable, Error: member.Err,
		LastSeenAt: member.ObservedAt,
	}
	if member.Raw == nil {
		return host
	}
	host.Platform = member.Raw.Host.Platform
	host.Hostname = member.Raw.Host.Hostname
	host.PlatformAuthenticated = member.Raw.PlatformAuthenticated
	host.Generation = member.Raw.Generation
	host.Version = member.Raw.Host.Version
	host.TmuxLastPolledAt = member.Raw.Host.TmuxLastPolledAt
	host.TmuxProbeError = member.Raw.Host.TmuxProbeError
	host.TmuxMetricsError = member.Raw.Host.TmuxMetricsError
	host.Capabilities = member.Raw.Capabilities
	host.TmuxSessions = append([]TmuxSessionInfo(nil), member.Raw.Host.TmuxSessions...)
	return host
}

func stampedProjects(projects []RawProject, nodeID string) []RawProject {
	out := make([]RawProject, len(projects))
	copy(out, projects)
	for index := range out {
		out[index].HostKey = nodeID
	}
	return out
}

func stampedWorktrees(worktrees []RawWorktree, nodeID string) []RawWorktree {
	out := make([]RawWorktree, len(worktrees))
	copy(out, worktrees)
	for index := range out {
		out[index].HostKey = nodeID
	}
	return out
}

func stampedSessions(sessions []RawSession, nodeID string) []RawSession {
	out := make([]RawSession, len(sessions))
	copy(out, sessions)
	for index := range out {
		out[index].HostKey = nodeID
	}
	return out
}

func stampedWorkspaces(workspaces []RawWorkspace, nodeID string) []RawWorkspace {
	out := make([]RawWorkspace, len(workspaces))
	copy(out, workspaces)
	for index := range out {
		out[index].HostKey = nodeID
	}
	return out
}

package fleet

import (
	"strings"
	"time"
)

// BuildEnriched transforms a raw, host-key-stamped snapshot into the
// enriched, client-ready Snapshot. Every entity ID is a deterministic
// UUID-v5 derived from that entity's own host key, so the same scoped key
// on two hosts yields distinct IDs (no cross-host collisions). selfKey
// identifies the local host. connectionState, when non-nil, resolves a
// per-host connection-state string by host key (nil in local-only builds).
func BuildEnriched(
	raw RawSnapshot,
	selfKey string,
	connectionState func(hostKey string) *string,
	policy AvailabilityPolicy,
	identity Identity,
) Snapshot {
	active := ActivePlatformHost(raw.Projects)

	resp := Snapshot{
		SchemaVersion:         SchemaVersion,
		Generation:            raw.Generation,
		PlatformAuthenticated: raw.PlatformAuthenticated,
		ActivePlatformHost:    active,
	}
	resp.Hosts = buildHosts(raw, selfKey, connectionState, policy, identity)
	resp.Projects = buildProjects(raw.Projects, active, identity)
	resp.ProjectMap = buildProjectMap(resp.Projects)
	worktrees, filteredKeys := buildWorktrees(raw.Worktrees, identity)
	resp.Worktrees = worktrees
	resp.Sessions = buildSessions(raw.Sessions, filteredKeys, identity)
	return resp
}

// MapConnectionState maps internal connection states to the protocol enum
// (connecting, online, degraded, offline). Returns nil for unknown states.
func MapConnectionState(internalState string) *string {
	var mapped string
	switch internalState {
	case "connecting":
		mapped = "connecting"
	case "connected":
		mapped = "online"
	case "probe_failed":
		mapped = "degraded"
	case "disconnected", "error":
		mapped = "offline"
	default:
		return nil
	}
	return &mapped
}

func buildHosts(
	raw RawSnapshot,
	selfKey string,
	connectionState func(hostKey string) *string,
	policy AvailabilityPolicy,
	identity Identity,
) []HostSummary {
	hosts := make([]HostSummary, 0, 1+len(raw.RemoteHosts))

	local := HostSummary{
		ID:                    identity.HostID(selfKey),
		ConfigKey:             selfKey,
		Name:                  raw.Host.Hostname,
		Kind:                  "self",
		Platform:              raw.Host.Platform,
		Reachable:             true,
		PreferredTransport:    "local",
		Capabilities:          raw.Capabilities,
		Diagnostics:           []HostDiagnostic{},
		OperationAvailability: map[string]HostOperationAvailability{},
	}
	if raw.Host.Hostname != "" {
		local.Hostname = &raw.Host.Hostname
	}
	if raw.Host.Version != "" {
		local.Version = &raw.Host.Version
	}
	if raw.Host.TmuxLastPolledAt != "" {
		ts := normalizeDateValue(raw.Host.TmuxLastPolledAt)
		local.TmuxLastPolledAt = &ts
	}
	local.TmuxProbeError = raw.Host.TmuxProbeError
	local.TmuxMetricsError = raw.Host.TmuxMetricsError
	localDiags := tmuxProbeDiagnostics(
		raw.Host.TmuxProbeError,
		raw.Host.TmuxMetricsError,
	)
	if raw.Capabilities != nil {
		localDiags = append(
			DiagnosticsFromCapabilities(*raw.Capabilities, raw.PlatformAuthenticated),
			localDiags...,
		)
	}
	if localDiags == nil {
		localDiags = []HostDiagnostic{}
	}
	local.Diagnostics = localDiags
	if raw.Capabilities != nil {
		local.OperationAvailability = OperationAvailabilityFromState(
			localDiags, raw.Capabilities.Commands, true, selfPolicy(policy),
		)
	}
	local.TmuxSessions = tmuxOrEmpty(raw.Host.TmuxSessions)
	if raw.Host.LastSeenAt != "" {
		ts := normalizeDateValue(raw.Host.LastSeenAt)
		local.LastSeenAt = &ts
	}
	hosts = append(hosts, local)

	for _, rh := range raw.RemoteHosts {
		var cs *string
		if connectionState != nil {
			cs = connectionState(rh.HostKey)
		}
		hosts = append(hosts, buildRemoteHost(rh, cs, policy, identity))
	}
	return hosts
}

func buildRemoteHost(rh RawRemoteHost, connectionState *string, policy AvailabilityPolicy, identity Identity) HostSummary {
	h := HostSummary{
		ID:                    identity.HostID(rh.HostKey),
		ConfigKey:             rh.HostKey,
		Name:                  rh.Name,
		Kind:                  "remote",
		Platform:              strings.ToLower(rh.Platform),
		Reachable:             rh.Reachable,
		PreferredTransport:    remoteTransport(rh.PreferredTransport),
		Capabilities:          rh.Capabilities,
		Diagnostics:           []HostDiagnostic{},
		OperationAvailability: map[string]HostOperationAvailability{},
	}
	if rh.LastSeenAt != "" {
		ts := normalizeDateValue(rh.LastSeenAt)
		h.LastSeenAt = &ts
	}
	if rh.TmuxLastPolledAt != "" {
		ts := normalizeDateValue(rh.TmuxLastPolledAt)
		h.TmuxLastPolledAt = &ts
	}
	h.TmuxProbeError = rh.TmuxProbeError
	h.TmuxMetricsError = rh.TmuxMetricsError
	diags := tmuxProbeDiagnostics(
		rh.TmuxProbeError,
		rh.TmuxMetricsError,
	)
	if rh.Capabilities != nil {
		diags = append(
			DiagnosticsFromCapabilities(*rh.Capabilities, rh.PlatformAuthenticated),
			diags...,
		)
	}
	if diags == nil {
		diags = []HostDiagnostic{}
	}
	h.Diagnostics = diags
	hostPolicy := policyForHost(policy, rh.HostKey)
	if rh.Capabilities != nil {
		h.OperationAvailability = OperationAvailabilityFromState(
			diags, rh.Capabilities.Commands, rh.Reachable, hostPolicy,
		)
	} else {
		h.OperationAvailability = OperationAvailabilityFromState(
			nil, CommandCapabilities{}, rh.Reachable, hostPolicy,
		)
	}
	h.Error = rh.Error
	h.ConnectionState = connectionState
	h.SSHDestination = rh.SSHDestination
	if rh.Version != "" {
		h.Version = &rh.Version
	}
	h.TmuxSessions = tmuxOrEmpty(rh.TmuxSessions)
	return h
}

func tmuxProbeDiagnostics(
	probeError string,
	metricsError string,
) []HostDiagnostic {
	var out []HostDiagnostic
	if probeError != "" {
		out = append(out, HostDiagnostic{
			Code:               "tmuxProbeFailed",
			Severity:           "warning",
			Summary:            "Tmux inventory probe failed",
			RecoverySuggestion: "Check that tmux is running and the configured tmux command can list sessions.",
		})
	}
	if metricsError != "" {
		out = append(out, HostDiagnostic{
			Code:               "tmuxMetricsUnavailable",
			Severity:           "warning",
			Summary:            "Tmux process metrics unavailable",
			RecoverySuggestion: "Check that tmux and process table commands are available on the host.",
		})
	}
	return out
}

func buildProjects(raw []RawProject, activeHost *string, identity Identity) []ProjectSummary {
	out := make([]ProjectSummary, 0, len(raw))
	for _, p := range raw {
		proj := ProjectSummary{
			ID:             identity.EntityID(p.HostKey, p.ScopedKey),
			HostID:         identity.HostID(p.HostKey),
			RegistryID:     p.RegistryID,
			ScopedKey:      p.ScopedKey,
			Name:           p.Name,
			RootPath:       p.RootPath,
			DefaultBranch:  p.DefaultBranch,
			RepositoryKind: p.RepositoryKind,
			Platform:       p.Platform,
			IsStale:        p.IsStale,
			IsSynthesized:  p.IsSynthesized,
		}
		if p.PlatformRepo != "" {
			url := "https://" + effectiveHost(p.PlatformHost) + "/" + p.PlatformRepo
			proj.PlatformURL = &url
		}
		proj.PlatformCoverage = PlatformCoverage(p, activeHost)
		out = append(out, proj)
	}
	return out
}

type worktreeKey struct {
	hostKey   string
	scopedKey string
}

func buildWorktrees(raw []RawWorktree, identity Identity) ([]WorktreeSummary, map[worktreeKey]bool) {
	out := make([]WorktreeSummary, 0, len(raw))
	filtered := make(map[worktreeKey]bool)
	for _, wt := range raw {
		if wt.IsStale && !wt.IsPrimary {
			filtered[worktreeKey{wt.HostKey, wt.ScopedKey}] = true
			continue
		}
		out = append(out, buildWorktree(wt, identity))
	}
	return out, filtered
}

func buildWorktree(wt RawWorktree, identity Identity) WorktreeSummary {
	w := WorktreeSummary{
		ID:                 identity.EntityID(wt.HostKey, wt.ScopedKey),
		HostID:             identity.HostID(wt.HostKey),
		ProjectID:          identity.EntityID(wt.HostKey, wt.ProjectKey),
		RegistryID:         wt.RegistryID,
		ScopedKey:          wt.ScopedKey,
		Name:               wt.Name,
		Path:               wt.Path,
		Branch:             wt.Branch,
		IsPrimary:          wt.IsPrimary,
		IsHidden:           wt.IsHidden,
		IsStale:            wt.IsStale,
		DiffAdded:          wt.DiffAdded,
		DiffRemoved:        wt.DiffRemoved,
		SyncAhead:          wt.SyncAhead,
		SyncBehind:         wt.SyncBehind,
		LinkedPRNumber:     wt.LinkedPRNumber,
		PRURL:              wt.PRURL,
		PRTitle:            wt.PRTitle,
		PRUpdatedAt:        normalizeDateStrPtr(wt.PRUpdatedAt),
		ChecksDetail:       lowerChecks(wt.ChecksDetail),
		LastPolledAt:       normalizeDateStrPtr(wt.LastPolledAt),
		SessionBackend:     sessionBackendOrDefault(wt.SessionBackend),
		LinkedIssueNumbers: intsOrEmpty(wt.LinkedIssueNumbers),
	}
	w.PRState = lowerPtr(wt.PRState)
	w.ChecksStatus = lowerPtr(wt.ChecksStatus)
	return w
}

// sessionBackendOrDefault normalizes a raw worktree session backend onto the
// exported canonical vocabulary (localPTY, localTmux, remoteTmux), defaulting
// an empty value to localPTY. A registered worktree with no active session
// carries no backend; emitting an empty string forces strict consumers to
// special-case it, so default it to the local-PTY attach instead. Values
// outside the vocabulary pass through unchanged.
func sessionBackendOrDefault(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return SessionBackendLocalPTY
	}
	for _, canonical := range []string{
		SessionBackendLocalPTY,
		SessionBackendLocalTmux,
		SessionBackendRemoteTmux,
	} {
		if strings.EqualFold(raw, canonical) {
			return canonical
		}
	}
	return raw
}

// lowerChecks lowercases the status/conclusion of each check, mirroring the
// PR-state/checks-status normalization. Returns nil for an empty input.
func lowerChecks(in []CheckDetail) []CheckDetail {
	if len(in) == 0 {
		return nil
	}
	out := make([]CheckDetail, len(in))
	for i, c := range in {
		c.Status = strings.ToLower(c.Status)
		c.Conclusion = strings.ToLower(c.Conclusion)
		out[i] = c
	}
	return out
}

// intsOrEmpty guarantees a non-nil slice so the enriched JSON emits [] not null.
func intsOrEmpty(in []int) []int {
	if in == nil {
		return []int{}
	}
	return in
}

// tmuxOrEmpty guarantees a non-nil slice so tmuxSessions emits [] not null.
func tmuxOrEmpty(in []TmuxSessionInfo) []TmuxSessionInfo {
	if in == nil {
		return []TmuxSessionInfo{}
	}
	return in
}

// remoteTransport defaults an unset remote transport to "http" so a hub peer's
// preferred transport is unchanged while a local daemon can advertise ssh/mosh.
func remoteTransport(t string) string {
	if t == "" {
		return "http"
	}
	return t
}

func buildSessions(raw []RawSession, filteredWorktreeKeys map[worktreeKey]bool, identity Identity) []SessionSummary {
	out := make([]SessionSummary, 0, len(raw))
	for _, s := range raw {
		if s.WorktreeKey != "" {
			if filteredWorktreeKeys[worktreeKey{s.HostKey, s.WorktreeKey}] {
				continue
			}
		}
		out = append(out, buildSession(s, identity))
	}
	return out
}

func buildSession(sess RawSession, identity Identity) SessionSummary {
	s := SessionSummary{
		ID:             identity.EntityID(sess.HostKey, sess.ScopedKey),
		HostID:         identity.HostID(sess.HostKey),
		ScopedKey:      sess.ScopedKey,
		RuntimeKind:    sess.RuntimeKind,
		Status:         sess.Status,
		SessionKind:    sess.SessionKind,
		Role:           sess.Role,
		ExecutableName: sess.ExecutableName,
		AgentKind:      sess.AgentKind,
		CPUPercent:     sess.CPUPercent,
		ResidentMB:     sess.ResidentMB,
		ProcessCount:   sess.ProcessCount,
		LastActiveAt:   normalizeDateStrPtr(sess.LastActiveAt),
	}
	if sess.WorktreeKey != "" {
		id := identity.EntityID(sess.HostKey, sess.WorktreeKey)
		s.WorktreeID = &id
	}
	if sess.LastOutputAt != nil {
		s.LastOutputAt = normalizeDateStr(*sess.LastOutputAt)
	}
	return s
}

// normalizeDateStrPtr normalizes an optional RFC3339 timestamp pointer.
func normalizeDateStrPtr(s *string) *string {
	if s == nil {
		return nil
	}
	return normalizeDateStr(*s)
}

// buildProjectMap maps "owner/name@host" to project ID for projects that
// have a platform URL.
func buildProjectMap(projects []ProjectSummary) map[string]string {
	m := make(map[string]string)
	for _, p := range projects {
		if p.PlatformURL == nil {
			continue
		}
		owner, name, host, ok := parsePlatformURL(*p.PlatformURL)
		if !ok {
			continue
		}
		m[owner+"/"+name+"@"+host] = p.ID
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func parsePlatformURL(rawURL string) (owner, name, host string, ok bool) {
	u := rawURL
	if idx := strings.Index(u, "://"); idx >= 0 {
		u = u[idx+3:]
	}
	slash := strings.IndexByte(u, '/')
	if slash < 0 {
		return "", "", "", false
	}
	host = u[:slash]
	path := strings.TrimPrefix(u[slash:], "/")
	path = strings.TrimSuffix(path, ".git")
	// The repo name is the final path segment; everything before it is the
	// owner path. Nested groups (e.g. GitLab "group/subgroup/project") keep
	// their full owner so the project key round-trips instead of truncating
	// to the first two segments and losing the repo.
	lastSlash := strings.LastIndexByte(path, '/')
	if lastSlash <= 0 || lastSlash == len(path)-1 {
		return "", "", "", false
	}
	return path[:lastSlash], path[lastSlash+1:], host, true
}

func lowerPtr(s *string) *string {
	if s == nil {
		return nil
	}
	low := strings.ToLower(*s)
	return &low
}

func normalizeDateStr(s string) *string {
	if s == "" {
		return nil
	}
	out := normalizeDateValue(s)
	return &out
}

func normalizeDateValue(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.Format("2006-01-02T15:04:05.000Z07:00")
}

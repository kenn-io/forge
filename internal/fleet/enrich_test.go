package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

const SchemaVersion = 2

// legacyRawSnapshot keeps the pre-federation projection fixtures concise while
// exercising those fixtures through the new neutral aggregate boundary. It is
// test-only; the production raw contract has no remote-host field.
type legacyRawSnapshot struct {
	SchemaVersion         int
	Generation            uint64
	Host                  RawHost
	PlatformAuthenticated *bool
	Capabilities          *Capabilities
	Projects              []RawProject
	Worktrees             []RawWorktree
	Sessions              []RawSession
	RemoteHosts           []RawRemoteHost
}

type RawRemoteHost struct {
	HostKey               string
	Name                  string
	BaseURL               string
	Platform              string
	Reachable             bool
	PlatformAuthenticated *bool
	Generation            uint64
	Version               string
	LastSeenAt            string
	TmuxLastPolledAt      string
	TmuxProbeError        string
	TmuxMetricsError      string
	Error                 *string
	Capabilities          *Capabilities
	PreferredTransport    string
	TmuxSessions          []TmuxSessionInfo
}

func projectLegacyFixture(
	raw legacyRawSnapshot,
	selfKey string,
	_ *string,
	policy AvailabilityPolicy,
	identity Identity,
) Snapshot {
	if selfKey == "" {
		selfKey = raw.Host.Hostname
	}
	if selfKey == "" {
		selfKey = "self"
	}
	observer := NodeID(selfKey)
	local := RawSnapshot{
		ProtocolVersion:       3,
		NodeID:                observer,
		Generation:            raw.Generation,
		Host:                  raw.Host,
		PlatformAuthenticated: raw.PlatformAuthenticated,
		Capabilities:          raw.Capabilities,
		Projects:              localProjects(raw.Projects, selfKey),
		Worktrees:             localWorktrees(raw.Worktrees, selfKey),
		Sessions:              localSessions(raw.Sessions, selfKey),
	}
	neutral := NeutralSnapshot{
		ProtocolVersion:       3,
		Generation:            raw.Generation,
		PlatformAuthenticated: raw.PlatformAuthenticated,
		Hosts:                 []NeutralHost{neutralLocalHost(local, RoleHub)},
		Projects:              legacyProjects(raw.Projects, selfKey),
		Worktrees:             legacyWorktrees(raw.Worktrees, selfKey),
		Sessions:              legacySessions(raw.Sessions, selfKey),
	}
	for _, remote := range raw.RemoteHosts {
		neutral.Hosts = append(neutral.Hosts, NeutralHost{
			NodeID: remoteNodeID(remote.HostKey), Name: remote.Name,
			Hostname: remote.Name, BaseURL: remote.BaseURL, Platform: remote.Platform,
			Reachable:             remote.Reachable,
			PlatformAuthenticated: remote.PlatformAuthenticated,
			Generation:            remote.Generation, Version: remote.Version,
			LastSeenAt: remote.LastSeenAt, TmuxLastPolledAt: remote.TmuxLastPolledAt,
			TmuxProbeError:   remote.TmuxProbeError,
			TmuxMetricsError: remote.TmuxMetricsError,
			Error:            remote.Error, Capabilities: remote.Capabilities,
			TmuxSessions: remote.TmuxSessions,
		})
	}
	got := ProjectForObserver(
		neutral, local, Observer{NodeID: observer, Role: RoleHub},
	)
	for index := range got.Hosts {
		host := &got.Hosts[index]
		commands := CommandCapabilities{}
		if host.Capabilities != nil {
			commands = host.Capabilities.Commands
		}
		host.OperationAvailability = OperationAvailabilityFromState(
			host.Diagnostics, commands, host.Reachable,
			policyForHost(policy, host.ConfigKey),
		)
	}
	_ = identity // The production projection owns the canonical identity now.
	return got
}

func remoteNodeID(value string) NodeID { return NodeID(value) }

func TestProjectForObserverPublishesNavigationDirectory(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	hubID := NodeID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	nodeID := NodeID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	hub := RawSnapshot{
		ProtocolVersion: 3,
		NodeID:          hubID,
		BaseURL:         "https://hub.example:8443",
		Host:            RawHost{Hostname: "hub"},
	}
	spoke := RawSnapshot{
		ProtocolVersion: 3,
		NodeID:          nodeID,
		BaseURL:         "https://spoke.example",
		Host:            RawHost{Hostname: "spoke"},
	}
	aggregate := BuildNeutralAggregate(hub, []PeerResult{{
		NodeID: nodeID, Name: "spoke", BaseURL: spoke.BaseURL,
		Role: RoleSpoke, Reachable: true, Raw: &spoke,
	}})

	hubView := ProjectForObserver(
		aggregate, hub, Observer{NodeID: hubID, Role: RoleHub},
	)
	require.Len(hubView.Hosts, 2)
	assert.Equal(string(hubID), hubView.Hosts[0].ConfigKey)
	assert.Equal(hubView.Hosts[0].ConfigKey, hubView.Hosts[0].NodeID)
	assert.Equal(RoleHub, hubView.Hosts[0].FederationRole)
	assert.Equal("https://hub.example:8443", hubView.Hosts[0].BaseURL)
	assert.Equal(string(nodeID), hubView.Hosts[1].NodeID)
	assert.Equal(RoleSpoke, hubView.Hosts[1].FederationRole)
	assert.Equal("https://spoke.example", hubView.Hosts[1].BaseURL)

	nodeView := ProjectForObserver(
		aggregate, spoke, Observer{NodeID: nodeID, Role: RoleSpoke},
	)
	require.Len(nodeView.Hosts, 2)
	assert.Equal(RoleHub, nodeView.Hosts[0].FederationRole)
	assert.Equal("https://hub.example:8443", nodeView.Hosts[0].BaseURL)
	assert.Equal("self", nodeView.Hosts[1].Kind)
	assert.Equal(RoleSpoke, nodeView.Hosts[1].FederationRole)
	assert.Equal("https://spoke.example", nodeView.Hosts[1].BaseURL)

	removed := ProjectForObserver(
		BuildNeutralAggregate(hub, nil), hub,
		Observer{NodeID: hubID, Role: RoleHub},
	)
	require.Len(removed.Hosts, 1)
	assert.Equal(string(hubID), removed.Hosts[0].NodeID)
}

func localProjects(projects []RawProject, self string) []RawProject {
	var out []RawProject
	for _, project := range projects {
		if project.HostKey == "" || project.HostKey == self {
			project.HostKey = ""
			out = append(out, project)
		}
	}
	return out
}

func localWorktrees(worktrees []RawWorktree, self string) []RawWorktree {
	var out []RawWorktree
	for _, worktree := range worktrees {
		if worktree.HostKey == "" || worktree.HostKey == self {
			worktree.HostKey = ""
			out = append(out, worktree)
		}
	}
	return out
}

func localSessions(sessions []RawSession, self string) []RawSession {
	var out []RawSession
	for _, session := range sessions {
		if session.HostKey == "" || session.HostKey == self {
			session.HostKey = ""
			out = append(out, session)
		}
	}
	return out
}

func legacyProjects(projects []RawProject, self string) []RawProject {
	out := append([]RawProject(nil), projects...)
	for index := range out {
		if out[index].HostKey == "" {
			out[index].HostKey = self
		}
	}
	return out
}

func legacyWorktrees(worktrees []RawWorktree, self string) []RawWorktree {
	out := append([]RawWorktree(nil), worktrees...)
	for index := range out {
		if out[index].HostKey == "" {
			out[index].HostKey = self
		}
	}
	return out
}

func legacySessions(sessions []RawSession, self string) []RawSession {
	out := append([]RawSession(nil), sessions...)
	for index := range out {
		if out[index].HostKey == "" {
			out[index].HostKey = self
		}
	}
	return out
}

func TestHubEnrichesWorkspaceByStableProviderIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	repository := RepositoryIdentity{
		Provider: "gitlab", PlatformHost: "gitlab.example", PlatformRepoID: "gid://repo/42",
		Owner: "group/subgroup", Name: "widget",
	}
	repoID, err := database.UpsertRepoByProviderID(t.Context(), db.RepoIdentity{
		Platform: repository.Provider, PlatformHost: repository.PlatformHost,
		PlatformRepoID: repository.PlatformRepoID, Owner: repository.Owner, Name: repository.Name,
	})
	require.NoError(err)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 7, Number: 7, Title: "Fix race", Author: "dev",
		State: db.MergeRequestStateOpen, HeadBranch: "fix-race", BaseBranch: "main",
		CIStatus: "success", ReviewDecision: "approved", Additions: 12, Deletions: 3,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	aggregate := NeutralSnapshot{
		ProtocolVersion: 3,
		Projects: []RawProject{{
			HostKey: "spoke-a", ScopedKey: "repo:/widget", Repository: repository,
		}},
		Worktrees: []RawWorktree{{
			HostKey: "spoke-a", ScopedKey: "worktree:/widget", ProjectKey: "repo:/widget",
			LinkedPRNumber: new(7),
		}},
		Workspaces: []RawWorkspace{{
			HostKey: "spoke-a", ID: "ws-7", Repository: repository,
			ItemType: "pull_request", ItemNumber: 7,
		}},
	}

	enriched, err := EnrichProviderState(context.Background(), database, aggregate)
	require.NoError(err)
	require.Len(enriched.Workspaces, 1)
	require.NotNil(enriched.Workspaces[0].MRTitle)
	assert.Equal("Fix race", *enriched.Workspaces[0].MRTitle)
	require.Len(enriched.Worktrees, 1)
	require.NotNil(enriched.Worktrees[0].PRTitle)
	assert.Equal("Fix race", *enriched.Worktrees[0].PRTitle)
}

func TestBuildEnrichedDerivesDistinctPerHostIDs(t *testing.T) {
	raw := legacyRawSnapshot{
		SchemaVersion: SchemaVersion,
		Host:          RawHost{Hostname: "studio", Platform: "linux"},
		Projects: []RawProject{
			{HostKey: "studio", ScopedKey: "repo:/a", Name: "a", RootPath: "/a"},
			{HostKey: "mbp", ScopedKey: "repo:/a", Name: "a", RootPath: "/a"},
		},
		RemoteHosts: []RawRemoteHost{{HostKey: "mbp", Name: "mbp", Reachable: true}},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.Len(got.Projects, 2, "want 2 projects")
	assert.NotEqual(got.Projects[0].ID, got.Projects[1].ID, "per-host project IDs must differ (no cross-host ID collision)")
	require.Len(got.Hosts, 2, "want self + 1 remote host")
	assert.Equal("self", got.Hosts[0].Kind, "local host must be self")
	assert.Equal(HostID("studio"), got.Hosts[0].ID, "local host id must be HostID(selfKey)")
}

func TestBuildEnrichedFiltersStaleWorktreesAndSessions(t *testing.T) {
	raw := legacyRawSnapshot{
		Host: RawHost{Hostname: "studio", Platform: "linux"},
		Worktrees: []RawWorktree{
			{HostKey: "studio", ScopedKey: "worktree:/a", ProjectKey: "repo:/a", Path: "/a", IsPrimary: true},
			{HostKey: "studio", ScopedKey: "worktree:/stale", ProjectKey: "repo:/a", Path: "/stale", IsStale: true},
		},
		Sessions: []RawSession{
			{HostKey: "studio", ScopedKey: "session:s1", WorktreeKey: "worktree:/stale", Status: "running"},
			{HostKey: "studio", ScopedKey: "session:s2", WorktreeKey: "worktree:/a", Status: "running"},
		},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.Len(got.Worktrees, 1, "stale non-primary worktree must be filtered")
	assert.Equal("worktree:/a", got.Worktrees[0].ScopedKey)
	require.Len(got.Sessions, 1, "sessions on filtered worktrees must be dropped")
	assert.Equal("session:s2", got.Sessions[0].ScopedKey)
}

func TestBuildEnrichedUnreachableHostReportsOffline(t *testing.T) {
	e := "boom"
	raw := legacyRawSnapshot{
		Host:        RawHost{Hostname: "studio", Platform: "linux"},
		RemoteHosts: []RawRemoteHost{{HostKey: "down", Name: "down", Reachable: false, Error: &e}},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	var remote *HostSummary
	for i := range got.Hosts {
		if got.Hosts[i].Kind == "remote" {
			remote = &got.Hosts[i]
		}
	}
	require.NotNil(remote, "expected a remote host")
	require.NotEmpty(remote.OperationAvailability, "unreachable host must report operation availability, not an empty map")
	for op, a := range remote.OperationAvailability {
		assert.False(a.Available, "op %s must be unavailable on an unreachable host", op)
	}
	require.NotNil(remote.Error, "unreachable host must surface its peer-fetch error")
	assert.Equal("boom", *remote.Error)
}

func TestBuildEnrichedLowercasesPRStateAndBuildsURL(t *testing.T) {
	st := "OPEN"
	cs := "SUCCESS"
	raw := legacyRawSnapshot{
		Host:      RawHost{Hostname: "studio", Platform: "linux"},
		Projects:  []RawProject{{HostKey: "studio", ScopedKey: "repo:/a", Name: "a", RootPath: "/a", PlatformRepo: "o/r", PlatformHost: "github.com"}},
		Worktrees: []RawWorktree{{HostKey: "studio", ScopedKey: "worktree:/a", ProjectKey: "repo:/a", Path: "/a", IsPrimary: true, PRState: &st, ChecksStatus: &cs}},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.NotNil(got.Projects[0].PlatformURL)
	assert.Equal("https://github.com/o/r", *got.Projects[0].PlatformURL)
	require.NotNil(got.Projects[0].PlatformCoverage)
	assert.Equal("active", *got.Projects[0].PlatformCoverage, "coverage should be active")
	require.NotNil(got.Worktrees[0].PRState)
	assert.Equal("open", *got.Worktrees[0].PRState, "PRState must be lowercased")
	require.NotNil(got.Worktrees[0].ChecksStatus)
	assert.Equal("success", *got.Worktrees[0].ChecksStatus, "ChecksStatus must be lowercased")
	assert.Equal(got.Projects[0].ID, got.ProjectMap["o/r@github.com"], "project map should map owner/repo@host to id")
}

// TestBuildEnrichedProjectMapPreservesNestedOwner guards the nested-path fix:
// a GitLab-style group/subgroup/project must keep its full owner path in the
// project map key instead of truncating to the first two segments.
func TestBuildEnrichedProjectMapPreservesNestedOwner(t *testing.T) {
	raw := legacyRawSnapshot{
		Host: RawHost{Hostname: "studio", Platform: "linux"},
		Projects: []RawProject{{
			HostKey: "studio", ScopedKey: "repo:/a", Name: "widget", RootPath: "/a",
			PlatformRepo: "group/subgroup/widget", PlatformHost: "gitlab.com",
		}},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.NotNil(got.Projects[0].PlatformURL)
	assert.Equal("https://gitlab.com/group/subgroup/widget", *got.Projects[0].PlatformURL)
	assert.Equal(
		got.Projects[0].ID,
		got.ProjectMap["group/subgroup/widget@gitlab.com"],
		"nested owner path must round-trip into the project map key",
	)
}

// TestBuildEnrichedCarriesIsSynthesized proves the synthesized flag round-trips
// from the raw project into the enriched ProjectSummary so consumers can treat
// orphan-workspace projects as read-only.
func TestBuildEnrichedCarriesIsSynthesized(t *testing.T) {
	raw := legacyRawSnapshot{
		Host: RawHost{Hostname: "studio", Platform: "linux"},
		Projects: []RawProject{
			{HostKey: "studio", ScopedKey: "repo:/a", Name: "a", RootPath: "/a"},
			{
				HostKey: "studio", ScopedKey: "repo:github:github.com:o/orphan",
				Name: "o/orphan", PlatformRepo: "o/orphan", PlatformHost: "github.com",
				DefaultBranch: "trunk", IsSynthesized: true,
			},
		},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.Len(got.Projects, 2)
	assert.False(got.Projects[0].IsSynthesized, "registered project is not synthesized")
	assert.True(got.Projects[1].IsSynthesized, "synthesized flag must round-trip")
	assert.Equal("trunk", got.Projects[1].DefaultBranch)
	assert.Empty(got.Projects[1].RootPath, "synthesized project carries no root path")
}

func TestBuildEnrichedDefaultsEmptySessionBackendToLocalPTY(t *testing.T) {
	raw := legacyRawSnapshot{
		Host: RawHost{Hostname: "studio", Platform: "linux"},
		Worktrees: []RawWorktree{{
			HostKey: "studio", ScopedKey: "worktree:/a", ProjectKey: "repo:/a",
			Path: "/a", IsPrimary: true,
		}},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.Len(got.Worktrees, 1)
	assert.Equal(SessionBackendLocalPTY, got.Worktrees[0].SessionBackend,
		"a registered worktree with no backend defaults to localPTY")
}

func TestBuildEnrichedCarriesFullWorktreeFields(t *testing.T) {
	url := "https://github.com/o/r/pull/7"
	updated := "2026-05-30T10:00:00Z"
	polled := "2026-05-30T10:01:00Z"
	raw := legacyRawSnapshot{
		Host: RawHost{Hostname: "studio", Platform: "linux"},
		Worktrees: []RawWorktree{{
			HostKey: "studio", ScopedKey: "worktree:/a", ProjectKey: "repo:/a", Path: "/a", IsPrimary: true,
			IsHidden: true, PRURL: &url, PRUpdatedAt: &updated, LastPolledAt: &polled,
			SessionBackend: "LocalTmux", LinkedIssueNumbers: []int{12, 34},
			ChecksDetail: []CheckDetail{{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"}},
		}},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.Len(got.Worktrees, 1)
	w := got.Worktrees[0]
	assert.True(w.IsHidden)
	assert.Equal(SessionBackendLocalTmux, w.SessionBackend,
		"sessionBackend must normalize onto the canonical vocabulary")
	assert.Equal([]int{12, 34}, w.LinkedIssueNumbers)
	require.NotNil(w.PRURL)
	assert.Equal(url, *w.PRURL)
	require.NotNil(w.PRUpdatedAt)
	require.NotNil(w.LastPolledAt)
	require.Len(w.ChecksDetail, 1)
	assert.Equal("completed", w.ChecksDetail[0].Status, "check status must be lowercased")
	assert.Equal("success", w.ChecksDetail[0].Conclusion, "check conclusion must be lowercased")
}

// TestBuildEnrichedCarriesPREnrichment proves the linked PR's review decision,
// mergeability, platform-reported diff size, and comment count thread through
// from the raw worktree onto the enriched, client-facing WorktreeSummary —
// without this copy the enrichment is stranded at the raw layer and the
// /snapshot consumer reads nothing.
func TestBuildEnrichedCarriesPREnrichment(t *testing.T) {
	reviewDecision := "CHANGES_REQUESTED"
	mergeable := "DIRTY"
	additions := 40
	deletions := 9
	comments := 4
	raw := legacyRawSnapshot{
		Host: RawHost{Hostname: "studio", Platform: "linux"},
		Worktrees: []RawWorktree{{
			HostKey: "studio", ScopedKey: "worktree:/a", ProjectKey: "repo:/a", Path: "/a",
			PRReviewDecision: &reviewDecision,
			PRMergeable:      &mergeable,
			PRAdditions:      &additions,
			PRDeletions:      &deletions,
			PRCommentCount:   &comments,
		}},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.Len(got.Worktrees, 1)
	w := got.Worktrees[0]
	require.NotNil(w.PRReviewDecision)
	assert.Equal("changes_requested", *w.PRReviewDecision, "review decision must be lowercased")
	require.NotNil(w.PRMergeable)
	assert.Equal("dirty", *w.PRMergeable, "mergeable state must be lowercased")
	require.NotNil(w.PRAdditions)
	assert.Equal(40, *w.PRAdditions)
	require.NotNil(w.PRDeletions)
	assert.Equal(9, *w.PRDeletions)
	require.NotNil(w.PRCommentCount)
	assert.Equal(4, *w.PRCommentCount)
}

// TestBuildEnrichedOmitsAbsentPREnrichment proves an unenriched worktree (no
// linked PR detail) carries no enrichment pointers, so the omitempty JSON tags
// drop the fields rather than emitting misleading zero/empty values.
func TestBuildEnrichedOmitsAbsentPREnrichment(t *testing.T) {
	raw := legacyRawSnapshot{
		Host: RawHost{Hostname: "studio", Platform: "linux"},
		Worktrees: []RawWorktree{{
			HostKey: "studio", ScopedKey: "worktree:/a", ProjectKey: "repo:/a", Path: "/a",
		}},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.Len(got.Worktrees, 1)
	w := got.Worktrees[0]
	assert.Nil(w.PRReviewDecision)
	assert.Nil(w.PRMergeable)
	assert.Nil(w.PRAdditions)
	assert.Nil(w.PRDeletions)
	assert.Nil(w.PRCommentCount)
}

func TestBuildEnrichedSessionRuntimeKindAndFields(t *testing.T) {
	cpu := 12.5
	rss := 256
	procs := 3
	active := "2026-05-30T10:00:00Z"
	raw := legacyRawSnapshot{
		Host: RawHost{Hostname: "studio", Platform: "linux"},
		Sessions: []RawSession{{
			HostKey: "studio", ScopedKey: "session:s1", Status: "running",
			RuntimeKind: "agent", SessionKind: "preset", Role: "driver",
			ExecutableName: "claude", AgentKind: "claude",
			CPUPercent: &cpu, ResidentMB: &rss, ProcessCount: &procs, LastActiveAt: &active,
		}},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.Len(got.Sessions, 1)
	s := got.Sessions[0]
	assert.Equal("agent", s.RuntimeKind, "runtimeKind carries the session intent")
	assert.Equal("preset", s.SessionKind)
	assert.Equal("driver", s.Role)
	assert.Equal("claude", s.ExecutableName)
	assert.Equal("claude", s.AgentKind)
	require.NotNil(s.CPUPercent)
	assert.InDelta(12.5, *s.CPUPercent, 0.0001)
	require.NotNil(s.ResidentMB)
	require.NotNil(s.ProcessCount)
	require.NotNil(s.LastActiveAt)
}

func TestBuildEnrichedCarriesHostFields(t *testing.T) {
	raw := legacyRawSnapshot{
		Host: RawHost{Hostname: "studio", Platform: "linux", LastSeenAt: "2026-05-30T10:00:00Z",
			TmuxSessions: []TmuxSessionInfo{
				{Name: "w-abc", Managed: true, WorktreeKey: "worktree:/a", Windows: []TmuxWindowInfo{{ID: "@1", Index: 0, Name: "edit"}}},
			}},
		RemoteHosts: []RawRemoteHost{
			{HostKey: "mbp", Name: "mbp", Reachable: true, PreferredTransport: "http"},
			{HostKey: "void", Name: "void", Reachable: true}, // no transport -> default http
		},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())

	local := got.Hosts[0]
	require.Len(local.TmuxSessions, 1)
	assert.Equal("edit", local.TmuxSessions[0].Windows[0].Name)
	assert.Equal("local", local.PreferredTransport)
	require.NotNil(local.LastSeenAt, "local host last-seen must be set")

	var remote, void *HostSummary
	for i := range got.Hosts {
		switch got.Hosts[i].ConfigKey {
		case "mbp":
			remote = &got.Hosts[i]
		case "void":
			void = &got.Hosts[i]
		}
	}
	require.NotNil(remote)
	assert.Equal("http", remote.PreferredTransport)
	assert.NotNil(remote.TmuxSessions, "remote tmux inventory must be [] not null")
	assert.Empty(remote.TmuxSessions)

	require.NotNil(void)
	assert.Equal("http", void.PreferredTransport, "empty transport defaults to http (hub peer)")
}

func TestBuildEnrichedCarriesTmuxFreshnessFields(t *testing.T) {
	raw := legacyRawSnapshot{
		Host: RawHost{
			Hostname:         "studio",
			Platform:         "linux",
			TmuxLastPolledAt: "2026-05-31T10:00:00Z",
			TmuxProbeError:   "inventory failed",
			TmuxMetricsError: "ps failed",
			TmuxSessions: []TmuxSessionInfo{{
				Name:             "managed",
				Managed:          true,
				SessionScopedKey: "session:ws-1:main",
				WindowCount:      1,
			}},
		},
		RemoteHosts: []RawRemoteHost{{
			HostKey:          "peer",
			Name:             "peer",
			Reachable:        true,
			TmuxLastPolledAt: "2026-05-31T10:01:00Z",
			TmuxProbeError:   "peer inventory failed",
			TmuxMetricsError: "peer ps failed",
		}},
	}
	require := require.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.Len(got.Hosts, 2)

	asrt := assert.New(t)
	local := got.Hosts[0]
	require.NotNil(local.TmuxLastPolledAt)
	asrt.Equal("2026-05-31T10:00:00.000Z", *local.TmuxLastPolledAt)
	asrt.Equal("inventory failed", local.TmuxProbeError)
	asrt.Equal("ps failed", local.TmuxMetricsError)
	require.Len(local.TmuxSessions, 1)
	asrt.Equal("session:ws-1:main", local.TmuxSessions[0].SessionScopedKey)
	asrt.Equal(1, local.TmuxSessions[0].WindowCount)

	remote := got.Hosts[1]
	require.NotNil(remote.TmuxLastPolledAt)
	asrt.Equal("2026-05-31T10:01:00.000Z", *remote.TmuxLastPolledAt)
	asrt.Equal("peer inventory failed", remote.TmuxProbeError)
	asrt.Equal("peer ps failed", remote.TmuxMetricsError)
}

func TestBuildEnrichedReportsTmuxProbeDiagnosticsWithoutBlocks(t *testing.T) {
	raw := legacyRawSnapshot{
		Host: RawHost{Hostname: "studio", Platform: "linux"},
		RemoteHosts: []RawRemoteHost{{
			HostKey:          "peer",
			Name:             "peer",
			Reachable:        true,
			TmuxProbeError:   "inventory failed",
			TmuxMetricsError: "ps failed",
		}},
	}
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.Len(t, got.Hosts, 2)
	remote := got.Hosts[1]
	require.Len(t, remote.Diagnostics, 2)
	asrt := assert.New(t)
	asrt.Equal("tmuxProbeFailed", remote.Diagnostics[0].Code)
	asrt.Empty(remote.Diagnostics[0].BlocksOperations)
	asrt.Equal("tmuxMetricsUnavailable", remote.Diagnostics[1].Code)
	asrt.Empty(remote.Diagnostics[1].BlocksOperations)
}

func TestRemoteHostVersionSurfaced(t *testing.T) {
	v := "1.4.2"
	raw := legacyRawSnapshot{
		Host: RawHost{Hostname: "local", Platform: "linux"},
		RemoteHosts: []RawRemoteHost{{
			HostKey:   "studio",
			Name:      "studio",
			Reachable: true,
			Version:   v,
		}},
	}
	got := projectLegacyFixture(raw, "", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.Len(t, got.Hosts, 2)
	remote := got.Hosts[1]
	require.NotNil(t, remote.Version)
	assert.Equal(t, v, *remote.Version)
}

// TestBuildEnrichedNormalizesWorktreeTimestamps proves PRUpdatedAt and
// LastPolledAt flow through the shared date normalizer like every other
// timestamp: the canonical form carries millisecond precision (.000), so a
// millis-less input is rewritten rather than passed through verbatim.
func TestBuildEnrichedNormalizesWorktreeTimestamps(t *testing.T) {
	updated := "2026-05-30T10:00:00Z"
	polled := "2026-05-30T08:30:00Z"
	raw := legacyRawSnapshot{
		Host: RawHost{Hostname: "studio", Platform: "linux"},
		Worktrees: []RawWorktree{{
			HostKey: "studio", ScopedKey: "worktree:/a", ProjectKey: "repo:/a", Path: "/a", IsPrimary: true,
			PRUpdatedAt: &updated, LastPolledAt: &polled,
		}},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())
	require.Len(got.Worktrees, 1)
	w := got.Worktrees[0]
	require.NotNil(w.PRUpdatedAt)
	assert.Equal("2026-05-30T10:00:00.000Z", *w.PRUpdatedAt, "PRUpdatedAt must be canonicalized via the shared normalizer")
	require.NotNil(w.LastPolledAt)
	assert.Equal("2026-05-30T08:30:00.000Z", *w.LastPolledAt, "LastPolledAt must be canonicalized via the shared normalizer")
}

func TestBuildEnrichedCarriesRegistryID(t *testing.T) {
	raw := legacyRawSnapshot{
		Host: RawHost{Hostname: "studio", Platform: "linux"},
		Projects: []RawProject{
			{HostKey: "studio", ScopedKey: "repo:/a", Name: "a", RootPath: "/a", RegistryID: "prj_abc"},
			{
				HostKey: "studio", ScopedKey: "repo:github:github.com:o/orphan",
				Name: "o/orphan", PlatformRepo: "o/orphan", PlatformHost: "github.com",
				IsSynthesized: true,
			},
		},
		Worktrees: []RawWorktree{
			{HostKey: "studio", ScopedKey: "worktree:/a", ProjectKey: "repo:/a", Path: "/a", IsPrimary: true},
			{HostKey: "studio", ScopedKey: "worktree:/a-feat", ProjectKey: "repo:/a", Path: "/a-feat", Branch: "feat", RegistryID: "wtr_xyz"},
		},
	}
	require := require.New(t)
	assert := assert.New(t)
	got := projectLegacyFixture(raw, "studio", nil, RealCapabilityPolicy{}, DefaultIdentity())

	require.Len(got.Projects, 2)
	assert.Equal("prj_abc", got.Projects[0].RegistryID, "registered project carries its registry id")
	assert.Empty(got.Projects[1].RegistryID, "synthesized project has no registry id")

	require.Len(got.Worktrees, 2)
	assert.Empty(got.Worktrees[0].RegistryID, "synthesized primary worktree has no registry id")
	assert.Equal("wtr_xyz", got.Worktrees[1].RegistryID, "registered worktree carries its registry id")
}

// hostKeyedTestPolicy suppresses one op for one specific host key,
// proving enrichment consults ForHost per remote host.
type hostKeyedTestPolicy struct {
	targetHostKey string
}

func (hostKeyedTestPolicy) Apply(map[string]HostOperationAvailability, bool) {}

func (p hostKeyedTestPolicy) ForHost(hostKey string) AvailabilityPolicy {
	if hostKey != p.targetHostKey {
		return RealCapabilityPolicy{}
	}
	return HubReadOnlyPolicy{
		Ops:    []string{OpRepositoryClone},
		Reason: "unroutable host",
	}
}

func TestBuildEnrichedAppliesHostKeyedPolicyPerHost(t *testing.T) {
	caps := &Capabilities{Commands: CommandCapabilities{
		WorktreeCreate: true, WorktreeDelete: true,
		SessionEnsure: true, SessionKill: true,
		RepositoryClone: true, ProjectAdd: true, ProjectRemove: true,
	}}
	raw := legacyRawSnapshot{
		SchemaVersion: SchemaVersion,
		Host:          RawHost{Hostname: "studio", Platform: "linux"},
		Capabilities:  caps,
		RemoteHosts: []RawRemoteHost{
			{HostKey: "peer", Name: "peer", Reachable: true, Capabilities: caps},
			{HostKey: "unroutable", Name: "unroutable", Reachable: true, Capabilities: caps},
		},
	}
	require := require.New(t)
	assert := assert.New(t)

	got := projectLegacyFixture(
		raw, "studio", nil,
		hostKeyedTestPolicy{targetHostKey: "unroutable"},
		DefaultIdentity(),
	)
	require.Len(got.Hosts, 3)

	byKey := map[string]HostSummary{}
	for _, h := range got.Hosts {
		byKey[h.ConfigKey] = h
	}
	assert.True(byKey["studio"].OperationAvailability[OpRepositoryClone].Available,
		"local host availability must not be suppressed")
	assert.True(byKey["peer"].OperationAvailability[OpRepositoryClone].Available,
		"routable peer must keep clone available")
	suppressed := byKey["unroutable"].OperationAvailability[OpRepositoryClone]
	assert.False(suppressed.Available, "unroutable host must suppress clone")
	require.NotNil(suppressed.UnavailableReason)
	assert.Equal("unroutable host", *suppressed.UnavailableReason)
}

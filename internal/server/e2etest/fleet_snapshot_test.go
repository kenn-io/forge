package e2etest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
	dbpkg "go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/fleet"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func bootFleetServer(t *testing.T, cfg *config.Config) (*httptest.Server, *dbpkg.DB) {
	t.Helper()
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	if cfg == nil {
		cfg = &config.Config{BasePath: "/"}
	}
	if cfg.Tmux.Command == nil {
		cfg.Tmux.Command = []string{"middleman-no-such-tmux"}
	}
	srv := server.New(database, syncer, nil, "/", cfg, server.ServerOptions{
		WorktreeDir:                        t.TempDir(),
		DisableWorkspaceBackgroundMonitors: true,
		HostCheck: server.HostCheckOptions{
			Bind:                 config.HostKey{Host: "127.0.0.1", Port: "8091"},
			AllowLoopbackAnyPort: true,
		},
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, database
}

const (
	fleetSnapshotEventuallyTimeout = 6 * time.Second
	fleetSnapshotEventuallyTick    = 25 * time.Millisecond
)

func getJSON(t *testing.T, ts *httptest.Server, path string, out any) {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET %s", path)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
}

func getRaw(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := client.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

func deleteJSON(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, http.NoBody)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

func patchJSON(
	t *testing.T,
	client *http.Client,
	url string,
	body any,
) (int, string) {
	t.Helper()
	require := require.New(t)

	var payload io.Reader = http.NoBody
	if body != nil {
		buf, err := json.Marshal(body)
		require.NoError(err)
		payload = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(http.MethodPatch, url, payload)
	require.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(err)
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(err)
	return resp.StatusCode, string(respBody)
}

func TestFleetSnapshotLocalE2E(t *testing.T) {
	require := require.New(t)
	ts, database := bootFleetServer(t, nil)
	ctx := context.Background()

	_, err := database.CreateProject(ctx, dbpkg.CreateProjectInput{
		DisplayName: "widget", LocalPath: t.TempDir() + "/widget", DefaultBranch: "main",
	})
	require.NoError(err)
	require.NoError(database.InsertWorkspace(ctx, &dbpkg.Workspace{
		ID: "ws-1", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: dbpkg.WorkspaceItemTypePullRequest, ItemNumber: 7,
		GitHeadRef: "feature", WorktreePath: t.TempDir() + "/ws", TmuxSession: "middleman-ws-1",
		Status: "ready",
	}))

	// raw: scoped keys, schemaVersion, and NO enriched "hosts"/UUIDs.
	var rawMap map[string]any
	getJSON(t, ts, "/api/v1/snapshot/raw", &rawMap)
	require.EqualValues(fleet.SchemaVersion, rawMap["schemaVersion"])
	_, hasHosts := rawMap["hosts"]
	require.False(hasHosts, "raw snapshot must not contain enriched hosts/UUIDs")
	var raw fleet.RawSnapshot
	getJSON(t, ts, "/api/v1/snapshot/raw", &raw)
	require.NotEmpty(raw.Projects)
	require.True(anyProjectScopedPrefix(raw.Projects, "repo:"), "raw projects use repo: scoped keys")
	require.True(anyWorktreeScopedPrefix(raw.Worktrees, "worktree:"), "raw worktrees use worktree: scoped keys")

	// enriched: UUID host id, kind self, seeded worktree present with a UUID id.
	var snap fleet.Snapshot
	getJSON(t, ts, "/api/v1/snapshot", &snap)
	require.NotEmpty(snap.Hosts)
	require.Equal("self", snap.Hosts[0].Kind)
	require.Len(snap.Hosts[0].ID, 36, "host id must be a 36-char UUID")
	require.NotEmpty(snap.Worktrees)
	for _, w := range snap.Worktrees {
		require.Len(w.ID, 36, "worktree id must be a 36-char UUID")
	}

	// The seeded PR workspace (ItemNumber 7) must surface its PR link
	// end-to-end through the enriched HTTP snapshot, not just the adapter
	// unit test: LinkedPRNumber is derived from ItemNumber for PR workspaces.
	var prWorktrees []fleet.WorktreeSummary
	for _, w := range snap.Worktrees {
		if w.LinkedPRNumber != nil {
			prWorktrees = append(prWorktrees, w)
		}
	}
	require.Len(prWorktrees, 1, "exactly one seeded PR workspace worktree")
	require.Equal(7, *prWorktrees[0].LinkedPRNumber, "PR workspace must surface linkedPRNumber=7")
}

func TestFleetSnapshotIssueWorkspaceLinksIssueOnlyE2E(t *testing.T) {
	require := require.New(t)
	ts, database := bootFleetServer(t, nil)
	ctx := context.Background()

	repoID, err := database.UpsertRepo(
		ctx,
		dbpkg.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = database.UpsertIssue(ctx, &dbpkg.Issue{
		RepoID: repoID, PlatformID: 4200, Number: 42,
		URL: "https://github.com/acme/widget/issues/42", Title: "Investigate flaky test",
		Author: "dev", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &dbpkg.MergeRequest{
		RepoID: repoID, PlatformID: 9900, Number: 99,
		URL: "https://github.com/acme/widget/pull/99", Title: "Fix flaky test",
		Author: "dev", State: "open", HeadBranch: "fix-flake", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	associatedPR := 99
	require.NoError(database.InsertWorkspace(ctx, &dbpkg.Workspace{
		ID: "ws-issue", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: dbpkg.WorkspaceItemTypeIssue, ItemNumber: 42,
		AssociatedPRNumber: &associatedPR,
		GitHeadRef:         "middleman/issue-42", WorktreePath: t.TempDir() + "/ws-issue",
		TmuxSession: "middleman-ws-issue", Status: "ready",
	}))

	var snap fleet.Snapshot
	getJSON(t, ts, "/api/v1/snapshot", &snap)

	issueWT := worktreeByLinkedIssue(snap.Worktrees, 42)
	require.NotNil(issueWT, "issue workspace worktree present")
	require.Equal([]int{42}, issueWT.LinkedIssueNumbers)
	require.NotNil(issueWT.LinkedPRNumber, "associated PR number is still linked")
	require.Equal(99, *issueWT.LinkedPRNumber)
	require.Nil(issueWT.PRTitle, "issue title must not surface as prTitle")
	require.Nil(issueWT.PRState, "issue state must not surface as prState")
}

func TestFleetSnapshotFanOutE2E(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	// PEER: real server with its own seeded project.
	peerTS, peerDB := bootFleetServer(t, nil)
	_, err := peerDB.CreateProject(ctx, dbpkg.CreateProjectInput{
		DisplayName: "peerapp", LocalPath: t.TempDir() + "/peerapp", DefaultBranch: "main",
	})
	require.NoError(err)

	// HUB: real server pointing at the peer + one unreachable peer.
	hubCfg := &config.Config{
		BasePath: "/",
		Fleet: config.Fleet{
			Enabled:     true,
			Key:         "hub",
			PeerTimeout: "1s",
			Peers: []config.FleetPeer{
				{Key: "peer", Name: "peer", BaseURL: peerTS.URL},
				{Key: "down", Name: "down", BaseURL: "http://127.0.0.1:1"}, // refused fast
			},
		},
	}
	hubTS, hubDB := bootFleetServer(t, hubCfg)
	_, err = hubDB.CreateProject(ctx, dbpkg.CreateProjectInput{
		DisplayName: "hubapp", LocalPath: t.TempDir() + "/hubapp", DefaultBranch: "main",
	})
	require.NoError(err)

	var snap fleet.Snapshot
	getJSON(t, hubTS, "/api/v1/snapshot?include_peers=true", &snap)

	self := findHost(snap.Hosts, "hub")
	peer := findHost(snap.Hosts, "peer")
	down := findHost(snap.Hosts, "down")
	require.NotNil(self, "self host present")
	require.NotNil(peer, "peer host present")
	require.NotNil(down, "unreachable host present")
	require.Equal("self", self.Kind)
	require.True(self.Reachable)
	require.True(peer.Reachable, "reachable peer")
	require.False(down.Reachable, "unreachable peer")
	require.NotNil(down.Error, "unreachable peer must report an error")

	// Hub and peer worktrees coexist with distinct per-host UUIDs.
	var sawHub, sawPeer bool
	for _, w := range snap.Worktrees {
		require.Len(w.ID, 36)
		if w.HostID == fleet.HostID("hub") {
			sawHub = true
		}
		if w.HostID == fleet.HostID("peer") {
			sawPeer = true
		}
	}
	require.True(sawHub, "hub worktree present")
	require.True(sawPeer, "peer worktree merged with distinct host id")
}

func TestFleetDisabledBlocksRemoteSnapshotAndProxyE2E(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := context.Background()

	var peerCalls int
	var peerCallsMu sync.Mutex
	peerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerCallsMu.Lock()
		peerCalls++
		peerCallsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"schemaVersion":2,"host":{"configKey":"peer","hostname":"peer","platform":"linux"}}`,
		))
	}))
	t.Cleanup(peerTS.Close)

	hubCfg := &config.Config{
		BasePath: "/",
		DataDir:  t.TempDir(),
		Fleet: config.Fleet{
			Enabled:     false,
			Key:         "hub",
			PeerTimeout: "10ms",
			Peers: []config.FleetPeer{
				{Key: "peer", Name: "peer", BaseURL: peerTS.URL},
			},
			SSHPeers: []config.FleetSSHPeer{
				{Key: "ssh-peer", Name: "ssh peer", Destination: "dev@ssh.local"},
			},
		},
	}
	hubTS, hubDB := bootFleetServer(t, hubCfg)
	_, err := hubDB.CreateProject(ctx, dbpkg.CreateProjectInput{
		DisplayName: "hubapp", LocalPath: t.TempDir() + "/hubapp", DefaultBranch: "main",
	})
	require.NoError(err)

	var snap fleet.Snapshot
	getJSON(t, hubTS, "/api/v1/snapshot?include_peers=true", &snap)
	require.NotNil(findHost(snap.Hosts, "hub"), "self host remains visible")
	assert.Nil(findHost(snap.Hosts, "peer"), "disabled federation hides configured peer")
	assert.Nil(findHost(snap.Hosts, "ssh-peer"), "disabled federation hides configured ssh peer")
	peerCallsMu.Lock()
	assert.Equal(0, peerCalls, "disabled federation must not fan out to peers")
	peerCallsMu.Unlock()

	status, body := getRaw(
		t, hubTS.Client(),
		hubTS.URL+"/api/v1/fleet/hosts/peer/workspaces",
	)
	assert.Equal(http.StatusNotFound, status)
	assert.Contains(body, `"code":"notFound"`)
	assert.Contains(body, `"hostKey":"peer"`)
	peerCallsMu.Lock()
	assert.Equal(0, peerCalls, "disabled remote proxy must not call peers")
	peerCallsMu.Unlock()

	status, body = getRaw(
		t, hubTS.Client(),
		hubTS.URL+"/api/v1/fleet/hosts/ssh-peer/workspaces",
	)
	assert.Equal(http.StatusNotFound, status)
	assert.Contains(body, `"code":"notFound"`)
	assert.Contains(body, `"hostKey":"ssh-peer"`)

	status, body = getRaw(
		t, hubTS.Client(),
		hubTS.URL+"/api/v1/fleet/hosts/hub/workspaces",
	)
	assert.Equal(http.StatusOK, status)
	assert.NotContains(body, "fleet host not found")
}

func TestFleetSnapshotLiveTmuxEnrichmentE2E(t *testing.T) {
	require := require.New(t)
	fakeTmux := writeFleetSnapshotFakeTmux(t)
	cfg := &config.Config{
		BasePath: "/",
		Tmux: config.Tmux{
			Command: []string{fakeTmux},
		},
	}
	ts, database := bootFleetServer(t, cfg)
	ctx := context.Background()
	createdAt := time.Date(2026, 5, 31, 9, 0, 0, 0, time.UTC)
	worktreePath := filepath.Join(t.TempDir(), "ws")

	require.NoError(database.InsertWorkspace(ctx, &dbpkg.Workspace{
		ID: "ws-live", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: dbpkg.WorkspaceItemTypePullRequest, ItemNumber: 7,
		GitHeadRef: "feature", WorktreePath: worktreePath,
		TmuxSession: "middleman-main", Status: "ready",
		CreatedAt: createdAt,
	}))
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &dbpkg.WorkspaceRuntimeSession{
		WorkspaceID: "ws-live",
		SessionKey:  "ws-live_codex",
		TargetKey:   "codex",
		Label:       "codex",
		Kind:        "agent",
		Scope:       "session",
		TmuxSession: "middleman-agent",
		CreatedAt:   createdAt,
	}))

	var raw fleet.RawSnapshot
	require.Eventually(func() bool {
		getJSON(t, ts, "/api/v1/snapshot/raw", &raw)
		return raw.Host.TmuxLastPolledAt != ""
	}, fleetSnapshotEventuallyTimeout, fleetSnapshotEventuallyTick)

	require.Len(raw.Host.TmuxSessions, 3)
	main := rawTmuxByName(raw.Host.TmuxSessions, "middleman-main")
	require.NotNil(main)
	require.True(main.Managed)
	require.Equal("session:ws-live:main", main.SessionScopedKey)
	require.Equal(1, main.WindowCount)
	require.Len(main.Windows, 1)

	runtimeScopedKey := "session:ws-live_codex"
	agent := rawTmuxByName(raw.Host.TmuxSessions, "middleman-agent")
	require.NotNil(agent)
	require.True(agent.Managed)
	require.Equal(runtimeScopedKey, agent.SessionScopedKey)
	require.NotNil(rawSessionByScopedKey(raw.Sessions, runtimeScopedKey))

	personal := rawTmuxByName(raw.Host.TmuxSessions, "personal")
	require.NotNil(personal)
	require.False(personal.Managed)
	require.Equal(2, personal.WindowCount)
	require.Empty(personal.Windows)

	var snap fleet.Snapshot
	getJSON(t, ts, "/api/v1/snapshot", &snap)
	require.NotEmpty(snap.Hosts)
	host := &snap.Hosts[0]
	require.Equal("self", host.Kind)
	require.NotNil(host.TmuxLastPolledAt)
	require.Len(host.TmuxSessions, 3)
	require.NotNil(summarySessionByScopedKey(snap.Sessions, runtimeScopedKey))
}

func TestFleetSnapshotProjectWorktreeRuntimeE2E(t *testing.T) {
	require := require.New(t)
	fakeTmux := writeFleetSnapshotProjectRuntimeTmux(t)
	cfg := &config.Config{
		BasePath: "/",
		Tmux: config.Tmux{
			Command: []string{fakeTmux},
		},
	}
	ts, database := bootFleetServer(t, cfg)
	ctx := context.Background()
	projectPath := filepath.Join(t.TempDir(), "app")
	worktreePath := filepath.Join(t.TempDir(), "app-runtime")
	createdAt := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)

	project, err := database.CreateProject(ctx, dbpkg.CreateProjectInput{
		DisplayName: "app", LocalPath: projectPath, DefaultBranch: "main",
	})
	require.NoError(err)
	worktree, err := database.CreateProjectWorktree(ctx, dbpkg.CreateProjectWorktreeInput{
		ProjectID: project.ID,
		Branch:    "runtime",
		Path:      worktreePath,
	})
	require.NoError(err)
	require.NoError(database.UpsertProjectWorktreeTmuxSession(
		ctx,
		&dbpkg.ProjectWorktreeTmuxSession{
			WorktreeID:  worktree.ID,
			SessionKey:  "wt_codex",
			SessionName: "middleman-project-worktree-agent",
			TargetKey:   "codex",
			CreatedAt:   createdAt,
		},
	))

	var raw fleet.RawSnapshot
	require.Eventually(func() bool {
		getJSON(t, ts, "/api/v1/snapshot/raw", &raw)
		return raw.Host.TmuxLastPolledAt != "" &&
			rawTmuxByName(raw.Host.TmuxSessions, "middleman-project-worktree-agent") != nil
	}, fleetSnapshotEventuallyTimeout, fleetSnapshotEventuallyTick)

	worktreeAbs, err := filepath.Abs(worktreePath)
	require.NoError(err)
	wtKey := "worktree:" + filepath.Clean(worktreeAbs)
	sessionScopedKey := "session:wt_codex"
	session := rawSessionByScopedKey(raw.Sessions, sessionScopedKey)
	require.NotNil(session)
	require.Equal(wtKey, session.WorktreeKey)
	require.Equal("agent", session.RuntimeKind)

	tmuxInfo := rawTmuxByName(raw.Host.TmuxSessions, "middleman-project-worktree-agent")
	require.NotNil(tmuxInfo)
	require.True(tmuxInfo.Managed)
	require.Equal(wtKey, tmuxInfo.WorktreeKey)
	require.Equal(sessionScopedKey, tmuxInfo.SessionScopedKey)

	var snap fleet.Snapshot
	getJSON(t, ts, "/api/v1/snapshot", &snap)
	require.NotNil(summarySessionByScopedKey(snap.Sessions, sessionScopedKey))
}

func TestFleetSnapshotEmptyTmuxServerE2E(t *testing.T) {
	require := require.New(t)
	fakeTmux := writeFleetSnapshotNoServerTmux(t)
	cfg := &config.Config{
		BasePath: "/",
		Tmux: config.Tmux{
			Command: []string{fakeTmux},
		},
	}
	ts, database := bootFleetServer(t, cfg)
	ctx := context.Background()
	require.NoError(database.InsertWorkspace(ctx, &dbpkg.Workspace{
		ID: "ws-empty", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: dbpkg.WorkspaceItemTypePullRequest, ItemNumber: 8,
		GitHeadRef: "feature", WorktreePath: filepath.Join(t.TempDir(), "ws"),
		TmuxSession: "missing-main", Status: "ready",
		CreatedAt: time.Date(2026, 5, 31, 9, 0, 0, 0, time.UTC),
	}))

	var raw fleet.RawSnapshot
	require.Eventually(func() bool {
		getJSON(t, ts, "/api/v1/snapshot/raw", &raw)
		return raw.Host.TmuxLastPolledAt != "" && raw.Host.TmuxProbeError == ""
	}, fleetSnapshotEventuallyTimeout, fleetSnapshotEventuallyTick)

	require.Empty(raw.Host.TmuxProbeError)
	require.Empty(raw.Host.TmuxSessions)
	main := rawSessionByScopedKey(raw.Sessions, "session:ws-empty:main")
	require.NotNil(main)
	require.Equal("running", main.Status)

	var snap fleet.Snapshot
	getJSON(t, ts, "/api/v1/snapshot", &snap)
	require.NotEmpty(snap.Hosts)
	host := snap.Hosts[0]
	require.NotNil(host.TmuxLastPolledAt)
	require.Empty(host.TmuxProbeError)
	require.Nil(diagnosticByCode(host.Diagnostics, "tmuxProbeFailed"))
}

func TestFleetSnapshotTmuxProbeFailureE2E(t *testing.T) {
	require := require.New(t)
	fakeTmux := writeFleetSnapshotFailingTmux(t)
	cfg := &config.Config{
		BasePath: "/",
		Tmux: config.Tmux{
			Command: []string{fakeTmux},
		},
	}
	ts, database := bootFleetServer(t, cfg)
	ctx := context.Background()
	require.NoError(database.InsertWorkspace(ctx, &dbpkg.Workspace{
		ID: "ws-failed", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: dbpkg.WorkspaceItemTypePullRequest, ItemNumber: 9,
		GitHeadRef: "feature", WorktreePath: filepath.Join(t.TempDir(), "ws"),
		TmuxSession: "unprobed-main", Status: "ready",
		CreatedAt: time.Date(2026, 5, 31, 9, 0, 0, 0, time.UTC),
	}))

	var raw fleet.RawSnapshot
	require.Eventually(func() bool {
		getJSON(t, ts, "/api/v1/snapshot/raw", &raw)
		return raw.Host.TmuxProbeError != ""
	}, fleetSnapshotEventuallyTimeout, fleetSnapshotEventuallyTick)

	require.Empty(raw.Host.TmuxLastPolledAt)
	require.Empty(raw.Host.TmuxSessions)
	main := rawSessionByScopedKey(raw.Sessions, "session:ws-failed:main")
	require.NotNil(main)
	require.Equal("running", main.Status)

	var snap fleet.Snapshot
	getJSON(t, ts, "/api/v1/snapshot", &snap)
	require.NotEmpty(snap.Hosts)
	host := snap.Hosts[0]
	require.Empty(host.TmuxLastPolledAt)
	require.NotEmpty(host.TmuxProbeError)
	diag := diagnosticByCode(host.Diagnostics, "tmuxProbeFailed")
	require.NotNil(diag)
	require.Empty(diag.BlocksOperations)
}

func writeFleetSnapshotFakeTmux(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmux")
	script := `#!/bin/sh
cmd="$1"
shift
case "$cmd" in
  list-sessions)
    case "$*" in
      *session_created*)
        printf '1717150000\t1\tmiddleman-main\n'
        printf '1717150000\t1\tmiddleman-agent\n'
        printf '1717150000\t2\tpersonal\n'
        ;;
      *)
        printf 'middleman-main\n'
        printf 'middleman-agent\n'
        printf 'personal\n'
        ;;
    esac
    ;;
  list-windows)
    printf 'middleman-main\t@1\t0\tmain\t1717150100\n'
    printf 'middleman-agent\t@2\t0\tagent\t1717150200\n'
    printf 'personal\t@3\t0\tprivate\t1717150300\n'
    printf 'personal\t@4\t1\tlogs\t1717150400\n'
    ;;
  list-panes)
    printf 'middleman-agent\t1717150200\t100\tcodex\n'
    ;;
  has-session|show-options|kill-session)
    exit 0
    ;;
  -V)
    printf 'tmux 3.4\n'
    ;;
esac
exit 0
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func writeFleetSnapshotProjectRuntimeTmux(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmux")
	script := `#!/bin/sh
cmd="$1"
shift
case "$cmd" in
  list-sessions)
    case "$*" in
      *session_created*)
        printf '1717243200\t1\tmiddleman-project-worktree-agent\n'
        ;;
      *)
        printf 'middleman-project-worktree-agent\n'
        ;;
    esac
    ;;
  list-windows)
    printf 'middleman-project-worktree-agent\t@1\t0\tagent\t1717243260\n'
    ;;
  list-panes)
    printf 'middleman-project-worktree-agent\t1717243260\t100\tcodex\n'
    ;;
  has-session|show-options|kill-session)
    exit 0
    ;;
  -V)
    printf 'tmux 3.4\n'
    ;;
esac
exit 0
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func writeFleetSnapshotNoServerTmux(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmux")
	script := `#!/bin/sh
cmd="$1"
case "$cmd" in
  list-sessions|list-windows)
    echo "no server running on /tmp/tmux-1000/default" >&2
    exit 1
    ;;
  list-panes)
    exit 0
    ;;
  has-session|show-options|kill-session)
    exit 0
    ;;
  -V|--version)
    printf 'tmux 3.4\n'
    ;;
esac
exit 0
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func writeFleetSnapshotFailingTmux(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmux")
	script := `#!/bin/sh
cmd="$1"
case "$cmd" in
  list-sessions|list-windows|list-panes)
    echo "tmux permission denied" >&2
    exit 2
    ;;
  has-session|show-options|kill-session)
    exit 0
    ;;
  -V|--version)
    printf 'tmux 3.4\n'
    ;;
esac
exit 0
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func TestFleetOperationProxyRoutesMutationsToPeerE2E(t *testing.T) {
	assert := Assert.New(t)
	req := require.New(t)

	type observedRequest struct {
		Method string
		Path   string
		Query  string
		Body   string
	}
	var observedMu sync.Mutex
	var observed []observedRequest
	var handlerErrors []string
	recordHandlerError := func(msg string) {
		observedMu.Lock()
		defer observedMu.Unlock()
		handlerErrors = append(handlerErrors, msg)
	}

	peerMux := http.NewServeMux()
	peerMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			recordHandlerError("read body: " + err.Error())
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		observedMu.Lock()
		observed = append(observed, observedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Body:   string(body),
		})
		observedMu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"workspaces":[{"id":"peer-ws","status":"ready"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"peer-ws","status":"queued"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/peer-ws":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"peer-ws","status":"ready"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces/peer-ws/retry":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"peer-ws","status":"creating"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces/peer-ws/refresh":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"peer-ws","status":"ready","refreshed":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/peer-ws/runtime":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"launch_targets":[],"sessions":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces/peer-ws/runtime/sessions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"key":"peer-ws:helper","workspace_id":"peer-ws","target_key":"helper"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/workspaces/peer-ws/runtime/sessions/sess-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"key":"sess-1","workspace_id":"peer-ws","label":"renamed"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/workspaces/peer-ws/runtime/sessions/sess-1":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/prj-peer/worktrees/wtr-peer/runtime":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"launch_targets":[],"sessions":[],"shell_session":null}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/projects/prj-peer/worktrees/wtr-peer/runtime/shell":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"key":"shell-peer","project_id":"prj-peer","worktree_id":"wtr-peer","target_key":"plain_shell"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/projects/prj-peer/worktrees/wtr-peer/runtime/sessions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"key":"agent-peer","project_id":"prj-peer","worktree_id":"wtr-peer","target_key":"helper"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/projects/prj-peer/worktrees/wtr-peer/runtime/sessions/agent-peer":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/workspaces/peer-ws":
			if r.URL.RawQuery != "force=true" {
				recordHandlerError("delete workspace query: " + r.URL.RawQuery)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	peerTS := httptest.NewServer(peerMux)
	t.Cleanup(peerTS.Close)

	hubCfg := &config.Config{
		BasePath: "/",
		Fleet: config.Fleet{
			Enabled: true,
			Key:     "hub",
			// PeerTimeout is the read fan-out budget. Drive mutations
			// deliberately forward the caller's request context instead
			// of reusing this short read timeout.
			PeerTimeout: "1ns",
			Peers: []config.FleetPeer{
				{Key: "peer", Name: "peer", BaseURL: peerTS.URL},
			},
		},
	}
	hubTS, _ := bootFleetServer(t, hubCfg)

	status, body := getRaw(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/workspaces")
	assert.Equal(http.StatusOK, status)
	req.JSONEq(`{"workspaces":[{"id":"peer-ws","status":"ready"}]}`, body)

	status, body = getRaw(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/workspaces/peer-ws")
	assert.Equal(http.StatusOK, status)
	req.JSONEq(`{"id":"peer-ws","status":"ready"}`, body)

	status, body = postJSON(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/workspaces/peer-ws/retry", nil)
	assert.Equal(http.StatusAccepted, status)
	req.JSONEq(`{"id":"peer-ws","status":"creating"}`, body)

	status, body = postJSON(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/workspaces/peer-ws/refresh", nil)
	assert.Equal(http.StatusOK, status)
	req.JSONEq(`{"id":"peer-ws","status":"ready","refreshed":true}`, body)

	status, body = getRaw(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/workspaces/peer-ws/runtime")
	assert.Equal(http.StatusOK, status)
	req.JSONEq(`{"launch_targets":[],"sessions":[]}`, body)

	status, body = postJSON(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/workspaces", map[string]any{
		"platform_host": "github.com",
		"owner":         "acme",
		"name":          "widget",
		"mr_number":     7,
	})
	assert.Equal(http.StatusAccepted, status)
	req.JSONEq(`{"id":"peer-ws","status":"queued"}`, body)

	status, body = postJSON(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/workspaces/peer-ws/runtime/sessions", map[string]any{
		"target_key": "helper",
	})
	assert.Equal(http.StatusOK, status)
	req.JSONEq(`{"key":"peer-ws:helper","workspace_id":"peer-ws","target_key":"helper"}`, body)

	status, body = patchJSON(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/workspaces/peer-ws/runtime/sessions/sess-1", map[string]any{
		"label": "renamed",
	})
	assert.Equal(http.StatusOK, status)
	req.JSONEq(`{"key":"sess-1","workspace_id":"peer-ws","label":"renamed"}`, body)

	status, body = deleteJSON(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/workspaces/peer-ws/runtime/sessions/sess-1")
	assert.Equal(http.StatusNoContent, status)
	assert.Empty(body)

	status, body = getRaw(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/projects/prj-peer/worktrees/wtr-peer/runtime")
	assert.Equal(http.StatusOK, status)
	req.JSONEq(`{"launch_targets":[],"sessions":[],"shell_session":null}`, body)

	status, body = postJSON(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/projects/prj-peer/worktrees/wtr-peer/runtime/shell", nil)
	assert.Equal(http.StatusOK, status)
	req.JSONEq(`{"key":"shell-peer","project_id":"prj-peer","worktree_id":"wtr-peer","target_key":"plain_shell"}`, body)

	status, body = postJSON(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/projects/prj-peer/worktrees/wtr-peer/runtime/sessions", map[string]any{
		"target_key": "helper",
	})
	assert.Equal(http.StatusOK, status)
	req.JSONEq(`{"key":"agent-peer","project_id":"prj-peer","worktree_id":"wtr-peer","target_key":"helper"}`, body)

	status, body = deleteJSON(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/projects/prj-peer/worktrees/wtr-peer/runtime/sessions/agent-peer")
	assert.Equal(http.StatusNoContent, status)
	assert.Empty(body)

	status, body = deleteJSON(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/peer/workspaces/peer-ws?force=true")
	assert.Equal(http.StatusNoContent, status)
	assert.Empty(body)

	observedMu.Lock()
	got := append([]observedRequest(nil), observed...)
	gotHandlerErrors := append([]string(nil), handlerErrors...)
	observedMu.Unlock()
	assert.Empty(gotHandlerErrors)
	req.Len(got, 14)
	assert.Equal(http.MethodGet, got[0].Method)
	assert.Equal("/api/v1/workspaces", got[0].Path)
	assert.Equal("/api/v1/workspaces/peer-ws", got[1].Path)
	assert.Equal("/api/v1/workspaces/peer-ws/retry", got[2].Path)
	assert.Equal("/api/v1/workspaces/peer-ws/refresh", got[3].Path)
	assert.Equal("/api/v1/workspaces/peer-ws/runtime", got[4].Path)
	assert.Equal(http.MethodPost, got[5].Method)
	assert.Equal("/api/v1/workspaces", got[5].Path)
	req.JSONEq(`{"platform_host":"github.com","owner":"acme","name":"widget","mr_number":7}`, got[5].Body)
	assert.Equal("/api/v1/workspaces/peer-ws/runtime/sessions", got[6].Path)
	req.JSONEq(`{"target_key":"helper"}`, got[6].Body)
	assert.Equal(http.MethodPatch, got[7].Method)
	assert.Equal("/api/v1/workspaces/peer-ws/runtime/sessions/sess-1", got[7].Path)
	req.JSONEq(`{"label":"renamed"}`, got[7].Body)
	assert.Equal("/api/v1/workspaces/peer-ws/runtime/sessions/sess-1", got[8].Path)
	assert.Equal(http.MethodGet, got[9].Method)
	assert.Equal("/api/v1/projects/prj-peer/worktrees/wtr-peer/runtime", got[9].Path)
	assert.Equal("/api/v1/projects/prj-peer/worktrees/wtr-peer/runtime/shell", got[10].Path)
	assert.Equal("/api/v1/projects/prj-peer/worktrees/wtr-peer/runtime/sessions", got[11].Path)
	req.JSONEq(`{"target_key":"helper"}`, got[11].Body)
	assert.Equal("/api/v1/projects/prj-peer/worktrees/wtr-peer/runtime/sessions/agent-peer", got[12].Path)
	assert.Equal("/api/v1/workspaces/peer-ws", got[13].Path)
	assert.Equal("force=true", got[13].Query)
}

func TestFleetOperationProxyUnknownHostE2E(t *testing.T) {
	hubCfg := &config.Config{
		BasePath: "/",
		Fleet: config.Fleet{
			Enabled: true,
			Key:     "hub",
		},
	}
	hubTS, _ := bootFleetServer(t, hubCfg)

	status, body := postJSON(t, hubTS.Client(), hubTS.URL+"/api/v1/fleet/hosts/missing/workspaces", map[string]any{})
	require.Equal(t, http.StatusNotFound, status)
	require.Contains(t, body, `"code":"notFound"`)
	require.Contains(t, body, `"hostKey":"missing"`)
}

func TestFleetOperationProxyPeerDispatchFailureE2E(t *testing.T) {
	assert := Assert.New(t)
	peerTS := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	peerURL := peerTS.URL
	peerTS.Close()

	hubCfg := &config.Config{
		BasePath: "/",
		Fleet: config.Fleet{
			Enabled: true,
			Key:     "hub",
			Peers: []config.FleetPeer{
				{Key: "peer", Name: "peer", BaseURL: peerURL},
			},
		},
	}
	hubTS, _ := bootFleetServer(t, hubCfg)

	status, body := postJSON(
		t,
		hubTS.Client(),
		hubTS.URL+"/api/v1/fleet/hosts/peer/workspaces",
		map[string]any{},
	)
	require.Equal(t, http.StatusBadGateway, status)
	problem := decodeFleetProblem(t, body)
	assert.Equal(http.StatusBadGateway, problem.Status)
	assert.Equal("upstreamError", problem.Code)
	assert.Contains(problem.Detail, "fleet peer request failed")
	assert.Equal("peer", problem.Details["hostKey"])
}

func TestFleetOperationProxyRoutesSelfNestedOwnerE2E(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	hubCfg := &config.Config{
		BasePath: "/",
		Fleet: config.Fleet{
			Enabled: true,
			Key:     "hub",
		},
	}
	hubTS, database := bootFleetServer(t, hubCfg)
	ctx := context.Background()

	repoID, err := database.UpsertRepo(ctx, dbpkg.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "group/subgroup",
		Name:         "widget",
	})
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = database.UpsertIssue(ctx, &dbpkg.Issue{
		RepoID: repoID, PlatformID: 7007, Number: 7,
		URL: "https://gitlab.com/group/subgroup/widget/-/issues/7", Title: "Nested owner issue", Author: "dev",
		State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(database.InsertWorkspace(ctx, &dbpkg.Workspace{
		ID: "ws-nested-issue", Platform: "gitlab", PlatformHost: "gitlab.com",
		RepoOwner: "group/subgroup", RepoName: "widget",
		ItemType: dbpkg.WorkspaceItemTypeIssue, ItemNumber: 7,
		GitHeadRef: "middleman/issue-7", WorktreePath: t.TempDir() + "/ws-nested",
		TmuxSession: "middleman-ws-nested", Status: "ready",
	}))

	status, body := postJSON(
		t,
		hubTS.Client(),
		hubTS.URL+"/api/v1/fleet/hosts/hub/host/gitlab.com/issues/gitlab/group%2Fsubgroup/widget/7/workspace",
		map[string]any{},
	)
	require.Equal(http.StatusAccepted, status, body)

	var got struct {
		ID        string `json:"id"`
		RepoOwner string `json:"repo_owner"`
		RepoName  string `json:"repo_name"`
		ItemType  string `json:"item_type"`
	}
	require.NoError(json.Unmarshal([]byte(body), &got))
	assert.Equal("ws-nested-issue", got.ID)
	assert.Equal("group/subgroup", got.RepoOwner)
	assert.Equal("widget", got.RepoName)
	assert.Equal(dbpkg.WorkspaceItemTypeIssue, got.ItemType)
}

func TestFleetTerminalWebSocketProxyE2E(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var handlerMu sync.Mutex
	var handlerErrors []string
	recordHandlerError := func(msg string) {
		handlerMu.Lock()
		defer handlerMu.Unlock()
		handlerErrors = append(handlerErrors, msg)
	}

	peerMux := http.NewServeMux()
	peerMux.HandleFunc("/ws/v1/workspaces/ws-1/runtime/sessions/sess-1/terminal", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "cols=80&rows=24" {
			recordHandlerError("terminal query: " + r.URL.RawQuery)
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			recordHandlerError("accept websocket: " + err.Error())
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "peer done")

		typ, data, err := conn.Read(r.Context())
		if err != nil {
			recordHandlerError("read websocket: " + err.Error())
			return
		}
		if typ != websocket.MessageBinary {
			recordHandlerError("message type: " + typ.String())
		}
		if err := conn.Write(r.Context(), websocket.MessageBinary, append([]byte("peer:"), data...)); err != nil {
			recordHandlerError("write websocket: " + err.Error())
		}
	})
	peerTS := httptest.NewServer(peerMux)
	t.Cleanup(peerTS.Close)

	hubCfg := &config.Config{
		BasePath: "/",
		Fleet: config.Fleet{
			Enabled: true,
			Key:     "hub",
			Peers: []config.FleetPeer{
				{Key: "peer", Name: "peer", BaseURL: peerTS.URL},
			},
		},
	}
	hubTS, _ := bootFleetServer(t, hubCfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(hubTS.URL, "http") +
		"/ws/v1/fleet/hosts/peer/workspaces/ws-1/runtime/sessions/sess-1/terminal?cols=80&rows=24"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	require.NoError(conn.Write(ctx, websocket.MessageBinary, []byte("ping")))
	typ, data, err := conn.Read(ctx)
	require.NoError(err)
	require.Equal(websocket.MessageBinary, typ)
	require.Equal("peer:ping", string(data))

	handlerMu.Lock()
	gotHandlerErrors := append([]string(nil), handlerErrors...)
	handlerMu.Unlock()
	assert.Empty(gotHandlerErrors)
}

func TestFleetTerminalWebSocketProxyPeerDialFailureE2E(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	peerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "websocket unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(peerTS.Close)

	hubCfg := &config.Config{
		BasePath: "/",
		Fleet: config.Fleet{
			Enabled: true,
			Key:     "hub",
			Peers: []config.FleetPeer{
				{Key: "peer", Name: "peer", BaseURL: peerTS.URL},
			},
		},
	}
	hubTS, _ := bootFleetServer(t, hubCfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(hubTS.URL, "http") +
		"/ws/v1/fleet/hosts/peer/workspaces/ws-1/runtime/sessions/sess-1/terminal"
	conn, resp, err := websocket.Dial(ctx, wsURL, nil)
	require.Error(err)
	require.Nil(conn)
	require.NotNil(resp)
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	require.NoError(readErr)

	require.Equal(http.StatusBadGateway, resp.StatusCode)
	problem := decodeFleetProblem(t, string(body))
	assert.Equal(http.StatusBadGateway, problem.Status)
	assert.Equal("upstreamError", problem.Code)
	assert.Contains(problem.Detail, "fleet peer websocket failed")
	assert.Equal("peer", problem.Details["hostKey"])
}

type fleetProblemBody struct {
	Status  int            `json:"status"`
	Code    string         `json:"code"`
	Detail  string         `json:"detail"`
	Details map[string]any `json:"details"`
}

func decodeFleetProblem(t *testing.T, body string) fleetProblemBody {
	t.Helper()
	var problem fleetProblemBody
	require.NoError(t, json.Unmarshal([]byte(body), &problem), body)
	return problem
}

func anyProjectScopedPrefix(ps []fleet.RawProject, prefix string) bool {
	for _, p := range ps {
		if len(p.ScopedKey) >= len(prefix) && p.ScopedKey[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func anyWorktreeScopedPrefix(ws []fleet.RawWorktree, prefix string) bool {
	for _, w := range ws {
		if len(w.ScopedKey) >= len(prefix) && w.ScopedKey[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func findHost(hosts []fleet.HostSummary, configKey string) *fleet.HostSummary {
	for i := range hosts {
		if hosts[i].ConfigKey == configKey {
			return &hosts[i]
		}
	}
	return nil
}

func rawTmuxByName(
	sessions []fleet.TmuxSessionInfo,
	name string,
) *fleet.TmuxSessionInfo {
	for i := range sessions {
		if sessions[i].Name == name {
			return &sessions[i]
		}
	}
	return nil
}

func rawSessionByScopedKey(
	sessions []fleet.RawSession,
	scopedKey string,
) *fleet.RawSession {
	for i := range sessions {
		if sessions[i].ScopedKey == scopedKey {
			return &sessions[i]
		}
	}
	return nil
}

func summarySessionByScopedKey(
	sessions []fleet.SessionSummary,
	scopedKey string,
) *fleet.SessionSummary {
	for i := range sessions {
		if sessions[i].ScopedKey == scopedKey {
			return &sessions[i]
		}
	}
	return nil
}

func diagnosticByCode(
	diagnostics []fleet.HostDiagnostic,
	code string,
) *fleet.HostDiagnostic {
	for i := range diagnostics {
		if diagnostics[i].Code == code {
			return &diagnostics[i]
		}
	}
	return nil
}

func worktreeByLinkedPR(ws []fleet.WorktreeSummary, pr int) *fleet.WorktreeSummary {
	for i := range ws {
		if ws[i].LinkedPRNumber != nil && *ws[i].LinkedPRNumber == pr {
			return &ws[i]
		}
	}
	return nil
}

func worktreeByLinkedIssue(ws []fleet.WorktreeSummary, issue int) *fleet.WorktreeSummary {
	for i := range ws {
		if slices.Contains(ws[i].LinkedIssueNumbers, issue) {
			return &ws[i]
		}
	}
	return nil
}

func worktreeByScopedKey(ws []fleet.WorktreeSummary, key string) *fleet.WorktreeSummary {
	for i := range ws {
		if ws[i].ScopedKey == key {
			return &ws[i]
		}
	}
	return nil
}

// TestFleetSnapshotDraftFoldE2E drives the draft-fold adapter rule through
// the real HTTP snapshot: an OPEN draft PR folds into prState "draft", but a
// CLOSED draft PR must keep its terminal state ("closed") rather than report
// "draft". Both come from the same worktreeFromWorkspace path, so this guards
// the conditional end-to-end, not just at the adapter unit boundary.
func TestFleetSnapshotDraftFoldE2E(t *testing.T) {
	require := require.New(t)
	ts, database := bootFleetServer(t, nil)
	ctx := context.Background()

	repoID, err := database.UpsertRepo(ctx, dbpkg.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	openDraft := true
	closedDraft := true
	_, err = database.UpsertMergeRequest(ctx, &dbpkg.MergeRequest{
		RepoID: repoID, PlatformID: 2001, Number: 1,
		URL: "https://github.com/acme/widget/pull/1", Title: "Open draft", Author: "dev",
		State: "open", IsDraft: openDraft, HeadBranch: "feat-open", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &dbpkg.MergeRequest{
		RepoID: repoID, PlatformID: 2002, Number: 2,
		URL: "https://github.com/acme/widget/pull/2", Title: "Closed draft", Author: "dev",
		State: "closed", IsDraft: closedDraft, HeadBranch: "feat-closed", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	require.NoError(database.InsertWorkspace(ctx, &dbpkg.Workspace{
		ID: "ws-open-draft", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: dbpkg.WorkspaceItemTypePullRequest, ItemNumber: 1,
		GitHeadRef: "feat-open", WorktreePath: t.TempDir() + "/ws-open", TmuxSession: "middleman-ws-open",
		Status: "ready",
	}))
	require.NoError(database.InsertWorkspace(ctx, &dbpkg.Workspace{
		ID: "ws-closed-draft", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget",
		ItemType: dbpkg.WorkspaceItemTypePullRequest, ItemNumber: 2,
		GitHeadRef: "feat-closed", WorktreePath: t.TempDir() + "/ws-closed", TmuxSession: "middleman-ws-closed",
		Status: "ready",
	}))

	var snap fleet.Snapshot
	getJSON(t, ts, "/api/v1/snapshot", &snap)

	openWT := worktreeByLinkedPR(snap.Worktrees, 1)
	require.NotNil(openWT, "open-draft workspace worktree present")
	require.NotNil(openWT.PRState)
	require.Equal("draft", *openWT.PRState, "an open draft PR must fold to prState=draft")

	closedWT := worktreeByLinkedPR(snap.Worktrees, 2)
	require.NotNil(closedWT, "closed-draft workspace worktree present")
	require.NotNil(closedWT.PRState)
	require.Equal("closed", *closedWT.PRState, "a closed draft PR must keep its terminal state, not draft")
}

// TestFleetSnapshotPeerRichFieldsE2E exercises the full hub fan-out for a
// reachable peer that serves a rich raw snapshot over HTTP: the hub fetches
// /api/v1/snapshot/raw, validates schemaVersion, merges, enriches, and
// serializes. It asserts the peer's host version + tmux inventory, degraded
// platform coverage, canonicalized worktree timestamps, lowercased PR state,
// session runtimeKind, and operation availability through the routed hub
// contract (routed capable peer mutations available, unrouted mutations
// suppressed, offline peer reports "Host is offline.").
func TestFleetSnapshotPeerRichFieldsE2E(t *testing.T) {
	require := require.New(t)
	prState := "OPEN"
	prUpdated := "2026-05-30T10:00:00Z"
	lastPolled := "2026-05-30T09:00:00Z"
	backendNotReady := false
	platformAuth := true
	peerRaw := fleet.RawSnapshot{
		SchemaVersion:         fleet.SchemaVersion,
		PlatformAuthenticated: &platformAuth,
		Capabilities: &fleet.Capabilities{
			Dependencies: fleet.DependencyCapabilities{Git: true, Tmux: true, Gh: true},
			Commands: fleet.CommandCapabilities{
				WorktreeCreate: true, WorktreeDelete: true, SessionEnsure: true,
				SessionKill: true, WorktreeImportPR: true, RepositoryClone: true,
				ProjectAdd: true, ProjectRemove: true,
			},
		},
		Host: fleet.RawHost{
			Hostname:         "studio",
			Platform:         "linux",
			Version:          "7.7.7",
			TmuxLastPolledAt: "2026-05-31T10:00:00Z",
			TmuxProbeError:   "inventory failed after last sample",
			TmuxMetricsError: "ps failed",
			TmuxSessions:     []fleet.TmuxSessionInfo{{Name: "w-1", Managed: true, WorktreeKey: "worktree:/peer/a"}},
		},
		Projects: []fleet.RawProject{{
			ScopedKey: "repo:/peer/a", Name: "peer/app", RootPath: "/peer/a",
			PlatformHost: "github.com", PlatformRepo: "peer/app",
			RepositoryKind: "git", BackendReady: &backendNotReady,
		}},
		Worktrees: []fleet.RawWorktree{{
			ScopedKey: "worktree:/peer/a", ProjectKey: "repo:/peer/a", Name: "a", Path: "/peer/a", IsPrimary: true,
			PRState: &prState, PRUpdatedAt: &prUpdated, LastPolledAt: &lastPolled,
		}},
		Sessions: []fleet.RawSession{{
			ScopedKey: "session:s1", WorktreeKey: "worktree:/peer/a", Status: "running",
			RuntimeKind: "agent", SessionKind: "preset", Role: "driver",
			ExecutableName: "claude", AgentKind: "claude",
		}},
	}

	peerMux := http.NewServeMux()
	peerMux.HandleFunc("/api/v1/snapshot/raw", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(peerRaw); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	peerTS := httptest.NewServer(peerMux)
	t.Cleanup(peerTS.Close)

	hubCfg := &config.Config{
		BasePath: "/",
		Fleet: config.Fleet{
			Enabled:     true,
			Key:         "hub",
			PeerTimeout: "5s",
			Peers: []config.FleetPeer{
				{Key: "peer", Name: "peer", BaseURL: peerTS.URL},
				{Key: "down", Name: "down", BaseURL: "http://127.0.0.1:1"},
			},
		},
	}
	hubTS, _ := bootFleetServer(t, hubCfg)

	var snap fleet.Snapshot
	getJSON(t, hubTS, "/api/v1/snapshot?include_peers=true", &snap)

	// Peer host: version + tmux inventory folded from its raw host record.
	peer := findHost(snap.Hosts, "peer")
	require.NotNil(peer, "reachable peer host present")
	require.True(peer.Reachable)
	require.Equal("linux", peer.Platform)
	require.NotNil(peer.Version)
	require.Equal("7.7.7", *peer.Version, "peer host version must surface through fan-out")
	require.NotNil(peer.TmuxLastPolledAt)
	require.Equal("2026-05-31T10:00:00.000Z", *peer.TmuxLastPolledAt, "peer tmux freshness must surface through fan-out")
	require.Equal("inventory failed after last sample", peer.TmuxProbeError, "peer tmux probe error must surface through fan-out")
	require.Equal("ps failed", peer.TmuxMetricsError, "peer tmux metrics error must surface through fan-out")
	require.Len(peer.TmuxSessions, 1, "peer tmux inventory must surface through fan-out")
	require.Equal("w-1", peer.TmuxSessions[0].Name)

	// Routed hub policy: a capable peer's mutation ops are available through
	// host-key proxying.
	wc := peer.OperationAvailability["worktreeCreate"]
	require.True(wc.Available, "hub exposes capable peer mutation ops")
	require.Nil(wc.UnavailableReason)
	clone := peer.OperationAvailability["repositoryClone"]
	require.True(clone.Available,
		"clone is hub-routed via /fleet/hosts/{key}/projects/clone")
	require.Nil(clone.UnavailableReason)

	// Offline peer: every op unavailable with the offline reason.
	down := findHost(snap.Hosts, "down")
	require.NotNil(down)
	require.False(down.Reachable)
	require.NotEmpty(down.OperationAvailability)
	for op, a := range down.OperationAvailability {
		require.False(a.Available, "offline op %s", op)
		require.NotNil(a.UnavailableReason)
		require.Equal("Host is offline.", *a.UnavailableReason, "offline op %s", op)
	}

	// Peer project: backendReady=false yields degraded platform coverage.
	var peerProj *fleet.ProjectSummary
	for i := range snap.Projects {
		if snap.Projects[i].ScopedKey == "repo:/peer/a" {
			peerProj = &snap.Projects[i]
		}
	}
	require.NotNil(peerProj, "peer project merged")
	require.NotNil(peerProj.PlatformCoverage)
	require.Equal("degraded", *peerProj.PlatformCoverage, "backendReady=false must degrade coverage")

	// Peer worktree: lowercased PR state + canonicalized timestamps.
	peerWT := worktreeByScopedKey(snap.Worktrees, "worktree:/peer/a")
	require.NotNil(peerWT, "peer worktree merged")
	require.NotNil(peerWT.PRState)
	require.Equal("open", *peerWT.PRState, "PR state must be lowercased through fan-out")
	require.NotNil(peerWT.PRUpdatedAt)
	require.Equal("2026-05-30T10:00:00.000Z", *peerWT.PRUpdatedAt, "PRUpdatedAt canonicalized through fan-out")
	require.NotNil(peerWT.LastPolledAt)
	require.Equal("2026-05-30T09:00:00.000Z", *peerWT.LastPolledAt, "LastPolledAt canonicalized through fan-out")

	// Peer session: runtimeKind and role carried through fan-out.
	var peerSess *fleet.SessionSummary
	for i := range snap.Sessions {
		if snap.Sessions[i].ScopedKey == "session:s1" {
			peerSess = &snap.Sessions[i]
		}
	}
	require.NotNil(peerSess, "peer session merged")
	require.Equal("agent", peerSess.RuntimeKind, "session runtimeKind carried through fan-out")
	require.Equal("driver", peerSess.Role)
}

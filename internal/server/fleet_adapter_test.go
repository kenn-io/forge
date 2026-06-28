package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/fleet"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/workspace"
)

func TestBuildLocalRawSynthesizesAndDedupes(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: filepath.Join(t.TempDir(), "app"), DefaultBranch: "main",
	})
	require.NoError(err)

	_, err = database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: filepath.Join(t.TempDir(), "app-feat"),
	})
	require.NoError(err)

	orphanPath := filepath.Join(t.TempDir(), "orphan")
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "ws-orphan", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "o", RepoName: "orphan",
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 1,
		GitHeadRef: "feature/orphan", WorktreePath: orphanPath,
		TmuxSession: "middleman-ws-orphan", Status: "ready",
	}))

	srv := &Server{db: database, workspaces: workspace.NewManager(database, t.TempDir())}

	raw, err := srv.buildLocalRaw(ctx)
	require.NoError(err)

	require.Len(raw.Projects, 2, "registered + synthesized orphan project")
	require.Len(raw.Worktrees, 3, "primary root + bare project-worktree + workspace worktree")
	require.GreaterOrEqual(len(raw.Sessions), 1, "ready workspace yields its main tmux session")
	require.Empty(raw.Host.TmuxSessions, "host tmux inventory comes from the live monitor cache")

	require.True(rawHasProjectName(raw.Projects, "app"))
	require.True(rawHasProjectName(raw.Projects, "o/orphan"))

	orphan := requireRawProjectScoped(t, raw.Projects, "repo:github:github.com:o/orphan")
	require.True(orphan.IsSynthesized, "orphan-workspace project is synthesized")
	require.Empty(orphan.RootPath, "synthesized project has no local checkout root")
	require.Empty(orphan.RepositoryKind, "synthesized project has no known repository kind")
	require.Empty(orphan.DefaultBranch, "default branch stays empty when the repo is not synced")
}

// TestBuildLocalRawSynthesizedProjectFillsDefaultBranchFromSyncedRepo confirms
// the synthesized orphan-workspace project borrows its default branch from the
// repo middleman already synced — a DB read, not read-path git I/O — while
// still carrying no RootPath/RepositoryKind (middleman owns no checkout).
func TestBuildLocalRawSynthesizedProjectFillsDefaultBranchFromSyncedRepo(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "o", Name: "synced", RepoPath: "o/synced",
	})
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderMetadata(ctx, repoID, db.RepoProviderMetadata{
		WebURL:        "https://github.com/o/synced",
		CloneURL:      "https://github.com/o/synced.git",
		DefaultBranch: "trunk",
	}))

	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "ws-synced", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "o", RepoName: "synced",
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 3,
		GitHeadRef: "feature/synced", WorktreePath: filepath.Join(t.TempDir(), "synced"),
		TmuxSession: "middleman-ws-synced", Status: "ready",
	}))

	srv := &Server{db: database, workspaces: workspace.NewManager(database, t.TempDir())}
	raw, err := srv.buildLocalRaw(ctx)
	require.NoError(err)

	proj := requireRawProjectScoped(t, raw.Projects, "repo:github:github.com:o/synced")
	require.True(proj.IsSynthesized, "orphan-workspace project is synthesized")
	require.Equal("trunk", proj.DefaultBranch, "default branch comes from the synced repo row")
	require.Empty(proj.RootPath, "synthesized project still has no local checkout root")
	require.Empty(proj.RepositoryKind, "synthesized project still has no known repository kind")
}

func TestBuildLocalRawOverlaysWorkspaceOntoProjectWorktree(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: filepath.Join(t.TempDir(), "app"), DefaultBranch: "main",
	})
	require.NoError(err)

	shared := filepath.Join(t.TempDir(), "shared-wt")
	_, err = database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: shared,
	})
	require.NoError(err)

	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "ws-shared", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "o", RepoName: "app",
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
		GitHeadRef: "feat", WorktreePath: shared, TmuxSession: "middleman-ws-shared", Status: "ready",
	}))

	srv := &Server{db: database, workspaces: workspace.NewManager(database, t.TempDir())}
	raw, err := srv.buildLocalRaw(ctx)
	require.NoError(err)

	var count int
	var merged *fleet.RawWorktree
	for i := range raw.Worktrees {
		if raw.Worktrees[i].Path == normPath(shared) {
			count++
			merged = &raw.Worktrees[i]
		}
	}
	require.Equal(1, count, "shared path must dedupe to a single worktree")
	require.NotNil(merged)
	require.NotNil(merged.LinkedPRNumber, "workspace overlay must win, carrying PR data")
	require.Equal(42, *merged.LinkedPRNumber)
}

func TestAddWorktreeWorkspaceOverlayPreservesStaleState(t *testing.T) {
	require := require.New(t)
	order := []string{}
	byScopedKey := map[string]*fleet.RawWorktree{}
	prNumber := 42

	addWorktree(&order, byScopedKey, fleet.RawWorktree{
		ScopedKey:  "worktree:/tmp/shared",
		ProjectKey: "repo:/tmp/app",
		Name:       "shared",
		Path:       "/tmp/shared",
		IsStale:    true,
	})
	addWorktree(&order, byScopedKey, fleet.RawWorktree{
		ScopedKey:      "worktree:/tmp/shared",
		ProjectKey:     "repo:/tmp/app",
		Name:           "shared",
		Path:           "/tmp/shared",
		LinkedPRNumber: &prNumber,
	})

	require.Len(order, 1)
	require.True(
		byScopedKey["worktree:/tmp/shared"].IsStale,
		"workspace overlay must not make a stale discovered worktree fresh",
	)
	require.NotNil(byScopedKey["worktree:/tmp/shared"].LinkedPRNumber)
}

func TestBuildLocalRawSurfacesHiddenWorktree(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: filepath.Join(t.TempDir(), "app"), DefaultBranch: "main",
	})
	require.NoError(err)
	wtPath := filepath.Join(t.TempDir(), "app-feat")
	wt, err := database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: wtPath,
	})
	require.NoError(err)
	_, err = database.SetProjectWorktreeHidden(ctx, proj.ID, wt.ID, true, time.Now())
	require.NoError(err)

	srv := &Server{db: database, workspaces: workspace.NewManager(database, t.TempDir())}
	raw, err := srv.buildLocalRaw(ctx)
	require.NoError(err)

	got := requireRawWorktree(t, raw.Worktrees, normPath(wtPath))
	require.True(got.IsHidden, "a hidden linked worktree surfaces IsHidden in the snapshot")
}

// TestBuildLocalRawSurfacesWorktreeSessionBackend proves a persisted
// session-backend override reaches the raw snapshot, where the host write-through
// for /api/worktree/set-session-backend needs it: the persisted value wins over
// the producer's empty->localPTY default (applied later in enrich).
func TestBuildLocalRawSurfacesWorktreeSessionBackend(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: filepath.Join(t.TempDir(), "app"), DefaultBranch: "main",
	})
	require.NoError(err)
	wtPath := filepath.Join(t.TempDir(), "app-feat")
	wt, err := database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: wtPath,
	})
	require.NoError(err)
	_, err = database.SetProjectWorktreeSessionBackend(ctx, proj.ID, wt.ID, "localTmux", time.Now())
	require.NoError(err)

	srv := &Server{db: database, workspaces: workspace.NewManager(database, t.TempDir())}
	raw, err := srv.buildLocalRaw(ctx)
	require.NoError(err)

	got := requireRawWorktree(t, raw.Worktrees, normPath(wtPath))
	require.Equal("localTmux", got.SessionBackend,
		"a persisted session backend surfaces on the raw worktree")
}

// TestBuildLocalRawDropsDeletedProject proves a deleted project — and its
// cascade-deleted worktrees — disappears from the fleet snapshot on the very
// next read, the guarantee the host write-through for /api/project/remove needs.
func TestBuildLocalRawDropsDeletedProject(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: filepath.Join(t.TempDir(), "app"), DefaultBranch: "main",
	})
	require.NoError(err)
	wtPath := filepath.Join(t.TempDir(), "app-feat")
	_, err = database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: wtPath,
	})
	require.NoError(err)

	srv := &Server{db: database, workspaces: workspace.NewManager(database, t.TempDir())}

	raw, err := srv.buildLocalRaw(ctx)
	require.NoError(err)
	require.NotEmpty(raw.Projects, "the registered project is in the snapshot before delete")
	requireRawWorktree(t, raw.Worktrees, normPath(wtPath))

	require.NoError(database.DeleteProject(ctx, proj.ID))

	after, err := srv.buildLocalRaw(ctx)
	require.NoError(err)
	require.Empty(after.Projects, "deleting the project drops it from the snapshot")
	require.Empty(after.Worktrees, "the cascade-deleted worktree is gone from the snapshot too")
}

func TestBuildLocalRawSurfacesPlatformAuthenticated(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	mon := &fleetPlatformAuthMonitor{resolve: func() bool { return true }, interval: time.Hour}
	mon.runOnce()

	srv := &Server{db: database, fleetPlatformAuthMonitor: mon}
	raw, err := srv.buildLocalRaw(context.Background())
	require.NoError(err)
	require.NotNil(raw.PlatformAuthenticated, "a resolved auth signal surfaces in the snapshot")
	require.True(*raw.PlatformAuthenticated)
}

func TestBuildLocalRawOmitsPlatformAuthenticatedWhenUnresolved(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)

	srv := &Server{db: database}
	raw, err := srv.buildLocalRaw(context.Background())
	require.NoError(err)
	require.Nil(raw.PlatformAuthenticated, "an unresolved auth state is omitted, not reported false")
}

func TestAddWorktreeWorkspaceOverlayPreservesHidden(t *testing.T) {
	require := require.New(t)
	order := []string{}
	byScopedKey := map[string]*fleet.RawWorktree{}
	prNumber := 7

	addWorktree(&order, byScopedKey, fleet.RawWorktree{
		ScopedKey: "worktree:/tmp/wt", ProjectKey: "repo:/tmp/app",
		Name: "wt", Path: "/tmp/wt", IsHidden: true,
	})
	addWorktree(&order, byScopedKey, fleet.RawWorktree{
		ScopedKey: "worktree:/tmp/wt", ProjectKey: "repo:/tmp/app",
		Name: "wt", Path: "/tmp/wt", LinkedPRNumber: &prNumber,
	})

	require.Len(order, 1)
	require.True(byScopedKey["worktree:/tmp/wt"].IsHidden,
		"a workspace overlay must not unhide a hidden worktree")
	require.NotNil(byScopedKey["worktree:/tmp/wt"].LinkedPRNumber)
}

func TestBuildLocalRawReconcilesLiveTmuxInventory(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()
	worktreePath := filepath.Join(t.TempDir(), "ws")
	createdAt := time.Date(2026, 5, 31, 9, 0, 0, 0, time.UTC)
	polledAt := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)

	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "ws-1", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "o", RepoName: "app",
		ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 7,
		GitHeadRef: "feature", WorktreePath: worktreePath,
		TmuxSession: "middleman-main", Status: "ready",
		CreatedAt: createdAt,
	}))
	require.NoError(database.UpsertWorkspaceRuntimeSession(ctx, &db.WorkspaceRuntimeSession{
		WorkspaceID: "ws-1",
		SessionKey:  "ws-1_codex",
		TargetKey:   "codex",
		Label:       "codex",
		Kind:        "agent",
		Scope:       "session",
		TmuxSession: "middleman-agent",
		CreatedAt:   createdAt,
	}))

	mon := newFleetTmuxMonitor([]string{"tmux"}, false, nil)
	mon.recordInventorySample(fleetTmuxInventorySample{
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
	})

	srv := &Server{
		db:               database,
		workspaces:       workspace.NewManager(database, t.TempDir()),
		fleetTmuxMonitor: mon,
	}
	raw, err := srv.buildLocalRaw(ctx)
	require.NoError(err)

	require.Equal(polledAt.Format(time.RFC3339), raw.Host.TmuxLastPolledAt)
	main := requireRawTmuxInfo(t, raw.Host.TmuxSessions, "middleman-main")
	require.True(main.Managed)
	require.Equal("session:ws-1:main", main.SessionScopedKey)
	require.Len(main.Windows, 1)

	runtimeScopedKey := "session:ws-1_codex"
	agent := requireRawTmuxInfo(t, raw.Host.TmuxSessions, "middleman-agent")
	require.True(agent.Managed)
	require.Equal(runtimeScopedKey, agent.SessionScopedKey)
	requireRawSession(t, raw.Sessions, runtimeScopedKey)

	personal := requireRawTmuxInfo(t, raw.Host.TmuxSessions, "personal")
	require.False(personal.Managed)
	require.Equal(2, personal.WindowCount)
	require.Empty(personal.Windows)
}

func TestBuildLocalRawIncludesProjectWorktreeRuntimeTmuxSession(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()
	projectPath := filepath.Join(t.TempDir(), "app")
	worktreePath := filepath.Join(t.TempDir(), "app-runtime")
	createdAt := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	polledAt := time.Date(2026, 6, 1, 8, 1, 0, 0, time.UTC)

	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: projectPath, DefaultBranch: "main",
	})
	require.NoError(err)
	worktree, err := database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID,
		Branch:    "runtime",
		Path:      worktreePath,
	})
	require.NoError(err)
	require.NoError(database.UpsertProjectWorktreeTmuxSession(ctx, &db.ProjectWorktreeTmuxSession{
		WorktreeID:  worktree.ID,
		SessionKey:  "wt_codex",
		SessionName: "middleman-project-worktree-agent",
		TargetKey:   "codex",
		CreatedAt:   createdAt,
	}))

	mon := newFleetTmuxMonitor([]string{"tmux"}, false, nil)
	mon.recordInventorySample(fleetTmuxInventorySample{
		PolledAt:  polledAt,
		Succeeded: true,
		Sessions: map[string]fleetTmuxLiveSession{
			"middleman-project-worktree-agent": {
				Name:        "middleman-project-worktree-agent",
				WindowCount: 1,
			},
		},
	})

	srv := &Server{db: database, fleetTmuxMonitor: mon}
	raw, err := srv.buildLocalRaw(ctx)
	require.NoError(err)

	wtKey := "worktree:" + normPath(worktreePath)
	sessionKey := "session:wt_codex"
	session := requireRawSession(t, raw.Sessions, sessionKey)
	require.Equal(wtKey, session.WorktreeKey)
	require.Equal("agent", session.RuntimeKind)
	require.Equal("codex", session.Label)

	info := requireRawTmuxInfo(
		t, raw.Host.TmuxSessions, "middleman-project-worktree-agent",
	)
	require.True(info.Managed)
	require.Equal(wtKey, info.WorktreeKey)
	require.Equal(sessionKey, info.SessionScopedKey)
}

func requireRawTmuxInfo(
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

func requireRawSession(
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
	require.Fail(t, "raw session not found", scopedKey)
	return fleet.RawSession{}
}

func rawHasProjectName(ps []fleet.RawProject, name string) bool {
	for _, p := range ps {
		if p.Name == name {
			return true
		}
	}
	return false
}

func requireRawProjectScoped(
	t *testing.T,
	ps []fleet.RawProject,
	key string,
) fleet.RawProject {
	t.Helper()
	for _, p := range ps {
		if p.ScopedKey == key {
			return p
		}
	}
	require.Fail(t, "raw project not found", key)
	return fleet.RawProject{}
}

// TestApplyLinkPRPopulatesEnrichment confirms the branch-match overlay carries
// the linked merge request's review/mergeable/size/comment enrichment onto the
// registered worktree.
func TestApplyLinkPRPopulatesEnrichment(t *testing.T) {
	require := require.New(t)
	wt := fleet.RawWorktree{}
	applyLinkPR(&wt, db.WorktreeLinkPR{
		WorktreeKey:    "worktree:/work/wt",
		Number:         7,
		State:          db.MergeRequestStateOpen,
		Title:          "Add feature",
		CIStatus:       "success",
		ReviewDecision: "changes_requested",
		MergeableState: "dirty",
		Additions:      12,
		Deletions:      3,
		CommentCount:   5,
	})
	require.NotNil(wt.LinkedPRNumber)
	require.Equal(7, *wt.LinkedPRNumber)
	require.NotNil(wt.PRReviewDecision)
	require.Equal("changes_requested", *wt.PRReviewDecision)
	require.NotNil(wt.PRMergeable)
	require.Equal("dirty", *wt.PRMergeable)
	require.NotNil(wt.PRAdditions)
	require.Equal(12, *wt.PRAdditions)
	require.NotNil(wt.PRDeletions)
	require.Equal(3, *wt.PRDeletions)
	require.NotNil(wt.PRCommentCount)
	require.Equal(5, *wt.PRCommentCount)
}

// TestApplyLinkPROmitsZeroAndEmptyEnrichment confirms an undetailed linked PR
// (no review decision, no mergeable state, zero counts) overlays no misleading
// empty or zero enrichment values.
func TestApplyLinkPROmitsZeroAndEmptyEnrichment(t *testing.T) {
	require := require.New(t)
	wt := fleet.RawWorktree{}
	applyLinkPR(&wt, db.WorktreeLinkPR{
		WorktreeKey: "worktree:/work/wt",
		Number:      7,
		State:       db.MergeRequestStateOpen,
	})
	require.Nil(wt.PRReviewDecision)
	require.Nil(wt.PRMergeable)
	require.Nil(wt.PRAdditions)
	require.Nil(wt.PRDeletions)
	require.Nil(wt.PRCommentCount)
}

func TestWorktreeFromWorkspaceFoldsDraft(t *testing.T) {
	open := "open"
	draftFlag := true
	sum := db.WorkspaceSummary{
		Workspace: db.Workspace{WorktreePath: "/tmp/wt-draft"},
		MRState:   &open,
		MRIsDraft: &draftFlag,
	}
	wt := worktreeFromWorkspace(sum, "worktree:/tmp/wt-draft", "repo:/tmp")
	require.NotNil(t, wt.PRState)
	require.Equal(t, "draft", *wt.PRState, "an open draft PR folds into PRState=draft")
}

// TestWorktreeFromWorkspaceClosedDraftKeepsTerminalState guards the fix for
// draft folding: the draft flag only overrides while the PR is open, so a
// closed (or merged) draft must report its terminal state, not "draft".
func TestWorktreeFromWorkspaceClosedDraftKeepsTerminalState(t *testing.T) {
	closed := "closed"
	draftFlag := true
	sum := db.WorkspaceSummary{
		Workspace: db.Workspace{WorktreePath: "/tmp/wt-closed"},
		MRState:   &closed,
		MRIsDraft: &draftFlag,
	}
	wt := worktreeFromWorkspace(sum, "worktree:/tmp/wt-closed", "repo:/tmp")
	require.NotNil(t, wt.PRState)
	require.Equal(t, "closed", *wt.PRState, "a closed draft must keep its terminal state, not draft")
}

// TestWorktreeFromWorkspaceIssueExposesIssueLink guards the fix for issue
// workspaces: the summary's joined title/state are the issue's, so they must
// not surface as PR fields. The issue number is exposed as the issue link,
// and only an associated PR number (not PR display data) is carried.
func TestWorktreeFromWorkspaceIssueExposesIssueLink(t *testing.T) {
	require := require.New(t)
	issueTitle := "Investigate flaky test"
	issueState := "open"
	associatedPR := 99
	sum := db.WorkspaceSummary{
		Workspace: db.Workspace{
			WorktreePath:       "/tmp/wt-issue",
			ItemType:           db.WorkspaceItemTypeIssue,
			ItemNumber:         42,
			AssociatedPRNumber: &associatedPR,
		},
		MRTitle: &issueTitle,
		MRState: &issueState,
	}
	wt := worktreeFromWorkspace(sum, "worktree:/tmp/wt-issue", "repo:/tmp")
	require.Equal([]int{42}, wt.LinkedIssueNumbers, "issue number must surface as the issue link")
	require.Nil(wt.PRTitle, "issue title must not surface as a PR title")
	require.Nil(wt.PRState, "issue state must not surface as a PR state")
	require.NotNil(wt.LinkedPRNumber, "an associated PR number is still carried")
	require.Equal(99, *wt.LinkedPRNumber)
}

// TestWorktreeFromWorkspacePRPopulatesPRFields confirms PR workspaces still
// carry their PR metadata and never gain a spurious issue link.
func TestWorktreeFromWorkspacePRPopulatesPRFields(t *testing.T) {
	require := require.New(t)
	title := "Add widget"
	state := "open"
	reviewDecision := "approved"
	mergeable := "clean"
	additions := 40
	deletions := 9
	comments := 4
	sum := db.WorkspaceSummary{
		Workspace: db.Workspace{
			WorktreePath: "/tmp/wt-pr",
			ItemType:     db.WorkspaceItemTypePullRequest,
			ItemNumber:   7,
		},
		MRTitle:          &title,
		MRState:          &state,
		MRReviewDecision: &reviewDecision,
		MRMergeableState: &mergeable,
		MRAdditions:      &additions,
		MRDeletions:      &deletions,
		MRCommentCount:   &comments,
	}
	wt := worktreeFromWorkspace(sum, "worktree:/tmp/wt-pr", "repo:/tmp")
	require.Empty(wt.LinkedIssueNumbers, "PR workspaces carry no issue link")
	require.NotNil(wt.LinkedPRNumber)
	require.Equal(7, *wt.LinkedPRNumber)
	require.NotNil(wt.PRTitle)
	require.Equal("Add widget", *wt.PRTitle)
	require.NotNil(wt.PRState)
	require.Equal("open", *wt.PRState)
	require.NotNil(wt.PRReviewDecision)
	require.Equal("approved", *wt.PRReviewDecision)
	require.NotNil(wt.PRMergeable)
	require.Equal("clean", *wt.PRMergeable)
	require.NotNil(wt.PRAdditions)
	require.Equal(40, *wt.PRAdditions)
	require.NotNil(wt.PRDeletions)
	require.Equal(9, *wt.PRDeletions)
	require.NotNil(wt.PRCommentCount)
	require.Equal(4, *wt.PRCommentCount)
}

// TestWorktreeFromWorkspacePREnrichmentOmitsZeroAndEmpty guards the
// zero/empty normalization: a synced PR with no review decision (stored "")
// and an undetailed PR (zero additions/deletions/comment count) must omit
// those fields rather than overlay misleading empty or "+0 −0" values.
func TestWorktreeFromWorkspacePREnrichmentOmitsZeroAndEmpty(t *testing.T) {
	require := require.New(t)
	title := "Add widget"
	state := "open"
	emptyDecision := ""
	zero := 0
	sum := db.WorkspaceSummary{
		Workspace: db.Workspace{
			WorktreePath: "/tmp/wt-pr",
			ItemType:     db.WorkspaceItemTypePullRequest,
			ItemNumber:   7,
		},
		MRTitle:          &title,
		MRState:          &state,
		MRReviewDecision: &emptyDecision,
		MRMergeableState: nil,
		MRAdditions:      &zero,
		MRDeletions:      &zero,
		MRCommentCount:   &zero,
	}
	wt := worktreeFromWorkspace(sum, "worktree:/tmp/wt-pr", "repo:/tmp")
	require.Nil(wt.PRReviewDecision, "empty review decision is omitted")
	require.Nil(wt.PRMergeable, "absent mergeable state is omitted")
	require.Nil(wt.PRAdditions, "zero additions are omitted")
	require.Nil(wt.PRDeletions, "zero deletions are omitted")
	require.Nil(wt.PRCommentCount, "zero comment count is omitted")
}

// TestWorktreeFromWorkspaceMapsSessionBackend confirms the workspace's
// persisted terminal backend is translated into the generic fleet
// session-backend vocabulary regardless of item type. A workspace with no
// backend column falls back to empty so the enrichment layer can apply the
// local default.
func TestWorktreeFromWorkspaceMapsSessionBackend(t *testing.T) {
	require := require.New(t)
	cases := []struct {
		name     string
		backend  string
		itemType string
		want     string
	}{
		{"pty owner maps to localPTY", workspace.TerminalBackendPtyOwner,
			db.WorkspaceItemTypePullRequest, fleet.SessionBackendLocalPTY},
		{"tmux maps to localTmux", workspace.TerminalBackendTmux,
			db.WorkspaceItemTypePullRequest, fleet.SessionBackendLocalTmux},
		{"issue workspace still carries the backend", workspace.TerminalBackendPtyOwner,
			db.WorkspaceItemTypeIssue, fleet.SessionBackendLocalPTY},
		{"unset backend falls back to empty", "",
			db.WorkspaceItemTypePullRequest, ""},
	}
	for _, tc := range cases {
		sum := db.WorkspaceSummary{
			Workspace: db.Workspace{
				WorktreePath:    "/tmp/wt",
				ItemType:        tc.itemType,
				ItemNumber:      1,
				TerminalBackend: tc.backend,
			},
		}
		wt := worktreeFromWorkspace(sum, "worktree:/tmp/wt", "repo:/tmp")
		require.Equal(tc.want, wt.SessionBackend, tc.name)
	}
}

// TestBuildLocalRawPopulatesRegistryIDs proves the raw snapshot stamps the
// database row id onto registered projects and worktrees as the registry id a
// remote client mutates by, while the synthesized primary root worktree (which
// has no registry row) carries none.
func TestBuildLocalRawPopulatesRegistryIDs(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()

	projPath := filepath.Join(t.TempDir(), "app")
	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "app", LocalPath: projPath, DefaultBranch: "main",
	})
	require.NoError(err)
	wtPath := filepath.Join(t.TempDir(), "app-feat")
	wt, err := database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feat", Path: wtPath,
	})
	require.NoError(err)

	srv := &Server{db: database, workspaces: workspace.NewManager(database, t.TempDir())}
	raw, err := srv.buildLocalRaw(ctx)
	require.NoError(err)

	registered := requireRawProjectScoped(t, raw.Projects, "repo:"+normPath(projPath))
	require.Equal(proj.ID, registered.RegistryID,
		"registered project carries its DB id as the registry id")

	primary := requireRawWorktree(t, raw.Worktrees, normPath(projPath))
	require.Empty(primary.RegistryID, "synthesized primary worktree has no registry id")

	feat := requireRawWorktree(t, raw.Worktrees, normPath(wtPath))
	require.Equal(wt.ID, feat.RegistryID,
		"registered worktree carries its DB id as the registry id")
}

// TestAddWorktreeWorkspaceOverlayPreservesRegistryID guards the dedup merge: a
// registered worktree overlaid by a workspace summary (which has no registry
// row) must keep its registry id so a remote client can still mutate it.
func TestAddWorktreeWorkspaceOverlayPreservesRegistryID(t *testing.T) {
	require := require.New(t)
	order := []string{}
	byScopedKey := map[string]*fleet.RawWorktree{}
	prNumber := 5

	addWorktree(&order, byScopedKey, fleet.RawWorktree{
		ScopedKey: "worktree:/tmp/wt", ProjectKey: "repo:/tmp/app",
		Name: "wt", Path: "/tmp/wt", RegistryID: "wtr_keepme",
	})
	addWorktree(&order, byScopedKey, fleet.RawWorktree{
		ScopedKey: "worktree:/tmp/wt", ProjectKey: "repo:/tmp/app",
		Name: "wt", Path: "/tmp/wt", LinkedPRNumber: &prNumber,
	})

	require.Len(order, 1)
	require.Equal("wtr_keepme", byScopedKey["worktree:/tmp/wt"].RegistryID,
		"a workspace overlay must not drop the registered worktree's registry id")
	require.NotNil(byScopedKey["worktree:/tmp/wt"].LinkedPRNumber)
}

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
)

type staticPullCandidateSource struct {
	candidates []db.MergeRequest
	err        error
	calls      int
}

func (s *staticPullCandidateSource) ListOpenPullCandidates(
	context.Context, Workspace,
) ([]db.MergeRequest, error) {
	s.calls++
	return append([]db.MergeRequest(nil), s.candidates...), s.err
}

func setupMonitorRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")

	runWorkspaceTestGit(
		t, dir, "init", "--bare", "--initial-branch=main", remote,
	)
	runWorkspaceTestGit(t, dir, "clone", remote, work)
	runWorkspaceTestGit(
		t, work, "config", "user.email", "test@test.com",
	)
	runWorkspaceTestGit(
		t, work, "config", "user.name", "Test",
	)
	require.NoError(t, os.WriteFile(
		filepath.Join(work, "base.txt"), []byte("base\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "base commit")
	runWorkspaceTestGit(t, work, "push", "origin", "main")

	return work
}

func insertMonitorWorkspace(
	t *testing.T,
	d *db.DB,
	worktreePath string,
	associatedPRNumber *int,
) string {
	t.Helper()
	ws := db.Workspace{
		ID:                 "ws-issue",
		PlatformHost:       "github.com",
		RepoOwner:          "acme",
		RepoName:           "widget",
		ItemType:           db.WorkspaceItemTypeIssue,
		ItemNumber:         7,
		GitHeadRef:         "kenn-forge/issue-7",
		AssociatedPRNumber: associatedPRNumber,
		WorktreePath:       worktreePath,
		TmuxSession:        "kenn-forge-ws-issue",
		Status:             "ready",
	}
	require.NoError(t, d.InsertWorkspace(context.Background(), &ws))
	return ws.ID
}

func insertMonitorWorkspaceWithIdentity(
	t *testing.T,
	d *db.DB,
	id, provider, host, owner, name, worktreePath string,
) string {
	t.Helper()
	require.NoError(t, d.InsertWorkspace(context.Background(), &db.Workspace{
		ID:           id,
		Platform:     provider,
		PlatformHost: host,
		RepoOwner:    owner,
		RepoName:     name,
		ItemType:     db.WorkspaceItemTypeIssue,
		ItemNumber:   7,
		GitHeadRef:   "kenn-forge/issue-7",
		WorktreePath: worktreePath,
		TmuxSession:  "kenn-forge-" + id,
		Status:       "ready",
	}))
	return id
}

func seedIssue(
	t *testing.T, d *db.DB,
	repoID int64, number int, title string,
) {
	t.Helper()
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	_, err := d.UpsertIssue(context.Background(), &db.Issue{
		RepoID:         repoID,
		PlatformID:     repoID*10000 + int64(number),
		Number:         number,
		URL:            "https://github.com/acme/widget/issues/" + strconv.Itoa(number),
		Title:          title,
		Author:         "author",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(t, err)
}

func replaceMonitorRepoRoute(t *testing.T, d *db.DB) int64 {
	t.Helper()
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	identity.PlatformRepoID = "repo-acme-widget-replacement"
	replacement, _, err := d.ReconcileRepositoryObservation(
		t.Context(), identity, time.Now().UTC().Add(time.Hour),
	)
	require.NoError(t, err)
	require.NotNil(t, replacement)
	return replacement.Repository.ID
}

func TestPRMonitorRunOnceSkipsWorkspaceForInactiveRepository(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	seedRepo(t, d, "github.com", "acme", "widget")
	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "feature/replacement")
	insertMonitorWorkspace(t, d, worktreePath, nil)

	replacementID := replaceMonitorRepoRoute(t, d)
	seedMRWithHeadRepo(
		t, d, replacementID, 42,
		"feature/replacement", "https://github.com/acme/widget.git",
	)

	updates, err := NewPRMonitor(d).RunOnce(t.Context())
	require.NoError(err)
	require.Empty(updates)
	workspace, err := d.GetWorkspace(t.Context(), "ws-issue")
	require.NoError(err)
	require.Nil(workspace.AssociatedPRNumber)
}

func TestPRMonitorLegacyWorkspaceSkipsHistoricallyReusedRoute(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	insertMonitorWorkspace(t, database, t.TempDir(), nil)
	workspace, err := database.GetWorkspace(t.Context(), "ws-issue")
	require.NoError(err)
	require.Zero(workspace.RepoID)

	observedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	_, _, err = database.ReconcileRepositoryObservation(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: "repo-original",
		Owner: "acme", Name: "widget",
	}, observedAt)
	require.NoError(err)
	_, _, err = database.ReconcileRepositoryObservation(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: "repo-original",
		Owner: "acme", Name: "moved-away",
	}, observedAt.Add(time.Minute))
	require.NoError(err)
	replacement, _, err := database.ReconcileRepositoryObservation(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: "repo-replacement",
		Owner: "acme", Name: "widget",
	}, observedAt.Add(2*time.Minute))
	require.NoError(err)
	require.NotNil(replacement)
	seedMRWithHeadRepo(
		t, database, replacement.Repository.ID, 42,
		"feature/replacement", "https://github.com/acme/widget.git",
	)

	candidates, err := NewPRMonitor(database).listOpenPullCandidates(t.Context(), workspace)
	require.NoError(err)
	require.Empty(candidates)
}

func TestPRMonitorRetiresWorkspaceWithoutRepositoryIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	workspaceID := insertMonitorWorkspaceWithIdentity(
		t, database, "ws-unresolved", "github", "github.com",
		"acme", "widget", t.TempDir(),
	)
	replacementID := seedRepo(t, database, "github.com", "acme", "widget")
	seedMRWithHeadRepo(
		t, database, replacementID, 42,
		"feature/replacement", "https://github.com/acme/widget.git",
	)
	source := &staticPullCandidateSource{}
	var retired []string
	monitor := NewPRMonitor(database, PRMonitorOptions{
		PullCandidates: source,
		RetireUnresolvedWorkspace: func(_ context.Context, id string) error {
			retired = append(retired, id)
			return nil
		},
	})

	updates, err := monitor.RunOnce(t.Context())

	require.NoError(err)
	assert.Empty(updates)
	assert.Equal([]string{workspaceID}, retired)
	assert.Zero(source.calls)
}

func TestPRMonitorRunOnceUsesUpstreamBranchMatch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Track workspace association")
	seedMRWithHeadRepo(
		t, d, repoID, 42,
		"feature/issue-7", "https://github.com/acme/widget.git",
	)

	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "feature/issue-7")
	require.NoError(os.WriteFile(
		filepath.Join(worktreePath, "feature.txt"), []byte("feature\n"), 0o644,
	))
	runWorkspaceTestGit(t, worktreePath, "add", ".")
	runWorkspaceTestGit(t, worktreePath, "commit", "-m", "feature commit")
	runWorkspaceTestGit(t, worktreePath, "push", "-u", "origin", "feature/issue-7")
	runWorkspaceTestGit(
		t, worktreePath,
		"remote", "set-url", "origin", "git@github.com:acme/widget.git",
	)
	insertMonitorWorkspace(t, d, worktreePath, nil)

	monitor := NewPRMonitor(d)
	updates, err := monitor.RunOnce(ctx)
	require.NoError(err)
	require.Len(updates, 1)
	assert.Equal("ws-issue", updates[0].WorkspaceID)
	assert.Equal(42, updates[0].PRNumber)

	ws, err := d.GetWorkspace(ctx, "ws-issue")
	require.NoError(err)
	require.NotNil(ws)
	require.NotNil(ws.AssociatedPRNumber)
	assert.Equal(42, *ws.AssociatedPRNumber)
}

func TestLaunchSpecMonitorUsesHubCandidatesWithoutProviderItemRows(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	seedRepo(t, database, "github.com", "acme", "widget")
	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "feature/issue-7")
	require.NoError(os.WriteFile(
		filepath.Join(worktreePath, "feature.txt"), []byte("feature\n"), 0o644,
	))
	runWorkspaceTestGit(t, worktreePath, "add", ".")
	runWorkspaceTestGit(t, worktreePath, "commit", "-m", "feature commit")
	runWorkspaceTestGit(t, worktreePath, "push", "-u", "origin", "feature/issue-7")
	runWorkspaceTestGit(
		t, worktreePath, "remote", "set-url", "origin",
		"git@github.com:acme/widget.git",
	)
	workspaceID := insertMonitorWorkspace(t, database, worktreePath, nil)
	workspaceRow, err := database.GetWorkspace(t.Context(), workspaceID)
	require.NoError(err)
	require.NotNil(workspaceRow)
	issuedAt := time.Now().UTC()
	require.NoError(database.PutWorkspaceLaunchSpec(
		t.Context(), workspaceID, WorkspaceLaunchSpec{
			Version: WorkspaceLaunchSpecVersion,
			Repository: WorkspaceLaunchRepository{
				Provider: "github", PlatformHost: "github.com",
				PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget",
				CloneURL: "https://github.com/acme/widget.git", DefaultBranch: "main",
			},
			ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 7,
			ItemKey: "7", GitHeadRef: workspaceRow.GitHeadRef,
			SourceVisible: true, IssuedAt: issuedAt,
			SourceVisibleUntil: issuedAt.Add(WorkspaceLaunchSpecVisibilityLease),
		},
	))
	manager := NewManager(database, t.TempDir())
	source := &staticPullCandidateSource{candidates: []db.MergeRequest{{
		Number: 42, State: db.MergeRequestStateOpen,
		HeadBranch:       "feature/issue-7",
		HeadRepoCloneURL: "https://github.com/acme/widget.git",
	}}}

	monitor := NewPRMonitor(database, PRMonitorOptions{
		LaunchSpecs: manager, PullCandidates: source,
	})
	updates, err := monitor.RunOnce(t.Context())
	require.NoError(err)
	require.Len(updates, 1)
	assert.Equal(workspaceID, updates[0].WorkspaceID)
	assert.Equal(42, updates[0].PRNumber)
	assert.Equal(1, source.calls)
}

func TestPRMonitorRunOnceFallsBackToLocalBranchNameAndHeadSHA(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Track workspace association")
	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "feature/local-only")
	headSHA, err := gitHeadSHA(ctx, worktreePath)
	require.NoError(err)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      repoID*10000 + 42,
		Number:          42,
		Title:           "Test PR",
		Author:          "author",
		State:           "open",
		HeadBranch:      "feature/local-only",
		PlatformHeadSHA: headSHA,
		BaseBranch:      "main",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)
	insertMonitorWorkspace(t, d, worktreePath, nil)

	monitor := NewPRMonitor(d)
	updates, err := monitor.RunOnce(ctx)
	require.NoError(err)
	require.Len(updates, 1)
	assert.Equal("ws-issue", updates[0].WorkspaceID)
	assert.Equal(42, updates[0].PRNumber)

	ws, err := d.GetWorkspace(ctx, "ws-issue")
	require.NoError(err)
	require.NotNil(ws)
	require.NotNil(ws.AssociatedPRNumber)
	assert.Equal(42, *ws.AssociatedPRNumber)
}

func TestPRMonitorRefreshWorkspaceAssociationAssociatesKataTask(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "feature/kata-task")
	headSHA, err := gitHeadSHA(ctx, worktreePath)
	require.NoError(err)
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/kata-task", headSHA, "")
	kataMetadata := db.WorkspaceKataMetadata{
		DaemonID:   "local",
		ProjectUID: "project-1",
		IssueUID:   "issue-1",
	}
	require.NoError(d.InsertWorkspace(ctx, &db.Workspace{
		ID:           "ws-kata",
		Platform:     "github",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypeKataTask,
		ItemKey:      db.KataWorkspaceItemKey(kataMetadata),
		GitHeadRef:   "feature/kata-task",
		WorktreePath: worktreePath,
		TmuxSession:  "kenn-forge-ws-kata",
		Status:       "ready",
		KataMetadata: &kataMetadata,
	}))

	monitor := NewPRMonitor(d)
	update, changed, err := monitor.RefreshWorkspaceAssociation(ctx, "ws-kata")
	require.NoError(err)
	assert.True(changed)
	assert.Equal(PRAssociationUpdate{WorkspaceID: "ws-kata", PRNumber: 42}, update)

	ws, err := d.GetWorkspace(ctx, "ws-kata")
	require.NoError(err)
	require.NotNil(ws)
	require.NotNil(ws.AssociatedPRNumber)
	assert.Equal(42, *ws.AssociatedPRNumber)
}

func TestPRMonitorRunOnceFallsBackToLocalHeadSHAWhenUpstreamRepoMetadataMissing(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "Group/SubGroup",
		Name:           "Project",
		RepoPath:       "Group/SubGroup/Project",
	})
	require.NoError(err)
	seedIssue(t, d, repoID, 7, "Track workspace association")

	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "feature/gitlab")
	require.NoError(os.WriteFile(
		filepath.Join(worktreePath, "feature.txt"), []byte("feature\n"), 0o644,
	))
	runWorkspaceTestGit(t, worktreePath, "add", ".")
	runWorkspaceTestGit(t, worktreePath, "commit", "-m", "feature commit")
	runWorkspaceTestGit(t, worktreePath, "push", "-u", "origin", "feature/gitlab")
	runWorkspaceTestGit(
		t, worktreePath,
		"remote", "set-url", "origin",
		"git@gitlab.com:Group/SubGroup/Project.git",
	)
	headSHA, err := gitHeadSHA(ctx, worktreePath)
	require.NoError(err)
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/gitlab", headSHA, "")
	workspaceID := insertMonitorWorkspaceWithIdentity(
		t, d, "ws-gitlab", "gitlab", "gitlab.com",
		"Group/SubGroup", "Project", worktreePath,
	)

	monitor := NewPRMonitor(d)
	updates, err := monitor.RunOnce(ctx)
	require.NoError(err)
	require.Len(updates, 1)
	assert.Equal(workspaceID, updates[0].WorkspaceID)
	assert.Equal(42, updates[0].PRNumber)

	ws, err := d.GetWorkspace(ctx, workspaceID)
	require.NoError(err)
	require.NotNil(ws)
	require.NotNil(ws.AssociatedPRNumber)
	assert.Equal(42, *ws.AssociatedPRNumber)
}

func TestPRMonitorRunOnceRejectsLocalBranchWithMismatchedHeadSHA(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Track workspace association")
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	_, err := d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      repoID*10000 + 42,
		Number:          42,
		Title:           "Test PR",
		Author:          "author",
		State:           "open",
		HeadBranch:      "feature/local-only",
		PlatformHeadSHA: "different-head",
		BaseBranch:      "main",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "feature/local-only")
	insertMonitorWorkspace(t, d, worktreePath, nil)

	monitor := NewPRMonitor(d)
	updates, err := monitor.RunOnce(ctx)
	require.NoError(err)
	assert.Empty(updates)

	ws, err := d.GetWorkspace(ctx, "ws-issue")
	require.NoError(err)
	require.NotNil(ws)
	assert.Nil(ws.AssociatedPRNumber)
}

func TestPRMonitorRunOnceRejectsLocalBranchWithMismatchedUpstreamRemote(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Track workspace association")
	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "feature/shared")
	require.NoError(os.WriteFile(
		filepath.Join(worktreePath, "feature.txt"), []byte("feature\n"), 0o644,
	))
	runWorkspaceTestGit(t, worktreePath, "add", ".")
	runWorkspaceTestGit(t, worktreePath, "commit", "-m", "feature commit")
	runWorkspaceTestGit(
		t, worktreePath,
		"remote", "set-url", "origin", "git@github.com:acme/widget.git",
	)
	runWorkspaceTestGit(
		t, worktreePath,
		"config", "branch.feature/shared.remote", "origin",
	)
	runWorkspaceTestGit(
		t, worktreePath,
		"config", "branch.feature/shared.merge", "refs/heads/feature/shared",
	)
	headSHA, err := gitHeadSHA(ctx, worktreePath)
	require.NoError(err)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:           repoID,
		PlatformID:       repoID*10000 + 42,
		Number:           42,
		Title:            "Test PR",
		Author:           "author",
		State:            "open",
		HeadBranch:       "feature/shared",
		HeadRepoCloneURL: "https://github.com/fork/widget.git",
		PlatformHeadSHA:  headSHA,
		BaseBranch:       "main",
		CreatedAt:        now,
		UpdatedAt:        now,
		LastActivityAt:   now,
	})
	require.NoError(err)
	insertMonitorWorkspace(t, d, worktreePath, nil)

	monitor := NewPRMonitor(d)
	updates, err := monitor.RunOnce(ctx)
	require.NoError(err)
	assert.Empty(updates)

	ws, err := d.GetWorkspace(ctx, "ws-issue")
	require.NoError(err)
	require.NotNil(ws)
	assert.Nil(ws.AssociatedPRNumber)
}

func TestPRMonitorRunOnceSkipsSyntheticIssueBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Track workspace association")
	seedMR(t, d, repoID, 42, "kenn-forge/issue-7")

	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "kenn-forge/issue-7")
	insertMonitorWorkspace(t, d, worktreePath, nil)

	monitor := NewPRMonitor(d)
	updates, err := monitor.RunOnce(ctx)
	require.NoError(err)
	assert.Empty(updates)

	ws, err := d.GetWorkspace(ctx, "ws-issue")
	require.NoError(err)
	require.NotNil(ws)
	assert.Nil(ws.AssociatedPRNumber)
}

func TestPRMonitorRunOnceAssociatesPRFromManagedIssueBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Track workspace association")
	seedMRWithHeadRepo(
		t, d, repoID, 42,
		"kenn-forge/issue-7", "https://github.com/acme/widget.git",
	)

	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "kenn-forge/issue-7")
	require.NoError(os.WriteFile(
		filepath.Join(worktreePath, "feature.txt"), []byte("feature\n"), 0o644,
	))
	runWorkspaceTestGit(t, worktreePath, "add", ".")
	runWorkspaceTestGit(t, worktreePath, "commit", "-m", "feature commit")
	runWorkspaceTestGit(t, worktreePath, "push", "-u", "origin", "kenn-forge/issue-7")
	runWorkspaceTestGit(
		t, worktreePath,
		"remote", "set-url", "origin", "git@github.com:acme/widget.git",
	)
	insertMonitorWorkspace(t, d, worktreePath, nil)

	monitor := NewPRMonitor(d)
	updates, err := monitor.RunOnce(ctx)
	require.NoError(err)
	require.Len(updates, 1)
	assert.Equal("ws-issue", updates[0].WorkspaceID)
	assert.Equal(42, updates[0].PRNumber)

	ws, err := d.GetWorkspace(ctx, "ws-issue")
	require.NoError(err)
	require.NotNil(ws)
	require.NotNil(ws.AssociatedPRNumber)
	assert.Equal(42, *ws.AssociatedPRNumber)
}

func TestPRMonitorRunOnceAssociatesPRWhenSlugWorkspaceCheckedOutToBareBranch(t *testing.T) {
	// Regression: a slug-style workspace (GitHeadRef = the slugged
	// branch) checked out to the legacy bare-form branch
	// `kenn-forge/issue-<n>` is NOT on its managed branch and should
	// not suppress PR detection. The bare-form fallback only applies
	// to pre-feature workspaces with an empty GitHeadRef.
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Track workspace association")

	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "kenn-forge/issue-7")
	require.NoError(os.WriteFile(
		filepath.Join(worktreePath, "feature.txt"), []byte("feature\n"), 0o644,
	))
	runWorkspaceTestGit(t, worktreePath, "add", ".")
	runWorkspaceTestGit(t, worktreePath, "commit", "-m", "feature commit")
	headSHA, err := gitHeadSHA(ctx, worktreePath)
	require.NoError(err)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      repoID*10000 + 42,
		Number:          42,
		Title:           "Test PR",
		Author:          "author",
		State:           "open",
		HeadBranch:      "kenn-forge/issue-7",
		PlatformHeadSHA: headSHA,
		BaseBranch:      "main",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	require.NoError(d.InsertWorkspace(ctx, &db.Workspace{
		ID:           "ws-issue-slug",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypeIssue,
		ItemNumber:   7,
		GitHeadRef:   "kenn-forge/issue-7-track-workspace-association",
		WorktreePath: worktreePath,
		TmuxSession:  "kenn-forge-ws-issue-slug",
		Status:       "ready",
	}))

	monitor := NewPRMonitor(d)
	updates, err := monitor.RunOnce(ctx)
	require.NoError(err)
	require.Len(updates, 1)
	assert.Equal("ws-issue-slug", updates[0].WorkspaceID)
	assert.Equal(42, updates[0].PRNumber)

	ws, err := d.GetWorkspace(ctx, "ws-issue-slug")
	require.NoError(err)
	require.NotNil(ws)
	require.NotNil(ws.AssociatedPRNumber)
	assert.Equal(42, *ws.AssociatedPRNumber)
}

func TestPRMonitorRunOnceUsesUpstreamRemoteIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Track workspace association")
	seedMRWithHeadRepo(
		t, d, repoID, 41,
		"shared-branch", "https://github.com/Fork-One/Widget.git",
	)
	seedMRWithHeadRepo(
		t, d, repoID, 42,
		"shared-branch", "https://github.com/fork-two/widget.git",
	)

	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "shared-branch")
	runWorkspaceTestGit(
		t, worktreePath,
		"remote", "set-url", "origin", "git@github.com:Fork-Two/Widget.git",
	)
	runWorkspaceTestGit(
		t, worktreePath,
		"config", "branch.shared-branch.remote", "origin",
	)
	runWorkspaceTestGit(
		t, worktreePath,
		"config", "branch.shared-branch.merge", "refs/heads/shared-branch",
	)
	insertMonitorWorkspace(t, d, worktreePath, nil)

	monitor := NewPRMonitor(d)
	updates, err := monitor.RunOnce(ctx)
	require.NoError(err)
	require.Len(updates, 1)
	assert.Equal("ws-issue", updates[0].WorkspaceID)
	assert.Equal(42, updates[0].PRNumber)
}

func TestPRMonitorRunOnceScopesCandidatesByWorkspaceProvider(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	githubRepoID, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "git.example.com",
		PlatformRepoID: "repo-github-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	gitlabRepoID, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "git.example.com",
		PlatformRepoID: "repo-gitlab-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	seedIssue(t, d, gitlabRepoID, 7, "Track workspace association")

	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "feature/provider-scope")
	require.NoError(os.WriteFile(
		filepath.Join(worktreePath, "feature.txt"), []byte("feature\n"), 0o644,
	))
	runWorkspaceTestGit(t, worktreePath, "add", ".")
	runWorkspaceTestGit(t, worktreePath, "commit", "-m", "feature commit")
	headSHA, err := gitHeadSHA(ctx, worktreePath)
	require.NoError(err)
	seedMRWithPlatformHead(
		t, d, githubRepoID, 42, "feature/provider-scope", headSHA, "",
	)
	workspaceID := insertMonitorWorkspaceWithIdentity(
		t, d, "ws-gitlab-provider-scope", "gitlab", "git.example.com",
		"acme", "widget", worktreePath,
	)

	monitor := NewPRMonitor(d)
	updates, err := monitor.RunOnce(ctx)
	require.NoError(err)
	assert.Empty(updates)

	ws, err := d.GetWorkspace(ctx, workspaceID)
	require.NoError(err)
	require.NotNil(ws)
	assert.Nil(ws.AssociatedPRNumber)
}

func TestPRMonitorRefreshWorkspaceAssociationReturnsInspectionError(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Track workspace association")
	workspaceID := insertMonitorWorkspace(
		t, d, filepath.Join(t.TempDir(), "missing-worktree"), nil,
	)

	monitor := NewPRMonitor(d)
	update, changed, err := monitor.RefreshWorkspaceAssociation(ctx, workspaceID)
	require.Error(err)
	require.ErrorContains(err, "detect associated PR")
	assert.False(changed)
	assert.Equal(PRAssociationUpdate{}, update)

	updates, err := monitor.RunOnce(ctx)
	require.NoError(err)
	assert.Empty(updates)
}

func TestSelectPRByUpstream(t *testing.T) {
	assert := assert.New(t)
	candidates := []db.MergeRequest{
		{
			Number:           41,
			HeadBranch:       "shared-branch",
			HeadRepoCloneURL: "https://github.com/fork-one/widget.git",
		},
		{
			Number:           42,
			HeadBranch:       "shared-branch",
			HeadRepoCloneURL: "https://github.com/fork-two/widget.git",
		},
		{
			Number:           43,
			HeadBranch:       "other-branch",
			HeadRepoCloneURL: "https://github.com/fork-two/widget.git",
		},
	}

	number, ok := selectPRByUpstream("github", candidates, upstreamState{
		branchName: "shared-branch",
		remoteURL:  "git@github.com:Fork-Two/Widget.git",
	})
	assert.True(ok)
	assert.Equal(42, number)

	number, ok = selectPRByUpstream("github", []db.MergeRequest{{
		Number:           44,
		HeadBranch:       "shared-branch",
		HeadRepoCloneURL: "https://ghe.example.com/fork-two/widget.git",
	}}, upstreamState{
		branchName: "shared-branch",
		remoteURL:  "git@github.com:Fork-Two/Widget.git",
	})
	assert.False(ok)
	assert.Zero(number)

	for _, upstream := range []upstreamState{
		{branchName: "shared-branch", remoteURL: "not a clone url"},
		{branchName: "missing", remoteURL: "git@github.com:Fork-Two/Widget.git"},
		{branchName: ""},
	} {
		number, ok = selectPRByUpstream("github", candidates, upstream)
		assert.False(ok)
		assert.Zero(number)
	}
}

func TestSelectPRByBranchRejectsAmbiguousMatches(t *testing.T) {
	assert := assert.New(t)
	candidates := []db.MergeRequest{
		{Number: 41, HeadBranch: "shared-local", PlatformHeadSHA: "abc123"},
		{Number: 42, HeadBranch: "shared-local", PlatformHeadSHA: "abc123"},
		{Number: 43, HeadBranch: "single-local", PlatformHeadSHA: "abc123"},
		{Number: 44, HeadBranch: "wrong-head", PlatformHeadSHA: "def456"},
	}

	number, ok := selectPRByLocalBranch(
		"github", candidates, "single-local", "abc123", upstreamState{},
	)
	assert.True(ok)
	assert.Equal(43, number)

	number, ok = selectPRByLocalBranch(
		"github", candidates, "shared-local", "abc123", upstreamState{},
	)
	assert.False(ok)
	assert.Zero(number)

	number, ok = selectPRByLocalBranch(
		"github", candidates, "wrong-head", "abc123", upstreamState{},
	)
	assert.False(ok)
	assert.Zero(number)
}

func TestWorkspacePRMonitorEligible(t *testing.T) {
	existing := 42
	tests := []struct {
		name string
		ws   *db.Workspace
		want bool
	}{
		{
			name: "ready issue workspace without association",
			ws: &db.Workspace{
				ItemType:     db.WorkspaceItemTypeIssue,
				Status:       "ready",
				WorktreePath: "/tmp/work",
			},
			want: true,
		},
		{
			name: "ready kata task workspace without association",
			ws: &db.Workspace{
				ItemType:     db.WorkspaceItemTypeKataTask,
				Status:       "ready",
				WorktreePath: "/tmp/work",
			},
			want: true,
		},
		{
			name: "pull request workspace",
			ws: &db.Workspace{
				ItemType:     db.WorkspaceItemTypePullRequest,
				Status:       "ready",
				WorktreePath: "/tmp/work",
			},
		},
		{
			name: "already associated",
			ws: &db.Workspace{
				ItemType:           db.WorkspaceItemTypeIssue,
				Status:             "ready",
				WorktreePath:       "/tmp/work",
				AssociatedPRNumber: &existing,
			},
		},
		{
			name: "not ready",
			ws: &db.Workspace{
				ItemType:     db.WorkspaceItemTypeIssue,
				Status:       "creating",
				WorktreePath: "/tmp/work",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, workspacePRMonitorEligible(tt.ws))
		})
	}
}

func TestNormalizeCloneRepoIdentity(t *testing.T) {
	assert := assert.New(t)

	assert.Equal(
		"github/github.com/fork/widget",
		normalizeCloneRepoIdentity("github", " git@GitHub.com:Fork/Widget.git "),
	)
	assert.Equal(
		"github/github.com/fork/widget",
		normalizeCloneRepoIdentity("github", "https://token@github.com/Fork/Widget/"),
	)
	assert.Equal(
		"github/github.com/fork/widget",
		normalizeCloneRepoIdentity("github", "ssh://git@github.com:22/Fork/Widget.git"),
	)
	assert.Equal(
		"github/ghe.example.com:8443/fork/widget",
		normalizeCloneRepoIdentity("github", "https://ghe.example.com:8443/Fork/Widget.git"),
	)
	assert.Equal(
		"gitlab/gitlab.com/group/subgroup/project",
		normalizeCloneRepoIdentity("gitlab", "https://gitlab.com/Group/Subgroup/Project.git"),
	)
	assert.Equal(
		"gitlab/gitlab.com/group/subgroup/project",
		normalizeCloneRepoIdentity("gitlab", "git@gitlab.com:Group/Subgroup/Project.git"),
	)
	assert.NotEqual(
		normalizeCloneRepoIdentity("github", "https://forge.example/acme/widget.git"),
		normalizeCloneRepoIdentity("gitlab", "https://forge.example/acme/widget.git"),
	)
	assert.Empty(normalizeCloneRepoIdentity("github", "/tmp/workspace/remote.git"))
	assert.Empty(normalizeCloneRepoIdentity("github", "not a clone url"))
}

func TestNormalizePlatformHostIdentity(t *testing.T) {
	assert := assert.New(t)

	assert.Equal("ghe.example.com:8443", normalizePlatformHostIdentity("GHE.example.com:8443"))
	assert.Equal("ghe.example.com", normalizePlatformHostIdentity("ghe.example.com:443"))
	assert.Equal("ghe.example.com", normalizePlatformHostIdentity("ghe.example.com"))
}

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
)

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
		GitHeadRef:         "middleman/issue-7",
		AssociatedPRNumber: associatedPRNumber,
		WorktreePath:       worktreePath,
		TmuxSession:        "middleman-ws-issue",
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
		GitHeadRef:   "middleman/issue-7",
		WorktreePath: worktreePath,
		TmuxSession:  "middleman-" + id,
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

func TestPRMonitorRunOnceUsesUpstreamBranchMatch(t *testing.T) {
	assert := Assert.New(t)
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

func TestPRMonitorRunOnceFallsBackToLocalBranchNameAndHeadSHA(t *testing.T) {
	assert := Assert.New(t)
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

func TestPRMonitorRunOnceFallsBackToLocalHeadSHAWhenUpstreamRepoMetadataMissing(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "Group/SubGroup",
		Name:         "Project",
		RepoPath:     "Group/SubGroup/Project",
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
	seedMRWithPlatformHead(t, d, repoID, 42, "feature/gitlab", headSHA)
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
	assert := Assert.New(t)
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
	assert := Assert.New(t)
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
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Track workspace association")
	seedMR(t, d, repoID, 42, "middleman/issue-7")

	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "middleman/issue-7")
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
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Track workspace association")
	seedMRWithHeadRepo(
		t, d, repoID, 42,
		"middleman/issue-7", "https://github.com/acme/widget.git",
	)

	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "middleman/issue-7")
	require.NoError(os.WriteFile(
		filepath.Join(worktreePath, "feature.txt"), []byte("feature\n"), 0o644,
	))
	runWorkspaceTestGit(t, worktreePath, "add", ".")
	runWorkspaceTestGit(t, worktreePath, "commit", "-m", "feature commit")
	runWorkspaceTestGit(t, worktreePath, "push", "-u", "origin", "middleman/issue-7")
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
	// `middleman/issue-<n>` is NOT on its managed branch and should
	// not suppress PR detection. The bare-form fallback only applies
	// to pre-feature workspaces with an empty GitHeadRef.
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Track workspace association")

	worktreePath := setupMonitorRepo(t)
	runWorkspaceTestGit(t, worktreePath, "checkout", "-b", "middleman/issue-7")
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
		HeadBranch:      "middleman/issue-7",
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
		GitHeadRef:   "middleman/issue-7-track-workspace-association",
		WorktreePath: worktreePath,
		TmuxSession:  "middleman-ws-issue-slug",
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
	assert := Assert.New(t)
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
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	githubRepoID, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "git.example.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	gitlabRepoID, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "git.example.com",
		Owner:        "acme",
		Name:         "widget",
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
		t, d, githubRepoID, 42, "feature/provider-scope", headSHA,
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
	assert := Assert.New(t)
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
	assert := Assert.New(t)
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

	number, ok := selectPRByUpstream(candidates, upstreamState{
		branchName: "shared-branch",
		remoteURL:  "git@github.com:Fork-Two/Widget.git",
	})
	assert.True(ok)
	assert.Equal(42, number)

	number, ok = selectPRByUpstream([]db.MergeRequest{{
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
		number, ok = selectPRByUpstream(candidates, upstream)
		assert.False(ok)
		assert.Zero(number)
	}
}

func TestSelectPRByBranchRejectsAmbiguousMatches(t *testing.T) {
	assert := Assert.New(t)
	candidates := []db.MergeRequest{
		{Number: 41, HeadBranch: "shared-local", PlatformHeadSHA: "abc123"},
		{Number: 42, HeadBranch: "shared-local", PlatformHeadSHA: "abc123"},
		{Number: 43, HeadBranch: "single-local", PlatformHeadSHA: "abc123"},
		{Number: 44, HeadBranch: "wrong-head", PlatformHeadSHA: "def456"},
	}

	number, ok := selectPRByLocalBranch(
		candidates, "single-local", "abc123", upstreamState{},
	)
	assert.True(ok)
	assert.Equal(43, number)

	number, ok = selectPRByLocalBranch(
		candidates, "shared-local", "abc123", upstreamState{},
	)
	assert.False(ok)
	assert.Zero(number)

	number, ok = selectPRByLocalBranch(
		candidates, "wrong-head", "abc123", upstreamState{},
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
			Assert.Equal(t, tt.want, workspacePRMonitorEligible(tt.ws))
		})
	}
}

func TestNormalizeCloneRepoIdentity(t *testing.T) {
	assert := Assert.New(t)

	assert.Equal(
		"github.com/fork/widget",
		normalizeCloneRepoIdentity(" git@GitHub.com:Fork/Widget.git "),
	)
	assert.Equal(
		"github.com/fork/widget",
		normalizeCloneRepoIdentity("https://token@github.com/Fork/Widget/"),
	)
	assert.Equal(
		"github.com/fork/widget",
		normalizeCloneRepoIdentity("ssh://git@github.com:22/Fork/Widget.git"),
	)
	assert.Equal(
		"ghe.example.com:8443/fork/widget",
		normalizeCloneRepoIdentity("https://ghe.example.com:8443/Fork/Widget.git"),
	)
	assert.Empty(normalizeCloneRepoIdentity("/tmp/workspace/remote.git"))
	assert.Empty(normalizeCloneRepoIdentity("not a clone url"))
}

func TestNormalizePlatformHostIdentity(t *testing.T) {
	assert := Assert.New(t)

	assert.Equal("ghe.example.com:8443", normalizePlatformHostIdentity("GHE.example.com:8443"))
	assert.Equal("ghe.example.com", normalizePlatformHostIdentity("ghe.example.com:443"))
	assert.Equal("ghe.example.com", normalizePlatformHostIdentity("ghe.example.com"))
}

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/ptyowner"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	return dbtest.Open(t)
}

func seedRepo(
	t *testing.T, d *db.DB,
	host, owner, name string,
) int64 {
	t.Helper()
	id, err := d.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity(host, owner, name),
	)
	require.NoError(t, err)
	return id
}

func seedMR(
	t *testing.T, d *db.DB,
	repoID int64, number int, headBranch string,
) {
	t.Helper()
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	mr := &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     repoID*10000 + int64(number),
		Number:         number,
		Title:          "Test PR",
		Author:         "author",
		State:          "open",
		HeadBranch:     headBranch,
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	_, err := d.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
}

func seedMRWithHeadRepo(
	t *testing.T, d *db.DB,
	repoID int64, number int,
	headBranch, cloneURL string,
) {
	t.Helper()
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	mr := &db.MergeRequest{
		RepoID:           repoID,
		PlatformID:       repoID*10000 + int64(number),
		Number:           number,
		Title:            "PR with head repo",
		Author:           "contributor",
		State:            "open",
		HeadBranch:       headBranch,
		BaseBranch:       "main",
		HeadRepoCloneURL: cloneURL,
		CreatedAt:        now,
		UpdatedAt:        now,
		LastActivityAt:   now,
	}
	_, err := d.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
}

func TestCreate(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	wtDir := t.TempDir()

	repoID := seedRepo(
		t, d, "github.com", "acme", "widget",
	)
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, wtDir)

	ws, err := mgr.Create(
		ctx, "github.com", "acme", "widget", 42,
	)
	require.NoError(err)
	require.NotNil(ws)

	assert.NotEmpty(ws.ID)
	assert.Len(ws.ID, 16) // 8 bytes hex-encoded
	assert.Equal("creating", ws.Status)
	assert.Equal("github.com", ws.PlatformHost)
	assert.Equal("acme", ws.RepoOwner)
	assert.Equal("widget", ws.RepoName)
	assert.Equal(db.WorkspaceItemTypePullRequest, ws.ItemType)
	assert.Equal(42, ws.ItemNumber)
	assert.Equal("feature/thing", ws.GitHeadRef)
	assert.Nil(ws.MRHeadRepo)
	assert.Contains(ws.WorktreePath, "pr-42")
	assert.Equal("middleman-"+ws.ID, ws.TmuxSession)

	// Verify persisted in DB.
	got, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal(ws.ID, got.ID)
	assert.Equal("creating", got.Status)
}

func TestCreatePRHeadRepoClassification(t *testing.T) {
	tests := []struct {
		name           string
		platformHost   string
		number         int
		headBranch     string
		headRepoURL    string
		wantMRHeadRepo string
	}{
		{
			name:           "fork PR keeps head repo",
			number:         99,
			headBranch:     "fix/typo",
			headRepoURL:    "https://github.com/contributor/widget.git",
			wantMRHeadRepo: "https://github.com/contributor/widget.git",
		},
		{
			name:        "same-repo PR with populated head repo is not fork",
			number:      244,
			headBranch:  "feature/thing",
			headRepoURL: "git@GitHub.com:Acme/Widget.git",
		},
		{
			name:         "same-repo PR on enterprise host with port is not fork",
			platformHost: "ghe.example.com:8443",
			number:       246,
			headBranch:   "feature/enterprise",
			headRepoURL:  "https://GHE.example.com:8443/Acme/Widget.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := Assert.New(t)
			d := openTestDB(t)
			platformHost := tt.platformHost
			if platformHost == "" {
				platformHost = "github.com"
			}
			repoID := seedRepo(
				t, d, platformHost, "acme", "widget",
			)
			seedMRWithHeadRepo(
				t, d, repoID, tt.number, tt.headBranch, tt.headRepoURL,
			)

			mgr := NewManager(d, t.TempDir())
			ws, err := mgr.Create(
				t.Context(), platformHost, "acme", "widget", tt.number,
			)
			require.NoError(t, err)
			require.NotNil(t, ws)

			if tt.wantMRHeadRepo == "" {
				// Same-repo PRs still have head repo clone URLs in GitHub
				// payloads. Keeping MRHeadRepo nil sends workspace setup down
				// the origin/<branch> path instead of the refs/pull/<number>/head
				// path reserved for forks.
				assert.Nil(ws.MRHeadRepo)
				return
			}
			require.NotNil(t, ws.MRHeadRepo)
			assert.Equal(tt.wantMRHeadRepo, *ws.MRHeadRepo)
		})
	}
}

func TestCreateIssueDefaultBranchSluggified(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		slugStyle bool
		want      string
	}{
		{
			name:      "slug style with usable title",
			title:     "Add foo to bar",
			slugStyle: true,
			want:      "middleman/issue-7-add-foo-to-bar",
		},
		{
			name:      "slug style with empty title falls back to bare",
			title:     "",
			slugStyle: true,
			want:      "middleman/issue-7",
		},
		{
			name:      "slug style with all-punctuation falls back to bare",
			title:     "?!@#",
			slugStyle: true,
			want:      "middleman/issue-7",
		},
		{
			name:      "bare style ignores title",
			title:     "Add foo to bar",
			slugStyle: false,
			want:      "middleman/issue-7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)

			d := openTestDB(t)
			ctx := t.Context()
			repoID := seedRepo(t, d, "github.com", "acme", "widget")
			seedIssue(t, d, repoID, 7, tt.title)

			mgr := NewManager(d, t.TempDir())
			mgr.SetIssueBranchSlugEnabled(tt.slugStyle)

			ws, err := mgr.CreateIssue(
				ctx, "github.com", "acme", "widget", 7,
				CreateIssueOptions{},
			)
			require.NoError(err)
			require.NotNil(ws)

			assert.Equal(tt.want, ws.GitHeadRef)
			assert.Equal(tt.want, ws.WorkspaceBranch)
		})
	}
}

func TestCreateIssueExplicitGitHeadRefBypassesSlug(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	ctx := t.Context()
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Add foo to bar")

	mgr := NewManager(d, t.TempDir())

	ws, err := mgr.CreateIssue(
		ctx, "github.com", "acme", "widget", 7,
		CreateIssueOptions{GitHeadRef: "custom/branch"},
	)
	require.NoError(err)
	require.NotNil(ws)
	assert.Equal("custom/branch", ws.GitHeadRef)
}

func TestCreateRepoNotTracked(t *testing.T) {
	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())

	_, err := mgr.Create(
		t.Context(), "github.com", "unknown", "repo", 1,
	)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrWorkspaceNotFound)
}

func TestCreateDuplicate(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	wtDir := t.TempDir()

	repoID := seedRepo(
		t, d, "github.com", "acme", "widget",
	)
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, wtDir)

	// First create succeeds.
	ws, err := mgr.Create(
		ctx, "github.com", "acme", "widget", 42,
	)
	require.NoError(err)
	require.NotNil(ws)

	// Second create for same MR fails with unique constraint.
	_, err = mgr.Create(
		ctx, "github.com", "acme", "widget", 42,
	)
	require.Error(err)
	require.ErrorIs(err, ErrWorkspaceDuplicate)
}

func TestCreateMRNotSynced(t *testing.T) {
	d := openTestDB(t)

	seedRepo(t, d, "github.com", "acme", "widget")

	mgr := NewManager(d, t.TempDir())

	_, err := mgr.Create(
		t.Context(), "github.com", "acme", "widget", 999,
	)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrWorkspaceNotSynced)
}

func TestSetupFailurePersistsStatusWhenContextCanceled(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	repoID := seedRepo(
		t, d, "github.com", "acme", "widget",
	)
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, wtDir)
	ws, err := mgr.Create(
		t.Context(), "github.com", "acme", "widget", 42,
	)
	require.NoError(err)
	require.NotNil(ws)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = mgr.Setup(ctx, ws)
	require.Error(err)
	require.Contains(err.Error(), "clone manager not set")

	got, err := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("error", got.Status)
	require.NotNil(got.ErrorMessage)
	assert.Contains(*got.ErrorMessage, "clone manager not set")

	events, err := d.ListWorkspaceSetupEvents(
		t.Context(), ws.ID,
	)
	require.NoError(err)
	require.Len(events, 2)
	assert.Equal("setup", events[0].Stage)
	assert.Equal("started", events[0].Outcome)
	assert.Equal("clone", events[1].Stage)
	assert.Equal("failure", events[1].Outcome)
	assert.Contains(events[1].Message, "clone manager not set")
}

func TestFailSetupUsesSinglePersistenceBudget(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	repoID := seedRepo(
		t, d, "github.com", "acme", "widget",
	)
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, wtDir)
	ws, err := mgr.Create(
		t.Context(), "github.com", "acme", "widget", 42,
	)
	require.NoError(err)
	require.NotNil(ws)

	origTimeout := workspacePersistTimeout
	workspacePersistTimeout = 200 * time.Millisecond
	t.Cleanup(func() { workspacePersistTimeout = origTimeout })

	tx, err := d.WriteDB().BeginTx(t.Context(), nil)
	require.NoError(err)
	t.Cleanup(func() { _ = tx.Rollback() })

	start := time.Now()
	err = mgr.failSetup(
		t.Context(),
		ws.ID, workspaceSetupStageClone,
		errors.New("forced persistence timeout"),
	)
	elapsed := time.Since(start)

	require.Error(err)
	assert.Contains(err.Error(), "forced persistence timeout")
	assert.Less(
		elapsed,
		workspacePersistTimeout+(workspacePersistTimeout/2),
	)
}

func TestFailSetupRespectsParentDeadline(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	repoID := seedRepo(
		t, d, "github.com", "acme", "widget",
	)
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, wtDir)
	ws, err := mgr.Create(
		t.Context(), "github.com", "acme", "widget", 42,
	)
	require.NoError(err)
	require.NotNil(ws)

	origTimeout := workspacePersistTimeout
	workspacePersistTimeout = time.Second
	t.Cleanup(func() { workspacePersistTimeout = origTimeout })

	tx, err := d.WriteDB().BeginTx(t.Context(), nil)
	require.NoError(err)
	t.Cleanup(func() { _ = tx.Rollback() })

	parent, cancel := context.WithTimeout(
		t.Context(), 100*time.Millisecond,
	)
	defer cancel()

	start := time.Now()
	err = mgr.failSetup(
		parent,
		ws.ID, workspaceSetupStageClone,
		errors.New("forced persistence timeout"),
	)
	elapsed := time.Since(start)

	require.Error(err)
	assert.Contains(err.Error(), "forced persistence timeout")
	assert.Less(elapsed, 300*time.Millisecond)
}

func TestAddPreferredWorktreeRejectsUnsafeBranchName(t *testing.T) {
	require := require.New(t)

	cloneDir := setupBareCloneForWorkspaceGitTest(t)
	mgr := NewManager(openTestDB(t), t.TempDir())
	ws := &Workspace{
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   42,
		GitHeadRef:   "-unsafe",
		WorktreePath: filepath.Join(t.TempDir(), "worktree"),
	}

	_, err := mgr.addPreferredWorktree(
		t.Context(), cloneDir, ws,
	)
	require.Error(err)
	require.Contains(err.Error(), "invalid branch name")
}

func TestAddPreferredWorktreeHeadRepoRouting(t *testing.T) {
	type worktreeExpectation struct {
		headSHA  string
		remote   string
		mergeRef string
	}

	tests := []struct {
		name        string
		number      int
		headBranch  string
		headRepoURL string
		configure   func(*testing.T, string, string, int) worktreeExpectation
	}{
		{
			name:        "same-repo PR tracks real remote branch",
			number:      244,
			headBranch:  "feature/thing",
			headRepoURL: "https://github.com/acme/widget.git",
			configure: func(
				t *testing.T, cloneDir, branch string, prNumber int,
			) worktreeExpectation {
				// Reproduce the dangerous repo state from issue #256: the real
				// branch and GitHub's synthetic pull ref both exist and point at
				// the same commit. Starting from refs/pull/<number>/head lets Git
				// auto-configure that synthetic ref as the upstream, which breaks
				// tools that inspect @{u}.
				sha := configureSameRepoPRRefs(
					t, cloneDir, branch, prNumber,
				)
				return worktreeExpectation{
					headSHA:  sha,
					remote:   "origin",
					mergeRef: "refs/heads/" + branch,
				}
			},
		},
		{
			name:        "fork PR prefers pull ref over same-named origin branch",
			number:      245,
			headBranch:  "fork/thing",
			headRepoURL: "https://github.com/contributor/widget.git",
			configure: func(
				t *testing.T, cloneDir, branch string, prNumber int,
			) worktreeExpectation {
				// A base repo can have a branch with the same name as a fork PR
				// branch, but that origin branch is not the fork head. Fork
				// workspaces must prefer the GitHub pull ref over any same-named
				// origin branch.
				originSHA, pullSHA := configureForkPRRefs(
					t, cloneDir, branch, prNumber,
				)
				gotOriginSHA, exists, err := gitRefSHA(
					t.Context(), cloneDir, "refs/remotes/origin/"+branch,
				)
				require.NoError(t, err)
				require.True(t, exists)
				require.NotEqual(t, originSHA, pullSHA)
				require.Equal(t, originSHA, gotOriginSHA)
				return worktreeExpectation{headSHA: pullSHA}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)
			cloneDir := setupBareCloneForWorkspaceGitTest(t)
			want := tt.configure(t, cloneDir, tt.headBranch, tt.number)

			d := openTestDB(t)
			repoID := seedRepo(t, d, "github.com", "acme", "widget")
			seedMRWithHeadRepo(
				t, d, repoID, tt.number, tt.headBranch, tt.headRepoURL,
			)
			mgr := NewManager(d, t.TempDir())
			ws, err := mgr.Create(
				t.Context(), "github.com", "acme", "widget", tt.number,
			)
			require.NoError(err)

			branch, err := mgr.addPreferredWorktree(t.Context(), cloneDir, ws)
			require.NoError(err)
			assert.Equal(tt.headBranch, branch)

			headSHA, err := gitHeadSHA(t.Context(), ws.WorktreePath)
			require.NoError(err)
			assert.Equal(want.headSHA, headSHA)

			if want.remote == "" && want.mergeRef == "" {
				return
			}
			remote, err := gitConfigValue(
				t.Context(), ws.WorktreePath,
				"branch."+tt.headBranch+".remote",
			)
			require.NoError(err)
			mergeRef, err := gitConfigValue(
				t.Context(), ws.WorktreePath,
				"branch."+tt.headBranch+".merge",
			)
			require.NoError(err)
			assert.Equal(want.remote, remote)
			assert.Equal(want.mergeRef, mergeRef)
		})
	}
}

func TestRollbackWorktreeDeletesBranchWhenContextCanceled(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	cloneDir := setupBareCloneForWorkspaceGitTest(t)
	branch := syntheticPRWorktreeBranch(42)
	require.NoError(runGit(
		t.Context(), cloneDir,
		"branch", branch, "main",
	))

	ws := &Workspace{
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   42,
		WorktreePath: filepath.Join(t.TempDir(), "missing-worktree"),
	}
	mgr := NewManager(openTestDB(t), t.TempDir())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	mgr.rollbackWorktree(ctx, cloneDir, ws, workspaceBranchUnknown)

	_, exists, err := gitRefSHA(
		t.Context(), cloneDir, "refs/heads/"+branch,
	)
	require.NoError(err)
	assert.False(exists)
}

func TestLocalBranchExistsIgnoresInheritedGitEnv(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	targetClone := setupBareCloneForWorkspaceGitTest(t)
	poisonClone := setupBareCloneForWorkspaceGitTest(t)
	require.NoError(runGit(
		context.Background(), poisonClone,
		"branch", "middleman/issue-7", "main",
	))

	t.Setenv("GIT_DIR", poisonClone)
	t.Setenv("GIT_WORK_TREE", t.TempDir())

	exists, err := localBranchExists(
		context.Background(), targetClone, "middleman/issue-7",
	)

	require.NoError(err)
	assert.False(exists)
}

func TestCleanupContextRespectsParentDeadline(t *testing.T) {
	require := require.New(t)

	parent, cancel := context.WithTimeout(
		t.Context(), 100*time.Millisecond,
	)
	defer cancel()

	cleanupCtx, cleanupCancel := cleanupContext(parent)
	defer cleanupCancel()

	deadline, ok := cleanupCtx.Deadline()
	require.True(ok)

	remaining := time.Until(deadline)
	require.LessOrEqual(remaining, 100*time.Millisecond)
	require.Greater(remaining, 0*time.Millisecond)
}

func setupBareCloneForWorkspaceGitTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	cloneDir := filepath.Join(dir, "clone.git")

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
	runWorkspaceTestGit(t, dir, "clone", "--bare", remote, cloneDir)

	return cloneDir
}

func configureSameRepoPRRefs(
	t *testing.T, cloneDir, branch string, prNumber int,
) string {
	t.Helper()
	out, err := gitOutput(t.Context(), cloneDir, "rev-parse", "main")
	require.NoError(t, err)
	sha := strings.TrimSpace(out)
	require.NotEmpty(t, sha)
	runWorkspaceTestGit(
		t, cloneDir, "update-ref", "refs/remotes/origin/"+branch, sha,
	)
	runWorkspaceTestGit(
		t, cloneDir, "update-ref",
		fmt.Sprintf("refs/pull/%d/head", prNumber), sha,
	)
	return sha
}

func configureForkPRRefs(
	t *testing.T, cloneDir, branch string, prNumber int,
) (originSHA, pullSHA string) {
	t.Helper()
	out, err := gitOutput(t.Context(), cloneDir, "rev-parse", "main")
	require.NoError(t, err)
	originSHA = strings.TrimSpace(out)
	require.NotEmpty(t, originSHA)
	treeOut, err := gitOutput(t.Context(), cloneDir, "rev-parse", "main^{tree}")
	require.NoError(t, err)
	runWorkspaceTestGit(t, cloneDir, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, cloneDir, "config", "user.name", "Test")
	commitOut, err := gitOutput(
		t.Context(), cloneDir,
		"commit-tree", strings.TrimSpace(treeOut),
		"-p", originSHA, "-m", "fork head",
	)
	require.NoError(t, err)
	pullSHA = strings.TrimSpace(commitOut)
	require.NotEmpty(t, pullSHA)
	runWorkspaceTestGit(
		t, cloneDir, "update-ref", "refs/remotes/origin/"+branch, originSHA,
	)
	runWorkspaceTestGit(
		t, cloneDir, "update-ref",
		fmt.Sprintf("refs/pull/%d/head", prNumber), pullSHA,
	)
	return originSHA, pullSHA
}

func runWorkspaceTestGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v failed: %s%s", args, out, stderr)
	return out
}

func TestShellFromPasswdLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			"normal zsh",
			"wesm:x:501:20:Wes McKinney:/Users/wesm:/bin/zsh",
			"/bin/zsh",
		},
		{
			"normal bash",
			"dev:x:1000:1000::/home/dev:/bin/bash",
			"/bin/bash",
		},
		{
			"nologin filtered",
			"nobody:x:65534:65534:Nobody:/nonexistent:/sbin/nologin",
			"",
		},
		{
			"false filtered",
			"git:x:998:998::/home/git:/usr/bin/false",
			"",
		},
		{
			"bin/false filtered",
			"svc:x:999:999::/srv:/bin/false",
			"",
		},
		{
			"empty shell",
			"user:x:1000:1000::/home/user:",
			"",
		},
		{
			"too few fields",
			"broken:line",
			"",
		},
		{
			"empty line",
			"",
			"",
		},
		{
			"trailing whitespace",
			"user:x:1000:1000::/home/user:/bin/zsh\n",
			"/bin/zsh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellFromPasswdLine(tt.line)
			require.Equal(t, tt.want, got)
		})
	}
}

// writeRecorderScript creates an executable shell script at a
// fresh path under t.TempDir() that appends the count and each
// argument, NUL-delimited, to TMUX_RECORD. Returns the script path
// and the record file path.
func writeRecorderScript(t *testing.T) (scriptPath, recordPath string) {
	t.Helper()
	dir := t.TempDir()
	recordPath = filepath.Join(dir, "record")
	scriptPath = filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(scriptPath, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", recordPath)
	return scriptPath, recordPath
}

// readRecorderArgv reads the NUL-delimited record file and returns
// each recorded invocation as a []string. Each invocation is stored
// as "<argc>\0<arg0>\0<arg1>...\0", so this reads argc then slurps
// that many args per invocation.
func readRecorderArgv(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	// Split on NUL. Each record is "<argc>\0<arg0>\0<arg1>\0...\0",
	// so the flushed stream always ends with a trailing \0 and Split
	// produces a final empty element after it. Strip exactly one
	// trailing empty so we don't mistake it for part of the next
	// record. Interior empty elements are real args (the NUL framing
	// exists to preserve them) and must NOT be skipped.
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	var out [][]string
	for i := 0; i < len(parts); {
		n, err := strconv.Atoi(parts[i])
		require.NoError(t, err)
		i++
		argv := parts[i : i+n]
		for j := range argv {
			argv[j] = normalizeRecordedTmuxArg(argv[j])
		}
		out = append(out, argv)
		i += n
	}
	return out
}

func normalizeRecordedTmuxArg(arg string) string {
	if runtime.GOOS != "windows" {
		return arg
	}
	switch arg {
	case "#session_name":
		return "#{session_name}"
	case "#pane_title":
		return "#{pane_title}"
	default:
		return arg
	}
}

func TestManagerEnsureTmuxHasSessionPrefix(t *testing.T) {
	assert := Assert.New(t)

	script, record := writeRecorderScript(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})

	// Script exits 0 for every invocation, so EnsureTmux observes
	// "session exists" after the has-session call and returns
	// without running new-session.
	require.NoError(t, mgr.EnsureTmux(t.Context(), "sess-A", t.TempDir()))

	argvs := readRecorderArgv(t, record)
	require.Len(t, argvs, 1)
	assert.Equal(
		[]string{"wrap", "has-session", "-t", "sess-A"},
		argvs[0],
	)
}

func TestManagerEnsureTerminalUsesPtyOwnerWhenConfigured(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	script, record := writeRecorderScript(t)
	owner := &fakePtyOwnerClient{}
	mgr := NewManager(openTestDB(t), t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	mgr.SetPtyOwnerClient(owner)

	require.NoError(mgr.EnsureTerminal(t.Context(), &db.Workspace{
		TmuxSession:     "sess-owner",
		WorktreePath:    "/tmp/ws",
		TerminalBackend: TerminalBackendPtyOwner,
	}))

	assert.Equal([]fakePtyOwnerCall{{
		Op: "ensure", Session: "sess-owner", Cwd: "/tmp/ws",
	}}, owner.Calls)
	_, err := os.Stat(record)
	assert.True(os.IsNotExist(err))
}

func TestManagerTerminalPaneSnapshotIncludesPtyOwnerTitle(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	owner := &fakePtyOwnerClient{
		SnapshotOutput: []byte("recent output"),
		SnapshotTitle:  "⠴ t3code-b5014b03",
	}
	mgr := NewManager(nil, t.TempDir())
	mgr.SetPtyOwnerClient(owner)
	ws := &db.Workspace{
		ID:              "ws-1",
		TmuxSession:     "middleman-ws-1",
		TerminalBackend: TerminalBackendPtyOwner,
	}

	snapshot, err := mgr.TerminalPaneSnapshot(
		context.Background(), ws, ws.TmuxSession,
	)

	require.NoError(err)
	assert.Equal("⠴ t3code-b5014b03", snapshot.Title)
	assert.Equal("recent output", snapshot.Output)
}

func TestManagerCleanupTerminalUsesPtyOwnerForBaseSession(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	script, record := writeRecorderScript(t)
	owner := &fakePtyOwnerClient{}
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	mgr.SetPtyOwnerClient(owner)

	ws, err := mgr.Create(t.Context(), "github.com", "acme", "widget", 42)
	require.NoError(err)
	_, err = mgr.Delete(t.Context(), ws.ID, true, nil)
	require.NoError(err)

	assert.Equal([]fakePtyOwnerCall{{
		Op: "stop", Session: ws.TmuxSession,
	}}, owner.Calls)
	_, err = os.Stat(record)
	assert.True(os.IsNotExist(err))
}

func TestManagerCleanupPtyOwnerWorkspaceStopsStoredRuntimeTmuxSessions(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	script, record := writeRecorderScript(t)
	owner := &fakePtyOwnerClient{}
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	mgr.SetPtyOwnerClient(owner)

	ws, err := mgr.Create(t.Context(), "github.com", "acme", "widget", 42)
	require.NoError(err)
	require.NoError(mgr.RecordRuntimeTmuxSession(
		t.Context(), ws.ID, "middleman-runtime-session", "agent-1",
		time.Date(2026, 4, 29, 1, 0, 0, 0, time.UTC),
	))

	_, err = mgr.Delete(t.Context(), ws.ID, true, nil)
	require.NoError(err)

	assert.Equal([]fakePtyOwnerCall{{
		Op: "stop", Session: ws.TmuxSession,
	}}, owner.Calls)
	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 1)
	assert.Equal(
		[]string{"wrap", "kill-session", "-t", "middleman-runtime-session"},
		argvs[0],
	)
	stored, err := d.ListWorkspaceTmuxSessions(t.Context(), ws.ID)
	require.NoError(err)
	assert.Empty(stored)
}

func TestManagerDeleteUsesTmuxPrefix(t *testing.T) {
	assert := Assert.New(t)

	script, record := writeRecorderScript(t)

	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})

	ctx := t.Context()
	ws, err := mgr.Create(ctx, "github.com", "acme", "widget", 42)
	require.NoError(t, err)

	// force=true skips the dirty-files check. m.clones is nil, so
	// Delete takes the clones==nil short-circuit after killing the
	// tmux session — no git operations are required.
	_, err = mgr.Delete(ctx, ws.ID, true, nil)
	require.NoError(t, err)

	// Delete invokes exactly one tmux command on this path
	// (kill-session). It ignores the exit code because the session
	// may not exist, but our script exits 0 so the invocation is
	// still recorded.
	argvs := readRecorderArgv(t, record)
	require.Len(t, argvs, 1)
	assert.Equal(
		[]string{"wrap", "kill-session", "-t", ws.TmuxSession},
		argvs[0],
	)
}

func TestManagerDeleteAllowsMissingTmuxSession(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    echo "can't find session: missing" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})

	ctx := context.Background()
	ws, err := mgr.Create(ctx, "github.com", "acme", "widget", 42)
	require.NoError(err)

	dirty, err := mgr.Delete(ctx, ws.ID, true, nil)
	require.NoError(err)
	assert.Nil(dirty)

	got, err := mgr.Get(ctx, ws.ID)
	require.NoError(err)
	assert.Nil(got)

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 1)
	assert.Equal(
		[]string{"wrap", "kill-session", "-t", ws.TmuxSession},
		argvs[0],
	)
}

func TestManagerDeleteFailsWhenTmuxKillFails(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    echo "permission denied" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})

	ctx := context.Background()
	ws, err := mgr.Create(ctx, "github.com", "acme", "widget", 42)
	require.NoError(err)
	require.NoError(d.UpdateWorkspaceStatus(ctx, ws.ID, "ready", nil))

	dirty, err := mgr.Delete(ctx, ws.ID, true, nil)
	assert.Nil(dirty)
	require.Error(err)
	assert.Contains(err.Error(), "kill tmux session")
	assert.Contains(err.Error(), "permission denied")

	got, getErr := mgr.Get(ctx, ws.ID)
	require.NoError(getErr)
	require.NotNil(got)
	assert.Equal(ws.ID, got.ID)

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 1)
	assert.Equal(
		[]string{"wrap", "kill-session", "-t", ws.TmuxSession},
		argvs[0],
	)
}

func TestManagerDeleteTreatsTmuxServerExitDuringKillAsGone(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    echo "server exited unexpectedly" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})

	ctx := context.Background()
	ws, err := mgr.Create(ctx, "github.com", "acme", "widget", 42)
	require.NoError(err)
	require.NoError(d.UpdateWorkspaceStatus(ctx, ws.ID, "ready", nil))

	dirty, err := mgr.Delete(ctx, ws.ID, true, nil)
	assert.Nil(dirty)
	require.NoError(err)

	got, getErr := mgr.Get(ctx, ws.ID)
	require.NoError(getErr)
	assert.Nil(got)

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 1)
	assert.Equal(
		[]string{"wrap", "kill-session", "-t", ws.TmuxSession},
		argvs[0],
	)
}

func TestManagerDeleteAllowsErroredWorkspaceWhenTmuxUnavailable(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{
		filepath.Join(t.TempDir(), "missing-tmux"),
	})

	ctx := context.Background()
	ws := &Workspace{
		ID:              "ws-tmux-unavailable",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/thing",
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath:    filepath.Join(t.TempDir(), "worktree"),
		TmuxSession:     "middleman-0000000000000042",
		Status:          "error",
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	dirty, err := mgr.Delete(ctx, ws.ID, true, nil)
	require.NoError(err)
	assert.Nil(dirty)

	got, err := mgr.Get(ctx, ws.ID)
	require.NoError(err)
	assert.Nil(got)
}

func TestManagerReapOrphanTmuxSessionsIgnoresUnavailableTmux(t *testing.T) {
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{filepath.Join(t.TempDir(), "missing-tmux")})

	require.NoError(mgr.ReapOrphanTmuxSessions(context.Background()))
}

func TestManagerReapOrphanTmuxSessionsKillsUnknownManagedSessions(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    printf 'middleman-0000000000000001\nmiddleman-ffffffffffffffff\nmiddleman-aaaaaaaaaaaaaaaa-0123456789abcdef\nmiddleman-aaaaaaaaaaaaaaaa-claude\nmiddleman-notes\nother-session\n'` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "show-options" ]; then` + "\n" +
		`    if [ "$5" = "middleman-aaaaaaaaaaaaaaaa-0123456789abcdef" ] || [ "$5" = "middleman-aaaaaaaaaaaaaaaa-claude" ]; then` + "\n" +
		`      printf '%s\n' "$MIDDLEMAN_TMUX_OWNER"` + "\n" +
		`      exit 0` + "\n" +
		`    fi` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	t.Setenv("MIDDLEMAN_TMUX_OWNER", mgr.tmuxOwnerMarker())

	live := &Workspace{
		ID:           "ws-live",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
		TmuxSession:  "middleman-0000000000000001",
		Status:       "ready",
	}
	require.NoError(d.InsertWorkspace(context.Background(), live))

	require.NoError(mgr.ReapOrphanTmuxSessions(context.Background()))

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 4)
	assert.Equal(
		[]string{"wrap", "list-sessions", "-F", "#{session_name}"},
		argvs[0],
	)
	assert.Equal(
		[]string{
			"wrap", "show-options", "-qv", "-t",
			"middleman-ffffffffffffffff", "@middleman_owner",
		},
		argvs[1],
	)
	assert.Equal(
		[]string{
			"wrap", "show-options", "-qv", "-t",
			"middleman-aaaaaaaaaaaaaaaa-0123456789abcdef",
			"@middleman_owner",
		},
		argvs[2],
	)
	assert.Equal(
		[]string{
			"wrap", "kill-session", "-t",
			"middleman-aaaaaaaaaaaaaaaa-0123456789abcdef",
		},
		argvs[3],
	)
	assert.NotContains(argvs, []string{
		"wrap", "show-options", "-qv", "-t",
		"middleman-aaaaaaaaaaaaaaaa-claude", "@middleman_owner",
	})
	assert.NotContains(argvs, []string{
		"wrap", "kill-session", "-t",
		"middleman-aaaaaaaaaaaaaaaa-claude",
	})
}

func TestManagerReapOrphanTmuxSessionsKeepsStoredRuntimeSessions(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    printf 'middleman-0000000000000001\nmiddleman-0000000000000001-57de4cf40144bdf7\nmiddleman-aaaaaaaaaaaaaaaa-c857d09db23e6822\n'` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "show-options" ]; then` + "\n" +
		`    printf '%s\n' "$MIDDLEMAN_TMUX_OWNER"` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	t.Setenv("MIDDLEMAN_TMUX_OWNER", mgr.tmuxOwnerMarker())

	require.NoError(d.InsertWorkspace(context.Background(), &Workspace{
		ID:           "0000000000000001",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
		TmuxSession:  "middleman-0000000000000001",
		Status:       "ready",
	}))
	require.NoError(d.UpsertWorkspaceTmuxSession(
		context.Background(),
		&db.WorkspaceTmuxSession{
			WorkspaceID: "0000000000000001",
			SessionName: "middleman-0000000000000001-57de4cf40144bdf7",
			TargetKey:   "codex",
		},
	))

	require.NoError(mgr.ReapOrphanTmuxSessions(context.Background()))

	argvs := readRecorderArgv(t, record)
	assert.Contains(argvs, []string{
		"wrap", "kill-session", "-t",
		"middleman-aaaaaaaaaaaaaaaa-c857d09db23e6822",
	})
	assert.NotContains(argvs, []string{
		"wrap", "kill-session", "-t",
		"middleman-0000000000000001-57de4cf40144bdf7",
	})
}

func TestManagerPruneMissingTmuxSessionsRemovesStaleRecords(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    printf 'middleman-0000000000000001\nmiddleman-0000000000000001-57de4cf40144bdf7\n'` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	mgr.SetPtyOwnerFallbackClient(&fakePtyOwnerClient{
		StateSessions: map[string]bool{
			"middleman-0000000000000003": true,
		},
	})
	ctx := context.Background()

	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "0000000000000001",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
		TmuxSession:  "middleman-0000000000000001",
		Status:       "ready",
	}))
	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "0000000000000002",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "gadget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   2,
		GitHeadRef:   "feature/stale",
		WorktreePath: filepath.Join(t.TempDir(), "stale"),
		TmuxSession:  "middleman-0000000000000002",
		Status:       "ready",
	}))
	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "0000000000000003",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "legacy-owner",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   3,
		GitHeadRef:   "feature/owner",
		WorktreePath: filepath.Join(t.TempDir(), "owner"),
		TmuxSession:  "middleman-0000000000000003",
		Status:       "ready",
	}))
	_, err := d.WriteDB().ExecContext(
		ctx,
		`UPDATE middleman_workspaces SET terminal_backend = '' WHERE id = ?`,
		"0000000000000003",
	)
	require.NoError(err)
	require.NoError(d.UpsertWorkspaceTmuxSession(
		ctx,
		&db.WorkspaceTmuxSession{
			WorkspaceID: "0000000000000001",
			SessionName: "middleman-0000000000000001-57de4cf40144bdf7",
			TargetKey:   "codex",
		},
	))
	require.NoError(d.UpsertWorkspaceTmuxSession(
		ctx,
		&db.WorkspaceTmuxSession{
			WorkspaceID: "0000000000000001",
			SessionName: "middleman-0000000000000001-c857d09db23e6822",
			TargetKey:   "claude",
		},
	))

	require.NoError(mgr.PruneMissingTmuxSessions(ctx))

	stored, err := d.ListWorkspaceTmuxSessions(ctx, "0000000000000001")
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal(
		"middleman-0000000000000001-57de4cf40144bdf7",
		stored[0].SessionName,
	)

	live, err := d.GetWorkspace(ctx, "0000000000000001")
	require.NoError(err)
	require.NotNil(live)
	assert.Equal("ready", live.Status)

	stale, err := d.GetWorkspace(ctx, "0000000000000002")
	require.NoError(err)
	require.NotNil(stale)
	assert.Equal("error", stale.Status)
	require.NotNil(stale.ErrorMessage)
	assert.Contains(*stale.ErrorMessage, "tmux session is no longer running")
	assert.Contains(*stale.ErrorMessage, "middleman-0000000000000002")

	legacyOwner, err := d.GetWorkspace(ctx, "0000000000000003")
	require.NoError(err)
	require.NotNil(legacyOwner)
	assert.Equal("ready", legacyOwner.Status)
}

func TestManagerTmuxSessionsForWorkspaceReadsStoredRuntimeSessions(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	require.NoError(d.InsertWorkspace(context.Background(), &Workspace{
		ID:           "0000000000000001",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
		TmuxSession:  "middleman-0000000000000001",
		Status:       "ready",
	}))
	require.NoError(d.UpsertWorkspaceTmuxSession(
		context.Background(),
		&db.WorkspaceTmuxSession{
			WorkspaceID: "0000000000000001",
			SessionName: "middleman-0000000000000001-57de4cf40144bdf7",
			TargetKey:   "codex",
		},
	))
	require.NoError(d.UpsertWorkspaceTmuxSession(
		context.Background(),
		&db.WorkspaceTmuxSession{
			WorkspaceID: "0000000000000001",
			SessionName: "middleman-0000000000000001-c857d09db23e6822",
			TargetKey:   "claude",
		},
	))
	require.NoError(d.InsertWorkspace(context.Background(), &Workspace{
		ID:           "0000000000000002",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "gadget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   2,
		GitHeadRef:   "feature/other",
		WorktreePath: filepath.Join(t.TempDir(), "other"),
		TmuxSession:  "middleman-0000000000000002",
		Status:       "ready",
	}))
	require.NoError(d.UpsertWorkspaceTmuxSession(
		context.Background(),
		&db.WorkspaceTmuxSession{
			WorkspaceID: "0000000000000002",
			SessionName: "middleman-0000000000000002-57de4cf40144bdf7",
			TargetKey:   "codex",
		},
	))

	sessions, err := mgr.TmuxSessionsForWorkspace(
		context.Background(),
		"0000000000000001",
		"middleman-0000000000000001",
	)
	require.NoError(err)

	assert.Equal([]string{
		"middleman-0000000000000001",
		"middleman-0000000000000001-c857d09db23e6822",
		"middleman-0000000000000001-57de4cf40144bdf7",
	}, sessions)

	sessions, err = mgr.TmuxSessionsForWorkspace(
		context.Background(),
		"0000000000000001",
		"",
	)
	require.NoError(err)
	assert.Equal([]string{
		"middleman-0000000000000001-c857d09db23e6822",
		"middleman-0000000000000001-57de4cf40144bdf7",
	}, sessions)
}

func TestManagerCleanupTmuxSessionKillsRuntimeSessionsForWorkspace(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})
	ws := &Workspace{
		ID:           "0000000000000001",
		TmuxSession:  "middleman-0000000000000001",
		Status:       "ready",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
	}
	require.NoError(d.InsertWorkspace(context.Background(), ws))
	require.NoError(d.UpsertWorkspaceTmuxSession(
		context.Background(),
		&db.WorkspaceTmuxSession{
			WorkspaceID: ws.ID,
			SessionName: "middleman-0000000000000001-57de4cf40144bdf7",
			TargetKey:   "codex",
		},
	))
	require.NoError(d.UpsertWorkspaceTmuxSession(
		context.Background(),
		&db.WorkspaceTmuxSession{
			WorkspaceID: ws.ID,
			SessionName: "middleman-0000000000000001-c857d09db23e6822",
			TargetKey:   "claude",
		},
	))

	require.NoError(mgr.cleanupTmuxSession(context.Background(), ws))

	argvs := readRecorderArgv(t, record)
	assert.Contains(argvs, []string{
		"kill-session", "-t", "middleman-0000000000000001",
	})
	assert.Contains(argvs, []string{
		"kill-session", "-t",
		"middleman-0000000000000001-c857d09db23e6822",
	})
	assert.Contains(argvs, []string{
		"kill-session", "-t",
		"middleman-0000000000000001-57de4cf40144bdf7",
	})
	assert.NotContains(argvs, []string{
		"kill-session", "-t",
		"middleman-0000000000000002-57de4cf40144bdf7",
	})
	stored, err := d.ListWorkspaceTmuxSessions(context.Background(), ws.ID)
	require.NoError(err)
	assert.Empty(stored)
}

func TestManagerCleanupTmuxSessionPreservesStoredRowsAfterRuntimeKillFailure(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`target=""` + "\n" +
		`prev=""` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$prev" = "-t" ]; then target="$a"; fi` + "\n" +
		`  prev="$a"` + "\n" +
		`done` + "\n" +
		`if [ "$1" = "kill-session" ]; then` + "\n" +
		`  case "$target" in` + "\n" +
		`    middleman-0000000000000001)` + "\n" +
		`      echo "can't find session: $target" >&2` + "\n" +
		`      exit 1` + "\n" +
		`      ;;` + "\n" +
		`    middleman-0000000000000001-57de4cf40144bdf7)` + "\n" +
		`      echo "permission denied" >&2` + "\n" +
		`      exit 42` + "\n" +
		`      ;;` + "\n" +
		`  esac` + "\n" +
		`fi` + "\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})
	ws := &Workspace{
		ID:           "0000000000000001",
		TmuxSession:  "middleman-0000000000000001",
		Status:       "error",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
	}
	require.NoError(d.InsertWorkspace(context.Background(), ws))
	for _, targetKey := range []string{"codex", "claude"} {
		require.NoError(d.UpsertWorkspaceTmuxSession(
			context.Background(),
			&db.WorkspaceTmuxSession{
				WorkspaceID: ws.ID,
				SessionName: map[string]string{
					"codex":  "middleman-0000000000000001-57de4cf40144bdf7",
					"claude": "middleman-0000000000000001-c857d09db23e6822",
				}[targetKey],
				TargetKey: targetKey,
			},
		))
	}

	err := mgr.cleanupTmuxSession(context.Background(), ws)
	require.Error(err)
	assert.Contains(err.Error(), "middleman-0000000000000001-57de4cf40144bdf7")

	argvs := readRecorderArgv(t, record)
	assert.Contains(argvs, []string{
		"kill-session", "-t", "middleman-0000000000000001",
	})
	assert.Contains(argvs, []string{
		"kill-session", "-t",
		"middleman-0000000000000001-57de4cf40144bdf7",
	})
	assert.Contains(argvs, []string{
		"kill-session", "-t",
		"middleman-0000000000000001-c857d09db23e6822",
	})

	stored, err := d.ListWorkspaceTmuxSessions(context.Background(), ws.ID)
	require.NoError(err)
	require.Len(stored, 2)
}

func TestManagerForgetMissingRuntimeTmuxSessionPreservesRecreatedRow(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`if [ "$1" = "has-session" ]; then` + "\n" +
		`  echo "can't find session: $3" >&2` + "\n" +
		`  exit 1` + "\n" +
		`fi` + "\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})
	require.NoError(d.InsertWorkspace(context.Background(), &Workspace{
		ID:           "ws-1",
		TmuxSession:  "middleman-ws-1",
		Status:       "ready",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
	}))
	oldCreatedAt := time.Date(2026, 4, 29, 1, 0, 0, 0, time.UTC)
	newCreatedAt := time.Date(2026, 4, 29, 1, 1, 0, 0, time.UTC)
	sessionName := "middleman-ws-1-helper"
	require.NoError(mgr.RecordRuntimeTmuxSession(
		context.Background(), "ws-1", sessionName, "helper", oldCreatedAt,
	))
	require.NoError(mgr.RecordRuntimeTmuxSession(
		context.Background(), "ws-1", sessionName, "helper", newCreatedAt,
	))

	deleted, err := mgr.ForgetMissingRuntimeTmuxSession(
		context.Background(), "ws-1", sessionName, oldCreatedAt,
	)
	require.NoError(err)
	assert.False(deleted)

	stored, err := d.ListWorkspaceTmuxSessions(context.Background(), "ws-1")
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal(newCreatedAt, stored[0].CreatedAt)
}

func TestManagerRequestRetryFailsWhenTmuxCleanupFails(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    echo "permission denied" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	ctx := context.Background()
	errMsg := "tmux new-session failed"
	ws := &Workspace{
		ID:              "ws-retry-cleanup-fails",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: "middleman/pr-42",
		WorktreePath:    "/tmp/ws-retry-cleanup-fails",
		TmuxSession:     "middleman-retry-cleanup-fails",
		Status:          "error",
		ErrorMessage:    &errMsg,
	}
	require.NoError(d.InsertWorkspace(ctx, ws))
	require.NoError(d.InsertWorkspaceSetupEvent(ctx, &db.WorkspaceSetupEvent{
		WorkspaceID: ws.ID,
		Stage:       workspaceSetupStageTmuxSession,
		Outcome:     "success",
		Message:     "tmux session started",
	}))

	next, startNow, err := mgr.RequestRetry(ctx, ws.ID)
	assert.Nil(next)
	assert.False(startNow)
	require.Error(err)
	assert.Contains(err.Error(), "cleanup workspace artifacts before retry")
	assert.Contains(err.Error(), "kill tmux session")
	assert.Contains(err.Error(), "permission denied")

	got, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("error", got.Status)
	require.NotNil(got.ErrorMessage)
	assert.Contains(*got.ErrorMessage, "permission denied")
	assert.Equal("middleman/pr-42", got.WorkspaceBranch)

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 1)
	assert.Equal(
		[]string{"wrap", "kill-session", "-t", ws.TmuxSession},
		argvs[0],
	)
}

func TestManagerRequestRetryConsumesQueuedRetryWhenCleanupFails(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	count := filepath.Join(dir, "count")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    n=0` + "\n" +
		`    if [ -f "$TMUX_COUNT" ]; then n=$(cat "$TMUX_COUNT"); fi` + "\n" +
		`    n=$((n + 1))` + "\n" +
		`    printf '%s' "$n" > "$TMUX_COUNT"` + "\n" +
		`    if [ "$n" -eq 1 ]; then` + "\n" +
		`      : > "$TMUX_STARTED"` + "\n" +
		`      while [ ! -f "$TMUX_RELEASE" ]; do sleep 0.01; done` + "\n" +
		`      echo "permission denied" >&2` + "\n" +
		`      exit 1` + "\n" +
		`    fi` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_STARTED", started)
	t.Setenv("TMUX_RELEASE", release)
	t.Setenv("TMUX_COUNT", count)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	ctx := context.Background()
	errMsg := "tmux new-session failed"
	ws := &Workspace{
		ID:              "ws-retry-cleanup-queued",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: "middleman/pr-42",
		WorktreePath:    "/tmp/ws-retry-cleanup-queued",
		TmuxSession:     "middleman-retry-cleanup-queued",
		Status:          "error",
		ErrorMessage:    &errMsg,
	}
	require.NoError(d.InsertWorkspace(ctx, ws))
	require.NoError(d.InsertWorkspaceSetupEvent(ctx, &db.WorkspaceSetupEvent{
		WorkspaceID: ws.ID,
		Stage:       workspaceSetupStageTmuxSession,
		Outcome:     "success",
		Message:     "tmux session started",
	}))

	type retryResult struct {
		ws       *Workspace
		startNow bool
		err      error
	}
	firstResult := make(chan retryResult, 1)
	go func() {
		next, startNow, err := mgr.RequestRetry(ctx, ws.ID)
		firstResult <- retryResult{ws: next, startNow: startNow, err: err}
	}()

	const retryWait = 5 * time.Second

	require.Eventually(func() bool {
		_, err := os.Stat(started)
		return err == nil
	}, retryWait, 10*time.Millisecond)
	require.Eventually(func() bool {
		got, err := d.GetWorkspace(ctx, ws.ID)
		return err == nil && got != nil && got.Status == "creating"
	}, retryWait, 10*time.Millisecond)

	queuedWS, startNow, err := mgr.RequestRetry(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(queuedWS)
	assert.False(startNow)
	assert.Equal("creating", queuedWS.Status)

	require.NoError(os.WriteFile(release, []byte("1"), 0o644))
	var first retryResult
	require.Eventually(func() bool {
		select {
		case first = <-firstResult:
			return true
		default:
			return false
		}
	}, retryWait, 10*time.Millisecond)
	assert.Nil(first.ws)
	assert.False(first.startNow)
	require.Error(first.err)
	assert.Contains(first.err.Error(), "permission denied")

	next, queued, err := mgr.StartQueuedRetryIfErrored(ctx, ws.ID)
	require.NoError(err)
	assert.Nil(next)
	assert.False(queued)

	got, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("error", got.Status)
}

func TestManagerRequestRetrySkipsGitCleanupWhenCloneMissing(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    echo "can't find session: missing" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	mgr.SetClones(gitclone.New(filepath.Join(dir, "clones"), nil))
	ctx := context.Background()
	errMsg := "ensure clone failed"
	ws := &Workspace{
		ID:              "ws-retry-missing-clone",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: "middleman/pr-42",
		WorktreePath:    filepath.Join(dir, "missing-worktree"),
		TmuxSession:     "middleman-retry-missing-clone",
		Status:          "error",
		ErrorMessage:    &errMsg,
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	next, startNow, err := mgr.RequestRetry(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(next)
	assert.True(startNow)
	assert.Equal("creating", next.Status)
	assert.Equal(workspaceBranchUnknown, next.WorkspaceBranch)

	got, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("creating", got.Status)
	assert.Equal(workspaceBranchUnknown, got.WorkspaceBranch)
	assert.Nil(got.ErrorMessage)
}

func TestManagerRequestRetryQueuesWhileCreatingAndStartsIfErrored(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	ctx := context.Background()
	ws := &Workspace{
		ID:              "ws-queued-retry",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath:    "/tmp/ws-queued-retry",
		TmuxSession:     "middleman-ws-queued-retry",
		Status:          "creating",
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	current, startNow, err := mgr.RequestRetry(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(current)
	assert.False(startNow)
	assert.Equal("creating", current.Status)

	errMsg := "ensure clone failed"
	require.NoError(d.UpdateWorkspaceStatus(ctx, ws.ID, "error", &errMsg))

	next, queued, err := mgr.StartQueuedRetryIfErrored(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(next)
	assert.True(queued)
	assert.Equal("creating", next.Status)
	assert.Nil(next.ErrorMessage)

	stored, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("creating", stored.Status)
	assert.Nil(stored.ErrorMessage)
	assert.Equal(workspaceBranchUnknown, stored.WorkspaceBranch)

	next, queued, err = mgr.StartQueuedRetryIfErrored(ctx, ws.ID)
	require.NoError(err)
	assert.Nil(next)
	assert.False(queued)
}

func TestManagerRequestRetryStartsWhenSetupFailedBeforeQueue(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	ctx := context.Background()
	errMsg := "ensure clone failed"
	ws := &Workspace{
		ID:              "ws-raced-retry",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: "middleman/pr-42",
		WorktreePath:    "/tmp/ws-raced-retry",
		TmuxSession:     "middleman-ws-raced-retry",
		Status:          "error",
		ErrorMessage:    &errMsg,
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	next, startNow, err := mgr.queueRetryOrStartErrored(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(next)
	assert.True(startNow)
	assert.Equal("creating", next.Status)
	assert.Nil(next.ErrorMessage)
	assert.Equal(workspaceBranchUnknown, next.WorkspaceBranch)

	stored, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("creating", stored.Status)
	assert.Nil(stored.ErrorMessage)
	assert.Equal(workspaceBranchUnknown, stored.WorkspaceBranch)

	next, queued, err := mgr.StartQueuedRetryIfErrored(ctx, ws.ID)
	require.NoError(err)
	assert.Nil(next)
	assert.False(queued)
}

func TestManagerRequestRetryDiscardsQueuedRetryWhenSetupSucceeds(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	ctx := context.Background()
	ws := &Workspace{
		ID:              "ws-discard-retry",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath:    "/tmp/ws-discard-retry",
		TmuxSession:     "middleman-ws-discard-retry",
		Status:          "creating",
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	current, startNow, err := mgr.RequestRetry(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(current)
	assert.False(startNow)

	require.NoError(d.UpdateWorkspaceStatus(ctx, ws.ID, "ready", nil))

	next, queued, err := mgr.StartQueuedRetryIfErrored(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(next)
	assert.False(queued)
	assert.Equal("ready", next.Status)

	stored, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("ready", stored.Status)
}

func TestManagerEnsureTmuxCreatesSessionOnMiss(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	// Script: "has-session" emits tmux's canonical "can't find
	// session" stderr and exits 1 (so isTmuxSessionAbsent classifies
	// it as session-missing rather than wrapper failure); everything
	// else succeeds, so EnsureTmux calls newTmuxSession.
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})

	require.NoError(mgr.EnsureTmux(t.Context(), "sess-B", "/tmp/cwd"))

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 3)
	assert.Equal(
		[]string{"has-session", "-t", "sess-B"},
		argvs[0],
	)
	// new-session argv: "new-session -d -s sess-B -c /tmp/cwd <shell> -l"
	// We check the prefix up to the shell; the shell resolves per
	// runtime so just assert it is non-empty and ends with "-l".
	require.GreaterOrEqual(len(argvs[1]), 8)
	assert.Equal("new-session", argvs[1][0])
	assert.Equal("-d", argvs[1][1])
	assert.Equal("-s", argvs[1][2])
	assert.Equal("sess-B", argvs[1][3])
	assert.Equal("-c", argvs[1][4])
	assert.Equal("/tmp/cwd", argvs[1][5])
	assert.NotEmpty(argvs[1][6])
	assert.Equal("-l", argvs[1][7])
	assert.Equal(
		[]string{
			"set-option", "-t", "sess-B",
			"@middleman_owner", mgr.tmuxOwnerMarker(),
		},
		argvs[2],
	)
}

func TestManagerEnsureTmuxCreatesSessionOnMacOSMissingServer(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "error connecting to /private/tmp/tmux-501/default (No such file or directory)" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})

	require.NoError(mgr.EnsureTmux(context.Background(), "sess-macos", "/tmp/cwd"))

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 3)
	assert.Equal(
		[]string{"has-session", "-t", "sess-macos"},
		argvs[0],
	)
	assert.Equal("new-session", argvs[1][0])
	assert.Equal("sess-macos", argvs[1][3])
	assert.Equal(
		[]string{
			"set-option", "-t", "sess-macos",
			"@middleman_owner", mgr.tmuxOwnerMarker(),
		},
		argvs[2],
	)
}

// TestReadRecorderArgvPreservesEmptyArgs pins down the parser's
// empty-arg handling. The NUL-delimited record format was chosen to
// round-trip argv with empty-string elements unambiguously; the
// parser must keep interior and trailing empties rather than
// collapsing them.
func TestReadRecorderArgvPreservesEmptyArgs(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	path := filepath.Join(t.TempDir(), "record")

	// First record: 3 args with an interior empty ("a", "", "b").
	// Second record: 2 args with a trailing empty ("x", "").
	body := "3\x00a\x00\x00b\x00" + "2\x00x\x00\x00"
	require.NoError(os.WriteFile(path, []byte(body), 0o644))

	argvs := readRecorderArgv(t, path)
	require.Len(argvs, 2)
	assert.Equal([]string{"a", "", "b"}, argvs[0])
	assert.Equal([]string{"x", ""}, argvs[1])
}

// TestManagerEnsureTmuxPropagatesBinaryError verifies that a wrapper
// misconfiguration (binary not on disk) surfaces as an error rather
// than being silently conflated with "session does not exist, please
// create one." The previous boolean-only tmuxSessionExists swallowed
// this case — EnsureTmux would proceed to run new-session with the
// same broken wrapper and the error would only surface on the second
// exec, masking the real cause.
func TestManagerEnsureTmuxPropagatesBinaryError(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	// Path that cannot possibly exist — exec returns a non-exit
	// error (ENOENT), not an *exec.ExitError.
	mgr.SetTmuxCommand(
		[]string{filepath.Join(t.TempDir(), "does-not-exist")},
	)

	err := mgr.EnsureTmux(t.Context(), "sess-X", "/tmp")
	require.Error(err)
	require.Contains(err.Error(), "tmux has-session")
}

// TestManagerEnsureTmuxPropagatesNon1ExitCode pins down the
// exit-code-1 carve-out in tmuxSessionExists. tmux's has-session
// exits 1 specifically when the session is not found; wrappers that
// fail for their own reasons typically exit with other codes (127
// "command not found", 203 "exec failed", etc.). A wrapper exiting
// with a non-1 code used to be silently treated as "session absent"
// because the old check matched any *exec.ExitError. Now it must
// propagate to the caller so misconfiguration surfaces cleanly.
func TestManagerEnsureTmuxPropagatesNon1ExitCode(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	// exit 127 mimics "command not found" — a common wrapper failure
	// signal that is NOT tmux's own "session missing" response.
	body := "#!/bin/sh\nexit 127\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})

	err := mgr.EnsureTmux(t.Context(), "sess-Y", "/tmp")
	require.Error(err)
	require.Contains(err.Error(), "tmux has-session")
}

// TestManagerEnsureTmuxPropagatesExit1NonTmuxError covers the
// second half of the session-absent heuristic: exit code 1 alone is
// not enough, the output must match tmux's canonical "session
// missing" phrases too. Many real wrappers and shell scripts use
// exit 1 as a generic failure signal — treating that as "session
// absent" would mask the wrapper bug by immediately trying
// new-session through the same broken wrapper.
func TestManagerEnsureTmuxPropagatesExit1NonTmuxError(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\necho 'wrapper blew up' >&2\nexit 1\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})

	err := mgr.EnsureTmux(t.Context(), "sess-Q", "/tmp")
	require.Error(err)
	require.Contains(err.Error(), "tmux has-session")
	require.Contains(err.Error(), "wrapper blew up")
}

// TestManagerEnsureTmuxIgnoresAbsencePhraseOnStdout pins down the
// stdout vs. stderr distinction. A wrapper that exits 1 with the
// tmux phrase on stdout (e.g. one that mirrors stderr to stdout for
// logging, or a script that coincidentally prints the phrase for
// unrelated reasons) must NOT be treated as session-absent — only
// stderr carries the authoritative tmux signal.
func TestManagerEnsureTmuxIgnoresAbsencePhraseOnStdout(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`echo "can't find session: sim"` + "\n" + // stdout only
		`echo "real failure" >&2` + "\n" +
		"exit 1\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})

	err := mgr.EnsureTmux(t.Context(), "sess-R", "/tmp")
	require.Error(err)
	require.Contains(err.Error(), "tmux has-session")
	require.Contains(err.Error(), "real failure")
}

type fakePtyOwnerCall struct {
	Op      string
	Session string
	Cwd     string
}

type fakePtyOwnerClient struct {
	Calls          []fakePtyOwnerCall
	StateExists    bool
	StateSessions  map[string]bool
	SnapshotOutput []byte
	SnapshotTitle  string
}

func (f *fakePtyOwnerClient) HasState(session string) bool {
	return f.StateExists || f.StateSessions[session]
}

func (f *fakePtyOwnerClient) Ensure(
	_ context.Context,
	session string,
	cwd string,
) error {
	f.Calls = append(f.Calls, fakePtyOwnerCall{
		Op: "ensure", Session: session, Cwd: cwd,
	})
	return nil
}

func (f *fakePtyOwnerClient) Attach(
	context.Context,
	string,
	int,
	int,
) (*ptyowner.Attachment, error) {
	return nil, nil
}

func (f *fakePtyOwnerClient) Stop(
	_ context.Context,
	session string,
) error {
	f.Calls = append(f.Calls, fakePtyOwnerCall{
		Op: "stop", Session: session,
	})
	return nil
}

func (f *fakePtyOwnerClient) Snapshot(
	context.Context,
	string,
) (ptyowner.Status, error) {
	return ptyowner.Status{
		Output: f.SnapshotOutput,
		Title:  f.SnapshotTitle,
	}, nil
}

func TestWorkspaceBranchCandidatesDoesNotIncludeBareForSluggedWorkspace(t *testing.T) {
	// Slug-style issue workspace whose bare-form branch name might
	// be a user-owned local branch unrelated to middleman. Cleanup
	// must return only the persisted GitHeadRef so the unrelated
	// branch is not deleted.
	assert := Assert.New(t)
	ws := &Workspace{
		ItemType:   db.WorkspaceItemTypeIssue,
		ItemNumber: 10,
		GitHeadRef: "middleman/issue-10-widget-rendering-broken",
	}
	got := workspaceBranchCandidates(ws, workspaceBranchUnknown)
	assert.Equal([]string{"middleman/issue-10-widget-rendering-broken"}, got)
}

func TestWorkspaceBranchCandidatesUsesBareFallbackOnlyForLegacyWorkspace(t *testing.T) {
	// Pre-feature issue workspaces have no recorded GitHeadRef.
	// Cleanup must still find the bare middleman/issue-<n> branch
	// those workspaces actually use.
	assert := Assert.New(t)
	ws := &Workspace{
		ItemType:   db.WorkspaceItemTypeIssue,
		ItemNumber: 10,
		GitHeadRef: "",
	}
	got := workspaceBranchCandidates(ws, workspaceBranchUnknown)
	assert.Equal([]string{"middleman/issue-10"}, got)
}

func TestFileLockManagerAcquireRelease(t *testing.T) {
	require := require.New(t)
	mgr := NewFileLockManager()
	ctx := t.Context()
	repo := t.TempDir()

	first, err := mgr.Acquire(ctx, repo)
	require.NoError(err)
	require.NoError(first.Unlock())

	second, err := mgr.Acquire(ctx, repo)
	require.NoError(err)
	require.NoError(second.Unlock())
}

func TestFileLockManagerSerializesGoroutines(t *testing.T) {
	require := require.New(t)
	mgr := NewFileLockManager()
	ctx := t.Context()
	repo := t.TempDir()

	const goroutines = 6
	var inCritical atomic.Int32
	var maxObserved atomic.Int32
	var overlap atomic.Int32

	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			lock, err := mgr.Acquire(ctx, repo)
			if err != nil {
				return
			}
			defer func() { _ = lock.Unlock() }()
			current := inCritical.Add(1)
			defer inCritical.Add(-1)
			if current > 1 {
				overlap.Add(1)
			}
			for {
				prev := maxObserved.Load()
				if current <= prev || maxObserved.CompareAndSwap(prev, current) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
		})
	}
	wg.Wait()

	require.Equal(int32(1), maxObserved.Load(),
		"only one goroutine should hold the lock at a time")
	require.Equal(int32(0), overlap.Load(),
		"no goroutine should observe another holder in its critical section")
	require.Equal(int32(0), inCritical.Load())
}

func TestFileLockManagerCtxCancelWhileWaiting(t *testing.T) {
	require := require.New(t)
	mgr := NewFileLockManager()
	repo := t.TempDir()

	held, err := mgr.Acquire(t.Context(), repo)
	require.NoError(err)
	defer func() { _ = held.Unlock() }()

	ctx, cancel := context.WithCancel(t.Context())
	gotErr := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := mgr.Acquire(ctx, repo)
		gotErr <- err
	}()
	<-started
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-gotErr:
		require.ErrorIs(err, context.Canceled)
	case <-time.After(2 * time.Second):
		require.FailNow("Acquire did not return after ctx cancel")
	}
}

func TestFileLockManagerDoubleUnlock(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	mgr := NewFileLockManager()
	lock, err := mgr.Acquire(t.Context(), t.TempDir())
	require.NoError(err)
	require.NoError(lock.Unlock())
	assert.Error(lock.Unlock())
}

func TestManagerWithRepoLockReleaseOnSuccess(t *testing.T) {
	require := require.New(t)
	mgr := NewManager(openTestDB(t), t.TempDir())
	repo := t.TempDir()

	calls := 0
	require.NoError(mgr.withRepoLock(t.Context(), repo, func() error {
		calls++
		return nil
	}))
	require.Equal(1, calls)

	again, err := mgr.locks.Acquire(t.Context(), repo)
	require.NoError(err)
	require.NoError(again.Unlock())
}

func TestManagerWithRepoLockReleaseOnError(t *testing.T) {
	require := require.New(t)
	mgr := NewManager(openTestDB(t), t.TempDir())
	repo := t.TempDir()

	sentinel := errors.New("inner failed")
	err := mgr.withRepoLock(t.Context(), repo, func() error {
		return sentinel
	})
	require.ErrorIs(err, sentinel)

	again, err := mgr.locks.Acquire(t.Context(), repo)
	require.NoError(err)
	require.NoError(again.Unlock())
}

func TestManagerAddWorktreeAcquiresRepoLock(t *testing.T) {
	require := require.New(t)
	cloneDir := setupBareCloneForWorkspaceGitTest(t)
	configureSameRepoPRRefs(t, cloneDir, "feature/lock-probe", 7)
	mgr := NewManager(openTestDB(t), t.TempDir())

	// Hold the per-repo lock from outside addWorktree; it must wait.
	held, err := mgr.locks.Acquire(t.Context(), cloneDir)
	require.NoError(err)

	ws := &Workspace{
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   7,
		GitHeadRef:   "feature/lock-probe",
		WorktreePath: filepath.Join(t.TempDir(), "wt"),
	}
	done := make(chan error, 1)
	go func() {
		_, err := mgr.addWorktree(t.Context(), cloneDir, ws)
		done <- err
	}()

	select {
	case <-done:
		require.FailNow("addWorktree completed while the per-repo lock was held")
	case <-time.After(80 * time.Millisecond):
	}

	require.NoError(held.Unlock())
	select {
	case err := <-done:
		require.NoError(err)
	case <-time.After(5 * time.Second):
		require.FailNow("addWorktree did not finish after lock release")
	}
}

func TestManagerCleanupForDeleteAcquiresRepoLock(t *testing.T) {
	require := require.New(t)

	host, owner, name := "github.com", "acme", "widget"
	baseDir := t.TempDir()
	cloneDir := filepath.Join(baseDir, host, owner, name+".git")
	require.NoError(os.MkdirAll(filepath.Dir(cloneDir), 0o755))
	runWorkspaceTestGit(
		t, baseDir, "init", "--bare", "--initial-branch=main", cloneDir,
	)

	mgr := NewManager(openTestDB(t), t.TempDir())
	mgr.SetClones(gitclone.New(baseDir, nil))

	ws := &Workspace{
		ID:           "ws-cleanup-lock",
		PlatformHost: host,
		RepoOwner:    owner,
		RepoName:     name,
		WorktreePath: filepath.Join(t.TempDir(), "missing-wt"),
	}

	held, err := mgr.locks.Acquire(t.Context(), cloneDir)
	require.NoError(err)
	done := make(chan error, 1)
	go func() { done <- mgr.cleanupWorkspaceArtifactsForDelete(t.Context(), ws) }()

	select {
	case <-done:
		require.FailNow("cleanupWorkspaceArtifactsForDelete proceeded under held lock")
	case <-time.After(80 * time.Millisecond):
	}
	require.NoError(held.Unlock())
	select {
	case err := <-done:
		require.NoError(err)
	case <-time.After(5 * time.Second):
		require.FailNow("cleanupWorkspaceArtifactsForDelete did not finish after release")
	}
}

package projects

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	Require "github.com/stretchr/testify/require"

	gitenv "go.kenn.io/kit/git/env"
	"go.kenn.io/middleman/internal/procutil"
)

func lifecycleGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := procutil.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitenv.StripAll(os.Environ())
	out, err := cmd.CombinedOutput()
	Require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// initLifecycleRepo creates a git repository with one commit on a stable
// default branch named "main" so tests do not depend on the host git's
// init.defaultBranch setting.
func initLifecycleRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := filepath.Join(t.TempDir(), "repo")
	Require.NoError(t, os.MkdirAll(dir, 0o755))
	lifecycleGit(t, dir, "init", "-q", "-b", "main")
	lifecycleGit(t, dir, "config", "user.email", "t@e.st")
	lifecycleGit(t, dir, "config", "user.name", "Tester")
	lifecycleGit(t, dir, "config", "commit.gpgsign", "false")
	lifecycleGit(t, dir, "commit", "--allow-empty", "-m", "initial")
	return dir
}

func branchExistsInRepo(t *testing.T, repo, branch string) bool {
	t.Helper()
	cmd := procutil.Command(
		"git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch,
	)
	cmd.Dir = repo
	cmd.Env = gitenv.StripAll(os.Environ())
	return cmd.Run() == nil
}

// writeHookScript writes an executable script that records its working
// directory and lifecycle environment to outFile, exiting with exitCode.
func writeHookScript(t *testing.T, dir, outFile string, exitCode int) string {
	t.Helper()
	script := filepath.Join(dir, "hook.sh")
	body := "#!/bin/sh\n" +
		"{\n" +
		"  pwd\n" +
		"  echo \"name=$MIDDLEMAN_WORKTREE_NAME\"\n" +
		"  echo \"path=$MIDDLEMAN_WORKTREE_PATH\"\n" +
		"  echo \"root=$MIDDLEMAN_PROJECT_ROOT\"\n" +
		"  echo \"branch=$MIDDLEMAN_BRANCH\"\n" +
		"} > " + outFile + "\n"
	if exitCode != 0 {
		body += "echo boom >&2\nexit " + string(rune('0'+exitCode)) + "\n"
	}
	Require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return script
}

func TestCreateWorktreeOnDiskDerivesPathAndCreatesBranch(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)

	result, err := CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot: repo,
		Branch:      "feat/new-thing",
	})
	require.NoError(err)

	wantBase, evalErr := filepath.EvalSymlinks(repo + "-worktrees")
	require.NoError(evalErr)
	assert.Equal(filepath.Join(wantBase, "feat-new-thing"), result.Path,
		"path derives from <root>-worktrees plus slash-slugged branch")
	assert.Equal("feat/new-thing", result.Branch)
	assert.True(branchExistsInRepo(t, repo, "feat/new-thing"))
	head := lifecycleGit(t, result.Path, "rev-parse", "--abbrev-ref", "HEAD")
	assert.Equal("feat/new-thing", head)
	assert.False(result.HookRan)
}

func TestCreateWorktreeOnDiskAttachesExistingBranch(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	lifecycleGit(t, repo, "branch", "existing")
	wantSHA := lifecycleGit(t, repo, "rev-parse", "existing")

	dest := filepath.Join(t.TempDir(), "wt")
	result, err := CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot: repo,
		Branch:      "existing",
		Path:        dest,
	})
	require.NoError(err)
	assert.Equal(dest, result.Path)
	assert.Equal(wantSHA, lifecycleGit(t, dest, "rev-parse", "HEAD"))
	assert.Equal("existing",
		lifecycleGit(t, dest, "rev-parse", "--abbrev-ref", "HEAD"))
}

func TestCreateWorktreeOnDiskFromBaseRef(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	lifecycleGit(t, repo, "checkout", "-q", "-b", "release")
	lifecycleGit(t, repo, "commit", "--allow-empty", "-m", "release work")
	releaseSHA := lifecycleGit(t, repo, "rev-parse", "release")
	lifecycleGit(t, repo, "checkout", "-q", "main")

	dest := filepath.Join(t.TempDir(), "wt")
	result, err := CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot: repo,
		Branch:      "hotfix",
		Path:        dest,
		BaseRef:     "release",
	})
	require.NoError(err)
	assert.Equal(releaseSHA, lifecycleGit(t, dest, "rev-parse", "HEAD"),
		"new branch starts at the base ref, not HEAD")
	assert.Equal("hotfix",
		lifecycleGit(t, dest, "rev-parse", "--abbrev-ref", "HEAD"))
	_ = result
}

func TestCreateWorktreeOnDiskRunsSetupHook(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	outFile := filepath.Join(t.TempDir(), "hook.out")
	script := writeHookScript(t, t.TempDir(), outFile, 0)
	// Hook scripts resolve against the project root; place one inside.
	inRepo := filepath.Join(repo, "setup.sh")
	data, err := os.ReadFile(script)
	require.NoError(err)
	require.NoError(os.WriteFile(inRepo, data, 0o755))

	dest := filepath.Join(t.TempDir(), "wt")
	result, err := CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot:  repo,
		Branch:       "feature",
		Path:         dest,
		SetupScript:  "setup.sh",
		WorktreeName: "Feature Work",
	})
	require.NoError(err)
	assert.True(result.HookRan)
	assert.Equal(inRepo, result.HookScript)

	recorded, err := os.ReadFile(outFile)
	require.NoError(err)
	lines := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	require.Len(lines, 5)
	// pwd resolves symlinks (macOS /var -> /private/var), so compare
	// canonical forms.
	canonicalDest, err := filepath.EvalSymlinks(dest)
	require.NoError(err)
	assert.Equal(canonicalDest, lines[0],
		"hook runs in the worktree directory")
	assert.Equal("name=Feature Work", lines[1])
	assert.Equal("path="+dest, lines[2])
	assert.Equal("root="+repo, lines[3])
	assert.Equal("branch=feature", lines[4])
}

func TestCreateWorktreeOnDiskRollsBackWhenSetupHookFails(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	outFile := filepath.Join(t.TempDir(), "hook.out")
	script := writeHookScript(t, t.TempDir(), outFile, 3)
	inRepo := filepath.Join(repo, "setup.sh")
	data, err := os.ReadFile(script)
	require.NoError(err)
	require.NoError(os.WriteFile(inRepo, data, 0o755))

	dest := filepath.Join(t.TempDir(), "wt")
	_, err = CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot: repo,
		Branch:      "feature",
		Path:        dest,
		SetupScript: "setup.sh",
	})
	var hookErr *HookError
	require.ErrorAs(err, &hookErr)
	assert.Equal(3, hookErr.ExitCode)
	assert.Contains(hookErr.Stderr, "boom")

	_, statErr := os.Stat(dest)
	assert.True(os.IsNotExist(statErr),
		"failed hook rolls the worktree directory back")
	assert.False(branchExistsInRepo(t, repo, "feature"),
		"failed hook rolls the created branch back")
}

func TestCreateWorktreeOnDiskKeepsPreexistingBranchOnHookFailure(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	lifecycleGit(t, repo, "branch", "existing")
	script := filepath.Join(repo, "setup.sh")
	require.NoError(os.WriteFile(
		script, []byte("#!/bin/sh\nexit 1\n"), 0o755,
	))

	dest := filepath.Join(t.TempDir(), "wt")
	_, err := CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot: repo,
		Branch:      "existing",
		Path:        dest,
		SetupScript: "setup.sh",
	})
	var hookErr *HookError
	require.ErrorAs(err, &hookErr)
	assert.True(branchExistsInRepo(t, repo, "existing"),
		"a branch the create did not make survives rollback")
}

func TestCreateWorktreeOnDiskRejectsExistingDestination(t *testing.T) {
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	dest := t.TempDir()

	_, err := CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot: repo,
		Branch:      "feature",
		Path:        dest,
	})
	require.ErrorIs(err, ErrWorktreeDestinationExists)
}

func TestCreateWorktreeOnDiskRejectsBranchCheckedOutElsewhere(t *testing.T) {
	require := Require.New(t)
	repo := initLifecycleRepo(t)

	dest := filepath.Join(t.TempDir(), "wt")
	_, err := CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot: repo,
		Branch:      "main",
		Path:        dest,
	})
	require.ErrorIs(err, ErrBranchInUse,
		"main is checked out in the primary worktree")
}

func TestCreateWorktreeOnDiskRejectsHookOutsideProject(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)

	dest := filepath.Join(t.TempDir(), "wt")
	_, err := CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot: repo,
		Branch:      "feature",
		Path:        dest,
		SetupScript: "../outside.sh",
	})
	require.ErrorIs(err, ErrHookOutsideProject)
	_, statErr := os.Stat(dest)
	assert.True(os.IsNotExist(statErr),
		"hook validation happens before any git side effect")
}

func TestCreateWorktreeOnDiskRejectsInvalidBranchName(t *testing.T) {
	require := Require.New(t)
	repo := initLifecycleRepo(t)

	_, err := CreateWorktreeOnDisk(context.Background(), CreateWorktreeOptions{
		ProjectRoot: repo,
		Branch:      "bad..name",
	})
	require.ErrorIs(err, ErrInvalidBranchName)
}

func TestRemoveWorktreeFromDiskRemovesWorktreeAndBranch(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	lifecycleGit(t, repo, "worktree", "add", "-b", "feature", dest)

	result, err := RemoveWorktreeFromDisk(context.Background(), RemoveWorktreeOptions{
		ProjectRoot:  repo,
		Path:         dest,
		Branch:       "feature",
		RemoveBranch: true,
	})
	require.NoError(err)
	assert.False(result.HookRan)
	_, statErr := os.Stat(dest)
	assert.True(os.IsNotExist(statErr))
	assert.False(branchExistsInRepo(t, repo, "feature"))
}

func TestRemoveWorktreeFromDiskKeepsBranchWithoutRemoveBranch(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	lifecycleGit(t, repo, "worktree", "add", "-b", "feature", dest)

	_, err := RemoveWorktreeFromDisk(context.Background(), RemoveWorktreeOptions{
		ProjectRoot: repo,
		Path:        dest,
		Branch:      "feature",
	})
	require.NoError(err)
	assert.True(branchExistsInRepo(t, repo, "feature"))
}

func TestRemoveWorktreeFromDiskRunsTeardownHookFirst(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	lifecycleGit(t, repo, "worktree", "add", "-b", "feature", dest)

	outFile := filepath.Join(t.TempDir(), "hook.out")
	script := writeHookScript(t, t.TempDir(), outFile, 0)
	inRepo := filepath.Join(repo, "teardown.sh")
	data, err := os.ReadFile(script)
	require.NoError(err)
	require.NoError(os.WriteFile(inRepo, data, 0o755))

	result, err := RemoveWorktreeFromDisk(context.Background(), RemoveWorktreeOptions{
		ProjectRoot:    repo,
		Path:           dest,
		Branch:         "feature",
		TeardownScript: "teardown.sh",
		WorktreeName:   "Feature Work",
	})
	require.NoError(err)
	assert.True(result.HookRan)

	recorded, err := os.ReadFile(outFile)
	require.NoError(err)
	lines := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	require.Len(lines, 5)
	canonicalDest, err := filepath.EvalSymlinks(dest)
	// The worktree is gone by the time we compare; EvalSymlinks on a
	// removed path fails, so canonicalize the parent instead.
	if err != nil {
		parent, evalErr := filepath.EvalSymlinks(filepath.Dir(dest))
		require.NoError(evalErr)
		canonicalDest = filepath.Join(parent, filepath.Base(dest))
	}
	assert.Equal(canonicalDest, lines[0],
		"teardown runs in the worktree before it is removed")
	assert.Equal("name=Feature Work", lines[1])
	assert.Equal("branch=feature", lines[4])
}

func TestRemoveWorktreeFromDiskAbortsWhenTeardownHookFails(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	lifecycleGit(t, repo, "worktree", "add", "-b", "feature", dest)
	script := filepath.Join(repo, "teardown.sh")
	require.NoError(os.WriteFile(
		script, []byte("#!/bin/sh\necho nope >&2\nexit 2\n"), 0o755,
	))

	_, err := RemoveWorktreeFromDisk(context.Background(), RemoveWorktreeOptions{
		ProjectRoot:    repo,
		Path:           dest,
		Branch:         "feature",
		TeardownScript: "teardown.sh",
		RemoveBranch:   true,
	})
	var hookErr *HookError
	require.ErrorAs(err, &hookErr)
	assert.Equal(2, hookErr.ExitCode)
	_, statErr := os.Stat(dest)
	require.NoError(statErr, "failed teardown leaves the worktree in place")
	assert.True(branchExistsInRepo(t, repo, "feature"))
}

func TestRemoveWorktreeFromDiskPrunesWhenPathAlreadyGone(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	lifecycleGit(t, repo, "worktree", "add", "-b", "feature", dest)
	require.NoError(os.RemoveAll(dest))

	_, err := RemoveWorktreeFromDisk(context.Background(), RemoveWorktreeOptions{
		ProjectRoot:  repo,
		Path:         dest,
		Branch:       "feature",
		RemoveBranch: true,
	})
	require.NoError(err)
	assert.False(branchExistsInRepo(t, repo, "feature"))
	list := lifecycleGit(t, repo, "worktree", "list", "--porcelain")
	assert.NotContains(list, dest, "stale worktree entry pruned")
}

func TestRemoveWorktreeFromDiskForceRemovesDirtyWorktree(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	lifecycleGit(t, repo, "worktree", "add", "-b", "feature", dest)
	require.NoError(os.WriteFile(
		filepath.Join(dest, "dirty.txt"), []byte("x\n"), 0o644,
	))

	_, err := RemoveWorktreeFromDisk(context.Background(), RemoveWorktreeOptions{
		ProjectRoot: repo,
		Path:        dest,
		Branch:      "feature",
		Force:       true,
	})
	require.NoError(err)
	_, statErr := os.Stat(dest)
	assert.True(os.IsNotExist(statErr))
}

func TestRemoveWorktreeFromDiskBranchInUseElsewhere(t *testing.T) {
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	lifecycleGit(t, repo, "worktree", "add", "-b", "feature", dest)

	_, err := RemoveWorktreeFromDisk(context.Background(), RemoveWorktreeOptions{
		ProjectRoot:  repo,
		Path:         dest,
		Branch:       "main",
		RemoveBranch: true,
	})
	require.ErrorIs(err, ErrBranchInUse,
		"deleting a branch checked out in another worktree is refused")
}

func TestWorktreeIsDirty(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	repo := initLifecycleRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	lifecycleGit(t, repo, "worktree", "add", "-b", "feature", dest)

	dirty, err := WorktreeIsDirty(context.Background(), dest)
	require.NoError(err)
	assert.False(dirty)

	require.NoError(os.WriteFile(
		filepath.Join(dest, "scratch.txt"), []byte("x\n"), 0o644,
	))
	dirty, err = WorktreeIsDirty(context.Background(), dest)
	require.NoError(err)
	assert.True(dirty)

	_, err = WorktreeIsDirty(context.Background(), filepath.Join(dest, "missing"))
	require.Error(err, "a missing path is an error, not clean")
}

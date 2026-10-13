package workspaceapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/internal/workspace"
	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/kwt/worktree"
)

func projectLifecycleFixture(t *testing.T, worktreeRoot string) (*Handler, *db.Project, *gitfixture.Repository) {
	t.Helper()
	require := require.New(t)
	repo := gitfixture.NewRepository(t, false)
	database := dbtest.Open(t)
	coordinator, err := workspace.NewRepositoryCoordinator(worktreeRoot)
	require.NoError(err)
	h := New(Deps{DB: database, RepositoryWorktrees: coordinator})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
		defer cancel()
		require.NoError(h.Shutdown(ctx))
	})
	project, err := database.CreateProject(t.Context(), db.CreateProjectInput{
		LocalPath: repo.Dir, DisplayName: "Example", DefaultBranch: "main",
	})
	require.NoError(err)
	return h, project, repo
}

func TestProjectsWithoutWorktreeRootRemainAvailable(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	h, project, repo := projectLifecycleFixture(t, "")
	require.Nil(h.workspaces)
	input := &registerWorktreeInput{ProjectID: project.ID}
	input.Body.Branch, input.Body.CreateOnDisk = "feature/topic", true
	input.Body.BaseDir = t.TempDir()
	created, err := h.RegisterWorktree(t.Context(), input)
	require.NoError(err)
	require.FileExists(filepath.Join(repo.Dir, ".git", ".kenn-forge-worktree.lock"))
	require.FileExists(filepath.Join(created.Body.Path, "seed.md"))
	require.Equal("feature-topic", filepath.Base(created.Body.Path))
	remove := &removeWorktreeInput{ProjectID: project.ID, WorktreeID: created.Body.ID}
	remove.Body.RemoveFromDisk, remove.Body.RemoveBranch = true, true
	_, err = h.removeProjectWorktree(t.Context(), remove)
	require.NoError(err)
	require.NoDirExists(created.Body.Path)
}

// Invoked by native Git hooks and explicit scripts, in a separate process.
func TestProjectRepositoryLockProbe(t *testing.T) {
	t.Parallel()
	path := os.Getenv("FORGE_TEST_REPOSITORY_LOCK")
	if path == "" {
		t.Skip("subprocess probe")
	}
	require := require.New(t)
	lock := flock.New(path)
	held, err := lock.TryLock()
	require.NoError(err)
	if held {
		defer func() { require.NoError(lock.Unlock()) }()
	}
	require.Equal(os.Getenv("FORGE_TEST_LOCK_AVAILABLE") == "true", held)
	require.NoError(os.WriteFile(os.Getenv("FORGE_TEST_PROBE_OUTPUT"), []byte("observed"), 0o600))
}

func projectLockProbeScript(t *testing.T, script, lockPath, output string, available bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Projects uses native executable scripts")
	}
	exe, err := os.Executable()
	require.NoError(t, err)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	body := fmt.Sprintf("#!/bin/sh\nexec env FORGE_TEST_REPOSITORY_LOCK=%s FORGE_TEST_LOCK_AVAILABLE=%t FORGE_TEST_PROBE_OUTPUT=%s %s -test.run '^TestProjectRepositoryLockProbe$'\n",
		quote(lockPath), available, quote(output), quote(exe))
	require.NoError(t, os.WriteFile(script, []byte(body), 0o700))
}

func TestProjectsShareRepositoryLockWithItems(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	root := t.TempDir()
	h, project, repo := projectLifecycleFixture(t, root)
	common := strings.TrimSpace(string(gitfixture.Run(t, repo.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")))
	lockPath := filepath.Join(root, ".kenn-forge-worktree-base-locks", fmt.Sprintf("%x", sha256.Sum256([]byte(common))), ".kenn-forge-worktree.lock")
	hooks := filepath.Join(repo.Dir, ".git", "hooks")
	gitfixture.Run(t, repo.Dir, "config", "core.hooksPath", hooks)
	output := filepath.Join(t.TempDir(), "native-hook")
	projectLockProbeScript(t, filepath.Join(hooks, "post-checkout"), lockPath, output, false)
	input := &registerWorktreeInput{ProjectID: project.ID}
	input.Body.Branch, input.Body.CreateOnDisk = "topic", true
	input.Body.Path = filepath.Join(t.TempDir(), "checkout")
	_, err := h.RegisterWorktree(t.Context(), input)
	require.NoError(err)
	require.FileExists(output)
	require.FileExists(lockPath)
}

func TestProjectsScriptsRunOutsideRepositoryLock(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	h, project, repo := projectLifecycleFixture(t, "")
	lockPath := filepath.Join(repo.Dir, ".git", ".kenn-forge-worktree.lock")
	setupOutput, teardownOutput := filepath.Join(t.TempDir(), "setup"), filepath.Join(t.TempDir(), "teardown")
	setup, teardown := filepath.Join(repo.Dir, "setup"), filepath.Join(repo.Dir, "teardown")
	projectLockProbeScript(t, setup, lockPath, setupOutput, true)
	projectLockProbeScript(t, teardown, lockPath, teardownOutput, true)
	input := &registerWorktreeInput{ProjectID: project.ID}
	input.Body.Branch, input.Body.CreateOnDisk = "topic", true
	input.Body.Path, input.Body.SetupScript = filepath.Join(t.TempDir(), "checkout"), setup
	created, err := h.RegisterWorktree(t.Context(), input)
	require.NoError(err)
	require.FileExists(setupOutput)
	remove := &removeWorktreeInput{ProjectID: project.ID, WorktreeID: created.Body.ID}
	remove.Body.RemoveFromDisk, remove.Body.TeardownScript = true, teardown
	_, err = h.removeProjectWorktree(t.Context(), remove)
	require.NoError(err)
	require.FileExists(teardownOutput)
	require.NoDirExists(created.Body.Path)
}

func TestProjectRegistrationFailureReportsRetainedArtifacts(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Projects uses native executable scripts")
	}
	require := require.New(t)
	h, project, repo := projectLifecycleFixture(t, "")
	_, err := h.db.WriteDB().ExecContext(t.Context(), `CREATE TRIGGER refuse_worktree BEFORE INSERT ON forge_project_worktrees BEGIN SELECT RAISE(FAIL, 'registration refused'); END`)
	require.NoError(err)
	script := filepath.Join(repo.Dir, "setup")
	require.NoError(os.WriteFile(script, []byte("#!/bin/sh\nprintf 'setup output' > retained.txt\n"), 0o700))
	input := &registerWorktreeInput{ProjectID: project.ID}
	input.Body.Branch, input.Body.CreateOnDisk = "topic", true
	input.Body.Path, input.Body.SetupScript = filepath.Join(t.TempDir(), "checkout"), script
	_, err = h.RegisterWorktree(t.Context(), input)
	require.Error(err)
	require.Contains(err.Error(), "registration refused")
	require.Contains(err.Error(), "cleanup incomplete")
	require.Contains(err.Error(), input.Body.Path)
	require.FileExists(filepath.Join(input.Body.Path, "retained.txt"))
	_, err = h.RegisterWorktree(t.Context(), input)
	require.Error(err)
	require.Contains(err.Error(), "already exists")
}

func TestProjectRemovalRechecksCheckoutAfterTeardown(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Projects uses native executable scripts")
	}
	for _, tt := range []struct {
		name, script, branch, errorText string
	}{
		{"dirty", "printf 'keep these edits' > seed.md\n", "topic", "uncommitted changes"},
		{"branch changed", "git switch -c changed\n", "changed", "branch_changed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			h, project, repo := projectLifecycleFixture(t, "")
			input := &registerWorktreeInput{ProjectID: project.ID}
			input.Body.Branch, input.Body.CreateOnDisk = "topic", true
			input.Body.Path = filepath.Join(t.TempDir(), "checkout")
			created, err := h.RegisterWorktree(t.Context(), input)
			require.NoError(err)
			before := gitfixture.Run(t, repo.Dir, "rev-parse", "refs/heads/topic")
			script := filepath.Join(repo.Dir, "teardown")
			require.NoError(os.WriteFile(script, []byte("#!/bin/sh\nset -e\n"+tt.script), 0o700))
			remove := &removeWorktreeInput{ProjectID: project.ID, WorktreeID: created.Body.ID}
			remove.Body.RemoveFromDisk, remove.Body.RemoveBranch = true, true
			remove.Body.TeardownScript = script

			_, err = h.removeProjectWorktree(t.Context(), remove)

			require.ErrorContains(err, tt.errorText)
			require.DirExists(created.Body.Path)
			require.Equal(tt.branch, strings.TrimSpace(string(gitfixture.Run(t, created.Body.Path, "branch", "--show-current"))))
			require.Equal(before, gitfixture.Run(t, repo.Dir, "rev-parse", "refs/heads/topic"))
			row, err := h.db.GetProjectWorktreeByID(t.Context(), created.Body.ID)
			require.NoError(err)
			require.Equal(created.Body.Path, row.Path)
			require.Equal("topic", row.Branch)
			if tt.name == "dirty" {
				contents, err := os.ReadFile(filepath.Join(created.Body.Path, "seed.md"))
				require.NoError(err)
				require.Equal("keep these edits", string(contents))
			}
		})
	}
}

// A forced removal deleted locked worktrees before kwt, as git worktree remove
// --force --force does. Without force, the lock still protects the checkout.
func TestProjectForceRemovalRemovesLockedWorktree(t *testing.T) {
	t.Parallel()
	for _, force := range []bool{true, false} {
		t.Run(fmt.Sprintf("force=%t", force), func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			h, project, repo := projectLifecycleFixture(t, "")
			input := &registerWorktreeInput{ProjectID: project.ID}
			input.Body.Branch, input.Body.CreateOnDisk = "topic", true
			input.Body.Path = filepath.Join(t.TempDir(), "checkout")
			created, err := h.RegisterWorktree(t.Context(), input)
			require.NoError(err)
			gitfixture.Run(t, repo.Dir, "worktree", "lock", "--reason", "removable media", created.Body.Path)
			remove := &removeWorktreeInput{ProjectID: project.ID, WorktreeID: created.Body.ID}
			remove.Body.RemoveFromDisk, remove.Body.Force = true, force

			_, err = h.removeProjectWorktree(t.Context(), remove)

			if !force {
				problem, ok := errors.AsType[*httpapi.ProblemError](err)
				require.True(ok, "want *ProblemError, got %T: %v", err, err)
				require.Equal(http.StatusConflict, problem.Status)
				require.Contains(problem.Detail, "retry with force")
				require.DirExists(created.Body.Path)
				return
			}
			require.NoError(err)
			require.NoDirExists(created.Body.Path)
			_, err = h.db.GetProjectWorktreeByID(t.Context(), created.Body.ID)
			require.Error(err)
		})
	}
}

// A forced removal unlocks the checkout first; when removal is then refused,
// the checkout keeps its original lock.
func TestProjectForceRemovalRestoresLockWhenRemovalIsRefused(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Projects uses native executable scripts")
	}
	require := require.New(t)
	h, project, repo := projectLifecycleFixture(t, "")
	input := &registerWorktreeInput{ProjectID: project.ID}
	input.Body.Branch, input.Body.CreateOnDisk = "topic", true
	input.Body.Path = filepath.Join(t.TempDir(), "checkout")
	created, err := h.RegisterWorktree(t.Context(), input)
	require.NoError(err)
	gitfixture.Run(t, repo.Dir, "worktree", "lock", "--reason", "removable media", created.Body.Path)
	script := filepath.Join(repo.Dir, "teardown")
	require.NoError(os.WriteFile(script, []byte("#!/bin/sh\nset -e\ngit switch -c changed\n"), 0o700))
	remove := &removeWorktreeInput{ProjectID: project.ID, WorktreeID: created.Body.ID}
	remove.Body.RemoveFromDisk, remove.Body.Force, remove.Body.TeardownScript = true, true, script

	_, err = h.removeProjectWorktree(t.Context(), remove)

	require.ErrorContains(err, "branch_changed")
	require.DirExists(created.Body.Path)
	list := string(gitfixture.Run(t, repo.Dir, "worktree", "list", "--porcelain"))
	require.Contains(list, "locked removable media\n")
	_, err = h.db.GetProjectWorktreeByID(t.Context(), created.Body.ID)
	require.NoError(err)
}

// A request canceled after the unlock still puts the lock back on the
// checkout it kept.
func TestProjectForceRemovalRestoresLockAfterCancellation(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	repo := gitfixture.NewRepository(t, false)
	path := filepath.Join(t.TempDir(), "checkout")
	gitfixture.Run(t, repo.Dir, "worktree", "add", "-b", "topic", path)
	gitfixture.Run(t, repo.Dir, "worktree", "lock", "--reason", "removable media", path)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	coordinator, err := workspace.NewRepositoryCoordinator("")
	require.NoError(err)
	repository, err := coordinator.Open(ctx, worktree.RepositoryOptions{
		Path: repo.Dir, Runner: gitsafe.Runner(),
		RunGit: func(ctx context.Context, runner gitcmd.Runner, dir string, args ...string) ([]byte, error) {
			out, err := runManagedWorktreeGit(ctx, runner, dir, args...)
			if len(args) > 1 && args[0] == "worktree" && args[1] == "unlock" {
				cancel()
			}
			return out, err
		},
	})
	require.NoError(err)
	req := worktree.RemovalRequest{
		Path: path, Force: true, ForceObservedBranch: true,
		Conditions: &worktree.RemovalConditions{Branch: "topic"},
	}

	err = repository.WithLock(t.Context(), func(scope *worktree.Scope) error {
		return removeLockedProjectWorktree(ctx, scope, req)
	})

	require.ErrorIs(err, context.Canceled)
	require.DirExists(path)
	list := string(gitfixture.Run(t, repo.Dir, "worktree", "list", "--porcelain"))
	require.Contains(list, "locked removable media\n")
}

// An invalid branch name is a request error. It must not resolve a checkout
// destination or create the worktree base folder.
func TestProjectCreationRejectsInvalidBranchName(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	h, project, repo := projectLifecycleFixture(t, "")
	input := &registerWorktreeInput{ProjectID: project.ID}
	input.Body.Branch, input.Body.CreateOnDisk = "..", true

	_, err := h.RegisterWorktree(t.Context(), input)

	problem, ok := errors.AsType[*httpapi.ProblemError](err)
	require.True(ok, "want *ProblemError, got %T: %v", err, err)
	require.Equal(http.StatusBadRequest, problem.Status)
	require.NoDirExists(repo.Dir + "-worktrees")
}

// Discovery stores a detached checkout under a "detached/<sha>" label, not a
// branch. Removing it must not treat that label as a required branch, and a
// detached checkout has no branch to delete.
func TestProjectRemovalDeletesDiscoveredDetachedWorktree(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	h, project, repo := projectLifecycleFixture(t, "")
	path := filepath.Join(t.TempDir(), "detached")
	gitfixture.Run(t, repo.Dir, "worktree", "add", "--detach", path, "HEAD")
	head := strings.TrimSpace(string(gitfixture.Run(t, path, "rev-parse", "--short", "HEAD")))
	row, err := h.db.CreateProjectWorktree(t.Context(), db.CreateProjectWorktreeInput{
		ProjectID: project.ID, Branch: "detached/" + head, Path: path,
	})
	require.NoError(err)
	branches := string(gitfixture.Run(t, repo.Dir, "branch", "--list"))
	remove := &removeWorktreeInput{ProjectID: project.ID, WorktreeID: row.ID}
	remove.Body.RemoveFromDisk, remove.Body.RemoveBranch = true, true

	_, err = h.removeProjectWorktree(t.Context(), remove)

	require.NoError(err)
	require.NoDirExists(path)
	require.NotContains(string(gitfixture.Run(t, repo.Dir, "worktree", "list", "--porcelain")), path)
	require.Equal(branches, string(gitfixture.Run(t, repo.Dir, "branch", "--list")))
	_, err = h.db.GetProjectWorktreeByID(t.Context(), row.ID)
	require.Error(err)
}

// A detached row removes only a checkout that is still detached at the
// commit seen before its teardown script ran.
func TestProjectRemovalRefusesChangedDetachedWorktree(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Projects uses native executable scripts")
	}
	for _, tt := range []struct{ name, before, teardown string }{
		{name: "branch checked out since discovery", before: "switch -c moved"},
		{name: "teardown commits", teardown: "git -c user.name=E -c user.email=e@example.com commit --allow-empty -m teardown\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			h, project, repo := projectLifecycleFixture(t, "")
			path := filepath.Join(t.TempDir(), "detached")
			gitfixture.Run(t, repo.Dir, "worktree", "add", "--detach", path, "HEAD")
			row, err := h.db.CreateProjectWorktree(t.Context(), db.CreateProjectWorktreeInput{
				ProjectID: project.ID, Branch: "detached/" + strings.TrimSpace(string(gitfixture.Run(t, path, "rev-parse", "--short", "HEAD"))), Path: path,
			})
			require.NoError(err)
			if tt.before != "" {
				gitfixture.Run(t, path, strings.Fields(tt.before)...)
			}
			remove := &removeWorktreeInput{ProjectID: project.ID, WorktreeID: row.ID}
			remove.Body.RemoveFromDisk, remove.Body.Force = true, true
			if tt.teardown != "" {
				script := filepath.Join(repo.Dir, "teardown")
				require.NoError(os.WriteFile(script, []byte("#!/bin/sh\nset -e\n"+tt.teardown), 0o700))
				remove.Body.TeardownScript = script
			}

			_, err = h.removeProjectWorktree(t.Context(), remove)

			require.Error(err)
			require.DirExists(path)
			_, err = h.db.GetProjectWorktreeByID(t.Context(), row.ID)
			require.NoError(err)
		})
	}
}

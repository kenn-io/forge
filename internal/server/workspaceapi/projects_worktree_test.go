package workspaceapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/workspace"
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

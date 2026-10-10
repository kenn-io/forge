package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	managed "go.kenn.io/kit/git/managed"
	"go.kenn.io/kwt/worktree"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHotWorktreeCanceledRegistration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX Git wrapper")
	}
	require := require.New(t)
	clone := setupBareCloneForWorkspaceGitTest(t)
	runWorkspaceTestGit(t, clone, "config", "extensions.worktreeConfig", "true")
	realGit, err := exec.LookPath("git")
	require.NoError(err)
	sibling := filepath.Join(t.TempDir(), "sibling")
	runWorkspaceTestGit(t, clone, "worktree", "add", "--detach", sibling, "HEAD")
	gate := t.TempDir()
	started, release := filepath.Join(gate, "started"), filepath.Join(gate, "release")
	script := `#!/bin/sh
case " $* " in
  *" worktree add "*)
    "$KENN_FORGE_TEST_REAL_GIT" "$@" || exit $?
    commondir="$KENN_FORGE_TEST_CLONE/worktrees/checkout/commondir"
    common=$(cat "$commondir")
    : > "$commondir"
    : > "$KENN_FORGE_TEST_STARTED"
    while [ ! -f "$KENN_FORGE_TEST_RELEASE" ]; do sleep 0.02; done
    printf '%s\n' "$common" > "$commondir"
    exit 0
    ;;
esac
exec "$KENN_FORGE_TEST_REAL_GIT" "$@"
`
	require.NoError(os.WriteFile(filepath.Join(gate, "git"), []byte(script), 0o700))
	t.Setenv("KENN_FORGE_TEST_REAL_GIT", realGit)
	t.Setenv("KENN_FORGE_TEST_CLONE", clone)
	t.Setenv("KENN_FORGE_TEST_STARTED", started)
	t.Setenv("KENN_FORGE_TEST_RELEASE", release)
	t.Setenv("PATH", gate+string(os.PathListSeparator)+os.Getenv("PATH"))
	manager := newWorkspaceTestManager(t, nil, t.TempDir())
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var prepareErr error
	go func() {
		defer close(done)
		prepareErr = manager.prepareHotWorktree(ctx, clone, workspacePath, "HEAD")
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o600)
		cancel()
		<-done
	})
	require.Eventually(func() bool { _, err := os.Stat(started); return err == nil }, 10*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
		require.FailNow("registration stopped with incomplete shared Git metadata", "%v", prepareErr)
	case <-time.After(time.Second):
	}
	require.NoError(os.WriteFile(release, nil, 0o600))
	<-done
	require.Error(prepareErr)
	runWorkspaceTestGit(t, clone, "worktree", "remove", "--force", sibling)
	// Git registered the worktree, but Forge never configured it or recorded
	// readiness. A restarted warmer must still finish and hand it off.
	manager = newWorkspaceTestManager(t, nil, manager.worktreeDir)
	require.NoError(manager.prepareHotWorktree(t.Context(), clone, workspacePath, "HEAD"))
	claimed, err := claimTestWarm(manager, t.Context(), clone, workspacePath, managed.CreateWorktreeOptions{Mode: managed.CheckoutDetached, BaseRef: "HEAD"})
	require.NoError(err)
	require.True(claimed)
	require.FileExists(filepath.Join(workspacePath, "base.txt"))
}

func TestHotWorktreeCanceledCheckout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX smudge filter")
	}
	for _, stage := range []string{"fill", "claim"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			ctx := t.Context()
			clone := setupBareCloneForWorkspaceGitTest(t)
			update := filepath.Join(t.TempDir(), "update")
			runWorkspaceTestGit(t, clone, "worktree", "add", "--detach", update, "HEAD")
			require.NoError(os.WriteFile(filepath.Join(update, ".gitattributes"), []byte("base.txt filter=gate\n"), 0o600))
			require.NoError(os.WriteFile(filepath.Join(update, "base.txt"), []byte("target\n"), 0o600))
			runWorkspaceTestGit(t, update, "add", ".")
			runWorkspaceTestGit(t, update, "commit", "-m", "gate checkout")
			target := strings.TrimSpace(string(runWorkspaceTestGit(t, update, "rev-parse", "HEAD")))
			gateDir := t.TempDir()
			script, started, release := filepath.Join(gateDir, "filter.sh"), filepath.Join(gateDir, "started"), filepath.Join(gateDir, "release")
			require.NoError(os.WriteFile(script, []byte("#!/bin/sh\n: > \"$1\"\nwhile [ ! -f \"$2\" ]; do sleep 0.02; done\ncat\n"), 0o700))
			runWorkspaceTestGit(t, clone, "config", "filter.gate.smudge", shellquote.Join(script, started, release))
			manager := newWorkspaceTestManager(t, nil, t.TempDir())
			workspacePath := filepath.Join(t.TempDir(), "workspace")
			if stage == "claim" {
				require.NoError(manager.prepareHotWorktree(ctx, clone, workspacePath, "HEAD"))
			}
			buildCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			var buildErr error
			go func() {
				defer close(done)
				if stage == "fill" {
					buildErr = manager.prepareHotWorktree(buildCtx, clone, workspacePath, target)
				} else {
					_, buildErr = claimTestWarm(manager, buildCtx, clone, workspacePath, managed.CreateWorktreeOptions{Mode: managed.CheckoutDetached, BaseRef: target})
				}
			}()
			t.Cleanup(func() {
				cancel()
				<-done
			})
			require.Eventually(func() bool { _, err := os.Stat(started); return err == nil }, 30*time.Second, 10*time.Millisecond)
			if stage == "fill" {
				lockCtx, stopLock := context.WithTimeout(ctx, 5*time.Second)
				defer stopLock()
				require.NoError(manager.withRepoLockForGitDir(lockCtx, clone, func(_ *worktree.Scope) error { return nil }))
				claimed, err := claimTestWarm(manager, ctx, clone, workspacePath, managed.CreateWorktreeOptions{Mode: managed.CheckoutExistingBranch, Branch: "main"})
				require.NoError(err)
				assert.False(t, claimed)
			}
			cancel()
			<-done
			require.Error(buildErr)
			require.NoError(os.WriteFile(release, nil, 0o600))
			// A new process must recover a real canceled Git checkout, including
			// Git's abandoned index lock, before publishing the spare as ready.
			manager = newWorkspaceTestManager(t, nil, manager.worktreeDir)
			require.NoError(manager.prepareHotWorktree(ctx, clone, workspacePath, target))
			claimed, err := claimTestWarm(manager, ctx, clone, workspacePath, managed.CreateWorktreeOptions{Mode: managed.CheckoutExistingBranch, Branch: "main"})
			require.NoError(err)
			assert.True(t, claimed)
		})
	}
}

// BenchmarkHotWorktree compares the complete local add seam, including pool
// validation, branch creation and workspace ownership. Preparation is excluded
// from the foreground timing, as it is in production.
func BenchmarkHotWorktree(b *testing.B) {
	ctx := b.Context()
	root := b.TempDir()
	source := filepath.Join(root, "source")
	require.NoError(b, os.Mkdir(source, 0o700))
	git := func(dir string, args ...string) {
		b.Helper()
		require.NoError(b, runGitWithoutHooks(ctx, dir, args...))
	}
	git(source, "init", "--initial-branch=main")
	git(source, "config", "user.name", "Fixture")
	git(source, "config", "user.email", "fixture@example.com")
	for n := range 10000 {
		dir := filepath.Join(source, fmt.Sprintf("dir-%03d", n/100))
		require.NoError(b, os.MkdirAll(dir, 0o700))
		require.NoError(b, os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%05d.txt", n)),
			[]byte(strings.Repeat(fmt.Sprintf("synthetic file %d\n", n), 10)), 0o600))
	}
	git(source, "add", ".")
	git(source, "commit", "-m", "baseline")
	git(source, "tag", "baseline")
	for n := range 10 {
		require.NoError(b, os.WriteFile(filepath.Join(source, "dir-000", fmt.Sprintf("file-%05d.txt", n)), []byte("changed\n"), 0o600))
	}
	git(source, "commit", "-am", "ten changed files")
	clone := filepath.Join(root, "clone.git")
	git(root, "clone", "--bare", "--no-hardlinks", source, clone)
	git(clone, "config", "extensions.worktreeConfig", "true")
	for _, target := range []string{"baseline", "main"} {
		for _, hot := range []bool{false, true} {
			b.Run(fmt.Sprintf("target=%s/hot=%t", target, hot), func(b *testing.B) {
				manager := newWorkspaceTestManager(b, nil, b.TempDir())
				ws := &Workspace{ID: "benchmark", WorktreePath: filepath.Join(b.TempDir(), "workspace")}
				for b.Loop() {
					b.StopTimer()
					if hot {
						require.NoError(b, manager.prepareHotWorktree(b.Context(), clone, ws.WorktreePath, "baseline"))
					}
					b.StartTimer()
					err := manager.withRepoLockForGitDir(b.Context(), clone, func(scope *worktree.Scope) error {
						_, err := createWorkspaceCheckout(b.Context(), scope, ws, managed.CreateWorktreeOptions{Branch: "workspace", BaseRef: target, Mode: managed.CheckoutNewBranch}, nil)
						return err
					})
					b.StopTimer()
					require.NoError(b, err)
					git(clone, "worktree", "remove", ws.WorktreePath)
					git(clone, "branch", "-D", "workspace")
					b.StartTimer()
				}
			})
		}
	}
}

func TestHotWorktreeClaimAndRefill(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	clone := setupBareCloneForWorkspaceGitTest(t)
	manager := newWorkspaceTestManager(t, nil, t.TempDir())
	ws := &Workspace{ID: "first", WorktreePath: filepath.Join(t.TempDir(), "first")}
	hot := hotWorktreePath(clone, ws.WorktreePath)
	alias := filepath.Join(t.TempDir(), "clone-alias")
	if err := os.Symlink(clone, alias); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	require.NoError(manager.prepareHotWorktree(ctx, alias, ws.WorktreePath, "HEAD"))
	before, err := os.Stat(filepath.Join(hot, "base.txt"))
	require.NoError(err)
	// A new manager can use a spare left by the previous process.
	manager = newWorkspaceTestManager(t, nil, manager.worktreeDir)
	require.NoError(manager.withRepoLockForGitDir(ctx, clone, func(scope *worktree.Scope) error {
		_, err := createWorkspaceCheckout(ctx, scope, ws, managed.CreateWorktreeOptions{Branch: "feature/first", BaseRef: "HEAD", Mode: managed.CheckoutNewBranch}, nil)
		return err
	}))
	after, err := os.Stat(filepath.Join(ws.WorktreePath, "base.txt"))
	require.NoError(err)
	assert.True(os.SameFile(before, after), "claim should move the prepared files")
	assert.Equal("feature/first", strings.TrimSpace(string(runWorkspaceTestGit(t, ws.WorktreePath, "branch", "--show-current"))))
	owned, err := manager.workspaceRegistrationMatches(ctx, clone, ws.WorktreePath, ws.ID)
	require.NoError(err)
	assert.True(owned)
	assert.NoDirExists(hot)

	require.NoError(manager.prepareHotWorktree(ctx, clone, ws.WorktreePath, "HEAD"))
	assert.FileExists(filepath.Join(hot, "base.txt"))
	// Replenishment must not change the workspace already handed to the user.
	still, err := os.Stat(filepath.Join(ws.WorktreePath, "base.txt"))
	require.NoError(err)
	assert.True(os.SameFile(after, still))
}

func TestHotWorktreeClaimsLatestRevisionOnlyOnce(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	clone := setupBareCloneForWorkspaceGitTest(t)
	parent := t.TempDir()
	manager := newWorkspaceTestManager(t, nil, t.TempDir())
	first := &Workspace{ID: "first", WorktreePath: filepath.Join(parent, "first")}
	require.NoError(manager.prepareHotWorktree(ctx, clone, first.WorktreePath, "HEAD"))
	before, err := os.Stat(filepath.Join(hotWorktreePath(clone, first.WorktreePath), "base.txt"))
	require.NoError(err)
	update := filepath.Join(t.TempDir(), "update")
	runWorkspaceTestGit(t, clone, "worktree", "add", "--detach", update, "HEAD")
	require.NoError(os.WriteFile(filepath.Join(update, "new.txt"), []byte("new revision\n"), 0o600))
	runWorkspaceTestGit(t, update, "add", "new.txt")
	runWorkspaceTestGit(t, update, "commit", "-m", "advance target")
	target := strings.TrimSpace(string(runWorkspaceTestGit(t, update, "rev-parse", "HEAD")))
	second := &Workspace{ID: "second", WorktreePath: filepath.Join(parent, "second")}
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, ws := range []*Workspace{first, second} {
		wg.Go(func() {
			errors <- manager.withRepoLockForGitDir(ctx, clone, func(scope *worktree.Scope) error {
				_, err := createWorkspaceCheckout(ctx, scope, ws, managed.CreateWorktreeOptions{Branch: ws.ID, BaseRef: target, Mode: managed.CheckoutNewBranch}, nil)
				return err
			})
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(err)
	}
	claimed := 0
	for _, ws := range []*Workspace{first, second} {
		info, err := os.Stat(filepath.Join(ws.WorktreePath, "base.txt"))
		require.NoError(err)
		if os.SameFile(before, info) {
			claimed++
		}
		assert.Equal(target, strings.TrimSpace(string(runWorkspaceTestGit(t, ws.WorktreePath, "rev-parse", "HEAD"))))
		assert.FileExists(filepath.Join(ws.WorktreePath, "new.txt"))
		assert.Empty(runWorkspaceTestGit(t, ws.WorktreePath, "status", "--porcelain"))
	}
	assert.Equal(1, claimed)
}

func TestHotWorktreePreservesChangedSpare(t *testing.T) {
	for _, change := range []string{"tracked", "ignored", "branch", "foreign"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			ctx := t.Context()
			clone := setupBareCloneForWorkspaceGitTest(t)
			manager := newWorkspaceTestManager(t, nil, t.TempDir())
			ws := &Workspace{ID: "new", WorktreePath: filepath.Join(t.TempDir(), "new")}
			hot := hotWorktreePath(clone, ws.WorktreePath)
			require.NoError(manager.prepareHotWorktree(ctx, clone, ws.WorktreePath, "HEAD"))
			meta, err := worktreeGitDir(ctx, hot)
			require.NoError(err)
			switch change {
			case "tracked":
				require.NoError(os.WriteFile(filepath.Join(hot, "base.txt"), []byte("keep me\n"), 0o600))
			case "ignored":
				runWorkspaceTestGit(t, hot, "config", "core.excludesFile", filepath.Join(meta, "ignore"))
				require.NoError(os.WriteFile(filepath.Join(meta, "ignore"), []byte("notes.txt\n"), 0o600))
				require.NoError(os.WriteFile(filepath.Join(hot, "notes.txt"), []byte("keep me\n"), 0o600))
			case "branch":
				runWorkspaceTestGit(t, hot, "switch", "-c", "user-work")
			case "foreign":
				require.NoError(os.Remove(filepath.Join(meta, hotWorktreeMarkerFile)))
			}
			before, err := os.Stat(filepath.Join(hot, "base.txt"))
			require.NoError(err)
			require.NoError(manager.withRepoLockForGitDir(ctx, clone, func(scope *worktree.Scope) error {
				_, err := createWorkspaceCheckout(ctx, scope, ws, managed.CreateWorktreeOptions{Branch: "new", BaseRef: "HEAD", Mode: managed.CheckoutNewBranch}, nil)
				return err
			}))
			after, err := os.Stat(filepath.Join(hot, "base.txt"))
			require.NoError(err)
			assert.True(t, os.SameFile(before, after))
			assert.FileExists(t, filepath.Join(ws.WorktreePath, "base.txt"))
		})
	}
}

func TestForegroundClaimCompetesWithWarmRefill(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	clone := setupBareCloneForWorkspaceGitTest(t)
	manager := newWorkspaceTestManager(t, nil, t.TempDir())
	path := filepath.Join(t.TempDir(), "workspace")
	require.NoError(manager.prepareHotWorktree(t.Context(), clone, path, "HEAD"))
	hot := hotWorktreePath(clone, path)
	old := strings.TrimSpace(string(runWorkspaceTestGit(t, clone, "rev-parse", "HEAD")))
	latest := strings.TrimSpace(string(runWorkspaceTestGit(t, clone, "commit-tree", old+"^{tree}", "-p", old, "-m", "advance base")))
	runWorkspaceTestGit(t, clone, "update-ref", "refs/heads/main", latest)
	pool := flock.New(filepath.Join(filepath.Dir(hot), ".kenn-forge-worktree.lock"))
	require.NoError(pool.Lock())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	started, done := make(chan struct{}), make(chan struct{})
	var refillErr error
	t.Cleanup(func() { require.NoError(pool.Unlock()); cancel(); <-done })
	go func() { close(started); refillErr = manager.prepareHotWorktree(ctx, clone, path, "HEAD"); close(done) }()
	<-started
	repo, err := manager.openRepositoryWorktrees(ctx, clone)
	require.NoError(err)
	claims := make(chan worktree.CreateResult, 2)
	failures := make(chan error, 2)
	var workers sync.WaitGroup
	for i := range 2 {
		workers.Go(func() {
			result, claimed, err := repo.ClaimWarm(ctx, worktree.WarmClaimRequest{
				Warm:   workspaceWarmRequest(clone, path, "HEAD"),
				Create: worktree.CreateRequest{Git: managed.CreateWorktreeOptions{Path: fmt.Sprintf("%s-%d", path, i), Mode: managed.CheckoutNewBranch, Branch: fmt.Sprintf("claim-%d", i), BaseRef: "HEAD"}, Identity: worktree.IdentityPolicy{FileName: workspaceOwnershipMarkerFile, Value: fmt.Sprintf("workspace-%d", i)}},
			})
			failures <- err
			if claimed {
				claims <- result
			}
		})
	}
	workers.Wait()
	close(claims)
	close(failures)
	for err := range failures {
		require.NoError(err)
	}
	require.Len(claims, 1)
	claimed := <-claims
	require.Equal(latest, strings.TrimSpace(string(runWorkspaceTestGit(t, claimed.Path, "rev-parse", "HEAD"))))
	require.NoError(pool.Unlock())
	<-done
	require.NoError(refillErr)
	require.FileExists(filepath.Join(hot, "base.txt"))
	require.Equal(latest, strings.TrimSpace(string(runWorkspaceTestGit(t, hot, "rev-parse", "HEAD"))))
}

func claimTestWarm(m *Manager, ctx context.Context, clone, path string, opts managed.CreateWorktreeOptions) (bool, error) {
	repo, err := m.openRepositoryWorktrees(ctx, clone)
	if err != nil {
		return false, err
	}
	opts.Path = path
	_, claimed, err := repo.ClaimWarm(ctx, worktree.WarmClaimRequest{Warm: workspaceWarmRequest(clone, path, opts.BaseRef), Create: worktree.CreateRequest{Git: opts}})
	return claimed, err
}

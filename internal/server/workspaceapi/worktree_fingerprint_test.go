package workspaceapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
)

func initFingerprintRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "--initial-branch=main", ".")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("base\n"), 0o644))
	runGit(t, dir, "add", "tracked.txt")
	runGit(t, dir, "commit", "-m", "base")
}

// touchGitFile bumps a git metadata file's mtime far enough that
// coarse-grained filesystems still observe a change.
func touchGitFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	future := info.ModTime().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(path, future, future))
}

func TestWorktreeGitFingerprintTracksHeadIndexAndRefs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	initFingerprintRepo(t, dir)

	base, err := worktreeGitFingerprint(dir)
	require.NoError(err)
	again, err := worktreeGitFingerprint(dir)
	require.NoError(err)
	assert.Equal(base, again, "fingerprint must be stable when nothing changed")

	// A worktree-only edit leaves every git metadata file untouched.
	require.NoError(os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("scratch\n"), 0o644))
	afterEdit, err := worktreeGitFingerprint(dir)
	require.NoError(err)
	assert.Equal(base, afterEdit)

	touchGitFile(t, filepath.Join(dir, ".git", "index"))
	afterIndex, err := worktreeGitFingerprint(dir)
	require.NoError(err)
	assert.NotEqual(afterEdit, afterIndex, "index change must move the fingerprint")

	touchGitFile(t, filepath.Join(dir, ".git", "HEAD"))
	afterHead, err := worktreeGitFingerprint(dir)
	require.NoError(err)
	assert.NotEqual(afterIndex, afterHead, "HEAD change must move the fingerprint")

	runGit(t, dir, "branch", "topic")
	afterRef, err := worktreeGitFingerprint(dir)
	require.NoError(err)
	assert.NotEqual(afterHead, afterRef, "new loose ref must move the fingerprint")
}

func TestWorktreeGitFingerprintFollowsLinkedWorktreeCommonDir(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	primary := filepath.Join(t.TempDir(), "primary")
	require.NoError(os.MkdirAll(primary, 0o755))
	initFingerprintRepo(t, primary)
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, primary, "worktree", "add", "-b", "linked", linked)

	base, err := worktreeGitFingerprint(linked)
	require.NoError(err)

	// A ref written in the shared common directory must be visible from
	// the linked worktree, whose own git directory holds no refs.
	runGit(t, primary, "branch", "shared-topic")
	afterCommonRef, err := worktreeGitFingerprint(linked)
	require.NoError(err)
	assert.NotEqual(base, afterCommonRef)

	// The linked worktree's private HEAD still counts.
	gitDir, err := worktreeGitDirNoExec(linked)
	require.NoError(err)
	touchGitFile(t, filepath.Join(gitDir, "HEAD"))
	afterLinkedHead, err := worktreeGitFingerprint(linked)
	require.NoError(err)
	assert.NotEqual(afterCommonRef, afterLinkedHead)
}

// installGitSpawnCounter puts a git shim first on PATH that records every
// invocation before executing the real git, so tests can prove whether a
// code path spawned git at all.
func installGitSpawnCounter(t *testing.T) func() int {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	shimDir := t.TempDir()
	logPath := filepath.Join(shimDir, "invocations.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\nexec " + realGit + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		raw, err := os.ReadFile(logPath)
		if err != nil {
			return 0
		}
		return strings.Count(string(raw), "\n")
	}
}

func TestBackgroundEnrichmentSkipsGitWhileFingerprintUnchanged(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	gitSpawns := installGitSpawnCounter(t)
	worktree := t.TempDir()
	initFingerprintRepo(t, worktree)

	database := dbtest.Open(t)
	manager := workspace.NewManager(database, t.TempDir())
	var clockMu sync.Mutex
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		defer clockMu.Unlock()
		now = now.Add(d)
	}
	handler := New(Deps{DB: database, Workspaces: manager, Now: clock})
	ctx, cancel := context.WithCancel(context.Background())
	handler.Start(ctx, true)
	t.Cleanup(func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		require.NoError(handler.Shutdown(shutdownCtx))
	})

	summary := &db.WorkspaceSummary{
		ID: "ws-fingerprint", Status: "ready", WorktreePath: worktree}
	waitForRefresh := func(after time.Time) workspaceResponse {
		t.Helper()
		var resp workspaceResponse
		require.Eventually(func() bool {
			resp = handler.toCachedWorkspaceResponse(summary)
			if resp.EnrichmentRefreshedAt == nil {
				return false
			}
			refreshedAt, err := time.Parse(time.RFC3339, *resp.EnrichmentRefreshedAt)
			return err == nil && !refreshedAt.Before(after)
		}, 5*time.Second, 10*time.Millisecond)
		return resp
	}

	first := waitForRefresh(clock())
	require.NotNil(first.WorktreeDirty)
	assert.False(*first.WorktreeDirty)
	assert.Equal(workspaceEnrichmentFresh, first.EnrichmentStatus)
	spawnsAfterFirstProbe := gitSpawns()
	require.Positive(spawnsAfterFirstProbe, "the first probe must run git")

	// Second refresh past the TTL with an untouched git directory: the
	// cache must be re-validated from the fingerprint alone.
	advance(workspaceEnrichmentTTL + time.Second)
	second := waitForRefresh(clock())
	assert.Equal(workspaceEnrichmentFresh, second.EnrichmentStatus)
	assert.Equal(spawnsAfterFirstProbe, gitSpawns(), "unchanged fingerprint must not spawn git")

	// Staging a new file rewrites the index, so the next refresh probes.
	require.NoError(os.WriteFile(filepath.Join(worktree, "staged.txt"), []byte("staged\n"), 0o644))
	runGit(t, worktree, "add", "staged.txt")
	spawnsBeforeIndexRefresh := gitSpawns()
	advance(workspaceEnrichmentTTL + time.Second)
	third := waitForRefresh(clock())
	require.NotNil(third.WorktreeDirty)
	assert.True(*third.WorktreeDirty)
	assert.Greater(gitSpawns(), spawnsBeforeIndexRefresh, "index change must re-probe with git")

	// Moving HEAD (a commit) also re-probes and clears the dirty state.
	runGit(t, worktree, "commit", "-m", "staged")
	spawnsBeforeHeadRefresh := gitSpawns()
	advance(workspaceEnrichmentTTL + time.Second)
	fourth := waitForRefresh(clock())
	require.NotNil(fourth.WorktreeDirty)
	assert.False(*fourth.WorktreeDirty)
	assert.Greater(gitSpawns(), spawnsBeforeHeadRefresh, "HEAD change must re-probe with git")

	// Past the forced interval an unchanged fingerprint probes again, so
	// worktree-only edits are eventually observed.
	require.NoError(os.WriteFile(filepath.Join(worktree, "unstaged.txt"), []byte("edit\n"), 0o644))
	spawnsBeforeForced := gitSpawns()
	advance(workspaceEnrichmentForcedProbeInterval + time.Second)
	fifth := waitForRefresh(clock())
	require.NotNil(fifth.WorktreeDirty)
	assert.True(*fifth.WorktreeDirty)
	assert.Greater(gitSpawns(), spawnsBeforeForced)
}

func TestListReadSchedulesTmuxOnlyWorkWhenDivergenceIsStillFresh(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv := newEnrichmentTestHandler(t, "")
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	srv.now = func() time.Time { return now }
	// Fill every worker slot so scheduled jobs stay queued for inspection.
	for range cap(srv.workspaceEnrichmentSlots) {
		srv.workspaceEnrichmentSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for range cap(srv.workspaceEnrichmentSlots) {
			<-srv.workspaceEnrichmentSlots
		}
	})
	summary := db.WorkspaceSummary{ID: "ws-cadence", Status: "ready"}
	srv.workspaceEnrichmentCache[summary.ID] = workspaceEnrichmentCacheEntry{
		hasDivergence:         true,
		hasTmux:               true,
		divergenceRefreshedAt: now,
		divergenceAttemptAt:   now,
		tmuxRefreshedAt:       now.Add(-workspaceTmuxEnrichmentTTL),
		tmuxAttemptAt:         now.Add(-workspaceTmuxEnrichmentTTL),
	}

	srv.toCachedWorkspaceResponse(&summary)

	srv.workspaceEnrichmentMu.Lock()
	job, queued := srv.workspaceEnrichmentPending[summary.ID]
	srv.workspaceEnrichmentMu.Unlock()
	require.True(queued, "a tmux-due read must queue work")
	assert.Equal(workspaceEnrichmentTmux, job.kind, "only tmux is due, so only tmux work may be queued")
}

func TestChangeAwareRefreshLeavesFreshDivergenceUntouched(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	gitSpawns := installGitSpawnCounter(t)
	worktree := t.TempDir()
	initFingerprintRepo(t, worktree)
	srv := newEnrichmentTestHandler(t, "")
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	srv.now = func() time.Time { return now }
	summary := db.WorkspaceSummary{ID: "ws-fresh-divergence", Status: "ready", WorktreePath: worktree}
	srv.workspaceEnrichmentGenerations[summary.ID] = 0
	ahead := 3
	divergenceAt := now.Add(-workspaceEnrichmentTTL / 2)
	srv.workspaceEnrichmentCache[summary.ID] = workspaceEnrichmentCacheEntry{
		response:              workspaceResponse{CommitsAhead: &ahead},
		hasDivergence:         true,
		hasTmux:               true,
		divergenceRefreshedAt: divergenceAt,
		divergenceAttemptAt:   divergenceAt,
		divergenceProbedAt:    divergenceAt,
		tmuxRefreshedAt:       now.Add(-workspaceTmuxEnrichmentTTL),
		tmuxAttemptAt:         now.Add(-workspaceTmuxEnrichmentTTL),
	}
	spawnsBefore := gitSpawns()

	// A full job that runs while only tmux is due (for example after an
	// upgrade from queued tmux work) must not probe or re-stamp divergence.
	result := srv.workspaceResponseWithChangeAwareEnrichment(t.Context(), &summary)
	entry, recorded, _ := srv.recordWorkspaceEnrichmentResult(
		summary.ID, srv.workspaceEnrichmentGeneration(summary.ID), result,
	)

	require.True(recorded)
	assert.Equal(spawnsBefore, gitSpawns(), "fresh divergence must not spawn git")
	assert.Equal(divergenceAt, entry.divergenceAttemptAt, "divergence attempt time must not advance")
	assert.Equal(divergenceAt, entry.divergenceRefreshedAt, "divergence refresh time must not advance")
	assert.Equal(now, entry.tmuxAttemptAt, "tmux was due and must be stamped")
	require.NotNil(entry.response.CommitsAhead)
	assert.Equal(3, *entry.response.CommitsAhead)
}

func TestElapsedForcedIntervalMakesDivergenceDueDespiteRecentRevalidation(t *testing.T) {
	assert := assert.New(t)
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	entry := workspaceEnrichmentCacheEntry{
		hasDivergence: true,
		hasTmux:       true,
		// Git last ran just past the forced interval; a fingerprint-only
		// re-validation happened moments ago.
		divergenceProbedAt:    now.Add(-workspaceEnrichmentForcedProbeInterval - time.Second),
		divergenceRefreshedAt: now.Add(-time.Second),
		divergenceAttemptAt:   now.Add(-time.Second),
		tmuxRefreshedAt:       now.Add(-workspaceTmuxEnrichmentTTL),
		tmuxAttemptAt:         now.Add(-workspaceTmuxEnrichmentTTL),
	}

	tmuxDue, divergenceDue := entry.componentsDue(now)

	assert.True(tmuxDue)
	assert.True(divergenceDue, "the forced interval must make git due even after a recent re-validation")
	assert.False(entry.gitProbeSkippable("same", now), "an elapsed forced interval must not be skippable")
}

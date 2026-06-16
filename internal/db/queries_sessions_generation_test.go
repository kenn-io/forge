package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeleteProjectWorktreeTmuxSessionCreatedAtPreservesNewerGeneration
// covers the relaunch race on caller-owned command session keys: an exited
// generation's cleanup must not delete the row a newer live session with the
// same key has since written.
func TestDeleteProjectWorktreeTmuxSessionCreatedAtPreservesNewerGeneration(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	project := createDiscoveryTestProject(t, d, "gen")
	wt, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: project.ID, Branch: "feature",
		Path: filepath.Join(t.TempDir(), "wt"),
	})
	require.NoError(err)

	oldGen := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	newGen := oldGen.Add(time.Minute)
	require.NoError(d.UpsertProjectWorktreeTmuxSession(ctx, &ProjectWorktreeTmuxSession{
		WorktreeID: wt.ID, SessionKey: "cmd-1", SessionName: "old",
		CreatedAt: oldGen,
	}))
	// The same key relaunches: the upsert advances the row's generation.
	require.NoError(d.UpsertProjectWorktreeTmuxSession(ctx, &ProjectWorktreeTmuxSession{
		WorktreeID: wt.ID, SessionKey: "cmd-1", SessionName: "new",
		CreatedAt: newGen,
	}))

	deleted, err := d.DeleteProjectWorktreeTmuxSessionCreatedAt(
		ctx, wt.ID, "cmd-1", oldGen,
	)
	require.NoError(err)
	assert.False(deleted, "stale-generation cleanup must not match the newer row")
	rows, err := d.ListProjectWorktreeTmuxSessions(ctx, wt.ID)
	require.NoError(err)
	require.Len(rows, 1)
	assert.Equal("new", rows[0].SessionName)

	deleted, err = d.DeleteProjectWorktreeTmuxSessionCreatedAt(
		ctx, wt.ID, "cmd-1", newGen,
	)
	require.NoError(err)
	assert.True(deleted, "matching-generation cleanup deletes the row")
}

// TestDeleteHostRuntimeTmuxSessionCreatedAtPreservesNewerGeneration is the
// host-scope twin of the project-worktree generation race.
func TestDeleteHostRuntimeTmuxSessionCreatedAtPreservesNewerGeneration(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	oldGen := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	newGen := oldGen.Add(time.Minute)
	require.NoError(d.UpsertHostRuntimeTmuxSession(ctx, &HostRuntimeTmuxSession{
		SessionKey: "cmd-1", SessionName: "old", CWD: "/tmp", CreatedAt: oldGen,
	}))
	require.NoError(d.UpsertHostRuntimeTmuxSession(ctx, &HostRuntimeTmuxSession{
		SessionKey: "cmd-1", SessionName: "new", CWD: "/tmp", CreatedAt: newGen,
	}))

	deleted, err := d.DeleteHostRuntimeTmuxSessionCreatedAt(ctx, "cmd-1", oldGen)
	require.NoError(err)
	assert.False(deleted, "stale-generation cleanup must not match the newer row")
	rows, err := d.ListHostRuntimeTmuxSessions(ctx)
	require.NoError(err)
	require.Len(rows, 1)
	assert.Equal("new", rows[0].SessionName)

	deleted, err = d.DeleteHostRuntimeTmuxSessionCreatedAt(ctx, "cmd-1", newGen)
	require.NoError(err)
	assert.True(deleted)
}

func TestDeleteProjectWorktreeTmuxSessionIgnoresNonTmuxRuntime(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	project := createDiscoveryTestProject(t, d, "non-tmux-project")
	wt, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: project.ID, Branch: "feature",
		Path: filepath.Join(t.TempDir(), "wt"),
	})
	require.NoError(err)
	createdAt := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	require.NoError(execProjectRuntimeSession(
		ctx, d, wt.ID, "pty-1", "ptyowner", createdAt,
	))

	require.NoError(d.DeleteProjectWorktreeTmuxSession(ctx, wt.ID, "pty-1"))
	assert.Equal(1, countProjectRuntimeSession(t, d, wt.ID, "pty-1"))

	deleted, err := d.DeleteProjectWorktreeTmuxSessionCreatedAt(
		ctx, wt.ID, "pty-1", createdAt,
	)
	require.NoError(err)
	assert.False(deleted)
	assert.Equal(1, countProjectRuntimeSession(t, d, wt.ID, "pty-1"))
}

func TestDeleteHostRuntimeTmuxSessionIgnoresNonTmuxRuntime(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	createdAt := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	require.NoError(execHostRuntimeSession(ctx, d, "host-pty-1", createdAt))

	require.NoError(d.DeleteHostRuntimeTmuxSession(ctx, "host-pty-1"))
	assert.Equal(1, countHostRuntimeSession(t, d, "host-pty-1"))

	deleted, err := d.DeleteHostRuntimeTmuxSessionCreatedAt(
		ctx, "host-pty-1", createdAt,
	)
	require.NoError(err)
	assert.False(deleted)
	assert.Equal(1, countHostRuntimeSession(t, d, "host-pty-1"))
}

// TestReconcileProjectInventoryDoesNotStealForeignWorktree verifies a path
// collision across projects: discovery for project B must not move a worktree
// row (and its stable id and tmux links) owned by project A.
func TestReconcileProjectInventoryDoesNotStealForeignWorktree(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	owner := createDiscoveryTestProject(t, d, "owner")
	intruder := createDiscoveryTestProject(t, d, "intruder")
	sharedPath := filepath.Join(t.TempDir(), "shared-wt")
	wt, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: owner.ID, Branch: "feature", Path: sharedPath,
	})
	require.NoError(err)

	require.NoError(d.ReconcileProjectInventory(ctx, intruder.ID, ProjectInventory{
		Worktrees: []DiscoveredWorktree{{Path: sharedPath, Branch: "stolen"}},
	}, now))

	kept, err := d.GetProjectWorktreeByID(ctx, wt.ID)
	require.NoError(err)
	assert.Equal(owner.ID, kept.ProjectID,
		"a path collision must not move the worktree to another project")
	assert.Equal("feature", kept.Branch,
		"a foreign discovery pass must not rewrite the owner's branch")
	assert.Empty(mustListWorktrees(t, d, intruder.ID),
		"the intruding project gains no row for the foreign path")
}

func execProjectRuntimeSession(
	ctx context.Context,
	d *DB,
	worktreeID string,
	sessionKey string,
	runtimeBackend string,
	createdAt time.Time,
) error {
	_, err := d.rw.ExecContext(ctx, `
		INSERT INTO middleman_project_worktree_runtime_sessions
			(worktree_id, session_key, target_key, runtime_backend,
			 backend_session_key, label, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		worktreeID, sessionKey, sessionKey, runtimeBackend,
		runtimeBackend+"-"+sessionKey, "pty", canonicalUTCTime(createdAt),
	)
	return err
}

func execHostRuntimeSession(
	ctx context.Context,
	d *DB,
	sessionKey string,
	createdAt time.Time,
) error {
	_, err := d.rw.ExecContext(ctx, `
		INSERT INTO middleman_host_runtime_sessions
			(session_key, runtime_backend, backend_session_key, label, cwd, created_at)
		VALUES (?, 'ptyowner', ?, 'pty', '/tmp', ?)`,
		sessionKey, "ptyowner-"+sessionKey, canonicalUTCTime(createdAt),
	)
	return err
}

func countProjectRuntimeSession(
	t *testing.T,
	d *DB,
	worktreeID string,
	sessionKey string,
) int {
	t.Helper()
	var count int
	err := d.ro.QueryRowContext(t.Context(), `
		SELECT COUNT(*)
		FROM middleman_project_worktree_runtime_sessions
		WHERE worktree_id = ? AND session_key = ?`,
		worktreeID, sessionKey,
	).Scan(&count)
	require.NoError(t, err)
	return count
}

func countHostRuntimeSession(t *testing.T, d *DB, sessionKey string) int {
	t.Helper()
	var count int
	err := d.ro.QueryRowContext(t.Context(), `
		SELECT COUNT(*)
		FROM middleman_host_runtime_sessions
		WHERE session_key = ?`,
		sessionKey,
	).Scan(&count)
	require.NoError(t, err)
	return count
}

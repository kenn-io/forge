package server

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/fleet"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/workspace"
)

// TestBuildLocalRawOverlaysBranchMatchedPR proves a registered worktree's
// durable branch-match link surfaces the linked PR's number, folded state,
// title, and checks status on the raw snapshot worktree — the snapshot half of
// middleman owning worktree-to-PR links.
func TestBuildLocalRawOverlaysBranchMatchedPR(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "widget",
		LocalPath:   filepath.Join(t.TempDir(), "widget"),
		RepoID:      sql.NullInt64{Int64: repoID, Valid: true},
	})
	require.NoError(err)
	wtPath := filepath.Join(t.TempDir(), "widget-feature")
	_, err = database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feature", Path: wtPath,
	})
	require.NoError(err)

	mrID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 42, Title: "Add feature",
		Author: "a", State: db.MergeRequestStateOpen, IsDraft: true, CIStatus: "success",
		HeadBranch: "feature", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(database.SetWorktreeLinks(ctx, []db.WorktreeLink{{
		MergeRequestID: mrID,
		WorktreeKey:    fleet.WorktreeScopedKey(wtPath),
		WorktreePath:   fleet.NormPath(wtPath),
		WorktreeBranch: "feature",
		LinkedAt:       now,
	}}))

	srv := &Server{db: database, workspaces: workspace.NewManager(database, t.TempDir())}
	raw, err := srv.buildLocalRaw(ctx)
	require.NoError(err)

	got := requireRawWorktree(t, raw.Worktrees, normPath(wtPath))
	require.NotNil(got.LinkedPRNumber)
	require.Equal(42, *got.LinkedPRNumber)
	require.NotNil(got.PRState)
	require.Equal("draft", *got.PRState, "open draft folds to draft")
	require.NotNil(got.PRTitle)
	require.Equal("Add feature", *got.PRTitle)
	require.NotNil(got.ChecksStatus)
	require.Equal("success", *got.ChecksStatus)
}

// TestRecomputeThenSnapshotShowsBranchMatchedPR proves the link writer and the
// snapshot reader agree on the worktree key end to end: a recompute over a
// registered worktree and its open MR produces a link that buildLocalRaw
// resolves back onto the same worktree. This is the path-normalization contract
// between fleet.WorktreeScopedKey on the write side and the snapshot's scoped
// key on the read side.
func TestRecomputeThenSnapshotShowsBranchMatchedPR(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	proj, err := database.CreateProject(ctx, db.CreateProjectInput{
		DisplayName: "widget",
		LocalPath:   filepath.Join(t.TempDir(), "widget"),
		RepoID:      sql.NullInt64{Int64: repoID, Valid: true},
	})
	require.NoError(err)
	wtPath := filepath.Join(t.TempDir(), "widget-feature")
	_, err = database.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feature", Path: wtPath,
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 7, Title: "Feature",
		Author: "a", State: db.MergeRequestStateOpen,
		HeadBranch: "feature", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	changed, err := recomputeWorktreeLinks(ctx, database, &fakeWatchedMRSetter{}, now)
	require.NoError(err)
	require.True(changed)

	srv := &Server{db: database, workspaces: workspace.NewManager(database, t.TempDir())}
	raw, err := srv.buildLocalRaw(ctx)
	require.NoError(err)

	got := requireRawWorktree(t, raw.Worktrees, normPath(wtPath))
	require.NotNil(got.LinkedPRNumber)
	require.Equal(7, *got.LinkedPRNumber)
}

// TestBuildLocalRawLeavesUnlinkedWorktreeWithoutPR proves a registered worktree
// with no branch-match link surfaces no PR fields, so the overlay is scoped to
// linked worktrees only.
func TestBuildLocalRawLeavesUnlinkedWorktreeWithoutPR(t *testing.T) {
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

	got := requireRawWorktree(t, raw.Worktrees, normPath(wtPath))
	require.Nil(got.LinkedPRNumber)
	require.Nil(got.PRState)
}

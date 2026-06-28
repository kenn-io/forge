package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createLinkedProject(t *testing.T, d *DB, name string, repoID int64) *Project {
	t.Helper()
	p, err := d.CreateProject(context.Background(), CreateProjectInput{
		DisplayName: name,
		LocalPath:   filepath.Join(t.TempDir(), name),
		RepoID:      sql.NullInt64{Int64: repoID, Valid: true},
	})
	require.NoError(t, err)
	return p
}

// TestListWorktreesForBranchMatch_ReturnsRepoLinkedWorktrees verifies the
// enumeration step of the branch-match recompute returns every non-stale
// worktree whose project links a repo, paired with that repo's id and platform
// identity, and excludes worktrees on local-only projects that cannot match a
// platform merge request.
func TestListWorktreesForBranchMatch_ReturnsRepoLinkedWorktrees(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	repoID := insertTestRepo(t, d, "acme", "widget")

	linked := createLinkedProject(t, d, "linked", repoID)
	wtPath := filepath.Join(t.TempDir(), "wt-feature")
	_, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: linked.ID, Branch: "feature", Path: wtPath,
	})
	require.NoError(err)

	localOnly, err := d.CreateProject(ctx, CreateProjectInput{
		DisplayName: "local-only",
		LocalPath:   filepath.Join(t.TempDir(), "local-only"),
	})
	require.NoError(err)
	_, err = d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: localOnly.ID,
		Branch:    "other",
		Path:      filepath.Join(t.TempDir(), "wt-other"),
	})
	require.NoError(err)

	refs, err := d.ListWorktreesForBranchMatch(ctx)
	require.NoError(err)

	require.Len(refs, 1)
	ref := refs[0]
	assert.Equal(wtPath, ref.Path)
	assert.Equal("feature", ref.Branch)
	assert.Equal(repoID, ref.RepoID)
	// The owning project's repo identity rides along so the recompute can
	// derive the watched-MR set without a second per-worktree repo fetch.
	assert.Equal("github", ref.Platform)
	assert.Equal("github.com", ref.Host)
	assert.Equal("acme", ref.Owner)
	assert.Equal("widget", ref.Name)
}

// TestListWorktreeLinkPRs_JoinsLinkedMergeRequestDisplayFields verifies the
// snapshot-side read returns each worktree link joined to its merge request's
// display fields, keyed by the worktree key the snapshot overlays onto
// registered worktrees.
func TestListWorktreeLinkPRs_JoinsLinkedMergeRequestDisplayFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	repoID := insertTestRepo(t, d, "acme", "widget")
	mr := testMR(repoID, 7, withMRTitle("Add feature"), withMRBranches("feature", "main"))
	mr.IsDraft = true
	mr.CIStatus = "success"
	mr.ReviewDecision = "approved"
	mr.MergeableState = "clean"
	mr.Additions = 12
	mr.Deletions = 3
	mr.CommentCount = 5
	mrID := insertTestMRWithOptions(t, d, mr)

	require.NoError(d.SetWorktreeLinks(ctx, []WorktreeLink{{
		MergeRequestID: mrID,
		WorktreeKey:    "worktree:/work/wt-feature",
		WorktreePath:   "/work/wt-feature",
		WorktreeBranch: "feature",
		LinkedAt:       baseTime(),
	}}))

	prs, err := d.ListWorktreeLinkPRs(ctx)
	require.NoError(err)

	require.Len(prs, 1)
	got := prs[0]
	assert.Equal("worktree:/work/wt-feature", got.WorktreeKey)
	assert.Equal(7, got.Number)
	assert.Equal("Add feature", got.Title)
	assert.Equal(MergeRequestStateOpen, got.State)
	assert.True(got.IsDraft)
	assert.Equal("success", got.CIStatus)
	// Enrichment columns ride along on the same join so the snapshot
	// producer can overlay review/mergeable/size/comment metadata without a
	// second per-worktree merge-request fetch.
	assert.Equal("approved", got.ReviewDecision)
	assert.Equal("clean", got.MergeableState)
	assert.Equal(12, got.Additions)
	assert.Equal(3, got.Deletions)
	assert.Equal(5, got.CommentCount)
}

// TestListWorktreeLinkPRs_EmptyWhenNoLinks verifies the snapshot-side read
// returns no rows when no worktree links exist, so the enrichment is a no-op.
func TestListWorktreeLinkPRs_EmptyWhenNoLinks(t *testing.T) {
	d := openTestDB(t)
	prs, err := d.ListWorktreeLinkPRs(context.Background())
	require.NoError(t, err)
	assert.Empty(t, prs)
}

// TestListWorktreesForBranchMatch_ExcludesStaleAndEmptyBranch verifies the
// enumeration excludes worktrees that cannot branch-match: a stale worktree
// (its checkout vanished) and detached-HEAD worktrees, both in the synthetic
// "detached"/"detached/<short-sha>" representation discovery stores and the
// empty-branch form.
func TestListWorktreesForBranchMatch_ExcludesStaleAndEmptyBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	repoID := insertTestRepo(t, d, "acme", "widget")
	project := createLinkedProject(t, d, "linked", repoID)

	stalePath := filepath.Join(t.TempDir(), "wt-stale")
	_, err := d.CreateProjectWorktree(ctx, CreateProjectWorktreeInput{
		ProjectID: project.ID, Branch: "gone", Path: stalePath,
	})
	require.NoError(err)

	// A discovery pass that omits the registered worktree marks it stale, and
	// surfaces detached-HEAD worktrees at different paths using the synthetic
	// labels discovery produces, plus the bare empty-branch form.
	detachedDir := t.TempDir()
	require.NoError(d.ReconcileProjectInventory(ctx, project.ID, ProjectInventory{
		Worktrees: []DiscoveredWorktree{
			{Path: filepath.Join(detachedDir, "wt-detached"), Branch: "detached"},
			{Path: filepath.Join(detachedDir, "wt-detached-sha"), Branch: "detached/abc1234"},
			{Path: filepath.Join(detachedDir, "wt-empty"), Branch: ""},
		},
	}, now))

	refs, err := d.ListWorktreesForBranchMatch(ctx)
	require.NoError(err)

	assert.Empty(refs)
}

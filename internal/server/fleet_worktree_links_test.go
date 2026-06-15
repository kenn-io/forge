package server

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	realdb "go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/fleet"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

// fakeWatchedMRSetter records every watched-MR set the recompute applies so
// tests can assert both content and that the set is re-applied each run.
type fakeWatchedMRSetter struct {
	calls [][]ghclient.WatchedMR
}

func (f *fakeWatchedMRSetter) SetWatchedMRs(mrs []ghclient.WatchedMR) {
	f.calls = append(f.calls, mrs)
}

// seedLinkRepoProjectWorktree registers a github-linked project with one
// worktree on the given branch and returns the repo id and worktree path.
func seedLinkRepoProjectWorktree(t *testing.T, d *realdb.DB, branch string) (int64, string) {
	t.Helper()
	ctx := t.Context()
	repoID, err := d.UpsertRepo(ctx, realdb.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	proj, err := d.CreateProject(ctx, realdb.CreateProjectInput{
		DisplayName: "widget",
		LocalPath:   filepath.Join(t.TempDir(), "widget"),
		RepoID:      sql.NullInt64{Int64: repoID, Valid: true},
	})
	require.NoError(t, err)
	wtPath := filepath.Join(t.TempDir(), "wt")
	_, err = d.CreateProjectWorktree(ctx, realdb.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: branch, Path: wtPath,
	})
	require.NoError(t, err)
	return repoID, wtPath
}

func seedLinkOpenMR(t *testing.T, d *realdb.DB, repoID int64, number int, head string, now time.Time) int64 {
	t.Helper()
	id, err := d.UpsertMergeRequest(t.Context(), &realdb.MergeRequest{
		RepoID: repoID, PlatformID: int64(number), Number: number,
		Title: "PR " + head, Author: "a", State: realdb.MergeRequestStateOpen,
		HeadBranch: head, BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(t, err)
	return id
}

// TestRecomputeWorktreeLinks_LinksWorktreeToOpenMRByBranch verifies the
// recompute matches a registered worktree to the open merge request on its head
// branch, persists a worktree link keyed by the snapshot scoped key, and reports
// the match to the watcher.
func TestRecomputeWorktreeLinks_LinksWorktreeToOpenMRByBranch(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := dbtest.Open(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	repoID, wtPath := seedLinkRepoProjectWorktree(t, d, "feature")
	mrID := seedLinkOpenMR(t, d, repoID, 42, "feature", now)

	watcher := &fakeWatchedMRSetter{}
	changed, err := recomputeWorktreeLinks(ctx, d, watcher, now)
	require.NoError(err)
	assert.True(changed)

	links, err := d.GetAllWorktreeLinks(ctx)
	require.NoError(err)
	require.Len(links, 1)
	assert.Equal(mrID, links[0].MergeRequestID)
	assert.Equal("worktree:"+fleet.NormPath(wtPath), links[0].WorktreeKey)
	assert.Equal(fleet.NormPath(wtPath), links[0].WorktreePath)
	assert.Equal("feature", links[0].WorktreeBranch)

	require.Len(watcher.calls, 1)
	require.Len(watcher.calls[0], 1)
	w := watcher.calls[0][0]
	assert.Equal("acme", w.Owner)
	assert.Equal("widget", w.Name)
	assert.Equal(42, w.Number)
	assert.Equal("github.com", w.PlatformHost)
}

// TestRecomputeWorktreeLinks_UnchangedSecondRunSkipsRewrite verifies a second
// recompute over the same matched set reports changed=false yet still re-applies
// the watched-MR set, which is lost across a syncer restart.
func TestRecomputeWorktreeLinks_UnchangedSecondRunSkipsRewrite(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := dbtest.Open(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	repoID, _ := seedLinkRepoProjectWorktree(t, d, "feature")
	seedLinkOpenMR(t, d, repoID, 42, "feature", now)

	watcher := &fakeWatchedMRSetter{}
	changed, err := recomputeWorktreeLinks(ctx, d, watcher, now)
	require.NoError(err)
	require.True(changed)

	changed, err = recomputeWorktreeLinks(ctx, d, watcher, now.Add(time.Hour))
	require.NoError(err)
	assert.False(changed)

	require.Len(watcher.calls, 2)
	assert.Len(watcher.calls[1], 1)
}

// TestRecomputeWorktreeLinks_NoOpenMRLeavesNoLinks verifies a worktree whose
// branch has no open merge request produces no links and reports changed=false,
// with an empty watched set.
func TestRecomputeWorktreeLinks_NoOpenMRLeavesNoLinks(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := dbtest.Open(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	seedLinkRepoProjectWorktree(t, d, "feature")

	watcher := &fakeWatchedMRSetter{}
	changed, err := recomputeWorktreeLinks(ctx, d, watcher, now)
	require.NoError(err)
	assert.False(changed)

	links, err := d.GetAllWorktreeLinks(ctx)
	require.NoError(err)
	assert.Empty(links)

	require.Len(watcher.calls, 1)
	assert.Empty(watcher.calls[0])
}

// TestRecomputeWorktreeLinks_LinkRemovedWhenMRMerges verifies the recompute
// drops a link once its merge request leaves the open state, so a merged PR's
// worktree stops reporting a live link on the next sync.
func TestRecomputeWorktreeLinks_LinkRemovedWhenMRMerges(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := dbtest.Open(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	repoID, _ := seedLinkRepoProjectWorktree(t, d, "feature")
	seedLinkOpenMR(t, d, repoID, 42, "feature", now)

	watcher := &fakeWatchedMRSetter{}
	changed, err := recomputeWorktreeLinks(ctx, d, watcher, now)
	require.NoError(err)
	require.True(changed)

	_, err = d.UpsertMergeRequest(ctx, &realdb.MergeRequest{
		RepoID: repoID, PlatformID: 42, Number: 42,
		Title: "PR feature", Author: "a", State: realdb.MergeRequestStateMerged,
		HeadBranch: "feature", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	changed, err = recomputeWorktreeLinks(ctx, d, watcher, now.Add(time.Hour))
	require.NoError(err)
	assert.True(changed)

	links, err := d.GetAllWorktreeLinks(ctx)
	require.NoError(err)
	assert.Empty(links)
}

// newLinkTriggerServer builds a minimal Server with a real syncer (the
// watched-MR setter) and an event hub, for exercising the on-demand
// recompute that worktree/project mutations trigger. The returned
// counter reads how many worktree_links_changed hints the hub has
// broadcast — the observable signal SSE subscribers act on.
func newLinkTriggerServer(
	t *testing.T, database *realdb.DB,
) (*Server, func() int) {
	t.Helper()
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{}, database, nil, nil, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	hub := NewEventHub()
	srv := &Server{db: database, syncer: syncer, hub: hub}
	return srv, func() int { return countLinkHints(hub) }
}

// countLinkHints replays the hub ring and counts
// worktree_links_changed broadcasts.
func countLinkHints(hub *EventHub) int {
	replay, _, _ := hub.ReplaySnapshotSince(0)
	count := 0
	for _, rec := range replay {
		if rec.Event.Type == "worktree_links_changed" {
			count++
		}
	}
	return count
}

// TestRegisterWorktreeRecomputesBranchMatchLinks verifies registering a
// worktree on a branch with an open MR immediately produces the branch-match
// link and fires OnWorktreeLinksRecomputed, without waiting for a sync.
func TestRegisterWorktreeRecomputesBranchMatchLinks(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	repoID, err := database.UpsertRepo(ctx, realdb.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	proj, err := database.CreateProject(ctx, realdb.CreateProjectInput{
		DisplayName: "widget",
		LocalPath:   filepath.Join(t.TempDir(), "widget"),
		RepoID:      sql.NullInt64{Int64: repoID, Valid: true},
	})
	require.NoError(err)
	mrID, err := database.UpsertMergeRequest(ctx, &realdb.MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 9, Title: "Feature",
		Author: "a", State: realdb.MergeRequestStateOpen,
		HeadBranch: "feature", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	srv, fired := newLinkTriggerServer(t, database)
	wtPath := filepath.Join(t.TempDir(), "widget-feature")
	var input registerWorktreeInput
	input.ProjectID = proj.ID
	input.Body.Branch = "feature"
	input.Body.Path = wtPath
	_, err = srv.registerWorktree(ctx, &input)
	require.NoError(err)

	links, err := database.GetAllWorktreeLinks(ctx)
	require.NoError(err)
	require.Len(links, 1)
	require.Equal(mrID, links[0].MergeRequestID)
	require.Equal(1, fired())
}

// TestDeleteProjectWorktreeRecomputesBranchMatchLinks verifies deleting a
// linked worktree immediately drops its branch-match link and fires
// OnWorktreeLinksRecomputed.
func TestDeleteProjectWorktreeRecomputesBranchMatchLinks(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	repoID, err := database.UpsertRepo(ctx, realdb.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	proj, err := database.CreateProject(ctx, realdb.CreateProjectInput{
		DisplayName: "widget",
		LocalPath:   filepath.Join(t.TempDir(), "widget"),
		RepoID:      sql.NullInt64{Int64: repoID, Valid: true},
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &realdb.MergeRequest{
		RepoID: repoID, PlatformID: 1, Number: 9, Title: "Feature",
		Author: "a", State: realdb.MergeRequestStateOpen,
		HeadBranch: "feature", BaseBranch: "main",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	wt, err := database.CreateProjectWorktree(ctx, realdb.CreateProjectWorktreeInput{
		ProjectID: proj.ID, Branch: "feature", Path: filepath.Join(t.TempDir(), "widget-feature"),
	})
	require.NoError(err)

	srv, fired := newLinkTriggerServer(t, database)
	require.NoError(database.SetWorktreeLinks(ctx, []realdb.WorktreeLink{{
		MergeRequestID: 1,
		WorktreeKey:    fleet.WorktreeScopedKey(wt.Path),
		WorktreePath:   fleet.NormPath(wt.Path),
		WorktreeBranch: "feature",
		LinkedAt:       now,
	}}))

	var input projectWorktreeIDInput
	input.ProjectID = proj.ID
	input.WorktreeID = wt.ID
	_, err = srv.deleteProjectWorktree(ctx, &input)
	require.NoError(err)

	links, err := database.GetAllWorktreeLinks(ctx)
	require.NoError(err)
	require.Empty(links)
	require.Equal(1, fired())
}

// TestWorktreeLinksSyncHook_FiresOnRecomputedAndChainsNext verifies the hook
// runs the recompute, persists the matched links, fires onRecomputed because
// the link set changed, and chains to next.
func TestWorktreeLinksSyncHook_FiresOnRecomputedAndChainsNext(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := dbtest.Open(t)
	ctx := t.Context()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	repoID, _ := seedLinkRepoProjectWorktree(t, d, "feature")
	seedLinkOpenMR(t, d, repoID, 42, "feature", now)

	fired := 0
	nextCalled := false
	hook := WorktreeLinksSyncHook(ctx, d, &fakeWatchedMRSetter{},
		func() { fired++ },
		func([]ghclient.RepoSyncResult) { nextCalled = true },
	)
	hook(nil)

	assert.Equal(1, fired)
	assert.True(nextCalled)
	links, err := d.GetAllWorktreeLinks(ctx)
	require.NoError(err)
	assert.Len(links, 1)
}

// TestWorktreeLinksSyncHook_NoFireWhenUnchanged verifies a second pass over the
// same matched set does not fire onRecomputed, since nothing changed.
func TestWorktreeLinksSyncHook_NoFireWhenUnchanged(t *testing.T) {
	assert := Assert.New(t)
	d := dbtest.Open(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	repoID, _ := seedLinkRepoProjectWorktree(t, d, "feature")
	seedLinkOpenMR(t, d, repoID, 42, "feature", now)

	fired := 0
	hook := WorktreeLinksSyncHook(t.Context(), d, &fakeWatchedMRSetter{},
		func() { fired++ }, nil,
	)
	hook(nil)
	hook(nil)

	assert.Equal(1, fired)
}

// TestWorktreeLinksSyncHook_CanceledContextSkipsRecomputeButChainsNext verifies
// a canceled hook context skips the recompute yet still chains to next.
func TestWorktreeLinksSyncHook_CanceledContextSkipsRecomputeButChainsNext(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := dbtest.Open(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	repoID, _ := seedLinkRepoProjectWorktree(t, d, "feature")
	seedLinkOpenMR(t, d, repoID, 42, "feature", now)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	fired := 0
	nextCalled := false
	hook := WorktreeLinksSyncHook(ctx, d, &fakeWatchedMRSetter{},
		func() { fired++ },
		func([]ghclient.RepoSyncResult) { nextCalled = true },
	)
	hook(nil)

	assert.Equal(0, fired)
	assert.True(nextCalled)
	links, err := d.GetAllWorktreeLinks(t.Context())
	require.NoError(err)
	assert.Empty(links)
}

// TestNotifyWorktreeChangeBroadcasts pins the fanout: a changed link
// set or stat sample reaches SSE subscribers as a payload-free
// refetch hint.
func TestNotifyWorktreeChangeBroadcasts(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv := &Server{
		hub: NewEventHub(),
	}
	ch, _ := srv.hub.Subscribe(t.Context(), false)

	srv.notifyWorktreeLinksChanged()
	srv.notifyWorktreeStatsChanged()

	var types []string
	for range 2 {
		select {
		case rec := <-ch:
			types = append(types, rec.Event.Type)
		case <-t.Context().Done():
			require.FailNow("missing broadcast")
		}
	}
	assert.Equal(
		[]string{"worktree_links_changed", "worktree_stats_changed"},
		types,
	)
	// Hub-less servers must not panic.
	bare := &Server{}
	bare.notifyWorktreeLinksChanged()
	bare.notifyWorktreeStatsChanged()
}

package server

import (
	"context"
	"log/slog"
	"os"
	"time"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/workspace"
)

// fleetWorktreeStatsInterval is how often the sampler re-measures every
// worktree's git stats. It is longer than the discovery interval because the
// work is heavier (a diff plus an ahead/behind probe per worktree) and the
// numbers drift more slowly than the structural worktree set.
const fleetWorktreeStatsInterval = 30 * time.Second

// fleetWorktreeStatsSampler keeps live git stats fresh for every worktree the
// fleet snapshot reports. On a fixed interval it measures each worktree's
// whole-branch diff size and upstream ahead/behind, writing them to the store
// so the snapshot read path (buildLocalRaw) stays free of per-worktree git I/O.
// It is the only writer of middleman_worktree_stats.
type fleetWorktreeStatsSampler struct {
	db       *db.DB
	interval time.Duration
	// onChanged, when set, is invoked once after a sampling pass (background,
	// fleet-wide, or single-worktree) observes changed git stats. It is a
	// payload-free signal — a refetch hint — not a stats carrier. May be nil.
	onChanged func()
}

func newFleetWorktreeStatsSampler(
	database *db.DB, onChanged func(),
) *fleetWorktreeStatsSampler {
	return &fleetWorktreeStatsSampler{
		db:        database,
		interval:  fleetWorktreeStatsInterval,
		onChanged: onChanged,
	}
}

// run drives sampling passes until ctx is cancelled, starting with an immediate
// pass so a freshly started daemon reports stats without waiting a full
// interval.
func (s *fleetWorktreeStatsSampler) run(ctx context.Context) {
	if s == nil || s.db == nil {
		return
	}
	s.runOnce(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}

// worktreeStatsTarget is one worktree to sample: its normalized path and the
// default branch its whole-branch diff is measured against.
type worktreeStatsTarget struct {
	path          string
	defaultBranch string
}

// runOnce samples every worktree once and prunes stats for paths that have left
// the snapshot's worktree set.
func (s *fleetWorktreeStatsSampler) runOnce(ctx context.Context) {
	if s == nil || s.db == nil {
		return
	}
	targets, err := s.collectTargets(ctx)
	if err != nil {
		slog.Warn("fleet worktree stats: collect targets failed", "err", err)
		return
	}
	anyChanged, keep := s.sampleTargets(ctx, targets)
	if err := s.db.PruneWorktreeStats(ctx, keep); err != nil {
		slog.Warn("fleet worktree stats: prune failed", "err", err)
	}
	if anyChanged {
		s.fireChanged()
	}
}

// sampleTargets measures and upserts each target's git stats, reporting whether
// any sample's counts changed and the set of paths to keep (for pruning). A
// target whose path is gone or whose probe fails leaves its prior sample in
// place and does not count as a change.
func (s *fleetWorktreeStatsSampler) sampleTargets(
	ctx context.Context, targets []worktreeStatsTarget,
) (anyChanged bool, keep []string) {
	now := time.Now()
	keep = make([]string, 0, len(targets))
	for _, target := range targets {
		keep = append(keep, target.path)
		stats, ok := sampleWorktreeGitStats(ctx, target.path, target.defaultBranch)
		if !ok {
			continue
		}
		changed, err := s.db.UpsertWorktreeStats(ctx, target.path, stats, now)
		if err != nil {
			slog.Warn("fleet worktree stats: upsert failed",
				"path", target.path, "err", err)
			continue
		}
		anyChanged = anyChanged || changed
	}
	return anyChanged, keep
}

// fireChanged invokes the onChanged refetch hint if one is registered. It is the
// single point through which background, fleet-wide, and single-worktree passes
// signal observed stat changes, so they share identical firing semantics.
func (s *fleetWorktreeStatsSampler) fireChanged() {
	if s.onChanged != nil {
		s.onChanged()
	}
}

// collectTargets enumerates the same worktrees buildLocalRaw reports — each
// registered project's primary and linked worktrees, plus active-workspace
// worktrees — deduped by normalized path. A registered worktree wins over a
// workspace overlay at the same path, so its project default branch sets the
// diff base. Stale projects and worktrees are skipped.
func (s *fleetWorktreeStatsSampler) collectTargets(
	ctx context.Context,
) ([]worktreeStatsTarget, error) {
	projects, err := s.db.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var targets []worktreeStatsTarget
	add := func(rawPath, defaultBranch string) {
		path := normPath(rawPath)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		targets = append(targets, worktreeStatsTarget{
			path:          path,
			defaultBranch: defaultBranch,
		})
	}

	for i := range projects {
		project := projects[i]
		if project.IsStale {
			continue
		}
		add(project.LocalPath, project.DefaultBranch)
		worktrees, err := s.db.ListProjectWorktrees(ctx, project.ID)
		if err != nil {
			return nil, err
		}
		for _, worktree := range worktrees {
			if worktree.IsStale {
				continue
			}
			add(worktree.Path, project.DefaultBranch)
		}
	}

	summaries, err := s.db.ListWorkspaceSummaries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range summaries {
		sum := summaries[i]
		if seen[normPath(sum.WorktreePath)] {
			continue
		}
		add(sum.WorktreePath, s.workspaceDefaultBranch(ctx, sum))
	}
	return targets, nil
}

// workspaceDefaultBranch resolves the diff base for an orphan-workspace worktree
// (one with no registered project) from the synced repo row, or "" when the repo
// is unknown — matching the snapshot's synthesized-project default branch.
func (s *fleetWorktreeStatsSampler) workspaceDefaultBranch(
	ctx context.Context, sum db.WorkspaceSummary,
) string {
	repo, err := s.db.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     sum.Platform,
		PlatformHost: sum.PlatformHost,
		Owner:        sum.RepoOwner,
		Name:         sum.RepoName,
	})
	if err != nil || repo == nil {
		return ""
	}
	return repo.DefaultBranch
}

// sampleWorktreeGitStats measures one worktree's whole-branch diff size and
// upstream ahead/behind. The boolean is false when the path is gone or the diff
// probe failed unexpectedly, so the caller leaves any prior sample in place; a
// branch with no upstream is a normal zero-sync sample, not a failure.
func sampleWorktreeGitStats(
	ctx context.Context, path, defaultBranch string,
) (db.WorktreeGitStats, bool) {
	if _, err := os.Stat(path); err != nil {
		return db.WorktreeGitStats{}, false
	}
	added, removed, ok, err := workspace.WorktreeDiffTotals(ctx, path, defaultBranch)
	if err != nil {
		slog.Warn("fleet worktree stats: diff totals failed", "path", path, "err", err)
		return db.WorktreeGitStats{}, false
	}
	if !ok {
		// No diff base resolved (bare or empty repository): a zero sample
		// would misreport a failed probe as a measured clean diff.
		return db.WorktreeGitStats{}, false
	}
	// A divergence probe that errors must not publish a clean in-sync zero;
	// ok=false with no error is the documented no-upstream case, which is a
	// genuine zero-sync sample.
	divergence, _, err := workspace.WorktreeDivergence(ctx, path)
	if err != nil {
		slog.Warn("fleet worktree stats: divergence failed", "path", path, "err", err)
		return db.WorktreeGitStats{}, false
	}
	return db.WorktreeGitStats{
		DiffAdded:   added,
		DiffRemoved: removed,
		SyncAhead:   divergence.Ahead,
		SyncBehind:  divergence.Behind,
	}, true
}

// refreshWorktreeStats re-measures a single worktree's git stats and upserts
// them immediately, bypassing the interval wait. It lets a lifecycle mutation
// (or an on-demand refresh) make the fleet snapshot's diff/sync fields coherent
// for the affected worktree without a full sampler pass; the background sampler
// remains the eventual-consistency fallback. It mirrors runOnce's per-target
// work — a path that is gone or whose diff probe fails leaves any prior sample
// in place — and normalizes the path so the upserted row matches the snapshot
// overlay's lookup key.
func (s *fleetWorktreeStatsSampler) refreshWorktreeStats(
	ctx context.Context, path, defaultBranch string,
) error {
	if s == nil || s.db == nil {
		return nil
	}
	path = normPath(path)
	if path == "" {
		return nil
	}
	stats, ok := sampleWorktreeGitStats(ctx, path, defaultBranch)
	if !ok {
		return nil
	}
	changed, err := s.db.UpsertWorktreeStats(ctx, path, stats, time.Now())
	if err != nil {
		return err
	}
	if changed {
		s.fireChanged()
	}
	return nil
}

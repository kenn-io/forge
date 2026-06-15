package server

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/db"
)

// fleetWorktreeDiscoveryInterval is how often the discoverer re-inspects every
// registered project's checkout. It is short because the result feeds the live
// fleet snapshot; a missing or newly added worktree should surface quickly.
const fleetWorktreeDiscoveryInterval = 15 * time.Second

// fleetWorktreeDiscoverer keeps the project registry's on-disk facts fresh.
// On a fixed interval it inspects each registered project's git checkout — its
// repository kind, default branch, and linked worktrees — and reconciles the
// result into the registry. This lets the fleet snapshot surface worktrees that
// were never explicitly registered and flag checkouts that disappeared, without
// the snapshot read path doing any git I/O of its own. Discovery is the only
// writer of the discovered columns (repository_kind, default_branch, is_stale).
type fleetWorktreeDiscoverer struct {
	db       *db.DB
	interval time.Duration
}

func newFleetWorktreeDiscoverer(database *db.DB) *fleetWorktreeDiscoverer {
	return &fleetWorktreeDiscoverer{
		db:       database,
		interval: fleetWorktreeDiscoveryInterval,
	}
}

// run drives discovery passes until ctx is cancelled, starting with an
// immediate pass so a freshly started daemon does not wait a full interval
// before its worktrees appear.
func (d *fleetWorktreeDiscoverer) run(ctx context.Context) {
	if d == nil || d.db == nil {
		return
	}
	d.runOnce(ctx)
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.runOnce(ctx)
		}
	}
}

// runOnce reconciles every registered project once.
func (d *fleetWorktreeDiscoverer) runOnce(ctx context.Context) {
	projects, err := d.db.ListProjects(ctx)
	if err != nil {
		slog.Warn("fleet worktree discovery: list projects failed", "err", err)
		return
	}
	for i := range projects {
		d.refreshProject(ctx, projects[i].ID, projects[i].LocalPath)
	}
}

// refreshProject inspects one project and reconciles the result. A checkout
// that cannot be inspected (moved or deleted) is marked stale rather than
// dropped, so a temporarily missing repository recovers on a later pass.
func (d *fleetWorktreeDiscoverer) refreshProject(
	ctx context.Context, projectID, localPath string,
) {
	if d == nil || d.db == nil {
		return
	}
	inv, err := discoverProjectInventory(ctx, localPath)
	if err != nil {
		slog.Warn("fleet worktree discovery: inspect failed, marking stale",
			"project_id", projectID, "path", localPath, "err", err)
		if markErr := d.db.MarkProjectStale(ctx, projectID, time.Now()); markErr != nil {
			slog.Warn("fleet worktree discovery: mark stale failed",
				"project_id", projectID, "err", markErr)
		}
		return
	}
	if err := d.db.ReconcileProjectInventory(ctx, projectID, inv, time.Now()); err != nil {
		slog.Warn("fleet worktree discovery: reconcile failed",
			"project_id", projectID, "err", err)
	}
}

// gitWorktreeListEntry is one block parsed from `git worktree list --porcelain`.
type gitWorktreeListEntry struct {
	path     string
	head     string
	branch   string
	bare     bool
	detached bool
}

// discoverProjectInventory inspects a project's git checkout and returns its
// repository kind, default branch, and every worktree — including the root
// checkout itself, which gets a registry row like any linked worktree so its
// runtime surface and session ownership work uniformly. Worktree identity is
// the normalized path, matching the snapshot's scoped keys.
func discoverProjectInventory(
	ctx context.Context, root string,
) (db.ProjectInventory, error) {
	root = normPath(root)

	bare, err := gitIsBareRepository(ctx, root)
	if err != nil {
		return db.ProjectInventory{}, err
	}
	entries, err := gitWorktreeEntries(ctx, root)
	if err != nil {
		return db.ProjectInventory{}, err
	}

	repoKind := "standard"
	if bare {
		repoKind = "bare"
	}

	// git reports symlink-resolved paths while the project root is stored as
	// given, so the root entry is recognized by comparing in resolved space
	// and recorded under the stored path.
	primaryKey := resolvedPathKey(root)
	worktrees := make([]db.DiscoveredWorktree, 0, len(entries))
	for _, e := range entries {
		path := normPath(e.path)
		if e.bare {
			continue
		}
		if resolvedPathKey(path) == primaryKey {
			path = root
		}
		branch := e.branch
		if branch == "" {
			branch = detachedWorktreeBranch(e.head)
		}
		worktrees = append(worktrees, db.DiscoveredWorktree{
			Path:   path,
			Branch: branch,
		})
	}

	return db.ProjectInventory{
		RepositoryKind: repoKind,
		DefaultBranch:  resolveDefaultBranch(ctx, root, entries),
		Worktrees:      worktrees,
	}, nil
}

func gitIsBareRepository(ctx context.Context, dir string) (bool, error) {
	out, err := gitDiscoveryOutput(ctx, dir, "rev-parse", "--is-bare-repository")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

func gitWorktreeEntries(ctx context.Context, dir string) ([]gitWorktreeListEntry, error) {
	out, err := gitDiscoveryOutput(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseGitWorktreeList(out), nil
}

// parseGitWorktreeList parses the porcelain worktree listing. Blocks are
// separated by a blank line; each carries a worktree path plus optional HEAD,
// branch, bare, and detached markers.
func parseGitWorktreeList(output string) []gitWorktreeListEntry {
	blocks := strings.Split(strings.TrimSpace(output), "\n\n")
	entries := make([]gitWorktreeListEntry, 0, len(blocks))
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var entry gitWorktreeListEntry
		for line := range strings.SplitSeq(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				entry.path = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			case strings.HasPrefix(line, "HEAD "):
				entry.head = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
			case strings.HasPrefix(line, "branch "):
				entry.branch = strings.TrimPrefix(
					strings.TrimSpace(strings.TrimPrefix(line, "branch ")),
					"refs/heads/",
				)
			case line == "bare":
				entry.bare = true
			case line == "detached":
				entry.detached = true
			}
		}
		if entry.path != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

// resolveDefaultBranch resolves a project's default branch from live git state,
// preferring the remote's published HEAD and falling back through the local
// HEAD, discovered branches, the ref list, and the configured init default.
func resolveDefaultBranch(
	ctx context.Context, root string, entries []gitWorktreeListEntry,
) string {
	if originHead := gitSymbolicRef(ctx, root, "refs/remotes/origin/HEAD"); originHead != "" {
		if i := strings.LastIndex(originHead, "/"); i >= 0 && i+1 < len(originHead) {
			return originHead[i+1:]
		}
		return originHead
	}
	if head := gitSymbolicRef(ctx, root, "HEAD"); head != "" {
		return head
	}

	discovered := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.branch != "" {
			discovered = append(discovered, e.branch)
		}
	}
	if branch := preferredBranch(discovered); branch != "" {
		return branch
	}

	if out, err := gitDiscoveryOutput(
		ctx, root, "for-each-ref", "--format=%(refname:short)", "refs/heads",
	); err == nil {
		if branch := preferredBranch(strings.Fields(out)); branch != "" {
			return branch
		}
	}
	if out, err := gitDiscoveryOutput(ctx, root, "config", "--get", "init.defaultBranch"); err == nil {
		if v := strings.TrimSpace(out); v != "" {
			return v
		}
	}
	return "main"
}

// preferredBranch picks main or master when present, else the first branch.
func preferredBranch(branches []string) string {
	for _, preferred := range []string{"main", "master"} {
		if slices.Contains(branches, preferred) {
			return preferred
		}
	}
	if len(branches) > 0 {
		return branches[0]
	}
	return ""
}

// gitSymbolicRef resolves a symbolic ref to its short target, returning "" when
// the ref is missing or not symbolic (git exits non-zero under --quiet).
func gitSymbolicRef(ctx context.Context, dir, ref string) string {
	out, err := gitDiscoveryOutput(ctx, dir, "symbolic-ref", "--quiet", "--short", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func gitDiscoveryOutput(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitcmd.New().Output(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// resolvedPathKey resolves symlinks so two spellings of the same directory (for
// example /tmp vs /private/tmp on macOS, or a checkout reached through a
// symlinked parent) compare equal. It falls back to the cleaned input when the
// path does not exist, so a removed checkout still yields a stable key.
func resolvedPathKey(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil && resolved != "" {
		return filepath.Clean(resolved)
	}
	return p
}

// detachedWorktreeBranch labels a detached worktree by its short HEAD so the
// snapshot has a stable, human-readable name instead of an empty branch.
func detachedWorktreeBranch(head string) string {
	head = strings.TrimSpace(head)
	if head == "" {
		return "detached"
	}
	if len(head) > 12 {
		head = head[:12]
	}
	return "detached/" + head
}

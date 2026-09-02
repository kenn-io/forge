package fleetapi

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/procutil"
	gitcmd "go.kenn.io/kit/git/cmd"
	gitworktree "go.kenn.io/kit/git/worktree"
)

// fleetWorktreeDiscoveryInterval is how often the discoverer re-inspects every
// registered project's checkout. It is short because the result feeds the live
// fleet snapshot; a missing or newly added worktree should surface quickly.
// Each pass is cheap for an unchanged project: it stats the git directory and
// only spawns git when that fingerprint moved.
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
	gate     *fleetMonitorGate

	// fingerprints remembers, per project ID, the git-directory fingerprint
	// observed by the last successful reconcile. A background pass whose
	// fingerprint still matches skips both the git spawns and the database
	// write; a failed or stale-marking pass drops the entry so recovery is
	// re-inspected in full.
	fingerprintsMu sync.Mutex
	fingerprints   map[string]string
}

func newFleetWorktreeDiscoverer(
	database *db.DB, hasSubscribers func() bool,
) *fleetWorktreeDiscoverer {
	return &fleetWorktreeDiscoverer{
		db:           database,
		interval:     fleetWorktreeDiscoveryInterval,
		gate:         newFleetMonitorGate("worktree discovery", hasSubscribers),
		fingerprints: map[string]string{},
	}
}

// run drives discovery passes until ctx is cancelled. The first pass runs as
// soon as a client is subscribed so a freshly started daemon does not wait a
// full interval before its worktrees appear.
func (d *fleetWorktreeDiscoverer) run(ctx context.Context) {
	if d == nil || d.db == nil {
		return
	}
	runFleetMonitorLoop(ctx, d.interval, d.gate, d.runOnce)
}

// runOnce reconciles every registered project whose git directory changed
// since its last successful reconcile, and prunes fingerprints of projects
// that left the registry.
func (d *fleetWorktreeDiscoverer) runOnce(ctx context.Context) {
	projects, err := d.db.ListProjects(ctx)
	if err != nil {
		slog.Warn("fleet worktree discovery: list projects failed", "err", err)
		return
	}
	keep := make(map[string]struct{}, len(projects))
	for i := range projects {
		keep[projects[i].ID] = struct{}{}
		d.refreshProjectIfChanged(ctx, projects[i].ID, projects[i].LocalPath)
	}
	d.fingerprintsMu.Lock()
	for id := range d.fingerprints {
		if _, ok := keep[id]; !ok {
			delete(d.fingerprints, id)
		}
	}
	d.fingerprintsMu.Unlock()
}

// refreshProjectIfChanged is the background variant of refreshProject: it
// inspects the project only when its git-directory fingerprint differs from
// the one recorded by the last successful reconcile. A checkout whose
// fingerprint cannot be computed (moved or deleted) always goes through the
// full path so it is marked stale.
func (d *fleetWorktreeDiscoverer) refreshProjectIfChanged(
	ctx context.Context, projectID, localPath string,
) {
	fingerprint, err := projectDiscoveryFingerprint(normPath(localPath))
	if err == nil {
		d.fingerprintsMu.Lock()
		previous, seen := d.fingerprints[projectID]
		d.fingerprintsMu.Unlock()
		if seen && previous == fingerprint {
			return
		}
	}
	d.reconcileProject(ctx, projectID, localPath, fingerprint, err == nil)
}

// refreshProject inspects one project and reconciles the result
// unconditionally. It is the on-demand path used after registration and
// lifecycle mutations, so it never consults the change fingerprint; it does
// record the fingerprint it observed so the next background pass can skip.
func (d *fleetWorktreeDiscoverer) refreshProject(
	ctx context.Context, projectID, localPath string,
) {
	if d == nil || d.db == nil {
		return
	}
	fingerprint, err := projectDiscoveryFingerprint(normPath(localPath))
	d.reconcileProject(ctx, projectID, localPath, fingerprint, err == nil)
}

// reconcileProject runs the git inspection and writes the result. A checkout
// that cannot be inspected (moved or deleted) is marked stale rather than
// dropped, so a temporarily missing repository recovers on a later pass. The
// fingerprint is remembered only after a successful reconcile; the
// fingerprint is taken before inspection so a change that lands mid-pass
// shows up as a mismatch next time instead of being lost.
func (d *fleetWorktreeDiscoverer) reconcileProject(
	ctx context.Context, projectID, localPath, fingerprint string, haveFingerprint bool,
) {
	if d == nil || d.db == nil {
		return
	}
	d.forgetFingerprint(projectID)
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
		return
	}
	if haveFingerprint {
		d.fingerprintsMu.Lock()
		d.fingerprints[projectID] = fingerprint
		d.fingerprintsMu.Unlock()
	}
}

func (d *fleetWorktreeDiscoverer) forgetFingerprint(projectID string) {
	d.fingerprintsMu.Lock()
	delete(d.fingerprints, projectID)
	d.fingerprintsMu.Unlock()
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
		path := normPath(e.Path)
		if e.Bare {
			continue
		}
		if resolvedPathKey(path) == primaryKey {
			path = root
		}
		branch := e.Branch
		if branch == "" {
			branch = detachedWorktreeBranch(e.Head)
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

func gitWorktreeEntries(ctx context.Context, dir string) ([]gitworktree.PorcelainEntry, error) {
	out, err := gitDiscoveryOutput(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return gitworktree.ParsePorcelain(out), nil
}

// resolveDefaultBranch resolves a project's default branch from live git state,
// preferring the remote's published HEAD and falling back through the local
// HEAD, discovered branches, the ref list, and the configured init default.
func resolveDefaultBranch(
	ctx context.Context, root string, entries []gitworktree.PorcelainEntry,
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
		if e.Branch != "" {
			discovered = append(discovered, e.Branch)
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

// gitDiscoveryOutput runs one git command for discovery through the shared
// subprocess limiter, so a registry of many projects cannot fan out past the
// host process budget the rest of the daemon respects.
func gitDiscoveryOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitcmd.New().Command(ctx, dir, args...)
	out, err := procutil.Output(ctx, cmd, "fleet worktree discovery")
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

package server

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/fleet"
	"go.kenn.io/middleman/internal/workspace"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

// buildLocalRaw builds this daemon's raw fleet inventory from the local
// database: the canonical union of registered projects, their worktrees,
// and active workspaces (synthesizing a project for workspaces whose repo
// identity has no registered project). Pure read — no DB writes. Host keys
// and UUIDs are left for the enrichment layer to stamp.
//
// The read path stays free of per-workspace git/tmux I/O: PR-level data comes
// straight from the cached workspace summary, and live tmux state is reconciled
// only from the bounded fleet tmux monitor cache.
func (s *Server) buildLocalRaw(ctx context.Context) (fleet.RawSnapshot, error) {
	caps := fleet.Probe(ctx, s.tmuxCmd)
	raw := fleet.RawSnapshot{
		SchemaVersion: fleet.SchemaVersion,
		Capabilities:  &caps,
		Host: fleet.RawHost{
			Hostname: hostnameOrEmpty(),
			Platform: platformString(),
			Version:  s.version,
		},
	}
	if s.hub != nil {
		raw.Generation = s.hub.Generation()
	}
	raw.PlatformAuthenticated = s.fleetPlatformAuthMonitor.authenticated()

	projByScoped := map[string]bool{}
	projByIdentity := map[string]string{} // platform identity -> project scoped key
	wtByPath := map[string]*fleet.RawWorktree{}
	var order []string
	var ownedTmux []fleetOwnedTmuxSession

	// 1. Registered projects + root primary worktree + bare project-worktrees.
	projects, err := s.db.ListProjects(ctx)
	if err != nil {
		return fleet.RawSnapshot{}, err
	}
	linkPRs, err := s.db.ListWorktreeLinkPRs(ctx)
	if err != nil {
		return fleet.RawSnapshot{}, err
	}
	prByKey := make(map[string]db.WorktreeLinkPR, len(linkPRs))
	for _, pr := range linkPRs {
		prByKey[pr.WorktreeKey] = pr
	}
	for _, p := range projects {
		key := "repo:" + normPath(p.LocalPath)
		// BackendReady is left nil: this daemon is the backend serving every
		// registered project, so a local project is ready by construction.
		// PlatformCoverage treats nil as ready ("active"); only an explicit
		// not-ready signal degrades coverage, and middleman has no per-project
		// readiness source distinct from host-level auth/diagnostics.
		rp := fleet.RawProject{
			ScopedKey:      key,
			RegistryID:     p.ID,
			Name:           p.DisplayName,
			RootPath:       normPath(p.LocalPath),
			DefaultBranch:  p.DefaultBranch,
			RepositoryKind: p.RepositoryKind,
			IsStale:        p.IsStale,
		}
		if p.PlatformIdentity != nil {
			rp.Platform = p.PlatformIdentity.Platform
			rp.PlatformHost = p.PlatformIdentity.Host
			rp.PlatformRepo = p.PlatformIdentity.Owner + "/" + p.PlatformIdentity.Name
			projByIdentity[identityKey(
				p.PlatformIdentity.Platform, p.PlatformIdentity.Host,
				p.PlatformIdentity.Owner, p.PlatformIdentity.Name,
			)] = key
		}
		raw.Projects = append(raw.Projects, rp)
		projByScoped[key] = true

		pws, err := s.db.ListProjectWorktrees(ctx, p.ID)
		if err != nil {
			return fleet.RawSnapshot{}, err
		}

		// The root checkout is a registered worktree row like any other
		// (discovery upserts it), flagged primary here by path. Emit it
		// first to keep primary-first ordering, and synthesize a rowless
		// primary only for projects discovery has not visited yet.
		rootKey := "worktree:" + normPath(p.LocalPath)
		rootRegistered := false
		for _, pw := range pws {
			if "worktree:"+normPath(pw.Path) == rootKey {
				rootRegistered = true
				break
			}
		}
		if !rootRegistered {
			addWorktree(&order, wtByPath, fleet.RawWorktree{
				ScopedKey:  rootKey,
				ProjectKey: key,
				Name:       filepath.Base(p.LocalPath),
				Path:       normPath(p.LocalPath),
				Branch:     p.DefaultBranch,
				IsPrimary:  true,
				IsStale:    p.IsStale,
			})
		}
		emit := func(pw db.ProjectWorktree) error {
			wtKey := "worktree:" + normPath(pw.Path)
			isPrimary := wtKey == rootKey
			rw := fleet.RawWorktree{
				ScopedKey:  wtKey,
				RegistryID: pw.ID,
				ProjectKey: key,
				Name:       filepath.Base(pw.Path),
				Path:       normPath(pw.Path),
				Branch:     pw.Branch,
				IsPrimary:  isPrimary,
				// The root row inherits project staleness: a failed
				// discovery pass marks only the project stale, and a
				// stale project's checkout must not read as healthy.
				IsStale:            pw.IsStale || (isPrimary && p.IsStale),
				IsHidden:           pw.IsHidden,
				SessionBackend:     pw.SessionBackend,
				LinkedIssueNumbers: pw.LinkedIssueNumbers,
			}
			if pr, ok := prByKey[wtKey]; ok {
				applyLinkPR(&rw, pr)
			}
			addWorktree(&order, wtByPath, rw)
			owned, err := ownedTmuxSessionsForProjectWorktree(ctx, s, pw, wtKey)
			if err != nil {
				return err
			}
			ownedTmux = append(ownedTmux, owned...)
			raw.Sessions = append(raw.Sessions, rawSessionsForOwnedTmux(owned)...)
			raw.Sessions = append(raw.Sessions, nonTmuxRuntimeSessionsForProjectWorktree(s, pw, wtKey)...)
			return nil
		}
		for _, pw := range pws {
			if "worktree:"+normPath(pw.Path) != rootKey {
				continue
			}
			if err := emit(pw); err != nil {
				return fleet.RawSnapshot{}, err
			}
		}
		for _, pw := range pws {
			if "worktree:"+normPath(pw.Path) == rootKey {
				continue
			}
			if err := emit(pw); err != nil {
				return fleet.RawSnapshot{}, err
			}
		}
	}

	// 2. Workspaces: synthesize missing projects, overlay worktrees, sessions.
	if s.workspaces != nil {
		summaries, err := s.workspaces.ListSummaries(ctx)
		if err != nil {
			return fleet.RawSnapshot{}, err
		}
		for i := range summaries {
			sum := summaries[i]
			ident := identityKey(sum.Platform, sum.PlatformHost, sum.RepoOwner, sum.RepoName)
			projKey, ok := projByIdentity[ident]
			if !ok {
				projKey = "repo:" + sum.Platform + ":" + sum.PlatformHost + ":" + sum.RepoOwner + "/" + sum.RepoName
				if !projByScoped[projKey] {
					raw.Projects = append(raw.Projects, s.synthesizedWorkspaceProject(ctx, sum, projKey))
					projByScoped[projKey] = true
					projByIdentity[ident] = projKey
				}
			}
			wtKey := "worktree:" + normPath(sum.WorktreePath)
			addWorktree(&order, wtByPath, worktreeFromWorkspace(sum, wtKey, projKey))
			owned, err := ownedTmuxSessionsForWorkspace(ctx, s, sum, wtKey)
			if err != nil {
				return fleet.RawSnapshot{}, err
			}
			ownedTmux = append(ownedTmux, owned...)
			raw.Sessions = append(raw.Sessions, rawSessionsForOwnedTmux(owned)...)
			raw.Sessions = append(raw.Sessions, nonTmuxRuntimeSessionsForWorkspace(s, sum, wtKey)...)
		}
	}

	if s.fleetTmuxMonitor != nil {
		reconcileFleetTmuxSnapshot(&raw, ownedTmux, s.fleetTmuxMonitor.snapshot())
	}

	stats, err := s.db.ListWorktreeStats(ctx)
	if err != nil {
		return fleet.RawSnapshot{}, err
	}
	for _, k := range order {
		// order and wtByPath are populated together in addWorktree, so every
		// key resolves to a non-nil overlay; the guard keeps nilaway sound.
		wt := wtByPath[k]
		if wt == nil {
			continue
		}
		applyWorktreeStats(wt, stats)
		raw.Worktrees = append(raw.Worktrees, *wt)
	}
	return raw, nil
}

// applyWorktreeStats overlays a worktree's sampled git stats by path. A sampled
// worktree surfaces all four counts as pointers (even zero); an unsampled path
// leaves them nil. The background stats sampler is the producer, keeping this
// read path free of git I/O.
func applyWorktreeStats(wt *fleet.RawWorktree, stats map[string]db.WorktreeGitStats) {
	sample, ok := stats[wt.Path]
	if !ok {
		return
	}
	added, removed := sample.DiffAdded, sample.DiffRemoved
	ahead, behind := sample.SyncAhead, sample.SyncBehind
	wt.DiffAdded = &added
	wt.DiffRemoved = &removed
	wt.SyncAhead = &ahead
	wt.SyncBehind = &behind
}

// synthesizedWorkspaceProject builds a placeholder project for a workspace
// whose repo identity has no registered project. middleman owns no local
// checkout for it, so it carries no RootPath or RepositoryKind and is flagged
// IsSynthesized; consumers must treat it as read-only (no worktree creation).
// The default branch is filled from the synced repo row when known — a DB
// read, never read-path git I/O.
func (s *Server) synthesizedWorkspaceProject(
	ctx context.Context, sum db.WorkspaceSummary, projKey string,
) fleet.RawProject {
	return fleet.RawProject{
		ScopedKey:     projKey,
		Name:          sum.RepoOwner + "/" + sum.RepoName,
		Platform:      sum.Platform,
		PlatformHost:  sum.PlatformHost,
		PlatformRepo:  sum.RepoOwner + "/" + sum.RepoName,
		DefaultBranch: s.syncedRepoDefaultBranch(ctx, sum),
		IsSynthesized: true,
	}
}

// syncedRepoDefaultBranch returns the default branch middleman recorded for a
// workspace's repo during sync, or "" when the repo is unknown or unsynced.
func (s *Server) syncedRepoDefaultBranch(ctx context.Context, sum db.WorkspaceSummary) string {
	if s.db == nil {
		return ""
	}
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

// worktreeFromWorkspace maps a workspace summary into a raw worktree
// overlay carrying item-level data straight from the cached summary.
//
// The summary's MR* columns are item-polymorphic: for an issue workspace the
// joined title and state are the issue's, not a pull request's. Surfacing
// them as PR fields would mislabel issue data as prTitle/prState and drop the
// actual issue link, so issue workspaces expose LinkedIssueNumbers (plus any
// associated PR number) and PR display fields stay strictly PR-only.
func worktreeFromWorkspace(sum db.WorkspaceSummary, wtKey, projKey string) fleet.RawWorktree {
	wt := fleet.RawWorktree{
		ScopedKey:      wtKey,
		ProjectKey:     projKey,
		Name:           filepath.Base(sum.WorktreePath),
		Path:           normPath(sum.WorktreePath),
		Branch:         sum.GitHeadRef,
		SessionBackend: sessionBackendForWorkspace(sum),
	}
	if sum.ItemType == db.WorkspaceItemTypeIssue {
		wt.LinkedIssueNumbers = []int{sum.ItemNumber}
		// AssociatedPRNumber is the PR linked to the issue, if any. The
		// summary does not carry that PR's title/state/checks, so only the
		// number is surfaced, never PR display fields built from issue data.
		wt.LinkedPRNumber = sum.AssociatedPRNumber
		return wt
	}
	// Pull-request workspaces: the PR number is the item itself, and the
	// joined MR metadata describes that PR. Diff size comes from the live git
	// stats sampler (overlaid by path), not the GitHub MR additions/deletions
	// cache, so registered and orphan worktrees report it the same way.
	prNumber := sum.ItemNumber
	wt.LinkedPRNumber = &prNumber
	wt.PRState = foldedPRState(sum)
	wt.PRTitle = sum.MRTitle
	wt.ChecksStatus = sum.MRCIStatus
	return wt
}

// sessionBackendForWorkspace translates a workspace's persisted terminal
// backend into the generic fleet session-backend vocabulary. A workspace always
// carries a backend (it defaults to tmux at creation), so the empty fallback
// only guards a malformed row; the enrichment layer applies the local default
// for worktrees that carry no backend of their own.
func sessionBackendForWorkspace(sum db.WorkspaceSummary) string {
	switch sum.TerminalBackend {
	case workspace.TerminalBackendPtyOwner:
		return fleet.SessionBackendLocalPTY
	case workspace.TerminalBackendTmux:
		return fleet.SessionBackendLocalTmux
	default:
		return ""
	}
}

// foldedPRState returns a pull-request workspace's display state, folding an
// open draft into "draft" while leaving terminal states (closed/merged)
// untouched so a closed draft still reports its real state.
func foldedPRState(sum db.WorkspaceSummary) *string {
	if sum.MRIsDraft != nil && *sum.MRIsDraft &&
		sum.MRState != nil && *sum.MRState == "open" {
		draft := "draft"
		return &draft
	}
	return sum.MRState
}

func ownedTmuxSessionsForWorkspace(
	ctx context.Context,
	s *Server,
	sum db.WorkspaceSummary,
	wtKey string,
) ([]fleetOwnedTmuxSession, error) {
	var out []fleetOwnedTmuxSession
	if sum.Status == "ready" && sum.TmuxSession != "" {
		out = append(out, fleetOwnedTmuxSession{
			Name:             sum.TmuxSession,
			WorktreeKey:      wtKey,
			SessionScopedKey: "session:" + sum.ID + ":main",
			RuntimeKind:      "tmux",
			Status:           "running",
			Label:            sum.TmuxSession,
			CreatedAt:        sum.CreatedAt,
		})
	}
	if s.db == nil {
		return out, nil
	}
	stored, err := s.db.ListWorkspaceRuntimeTmuxSessions(ctx, sum.ID)
	if err != nil {
		return nil, err
	}
	runtimeByKey := runtimeSessionByKey(s, sum.ID)
	for _, storedSession := range stored {
		if storedSession.TmuxSession == "" || storedSession.TargetKey == "" {
			continue
		}
		key := storedSession.SessionKey
		session := fleetOwnedTmuxSession{
			Name:             storedSession.TmuxSession,
			WorktreeKey:      wtKey,
			SessionScopedKey: "session:" + key,
			RuntimeKind:      string(localruntime.LaunchTargetAgent),
			Status:           "running",
			Label:            storedSession.TargetKey,
			CreatedAt:        storedSession.CreatedAt,
		}
		if si, ok := runtimeByKey[key]; ok {
			session.RuntimeKind = string(si.Kind)
			session.Status = string(si.Status)
			session.Label = si.Label
		}
		out = append(out, session)
	}
	return out, nil
}

func ownedTmuxSessionsForProjectWorktree(
	ctx context.Context,
	s *Server,
	worktree db.ProjectWorktree,
	wtKey string,
) ([]fleetOwnedTmuxSession, error) {
	if s.db == nil {
		return nil, nil
	}
	stored, err := s.db.ListProjectWorktreeTmuxSessions(ctx, worktree.ID)
	if err != nil {
		return nil, err
	}
	scope := projectWorktreeRuntimeScope(worktree.ID)
	runtimeByKey := runtimeSessionByKey(s, scope)
	out := make([]fleetOwnedTmuxSession, 0, len(stored))
	for _, storedSession := range stored {
		if storedSession.SessionName == "" {
			continue
		}
		key := storedSession.SessionKey
		kind := localruntime.LaunchTargetAgent
		label := storedSession.TargetKey
		if storedSession.TargetKey == "" {
			// Command sessions have no launch target; the launch-time
			// label travels with the stored row.
			kind = localruntime.LaunchTargetCommand
		}
		if storedSession.Label != "" {
			label = storedSession.Label
		}
		session := fleetOwnedTmuxSession{
			Name:             storedSession.SessionName,
			WorktreeKey:      wtKey,
			SessionScopedKey: "session:" + key,
			RuntimeKind:      string(kind),
			Status:           "running",
			Label:            label,
			CreatedAt:        storedSession.CreatedAt,
		}
		if si, ok := runtimeByKey[key]; ok {
			session.RuntimeKind = string(si.Kind)
			session.Status = string(si.Status)
			session.Label = si.Label
		}
		out = append(out, session)
	}
	return out, nil
}

func nonTmuxRuntimeSessionsForWorkspace(
	s *Server,
	sum db.WorkspaceSummary,
	wtKey string,
) []fleet.RawSession {
	if s.runtime == nil {
		return nil
	}
	var out []fleet.RawSession
	for _, si := range s.runtime.ListSessions(sum.ID) {
		if si.TmuxSession != "" {
			continue
		}
		out = append(out, fleet.RawSession{
			ScopedKey:   "session:" + si.Key,
			WorktreeKey: wtKey,
			Status:      string(si.Status),
			RuntimeKind: string(si.Kind),
			Label:       si.Label,
		})
	}
	return out
}

func nonTmuxRuntimeSessionsForProjectWorktree(
	s *Server,
	worktree db.ProjectWorktree,
	wtKey string,
) []fleet.RawSession {
	if s.runtime == nil {
		return nil
	}
	scope := projectWorktreeRuntimeScope(worktree.ID)
	var out []fleet.RawSession
	for _, si := range s.runtime.ListSessions(scope) {
		if si.TmuxSession != "" {
			continue
		}
		out = append(out, fleet.RawSession{
			ScopedKey:   "session:" + si.Key,
			WorktreeKey: wtKey,
			Status:      string(si.Status),
			RuntimeKind: string(si.Kind),
			Label:       si.Label,
		})
	}
	return out
}

func runtimeSessionByKey(
	s *Server,
	workspaceID string,
) map[string]localruntime.SessionInfo {
	out := map[string]localruntime.SessionInfo{}
	if s.runtime == nil {
		return out
	}
	for _, si := range s.runtime.ListSessions(workspaceID) {
		out[si.Key] = si
	}
	return out
}

// addWorktree dedupes worktrees by scoped key (== normalized path). A workspace
// overlay carrying PR or issue identity replaces a bare/primary entry at the
// same path, preserving the primary and stale flags. Linked issue numbers are
// the exception: a registered worktree's explicit links (from the registry
// column) and the workspace-item issue linkage are unioned so neither source
// clobbers the other. Git stats are not part of this merge — they are overlaid
// by path after dedup, so a discovered entry and its workspace overlay resolve
// to the same sample.
func addWorktree(order *[]string, m map[string]*fleet.RawWorktree, w fleet.RawWorktree) {
	if existing, ok := m[w.ScopedKey]; ok {
		if w.LinkedPRNumber != nil || len(w.LinkedIssueNumbers) > 0 {
			merged := w
			if !merged.IsPrimary {
				merged.IsPrimary = existing.IsPrimary
			}
			if merged.RegistryID == "" {
				merged.RegistryID = existing.RegistryID
			}
			merged.IsStale = existing.IsStale || merged.IsStale
			merged.IsHidden = existing.IsHidden || merged.IsHidden
			merged.LinkedIssueNumbers = mergedIssueNumbers(
				existing.LinkedIssueNumbers, w.LinkedIssueNumbers,
			)
			*existing = merged
		}
		return
	}
	cp := w
	m[w.ScopedKey] = &cp
	*order = append(*order, w.ScopedKey)
}

// mergedIssueNumbers returns the sorted, deduped union of two linked-issue
// lists, combining a registered worktree's explicit links with the
// workspace-item issue linkage overlaid at the same path.
func mergedIssueNumbers(a, b []int) []int {
	merged := make([]int, 0, len(a)+len(b))
	merged = append(merged, a...)
	merged = append(merged, b...)
	slices.Sort(merged)
	return slices.Compact(merged)
}

// normPath returns an absolute, cleaned path used as the worktree
// identity (scoped key). It delegates to fleet.NormPath so the snapshot and
// the branch-match link writer normalize paths identically.
func normPath(p string) string {
	return fleet.NormPath(p)
}

func identityKey(platform, host, owner, name string) string {
	return platform + "\x00" + host + "\x00" + owner + "\x00" + name
}

func hostnameOrEmpty() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// platformString reports the OS as the fleet platform token.
func platformString() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return runtime.GOOS
}

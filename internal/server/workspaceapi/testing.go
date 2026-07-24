package workspaceapi

import (
	"context"
	"time"

	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/workspace"
)

// DiffEventData is the workspace diff invalidation event payload.
type DiffEventData = workspaceDiffEventData

// DiffSnapshotInfo exposes only the opaque identity required by wire tests.
type DiffSnapshotInfo struct {
	Revision uint64
	Version  string
}

// ExpireDefaultHeadValidation makes the default HEAD entry eligible for a
// validation probe without changing its published snapshot.
func (h *Handler) ExpireDefaultHeadValidation(
	workspaceID, worktreePath string, retryAfter time.Time,
) bool {
	key := workspaceDiffLogicalKey{WorkspaceID: workspaceID, Spec: workspace.DiffSnapshotSpec{
		WorktreePath: worktreePath, Base: workspace.WorktreeDiffBaseHead,
	}}
	h.workspaceDiffCache.mu.Lock()
	defer h.workspaceDiffCache.mu.Unlock()
	entry := h.workspaceDiffCache.peekEntryLocked(key)
	if entry == nil {
		return false
	}
	entry.validatedAt = time.Now().Add(-workspaceDiffCacheFreshFor - time.Second)
	entry.retryAfter = retryAfter
	return true
}

// ValidateDefaultHead validates the default HEAD cache entry now.
func (h *Handler) ValidateDefaultHead(
	ctx context.Context, workspaceID, worktreePath string,
) error {
	return h.workspaceDiffCache.validate(ctx, workspaceDiffLogicalKey{
		WorkspaceID: workspaceID,
		Spec: workspace.DiffSnapshotSpec{
			WorktreePath: worktreePath, Base: workspace.WorktreeDiffBaseHead,
		},
	})
}

// AddDiffCachePressureEntries fills inactive cache cost while preserving
// active pair leases.
func (h *Handler) AddDiffCachePressureEntries(now time.Time) bool {
	entry := func() *workspaceDiffCacheEntry {
		return &workspaceDiffCacheEntry{
			snapshot:   &workspaceDiffSnapshot{SizeBytes: workspaceDiffCacheMaxBytes},
			lastAccess: now,
		}
	}
	h.workspaceDiffCache.mu.Lock()
	defer h.workspaceDiffCache.mu.Unlock()
	first := h.workspaceDiffCache.storeEntryLocked(
		workspaceDiffLogicalKey{WorkspaceID: "pressure-1"}, entry(), now,
	)
	second := h.workspaceDiffCache.storeEntryLocked(
		workspaceDiffLogicalKey{WorkspaceID: "pressure-2"}, entry(), now,
	)
	return first && second
}

// DiffTestDeps replaces the cache's Git operations for deterministic tests.
type DiffTestDeps struct {
	Resolve     func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error)
	Fingerprint func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error)
	Prepare     func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error)
}

// SetDiffTestDeps replaces provided cache operations and returns a restore
// function.
func (h *Handler) SetDiffTestDeps(deps DiffTestDeps) func() {
	prior := h.workspaceDiffCache.deps
	if deps.Resolve != nil {
		h.workspaceDiffCache.deps.resolve = deps.Resolve
	}
	if deps.Fingerprint != nil {
		h.workspaceDiffCache.deps.fingerprint = deps.Fingerprint
	}
	if deps.Prepare != nil {
		h.workspaceDiffCache.deps.prepare = deps.Prepare
	}
	return func() { h.workspaceDiffCache.deps = prior }
}

// WrapDiffPrepare installs a preparation wrapper and returns a restore
// function.
func (h *Handler) WrapDiffPrepare(
	wrapper func(
		func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error),
	) func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error),
) func() {
	prior := h.workspaceDiffCache.deps.prepare
	h.workspaceDiffCache.deps.prepare = wrapper(prior)
	return func() { h.workspaceDiffCache.deps.prepare = prior }
}

// SelectedDiffCount reports active selection leases for a workspace.
func (h *Handler) SelectedDiffCount(workspaceID string) int {
	h.workspaceDiffCache.mu.Lock()
	defer h.workspaceDiffCache.mu.Unlock()
	return h.workspaceDiffCache.selected[workspaceID]
}

// DiffSnapshotForBase prepares and returns an opaque snapshot identity.
func (h *Handler) DiffSnapshotForBase(
	ctx context.Context, workspaceID string, base workspace.WorktreeDiffBase,
) (DiffSnapshotInfo, error) {
	req, err := h.workspaceDiffRequest(ctx, workspaceID, string(base))
	if err != nil {
		return DiffSnapshotInfo{}, err
	}
	snapshot, _, err := h.workspaceDiffCache.Get(ctx, h.workspaceDiffCacheKey(req, false))
	if err != nil {
		return DiffSnapshotInfo{}, err
	}
	return DiffSnapshotInfo{Revision: snapshot.Revision, Version: snapshot.Version}, nil
}

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/workspace"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"
)

const (
	workspaceDiffCacheFreshFor  = 15 * time.Second
	workspaceDiffCacheIdleTTL   = 10 * time.Minute
	workspaceDiffCacheRetryWait = 5 * time.Second
	workspaceDiffCacheMaxBytes  = int64(128 << 20)
)

var errWorkspaceDiffMovedDuringPreparation = errors.New("workspace diff moved during preparation")
var errWorkspaceDiffBaseUnavailable = errors.New("workspace diff base is unavailable")

type workspaceDiffLogicalKey struct {
	WorkspaceID string
	Spec        workspace.DiffSnapshotSpec
}

type workspaceDiffSnapshot struct {
	Resolved    workspace.ResolvedDiffSnapshotSpec
	Fingerprint workspace.DiffFingerprint
	Revision    uint64
	Version     string
	Diff        *gitclone.DiffResult
	Files       []gitclone.DiffFile
	SizeBytes   int64
}

type workspaceDiffCacheState string

const (
	workspaceDiffCacheHit       workspaceDiffCacheState = "hit"
	workspaceDiffCacheStale     workspaceDiffCacheState = "stale"
	workspaceDiffCacheMiss      workspaceDiffCacheState = "miss"
	workspaceDiffCacheCoalesced workspaceDiffCacheState = "coalesced"
)

type workspaceDiffCacheDeps struct {
	now         func() time.Time
	resolve     func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error)
	fingerprint func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error)
	prepare     func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error)
	onChanged   func(workspaceID string, revision uint64, version string)
	onColdWait  func()
}

type workspaceDiffCacheEntry struct {
	snapshot    *workspaceDiffSnapshot
	validatedAt time.Time
	lastAccess  time.Time
	retryAfter  time.Time
}

type workspaceDiffCache struct {
	root       context.Context
	deps       workspaceDiffCacheDeps
	generation string

	mu         sync.Mutex
	entries    map[workspaceDiffLogicalKey]*workspaceDiffCacheEntry
	inFlight   map[workspaceDiffLogicalKey]bool
	selected   map[string]int
	active     map[string]map[workspaceDiffLogicalKey]time.Time
	nextRev    uint64
	totalBytes int64
	group      singleflight.Group
	wg         sync.WaitGroup
}

func newWorkspaceDiffCache(
	root context.Context,
	deps workspaceDiffCacheDeps,
) *workspaceDiffCache {
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.resolve == nil {
		deps.resolve = workspace.ResolveDiffSnapshotSpec
	}
	if deps.fingerprint == nil {
		deps.fingerprint = workspace.FingerprintDiffSnapshot
	}
	if deps.prepare == nil {
		deps.prepare = workspace.PrepareDiffSnapshot
	}
	c := &workspaceDiffCache{
		root:       root,
		deps:       deps,
		generation: newWorkspaceDiffCacheGeneration(),
		entries:    make(map[workspaceDiffLogicalKey]*workspaceDiffCacheEntry),
		inFlight:   make(map[workspaceDiffLogicalKey]bool),
		selected:   make(map[string]int),
		active:     make(map[string]map[workspaceDiffLogicalKey]time.Time),
	}
	c.wg.Add(1)
	go c.validationLoop()
	return c
}

func newWorkspaceDiffCacheGeneration() string {
	var data [8]byte
	if _, err := rand.Read(data[:]); err == nil {
		return hex.EncodeToString(data[:])
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

func (c *workspaceDiffCache) Get(
	ctx context.Context,
	key workspaceDiffLogicalKey,
) (*workspaceDiffSnapshot, workspaceDiffCacheState, error) {
	now := c.deps.now()
	c.mu.Lock()
	if entry := c.entries[key]; entry != nil {
		entry.lastAccess = now
		c.markActiveLocked(key, now)
		fresh := now.Sub(entry.validatedAt) <= workspaceDiffCacheFreshFor
		retryAllowed := !now.Before(entry.retryAfter)
		snapshot := cloneWorkspaceDiffSnapshot(entry.snapshot, !fresh)
		c.mu.Unlock()
		state := workspaceDiffCacheHit
		if !fresh {
			state = workspaceDiffCacheStale
			if retryAllowed {
				c.validateAsync(key)
			}
		}
		c.setSpanAttributes(ctx, key, snapshot, state)
		return snapshot, state, nil
	}
	leader := !c.inFlight[key]
	if leader {
		c.inFlight[key] = true
	}
	c.mu.Unlock()

	resultCh := c.group.DoChan(c.singleflightKey(key), func() (any, error) {
		defer func() {
			c.mu.Lock()
			delete(c.inFlight, key)
			c.mu.Unlock()
		}()
		return c.refresh(c.root, key)
	})
	if c.deps.onColdWait != nil {
		c.deps.onColdWait()
	}
	select {
	case <-ctx.Done():
		return nil, workspaceDiffCacheMiss, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return nil, workspaceDiffCacheMiss, result.Err
		}
		snapshot := cloneWorkspaceDiffSnapshot(result.Val.(*workspaceDiffSnapshot), false)
		state := workspaceDiffCacheMiss
		if !leader {
			state = workspaceDiffCacheCoalesced
		}
		c.setSpanAttributes(ctx, key, snapshot, state)
		return snapshot, state, nil
	}
}

func (c *workspaceDiffCache) validate(
	ctx context.Context,
	key workspaceDiffLogicalKey,
) error {
	resultCh := c.group.DoChan(c.singleflightKey(key), func() (any, error) {
		return c.refresh(c.root, key)
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-resultCh:
		return result.Err
	}
}

func (c *workspaceDiffCache) refresh(
	ctx context.Context,
	key workspaceDiffLogicalKey,
) (*workspaceDiffSnapshot, error) {
	resolved, ok, err := c.deps.resolve(ctx, key.Spec)
	if err != nil {
		c.recordFailure(key)
		return nil, err
	}
	if !ok {
		err = errWorkspaceDiffBaseUnavailable
		c.recordFailure(key)
		return nil, err
	}
	before, err := c.deps.fingerprint(ctx, resolved)
	if err != nil {
		c.recordFailure(key)
		return nil, err
	}

	now := c.deps.now()
	c.mu.Lock()
	entry := c.entries[key]
	if entry != nil && entry.snapshot.Fingerprint == before {
		entry.validatedAt = now
		entry.retryAfter = time.Time{}
		entry.lastAccess = now
		snapshot := entry.snapshot
		c.mu.Unlock()
		return snapshot, nil
	}
	c.mu.Unlock()

	diff, err := c.deps.prepare(ctx, resolved)
	if err != nil {
		c.recordFailure(key)
		return nil, err
	}
	afterResolved, ok, err := c.deps.resolve(ctx, key.Spec)
	if err != nil || !ok {
		if err == nil {
			err = errWorkspaceDiffMovedDuringPreparation
		}
		c.recordFailure(key)
		return nil, err
	}
	after, err := c.deps.fingerprint(ctx, afterResolved)
	if err != nil {
		c.recordFailure(key)
		return nil, err
	}
	if before != after || resolved.BaseOID != afterResolved.BaseOID || resolved.HeadOID != afterResolved.HeadOID {
		c.recordFailure(key)
		return nil, errWorkspaceDiffMovedDuringPreparation
	}

	files := workspaceDiffFilesProjection(diff.Files)
	sizeBytes := approximateWorkspaceDiffBytes(diff, files)
	c.mu.Lock()
	previous := c.entries[key]
	c.nextRev++
	snapshot := &workspaceDiffSnapshot{
		Resolved:    afterResolved,
		Fingerprint: after,
		Revision:    c.nextRev,
		Version:     fmt.Sprintf("%s:%d", c.generation, c.nextRev),
		Diff:        diff,
		Files:       files,
		SizeBytes:   sizeBytes,
	}
	if previous != nil {
		c.totalBytes -= previous.snapshot.SizeBytes
	}
	c.entries[key] = &workspaceDiffCacheEntry{
		snapshot:    snapshot,
		validatedAt: now,
		lastAccess:  now,
	}
	c.totalBytes += sizeBytes
	c.markActiveLocked(key, now)
	changed := previous != nil && previous.snapshot.Fingerprint != after
	c.evictLocked(now)
	c.mu.Unlock()
	if changed && c.deps.onChanged != nil {
		c.deps.onChanged(key.WorkspaceID, snapshot.Revision, snapshot.Version)
	}
	return snapshot, nil
}

func (c *workspaceDiffCache) recordFailure(key workspaceDiffLogicalKey) {
	c.mu.Lock()
	if entry := c.entries[key]; entry != nil {
		entry.retryAfter = c.deps.now().Add(workspaceDiffCacheRetryWait)
	}
	c.mu.Unlock()
}

func (c *workspaceDiffCache) validateAsync(key workspaceDiffLogicalKey) {
	c.wg.Go(func() {
		_ = c.validate(c.root, key)
	})
}

func (c *workspaceDiffCache) Select(
	workspaceID string,
	resolveKey func(context.Context) (workspaceDiffLogicalKey, error),
) func() {
	c.mu.Lock()
	first := c.selected[workspaceID] == 0
	c.selected[workspaceID]++
	c.mu.Unlock()
	if first && resolveKey != nil {
		c.wg.Go(func() {
			key, err := resolveKey(c.root)
			if err != nil {
				return
			}
			c.MarkActive(key)
			_, _, _ = c.Get(c.root, key)
		})
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if c.selected[workspaceID] <= 1 {
				delete(c.selected, workspaceID)
				delete(c.active, workspaceID)
			} else {
				c.selected[workspaceID]--
			}
			c.mu.Unlock()
		})
	}
}

func (c *workspaceDiffCache) MarkActive(key workspaceDiffLogicalKey) {
	c.mu.Lock()
	c.markActiveLocked(key, c.deps.now())
	c.mu.Unlock()
}

func (c *workspaceDiffCache) markActiveLocked(key workspaceDiffLogicalKey, now time.Time) {
	if c.selected[key.WorkspaceID] == 0 {
		return
	}
	keys := c.active[key.WorkspaceID]
	if keys == nil {
		keys = make(map[workspaceDiffLogicalKey]time.Time)
		c.active[key.WorkspaceID] = keys
	}
	keys[key] = now
}

func (c *workspaceDiffCache) ValidateSelected() {
	now := c.deps.now()
	c.mu.Lock()
	keys := make([]workspaceDiffLogicalKey, 0)
	for workspaceID, active := range c.active {
		if c.selected[workspaceID] == 0 {
			continue
		}
		for key, accessed := range active {
			if now.Sub(accessed) > workspaceDiffCacheIdleTTL {
				continue
			}
			if entry := c.entries[key]; entry != nil && now.Before(entry.retryAfter) {
				continue
			}
			keys = append(keys, key)
		}
	}
	c.mu.Unlock()
	for _, key := range keys {
		c.validateAsync(key)
	}
}

func (c *workspaceDiffCache) validationLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(workspaceDiffCacheFreshFor)
	defer ticker.Stop()
	for {
		select {
		case <-c.root.Done():
			return
		case <-ticker.C:
			c.ValidateSelected()
		}
	}
}

func (c *workspaceDiffCache) Wait() {
	c.wg.Wait()
}

func (c *workspaceDiffCache) singleflightKey(key workspaceDiffLogicalKey) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t",
		key.WorkspaceID,
		key.Spec.WorktreePath,
		key.Spec.Base,
		key.Spec.MergeTargetBranch,
		key.Spec.FromSHA,
		key.Spec.ToSHA,
		key.Spec.HideWhitespace,
	)
}

func (c *workspaceDiffCache) setSpanAttributes(
	ctx context.Context,
	key workspaceDiffLogicalKey,
	snapshot *workspaceDiffSnapshot,
	state workspaceDiffCacheState,
) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(
		attribute.String("workspace.id", key.WorkspaceID),
		attribute.String("workspace.diff.cache_result", string(state)),
		attribute.Int64("workspace.diff.snapshot_bytes", snapshot.SizeBytes),
		attribute.Int64("workspace.diff.revision", int64(snapshot.Revision)),
	)
}

func (c *workspaceDiffCache) evictLocked(now time.Time) {
	for key, entry := range c.entries {
		if c.activeKeyLocked(key) || now.Sub(entry.lastAccess) <= workspaceDiffCacheIdleTTL {
			continue
		}
		c.removeEntryLocked(key, entry)
	}
	if c.totalBytes <= workspaceDiffCacheMaxBytes {
		return
	}
	type candidate struct {
		key   workspaceDiffLogicalKey
		entry *workspaceDiffCacheEntry
	}
	candidates := make([]candidate, 0, len(c.entries))
	for key, entry := range c.entries {
		if !c.activeKeyLocked(key) {
			candidates = append(candidates, candidate{key: key, entry: entry})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].entry.lastAccess.Before(candidates[j].entry.lastAccess)
	})
	for _, candidate := range candidates {
		if c.totalBytes <= workspaceDiffCacheMaxBytes {
			break
		}
		c.removeEntryLocked(candidate.key, candidate.entry)
	}
}

func (c *workspaceDiffCache) activeKeyLocked(key workspaceDiffLogicalKey) bool {
	if c.selected[key.WorkspaceID] == 0 {
		return false
	}
	_, ok := c.active[key.WorkspaceID][key]
	return ok
}

func (c *workspaceDiffCache) removeEntryLocked(
	key workspaceDiffLogicalKey,
	entry *workspaceDiffCacheEntry,
) {
	delete(c.entries, key)
	c.totalBytes -= entry.snapshot.SizeBytes
}

func workspaceDiffFilesProjection(files []gitclone.DiffFile) []gitclone.DiffFile {
	projection := make([]gitclone.DiffFile, len(files))
	copy(projection, files)
	for i := range projection {
		projection[i].Patch = ""
		projection[i].Hunks = []gitclone.Hunk{}
	}
	return projection
}

func cloneWorkspaceDiffSnapshot(
	snapshot *workspaceDiffSnapshot,
	stale bool,
) *workspaceDiffSnapshot {
	clone := *snapshot
	diff := *snapshot.Diff
	diff.Stale = stale
	diff.Files = append([]gitclone.DiffFile(nil), snapshot.Diff.Files...)
	clone.Diff = &diff
	clone.Files = append([]gitclone.DiffFile(nil), snapshot.Files...)
	return &clone
}

func approximateWorkspaceDiffBytes(
	diff *gitclone.DiffResult,
	files []gitclone.DiffFile,
) int64 {
	size := int64(64)
	for _, list := range [][]gitclone.DiffFile{diff.Files, files} {
		for _, file := range list {
			size += int64(len(file.Path) + len(file.OldPath) + len(file.Status) + len(file.Patch) + 96)
			for _, hunk := range file.Hunks {
				size += int64(len(hunk.Section) + 48)
				for _, line := range hunk.Lines {
					size += int64(len(line.Type) + len(line.Content) + 32)
				}
			}
		}
	}
	return size
}

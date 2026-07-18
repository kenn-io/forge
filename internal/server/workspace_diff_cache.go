package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/workspace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"
)

const (
	workspaceDiffCacheFreshFor      = 15 * time.Second
	workspaceDiffCacheIdleTTL       = 10 * time.Minute
	workspaceDiffCacheRetryWait     = 5 * time.Second
	workspaceDiffPreparationTimeout = 30 * time.Second
	workspaceDiffValidationPoll     = time.Second
	workspaceDiffCacheMaxBytes      = int64(128 << 20)
	workspaceDiffCachePairRetention = time.Minute
)

var errWorkspaceDiffMovedDuringPreparation = errors.New("workspace diff moved during preparation")
var errWorkspaceDiffBaseUnavailable = errors.New("workspace diff base is unavailable")

var workspaceDiffCacheTracer = otel.Tracer("go.kenn.io/middleman/internal/server/workspace-diff-cache")

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
	after       func(time.Duration) <-chan time.Time
	resolve     func(context.Context, workspace.DiffSnapshotSpec) (workspace.ResolvedDiffSnapshotSpec, bool, error)
	fingerprint func(context.Context, workspace.ResolvedDiffSnapshotSpec) (workspace.DiffFingerprint, error)
	prepare     func(context.Context, workspace.ResolvedDiffSnapshotSpec) (*gitclone.DiffResult, error)
	onChanged   func(workspaceID string, revision uint64, version string)
	onReady     func(workspaceID string, revision uint64, version string)
	onColdWait  func()
	maxBytes    int64
}

type workspaceDiffCacheEntry struct {
	snapshot      *workspaceDiffSnapshot
	validatedAt   time.Time
	lastAccess    time.Time
	retryAfter    time.Time
	retainedUntil time.Time
	costExempt    bool
}

type workspaceDiffCache struct {
	root       context.Context
	deps       workspaceDiffCacheDeps
	generation string

	mu               sync.Mutex
	protectedEntries *ttlcache.Cache[workspaceDiffLogicalKey, *workspaceDiffCacheEntry]
	inactiveEntries  *ttlcache.Cache[workspaceDiffLogicalKey, *workspaceDiffCacheEntry]
	inFlight         map[workspaceDiffLogicalKey]bool
	selected         map[string]int
	active           map[string]map[workspaceDiffLogicalKey]time.Time
	selectionCancel  map[string]context.CancelFunc
	nextRev          uint64
	group            singleflight.Group
	wg               sync.WaitGroup

	validationMu      sync.Mutex
	validationCond    *sync.Cond
	validationQueue   []workspaceDiffLogicalKey
	validationQueued  map[workspaceDiffLogicalKey]struct{}
	validationStopped bool
}

func newWorkspaceDiffCache(
	root context.Context,
	deps workspaceDiffCacheDeps,
) *workspaceDiffCache {
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.after == nil {
		deps.after = time.After
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
	if deps.maxBytes == 0 {
		deps.maxBytes = workspaceDiffCacheMaxBytes
	}
	protectedEntries := ttlcache.New(
		ttlcache.WithTTL[workspaceDiffLogicalKey, *workspaceDiffCacheEntry](workspaceDiffCacheIdleTTL),
		ttlcache.WithDisableTouchOnHit[workspaceDiffLogicalKey, *workspaceDiffCacheEntry](),
	)
	inactiveEntries := ttlcache.New(
		ttlcache.WithTTL[workspaceDiffLogicalKey, *workspaceDiffCacheEntry](workspaceDiffCacheIdleTTL),
		ttlcache.WithDisableTouchOnHit[workspaceDiffLogicalKey, *workspaceDiffCacheEntry](),
		ttlcache.WithMaxCost(uint64(deps.maxBytes), workspaceDiffCacheEntryCost),
	)
	c := &workspaceDiffCache{
		root:             root,
		deps:             deps,
		generation:       newWorkspaceDiffCacheGeneration(),
		protectedEntries: protectedEntries,
		inactiveEntries:  inactiveEntries,
		inFlight:         make(map[workspaceDiffLogicalKey]bool),
		selected:         make(map[string]int),
		active:           make(map[string]map[workspaceDiffLogicalKey]time.Time),
		selectionCancel:  make(map[string]context.CancelFunc),
		validationQueued: make(map[workspaceDiffLogicalKey]struct{}),
	}
	c.validationCond = sync.NewCond(&c.validationMu)
	c.wg.Add(1)
	go c.validationWorker()
	c.wg.Go(func() {
		<-root.Done()
		c.validationMu.Lock()
		c.validationStopped = true
		c.validationCond.Broadcast()
		c.validationMu.Unlock()
	})
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
	if entry := c.getEntryLocked(key); entry != nil {
		updated := *entry
		updated.lastAccess = now
		updated.retainedUntil = now.Add(workspaceDiffCachePairRetention)
		updated.costExempt = true
		c.markActiveLocked(key, now)
		c.storeEntryLocked(key, &updated, now)
		fresh := now.Sub(updated.validatedAt) <= workspaceDiffCacheFreshFor
		retryAllowed := !now.Before(updated.retryAfter)
		snapshot := cloneWorkspaceDiffSnapshot(updated.snapshot, !fresh)
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
	c.markActiveLocked(key, now)
	c.mu.Unlock()

	resultCh := c.group.DoChan(c.singleflightKey(key), func() (any, error) {
		defer func() {
			c.mu.Lock()
			delete(c.inFlight, key)
			c.mu.Unlock()
		}()
		return c.refreshShared(key)
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
		snapshot := result.Val.(*workspaceDiffSnapshot)
		if entry := c.touchEntry(key, now); entry != nil {
			snapshot = entry.snapshot
		}
		snapshot = cloneWorkspaceDiffSnapshot(snapshot, false)
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
	resultCh := c.validationResult(key)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-resultCh:
		return result.Err
	}
}

func (c *workspaceDiffCache) validationResult(
	key workspaceDiffLogicalKey,
) <-chan singleflight.Result {
	return c.group.DoChan(c.singleflightKey(key), func() (any, error) {
		return c.refreshShared(key)
	})
}

func (c *workspaceDiffCache) refreshShared(
	key workspaceDiffLogicalKey,
) (*workspaceDiffSnapshot, error) {
	ctx, cancel := context.WithTimeout(c.root, workspaceDiffPreparationTimeout)
	defer cancel()
	return c.refresh(ctx, key)
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
	entry := c.peekEntryLocked(key)
	if entry != nil && entry.snapshot.Fingerprint == before {
		entry.validatedAt = now
		entry.retryAfter = time.Time{}
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
	previous := c.peekEntryLocked(key)
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
	lastAccess := now
	if previous != nil {
		lastAccess = previous.lastAccess
	}
	entry = &workspaceDiffCacheEntry{
		snapshot:      snapshot,
		validatedAt:   now,
		lastAccess:    lastAccess,
		retainedUntil: now.Add(workspaceDiffCachePairRetention),
		costExempt:    true,
	}
	c.storeEntryLocked(key, entry, now)
	changed := previous != nil && previous.snapshot.Fingerprint != after
	c.mu.Unlock()
	if changed && c.deps.onChanged != nil {
		c.deps.onChanged(key.WorkspaceID, snapshot.Revision, snapshot.Version)
	}
	return snapshot, nil
}

func (c *workspaceDiffCache) recordFailure(key workspaceDiffLogicalKey) {
	c.mu.Lock()
	if entry := c.peekEntryLocked(key); entry != nil {
		entry.retryAfter = c.deps.now().Add(workspaceDiffCacheRetryWait)
	}
	c.mu.Unlock()
}

func (c *workspaceDiffCache) peekEntry(key workspaceDiffLogicalKey) *workspaceDiffCacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peekEntryLocked(key)
}

func (c *workspaceDiffCache) peekEntryLocked(key workspaceDiffLogicalKey) *workspaceDiffCacheEntry {
	return c.getEntryLocked(key)
}

func (c *workspaceDiffCache) touchEntry(
	key workspaceDiffLogicalKey,
	now time.Time,
) *workspaceDiffCacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.getEntryLocked(key)
	if entry == nil {
		return nil
	}
	updated := *entry
	updated.lastAccess = now
	updated.retainedUntil = now.Add(workspaceDiffCachePairRetention)
	updated.costExempt = true
	c.markActiveLocked(key, now)
	if !c.storeEntryLocked(key, &updated, now) {
		return nil
	}
	return &updated
}

func (c *workspaceDiffCache) storeEntryLocked(
	key workspaceDiffLogicalKey,
	entry *workspaceDiffCacheEntry,
	now time.Time,
) bool {
	ttl := workspaceDiffCacheIdleTTL - now.Sub(entry.lastAccess)
	active := c.activeKeyLocked(key)
	if active {
		ttl = ttlcache.NoTTL
	} else if ttl <= 0 {
		c.deleteEntryLocked(key)
		return false
	}
	if entry.costExempt || active {
		c.inactiveEntries.Delete(key)
		c.protectedEntries.Set(key, entry, ttl)
		return c.protectedEntries.Has(key)
	}
	c.protectedEntries.Delete(key)
	c.inactiveEntries.Set(key, entry, ttl)
	return c.inactiveEntries.Has(key)
}

func (c *workspaceDiffCache) getEntryLocked(key workspaceDiffLogicalKey) *workspaceDiffCacheEntry {
	item := c.protectedEntries.Get(key)
	if item == nil {
		item = c.inactiveEntries.Get(key)
	}
	if item == nil {
		return nil
	}
	return item.Value()
}

func (c *workspaceDiffCache) deleteEntryLocked(key workspaceDiffLogicalKey) {
	c.protectedEntries.Delete(key)
	c.inactiveEntries.Delete(key)
}

func (c *workspaceDiffCache) allEntriesLocked() map[workspaceDiffLogicalKey]*ttlcache.Item[workspaceDiffLogicalKey, *workspaceDiffCacheEntry] {
	entries := c.inactiveEntries.Items()
	maps.Copy(entries, c.protectedEntries.Items())
	return entries
}

func workspaceDiffCacheEntryCost(
	item ttlcache.CostItem[workspaceDiffLogicalKey, *workspaceDiffCacheEntry],
) uint64 {
	entry := item.Value
	if entry == nil || entry.snapshot == nil || entry.snapshot.SizeBytes <= 0 {
		return 0
	}
	return uint64(entry.snapshot.SizeBytes)
}

func (c *workspaceDiffCache) validateAsync(key workspaceDiffLogicalKey) {
	c.validationMu.Lock()
	defer c.validationMu.Unlock()
	if c.validationStopped {
		return
	}
	if _, exists := c.validationQueued[key]; exists {
		return
	}
	c.validationQueued[key] = struct{}{}
	c.validationQueue = append(c.validationQueue, key)
	c.validationCond.Signal()
}

func (c *workspaceDiffCache) validationWorker() {
	defer c.wg.Done()
	for {
		c.validationMu.Lock()
		for len(c.validationQueue) == 0 && !c.validationStopped {
			c.validationCond.Wait()
		}
		if c.validationStopped {
			c.validationMu.Unlock()
			return
		}
		key := c.validationQueue[0]
		c.validationQueue = c.validationQueue[1:]
		c.validationMu.Unlock()

		ctx, span := workspaceDiffCacheTracer.Start(c.root, "workspace.diff.validate.background",
			trace.WithAttributes(
				attribute.String("workspace.id", key.WorkspaceID),
				attribute.String("workspace.diff.base", string(key.Spec.Base)),
			),
		)
		if err := c.validate(ctx, key); err != nil {
			span.RecordError(err)
		}
		span.End()

		c.validationMu.Lock()
		delete(c.validationQueued, key)
		c.validationMu.Unlock()
	}
}

func (c *workspaceDiffCache) Select(
	workspaceID string,
	resolveKey func(context.Context) (workspaceDiffLogicalKey, error),
) func() {
	c.mu.Lock()
	first := c.selected[workspaceID] == 0
	c.selected[workspaceID]++
	var selectionCtx context.Context
	if first && resolveKey != nil {
		var cancel context.CancelFunc
		selectionCtx, cancel = context.WithCancel(c.root)
		c.selectionCancel[workspaceID] = cancel
	}
	c.mu.Unlock()
	if first && resolveKey != nil {
		c.wg.Go(func() {
			c.prewarmSelected(selectionCtx, resolveKey)
		})
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			var cancel context.CancelFunc
			if c.selected[workspaceID] <= 1 {
				cancel = c.selectionCancel[workspaceID]
				delete(c.selectionCancel, workspaceID)
				delete(c.selected, workspaceID)
			} else {
				c.selected[workspaceID]--
			}
			c.maintainLocked(c.deps.now())
			c.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		})
	}
}

func (c *workspaceDiffCache) prewarmSelected(
	ctx context.Context,
	resolveKey func(context.Context) (workspaceDiffLogicalKey, error),
) {
	for {
		key, err := resolveKey(ctx)
		if err == nil {
			c.MarkActive(key)
			var snapshot *workspaceDiffSnapshot
			var state workspaceDiffCacheState
			snapshot, state, err = c.Get(ctx, key)
			if err == nil &&
				(state == workspaceDiffCacheMiss || state == workspaceDiffCacheCoalesced) &&
				c.deps.onReady != nil {
				c.deps.onReady(key.WorkspaceID, snapshot.Revision, snapshot.Version)
			}
		}
		if err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-c.deps.after(workspaceDiffCacheRetryWait):
		}
	}
}

func (c *workspaceDiffCache) MarkActive(key workspaceDiffLogicalKey) {
	c.mu.Lock()
	now := c.deps.now()
	c.markActiveLocked(key, now)
	if entry := c.getEntryLocked(key); entry != nil {
		updated := *entry
		updated.retainedUntil = now.Add(workspaceDiffCachePairRetention)
		updated.costExempt = true
		c.storeEntryLocked(key, &updated, now)
	}
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
	c.validateSelected(false)
}

func (c *workspaceDiffCache) RevalidateSelected() {
	c.validateSelected(true)
}

func (c *workspaceDiffCache) validateSelected(force bool) {
	now := c.deps.now()
	c.mu.Lock()
	c.maintainLocked(now)
	keys := make([]workspaceDiffLogicalKey, 0)
	for workspaceID, active := range c.active {
		if c.selected[workspaceID] == 0 {
			continue
		}
		for key := range active {
			entry := c.peekEntryLocked(key)
			if entry == nil {
				continue
			}
			if (!force && now.Sub(entry.validatedAt) < workspaceDiffCacheFreshFor-workspaceDiffValidationPoll) ||
				now.Before(entry.retryAfter) {
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

func (c *workspaceDiffCache) RevalidateWorkspace(workspaceID string) {
	c.mu.Lock()
	items := c.allEntriesLocked()
	keys := make([]workspaceDiffLogicalKey, 0)
	for key := range items {
		if key.WorkspaceID == workspaceID {
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
	ticker := time.NewTicker(workspaceDiffValidationPoll)
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

func (c *workspaceDiffCache) maintain(now time.Time) {
	c.mu.Lock()
	c.maintainLocked(now)
	c.mu.Unlock()
	c.protectedEntries.DeleteExpired()
	c.inactiveEntries.DeleteExpired()
}

func (c *workspaceDiffCache) maintainLocked(now time.Time) {
	for workspaceID, keys := range c.active {
		for key, accessed := range keys {
			if now.Sub(accessed) > workspaceDiffCacheIdleTTL {
				delete(keys, key)
				c.deleteEntryLocked(key)
			}
		}
		if len(keys) == 0 {
			delete(c.active, workspaceID)
		}
	}
	for key, item := range c.allEntriesLocked() {
		entry := item.Value()
		if c.activeKeyLocked(key) {
			continue
		}
		if now.Sub(entry.lastAccess) > workspaceDiffCacheIdleTTL {
			c.deleteEntryLocked(key)
			continue
		}
		if entry.costExempt && !now.Before(entry.retainedUntil) {
			updated := *entry
			updated.costExempt = false
			c.storeEntryLocked(key, &updated, now)
		}
	}
}

func (c *workspaceDiffCache) activeKeyLocked(key workspaceDiffLogicalKey) bool {
	if c.selected[key.WorkspaceID] == 0 {
		return false
	}
	_, ok := c.active[key.WorkspaceID][key]
	return ok
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

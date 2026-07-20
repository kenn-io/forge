package server

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/jellydator/ttlcache/v3"
)

const (
	kataSnapshotCacheTTL      = 5 * time.Second
	kataSnapshotCacheCapacity = 128
)

type kataSnapshotKey struct {
	DaemonID          string
	DaemonFingerprint string
	Scope             string
	ProjectUID        string
	Authority         string
}

type kataProjectSummary struct {
	ID          int64
	UID         string
	Name        string
	Metadata    map[string]any
	Revision    int64
	CreatedAt   time.Time
	DeletedAt   *time.Time
	OpenCount   int64
	ClosedCount int64
}

type kataLinkPeer struct {
	UID     string
	ShortID string
}

type kataChildCounts struct {
	Open  int64
	Total int64
}

type kataTaskSummary struct {
	ID            int64
	UID           string
	ProjectID     int64
	ShortID       string
	QualifiedID   string
	Title         string
	Body          string
	Status        string
	ProjectUID    string
	ProjectName   string
	Metadata      map[string]any
	Revision      int64
	Owner         *string
	Author        string
	Priority      *int64
	Labels        []string
	Parent        *kataLinkPeer
	Blocks        []kataLinkPeer
	BlockedBy     []kataLinkPeer
	Related       []kataLinkPeer
	ChildCounts   *kataChildCounts
	RecurrenceID  *int64
	OccurrenceKey *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ClosedReason  *string
	ClosedAt      *time.Time
	DeletedAt     *time.Time
}

type kataAuthoritySnapshot struct {
	Generation      uint64
	FetchedAt       time.Time
	Projects        []kataProjectSummary
	MemberIssueUIDs []string
	Issues          []kataTaskSummary
}

type kataSnapshotCache struct {
	mu             sync.Mutex
	entries        *ttlcache.Cache[kataSnapshotKey, kataAuthoritySnapshot]
	keysByDaemon   map[string]map[kataSnapshotKey]struct{}
	daemonEpochs   map[string]uint64
	cleanupEvery   time.Duration
	stopOnEviction func()
}

func newKataSnapshotCache() *kataSnapshotCache {
	return newKataSnapshotCacheWithConfig(kataSnapshotCacheTTL, kataSnapshotCacheCapacity)
}

func newKataSnapshotCacheWithConfig(ttl time.Duration, capacity uint64) *kataSnapshotCache {
	entries := ttlcache.New(
		ttlcache.WithTTL[kataSnapshotKey, kataAuthoritySnapshot](ttl),
		ttlcache.WithCapacity[kataSnapshotKey, kataAuthoritySnapshot](capacity),
		ttlcache.WithDisableTouchOnHit[kataSnapshotKey, kataAuthoritySnapshot](),
	)
	cache := &kataSnapshotCache{
		entries:      entries,
		keysByDaemon: make(map[string]map[kataSnapshotKey]struct{}),
		daemonEpochs: make(map[string]uint64),
		cleanupEvery: ttl,
	}
	cache.stopOnEviction = entries.OnEviction(func(_ context.Context, _ ttlcache.EvictionReason, item *ttlcache.Item[kataSnapshotKey, kataAuthoritySnapshot]) {
		cache.removeEvictedKey(item.Key())
	})
	return cache
}

func (c *kataSnapshotCache) get(key kataSnapshotKey) (kataAuthoritySnapshot, bool) {
	if c == nil || c.entries == nil {
		return kataAuthoritySnapshot{}, false
	}
	item := c.entries.Get(key)
	if item == nil {
		c.entries.DeleteExpired()
		return kataAuthoritySnapshot{}, false
	}
	return cloneKataAuthoritySnapshot(item.Value()), true
}

func (c *kataSnapshotCache) set(key kataSnapshotKey, snapshot kataAuthoritySnapshot) {
	if c == nil || c.entries == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(key, snapshot)
}

func (c *kataSnapshotCache) setIfDaemonEpoch(
	key kataSnapshotKey,
	snapshot kataAuthoritySnapshot,
	expectedEpoch uint64,
) bool {
	if c == nil || c.entries == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.daemonEpochs[key.DaemonID] != expectedEpoch {
		return false
	}
	c.setLocked(key, snapshot)
	return true
}

func (c *kataSnapshotCache) setLocked(key kataSnapshotKey, snapshot kataAuthoritySnapshot) {
	keys := c.keysByDaemon[key.DaemonID]
	if keys == nil {
		keys = make(map[kataSnapshotKey]struct{})
		c.keysByDaemon[key.DaemonID] = keys
	}
	keys[key] = struct{}{}
	c.entries.Set(key, cloneKataAuthoritySnapshot(snapshot), ttlcache.DefaultTTL)
}

func (c *kataSnapshotCache) run(ctx context.Context) {
	if c == nil || c.entries == nil {
		return
	}
	cleanupEvery := c.cleanupEvery
	if cleanupEvery <= 0 {
		cleanupEvery = kataSnapshotCacheTTL
	}
	ticker := time.NewTicker(cleanupEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.entries.DeleteExpired()
		}
	}
}

func (c *kataSnapshotCache) daemonEpoch(daemonID string) uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.daemonEpochs[daemonID]
}

func (c *kataSnapshotCache) invalidateDaemon(daemonID string) uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.invalidateDaemonLocked(daemonID)
}

func (c *kataSnapshotCache) invalidateDaemonIfEpoch(daemonID string, expectedEpoch uint64) (uint64, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.daemonEpochs[daemonID] != expectedEpoch {
		return c.daemonEpochs[daemonID], false
	}
	return c.invalidateDaemonLocked(daemonID), true
}

func (c *kataSnapshotCache) invalidateDaemonLocked(daemonID string) uint64 {
	c.daemonEpochs[daemonID]++
	for key := range c.keysByDaemon[daemonID] {
		c.entries.Delete(key)
	}
	delete(c.keysByDaemon, daemonID)
	return c.daemonEpochs[daemonID]
}

func (c *kataSnapshotCache) removeEvictedKey(key kataSnapshotKey) {
	if c == nil || c.entries == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries.Has(key) {
		return
	}
	keys := c.keysByDaemon[key.DaemonID]
	delete(keys, key)
	if len(keys) == 0 {
		delete(c.keysByDaemon, key.DaemonID)
	}
}

func (c *kataSnapshotCache) close() {
	if c == nil || c.stopOnEviction == nil {
		return
	}
	c.stopOnEviction()
}

func cloneKataAuthoritySnapshot(snapshot kataAuthoritySnapshot) kataAuthoritySnapshot {
	snapshot.Projects = slices.Clone(snapshot.Projects)
	for i := range snapshot.Projects {
		snapshot.Projects[i].Metadata = cloneKataMetadata(snapshot.Projects[i].Metadata)
		snapshot.Projects[i].DeletedAt = cloneKataPointer(snapshot.Projects[i].DeletedAt)
	}
	snapshot.MemberIssueUIDs = slices.Clone(snapshot.MemberIssueUIDs)
	snapshot.Issues = slices.Clone(snapshot.Issues)
	for i := range snapshot.Issues {
		issue := &snapshot.Issues[i]
		issue.Metadata = cloneKataMetadata(issue.Metadata)
		issue.Owner = cloneKataPointer(issue.Owner)
		issue.Priority = cloneKataPointer(issue.Priority)
		issue.Labels = slices.Clone(issue.Labels)
		issue.Parent = cloneKataPointer(issue.Parent)
		issue.Blocks = slices.Clone(issue.Blocks)
		issue.BlockedBy = slices.Clone(issue.BlockedBy)
		issue.Related = slices.Clone(issue.Related)
		issue.ChildCounts = cloneKataPointer(issue.ChildCounts)
		issue.RecurrenceID = cloneKataPointer(issue.RecurrenceID)
		issue.OccurrenceKey = cloneKataPointer(issue.OccurrenceKey)
		issue.ClosedReason = cloneKataPointer(issue.ClosedReason)
		issue.ClosedAt = cloneKataPointer(issue.ClosedAt)
		issue.DeletedAt = cloneKataPointer(issue.DeletedAt)
	}
	return snapshot
}

func cloneKataMetadata(metadata map[string]any) map[string]any {
	cloned := maps.Clone(metadata)
	for key, value := range cloned {
		cloned[key] = cloneKataMetadataValue(value)
	}
	return cloned
}

func cloneKataMetadataValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneKataMetadata(value)
	case []any:
		cloned := slices.Clone(value)
		for i := range cloned {
			cloned[i] = cloneKataMetadataValue(cloned[i])
		}
		return cloned
	default:
		return value
	}
}

func cloneKataPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

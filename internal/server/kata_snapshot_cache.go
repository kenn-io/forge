package server

import (
	"context"
	"sync"
	"time"

	"github.com/jellydator/ttlcache/v3"
)

const (
	kataSnapshotCacheTTL      = 5 * time.Second
	kataSnapshotCacheCapacity = 128
)

type kataSnapshotKey struct {
	DaemonID   string
	View       string
	Scope      string
	ProjectUID string
	Authority  string
}

type kataProjectSummary struct {
	ID        int64
	UID       string
	Name      string
	OpenCount int64
}

type kataTaskSummary struct {
	UID string
}

type kataAuthoritySnapshot struct {
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
	return item.Value(), true
}

func (c *kataSnapshotCache) set(key kataSnapshotKey, snapshot kataAuthoritySnapshot) {
	if c == nil || c.entries == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	keys := c.keysByDaemon[key.DaemonID]
	if keys == nil {
		keys = make(map[kataSnapshotKey]struct{})
		c.keysByDaemon[key.DaemonID] = keys
	}
	keys[key] = struct{}{}
	c.entries.Set(key, snapshot, ttlcache.DefaultTTL)
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

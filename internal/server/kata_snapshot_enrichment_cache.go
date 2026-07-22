package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/jellydator/ttlcache/v3"
	"golang.org/x/sync/singleflight"

	katagenerated "go.kenn.io/kata/pkg/client/generated"
)

const (
	kataSnapshotEnrichmentCacheTTL      = 5 * time.Second
	kataSnapshotEnrichmentCacheCapacity = 128
	kataSnapshotEnrichmentCacheMaxBytes = uint64(16 << 20)
)

var errKataSnapshotEnrichmentStale = errors.New("kata enrichment invalidated while loading")

type kataIssueDetailCacheKey struct {
	DaemonID    string
	DaemonEpoch uint64
	IssueUID    string
}

type kataProjectEventsCacheKey struct {
	DaemonID    string
	DaemonEpoch uint64
	ProjectID   int64
}

type kataGraphCacheKey struct {
	DaemonID    string
	DaemonEpoch uint64
	SourceUID   string
	Depth       string
	HideDone    bool
}

type kataCachedIssueDetail struct {
	Body  *katagenerated.ShowIssueResponseBody
	Issue katagenerated.Issue
	ETag  string
}

type kataSnapshotEnrichmentCache struct {
	root   context.Context
	cancel context.CancelFunc

	details *ttlcache.Cache[kataIssueDetailCacheKey, kataCachedIssueDetail]
	events  *ttlcache.Cache[kataProjectEventsCacheKey, []katagenerated.EventEnvelope]
	graphs  *ttlcache.Cache[kataGraphCacheKey, *katagenerated.ReachableGraphResponseBody]

	detailGroup singleflight.Group
	eventGroup  singleflight.Group
	graphGroup  singleflight.Group

	mu                  sync.Mutex
	detailKeysByDaemon  map[string]map[kataIssueDetailCacheKey]struct{}
	eventKeysByDaemon   map[string]map[kataProjectEventsCacheKey]struct{}
	graphKeysByDaemon   map[string]map[kataGraphCacheKey]struct{}
	currentDaemonEpoch  func(string) uint64
	maxBytes            uint64
	cleanupEvery        time.Duration
	stopEvictionWorkers []func()

	loadsMu       sync.Mutex
	loadsStopping bool
	loads         sync.WaitGroup
	closeOnce     sync.Once
}

func newKataSnapshotEnrichmentCache(currentDaemonEpoch func(string) uint64) *kataSnapshotEnrichmentCache {
	return newKataSnapshotEnrichmentCacheWithLimits(
		context.Background(),
		kataSnapshotEnrichmentCacheTTL,
		kataSnapshotEnrichmentCacheCapacity,
		kataSnapshotEnrichmentCacheMaxBytes,
		currentDaemonEpoch,
	)
}

func newKataSnapshotEnrichmentCacheWithRoot(
	root context.Context,
	currentDaemonEpoch func(string) uint64,
) *kataSnapshotEnrichmentCache {
	return newKataSnapshotEnrichmentCacheWithLimits(
		root,
		kataSnapshotEnrichmentCacheTTL,
		kataSnapshotEnrichmentCacheCapacity,
		kataSnapshotEnrichmentCacheMaxBytes,
		currentDaemonEpoch,
	)
}

func newKataSnapshotEnrichmentCacheWithConfig(
	ttl time.Duration,
	capacity uint64,
	currentDaemonEpoch func(string) uint64,
) *kataSnapshotEnrichmentCache {
	return newKataSnapshotEnrichmentCacheWithLimits(
		context.Background(), ttl, capacity, kataSnapshotEnrichmentCacheMaxBytes, currentDaemonEpoch,
	)
}

func newKataSnapshotEnrichmentCacheWithLimits(
	root context.Context,
	ttl time.Duration,
	capacity uint64,
	maxBytes uint64,
	currentDaemonEpoch func(string) uint64,
) *kataSnapshotEnrichmentCache {
	if root == nil {
		root = context.Background()
	}
	if currentDaemonEpoch == nil {
		panic("kata enrichment cache requires an authoritative daemon epoch source")
	}
	cacheRoot, cancel := context.WithCancel(root)
	details := ttlcache.New(
		ttlcache.WithTTL[kataIssueDetailCacheKey, kataCachedIssueDetail](ttl),
		ttlcache.WithCapacity[kataIssueDetailCacheKey, kataCachedIssueDetail](capacity),
		ttlcache.WithDisableTouchOnHit[kataIssueDetailCacheKey, kataCachedIssueDetail](),
		ttlcache.WithMaxCost[kataIssueDetailCacheKey, kataCachedIssueDetail](maxBytes, kataIssueDetailCacheCost),
	)
	events := ttlcache.New(
		ttlcache.WithTTL[kataProjectEventsCacheKey, []katagenerated.EventEnvelope](ttl),
		ttlcache.WithCapacity[kataProjectEventsCacheKey, []katagenerated.EventEnvelope](capacity),
		ttlcache.WithDisableTouchOnHit[kataProjectEventsCacheKey, []katagenerated.EventEnvelope](),
		ttlcache.WithMaxCost[kataProjectEventsCacheKey, []katagenerated.EventEnvelope](maxBytes, kataProjectEventsCacheCost),
	)
	graphs := ttlcache.New(
		ttlcache.WithTTL[kataGraphCacheKey, *katagenerated.ReachableGraphResponseBody](ttl),
		ttlcache.WithCapacity[kataGraphCacheKey, *katagenerated.ReachableGraphResponseBody](capacity),
		ttlcache.WithDisableTouchOnHit[kataGraphCacheKey, *katagenerated.ReachableGraphResponseBody](),
		ttlcache.WithMaxCost[kataGraphCacheKey, *katagenerated.ReachableGraphResponseBody](maxBytes, kataGraphCacheCost),
	)
	cache := &kataSnapshotEnrichmentCache{
		root:                cacheRoot,
		cancel:              cancel,
		details:             details,
		events:              events,
		graphs:              graphs,
		detailKeysByDaemon:  make(map[string]map[kataIssueDetailCacheKey]struct{}),
		eventKeysByDaemon:   make(map[string]map[kataProjectEventsCacheKey]struct{}),
		graphKeysByDaemon:   make(map[string]map[kataGraphCacheKey]struct{}),
		currentDaemonEpoch:  currentDaemonEpoch,
		maxBytes:            maxBytes,
		cleanupEvery:        ttl,
		stopEvictionWorkers: make([]func(), 0, 3),
	}
	cache.stopEvictionWorkers = append(cache.stopEvictionWorkers,
		details.OnEviction(func(_ context.Context, _ ttlcache.EvictionReason, item *ttlcache.Item[kataIssueDetailCacheKey, kataCachedIssueDetail]) {
			cache.removeDetailKey(item.Key())
		}),
		events.OnEviction(func(_ context.Context, _ ttlcache.EvictionReason, item *ttlcache.Item[kataProjectEventsCacheKey, []katagenerated.EventEnvelope]) {
			cache.removeEventKey(item.Key())
		}),
		graphs.OnEviction(func(_ context.Context, _ ttlcache.EvictionReason, item *ttlcache.Item[kataGraphCacheKey, *katagenerated.ReachableGraphResponseBody]) {
			cache.removeGraphKey(item.Key())
		}),
	)
	return cache
}

func (c *kataSnapshotEnrichmentCache) issueDetail(
	ctx context.Context,
	key kataIssueDetailCacheKey,
	load func(context.Context) (kataCachedIssueDetail, error),
) (kataCachedIssueDetail, error) {
	if err := ctx.Err(); err != nil {
		return kataCachedIssueDetail{}, err
	}
	if !c.acceptsEpoch(key.DaemonID, key.DaemonEpoch) {
		return kataCachedIssueDetail{}, errKataSnapshotEnrichmentStale
	}
	if item := c.details.Get(key); item != nil {
		return item.Value(), nil
	}
	resultCh := c.detailGroup.DoChan(kataIssueDetailSingleflightKey(key), func() (any, error) {
		if !c.acceptsEpoch(key.DaemonID, key.DaemonEpoch) {
			return nil, errKataSnapshotEnrichmentStale
		}
		if item := c.details.Get(key); item != nil {
			return item.Value(), nil
		}
		if !c.beginLoad() {
			return nil, context.Canceled
		}
		defer c.loads.Done()
		loadCtx, cancel := context.WithTimeout(c.root, kataDaemonReadTimeout)
		defer cancel()
		value, err := load(loadCtx)
		if err != nil {
			return nil, err
		}
		if !c.acceptsEpoch(key.DaemonID, key.DaemonEpoch) {
			return nil, errKataSnapshotEnrichmentStale
		}
		c.storeDetail(key, value)
		return value, nil
	})
	select {
	case <-ctx.Done():
		return kataCachedIssueDetail{}, ctx.Err()
	case completed := <-resultCh:
		if completed.Err != nil {
			return kataCachedIssueDetail{}, completed.Err
		}
		return completed.Val.(kataCachedIssueDetail), nil
	}
}

func (c *kataSnapshotEnrichmentCache) projectEvents(
	ctx context.Context,
	key kataProjectEventsCacheKey,
	load func(context.Context) ([]katagenerated.EventEnvelope, error),
) ([]katagenerated.EventEnvelope, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.acceptsEpoch(key.DaemonID, key.DaemonEpoch) {
		return nil, errKataSnapshotEnrichmentStale
	}
	if item := c.events.Get(key); item != nil {
		return slices.Clone(item.Value()), nil
	}
	resultCh := c.eventGroup.DoChan(kataProjectEventsSingleflightKey(key), func() (any, error) {
		if !c.acceptsEpoch(key.DaemonID, key.DaemonEpoch) {
			return nil, errKataSnapshotEnrichmentStale
		}
		if item := c.events.Get(key); item != nil {
			return slices.Clone(item.Value()), nil
		}
		if !c.beginLoad() {
			return nil, context.Canceled
		}
		defer c.loads.Done()
		loadCtx, cancel := context.WithTimeout(c.root, kataDaemonReadTimeout)
		defer cancel()
		value, err := load(loadCtx)
		if err != nil {
			return nil, err
		}
		if !c.acceptsEpoch(key.DaemonID, key.DaemonEpoch) {
			return nil, errKataSnapshotEnrichmentStale
		}
		value = slices.Clone(value)
		c.storeEvents(key, value)
		return value, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case completed := <-resultCh:
		if completed.Err != nil {
			return nil, completed.Err
		}
		return slices.Clone(completed.Val.([]katagenerated.EventEnvelope)), nil
	}
}

func (c *kataSnapshotEnrichmentCache) graph(
	ctx context.Context,
	key kataGraphCacheKey,
	load func(context.Context) (*katagenerated.ReachableGraphResponseBody, error),
) (*katagenerated.ReachableGraphResponseBody, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.acceptsEpoch(key.DaemonID, key.DaemonEpoch) {
		return nil, errKataSnapshotEnrichmentStale
	}
	if item := c.graphs.Get(key); item != nil {
		return item.Value(), nil
	}
	resultCh := c.graphGroup.DoChan(kataGraphSingleflightKey(key), func() (any, error) {
		if !c.acceptsEpoch(key.DaemonID, key.DaemonEpoch) {
			return nil, errKataSnapshotEnrichmentStale
		}
		if item := c.graphs.Get(key); item != nil {
			return item.Value(), nil
		}
		if !c.beginLoad() {
			return nil, context.Canceled
		}
		defer c.loads.Done()
		loadCtx, cancel := context.WithTimeout(c.root, kataDaemonReadTimeout)
		defer cancel()
		value, err := load(loadCtx)
		if err != nil {
			return nil, err
		}
		if !c.acceptsEpoch(key.DaemonID, key.DaemonEpoch) {
			return nil, errKataSnapshotEnrichmentStale
		}
		c.storeGraph(key, value)
		return value, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case completed := <-resultCh:
		if completed.Err != nil {
			return nil, completed.Err
		}
		return completed.Val.(*katagenerated.ReachableGraphResponseBody), nil
	}
}

func (c *kataSnapshotEnrichmentCache) acceptsEpoch(daemonID string, epoch uint64) bool {
	return c != nil && c.currentDaemonEpoch(daemonID) == epoch
}

func (c *kataSnapshotEnrichmentCache) invalidateDaemon(daemonID string, authorityEpoch uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.acceptsEpoch(daemonID, authorityEpoch) {
		c.mu.Unlock()
		return
	}
	c.deleteDaemonBeforeEpochLocked(daemonID, authorityEpoch)
	c.mu.Unlock()
}

func (c *kataSnapshotEnrichmentCache) deleteDaemonBeforeEpochLocked(daemonID string, authorityEpoch uint64) {
	for key := range c.detailKeysByDaemon[daemonID] {
		if key.DaemonEpoch < authorityEpoch {
			c.details.Delete(key)
		}
	}
	for key := range c.eventKeysByDaemon[daemonID] {
		if key.DaemonEpoch < authorityEpoch {
			c.events.Delete(key)
		}
	}
	for key := range c.graphKeysByDaemon[daemonID] {
		if key.DaemonEpoch < authorityEpoch {
			c.graphs.Delete(key)
		}
	}
}

func (c *kataSnapshotEnrichmentCache) storeDetail(key kataIssueDetailCacheKey, value kataCachedIssueDetail) {
	if kataSerializedCost(value) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.acceptsEpoch(key.DaemonID, key.DaemonEpoch) {
		return
	}
	keys := c.detailKeysByDaemon[key.DaemonID]
	if keys == nil {
		keys = make(map[kataIssueDetailCacheKey]struct{})
		c.detailKeysByDaemon[key.DaemonID] = keys
	}
	keys[key] = struct{}{}
	c.details.Set(key, value, ttlcache.DefaultTTL)
}

func (c *kataSnapshotEnrichmentCache) storeEvents(key kataProjectEventsCacheKey, value []katagenerated.EventEnvelope) {
	if kataSerializedCost(value) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.acceptsEpoch(key.DaemonID, key.DaemonEpoch) {
		return
	}
	keys := c.eventKeysByDaemon[key.DaemonID]
	if keys == nil {
		keys = make(map[kataProjectEventsCacheKey]struct{})
		c.eventKeysByDaemon[key.DaemonID] = keys
	}
	keys[key] = struct{}{}
	c.events.Set(key, slices.Clone(value), ttlcache.DefaultTTL)
}

func (c *kataSnapshotEnrichmentCache) storeGraph(key kataGraphCacheKey, value *katagenerated.ReachableGraphResponseBody) {
	if kataSerializedCost(value) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.acceptsEpoch(key.DaemonID, key.DaemonEpoch) {
		return
	}
	keys := c.graphKeysByDaemon[key.DaemonID]
	if keys == nil {
		keys = make(map[kataGraphCacheKey]struct{})
		c.graphKeysByDaemon[key.DaemonID] = keys
	}
	keys[key] = struct{}{}
	c.graphs.Set(key, value, ttlcache.DefaultTTL)
}

func (c *kataSnapshotEnrichmentCache) removeDetailKey(key kataIssueDetailCacheKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.details.Has(key) {
		return
	}
	delete(c.detailKeysByDaemon[key.DaemonID], key)
	if len(c.detailKeysByDaemon[key.DaemonID]) == 0 {
		delete(c.detailKeysByDaemon, key.DaemonID)
	}
}

func (c *kataSnapshotEnrichmentCache) removeEventKey(key kataProjectEventsCacheKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.events.Has(key) {
		return
	}
	delete(c.eventKeysByDaemon[key.DaemonID], key)
	if len(c.eventKeysByDaemon[key.DaemonID]) == 0 {
		delete(c.eventKeysByDaemon, key.DaemonID)
	}
}

func (c *kataSnapshotEnrichmentCache) removeGraphKey(key kataGraphCacheKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.graphs.Has(key) {
		return
	}
	delete(c.graphKeysByDaemon[key.DaemonID], key)
	if len(c.graphKeysByDaemon[key.DaemonID]) == 0 {
		delete(c.graphKeysByDaemon, key.DaemonID)
	}
}

func (c *kataSnapshotEnrichmentCache) beginLoad() bool {
	c.loadsMu.Lock()
	defer c.loadsMu.Unlock()
	if c.loadsStopping {
		return false
	}
	c.loads.Add(1)
	return true
}

func (c *kataSnapshotEnrichmentCache) run(ctx context.Context) {
	if c == nil {
		return
	}
	cleanupEvery := c.cleanupEvery
	if cleanupEvery <= 0 {
		cleanupEvery = kataSnapshotEnrichmentCacheTTL
	}
	ticker := time.NewTicker(cleanupEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.details.DeleteExpired()
			c.events.DeleteExpired()
			c.graphs.DeleteExpired()
		}
	}
}

func (c *kataSnapshotEnrichmentCache) close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		c.loadsMu.Lock()
		c.loadsStopping = true
		c.loadsMu.Unlock()
		c.cancel()
		c.loads.Wait()
		for _, stop := range c.stopEvictionWorkers {
			stop()
		}
	})
}

func kataIssueDetailCacheCost(item ttlcache.CostItem[kataIssueDetailCacheKey, kataCachedIssueDetail]) uint64 {
	return kataSerializedCost(item.Value)
}

func kataProjectEventsCacheCost(item ttlcache.CostItem[kataProjectEventsCacheKey, []katagenerated.EventEnvelope]) uint64 {
	return kataSerializedCost(item.Value)
}

func kataGraphCacheCost(item ttlcache.CostItem[kataGraphCacheKey, *katagenerated.ReachableGraphResponseBody]) uint64 {
	return kataSerializedCost(item.Value)
}

func kataSerializedCost(value any) uint64 {
	encoded, err := json.Marshal(value)
	if err != nil {
		return math.MaxUint64
	}
	return uint64(len(encoded))
}

func kataIssueDetailSingleflightKey(key kataIssueDetailCacheKey) string {
	return fmt.Sprintf("%s\x00%d\x00%s", key.DaemonID, key.DaemonEpoch, key.IssueUID)
}

func kataProjectEventsSingleflightKey(key kataProjectEventsCacheKey) string {
	return fmt.Sprintf("%s\x00%d\x00%d", key.DaemonID, key.DaemonEpoch, key.ProjectID)
}

func kataGraphSingleflightKey(key kataGraphCacheKey) string {
	return fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%t", key.DaemonID, key.DaemonEpoch, key.SourceUID, key.Depth, key.HideDone)
}

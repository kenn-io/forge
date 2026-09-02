package db

import (
	"container/list"
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
)

// stmtCacheLimit bounds the number of distinct SQL texts each pool keeps
// prepared. The package has a few hundred static statements; the bound only
// matters for queries that splice a variable number of placeholders into the
// text, which cycle through the least recently used slots.
const stmtCacheLimit = 512

// stmtCache memoizes *sql.Stmt handles by SQL text for one connection pool.
// database/sql statements are pool-aware: each handle keeps one compiled
// statement per connection it has run on, so with idle connections pinned to
// the pool size every hot query compiles once per connection for the life of
// the process instead of once per call.
//
// The cache is a transparent layer: it never rewrites SQL, and it exposes the
// same ExecContext, QueryContext, and QueryRowContext shape as *sql.DB so the
// package's queryer interfaces accept it in place of the raw pool.
type stmtCache struct {
	pool  *sql.DB
	limit int

	mu       sync.Mutex
	closed   bool
	entries  map[string]*list.Element
	lru      list.List // front is most recently used
	prepared atomic.Int64
}

type stmtCacheEntry struct {
	query   string
	stmt    *sql.Stmt
	inUse   int
	evicted bool
}

// newStmtCache wraps pool. A limit of zero or less disables caching: every
// call goes straight to the pool, which compiles and finalizes per call.
func newStmtCache(pool *sql.DB, limit int) *stmtCache {
	return &stmtCache{pool: pool, limit: limit, entries: make(map[string]*list.Element)}
}

func (c *stmtCache) disabled() bool { return c.limit <= 0 }

// ExecContext executes query through a cached prepared statement.
func (c *stmtCache) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if c.disabled() {
		return c.pool.ExecContext(ctx, query, args...)
	}
	stmt, release, err := c.acquire(ctx, query)
	if err != nil {
		return nil, err
	}
	defer release()
	return stmt.ExecContext(ctx, args...)
}

// QueryContext runs query through a cached prepared statement. The returned
// rows keep the statement alive inside database/sql until they are closed, so
// a later eviction cannot invalidate an open result set.
func (c *stmtCache) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if c.disabled() {
		return c.pool.QueryContext(ctx, query, args...)
	}
	stmt, release, err := c.acquire(ctx, query)
	if err != nil {
		return nil, err
	}
	defer release()
	return stmt.QueryContext(ctx, args...)
}

// QueryRowContext runs query through a cached prepared statement. A prepare
// failure is surfaced by the row's Scan, matching *sql.DB.QueryRowContext.
func (c *stmtCache) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if c.disabled() {
		return c.pool.QueryRowContext(ctx, query, args...)
	}
	stmt, release, err := c.acquire(ctx, query)
	if err != nil {
		// database/sql exposes no way to build a *sql.Row carrying err, so
		// let the pool repeat the failing prepare and report it from Scan.
		return c.pool.QueryRowContext(ctx, query, args...)
	}
	defer release()
	return stmt.QueryRowContext(ctx, args...)
}

// acquire returns the cached statement for query, preparing it on a miss. The
// release function must be called once the statement call has returned; an
// entry evicted while calls are still in flight is closed by the last release.
func (c *stmtCache) acquire(ctx context.Context, query string) (*sql.Stmt, func(), error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, errStmtCacheClosed
	}
	if element, ok := c.entries[query]; ok {
		c.lru.MoveToFront(element)
		entry := element.Value.(*stmtCacheEntry)
		entry.inUse++
		c.mu.Unlock()
		return entry.stmt, func() { c.release(entry) }, nil
	}
	c.mu.Unlock()

	// Compile outside the lock so one slow prepare does not stall unrelated
	// queries; a concurrent miss on the same text loses the race below.
	stmt, err := c.pool.PrepareContext(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	c.prepared.Add(1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		stmt.Close()
		return nil, nil, errStmtCacheClosed
	}
	if element, ok := c.entries[query]; ok {
		c.lru.MoveToFront(element)
		entry := element.Value.(*stmtCacheEntry)
		entry.inUse++
		c.mu.Unlock()
		stmt.Close()
		return entry.stmt, func() { c.release(entry) }, nil
	}
	entry := &stmtCacheEntry{query: query, stmt: stmt, inUse: 1}
	c.entries[query] = c.lru.PushFront(entry)
	var closers []*sql.Stmt
	for c.lru.Len() > c.limit {
		oldest := c.lru.Back()
		victim := oldest.Value.(*stmtCacheEntry)
		c.lru.Remove(oldest)
		delete(c.entries, victim.query)
		victim.evicted = true
		if victim.inUse == 0 {
			closers = append(closers, victim.stmt)
		}
	}
	c.mu.Unlock()
	for _, closer := range closers {
		closer.Close()
	}
	return stmt, func() { c.release(entry) }, nil
}

func (c *stmtCache) release(entry *stmtCacheEntry) {
	c.mu.Lock()
	entry.inUse--
	closeNow := entry.evicted && entry.inUse == 0
	c.mu.Unlock()
	if closeNow {
		entry.stmt.Close()
	}
}

// Close finalizes every cached statement. It must run before the owning pool
// closes so statement handles are released deliberately rather than as a side
// effect of connection teardown.
func (c *stmtCache) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	stmts := make([]*sql.Stmt, 0, len(c.entries))
	for element := c.lru.Front(); element != nil; element = element.Next() {
		stmts = append(stmts, element.Value.(*stmtCacheEntry).stmt)
	}
	c.entries = nil
	c.lru.Init()
	c.mu.Unlock()

	var errs []error
	for _, stmt := range stmts {
		if err := stmt.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// len reports the number of cached statements.
func (c *stmtCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// preparedCount reports how many statements the cache has compiled so far.
func (c *stmtCache) preparedCount() int64 {
	return c.prepared.Load()
}

var errStmtCacheClosed = errors.New("statement cache closed")

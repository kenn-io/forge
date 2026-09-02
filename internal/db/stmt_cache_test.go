package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAppliesConnectionPragmasToEveryPooledConnection(t *testing.T) {
	d := openTestDB(t)
	ctx := t.Context()

	readPragmas := func(t *testing.T, conn *sql.Conn) map[string]int64 {
		t.Helper()
		got := map[string]int64{}
		for _, name := range []string{"cache_size", "mmap_size", "temp_store", "foreign_keys", "synchronous"} {
			var value int64
			require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA "+name).Scan(&value))
			got[name] = value
		}
		return got
	}
	want := map[string]int64{
		"cache_size":   -65536,
		"mmap_size":    268435456,
		"temp_store":   2, // MEMORY
		"foreign_keys": 1,
		"synchronous":  2, // FULL: unchanged WAL default
	}

	// Hold every read connection open at once so each one is a distinct
	// SQLite connection that had to apply the DSN pragmas on its own.
	var readConns []*sql.Conn
	for range readPoolSize {
		conn, err := d.ReadDB().Conn(ctx)
		require.NoError(t, err)
		readConns = append(readConns, conn)
	}
	for i, conn := range readConns {
		assert.Equal(t, want, readPragmas(t, conn), "read connection %d", i)
		require.NoError(t, conn.Close())
	}

	writeConn, err := d.WriteDB().Conn(ctx)
	require.NoError(t, err)
	assert.Equal(t, want, readPragmas(t, writeConn), "write connection")
	require.NoError(t, writeConn.Close())

	assert.Equal(t, readPoolSize, d.ReadDB().Stats().MaxOpenConnections)
	assert.Equal(t, writePoolSize, d.WriteDB().Stats().MaxOpenConnections)
	assert.Zero(t, d.ReadDB().Stats().MaxIdleClosed, "read pool must not close idle connections")
}

func seedStatementCacheRepos(t testing.TB, d *DB) RepoIdentity {
	t.Helper()
	ctx := context.Background()
	var first RepoIdentity
	for i := range 25 {
		identity := verifiedTestRepoIdentity("github", "github.com", "acme", fmt.Sprintf("widget-%02d", i))
		if i == 0 {
			first = identity
		}
		_, err := d.UpsertRepo(ctx, identity)
		require.NoError(t, err)
	}
	return first
}

func TestRepositoryLookupCompilesEachStatementOncePerConnection(t *testing.T) {
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	identity := seedStatementCacheRepos(t, d)

	repo, err := d.GetRepoByIdentity(ctx, identity)
	require.NoError(t, err)
	require.NotNil(t, repo)
	warm := d.roStmts.preparedCount()
	assert.Positive(warm)
	assert.Equal(int(warm), d.roStmts.len())

	const repeats = 200
	for range repeats {
		again, err := d.GetRepoByIdentity(ctx, identity)
		require.NoError(t, err)
		require.Equal(t, repo, again)
	}
	assert.Equal(warm, d.roStmts.preparedCount(),
		"%d repeated lookups must reuse the statements compiled by the first", repeats)
	assert.Equal(int(warm), d.roStmts.len())
}

func TestStmtCacheEvictsLeastRecentlyUsedBeyondLimit(t *testing.T) {
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	cache := newStmtCache(d.ReadDB(), 2)
	t.Cleanup(func() { require.NoError(t, cache.Close()) })

	scanOne := func(query string) int64 {
		var value int64
		require.NoError(t, cache.QueryRowContext(ctx, query).Scan(&value))
		return value
	}
	assert.Equal(int64(1), scanOne("SELECT 1"))
	assert.Equal(int64(2), scanOne("SELECT 2"))
	assert.Equal(int64(1), scanOne("SELECT 1")) // refresh: 2 is now oldest
	assert.Equal(int64(3), scanOne("SELECT 3"))
	assert.Equal(2, cache.len())
	assert.Equal(int64(3), cache.preparedCount())

	cache.mu.Lock()
	_, hasOne := cache.entries["SELECT 1"]
	_, hasTwo := cache.entries["SELECT 2"]
	cache.mu.Unlock()
	assert.True(hasOne, "most recently used statement stays cached")
	assert.False(hasTwo, "least recently used statement is evicted")

	assert.Equal(int64(2), scanOne("SELECT 2"))
	assert.Equal(int64(4), cache.preparedCount(), "an evicted statement is compiled again on demand")
	assert.Equal(2, cache.len())
}

func TestStmtCacheClosesEvictedStatementOnlyAfterInFlightCalls(t *testing.T) {
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	cache := newStmtCache(d.ReadDB(), 1)
	t.Cleanup(func() { require.NoError(t, cache.Close()) })

	held, release, err := cache.acquire(ctx, "SELECT 1")
	require.NoError(t, err)

	var value int64
	require.NoError(t, cache.QueryRowContext(ctx, "SELECT 2").Scan(&value))
	assert.Equal(int64(2), value)
	assert.Equal(1, cache.len())

	require.NoError(t, held.QueryRowContext(ctx).Scan(&value),
		"an evicted statement stays usable while a call still holds it")
	assert.Equal(int64(1), value)

	release()
	assert.ErrorContains(held.QueryRowContext(ctx).Scan(&value), "statement is closed")
}

func TestStmtCacheServesConcurrentCallersUnderEviction(t *testing.T) {
	d := openTestDB(t)
	ctx := t.Context()
	cache := newStmtCache(d.ReadDB(), 2)
	t.Cleanup(func() { require.NoError(t, cache.Close()) })

	const workers, iterations, distinct = 8, 100, 5
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Go(func() {
			for i := range iterations {
				want := int64((worker + i) % distinct)
				var got int64
				if err := cache.QueryRowContext(ctx, fmt.Sprintf("SELECT %d", want)).Scan(&got); err != nil {
					errs <- err
					return
				}
				if got != want {
					errs <- fmt.Errorf("SELECT %d returned %d", want, got)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.LessOrEqual(t, cache.len(), 2)
}

func TestDBCloseFinalizesCachedStatements(t *testing.T) {
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	identity := seedStatementCacheRepos(t, d)
	_, err := d.GetRepoByIdentity(ctx, identity)
	require.NoError(t, err)
	require.Positive(t, d.roStmts.len())

	require.NoError(t, d.Close())
	assert.Zero(d.roStmts.len())
	assert.Zero(d.rwStmts.len())
	_, _, err = d.roStmts.acquire(ctx, "SELECT 1")
	require.ErrorIs(t, err, errStmtCacheClosed)
	require.NoError(t, d.Close(), "closing twice stays safe for test cleanup")
}

// BenchmarkRepositoryCatalogLookup compares a hot read with the per-pool
// statement cache against the previous behavior of compiling every statement
// on each call. The uncached variant uses a zero-capacity cache, which
// prepares and finalizes on every call exactly like a raw *sql.DB query.
func BenchmarkRepositoryCatalogLookup(b *testing.B) {
	for _, variant := range []struct {
		name  string
		limit int
	}{
		{name: "uncached", limit: 0},
		{name: "cached", limit: stmtCacheLimit},
	} {
		b.Run(variant.name, func(b *testing.B) {
			d, err := Open(filepath.Join(b.TempDir(), "bench.db"))
			require.NoError(b, err)
			b.Cleanup(func() { require.NoError(b, d.Close()) })
			ctx := context.Background()
			identity := seedStatementCacheRepos(b, d)
			d.roStmts = newStmtCache(d.ReadDB(), variant.limit)

			b.ResetTimer()
			for b.Loop() {
				_, err := d.GetRepoByIdentity(ctx, identity)
				require.NoError(b, err)
			}
			b.StopTimer()
			b.ReportMetric(float64(d.roStmts.preparedCount())/float64(b.N), "prepares/op")
		})
	}
}

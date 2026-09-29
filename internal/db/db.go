package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"

	"go.kenn.io/forge/internal/db/dbupgrade"
)

// DB holds separate read-write and read-only connections to the SQLite database.
type DB struct {
	rw                *sql.DB
	ro                *sql.DB
	rwStmts           *stmtCache
	roStmts           *stmtCache
	mrSnapshotLocksMu sync.Mutex
	mrSnapshotLocks   map[mergeRequestSnapshotLockKey]*mergeRequestSnapshotLock
}

type mergeRequestSnapshotLockKey struct {
	repoID int64
	number int
}

type mergeRequestSnapshotLock struct {
	token chan struct{}
	refs  int
}

// ErrRepositoryIdentityChanged reports that the repository behind a route or
// stored reference is no longer the repository the caller resolved.
var ErrRepositoryIdentityChanged = errors.New("repository identity changed")

// Shared SQL adapter surfaces so helpers can run against *sql.DB, *sql.Tx, or
// the statement cache without duplicating identical interface declarations.
type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type rowQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type scanner interface {
	Scan(...any) error
}

// Open opens (or creates) a SQLite database at path, enables WAL mode, and
// runs embedded schema migrations before returning database handles.
func Open(path string) (*DB, error) {
	return open(path, true)
}

// OpenPreparedForTest opens a database file that was already initialized from
// a migrated test template. It intentionally skips migration checks so large
// test suites can keep per-test DB isolation without paying migration setup on
// every fixture.
func OpenPreparedForTest(path string) (*DB, error) {
	return open(path, false)
}

// Connection pool sizing. The writer is a single connection because SQLite
// serializes writers anyway; readers run concurrently under WAL.
const (
	writePoolSize = 1
	readPoolSize  = 4
)

// connectionDSN carries the per-connection pragmas. modernc.org/sqlite runs
// each _pragma entry when it opens a connection, so every pooled connection
// gets the same settings without a connect hook.
//
//   - busy_timeout keeps writers from failing immediately on a locked file.
//   - foreign_keys enforces the schema's referential integrity.
//   - cache_size is negative KiB: 64 MiB per connection. A maintainer
//     database with tens of thousands of merge requests and their events is
//     well over 100 MB, and the 2 MB default kept hot reads on pread.
//   - mmap_size lets reads share the OS page cache instead of copying through
//     SQLite's own cache; 256 MiB covers the whole file for typical installs.
//     SQLite clamps the value to its compile-time maximum and ignores it on
//     hosts without memory mapping, so no fallback path is needed.
//   - temp_store keeps sort and materialization scratch space in memory.
//
// Memory envelope: the five pooled connections can retain at most 320 MiB of
// private page cache, but with mmap active reads come straight from the shared
// file mapping and only written pages land in the private cache, so the
// writer's cache is the one that fills. The mapping itself is pageable, not
// resident.
//
// synchronous stays at the WAL default (FULL) so every committed transaction
// is durable across power loss, not just process crashes. The values are
// constants rather than config: they are tuning for the daemon's own store,
// not a user-facing preference, and config persistence has no I/O section.
const connectionDSN = "?_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=cache_size(-65536)" +
	"&_pragma=mmap_size(268435456)" +
	"&_pragma=temp_store(MEMORY)"

func open(path string, initialize bool) (*DB, error) {
	rw, err := openPool(path, writePoolSize)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	ro, err := openPool(path, readPoolSize)
	if err != nil {
		rw.Close()
		return nil, fmt.Errorf("open db read-only: %w", err)
	}

	d := &DB{
		rw:      rw,
		ro:      ro,
		rwStmts: newStmtCache(rw, stmtCacheLimit),
		roStmts: newStmtCache(ro, stmtCacheLimit),
	}
	if initialize {
		err = d.init()
	}
	if err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// openPool opens one connection pool whose idle limit matches its open limit,
// so connections (and the statements compiled on them) survive idle periods
// instead of being closed and re-opened with the DSN pragmas on every burst.
func openPool(path string, size int) (*sql.DB, error) {
	pool, err := sql.Open("sqlite", path+connectionDSN)
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(size)
	pool.SetMaxIdleConns(size)
	return pool, nil
}

func (d *DB) init() error {
	if _, err := d.rw.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}

	startVersion, err := runMigrations(d.rw)
	if err != nil {
		return err
	}
	if dbupgrade.NeedsLegacyTimestampRepair(startVersion) {
		if err := d.Tx(context.Background(), func(tx *sql.Tx) error {
			return dbupgrade.RepairLegacyTimestamps(context.Background(), tx)
		}); err != nil {
			return fmt.Errorf("repair legacy timestamp storage: %w", err)
		}
	}
	return d.Optimize(context.Background())
}

// Optimize refreshes the statistics the SQLite query planner uses to choose
// indexes. A store that has never been analyzed gets a full ANALYZE, which
// takes a few hundred milliseconds on a 160 MB file; once sqlite_stat1
// exists the pragma only re-analyzes tables whose shape changed enough to
// matter and returns in well under a millisecond. Without statistics the
// planner guesses, and on a large event table it guessed wrong for the
// activity feed and merge request detail reads.
func (d *DB) Optimize(ctx context.Context) (err error) {
	// Hold every reader so database/sql cannot select the same underlying
	// connection twice. SQLite persists statistics in the database but loads
	// and applies them separately on each connection.
	connections := make([]*sql.Conn, 0, readPoolSize)
	defer func() {
		var closeErr error
		for _, connection := range connections {
			closeErr = errors.Join(closeErr, connection.Close())
		}
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("release read connections: %w", closeErr))
		}
	}()

	for range readPoolSize {
		connection, connErr := d.ro.Conn(ctx)
		if connErr != nil {
			return fmt.Errorf("reserve read connections: %w", connErr)
		}
		connections = append(connections, connection)
	}

	if _, err := connections[0].ExecContext(ctx, "PRAGMA optimize=0x10002"); err != nil {
		return fmt.Errorf("optimize db: %w", err)
	}
	for _, connection := range connections {
		if _, err := connection.ExecContext(ctx, "ANALYZE sqlite_schema"); err != nil {
			return fmt.Errorf("reload planner statistics: %w", err)
		}
	}
	return nil
}

// Close finalizes cached statements and closes both connection pools.
func (d *DB) Close() error {
	return errors.Join(
		d.roStmts.Close(),
		d.rwStmts.Close(),
		d.ro.Close(),
		d.rw.Close(),
	)
}

// ReadDB returns the read-only connection pool.
func (d *DB) ReadDB() *sql.DB { return d.ro }

// WriteDB returns the read-write connection pool.
func (d *DB) WriteDB() *sql.DB { return d.rw }

func (d *DB) execContext(
	ctx context.Context,
	query string,
	args ...any,
) (sql.Result, error) {
	return d.rwStmts.ExecContext(ctx, query, args...)
}

// rwExecContext writes through the write pool's statement cache.
func (d *DB) rwExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.rwStmts.ExecContext(ctx, query, args...)
}

// rwQueryContext runs a RETURNING or read-your-writes query on the write pool.
func (d *DB) rwQueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.rwStmts.QueryContext(ctx, query, args...)
}

// rwQueryRowContext runs a single-row RETURNING query on the write pool.
func (d *DB) rwQueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.rwStmts.QueryRowContext(ctx, query, args...)
}

// roQueryContext runs a read on the read pool through the statement cache.
func (d *DB) roQueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.roStmts.QueryContext(ctx, query, args...)
}

// roQueryRowContext runs a single-row read on the read pool through the
// statement cache.
func (d *DB) roQueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.roStmts.QueryRowContext(ctx, query, args...)
}

// LockMergeRequestSnapshot serializes parent snapshot commits for one merge
// request. The returned release function must be called exactly once.
func (d *DB) LockMergeRequestSnapshot(
	ctx context.Context,
	repoID int64,
	number int,
) (func(), error) {
	return d.lockMergeRequestSnapshot(ctx, repoID, number)
}

func (d *DB) lockMergeRequestSnapshot(
	ctx context.Context,
	repoID int64,
	number int,
) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := mergeRequestSnapshotLockKey{repoID: repoID, number: number}

	d.mrSnapshotLocksMu.Lock()
	if d.mrSnapshotLocks == nil {
		d.mrSnapshotLocks = make(
			map[mergeRequestSnapshotLockKey]*mergeRequestSnapshotLock,
		)
	}
	lock := d.mrSnapshotLocks[key]
	if lock == nil {
		lock = &mergeRequestSnapshotLock{token: make(chan struct{}, 1)}
		d.mrSnapshotLocks[key] = lock
	}
	lock.refs++
	d.mrSnapshotLocksMu.Unlock()

	select {
	case lock.token <- struct{}{}:
	case <-ctx.Done():
		d.releaseMergeRequestSnapshotLockRef(key, lock)
		return nil, ctx.Err()
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-lock.token
			d.releaseMergeRequestSnapshotLockRef(key, lock)
		})
	}, nil
}

func (d *DB) releaseMergeRequestSnapshotLockRef(
	key mergeRequestSnapshotLockKey,
	lock *mergeRequestSnapshotLock,
) {
	d.mrSnapshotLocksMu.Lock()
	defer d.mrSnapshotLocksMu.Unlock()
	lock.refs--
	if lock.refs == 0 {
		delete(d.mrSnapshotLocks, key)
	}
}

// Tx runs fn inside a transaction, rolling back on error.
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.rw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

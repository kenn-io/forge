package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"go.kenn.io/forge/internal/db/dbupgrade"
	_ "modernc.org/sqlite"
)

// DB holds separate read-write and read-only connections to the SQLite database.
type DB struct {
	rw                *sql.DB
	ro                *sql.DB
	mrReconcileMu     sync.RWMutex
	mrReconcileGate   sync.Mutex
	mrSnapshotLocksMu sync.Mutex
	mrSnapshotLocks   map[mergeRequestSnapshotLockKey]*mergeRequestSnapshotLock

	beforeRepositoryReconciliationWriteLock func()
}

type mergeRequestSnapshotLockKey struct {
	repoID int64
	number int
}

type mergeRequestSnapshotLock struct {
	token chan struct{}
	refs  int
}

var ErrRepositoryRouteFenceChanged = errors.New("repository route fence changed")

type repositoryRouteGuardContextKey struct{}

type repositoryRouteLeaseContextKey struct{}

type repositoryRouteGuard struct {
	db       *DB
	identity RepoIdentity
	fence    RepositoryRouteFence
}

type repositoryRouteLease struct {
	guard *repositoryRouteGuard
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

func open(path string, initialize bool) (*DB, error) {
	rw, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	rw.SetMaxOpenConns(1)

	ro, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		rw.Close()
		return nil, fmt.Errorf("open db read-only: %w", err)
	}
	ro.SetMaxOpenConns(4)

	d := &DB{rw: rw, ro: ro}
	if initialize {
		err = d.init()
	}
	if err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) init() error {
	if _, err := d.rw.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("enable WAL: %w", err)
	}

	startVersion, err := runMigrations(d.rw)
	if err != nil {
		return err
	}
	if !dbupgrade.NeedsLegacyTimestampRepair(startVersion) {
		return nil
	}
	if err := d.Tx(context.Background(), func(tx *sql.Tx) error {
		return dbupgrade.RepairLegacyTimestamps(context.Background(), tx)
	}); err != nil {
		return fmt.Errorf("repair legacy timestamp storage: %w", err)
	}
	return nil
}

// Close closes both database connections.
func (d *DB) Close() error {
	d.ro.Close()
	return d.rw.Close()
}

// ReadDB returns the read-only connection pool.
func (d *DB) ReadDB() *sql.DB { return d.ro }

// WriteDB returns the read-write connection pool.
func (d *DB) WriteDB() *sql.DB { return d.rw }

// LockRepositoryReconciliationRead keeps repository identity and its related
// rows stable until the returned release function is called exactly once.
func (d *DB) LockRepositoryReconciliationRead(
	ctx context.Context,
) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mrReconcileGate.Lock()
	d.mrReconcileMu.RLock()
	d.mrReconcileGate.Unlock()
	if err := ctx.Err(); err != nil {
		d.mrReconcileMu.RUnlock()
		return nil, err
	}

	var once sync.Once
	return func() {
		once.Do(d.mrReconcileMu.RUnlock)
	}, nil
}

// WithRepositoryRouteFence binds subsequent database writes to one observed
// repository route generation. Reads are unaffected; each write validates the
// fence while holding the reconciliation read lock through commit.
func (d *DB) WithRepositoryRouteFence(
	ctx context.Context,
	identity RepoIdentity,
	fence RepositoryRouteFence,
) context.Context {
	return context.WithValue(ctx, repositoryRouteGuardContextKey{}, &repositoryRouteGuard{
		db: d, identity: canonicalRepoIdentity(identity), fence: fence,
	})
}

func (d *DB) repositoryRouteGuard(ctx context.Context) *repositoryRouteGuard {
	guard, _ := ctx.Value(repositoryRouteGuardContextKey{}).(*repositoryRouteGuard)
	if guard == nil || guard.db != d {
		return nil
	}
	return guard
}

// lockRepositoryRouteWrite validates an optional context route guard and keeps
// repository reconciliation from interleaving until release. The returned
// context makes nested guarded writes re-entrant for the same short critical
// section.
func (d *DB) lockRepositoryRouteWrite(
	ctx context.Context,
) (context.Context, func(), error) {
	guard := d.repositoryRouteGuard(ctx)
	if guard == nil {
		return ctx, func() {}, nil
	}
	if lease, _ := ctx.Value(repositoryRouteLeaseContextKey{}).(*repositoryRouteLease); lease != nil && lease.guard == guard {
		return ctx, func() {}, nil
	}

	release, err := d.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		return ctx, nil, err
	}
	matches, err := d.RepositoryRouteFenceMatchesUnderRepositoryReconciliationRead(
		ctx, guard.identity, guard.fence,
	)
	if err != nil {
		release()
		return ctx, nil, err
	}
	if !matches {
		release()
		return ctx, nil, fmt.Errorf(
			"%w for %s/%s", ErrRepositoryRouteFenceChanged,
			guard.identity.PlatformHost, guard.identity.RepoPath,
		)
	}
	locked := context.WithValue(
		ctx, repositoryRouteLeaseContextKey{}, &repositoryRouteLease{guard: guard},
	)
	return locked, release, nil
}

// LockRepositoryReconciliationReadForWrite holds repository identity stable
// for a compound write. When ctx carries a route fence, it validates that
// fence and returns a context that makes nested guarded DB writes re-entrant.
func (d *DB) LockRepositoryReconciliationReadForWrite(
	ctx context.Context,
) (context.Context, func(), error) {
	if d.repositoryRouteGuard(ctx) != nil {
		return d.lockRepositoryRouteWrite(ctx)
	}
	release, err := d.LockRepositoryReconciliationRead(ctx)
	return ctx, release, err
}

func (d *DB) execContext(
	ctx context.Context,
	query string,
	args ...any,
) (sql.Result, error) {
	lockedCtx, release, err := d.lockRepositoryRouteWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return d.rw.ExecContext(lockedCtx, query, args...)
}

func (d *DB) lockRepositoryReconciliationWrite() func() {
	d.mrReconcileGate.Lock()
	if hook := d.beforeRepositoryReconciliationWriteLock; hook != nil {
		hook()
	}
	d.mrReconcileMu.Lock()
	d.mrReconcileGate.Unlock()
	return d.mrReconcileMu.Unlock
}

// SetBeforeRepositoryReconciliationWriteLockForTest installs a hook after
// write admission closes to new readers and immediately before the write lock.
func (d *DB) SetBeforeRepositoryReconciliationWriteLockForTest(
	hook func(),
) func() {
	d.mrReconcileGate.Lock()
	previous := d.beforeRepositoryReconciliationWriteLock
	d.beforeRepositoryReconciliationWriteLock = hook
	d.mrReconcileGate.Unlock()
	return func() {
		d.mrReconcileGate.Lock()
		d.beforeRepositoryReconciliationWriteLock = previous
		d.mrReconcileGate.Unlock()
	}
}

// LockMergeRequestSnapshot serializes parent snapshot commits for one merge
// request. The returned release function must be called exactly once.
func (d *DB) LockMergeRequestSnapshot(
	ctx context.Context,
	repoID int64,
	number int,
) (func(), error) {
	// Repository reconciliation can move this merge request to a different
	// repo ID. Hold the stable read side for the entire per-MR lock lifetime
	// so a snapshot commit and its lock key cannot be split by that move.
	releaseReconciliation, err := d.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		return nil, err
	}
	releaseSnapshot, err := d.lockMergeRequestSnapshotUnderRepositoryReconciliationRead(
		ctx, repoID, number,
	)
	if err != nil {
		releaseReconciliation()
		return nil, err
	}
	return func() {
		releaseSnapshot()
		releaseReconciliation()
	}, nil
}

func (d *DB) lockMergeRequestSnapshotUnderRepositoryReconciliationRead(
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
	lockedCtx, release, err := d.lockRepositoryRouteWrite(ctx)
	if err != nil {
		return err
	}
	defer release()
	tx, err := d.rw.BeginTx(lockedCtx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

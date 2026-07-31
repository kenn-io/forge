package db

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"

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

type repositoryReconciliationReadLeaseContextKey struct{}

type repositoryReconciliationReadLease struct {
	db     *DB
	active atomic.Bool
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
	_, release, err := d.LeaseRepositoryReconciliationRead(ctx)
	return release, err
}

// LeaseRepositoryReconciliationRead returns a context that identifies the
// active lease to nested repository-aware helpers. A nested acquisition on the
// same DB becomes a no-op, avoiding RWMutex read recursion when a writer has
// already closed admission to new readers.
func (d *DB) LeaseRepositoryReconciliationRead(
	ctx context.Context,
) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if existing, ok := ctx.Value(
		repositoryReconciliationReadLeaseContextKey{},
	).(*repositoryReconciliationReadLease); ok &&
		existing.db == d && existing.active.Load() {
		return ctx, func() {}, nil
	}
	d.mrReconcileGate.Lock()
	d.mrReconcileMu.RLock()
	d.mrReconcileGate.Unlock()
	if err := ctx.Err(); err != nil {
		d.mrReconcileMu.RUnlock()
		return nil, nil, err
	}

	lease := &repositoryReconciliationReadLease{db: d}
	lease.active.Store(true)
	var once sync.Once
	release := func() {
		once.Do(func() {
			lease.active.Store(false)
			d.mrReconcileMu.RUnlock()
		})
	}
	return context.WithValue(
		ctx,
		repositoryReconciliationReadLeaseContextKey{},
		lease,
	), release, nil
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

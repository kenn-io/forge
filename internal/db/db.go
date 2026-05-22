package db

import (
	"context"
	"database/sql"
	"fmt"

	"go.kenn.io/middleman/internal/db/dbupgrade"
	_ "modernc.org/sqlite"
)

// DB holds separate read-write and read-only connections to the SQLite database.
type DB struct {
	rw *sql.DB
	ro *sql.DB
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

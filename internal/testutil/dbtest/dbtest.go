package dbtest

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
)

var templateCache = &templateState{}

type templateState struct {
	once       sync.Once
	data       []byte
	initErr    error
	buildCount atomic.Int32
}

// Open returns an isolated, migrated SQLite test database.
//
// The first call builds a migrated template database. Later calls copy that
// template into t.TempDir(), preserving test isolation without rerunning every
// migration for every test fixture.
func Open(tb testing.TB) *db.DB {
	tb.Helper()

	path := filepath.Join(tb.TempDir(), "test.db")
	return OpenAt(tb, path)
}

// OpenAt copies the migrated test template to path and opens that copy.
func OpenAt(tb testing.TB, path string) *db.DB {
	tb.Helper()

	copyFile(tb, templateBytes(tb), path)
	return OpenPreparedAt(tb, path)
}

// OpenPreparedAt opens an existing database that was prepared through this
// fixture without rerunning migrations.
func OpenPreparedAt(tb testing.TB, path string) *db.DB {
	tb.Helper()

	database, err := db.OpenPreparedForTest(path)
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = database.Close() })
	return database
}

// OpenWithMigrationsAt opens path through the production migration path for
// tests that specifically exercise migration or startup repair behavior.
func OpenWithMigrationsAt(tb testing.TB, path string) *db.DB {
	tb.Helper()

	database, err := db.Open(path)
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = database.Close() })
	return database
}

func templateBytes(tb testing.TB) []byte {
	tb.Helper()

	templateCache.once.Do(func() {
		templateCache.buildCount.Add(1)
		path := filepath.Join(tb.TempDir(), "template.db")
		database, err := db.Open(path)
		if err != nil {
			templateCache.initErr = err
			return
		}
		if _, err := database.WriteDB().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			_ = database.Close()
			templateCache.initErr = err
			return
		}
		if err := database.Close(); err != nil {
			templateCache.initErr = err
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			templateCache.initErr = err
			return
		}
		templateCache.data = data
	})
	require.NoError(tb, templateCache.initErr)
	return templateCache.data
}

func copyFile(tb testing.TB, src []byte, dst string) {
	tb.Helper()

	require.NoError(tb, os.WriteFile(dst, src, 0o600))
}

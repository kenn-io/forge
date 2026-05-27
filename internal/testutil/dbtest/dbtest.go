package dbtest

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
)

var templateCache = &templateState{}
var templateRoot = os.TempDir()

type templateState struct {
	once       sync.Once
	path       string
	initErr    error
	buildCount atomic.Int32
}

// Open returns an isolated, migrated SQLite test database.
//
// The first call builds a migrated template database. Later calls copy that
// template into t.TempDir(), preserving test isolation without rerunning every
// migration for every test fixture.
func Open(t testing.TB) *db.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	return OpenAt(t, path)
}

// OpenAt copies the migrated test template to path and opens that copy.
func OpenAt(t testing.TB, path string) *db.DB {
	t.Helper()

	templatePath := templatePath(t)
	copyFile(t, templatePath, path)
	return OpenPreparedAt(t, path)
}

// OpenPreparedAt opens an existing database that was prepared through this
// fixture without rerunning migrations.
func OpenPreparedAt(t testing.TB, path string) *db.DB {
	t.Helper()

	database, err := db.OpenPreparedForTest(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

// OpenWithMigrationsAt opens path through the production migration path for
// tests that specifically exercise migration or startup repair behavior.
func OpenWithMigrationsAt(t testing.TB, path string) *db.DB {
	t.Helper()

	database, err := db.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func templatePath(t testing.TB) string {
	t.Helper()

	templateCache.once.Do(func() {
		templateCache.buildCount.Add(1)
		dir, err := os.MkdirTemp(templateRoot, "middleman-test-db-template-*")
		if err != nil {
			templateCache.initErr = err
			return
		}
		path := filepath.Join(dir, "template.db")
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
		templateCache.path = path
	})
	require.NoError(t, templateCache.initErr)
	return templateCache.path
}

func copyFile(t testing.TB, src, dst string) {
	t.Helper()

	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, data, 0o600))
}

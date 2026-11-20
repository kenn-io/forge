package db

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

var testDBTemplate = &testDBTemplateState{}

type testDBTemplateState struct {
	once     sync.Once
	ready    atomic.Bool
	data     []byte
	versions map[uint][]byte
	initErr  error
}

func openTemplateTestDB(t *testing.T) *DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	copyTestDBTemplate(t, testDBTemplateBytes(t), path)

	d, err := OpenPreparedForTest(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	return d
}

func testDBTemplateBytes(t *testing.T) []byte {
	t.Helper()

	testDBTemplate.once.Do(func() {
		path := filepath.Join(t.TempDir(), "template.db")
		testDBTemplate.versions = make(map[uint][]byte)
		// Add new test start versions here; unlisted versions replay from zero.
		for _, version := range []uint{36, 37, 38, 43, 44, 45, 47, 49, 50, 51, 52, 53, 54, 55, 59, 64, 68, 75, 83, 87, 91, 93, 94, 95} {
			raw, err := migrateTestDBToVersion(t.Context(), path, version)
			if err != nil {
				testDBTemplate.initErr = err
				return
			}
			if err := raw.Close(); err != nil {
				testDBTemplate.initErr = err
				return
			}
			data, err := os.ReadFile(path)
			if err != nil {
				testDBTemplate.initErr = err
				return
			}
			testDBTemplate.versions[version] = data
		}
		d, err := Open(path)
		if err != nil {
			testDBTemplate.initErr = err
			return
		}
		if _, err := d.WriteDB().ExecContext(t.Context(), "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			_ = d.Close()
			testDBTemplate.initErr = err
			return
		}
		if err := d.Close(); err != nil {
			testDBTemplate.initErr = err
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			testDBTemplate.initErr = err
			return
		}
		testDBTemplate.data = data
		testDBTemplate.ready.Store(true)
	})
	require.NoError(t, testDBTemplate.initErr)
	return testDBTemplate.data
}

func copyTestDBTemplate(t *testing.T, src []byte, dst string) {
	t.Helper()

	require.NoError(t, os.WriteFile(dst, src, 0o600))
}

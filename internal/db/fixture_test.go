package db

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

var testDBTemplate = &testDBTemplateState{}

type testDBTemplateState struct {
	once     sync.Once
	data     []byte
	mu       sync.Mutex
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
	})
	require.NoError(t, testDBTemplate.initErr)
	return testDBTemplate.data
}

func historicalTestDBBytes(t *testing.T, version uint) []byte {
	t.Helper()
	// Add every fresh historical test start version here.
	versions := []uint{36, 37, 38, 43, 44, 45, 47, 49, 50, 51, 52, 53, 54, 55, 59, 64, 68, 75, 83, 87, 91, 93, 94, 95}
	require.True(t, slices.Contains(versions, version), "add start version %d to historical test versions", version)

	testDBTemplate.mu.Lock()
	defer testDBTemplate.mu.Unlock()
	if data, ok := testDBTemplate.versions[version]; ok {
		return data
	}
	path := filepath.Join(t.TempDir(), "historical.db")
	var nearest uint
	for candidate := range testDBTemplate.versions {
		if candidate < version && candidate > nearest {
			nearest = candidate
		}
	}
	if nearest > 0 {
		copyTestDBTemplate(t, testDBTemplate.versions[nearest], path)
	}
	raw, err := migrateTestDBToVersion(t.Context(), path, version)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	if testDBTemplate.versions == nil {
		testDBTemplate.versions = make(map[uint][]byte)
	}
	testDBTemplate.versions[version] = data
	return data
}

func copyTestDBTemplate(t *testing.T, src []byte, dst string) {
	t.Helper()

	require.NoError(t, os.WriteFile(dst, src, 0o600))
}

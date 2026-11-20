package db

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

var testDBTemplate = &testDBTemplateState{versions: make(map[uint][]byte)}

var historicalTestVersions = slices.Sorted(slices.Values([]uint{36, 37, 38, 43, 44, 45, 47, 49, 50, 51, 52, 53, 54, 55, 59, 64, 68, 75, 83, 87, 91, 93, 94, 95}))

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
		data, err := historicalTestDBBytes(t, historicalTestVersions[len(historicalTestVersions)-1])
		if err != nil {
			testDBTemplate.initErr = err
			return
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			testDBTemplate.initErr = err
			return
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
		data, err = os.ReadFile(path)
		if err != nil {
			testDBTemplate.initErr = err
			return
		}
		testDBTemplate.data = data
	})
	require.NoError(t, testDBTemplate.initErr)
	return testDBTemplate.data
}

func historicalTestDBBytes(t *testing.T, version uint) ([]byte, error) {
	t.Helper()

	testDBTemplate.mu.Lock()
	defer testDBTemplate.mu.Unlock()
	if data, ok := testDBTemplate.versions[version]; ok {
		return data, nil
	}
	path := filepath.Join(t.TempDir(), "historical.db")
	var nearest uint
	for candidate := range testDBTemplate.versions {
		if candidate < version && candidate > nearest {
			nearest = candidate
		}
	}
	if nearest > 0 {
		if err := os.WriteFile(path, testDBTemplate.versions[nearest], 0o600); err != nil {
			return nil, err
		}
	}
	targets := append(slices.Clone(historicalTestVersions), version)
	slices.Sort(targets)
	for _, target := range slices.Compact(targets) {
		if target <= nearest {
			continue
		}
		if target > version {
			break
		}
		raw, err := migrateTestDBToVersion(t.Context(), path, target)
		if err != nil {
			return nil, err
		}
		if err := raw.Close(); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		testDBTemplate.versions[target] = data
	}
	return testDBTemplate.versions[version], nil
}

func copyTestDBTemplate(t *testing.T, src []byte, dst string) {
	t.Helper()

	require.NoError(t, os.WriteFile(dst, src, 0o600))
}

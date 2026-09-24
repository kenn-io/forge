package db

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

var testDBTemplate = &testDBTemplateState{}

type testDBTemplateState struct {
	once    sync.Once
	data    []byte
	initErr error
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

func copyTestDBTemplate(t *testing.T, src []byte, dst string) {
	t.Helper()

	require.NoError(t, os.WriteFile(dst, src, 0o600))
}

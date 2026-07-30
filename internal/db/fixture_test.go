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
	path    string
	initErr error
}

func openTemplateTestDB(t *testing.T) *DB {
	t.Helper()

	templatePath := testDBTemplatePath(t)
	path := filepath.Join(t.TempDir(), "test.db")
	copyTestDBTemplate(t, templatePath, path)

	d, err := OpenPreparedForTest(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	return d
}

func testDBTemplatePath(t *testing.T) string {
	t.Helper()

	testDBTemplate.once.Do(func() {
		dir, err := os.MkdirTemp("", "kenn-forge-db-package-test-template-*")
		if err != nil {
			testDBTemplate.initErr = err
			return
		}
		path := filepath.Join(dir, "template.db")
		d, err := Open(path)
		if err != nil {
			testDBTemplate.initErr = err
			return
		}
		if _, err := d.WriteDB().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			_ = d.Close()
			testDBTemplate.initErr = err
			return
		}
		if err := d.Close(); err != nil {
			testDBTemplate.initErr = err
			return
		}
		testDBTemplate.path = path
	})
	require.NoError(t, testDBTemplate.initErr)
	return testDBTemplate.path
}

func copyTestDBTemplate(t *testing.T, src, dst string) {
	t.Helper()

	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, data, 0o600))
}

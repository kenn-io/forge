//go:build linux

package pathidentity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalExistingFallsBackWithoutProcFS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Config.toml")
	require.NoError(t, os.WriteFile(path, []byte("test\n"), 0o600))

	canonical, err := canonicalExistingFromProc(
		path, filepath.Join(t.TempDir(), "missing-proc"),
	)

	require.NoError(t, err)
	assert.Equal(t, path, canonical)
}

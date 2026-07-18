//go:build unix

package workspace

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiffContentDigestRejectsFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "changed.fifo")
	require.NoError(t, syscall.Mkfifo(path, 0o600))

	_, _, err := diffContentDigest(t.Context(), path)

	require.ErrorContains(t, err, "not a regular file")
}

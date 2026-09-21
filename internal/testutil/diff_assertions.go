package testutil

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
)

// RequireWorkspaceDiffFile returns the response file with path or fails the test.
func RequireWorkspaceDiffFile(
	tb testing.TB,
	files []generated.DiffFile,
	path string,
) generated.DiffFile {
	tb.Helper()
	for _, file := range files {
		if file.Path == path {
			return file
		}
	}
	require.Failf(tb, "workspace diff file not found", "path %q", path)
	return generated.DiffFile{}
}

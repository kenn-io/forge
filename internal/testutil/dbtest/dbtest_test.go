package dbtest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
)

func TestOpenUsesIsolatedCopiesOfCachedMigratedTemplate(t *testing.T) {
	require := require.New(t)
	resetTemplateForTest(t)

	first := Open(t)
	second := Open(t)

	firstRepoID, err := first.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotZero(firstRepoID)

	got, err := second.GetRepoByOwnerName(t.Context(), "acme", "widget")
	require.NoError(err)
	require.Nil(got)
	require.Equal(1, templateBuildCountForTest())
}

func TestOpenKeepsCachedTemplateOutOfPerTestTMPDIR(t *testing.T) {
	require := require.New(t)
	resetTemplateForTest(t)

	tmpRoot := filepath.Join(t.TempDir(), "tmp")
	require.NoError(os.MkdirAll(tmpRoot, 0o700))
	t.Setenv("TMPDIR", tmpRoot)

	first := Open(t)
	require.NotNil(first)
	require.Equal(1, templateBuildCountForTest())

	require.NoError(os.RemoveAll(tmpRoot))
	second := Open(t)
	require.NotNil(second)
	require.Equal(1, templateBuildCountForTest())
}

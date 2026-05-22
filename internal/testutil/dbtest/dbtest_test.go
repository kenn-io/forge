package dbtest

import (
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

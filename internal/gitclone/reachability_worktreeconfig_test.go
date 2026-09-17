package gitclone

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommitsReachableFromWorktreeConfig(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mgr, shas := setupAncestryClone(t)
	clonePath, err := mgr.ClonePath("github", "example.com", "acme", "widgets")
	require.NoError(err)
	commitTestRun(t, clonePath, "git", "config", "extensions.worktreeConfig", "true")
	result, err := mgr.CommitsReachableFrom(t.Context(), "github", "example.com", "acme", "widgets", shas["c2"], []string{shas["c1"], shas["c3"]})
	require.NoError(err)
	assert.True(result.HeadVerified)
	assert.True(result.Live[shas["c1"]])
	assert.False(result.Live[shas["c3"]])
}

package platform

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeKindAllowsFutureProviderKinds(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	kind, err := NormalizeKind("Codeberg")
	require.NoError(err)
	assert.Equal(Kind("codeberg"), kind)
	assert.True(AllowsNestedOwner(kind))

	_, ok := DefaultHost(kind)
	assert.False(ok)

	fj, err := NormalizeKind("fj")
	require.NoError(err)
	assert.Equal(KindForgejo, fj)

	forgejo, err := NormalizeKind("Forgejo")
	require.NoError(err)
	assert.Equal(KindForgejo, forgejo)

	tea, err := NormalizeKind("tea")
	require.NoError(err)
	assert.Equal(KindGitea, tea)

	gitea, err := NormalizeKind("Gitea")
	require.NoError(err)
	assert.Equal(KindGitea, gitea)
}

func TestNormalizeKindCanonicalizesBuiltInShorthands(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	gh, err := NormalizeKind("GH")
	require.NoError(err)
	assert.Equal(KindGitHub, gh)

	gl, err := NormalizeKind(" gl ")
	require.NoError(err)
	assert.Equal(KindGitLab, gl)

	fj, err := NormalizeKind("FJ")
	require.NoError(err)
	assert.Equal(KindForgejo, fj)

	tea, err := NormalizeKind(" tea ")
	require.NoError(err)
	assert.Equal(KindGitea, tea)
}

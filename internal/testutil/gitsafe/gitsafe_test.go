package gitsafe

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnsetInheritedGitEnvRemovesRepoBindingVars(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	// t.Setenv restores these after the test; it also fails if the test
	// is marked parallel, which keeps the process-global mutation safe.
	t.Setenv("GIT_DIR", "/some/host/.git")
	t.Setenv("GIT_WORK_TREE", "/some/host")
	t.Setenv("GIT_CONFIG_GLOBAL", "/some/host/.gitconfig")
	t.Setenv("GIT_AUTHOR_NAME", "Inherited Author")
	t.Setenv("GITSAFE_TEST_KEEPME", "keep")

	UnsetInheritedGitEnv()

	for _, name := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_GLOBAL", "GIT_AUTHOR_NAME",
	} {
		_, present := os.LookupEnv(name)
		assert.Falsef(present, "%s must be unset so fixtures cannot reach the host repo", name)
	}

	value, present := os.LookupEnv("GITSAFE_TEST_KEEPME")
	require.True(present, "non-git env must be preserved")
	assert.Equal("keep", value)
}

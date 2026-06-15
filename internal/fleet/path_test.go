package fleet

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormPathCleansAndAbsolutizes(t *testing.T) {
	assert := assert.New(t)
	base := t.TempDir()
	want := filepath.Join(base, "wt")
	assert.Equal(want, NormPath(filepath.Join(base, ".", "wt")))
	assert.Equal(want, NormPath(filepath.Join(base, "sub", "..", "wt")))
}

func TestWorktreeScopedKeyPrefixesNormalizedPath(t *testing.T) {
	assert := assert.New(t)
	base := t.TempDir()
	assert.Equal(
		"worktree:"+filepath.Join(base, "wt"),
		WorktreeScopedKey(filepath.Join(base, ".", "wt")),
	)
}

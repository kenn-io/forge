package fleet

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWorktreeScopedKeyPrefixesNormalizedPath(t *testing.T) {
	assert := assert.New(t)
	base := t.TempDir()
	assert.Equal(
		"worktree:"+filepath.Join(base, "wt"),
		WorktreeScopedKey(filepath.Join(base, ".", "wt")),
	)
}

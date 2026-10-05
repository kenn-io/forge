package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A running agent can hold the context file open while another agent
// launches. os.Root opens with the same share mode as Node and Rust readers,
// which still lets the file be replaced.
func TestWriteGeneratedFileAtomicReplacesFileHeldOpenByReader(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	worktree := t.TempDir()
	path := filepath.Join(worktree, "CLAUDE.local.md")
	require.NoError(os.WriteFile(path, []byte("old\n"), 0o644))
	root, err := os.OpenRoot(worktree)
	require.NoError(err)
	t.Cleanup(func() { _ = root.Close() })
	reader, err := root.Open("CLAUDE.local.md")
	require.NoError(err)
	t.Cleanup(func() { _ = reader.Close() })

	require.NoError(writeGeneratedFileAtomic(worktree, "CLAUDE.local.md", []byte("new\n")))

	content, err := os.ReadFile(path)
	require.NoError(err)
	assert.Equal("new\n", string(content))
	entries, err := os.ReadDir(worktree)
	require.NoError(err)
	require.Len(entries, 1, "no temporary file may be left behind")
	assert.Equal("CLAUDE.local.md", entries[0].Name())
}

package landedwork

import (
	"testing"

	gitdiff "github.com/sourcegraph/go-diff/diff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEditHunkBytes(t *testing.T) {
	for _, tc := range []struct {
		body           string
		mark           int32
		removed, added string
	}{
		{"-old\n+new", 0, "old\n", "new"},
		{"-old\n+new\n", 5, "old", "new\n"},
		{"-\xff\r\n+\xfe\r\n", 0, "\xff\r\n", "\xfe\r\n"},
	} {
		run, err := editHunk(&gitdiff.Hunk{Body: []byte(tc.body), OrigNoNewlineAt: tc.mark, OrigLines: 1, NewLines: 1})
		require.NoError(t, err)
		assert.Equal(t, editRun{removed: tc.removed, added: tc.added}, run)
	}
}

func TestEditMetadataRejectsTruncation(t *testing.T) {
	for _, raw := range []string{":100644 100644\x00name\x00", ":100644 100644 a b M\x00name", "garbage"} {
		_, err := parseEditFiles([]byte(raw))
		require.ErrorIs(t, err, errEdits)
	}
}

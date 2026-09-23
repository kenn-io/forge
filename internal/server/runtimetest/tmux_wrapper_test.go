package runtimetest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/workspaceapi"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestIsWorkingTmuxTitleDetectsCodexSpinner(t *testing.T) {
	assert := assert.New(t)

	cases := []struct {
		name    string
		title   string
		working bool
	}{
		{
			name:    "codex spinner frame",
			title:   "⠴ t3code-b5014b03",
			working: true,
		},
		{
			name:    "another codex spinner frame",
			title:   "⠦ t3code-b5014b03",
			working: true,
		},
		{
			name:    "settled codex title",
			title:   "t3code-b5014b03",
			working: false,
		},
		{
			name:    "english busy title is not protocol",
			title:   "codex working",
			working: false,
		},
		{
			name:    "opencode style title is not protocol",
			title:   "OC | Run sleep 10",
			working: false,
		},
		{
			name:    "pi style title is not protocol",
			title:   "π - tmp.foo",
			working: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(tc.working, workspaceapi.IsWorkingTmuxTitle(tc.title))
		})
	}
}

// TestReadTmuxRecordPreservesEmptyArgs pins down the parser's
// empty-arg handling. The NUL-delimited record format was chosen to
// round-trip argv with empty-string elements unambiguously; the
// parser must keep interior and trailing empties rather than
// collapsing them.
func TestReadTmuxRecordPreservesEmptyArgs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	path := filepath.Join(t.TempDir(), "record")

	// First record: 3 args with an interior empty ("a", "", "b").
	// Second record: 2 args with a trailing empty ("x", "").
	body := "3\x00a\x00\x00b\x00" + "2\x00x\x00\x00"
	require.NoError(os.WriteFile(path, []byte(body), 0o644))

	argvs := serverfake.ReadTmuxRecord(t, path)
	require.Len(argvs, 2)
	assert.Equal([]string{"a", "", "b"}, argvs[0])
	assert.Equal([]string{"x", ""}, argvs[1])
}

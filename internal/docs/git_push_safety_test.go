package docs

import (
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyPushURLDriveLetterPaths(t *testing.T) {
	assert := Assert.New(t)
	root := t.TempDir()

	// On non-Windows platforms a drive-letter string is an scp-like ssh
	// URL for a single-letter host.
	gitParsesDrivePaths = false
	t.Cleanup(func() { gitParsesDrivePaths = false })
	class, err := classifyPushURL(root, `C:/evil.git`)
	require.NoError(t, err)
	assert.Equal(pushTargetNetwork, class)

	// On Windows git parses the same string as a local filesystem path,
	// so it must take the local branch (containment + receive-pack
	// hardening), never the network one.
	gitParsesDrivePaths = true
	for _, raw := range []string{`C:\evil.git`, `C:/evil.git`, `c:relative.git`, `file:///C:/evil.git`} {
		class, err := classifyPushURL(root, raw)
		if err == nil {
			assert.Equal(pushTargetLocal, class, "url %s classified as network", raw)
		}
	}

	assert.False(hasDriveLetterPrefix("host:path"))
	assert.False(hasDriveLetterPrefix("ab:path"))
	assert.True(hasDriveLetterPrefix(`Z:\x`))
}

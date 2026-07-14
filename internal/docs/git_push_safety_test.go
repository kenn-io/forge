package docs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyPushURLDriveLetterPaths(t *testing.T) {
	assert := assert.New(t)
	root := t.TempDir()

	// On non-Windows platforms a drive-letter string is an scp-like ssh
	// URL for a single-letter host.
	gitParsesDrivePaths = false
	t.Cleanup(func() { gitParsesDrivePaths = false })
	class, err := classifyRemoteURL(root, `C:/evil.git`, "push")
	require.NoError(t, err)
	assert.Equal(pushTargetNetwork, class)

	// On Windows git parses the same string as a local filesystem path,
	// so it must take the local branch (containment + receive-pack
	// hardening), never the network one.
	gitParsesDrivePaths = true
	for _, raw := range []string{`C:\evil.git`, `C:/evil.git`, `c:relative.git`, `file:///C:/evil.git`} {
		class, err := classifyRemoteURL(root, raw, "push")
		if err == nil {
			assert.Equal(pushTargetLocal, class, "url %s classified as network", raw)
		}
	}

	assert.False(hasDriveLetterPrefix("host:path"))
	assert.False(hasDriveLetterPrefix("ab:path"))
	assert.True(hasDriveLetterPrefix(`Z:\x`))
}

func TestClassifyRemoteURLLabelsDirection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()

	_, pushErr := classifyRemoteURL(root, "ext::sh -c true", "push")
	require.Error(pushErr)
	assert.Contains(pushErr.Error(), "push url ext::sh -c true")

	_, fetchErr := classifyRemoteURL(root, "ext::sh -c true", "fetch")
	require.Error(fetchErr)
	assert.Contains(fetchErr.Error(), "fetch url ext::sh -c true")

	_, insideErr := classifyRemoteURL(root, root, "fetch")
	require.Error(insideErr)
	assert.Contains(insideErr.Error(), "fetch target resolves inside the docs folder")
}

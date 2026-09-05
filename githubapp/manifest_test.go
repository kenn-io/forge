package githubapp_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
)

func TestManifestUsesOnlyCallerPermissions(t *testing.T) {
	assert := assert.New(t)
	permissions := map[string]string{"contents": "read", "pull_requests": "read", "metadata": "read"}
	manifest, err := githubapp.NewManifest("team-a-analysis", "https://example.org", "https://example.org/callback", permissions, []string{})
	require.NoError(t, err)
	assert.Equal(map[string]string{"contents": "read", "pull_requests": "read", "metadata": "read"}, manifest.DefaultPermissions)
	assert.False(manifest.HookAttributes.Active)
	permissions["issues"] = "read"
	assert.NotContains(manifest.DefaultPermissions, "issues")
}

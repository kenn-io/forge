package docs

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// GIT_* binding and config-redirect variables are stripped by kit's
// gitcmd.Runner (StripEnv); only the middleman-specific secret stripping
// is owned and therefore tested here.
func TestStripDocsSecretEnvDropsCredentialLikeVars(t *testing.T) {
	assert := assert.New(t)

	got := stripDocsSecretEnv([]string{
		"PATH=/bin",
		"MIDDLEMAN_GITHUB_TOKEN=provider-secret",
		"MIDDLEMAN_CUSTOM_TOKEN=custom-secret",
		"MSGVAULT_API_KEY=message-secret",
		"SERVICE_PASSWORD=password-secret",
		"AWS_ACCESS_KEY=cloud-secret",
		"UNRELATED=value",
	})

	assert.Equal([]string{"PATH=/bin", "UNRELATED=value"}, got)
}

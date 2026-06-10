package docs

import (
	"testing"

	Assert "github.com/stretchr/testify/assert"
)

func TestCleanGitEnvStripsConfigRedirects(t *testing.T) {
	assert := Assert.New(t)

	got := cleanGitEnv([]string{
		"PATH=/bin",
		"GIT_DIR=/tmp/host/.git",
		"GIT_WORK_TREE=/tmp/host",
		"GIT_CONFIG=/tmp/host/.git/config",
		"GIT_CONFIG_GLOBAL=/tmp/global",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=user.name",
		"GIT_CONFIG_VALUE_0=Middleman Fixture",
		"GIT_AUTHOR_NAME=Middleman Fixture",
		"GIT_COMMITTER_EMAIL=middleman-fixture@example.invalid",
		"GIT_SSH_COMMAND=ssh -i /tmp/key",
		"MIDDLEMAN_GITHUB_TOKEN=provider-secret",
		"MIDDLEMAN_CUSTOM_TOKEN=custom-secret",
		"MSGVAULT_API_KEY=message-secret",
		"SERVICE_PASSWORD=password-secret",
		"UNRELATED=value",
	})

	assert.Equal([]string{
		"PATH=/bin",
		"GIT_AUTHOR_NAME=Middleman Fixture",
		"GIT_COMMITTER_EMAIL=middleman-fixture@example.invalid",
		"GIT_SSH_COMMAND=ssh -i /tmp/key",
		"UNRELATED=value",
	}, got)
}

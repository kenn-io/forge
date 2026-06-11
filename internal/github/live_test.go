package github

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

const liveGitHubTestsEnv = "MIDDLEMAN_LIVE_GITHUB_TESTS"

func skipUnlessLiveGitHubTests(t *testing.T) {
	t.Helper()
	if os.Getenv(liveGitHubTestsEnv) != "1" {
		t.Skipf("set %s=1 to validate against GitHub", liveGitHubTestsEnv)
	}
}

func liveGitHubToken() string {
	if token := os.Getenv("MIDDLEMAN_GITHUB_TOKEN"); token != "" {
		return token
	}
	return os.Getenv("GITHUB_TOKEN")
}

func requireLiveGitHubToken(t *testing.T) string {
	t.Helper()
	token := liveGitHubToken()
	require.NotEmpty(t, token, "set MIDDLEMAN_GITHUB_TOKEN or GITHUB_TOKEN")
	return token
}

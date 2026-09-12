package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceDaemonStartsAndRestartsWithoutGitHubLogin(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newDaemonLifecycleFixture(t, buildForge(t))
	dataDir := fixture.dataDir("service-data")
	secret := filepath.Join(fixture.root, "client-secret")
	require.NoError(os.WriteFile(secret, []byte("synthetic-secret"), 0o600))
	port := reserveFreePort(t)
	require.NoError(os.WriteFile(fixture.configPath, []byte(fmt.Sprintf(`
host = "127.0.0.1"
port = %d
data_dir = %q
[service]
enabled = true
github_user_id = 123
github_client_id = "synthetic-client"
github_client_secret_file = %q
base_url = "https://forge.example.com"
`, port, dataDir, secret)), 0o600))
	t.Cleanup(func() { _, _, _ = fixture.run("stop") })
	for _, action := range []string{"start", "restart"} {
		_, stderr, err := fixture.run(action)
		require.NoError(err, stderr)
		fixture.verifiedRuntime(dataDir)
		for _, check := range []struct {
			path   string
			status int
			body   string
		}{
			{"/", http.StatusOK, "Sign in with GitHub"},
			{"/healthz", http.StatusOK, "ok"},
			{"/api/v1/snapshot", http.StatusUnauthorized, "Sign in with GitHub"},
		} {
			response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, check.path))
			require.NoError(err)
			body, err := io.ReadAll(response.Body)
			require.NoError(response.Body.Close())
			require.NoError(err)
			assert.Equal(check.status, response.StatusCode, check.path)
			assert.Contains(string(body), check.body)
		}
	}
}

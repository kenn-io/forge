package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/runtimelock"
)

// TestAuthURLBootstrapE2E pins the browser login flow end to end
// against a real require_auth daemon started from the built binary:
// `middleman auth url` prints a bootstrap link from the runtime
// metadata, following it sets the session cookie and strips the token
// from the redirect target, the cookie authorizes the real API, and
// /auth/logout expires it again. The link is followed as soon as the
// metadata exists — deliberately not waiting for full readiness —
// because the CLI hands out URLs during the startup window and the
// startup handler must already honor them.
func TestAuthURLBootstrapE2E(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	bin := buildMiddleman(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	cfgPath := filepath.Join(root, "config.toml")
	port := reserveFreePort(t)
	writeMinimalConfig(t, cfgPath, dataDir, port)
	appendConfig(t, cfgPath, "\n[api]\nrequire_auth = true\n")

	daemon := procutil.Command(bin, "--config", cfgPath)
	daemon.Stdout = os.Stderr
	daemon.Stderr = os.Stderr
	daemon.Env = append(os.Environ(), "MIDDLEMAN_LOG_LEVEL=warn")
	require.NoError(daemon.Start())
	t.Cleanup(func() {
		if daemon.Process != nil {
			_ = daemon.Process.Signal(syscall.SIGTERM)
			_ = daemon.Wait()
		}
	})
	waitForFile(t, runtimelock.MetadataPath(dataDir), 10*time.Second)
	waitForFile(t, runtimelock.AuthTokenPath(dataDir), 10*time.Second)

	urlCmd := procutil.Command(bin, "auth", "url", "--config", cfgPath)
	printed, err := urlCmd.Output()
	require.NoError(err)
	bootstrap := strings.TrimSpace(string(printed))
	require.Contains(bootstrap, fmt.Sprintf("http://127.0.0.1:%d/", port))

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(bootstrap)
	require.NoError(err)
	resp.Body.Close()
	require.Equal(http.StatusSeeOther, resp.StatusCode,
		"the printed link must bootstrap even during the startup window")
	assert.Equal("/", resp.Header.Get("Location"),
		"token must be stripped from the redirect target")
	cookies := resp.Cookies()
	require.Len(cookies, 1)
	assert.Equal("middleman_auth", cookies[0].Name)

	waitForDaemonReady(t, port)
	apiURL := fmt.Sprintf("http://127.0.0.1:%d/api/v1/snapshot", port)
	apiReq, err := http.NewRequest(http.MethodGet, apiURL, nil)
	require.NoError(err)
	apiReq.AddCookie(cookies[0])
	apiResp, err := client.Do(apiReq)
	require.NoError(err)
	apiResp.Body.Close()
	assert.Equal(http.StatusOK, apiResp.StatusCode,
		"the bootstrap cookie authorizes the real API")

	logoutResp, err := client.Get(
		fmt.Sprintf("http://127.0.0.1:%d/auth/logout", port),
	)
	require.NoError(err)
	logoutResp.Body.Close()
	require.Equal(http.StatusSeeOther, logoutResp.StatusCode)
	logoutCookies := logoutResp.Cookies()
	require.Len(logoutCookies, 1)
	assert.Negative(logoutCookies[0].MaxAge, "logout must expire the cookie")
}

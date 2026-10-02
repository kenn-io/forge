package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
)

func TestVanillaContainerStartupE2E(t *testing.T) {
	bin := buildForge(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENN_FORGE_HOME", filepath.Join(home, ".kenn", "forge"))
	t.Setenv("KENN_FORGE_HOST", "0.0.0.0")
	t.Setenv("KENN_FORGE_PORT", "8091")
	t.Setenv("KENN_FORGE_REQUIRE_AUTH", "true")
	t.Setenv("KENN_FORGE_LOG_LEVEL", "warn")
	var originalToken string
	for _, proxy := range []bool{false, true} {
		t.Setenv("KENN_FORGE_TRUST_REVERSE_PROXY", strconv.FormatBool(proxy))
		port := reserveFreePort(t)
		authority := "127.0.0.1:" + strconv.Itoa(port)
		baseURL := "http://" + authority
		cmd := procutil.Command(bin, "serve", "--host", "0.0.0.0", "--port", strconv.Itoa(port), "--disable-sync")
		log, err := os.Create(filepath.Join(t.TempDir(), "serve.log"))
		require.NoError(t, err)
		t.Cleanup(func() { _ = log.Close() })
		cmd.Stdout, cmd.Stderr = log, log
		require.NoError(t, cmd.Start())
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		t.Cleanup(func() {
			if !stopped {
				_ = cmd.Process.Kill()
				<-done
			}
		})
		client := &http.Client{Timeout: time.Second}
		requestStatus := func(path, token, host string) int {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+path, nil)
			require.NoError(t, err)
			req.Header.Set("X-Forwarded-Host", authority)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Del("X-Forwarded-Host")
			}
			if host != "" {
				req.Host = host
			}
			resp, err := client.Do(req)
			if err != nil {
				return 0
			}
			defer resp.Body.Close()
			return resp.StatusCode
		}
		require.Eventually(t, func() bool { return requestStatus("/healthz", "", "") == http.StatusOK }, 60*time.Second, 100*time.Millisecond)
		token, err := os.ReadFile(filepath.Join(home, ".kenn", "forge", "auth_token"))
		require.NoError(t, err)
		tokenInfo, err := os.Stat(filepath.Join(home, ".kenn", "forge", "auth_token"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), tokenInfo.Mode().Perm())
		if originalToken == "" {
			originalToken = string(token)
		} else {
			assert.Equal(t, originalToken, string(token))
		}
		assert.Equal(t, http.StatusUnauthorized, requestStatus("/api/v1/snapshot", "", ""))
		assert.Equal(t, http.StatusOK, requestStatus("/api/v1/snapshot", strings.TrimSpace(string(token)), ""))
		assert.Equal(t, http.StatusForbidden, requestStatus("/healthz", "", "attacker.example:8091"))
		out, stderr, err := runDaemonLifecycle(bin, "status", "--json")
		require.NoError(t, err, stderr)
		assert.Contains(t, out, authority)
		require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
		select {
		case err := <-done:
			require.NoError(t, err)
			stopped = true
		case <-time.After(10 * time.Second):
			require.FailNow(t, "serve did not stop gracefully")
		}
	}
}

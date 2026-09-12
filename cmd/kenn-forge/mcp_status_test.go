package main

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/runtimelock"
)

func TestMCPStatusReportsActiveListenerFromDaemon(t *testing.T) {
	for _, tc := range []struct {
		name, settings string
		active, auth   bool
	}{
		{"active despite saved disable", `{"mcp":{"enabled":false,"active_url":"http://127.0.0.1:9123/mcp","active_requires_auth":true,"restart_required":true}}`, true, true},
		{"active without auth", `{"mcp":{"enabled":true,"active_url":"http://127.0.0.1:9123/mcp","active_requires_auth":false}}`, true, false},
		{"saved enable awaits restart", `{"mcp":{"enabled":true,"active_url":"","restart_required":true}}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			root := t.TempDir()
			token, err := runtimelock.EnsureAuthToken(root)
			tokenPath := runtimelock.AuthTokenPath(root)
			require.NoError(err)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/forge/api/v1/settings" || r.Header.Get("Authorization") != "Bearer "+token {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = w.Write([]byte(tc.settings))
			}))
			t.Cleanup(server.Close)
			lock, err := runtimelock.Acquire(root)
			require.NoError(err)
			t.Cleanup(func() { require.NoError(lock.Release()) })
			require.NoError(lock.WriteMetadata(runtimelock.Metadata{
				PID: os.Getpid(), ListenAddr: strings.TrimPrefix(server.URL, "http://"), BasePath: "/forge", TokenPath: tokenPath,
			}))
			configPath := filepath.Join(root, "config.toml")
			require.NoError(os.WriteFile(configPath, fmt.Appendf(nil, "data_dir = %q\n", root), 0o600))
			var output bytes.Buffer
			command := newMCPCommand(&output, loadMCPQuickstart)
			command.SetArgs([]string{"status", "--json", "--config", configPath})
			require.NoError(command.ExecuteContext(t.Context()))
			var rows []mcpListenerStatus
			require.NoError(json.Unmarshal(output.Bytes(), &rows))
			if !tc.active {
				assert.JSONEq(`[]`, output.String())
				return
			}
			require.Len(rows, 1)
			assert.Equal("http://127.0.0.1:9123/mcp", rows[0].URL)
			assert.Equal(server.URL+"/forge", rows[0].BackendURL)
			assert.Equal("http", rows[0].Transport)
			assert.Equal(os.Getpid(), rows[0].PID)
			if tc.auth {
				assert.Equal(tokenPath, rows[0].TokenPath)
			} else {
				assert.NotContains(output.String(), "token_path")
			}
			assert.NotContains(output.String(), token)
		})
	}
}

func TestMCPStatusStoppedDoesNotCreateRuntime(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	dataDir := filepath.Join(root, "data")
	require.NoError(os.WriteFile(configPath, fmt.Appendf(nil, "data_dir = %q\n", dataDir), 0o600))
	var output bytes.Buffer
	command := newMCPCommand(&output, loadMCPQuickstart)
	command.SetArgs([]string{"status", "--json", "--config", configPath})
	require.NoError(command.ExecuteContext(t.Context()))
	assert.JSONEq(`[]`, output.String())
	_, err := os.Stat(runtimelock.LockPath(dataDir))
	assert.ErrorIs(err, os.ErrNotExist)
}

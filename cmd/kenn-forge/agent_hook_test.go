package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/runtimelock"
)

func TestAgentHookSessionEndReportsDaemonFailure(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	require.NoError(os.WriteFile(configPath, fmt.Appendf(nil, "data_dir = %q\n", filepath.ToSlash(root)), 0o600))
	lock, err := runtimelock.Acquire(root)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(lock.Release()) })
	require.NoError(lock.WriteMetadata(runtimelock.Metadata{PID: os.Getpid(), ListenAddr: server.Listener.Addr().String()}))
	_, err = runtimelock.EnsureAuthToken(root)
	require.NoError(err)
	for _, event := range []string{"SessionEnd", "SessionStart", "Stop"} {
		err := receiveAgentHook(t.Context(), "claude", configPath, agentHookSource, strings.NewReader(`{"session_id":"chat","hook_event_name":"`+event+`","source":"startup","reason":"prompt_input_exit"}`), io.Discard)
		if event == "SessionEnd" {
			require.ErrorContains(err, "500")
		} else {
			require.NoError(err)
		}
	}
	server.Close()
	require.NoError(receiveAgentHook(t.Context(), "claude", configPath, agentHookSource, strings.NewReader(`{"session_id":"chat","hook_event_name":"SessionEnd","reason":"prompt_input_exit"}`), io.Discard))
}

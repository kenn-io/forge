package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentHookSessionEndReportsDaemonFailure(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusInternalServerError, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			require := require.New(t)
			server, configPath, _ := agentHookDaemonFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, err := io.WriteString(w, `{"detail":"record agent hook activity: remove D:\\activity\\report.json: access denied"}`)
				assert.NoError(t, err)
			}))
			for _, event := range []string{"SessionEnd", "SessionStart", "Stop"} {
				err := receiveAgentHook(t.Context(), "claude", configPath, agentHookSource, strings.NewReader(`{"session_id":"chat","hook_event_name":"`+event+`","source":"startup","reason":"prompt_input_exit"}`), io.Discard)
				if event == "SessionEnd" && status >= http.StatusInternalServerError {
					require.ErrorContains(err, "500")
					require.ErrorContains(err, `remove D:\activity\report.json: access denied`)
				} else {
					require.NoError(err)
				}
			}
			server.Close()
			require.NoError(receiveAgentHook(t.Context(), "claude", configPath, agentHookSource, strings.NewReader(`{"session_id":"chat","hook_event_name":"SessionEnd","reason":"prompt_input_exit"}`), io.Discard))
		})
	}
}

func TestAgentHookSessionEndBoundsDaemonDetail(t *testing.T) {
	t.Parallel()
	_, configPath, _ := agentHookDaemonFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, err := io.WriteString(w, "invalid")
		assert.NoError(t, err)
	}))
	err := receiveAgentHook(t.Context(), "claude", configPath, agentHookSource, strings.NewReader(`{"session_id":"chat","hook_event_name":"SessionEnd","reason":"prompt_input_exit"}`), io.Discard)
	require.EqualError(t, err, "handle SessionEnd agent hook: daemon rejected agent hook: 500 Internal Server Error")
}

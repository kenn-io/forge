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
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"server failure", http.StatusInternalServerError, `{"detail":"record agent hook activity: remove D:\\activity\\report.json: access denied"}`},
		{"unauthorized", http.StatusUnauthorized, `{"detail":"record agent hook activity: remove D:\\activity\\report.json: access denied"}`},
		{"unparseable body", http.StatusInternalServerError, "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			server, configPath, _ := agentHookDaemonFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, err := io.WriteString(w, tc.body)
				assert.NoError(t, err)
			}))
			for _, event := range []string{"SessionEnd", "SessionStart", "Stop"} {
				err := receiveAgentHook(t.Context(), "claude", configPath, agentHookSource, strings.NewReader(`{"session_id":"chat","hook_event_name":"`+event+`","source":"startup","reason":"prompt_input_exit"}`), io.Discard)
				if event == "SessionEnd" && tc.status >= http.StatusInternalServerError {
					require.ErrorContains(err, "500")
					if tc.body == "invalid" {
						require.EqualError(err, "handle SessionEnd agent hook: daemon rejected agent hook: 500 Internal Server Error")
					} else {
						require.ErrorContains(err, `remove D:\activity\report.json: access denied`)
					}
				} else {
					require.NoError(err)
				}
			}
			server.Close()
			require.NoError(receiveAgentHook(t.Context(), "claude", configPath, agentHookSource, strings.NewReader(`{"session_id":"chat","hook_event_name":"SessionEnd","reason":"prompt_input_exit"}`), io.Discard))
		})
	}
}

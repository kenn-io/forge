package server

import (
	"bufio"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// TestSettingsACPAgentHelper is a minimal ACP agent that starts only when
// FORGE_TEST_AGENT_MODE is "configured", and records that it launched.
func TestSettingsACPAgentHelper(t *testing.T) {
	if os.Getenv("KENN_FORGE_SETTINGS_ACP_HELPER") != "1" {
		return
	}
	_ = os.WriteFile(os.Getenv("KENN_FORGE_SETTINGS_ACP_LAUNCHED"), nil, 0o600)
	if os.Getenv("FORGE_TEST_AGENT_MODE") != "configured" {
		os.Exit(3)
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			os.Exit(0)
		}
		var message struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
		}
		if json.Unmarshal(line, &message) != nil || len(message.ID) == 0 {
			continue
		}
		result := `{}`
		switch message.Method {
		case "initialize":
			result = `{"protocolVersion":1,"agentCapabilities":{}}`
		case "session/new":
			result = `{"sessionId":"settings-test"}`
		}
		fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", message.ID, result)
	}
}

// Testing an ACP agent from settings launches it with the draft's
// environment, and rejects an invalid variable name before launching.
func TestTestACPAgentLaunchesWithItsEnvironment(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, _, _ := setupTestServer(t)
	srv.runtime = localruntime.NewManager(localruntime.Options{})
	launched := filepath.Join(t.TempDir(), "launched")
	command := []string{os.Args[0], "-test.run=^TestSettingsACPAgentHelper$"}
	// The helper's own switches travel in the draft's environment too.
	env := map[string]string{"KENN_FORGE_SETTINGS_ACP_HELPER": "1", "KENN_FORGE_SETTINGS_ACP_LAUNCHED": launched}

	invalid := maps.Clone(env)
	invalid["1BAD"] = "configured"
	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/settings/agents/test-acp", map[string]any{
		"command": command, "env": invalid,
	})
	assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.NoFileExists(t, launched, "an invalid name is rejected before the agent starts")

	env["FORGE_TEST_AGENT_MODE"] = "configured"
	rr = testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/settings/agents/test-acp", map[string]any{
		"command": command, "env": env,
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var result testACPAgentResult
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &result))
	assert.True(t, result.Valid, result.Message)
	assert.FileExists(t, launched)
}

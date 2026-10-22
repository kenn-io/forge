package codexhooks

import (
	"bytes"
	"encoding/json/jsontext"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEffectiveAccessWithCodex(t *testing.T) {
	const permissions = "default_permissions = \":danger-full-access\"\n"
	for _, tc := range []struct {
		name, executable, config, projectConfig, sandboxMode, sandbox, approval string
		args                                                                    []string
	}{
		{name: "project access overrides global", executable: "codex", config: "sandbox_mode = \"danger-full-access\"\n", projectConfig: "sandbox_mode = \"read-only\"\napproval_policy = \"on-request\"\n", sandboxMode: "read-only", sandbox: "readOnly", approval: "on-request"},
		{name: "default permissions", executable: "codex", config: permissions, sandboxMode: "danger-full-access", sandbox: "dangerFullAccess", approval: "never"},
		{name: "default permissions through codex-acp cli", executable: "codex-acp", args: []string{"cli"}, config: permissions, sandboxMode: "danger-full-access", sandbox: "dangerFullAccess", approval: "never"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executable, err := exec.LookPath(tc.executable)
			if err != nil {
				t.Skipf("%s is not installed", tc.executable)
			}
			assert := assert.New(t)
			require := require.New(t)
			codexHome := t.TempDir()
			workspace := t.TempDir()
			t.Setenv("CODEX_HOME", codexHome)
			config := "# Preserve user comments.\n" + tc.config + "approval_policy = \"never\"\n[features]\nplugins = false\n"
			configPath := filepath.Join(codexHome, "config.toml")
			require.NoError(os.WriteFile(configPath, []byte(config), 0o600))
			projectPath := filepath.Join(workspace, ".codex", "config.toml")
			if tc.projectConfig != "" {
				require.NoError(os.MkdirAll(filepath.Dir(projectPath), 0o700))
				require.NoError(os.WriteFile(projectPath, []byte(tc.projectConfig), 0o600))
			}

			// CODEX_HOME does not isolate machine-wide requirements, including
			// those applied by the adapter's bundled Codex.
			c, closeClient, err := startClient(t.Context(), executable, tc.args, nil, workspace)
			require.NoError(err)
			var result struct {
				Requirements struct {
					AllowedSandboxModes       []string        `json:"allowedSandboxModes"`
					AllowedApprovalPolicies   []any           `json:"allowedApprovalPolicies"`
					AllowedPermissionProfiles map[string]bool `json:"allowedPermissionProfiles"`
					DefaultPermissions        *string         `json:"defaultPermissions"`
				} `json:"requirements"`
			}
			err = c.call("configRequirements/read", nil, &result)
			closeClient()
			require.NoError(err)
			policy := result.Requirements
			if policy.AllowedSandboxModes != nil && !slices.Contains(policy.AllowedSandboxModes, tc.sandboxMode) {
				t.Skipf("machine requirements exclude %s: %v", tc.sandboxMode, policy.AllowedSandboxModes)
			}
			if policy.AllowedApprovalPolicies != nil && !slices.ContainsFunc(policy.AllowedApprovalPolicies, func(p any) bool { return p == tc.approval }) {
				t.Skipf("machine requirements exclude %s approval: %v", tc.approval, policy.AllowedApprovalPolicies)
			}
			profile := ":" + tc.sandboxMode
			if policy.AllowedPermissionProfiles != nil && !policy.AllowedPermissionProfiles[profile] {
				t.Skipf("machine requirements exclude %s: %v", profile, policy.AllowedPermissionProfiles)
			}
			if tc.projectConfig != "" && policy.DefaultPermissions != nil && *policy.DefaultPermissions != profile {
				t.Skipf("machine requirements force default permissions: %s", *policy.DefaultPermissions)
			}

			command := append([]string{executable}, tc.args...)
			sandbox, approval, err := EffectiveAccess(t.Context(), command, os.Environ(), workspace, nil)
			require.NoError(err)
			assert.Equal(tc.sandbox, sandbox)
			assert.Equal(tc.approval, approval)
			after, err := os.ReadFile(configPath)
			require.NoError(err)
			assert.Equal([]byte(config), after, "access probe must not persist project trust or rewrite config")
			if tc.projectConfig != "" {
				after, err := os.ReadFile(projectPath)
				require.NoError(err)
				assert.Equal([]byte(tc.projectConfig), after, "access probe must not rewrite project config")
			}
		})
	}
}

func TestReadAccess(t *testing.T) {
	for _, tc := range []struct {
		name, reply, sandbox, approval, failure, wantRequest string
		config                                               map[string]any
	}{
		{name: "full access", reply: `{"result":{"sandbox":{"type":"dangerFullAccess"},"approvalPolicy":"never"}}`, sandbox: "dangerFullAccess", approval: "never", wantRequest: `{"id":1,"method":"thread/start","params":{"ephemeral":true,"config":{"projects":{"/workspace":{"trust_level":"trusted"}}}}}`},
		{name: "caller config preserved with projects overridden", reply: `{"result":{"sandbox":{"type":"readOnly"},"approvalPolicy":"on-request"}}`, sandbox: "readOnly", approval: "on-request", config: map[string]any{"model": "example-model", "projects": map[string]any{"/workspace": map[string]any{"trust_level": "untrusted"}}}, wantRequest: `{"id":1,"method":"thread/start","params":{"ephemeral":true,"config":{"model":"example-model","projects":{"/workspace":{"trust_level":"trusted"}}}}}`},
		{name: "granular approval", reply: `{"result":{"sandbox":{"type":"readOnly"},"approvalPolicy":{"granular":{"sandbox_approval":true}}}}`, sandbox: "readOnly"},
		{name: "rpc error", reply: `{"error":{"message":"policy rejected"}}`, failure: "policy rejected"},
		{name: "missing sandbox type", reply: `{"result":{"sandbox":{"type":""},"approvalPolicy":"never"}}`, failure: "no sandbox type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent bytes.Buffer
			c := &client{input: &sent, output: jsontext.NewDecoder(strings.NewReader(`{"id":1,` + tc.reply[1:]))}
			sandbox, approval, err := c.readAccess("/workspace", tc.config)
			if tc.failure != "" {
				require.ErrorContains(t, err, tc.failure)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.sandbox, sandbox)
			assert.Equal(t, tc.approval, approval)
			if tc.wantRequest != "" {
				assert.JSONEq(t, tc.wantRequest, sent.String())
			}
		})
	}
}

package codexhooks

import (
	"bytes"
	"encoding/json/jsontext"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/gitsafe"
)

func TestMain(m *testing.M) {
	os.Exit(gitsafe.RunIsolatedMain(m))
}

func TestClientHandshake(t *testing.T) {
	var sent bytes.Buffer
	c := &client{
		input: &sent,
		output: jsontext.NewDecoder(strings.NewReader(
			"{\"id\":1,\"result\":{}}\n{\"id\":2,\"result\":{}}\n")),
	}
	require.NoError(t, c.initialize())
	require.NoError(t, c.call("config/read", map[string]bool{"includeLayers": true}, nil))

	// The acknowledgment is a notification between initialization and the
	// first request, with no request ID or response of its own.
	messages := strings.Split(strings.TrimSpace(sent.String()), "\n")
	require.Len(t, messages, 3)
	assert.JSONEq(t, `{"method":"initialized","params":{}}`, messages[1])
}

func TestConfigOptions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		want      []string
		supported bool
	}{
		{name: "resume", args: []string{"-c", "model=local", "--search", "resume", "session"}, want: []string{"-c", "model=local"}, supported: true},
		{name: "inline options", args: []string{"-cmodel=local", "--config=features.hooks=true", "--disable", "feature"}, want: []string{"-cmodel=local", "--config=features.hooks=true", "--disable", "feature"}, supported: true},
		{name: "prompt", args: []string{"--", "-p", "text"}, supported: true},
		{name: "profile", args: []string{"-p", "work"}},
		{name: "inline profile", args: []string{"--profile=work"}},
		{name: "short profile", args: []string{"-pwork"}},
		{name: "missing value", args: []string{"-c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options, supported := configOptions(tc.args)
			assert.Equal(t, tc.supported, supported)
			assert.Equal(t, tc.want, options)
		})
	}
}

func TestApprovalEdits(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "new")
	sibling := filepath.Join(t.TempDir(), "approved")
	path := filepath.Join(cwd, ".codex", "hooks.json")
	key := path + ":session_start:0:0"
	sourceKey := filepath.Join(sibling, ".codex", "hooks.json") + ":session_start:0:0"
	for _, tc := range []struct {
		name        string
		hash        string
		source      string
		enabled     bool
		sourceOff   bool
		hasDecision bool
		want        bool
	}{
		{name: "same reviewed hook", hash: "reviewed", source: "project", enabled: true, want: true},
		{name: "changed command", hash: "changed", source: "project", enabled: true},
		{name: "disabled hook", hash: "reviewed", source: "project"},
		{name: "disabled approval", hash: "reviewed", source: "project", enabled: true, sourceOff: true},
		{name: "existing decision", hash: "reviewed", source: "project", enabled: true, hasDecision: true},
		{name: "user hook", hash: "reviewed", source: "user", enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			states := map[string]hookState{sourceKey: {TrustedHash: "reviewed", Enabled: new(!tc.sourceOff)}}
			if tc.hasDecision {
				states[key] = hookState{TrustedHash: "previous"}
			}
			edits := approvalEdits(cwd, []string{sibling}, states, []hookMetadata{{
				Key: key, Source: tc.source, SourcePath: path, CurrentHash: tc.hash,
				TrustStatus: "untrusted", Enabled: tc.enabled,
			}})
			if tc.want {
				assert.Equal(t, []configEdit{{KeyPath: toml.Key{"hooks", "state", key, "trusted_hash"}.String(), Value: "reviewed", MergeStrategy: "replace"}}, edits)
			} else {
				assert.Empty(t, edits)
			}
		})
	}
}

func TestReuseApprovalsWithCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("Codex CLI is not installed")
	}
	assert := assert.New(t)
	require := require.New(t)
	repo := gitfixture.NewRepository(t, false)
	const hooks = `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo activity","timeout":2}]}]}}`
	repo.Write(t, ".codex/hooks.json", hooks)
	repo.Stage(t, ".codex/hooks.json")
	gitfixture.Run(t, repo.Dir, "commit", "-m", "add hook")
	bare := filepath.Join(t.TempDir(), "repository.git")
	gitfixture.Run(t, repo.Dir, "clone", "--bare", repo.Dir, bare)
	gitfixture.Run(t, bare, "config", "extensions.worktreeConfig", "true")

	worktrees := make(map[string]string)
	for _, name := range []string{"approved", "unchanged", "changed", "disabled"} {
		dir := filepath.Join(t.TempDir(), name)
		gitfixture.Run(t, bare, "worktree", "add", "--detach", dir, "HEAD")
		gitfixture.Run(t, dir, "config", "--worktree", "core.bare", "false")
		worktrees[name] = dir
	}
	other := gitfixture.NewRepository(t, false)
	other.Write(t, ".codex/hooks.json", hooks)
	worktrees["other repository"] = other.Dir
	changed := filepath.Join(worktrees["changed"], ".codex/hooks.json")
	require.NoError(os.WriteFile(changed, bytes.ReplaceAll([]byte(hooks), []byte("echo activity"), []byte("echo changed")), 0o600))

	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	projects := map[string]any{repo.Dir: map[string]string{"trust_level": "trusted"}}
	for _, dir := range worktrees {
		projects[dir] = map[string]string{"trust_level": "trusted"}
	}
	var config bytes.Buffer
	config.WriteString("# Preserve user comments.\n")
	// Keep unrelated plugin marketplace downloads out of the CLI fixture.
	require.NoError(toml.NewEncoder(&config).Encode(map[string]any{
		"projects": projects,
		"features": map[string]bool{"plugins": false},
	}))
	configPath := filepath.Join(codexHome, "config.toml")
	require.NoError(os.WriteFile(configPath, config.Bytes(), 0o600))
	c, closeClient, err := startClient(t.Context(), codex, nil, worktrees["approved"])
	require.NoError(err)
	defer closeClient()

	var listed struct {
		Data []struct {
			Hooks []struct {
				Key         string `json:"key"`
				CurrentHash string `json:"currentHash"`
				TrustStatus string `json:"trustStatus"`
				Enabled     bool   `json:"enabled"`
			} `json:"hooks"`
		} `json:"data"`
	}
	require.NoError(c.call("hooks/list", map[string]any{"cwds": []string{worktrees["approved"]}}, &listed))
	require.Len(listed.Data, 1)
	require.Len(listed.Data[0].Hooks, 1)
	source := listed.Data[0].Hooks[0]
	require.Equal("untrusted", source.TrustStatus)
	// A repository cannot manufacture a user approval in project config.
	var forged bytes.Buffer
	require.NoError(toml.NewEncoder(&forged).Encode(map[string]any{
		"hooks": map[string]any{"state": map[string]any{
			source.Key: map[string]string{"trusted_hash": source.CurrentHash},
		}},
	}))
	projectConfig := filepath.Join(worktrees["unchanged"], ".codex/config.toml")
	require.NoError(os.WriteFile(projectConfig, forged.Bytes(), 0o600))
	require.NoError(ReuseApprovals(t.Context(), []string{codex}, worktrees["unchanged"]))
	require.NoError(c.call("hooks/list", map[string]any{"cwds": []string{worktrees["unchanged"]}}, &listed))
	require.Len(listed.Data, 1)
	require.Len(listed.Data[0].Hooks, 1)
	assert.Equal("untrusted", listed.Data[0].Hooks[0].TrustStatus)
	require.NoError(os.Remove(projectConfig))

	disabledKey := filepath.Join(worktrees["disabled"], ".codex/hooks.json") + ":session_start:0:0"
	require.NoError(c.call("config/batchWrite", map[string]any{"edits": []any{
		map[string]any{"keyPath": toml.Key{"hooks", "state", source.Key, "trusted_hash"}.String(), "value": source.CurrentHash, "mergeStrategy": "replace"},
		map[string]any{"keyPath": toml.Key{"hooks", "state", source.Key, "enabled"}.String(), "value": false, "mergeStrategy": "replace"},
		map[string]any{"keyPath": toml.Key{"hooks", "state", disabledKey, "enabled"}.String(), "value": false, "mergeStrategy": "replace"},
	}}, nil))
	require.NoError(ReuseApprovals(t.Context(), []string{codex}, worktrees["unchanged"]))
	require.NoError(c.call("hooks/list", map[string]any{"cwds": []string{worktrees["unchanged"]}}, &listed))
	require.Len(listed.Data, 1)
	require.Len(listed.Data[0].Hooks, 1)
	assert.Equal("untrusted", listed.Data[0].Hooks[0].TrustStatus, "disabled source approval is not reused")
	require.NoError(c.call("config/batchWrite", map[string]any{"edits": []any{
		map[string]any{"keyPath": toml.Key{"hooks", "state", source.Key, "enabled"}.String(), "value": true, "mergeStrategy": "replace"},
	}}, nil))

	for _, name := range []string{"unchanged", "changed", "disabled", "other repository"} {
		dir := worktrees[name]
		require.NoError(ReuseApprovals(t.Context(), []string{codex}, dir), name)
		require.NoError(c.call("hooks/list", map[string]any{"cwds": []string{dir}}, &listed), name)
		require.Len(listed.Data, 1)
		require.Len(listed.Data[0].Hooks, 1)
		hook := listed.Data[0].Hooks[0]
		if name == "unchanged" {
			assert.Equal("trusted", hook.TrustStatus, name)
		} else {
			assert.Equal("untrusted", hook.TrustStatus, name)
		}
		assert.Equal(name != "disabled", hook.Enabled, name)
	}
	before, err := os.ReadFile(configPath)
	require.NoError(err)
	require.NoError(ReuseApprovals(t.Context(), []string{codex}, worktrees["unchanged"]))
	after, err := os.ReadFile(configPath)
	require.NoError(err)
	assert.Equal(string(before), string(after), "repeat launches must not rewrite approvals")
	assert.Contains(string(after), "# Preserve user comments.")
}

package localruntime

import (
	"errors"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/config"
)

func fakeLookPath(paths map[string]string) lookPathFunc {
	return func(name string) (string, error) {
		if path, ok := paths[name]; ok {
			return path, nil
		}
		return "", errors.New("not found")
	}
}

func findTarget(
	t *testing.T,
	targets []LaunchTarget,
	key string,
) LaunchTarget {
	t.Helper()
	for _, target := range targets {
		if target.Key == key {
			return target
		}
	}
	require.Failf(t, "target not found", "key %q", key)
	return LaunchTarget{}
}

func TestResolveLaunchTargetsConfigOverridesBuiltin(t *testing.T) {
	enabled := true
	cfg := []config.Agent{{
		Key:     "codex",
		Label:   "Custom Codex",
		Command: []string{"/opt/codex"},
		Enabled: &enabled,
	}}
	targets := ResolveLaunchTargets(
		cfg,
		[]string{"tmux"},
		fakeLookPath(map[string]string{
			"codex": "/usr/bin/codex",
			"tmux":  "/usr/bin/tmux",
		}),
	)

	codex := findTarget(t, targets, "codex")
	assert := Assert.New(t)
	assert.Equal("Custom Codex", codex.Label)
	assert.Equal(LaunchTargetAgent, codex.Kind)
	assert.Equal("config", codex.Source)
	assert.Equal([]string{"/opt/codex"}, codex.Command)
	assert.True(codex.Available)
}

func TestResolveLaunchTargetsDisabledConfigSuppressesBuiltin(
	t *testing.T,
) {
	disabled := false
	cfg := []config.Agent{{
		Key:     "codex",
		Enabled: &disabled,
	}}
	targets := ResolveLaunchTargets(
		cfg,
		[]string{"tmux"},
		fakeLookPath(map[string]string{
			"codex": "/usr/bin/codex",
		}),
	)

	codex := findTarget(t, targets, "codex")
	assert := Assert.New(t)
	assert.False(codex.Available)
	assert.Equal("config", codex.Source)
	assert.Contains(codex.DisabledReason, "disabled")
}

func TestResolveLaunchTargetsConfigKeyCoexistsWithBuiltin(
	t *testing.T,
) {
	cfg := []config.Agent{{
		Key:     "custom",
		Label:   "Custom Agent",
		Command: []string{"/opt/custom"},
	}}
	targets := ResolveLaunchTargets(
		cfg,
		[]string{"tmux"},
		fakeLookPath(map[string]string{
			"codex": "/usr/bin/codex",
			"tmux":  "/usr/bin/tmux",
		}),
	)

	assert := Assert.New(t)
	assert.Equal("Custom Agent", findTarget(t, targets, "custom").Label)
	assert.True(findTarget(t, targets, "custom").Available)
	assert.True(findTarget(t, targets, "codex").Available)
}

func TestResolveLaunchTargetsUndetectedBuiltinUnavailable(
	t *testing.T,
) {
	targets := ResolveLaunchTargets(nil, []string{"tmux"}, fakeLookPath(nil))

	codex := findTarget(t, targets, "codex")
	assert := Assert.New(t)
	assert.False(codex.Available)
	assert.Contains(codex.DisabledReason, "not found")
}

func TestResolveLaunchTargetsIncludesSystemTargets(t *testing.T) {
	targets := ResolveLaunchTargets(
		nil,
		[]string{"tmux"},
		fakeLookPath(map[string]string{
			"tmux": "/usr/bin/tmux",
		}),
	)

	tmux := findTarget(t, targets, "tmux")
	shell := findTarget(t, targets, "plain_shell")
	assert := Assert.New(t)
	assert.Equal(LaunchTargetTmux, tmux.Kind)
	assert.True(tmux.Available)
	assert.Equal([]string{"tmux"}, tmux.Command)
	assert.Equal(LaunchTargetPlainShell, shell.Kind)
	assert.True(shell.Available)
}

func TestResolveLaunchTargetsMarksTmuxUnavailable(t *testing.T) {
	targets := ResolveLaunchTargets(nil, []string{"tmux"}, fakeLookPath(nil))

	tmux := findTarget(t, targets, "tmux")
	assert := Assert.New(t)
	assert.Equal(LaunchTargetTmux, tmux.Kind)
	assert.False(tmux.Available)
	assert.Contains(tmux.DisabledReason, "not found")
}

func TestResolveLaunchTargetsUsesConfiguredTmuxCommand(t *testing.T) {
	targets := ResolveLaunchTargets(
		nil,
		[]string{"/opt/bin/tmux-wrapper", "--scope", "tmux"},
		fakeLookPath(map[string]string{
			"/opt/bin/tmux-wrapper": "/opt/bin/tmux-wrapper",
		}),
	)

	tmux := findTarget(t, targets, "tmux")
	assert := Assert.New(t)
	assert.Equal([]string{"/opt/bin/tmux-wrapper", "--scope", "tmux"}, tmux.Command)
	assert.True(tmux.Available)
}

package localruntime

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
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
	assert := assert.New(t)
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
	assert := assert.New(t)
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

	assert := assert.New(t)
	assert.Equal("Custom Agent", findTarget(t, targets, "custom").Label)
	assert.True(findTarget(t, targets, "custom").Available)
	assert.True(findTarget(t, targets, "codex").Available)
}

func TestResolveLaunchTargetsUndetectedBuiltinUnavailable(
	t *testing.T,
) {
	targets := ResolveLaunchTargets(nil, []string{"tmux"}, fakeLookPath(nil))

	codex := findTarget(t, targets, "codex")
	assert := assert.New(t)
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

	shell := findTarget(t, targets, "shell")
	plainShell := findTarget(t, targets, "plain_shell")
	assert := assert.New(t)
	assert.Equal(LaunchTargetShell, shell.Kind)
	assert.True(shell.Available)
	assert.Equal([]string{"tmux"}, shell.Command)
	assert.Equal(LaunchTargetPlainShell, plainShell.Kind)
	assert.True(plainShell.Available)
}

func TestResolveLaunchTargetsMarksTmuxUnavailable(t *testing.T) {
	targets := ResolveLaunchTargets(nil, []string{"tmux"}, fakeLookPath(nil))

	shell := findTarget(t, targets, "shell")
	assert := assert.New(t)
	assert.Equal(LaunchTargetShell, shell.Kind)
	assert.False(shell.Available)
	assert.Contains(shell.DisabledReason, "not found")
}

func TestResolveLaunchTargetsUsesConfiguredTmuxCommand(t *testing.T) {
	targets := ResolveLaunchTargets(
		nil,
		[]string{"/opt/bin/tmux-wrapper", "--scope", "tmux"},
		fakeLookPath(map[string]string{
			"/opt/bin/tmux-wrapper": "/opt/bin/tmux-wrapper",
		}),
	)

	shell := findTarget(t, targets, "shell")
	assert := assert.New(t)
	assert.Equal([]string{"/opt/bin/tmux-wrapper", "--scope", "tmux"}, shell.Command)
	assert.True(shell.Available)
}

func TestResolveLaunchTargetsSkipsSystemKeyAgents(t *testing.T) {
	cfg := []config.Agent{
		{
			Key:     "shell",
			Label:   "Configured Shell",
			Command: []string{"/opt/shell-agent"},
		},
		{
			Key:     "plain_shell",
			Label:   "Configured Plain Shell",
			Command: []string{"/opt/plain-shell-agent"},
		},
	}

	targets := ResolveLaunchTargets(
		cfg,
		[]string{"tmux"},
		fakeLookPath(map[string]string{
			"tmux": "/usr/bin/tmux",
		}),
	)

	assert := assert.New(t)
	assert.Len(targetsWithKey(targets, "shell"), 1)
	assert.Len(targetsWithKey(targets, "plain_shell"), 1)
	assert.Equal("system", findTarget(t, targets, "shell").Source)
	assert.Equal("system", findTarget(t, targets, "plain_shell").Source)
}

func targetsWithKey(targets []LaunchTarget, key string) []LaunchTarget {
	matches := make([]LaunchTarget, 0, 1)
	for _, target := range targets {
		if target.Key == key {
			matches = append(matches, target)
		}
	}
	return matches
}

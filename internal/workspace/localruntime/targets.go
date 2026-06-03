package localruntime

import (
	"fmt"
	"os/exec"
	"slices"

	"go.kenn.io/middleman/internal/config"
)

type lookPathFunc func(string) (string, error)

var builtinAgents = []LaunchTarget{
	{
		Key: "codex", Label: "Codex", Kind: LaunchTargetAgent,
		Source: "builtin", Command: []string{"codex"},
	},
	{
		Key: "claude", Label: "Claude", Kind: LaunchTargetAgent,
		Source: "builtin", Command: []string{"claude"},
	},
	{
		Key: "gemini", Label: "Gemini", Kind: LaunchTargetAgent,
		Source: "builtin", Command: []string{"gemini"},
	},
	{
		Key: "opencode", Label: "opencode", Kind: LaunchTargetAgent,
		Source: "builtin", Command: []string{"opencode"},
	},
	{
		Key: "aider", Label: "aider", Kind: LaunchTargetAgent,
		Source: "builtin", Command: []string{"aider"},
	},
}

func ResolveLaunchTargets(
	agents []config.Agent,
	tmuxCommand []string,
	lookPath lookPathFunc,
) []LaunchTarget {
	if lookPath == nil {
		lookPath = exec.LookPath
	}

	targets := make([]LaunchTarget, 0, len(agents)+len(builtinAgents)+2)
	seen := make(map[string]struct{}, len(agents)+len(builtinAgents))

	for _, agent := range agents {
		if isSystemLaunchTargetKey(agent.Key) {
			continue
		}
		target := LaunchTarget{
			Key:     agent.Key,
			Label:   agent.Label,
			Kind:    LaunchTargetAgent,
			Source:  "config",
			Command: slices.Clone(agent.Command),
		}
		if agent.EnabledOrDefault() {
			target.Available = true
		} else {
			target.DisabledReason = "disabled by config"
		}
		targets = append(targets, target)
		seen[target.Key] = struct{}{}
	}

	for _, builtin := range builtinAgents {
		if _, ok := seen[builtin.Key]; ok {
			continue
		}
		target := cloneTarget(builtin)
		if _, err := lookPath(target.Command[0]); err != nil {
			target.Available = false
			target.DisabledReason = fmt.Sprintf(
				"%s not found on PATH", target.Command[0],
			)
		} else {
			target.Available = true
		}
		targets = append(targets, target)
		seen[target.Key] = struct{}{}
	}

	targets = append(targets, shellTarget(tmuxCommand, lookPath))
	targets = append(targets, LaunchTarget{
		Key:       "plain_shell",
		Label:     "Shell",
		Kind:      LaunchTargetPlainShell,
		Source:    "system",
		Available: true,
	})
	return targets
}

func shellTarget(
	tmuxCommand []string,
	lookPath lookPathFunc,
) LaunchTarget {
	command := slices.Clone(tmuxCommand)
	if len(command) == 0 {
		command = []string{"tmux"}
	}
	target := LaunchTarget{
		Key: "shell", Label: "Shell", Kind: LaunchTargetShell,
		Source: "system", Command: command,
	}
	if command[0] == "" {
		target.DisabledReason = "tmux command is empty"
		return target
	}
	if _, err := lookPath(command[0]); err != nil {
		target.DisabledReason = fmt.Sprintf(
			"%s not found on PATH", command[0],
		)
		return target
	}
	target.Available = true
	return target
}

func isSystemLaunchTargetKey(key string) bool {
	return key == string(LaunchTargetShell) ||
		key == string(LaunchTargetPlainShell) ||
		key == "tmux"
}

func cloneTarget(target LaunchTarget) LaunchTarget {
	target.Command = slices.Clone(target.Command)
	return target
}

package localruntime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/procutil"
)

// agentResumeCommand preserves configured options and uses the hook's agent
// identity, since configured target names need not identify an agent family.
func agentResumeCommand(command []string, agent, sessionID string) ([]string, error) {
	if len(command) == 0 || strings.TrimSpace(sessionID) == "" || strings.HasPrefix(sessionID, "-") {
		return nil, fmt.Errorf("agent resume requires a command and session ID")
	}
	if err := validateAgentResumeOptions(command[1:], agent); err != nil {
		return nil, err
	}
	result := slices.Clone(command)
	switch agent {
	case "codex":
		result = append(result, "resume", sessionID)
	case "claude":
		result = append(result, "--resume", sessionID)
	case "pi":
		result = append(result, "--session", sessionID)
	default:
		return nil, fmt.Errorf("session resume is not supported for agent %q", agent)
	}
	return result, nil
}

// A successful PTY spawn does not mean tmux attached. Probe before spawning so
// a missing server cannot erase the recovery report through an asynchronous exit.
func (m *Manager) requireTmuxSession(ctx context.Context, session string) error {
	command := slices.Clone(m.tmuxCommand)
	if len(command) == 0 {
		command = config.DefaultTmuxCommand()
	}
	command, err := resolveTmuxCommand(command)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSessionUnavailable, err)
	}
	args := append(command[1:], "has-session", "-t", session)
	cmd := procutil.CommandContext(ctx, command[0], args...)
	cmd.Env = TmuxClientEnvironment(os.Environ(), m.currentStripEnvVars())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := procutil.Run(ctx, cmd, "tmux subprocess capacity"); err != nil {
		if isTmuxSessionAbsent(stderr.Bytes(), err) {
			return fmt.Errorf("%w: %s", ErrSessionNotFound, session)
		}
		return fmt.Errorf("%w: check tmux session: %v: %s", ErrSessionUnavailable, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Recovery accepts options with known arity only. Positional input, existing
// session selectors and end-of-options delimiters could turn resume into a
// fresh prompt. Retain those sessions for manual recovery instead of guessing.
func validateAgentResumeOptions(args []string, agent string) error {
	var valueOptions, switchOptions string
	switch agent {
	case "codex":
		valueOptions = "-c --config --enable --disable -m --model -p --profile -s --sandbox -a --ask-for-approval -C --cd --add-dir --local-provider --remote --remote-auth-token-env"
		switchOptions = "--full-auto --oss --strict-config --approve-for-me --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust --search --no-alt-screen"
	case "claude":
		valueOptions = "--model --effort --agent --agents --permission-mode --settings --setting-sources --mcp-config --add-dir --allowedTools --allowed-tools --disallowedTools --disallowed-tools --tools --system-prompt --append-system-prompt --plugin-dir"
		switchOptions = "--dangerously-skip-permissions --allow-dangerously-skip-permissions --verbose --strict-mcp-config --bare --chrome --no-chrome"
	case "pi":
		valueOptions = "--provider --model --api-key --system-prompt --append-system-prompt --models --tools -t --exclude-tools -xt --thinking --extension -e --skill --prompt-template --theme --use-theme --tui-mode --session-dir"
		switchOptions = "--no-tools -nt --no-builtin-tools -nbt --no-extensions -ne --no-skills -ns --no-prompt-templates -np --no-themes --no-context-files -nc --verbose --approve -a --no-approve -na --offline"
	default:
		return fmt.Errorf("session resume is not supported for agent %q", agent)
	}
	values, switches := strings.Fields(valueOptions), strings.Fields(switchOptions)
	for i := 0; i < len(args); i++ {
		option, _, inline := strings.Cut(args[i], "=")
		if slices.Contains(values, option) {
			if inline {
				continue
			}
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "-") {
				return fmt.Errorf("cannot resume %s: option %q requires a value", agent, option)
			}
			i++
			continue
		}
		if slices.Contains(switches, option) && !inline {
			continue
		}
		return fmt.Errorf("cannot automatically resume %s: unsupported configured argument %q; use an options-only agent command or resume manually", agent, option)
	}
	return nil
}

package localruntime

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"go.kenn.io/forge/internal/codexhooks"
)

const codexACPInitialModeTimeout = 5 * time.Second

var codexEffectiveAccess = codexhooks.EffectiveAccess

// codexAccessProbe reads the access Codex resolves for a chat's worktree. It
// takes about as long as the agent's own session start, so it runs alongside.
type codexAccessProbe struct {
	cancel            context.CancelFunc
	done              chan struct{}
	sandbox, approval string
	err               error
}

// startCodexAccessProbe starts reading Codex's access when agentName is
// codex-acp, launched in a form the probe supports, and env does not choose
// its initial mode. It returns nil otherwise.
func startCodexAccessProbe(ctx context.Context, agentName string, command, env []string, cwd string) *codexAccessProbe {
	if agentName != "@agentclientprotocol/codex-acp" || !codexACPProbeSupported(command) {
		return nil
	}
	var config map[string]any
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if key == "INITIAL_AGENT_MODE" && value != "" {
			return nil
		}
		if key == "CODEX_CONFIG" {
			_ = json.Unmarshal([]byte(value), &config)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, codexACPInitialModeTimeout)
	probe := &codexAccessProbe{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(probe.done)
		command := append(slices.Clone(command), "cli")
		executable, err := resolveExecutable(command[0])
		if err != nil {
			probe.err = err
			return
		}
		command[0] = executable
		probe.sandbox, probe.approval, probe.err = codexEffectiveAccess(ctx, command, env, cwd, config)
	}()
	return probe
}

// stop ends the probe, if any, and waits for it.
func (p *codexAccessProbe) stop() {
	if p != nil {
		p.cancel()
		<-p.done
	}
}

// deriveCodexMode maps Codex's resolved access to a codex-acp mode when the
// chat would otherwise start in the adapter's default mode. It uses probe when
// one already started, and otherwise starts its own.
func (a *ACP) deriveCodexMode(ctx context.Context, command, env []string, cwd, savedMode string, probe *codexAccessProbe) string {
	a.mu.Lock()
	derive := slices.ContainsFunc(a.state.ConfigOptions, func(option ACPConfigOption) bool {
		return option.ID == "mode" && option.CurrentValue == "agent" && !configOffers(option, savedMode)
	})
	a.mu.Unlock()
	if !derive {
		return ""
	}
	if probe == nil {
		probe = startCodexAccessProbe(ctx, a.agentName, command, env, cwd)
		if probe == nil {
			return ""
		}
		defer probe.stop()
	}
	<-probe.done
	sandbox, approval, err := probe.sandbox, probe.approval, probe.err
	var notice string
	switch {
	case err != nil:
		notice = fmt.Sprintf("Forge couldn't use Codex's access settings (%v), so this chat starts in its default mode.", err)
	case sandbox == "dangerFullAccess" && approval == "never":
		return "agent-full-access"
	case sandbox == "readOnly" && approval == "on-request":
		return "read-only"
	case sandbox == "workspaceWrite":
		return ""
	default:
		if approval == "" {
			approval = "custom"
		}
		notice = fmt.Sprintf("Codex resolves %s with %s approvals here and codex-acp has no matching mode, so this chat starts in its default mode. Pick a mode and Forge keeps it for this agent on this machine.", sandbox, approval)
	}
	a.mu.Lock()
	a.state.Notices = append(a.state.Notices, notice)
	a.changedLocked()
	a.mu.Unlock()
	return ""
}

func codexACPProbeSupported(command []string) bool {
	if len(command) == 0 {
		return false
	}
	name := filepath.Base(command[0])
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(name), ".exe"), ".cmd")
	}
	switch name {
	case "codex-acp", "npx", "bunx":
		return true
	}
	standalone, _ := filepath.Match("codex-acp-*-*", name)
	return standalone
}

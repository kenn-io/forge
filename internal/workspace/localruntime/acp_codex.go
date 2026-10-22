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

func (a *ACP) deriveCodexMode(ctx context.Context, command, env []string, cwd, savedMode string) string {
	if a.agentName != "@agentclientprotocol/codex-acp" || !codexACPProbeSupported(command) {
		return ""
	}
	a.mu.Lock()
	derive := slices.ContainsFunc(a.state.ConfigOptions, func(option ACPConfigOption) bool {
		return option.ID == "mode" && option.CurrentValue == "agent" && !configOffers(option, savedMode)
	})
	a.mu.Unlock()
	if !derive {
		return ""
	}
	var config map[string]any
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if key == "INITIAL_AGENT_MODE" && value != "" {
			return ""
		}
		if key == "CODEX_CONFIG" {
			_ = json.Unmarshal([]byte(value), &config)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, codexACPInitialModeTimeout)
	defer cancel()
	probe := append(slices.Clone(command), "cli")
	executable, err := resolveExecutable(command[0])
	var sandbox, approval string
	if err == nil {
		probe[0] = executable
		sandbox, approval, err = codexEffectiveAccess(ctx, probe, env, cwd, config)
	}
	var notice string
	switch {
	case err != nil:
		notice = fmt.Sprintf("Forge couldn't use Codex's access settings (%v), so this chat starts in codex-acp's default Agent mode.", err)
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
		notice = fmt.Sprintf("Codex resolves %s with %s approvals here and codex-acp has no matching mode, so this chat starts in its default Agent mode. Pick a mode to keep it for this workspace.", sandbox, approval)
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

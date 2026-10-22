package localruntime

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	"go.kenn.io/forge/internal/codexhooks"
)

const codexACPInitialModeTimeout = 5 * time.Second

var codexEffectiveAccess = codexhooks.EffectiveAccess

func (a *ACP) deriveCodexMode(ctx context.Context, command, env []string, cwd, savedMode string) string {
	if a.agentName != "@agentclientprotocol/codex-acp" {
		return ""
	}
	a.mu.Lock()
	derive := slices.ContainsFunc(a.state.ConfigOptions, func(option ACPConfigOption) bool {
		return option.ID == "mode" && option.Type == "select" && option.CurrentValue == "agent" && !configOffers(option, savedMode)
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
		if key == "CODEX_CONFIG" {
			_ = json.Unmarshal([]byte(value), &config)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, codexACPInitialModeTimeout)
	defer cancel()
	sandbox, approval, err := codexEffectiveAccess(ctx, append(slices.Clone(command), "cli"), env, cwd, config)
	var notice string
	switch {
	case err != nil:
		notice = fmt.Sprintf("Forge couldn't read Codex's access settings, so this chat uses codex-acp's default mode, which Codex may reject: %v", err)
	case sandbox == "dangerFullAccess" && approval == "never":
		return "agent-full-access"
	case sandbox == "readOnly" && approval == "on-request":
		return "read-only"
	case sandbox == "workspaceWrite" && approval == "on-request":
		return ""
	default:
		if approval == "" {
			approval = "custom"
		}
		notice = fmt.Sprintf("Codex resolves %s with %s approvals here. codex-acp has no matching mode, so this chat starts in its default mode, which Codex may reject.", sandbox, approval)
	}
	a.mu.Lock()
	a.state.Notices = append(a.state.Notices, notice)
	a.changedLocked()
	a.mu.Unlock()
	return ""
}

package fleet

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/procutil"
)

// probeCommandTimeout bounds each version-probe subprocess. Probe runs while
// building snapshot responses, so a hung wrapper or blocked executable must
// not hang the snapshot endpoints.
const probeCommandTimeout = 5 * time.Second

// Probe inspects the local host for tool availability and derives the
// capability set advertised in fleet snapshots. It is pure detection:
// it shells out to git/gh/tmux with version flags and never mutates
// state. Command capabilities are derived from dependency presence. Each
// probe command runs under ctx with a short timeout and the shared
// subprocess limiter.
//
// tmuxCmd is the configured tmux command and argv prefix (e.g. ["tmux"]
// or a wrapper such as ["systemd-run", "--user", "--scope", "tmux"]). An
// empty slice falls back to the bare "tmux" binary, so a server with a
// custom tmux.command is not misreported as lacking tmux and does not have
// its session operations disabled in the snapshot.
func Probe(ctx context.Context, tmuxCmd []string) Capabilities {
	tmuxCmd = tmuxCommandOrDefault(tmuxCmd)
	deps := DependencyCapabilities{
		Git:  commandSucceeds(ctx, "git", "--version"),
		Gh:   commandSucceeds(ctx, "gh", "--version"),
		Tmux: tmuxCommandSucceeds(ctx, tmuxCmd),
	}

	var tmuxVersion string
	if deps.Tmux {
		tmuxVersion = tmuxVersionString(ctx, tmuxCmd)
	}

	return Capabilities{
		Commands:     commandsForDeps(deps),
		Dependencies: deps,
		Features: FeatureCapabilities{
			ResourceMetrics: false,
			SetupHook:       false,
			TeardownHook:    false,
			MoshAttach:      false,
			TmuxVersion:     tmuxVersion,
		},
	}
}

// commandsForDeps derives the command-capability set from detected tool
// dependencies. It is pure (no I/O), so the derivation rules can be
// table-tested across every dependency combination without depending on
// which tools happen to be installed on the test machine.
func commandsForDeps(deps DependencyCapabilities) CommandCapabilities {
	git, gh, tmux := deps.Git, deps.Gh, deps.Tmux
	return CommandCapabilities{
		WorktreeCreate:   git,
		WorktreeImportPR: git && gh,
		WorktreeDelete:   git,
		SessionEnsure:    tmux,
		SessionKill:      tmux,
		RepositoryClone:  git,
		ProjectAdd:       git,
		ProjectRemove:    true,
	}
}

// commandSucceeds reports whether executable is on PATH and exits 0 for
// the given version-probe arguments within the probe timeout.
func commandSucceeds(ctx context.Context, executable string, args ...string) bool {
	if _, err := exec.LookPath(executable); err != nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeCommandTimeout)
	defer cancel()
	cmd := procutil.CommandContext(probeCtx, executable, args...)
	return procutil.Run(probeCtx, cmd, "fleet capability probe") == nil
}

// tmuxCommandOrDefault returns tmuxCmd, or ["tmux"] when it is empty or its
// executable is blank, mirroring (*config.Config).TmuxCommand's default.
func tmuxCommandOrDefault(tmuxCmd []string) []string {
	if len(tmuxCmd) == 0 || tmuxCmd[0] == "" {
		return []string{"tmux"}
	}
	return tmuxCmd
}

// tmuxCommandSucceeds reports whether the configured tmux command runs and
// exits 0 for `-V`. tmuxCmd must be non-empty (see tmuxCommandOrDefault).
func tmuxCommandSucceeds(ctx context.Context, tmuxCmd []string) bool {
	args := append(slices.Clone(tmuxCmd[1:]), "-V")
	return commandSucceeds(ctx, tmuxCmd[0], args...)
}

// tmuxVersionString returns the version token from the configured tmux
// command's `-V` output (e.g. "3.4"), or "" if it cannot be determined.
// tmuxCmd must be non-empty (see tmuxCommandOrDefault).
func tmuxVersionString(ctx context.Context, tmuxCmd []string) string {
	args := append(slices.Clone(tmuxCmd[1:]), "-V")
	probeCtx, cancel := context.WithTimeout(ctx, probeCommandTimeout)
	defer cancel()
	cmd := procutil.CommandContext(probeCtx, tmuxCmd[0], args...)
	out, err := procutil.Output(probeCtx, cmd, "fleet capability probe")
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out)) // e.g. ["tmux", "3.4"]
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}

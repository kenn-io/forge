package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.kenn.io/middleman/internal/agentactivity"
	"go.kenn.io/middleman/internal/config"
)

// The agent-hook command has two audiences: agents invoke `run` on every
// lifecycle event, and maintainers run install/uninstall once to register that
// receiver in the agent's own hook config.
func runAgentHookCLI(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: middleman agent-hook <run|install|uninstall>")
	}
	switch args[0] {
	case "run":
		return runAgentHookReceiver(args[1:], stdin)
	case "install":
		return runAgentHookInstall(args[1:], stdout)
	case "uninstall":
		return runAgentHookUninstall(args[1:], stdout)
	default:
		return fmt.Errorf("unknown agent-hook subcommand %q", args[0])
	}
}

// runAgentHookReceiver records one hook payload. It runs inside the agent's
// critical path, so it fails open: bad flags, unreadable input, or an
// unwritable state directory must never interrupt the session.
func runAgentHookReceiver(args []string, stdin io.Reader) error {
	fs := flag.NewFlagSet("middleman agent-hook run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateDir := fs.String("state-dir", "", "agent activity state directory")
	source := fs.String("source", "", "hook source marker")
	if err := fs.Parse(args); err != nil {
		return nil
	}
	if *source != agentactivity.HookSource {
		return nil
	}
	_ = agentactivity.NewStore(*stateDir).HandleHook(
		stdin, os.Getenv(agentactivity.RuntimeSessionKeyEnv),
	)
	return nil
}

func runAgentHookInstall(args []string, stdout io.Writer) error {
	flags, err := parseAgentHookFlags("install", args)
	if err != nil {
		return err
	}
	stateDir, err := agentHookStateDir(flags.configPath)
	if err != nil {
		return err
	}
	executable, err := agentHookExecutable(flags.binary)
	if err != nil {
		return err
	}
	for _, integration := range flags.integrations {
		result, err := agentactivity.Install(integration, executable, stateDir)
		if err != nil {
			return err
		}
		message := fmt.Sprintf(
			"Installed middleman %s hooks in %s\n", integration, result.ConfigPath,
		)
		if integration == agentactivity.IntegrationCodex {
			message += "Open /hooks in Codex once to review and trust the new hook commands.\n"
		}
		if _, err := io.WriteString(stdout, message); err != nil {
			return err
		}
	}
	return nil
}

func runAgentHookUninstall(args []string, stdout io.Writer) error {
	flags, err := parseAgentHookFlags("uninstall", args)
	if err != nil {
		return err
	}
	for _, integration := range flags.integrations {
		result, err := agentactivity.Uninstall(integration)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout,
			"Removed middleman %s hooks from %s\n", integration, result.ConfigPath,
		); err != nil {
			return err
		}
	}
	return nil
}

type agentHookFlags struct {
	configPath   string
	binary       string
	integrations []agentactivity.Integration
}

// parseAgentHookFlags parses the flags shared by install and uninstall so both
// subcommands accept the same arguments.
func parseAgentHookFlags(action string, args []string) (agentHookFlags, error) {
	fs := flag.NewFlagSet("middleman agent-hook "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", config.DefaultConfigPath(), "middleman config path")
	agent := fs.String("agent", "", "agent integration (claude or codex; empty applies to both)")
	binary := fs.String("binary", "", "middleman binary path used by installed hooks")
	if err := fs.Parse(args); err != nil {
		return agentHookFlags{}, err
	}
	integrations := []agentactivity.Integration{
		agentactivity.IntegrationClaude,
		agentactivity.IntegrationCodex,
	}
	if strings.TrimSpace(*agent) != "" {
		integration, err := agentactivity.ParseIntegration(*agent)
		if err != nil {
			return agentHookFlags{}, err
		}
		integrations = []agentactivity.Integration{integration}
	}
	return agentHookFlags{
		configPath:   *configPath,
		binary:       strings.TrimSpace(*binary),
		integrations: integrations,
	}, nil
}

// agentHookStateDir resolves where installed hooks write their reports. The
// server derives the same directory from its own data dir, so an install that
// pointed anywhere else would leave workspace rows without agent state.
func agentHookStateDir(configPath string) (string, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", err
	}
	// Hooks run from the agent's working directory, where a relative data dir
	// would resolve differently for every session.
	if !filepath.IsAbs(cfg.DataDir) {
		return "", fmt.Errorf(
			"agent hook install requires an absolute data_dir: %q", cfg.DataDir,
		)
	}
	return agentactivity.StateDir(cfg.DataDir), nil
}

func agentHookExecutable(binary string) (string, error) {
	if binary != "" {
		return binary, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve middleman executable: %w", err)
	}
	return executable, nil
}

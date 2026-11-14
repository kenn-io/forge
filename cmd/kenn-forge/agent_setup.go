package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/kit/agenthook"
	"go.kenn.io/kit/agentmcp"
	"go.kenn.io/kit/agentsetup"
)

func newAgentCommand(load mcpQuickstartLoader) *cobra.Command {
	group := &cobra.Command{Use: "agent", Short: "Connect agents to Forge", Args: cobra.NoArgs}
	var setup *cobra.Command
	configPath := config.DefaultConfigPath()
	options := agentsetup.Options{
		SetupCommand: "kenn-forge agent setup",
		Items: func(agent string) ([]agentsetup.Item, error) {
			target, err := resolveAgentHookTarget(configPath, "")
			var hooks agentsetup.Item
			if err != nil {
				hooks = agentsetup.Item{Name: "hooks", Check: func(context.Context) (agentsetup.Status, error) {
					return agentsetup.Status{State: agentsetup.StateFailed, Reason: err.Error(), NextStep: "Check the Forge config and retry."}, nil
				}}
			} else {
				hooks = agentsetup.Hooks(agent, target.options(agenthook.Agent(agent)))
			}
			info, discoveryErr := load(setup.Context(), configPath, 5*time.Second)
			selected, _ := setup.Flags().GetStringSlice("agent")
			command := setupCommand(configPath)
			if len(selected) > 0 {
				command += " --agent " + strings.Join(selected, ",")
			}
			return []agentsetup.Item{hooks, forgeSetupMCP(agent, info, discoveryErr, command)}, nil
		},
	}
	setup = agentsetup.NewCommand(options)
	setup.Flags().StringVar(&configPath, "config", configPath, "path to config file")
	setup.RunE = func(cmd *cobra.Command, args []string) error {
		options.SetupCommand = setupCommand(configPath)
		configured := agentsetup.NewCommand(options)
		var flagErr error
		cmd.Flags().Visit(func(flag *pflag.Flag) {
			target := configured.Flags().Lookup(flag.Name)
			if target == nil || flagErr != nil {
				return
			}
			if values, ok := flag.Value.(pflag.SliceValue); ok {
				flagErr = target.Value.(pflag.SliceValue).Replace(values.GetSlice())
			} else {
				flagErr = target.Value.Set(flag.Value.String())
			}
		})
		if flagErr != nil {
			return flagErr
		}
		configured.SetOut(cmd.OutOrStdout())
		configured.SetContext(cmd.Context())
		err := configured.RunE(configured, args)
		cmd.SilenceErrors, cmd.SilenceUsage = configured.SilenceErrors, configured.SilenceUsage
		return err
	}
	group.AddCommand(setup)
	return group
}

func setupCommand(configPath string) string {
	args := []string{"kenn-forge", "agent", "setup"}
	if configPath != config.DefaultConfigPath() {
		args = append(args, "--config", configPath)
	}
	return shellquote.Join(args...)
}

func forgeSetupMCP(agent string, info mcpQuickstartInfo, discoveryErr error, command string) agentsetup.Item {
	if discoveryErr == nil && info.Active {
		return withTokenGuidance(agentsetup.MCP(agent, "kenn-forge", forgeMCPServer(info)), info)
	}
	reason, steps := "Forge MCP isn't active", info.NextSteps
	if discoveryErr != nil {
		reason, steps = discoveryErr.Error(), []string{"Check the Forge config and daemon."}
	} else if info.pid == 0 {
		reason = "Forge daemon isn't running"
	}
	nextStep := strings.TrimSuffix(strings.Join(steps, " "), ".") + ", then run `" + command + "`."
	return agentsetup.Item{Name: "mcp kenn-forge", Check: func(context.Context) (agentsetup.Status, error) {
		return agentsetup.Status{State: agentsetup.StateFailed, Reason: reason, NextStep: nextStep}, nil
	}, Commands: func() (agenthook.Commands, error) {
		return agenthook.Commands{}, fmt.Errorf("%s: %s", reason, nextStep)
	}}
}

func forgeMCPServer(info mcpQuickstartInfo) agentmcp.Server {
	server := agentmcp.Server{URL: info.Endpoint}
	if info.Authentication.Required {
		server.BearerTokenEnvVar = mcpTokenEnvironment
	} else {
		server.PreviousBearerTokenEnvVar = mcpTokenEnvironment
	}
	return server
}

// withTokenGuidance tells a user whose shell lacks the token where to find it,
// both in check results and in the manual commands printed by --print.
func withTokenGuidance(item agentsetup.Item, info mcpQuickstartInfo) agentsetup.Item {
	if !info.Authentication.Required || os.Getenv(mcpTokenEnvironment) != "" {
		return item
	}
	check, commands := item.Check, item.Commands
	path := info.Authentication.TokenPath
	item.Check = func(ctx context.Context) (agentsetup.Status, error) {
		status, err := check(ctx)
		if status.State != agentsetup.StateOK {
			status.NextStep += fmt.Sprintf("; export %s from %s", mcpTokenEnvironment, path)
		}
		return status, err
	}
	item.Commands = func() (agenthook.Commands, error) {
		cmds, err := commands()
		cmds.POSIX = fmt.Sprintf("export %s=\"$(cat %s)\"\n%s", mcpTokenEnvironment, shellquote.Join(path), cmds.POSIX)
		cmds.PowerShell = fmt.Sprintf("$env:%s = (Get-Content -Raw -LiteralPath '%s').Trim()\n%s", mcpTokenEnvironment, strings.ReplaceAll(path, "'", "''"), cmds.PowerShell)
		return cmds, err
	}
	// A fresh Item drops the library's retry-only next step so the token hint survives.
	return agentsetup.Item{Name: item.Name, Install: item.Install, Check: item.Check, Commands: item.Commands}
}

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/kit/agenthook"
	"go.kenn.io/kit/agentmcp"
	"go.kenn.io/kit/agentsetup"
)

func newAgentCommand(load mcpQuickstartLoader) *cobra.Command {
	group := &cobra.Command{Use: "agent", Short: "Connect agents to Forge", Args: cobra.NoArgs}
	configPath := config.DefaultConfigPath()
	var setup *cobra.Command
	discover := sync.OnceValues(func() (mcpQuickstartInfo, error) {
		return load(setup.Context(), configPath, 5*time.Second)
	})
	setup = agentsetup.NewCommand(agentsetup.Options{
		SetupCommand: "kenn-forge agent setup",
		Items: func(agent string) ([]agentsetup.Item, error) {
			target, err := resolveAgentHookTarget(configPath, "")
			if err != nil {
				return nil, err
			}
			info, discoveryErr := discover()
			return []agentsetup.Item{
				agentsetup.Hooks(agent, target.options(agenthook.Agent(agent))),
				forgeSetupMCP(agent, info, discoveryErr),
			}, nil
		},
	})
	group.AddCommand(setup)
	return group
}

func forgeSetupMCP(agent string, info mcpQuickstartInfo, discoveryErr error) agentsetup.Item {
	if discoveryErr != nil || !info.Active {
		reason := "Forge MCP isn't active"
		if discoveryErr != nil {
			reason = discoveryErr.Error()
		} else if info.pid == 0 {
			reason = "Forge daemon isn't running"
		}
		nextStep := strings.Join(info.NextSteps, " ")
		return agentsetup.Item{Name: "mcp kenn-forge", Check: func(context.Context) (agentsetup.Status, error) {
			return agentsetup.Status{State: agentsetup.StateFailed, Reason: reason, NextStep: nextStep}, nil
		}, Commands: func() (agenthook.Commands, error) {
			return agenthook.Commands{}, fmt.Errorf("%s: %s", reason, nextStep)
		}}
	}
	return withTokenGuidance(agentsetup.MCP(agent, "kenn-forge", forgeMCPServer(info)), info)
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
		if status.State == agentsetup.StateFailed || status.State == agentsetup.StateUnknown {
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
	return item
}

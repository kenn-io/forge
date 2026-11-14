package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/kit/agenthook"
	"go.kenn.io/kit/agentmcp"
	"go.kenn.io/kit/agentsetup"
)

func newAgentCommand(load mcpQuickstartLoader) *cobra.Command {
	group := &cobra.Command{Use: "agent", Short: "Connect agents to Forge", Args: cobra.NoArgs}
	var setup *cobra.Command
	discover := sync.OnceValues(func() (mcpQuickstartInfo, error) {
		return load(setup.Context(), config.DefaultConfigPath(), 5*time.Second)
	})
	setup = agentsetup.NewCommand(agentsetup.Options{
		SetupCommand: "kenn-forge agent setup",
		Items: func(agent string) ([]agentsetup.Item, error) {
			target, err := resolveAgentHookTarget(config.DefaultConfigPath(), "")
			var hooks agentsetup.Item
			if err != nil {
				hooks = agentsetup.Item{Name: "hooks", Check: func(context.Context) (agentsetup.Status, error) {
					return agentsetup.Status{State: agentsetup.StateFailed, Reason: err.Error(), NextStep: "Set an absolute data_dir in the Forge config."}, nil
				}}
			} else {
				hooks = agentsetup.Hooks(agent, target.options(agenthook.Agent(agent)))
			}
			info, discoveryErr := discover()
			selected, _ := setup.Flags().GetStringSlice("agent")
			command := "kenn-forge agent setup"
			if len(selected) > 0 {
				command += " --agent " + strings.Join(selected, ",")
			}
			return []agentsetup.Item{hooks, forgeSetupMCP(agent, info, discoveryErr, command)}, nil
		},
	})
	group.AddCommand(setup)
	return group
}

func forgeSetupMCP(agent string, info mcpQuickstartInfo, discoveryErr error, command string) agentsetup.Item {
	if discoveryErr != nil || !info.Active {
		reason := "Forge MCP isn't active"
		if discoveryErr != nil {
			reason = discoveryErr.Error()
		} else if info.pid == 0 {
			reason = "Forge daemon isn't running"
		}
		steps := info.NextSteps
		if len(steps) == 0 {
			steps = []string{"Run `kenn-forge mcp quickstart`."}
		}
		return agentsetup.Item{Name: "mcp kenn-forge", Check: func(context.Context) (agentsetup.Status, error) {
			return agentsetup.Status{State: agentsetup.StateFailed, Reason: reason, NextStep: strings.Join(steps, " ") + " then run `" + command + "`"}, nil
		}}
	}
	if info.Authentication.Required && agent != "claude" && agent != "codex" {
		return agentsetup.Item{Name: "mcp kenn-forge", Check: func(context.Context) (agentsetup.Status, error) {
			return agentsetup.Status{State: agentsetup.StateUnknown, Reason: "Token reference is unavailable for " + agent, NextStep: "Run `kenn-forge mcp quickstart` for manual setup."}, nil
		}}
	}
	server := agentmcp.Server{URL: info.Endpoint}
	if info.Authentication.Required {
		server.BearerTokenEnvVar = mcpTokenEnvironment
	}
	item := agentsetup.MCP(agent, "kenn-forge", server)
	check := item.Check
	item.Check = func(ctx context.Context) (agentsetup.Status, error) {
		status, err := check(ctx)
		if (status.State == agentsetup.StateFailed || status.State == agentsetup.StateUnknown) && info.Authentication.Required && os.Getenv(mcpTokenEnvironment) == "" {
			if status.NextStep == "" {
				status.NextStep = command
			}
			status.NextStep += fmt.Sprintf("; export %s from %s", mcpTokenEnvironment, info.Authentication.TokenPath)
		}
		return status, err
	}
	return item
}

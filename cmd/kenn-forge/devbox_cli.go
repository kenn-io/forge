package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/cli/serve"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/devbox"
	"go.kenn.io/forge/internal/procutil"
)

func newDevboxCommand(run serve.Runner) *cobra.Command {
	root := &cobra.Command{Use: "devbox", Short: "Execution workers and account-scoped GitHub access"}
	var workerConfig string
	worker := &cobra.Command{
		Use: "worker", Short: "Run an execution-only workspace worker", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(workerConfig)
			if err != nil {
				return err
			}
			if !cfg.ExecutionWorker.Enabled {
				return errors.New("worker command requires execution_worker.enabled=true")
			}
			return run(serve.Options{ConfigPath: workerConfig, DisableSync: true})
		},
	}
	worker.Flags().StringVar(&workerConfig, "config", "", "worker TOML configuration")
	_ = worker.MarkFlagRequired("config")
	var brokerConfig string
	broker := &cobra.Command{
		Use: "broker", Short: "Serve UID-bound GitHub App credentials on a Unix socket", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var cfg devbox.BrokerConfig
			metadata, err := toml.DecodeFile(brokerConfig, &cfg)
			if err != nil {
				return err
			}
			if len(metadata.Undecoded()) != 0 {
				return fmt.Errorf("unknown broker settings: %v", metadata.Undecoded())
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			return devbox.ServeBroker(ctx, cfg)
		},
	}
	broker.Flags().StringVar(&brokerConfig, "config", "", "broker TOML configuration")
	_ = broker.MarkFlagRequired("config")
	var credentialSocket string
	credential := &cobra.Command{
		Use: "credential get|store|erase", Short: "Git credential helper for an execution account", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := devbox.NewBrokerClient(credentialSocket)
			defer client.Close()
			return devbox.RunCredential(cmd.Context(), client, args[0], cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	credential.Flags().StringVar(&credentialSocket, "socket", "", "account credential broker socket")
	_ = credential.MarkFlagRequired("socket")
	var githubSocket, repository string
	github := &cobra.Command{
		Use: "github --repository owner/name -- pr <command>", Short: "Run a supported GitHub PR command with App credentials", Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDevboxGitHub(cmd.Context(), cmd, githubSocket, repository, args)
		},
	}
	github.Flags().StringVar(&githubSocket, "socket", "", "account credential broker socket")
	github.Flags().StringVar(&repository, "repository", "", "admitted GitHub owner/name")
	_ = github.MarkFlagRequired("socket")
	_ = github.MarkFlagRequired("repository")
	var registryConfig string
	registry := &cobra.Command{Use: "registry", Short: "Serve devbox discovery through a trusted Unix-socket proxy", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var cfg devbox.RegistryConfig
			metadata, err := toml.DecodeFile(registryConfig, &cfg)
			if err != nil {
				return err
			}
			if len(metadata.Undecoded()) != 0 {
				return fmt.Errorf("unknown registry settings: %v", metadata.Undecoded())
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			return devbox.ServeRegistry(ctx, cfg)
		}}
	registry.Flags().StringVar(&registryConfig, "config", "", "registry TOML configuration")
	_ = registry.MarkFlagRequired("config")
	root.AddCommand(worker, broker, registry, credential, github)
	return root
}

func runDevboxGitHub(ctx context.Context, command *cobra.Command, socket, repository string, args []string) error {
	if args[0] != "pr" || !strings.Contains(" create edit comment view list checks ", " "+args[1]+" ") {
		return errors.New("supported GitHub commands: pr create, edit, comment, view, list, checks")
	}
	if _, _, err := devbox.ParseRepository(repository); err != nil {
		return err
	}
	client := devbox.NewBrokerClient(socket)
	defer client.Close()
	credential, err := client.Credential(ctx, repository, "pr")
	if err != nil {
		return err
	}
	privateConfig, err := os.MkdirTemp("", "forge-devbox-gh-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(privateConfig)
	child := procutil.CommandContext(ctx, "gh", args...)
	child.Stdin, child.Stdout, child.Stderr = command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr()
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "GH_") && !strings.HasPrefix(name, "GITHUB_") {
			child.Env = append(child.Env, entry)
		}
	}
	child.Env = append(child.Env, "GH_TOKEN="+credential.Token, "GH_CONFIG_DIR="+privateConfig,
		"GH_REPO=github.com/"+repository, "GH_HOST=github.com", "GH_PROMPT_DISABLED=1")
	return child.Run()
}

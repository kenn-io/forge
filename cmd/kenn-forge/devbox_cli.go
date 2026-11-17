package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
	registry := &cobra.Command{
		Use: "registry", Short: "Serve devbox discovery through a trusted Unix-socket proxy", Args: cobra.NoArgs,
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
		},
	}
	registry.Flags().StringVar(&registryConfig, "config", "", "registry TOML configuration")
	_ = registry.MarkFlagRequired("config")
	root.AddCommand(worker, broker, registry, credential, github)
	return root
}

func runDevboxGitHub(ctx context.Context, command *cobra.Command, socket, repository string, args []string) error {
	if args[0] != "pr" || !strings.Contains(" create edit comment view list checks close reopen ready merge ", " "+args[1]+" ") {
		return errors.New("supported GitHub commands: pr create, edit, comment, view, list, checks, close, reopen, ready, merge")
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
	if strings.Contains(" close reopen ready merge ", " "+args[1]+" ") {
		selector, repo, err := devboxGitHubActionFlags(args)
		if err != nil {
			return err
		}
		viewArgs := []string{"pr", "view"}
		if selector != "" {
			viewArgs = append(viewArgs, selector)
		}
		if repo != "" {
			viewArgs = append(viewArgs, "--repo", repo)
		}
		viewArgs = append(viewArgs, "--json", "author")
		view := procutil.CommandContext(ctx, "gh", viewArgs...)
		view.Env = devboxGitHubEnv(credential.Token, privateConfig, repository)
		view.Stderr = command.ErrOrStderr()
		output, err := view.Output()
		if err != nil {
			return fmt.Errorf("look up pull request author: %w", err)
		}
		var result struct {
			Author struct {
				Login string `json:"login"`
				IsBot bool   `json:"is_bot"`
			} `json:"author"`
		}
		if err := json.Unmarshal(output, &result); err != nil {
			return fmt.Errorf("read pull request author: %w", err)
		}
		if credential.AppSlug == "" || !result.Author.IsBot || result.Author.Login != "app/"+credential.AppSlug {
			return fmt.Errorf("pull request author %q is not GitHub App %q", result.Author.Login, credential.AppSlug)
		}
	}
	child := procutil.CommandContext(ctx, "gh", args...)
	child.Stdin, child.Stdout, child.Stderr = command.InOrStdin(), command.OutOrStdout(), command.ErrOrStderr()
	child.Env = devboxGitHubEnv(credential.Token, privateConfig, repository)
	return child.Run()
}

func devboxGitHubEnv(token, config, repository string) []string {
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "GH_") && !strings.HasPrefix(name, "GITHUB_") {
			env = append(env, entry)
		}
	}
	return append(env, "GH_TOKEN="+token, "GH_CONFIG_DIR="+config,
		"GH_REPO=github.com/"+repository, "GH_HOST=github.com", "GH_PROMPT_DISABLED=1")
}

func devboxGitHubActionFlags(args []string) (string, string, error) {
	flags := pflag.NewFlagSet("gh pr "+args[1], pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.BoolP("delete-branch", "d", false, "")
	flags.StringP("repo", "R", "", "")
	switch args[1] {
	case "close", "reopen":
		flags.StringP("comment", "c", "", "")
	case "ready":
		flags.Bool("undo", false, "")
	case "merge":
		for _, flag := range []struct{ name, short string }{
			{"author-email", "A"}, {"body", "b"}, {"body-file", "F"}, {"subject", "t"},
		} {
			flags.StringP(flag.name, flag.short, "", "")
		}
		flags.String("match-head-commit", "", "")
		for _, name := range []string{"admin", "auto", "disable-auto"} {
			flags.Bool(name, false, "")
		}
		for _, flag := range []struct{ name, short string }{{"merge", "m"}, {"rebase", "r"}, {"squash", "s"}} {
			flags.BoolP(flag.name, flag.short, false, "")
		}
	}
	if err := flags.Parse(args[2:]); err != nil {
		return "", "", err
	}
	repo, _ := flags.GetString("repo")
	if flags.NArg() > 1 {
		return "", "", errors.New("expected at most one pull request selector")
	}
	return flags.Arg(0), repo, nil
}

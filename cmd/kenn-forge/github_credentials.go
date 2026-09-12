package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/serviceauth"
)

func newGitHubCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "github", Short: "Run GitHub commands with the service account"}

	var credentialConfig string
	credential := &cobra.Command{
		Use:   "credential [get|store|erase]",
		Short: "Provide service credentials to Git",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGitHubCredential(cmd.Context(), credentialConfig, args[0], stdin, stdout)
		},
	}
	credential.Flags().StringVar(&credentialConfig, "config", config.DefaultConfigPath(), "path to config file")

	var execConfig string
	execCommand := &cobra.Command{
		Use:   "exec -- COMMAND [ARG...]",
		Short: "Run a command with the service GitHub token",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGitHubExec(cmd.Context(), execConfig, args, stdin, stdout, stderr)
		},
	}
	execCommand.Flags().StringVar(&execConfig, "config", config.DefaultConfigPath(), "path to config file")
	cmd.AddCommand(credential, execCommand)
	return cmd
}

func newServiceAuthManager(configPath string) (*serviceauth.Manager, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	if !cfg.Service.Enabled {
		return nil, errors.New("GitHub service account mode is not enabled")
	}
	return serviceauth.New(serviceauth.Options{
		ClientID:         cfg.Service.GitHubClientID,
		ClientSecretFile: cfg.Service.GitHubClientSecretFile,
		BaseURL:          cfg.Service.BaseURL,
		DataDir:          cfg.DataDir,
		OwnerID:          cfg.Service.GitHubUserID,
	})
}

func runGitHubCredential(
	ctx context.Context, configPath, action string, stdin io.Reader, stdout io.Writer,
) error {
	fields, err := readGitCredential(stdin)
	if err != nil {
		return err
	}
	switch action {
	case "get", "store", "erase":
	default:
		return fmt.Errorf("unsupported Git credential action %q", action)
	}
	if fields["protocol"] != "https" || !strings.EqualFold(fields["host"], "github.com") {
		return nil
	}
	manager, err := newServiceAuthManager(configPath)
	if err != nil {
		if action == "get" {
			return writeGitCredentialQuit(stdout)
		}
		return err
	}
	switch action {
	case "store":
		return nil
	case "erase":
		manager.Invalidate(fields["password"])
		return nil
	case "get":
	}
	token, err := manager.Token(ctx)
	if err != nil {
		return writeGitCredentialQuit(stdout)
	}
	_, err = fmt.Fprintf(stdout, "username=x-access-token\npassword=%s\n\n", token)
	return err
}

func writeGitCredentialQuit(stdout io.Writer) error {
	_, err := io.WriteString(stdout, "quit=true\n\n")
	return err
}

func readGitCredential(input io.Reader) (map[string]string, error) {
	fields := make(map[string]string)
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			fields[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Git credential request: %w", err)
	}
	return fields, nil
}

func runGitHubExec(
	ctx context.Context,
	configPath string,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	manager, err := newServiceAuthManager(configPath)
	if err != nil {
		return err
	}
	token, err := manager.Token(ctx)
	if err != nil {
		return err
	}
	child := procutil.CommandContext(ctx, args[0], args[1:]...)
	child.Stdin = stdin
	child.Stdout = stdout
	child.Stderr = stderr
	child.Env = githubCommandEnvironment(os.Environ(), token)
	if err := child.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return &apiVerbError{code: exitErr.ExitCode(), err: err}
		}
		return fmt.Errorf("run GitHub command: %w", err)
	}
	return nil
}

func githubCommandEnvironment(base []string, token string) []string {
	blocked := []string{
		"GH_TOKEN",
		"GITHUB_TOKEN",
		"KENN_FORGE_GITHUB_TOKEN",
		"GH_ENTERPRISE_TOKEN",
		"GITHUB_ENTERPRISE_TOKEN",
		"GH_HOST",
	}
	out := make([]string, 0, len(base)+1)
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || !containsEnvironmentName(blocked, name) {
			out = append(out, entry)
		}
	}
	return append(out, "GH_TOKEN="+token, "GH_HOST=github.com")
}

func containsEnvironmentName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name || runtime.GOOS == "windows" && strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}

package main

import (
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/cli/ctl"
	"go.kenn.io/forge/internal/cli/serve"
	"go.kenn.io/forge/internal/config"
)

type cliOptions struct {
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	RunServer    serve.Runner
	DaemonRunner daemonCommandRunner
}

func newRootCommand(opts cliOptions) *cobra.Command {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.RunServer == nil {
		opts.RunServer = runServer
	}
	if opts.DaemonRunner == nil {
		opts.DaemonRunner = newDaemonLifecycle(defaultDaemonLifecycleDeps())
	}
	root := &cobra.Command{
		Use:               "kenn-forge",
		Short:             "Local-first maintainer console",
		Args:              cobra.NoArgs,
		SilenceErrors:     true,
		SilenceUsage:      true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetIn(opts.Stdin)
	root.SetOut(opts.Stdout)
	root.SetErr(opts.Stderr)
	ctl.RegisterCommands(root, ctl.Options{
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
		APICommandFactory: func(request ctl.APIRequest) *cobra.Command {
			return newAPIVerbCommand(opts.Stdin, opts.Stdout, request)
		},
	})

	root.AddCommand(
		newVersionCommand(opts.Stdout),
		newConfigCommand(opts.Stdout),
		newDocsCommand(opts.Stdout),
		newArchiveCommand(opts.Stdout, time.Now),
		newAgentHookCommand(opts.Stdin, opts.Stdout),
		newDaemonCommand(opts.DaemonRunner),
		newPtyOwnerCommand(),
		serve.NewCommand(opts.RunServer),
	)
	return root
}

func runCLI(args []string, stdout io.Writer) error {
	cmd := newRootCommand(cliOptions{Stdout: stdout})
	cmd.SetArgs(normalizeSingleDashLongFlags(args))
	return cmd.Execute()
}

// normalizeSingleDashLongFlags preserves the long-flag spelling used by the
// shipped command docs and scripts before the CLI moved from flag to pflag.
// Native one-letter shorthands and everything after -- retain their meaning.
func normalizeSingleDashLongFlags(args []string) []string {
	normalized := append([]string(nil), args...)
	for i, arg := range normalized {
		if arg == "--" {
			break
		}
		if len(arg) <= 2 || arg[0] != '-' || arg[1] == '-' {
			continue
		}
		if arg[2] == '=' || !isASCIILetter(arg[1]) {
			continue
		}
		normalized[i] = "--" + arg[1:]
	}
	return normalized
}

func isASCIILetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func newVersionCommand(stdout io.Writer) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print kenn-forge build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return writeVersion(stdout, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "render output as JSON")
	return cmd
}

func newConfigCommand(stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Read kenn-forge configuration"}
	var configPath string
	read := &cobra.Command{
		Use:   "read KEY",
		Short: "Read a configuration value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return readConfigValue(configPath, args[0], stdout)
		},
	}
	read.Flags().StringVar(&configPath, "config", config.DefaultConfigPath(), "path to config file")
	cmd.AddCommand(read)
	return cmd
}

func newAgentHookCommand(stdin io.Reader, stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{Use: "agent-hook", Short: "Receive and install agent lifecycle hooks"}
	var agent, configPath, source string
	run := &cobra.Command{
		Use:   "run",
		Short: "Receive one agent lifecycle hook",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return receiveAgentHook(cmd.Context(), agent, configPath, source, stdin, stdout)
		},
	}
	run.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error { return nil })
	run.Flags().StringVar(&agent, "agent", "", "agent hook integration")
	run.Flags().StringVar(&configPath, "config", config.DefaultConfigPath(), "kenn-forge config path")
	run.Flags().StringVar(&source, "source", "", "hook source marker")
	cmd.AddCommand(run)

	for _, action := range []string{"install", "uninstall"} {
		var actionConfigPath, actionAgent, binary string
		leaf := &cobra.Command{
			Use:   action,
			Short: action + " kenn-forge agent lifecycle hooks",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return installAgentHooks(action, actionConfigPath, actionAgent, binary, stdout)
			},
		}
		leaf.Flags().StringVar(&actionConfigPath, "config", config.DefaultConfigPath(), "kenn-forge config path")
		leaf.Flags().StringVar(&actionAgent, "agent", "", "agent hook integration (empty selects all)")
		leaf.Flags().StringVar(&binary, "binary", "", "kenn-forge binary path used by installed hooks")
		cmd.AddCommand(leaf)
	}
	return cmd
}

func newPtyOwnerCommand() *cobra.Command {
	var root, session, cwd, commandJSON string
	cmd := &cobra.Command{
		Use:    "pty-owner",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPtyOwner(root, session, cwd, commandJSON)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "pty owner state root")
	cmd.Flags().StringVar(&session, "session", "", "session name")
	cmd.Flags().StringVar(&cwd, "cwd", "", "working directory")
	cmd.Flags().StringVar(&commandJSON, "command-json", "", "JSON command argv")
	return cmd
}

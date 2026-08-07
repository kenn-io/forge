package main

import (
	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
)

func newDaemonCommand(runner daemonCommandRunner) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the background server",
		Args:  cobra.NoArgs,
	}
	cmd.PersistentFlags().StringVar(
		&configPath,
		"config",
		config.DefaultConfigPath(),
		"path to config file",
	)

	start := &cobra.Command{
		Use:   "start",
		Short: "Start the background server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runner.Start(cmd.Context(), configPath, cmd.OutOrStdout())
		},
	}
	var asJSON bool
	status := &cobra.Command{
		Use:   "status",
		Short: "Show background server status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runner.Status(cmd.Context(), configPath, asJSON, cmd.OutOrStdout())
		},
	}
	status.Flags().BoolVar(&asJSON, "json", false, "render output as JSON")
	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the background server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runner.Stop(cmd.Context(), configPath, cmd.OutOrStdout())
		},
	}
	restart := &cobra.Command{
		Use:   "restart",
		Short: "Restart the background server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runner.Restart(cmd.Context(), configPath, cmd.OutOrStdout())
		},
	}
	cmd.AddCommand(start, status, stop, restart)
	return cmd
}

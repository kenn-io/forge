package main

import (
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"go.kenn.io/forge/internal/workspace/localruntime"
)

func newACPHostCommand() *cobra.Command {
	return &cobra.Command{
		Use: "acp-host CONFIG", Hidden: true, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
			defer cancel()
			stopTelemetry, err := initTelemetry(ctx, "kenn-forge-acp-host")
			if err != nil {
				return err
			}
			defer stopTelemetry()
			return localruntime.RunACPHost(ctx, args[0])
		},
	}
}

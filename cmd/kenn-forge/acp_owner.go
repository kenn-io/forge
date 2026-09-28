package main

import (
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func newACPOwnerCommand() *cobra.Command {
	return &cobra.Command{
		Use: "acp-owner CONFIG", Hidden: true, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
			defer cancel()
			return localruntime.RunACPOwner(ctx, args[0])
		},
	}
}

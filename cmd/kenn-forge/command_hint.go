package main

import (
	"github.com/spf13/cobra"
)

func commandHint(cmd *cobra.Command, args []string, flags ...string) string {
	for _, name := range flags {
		if cmd.Flags().Changed(name) {
			args = append(args, "--"+name, cmd.Flag(name).Value.String())
		}
	}
	return quoteCommand(args)
}

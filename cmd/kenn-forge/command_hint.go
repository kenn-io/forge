package main

import (
	"github.com/spf13/cobra"
)

func commandHint(cmd *cobra.Command, args []string, flags ...string) string {
	for _, name := range flags {
		if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
			args = append(args, "--"+name, flag.Value.String())
		}
	}
	return quoteCommand(args)
}

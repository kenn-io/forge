package serve

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
)

type Options struct {
	ConfigPath   string
	ProfilerAddr string
	DisableSync  bool
}

type Runner func(opts Options) error

func NewCommand(run Runner) *cobra.Command {
	opts := Options{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the kenn-forge daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ProfilerAddr = strings.TrimSpace(opts.ProfilerAddr)
			return run(opts)
		},
	}
	cmd.Flags().StringVar(&opts.ConfigPath, "config", config.DefaultConfigPath(), "path to config file")
	cmd.Flags().BoolVar(&opts.DisableSync, "disable-sync", false, "disable all provider synchronization")
	cmd.Flags().StringVar(
		&opts.ProfilerAddr,
		"pprof-addr",
		strings.TrimSpace(os.Getenv("KENN_FORGE_PPROF_ADDR")),
		"address for optional net/http/pprof listener (empty disables)",
	)
	return cmd
}

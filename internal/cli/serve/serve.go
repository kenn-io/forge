package serve

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
	"go.kenn.io/middleman/internal/config"
)

type Options struct {
	ConfigPath   string
	ProfilerAddr string
}

type Runner func(opts Options) error

func NewCommand(run Runner) *cobra.Command {
	opts := Options{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the middleman daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ProfilerAddr = strings.TrimSpace(opts.ProfilerAddr)
			return run(opts)
		},
	}
	cmd.Flags().StringVar(&opts.ConfigPath, "config", config.DefaultConfigPath(), "path to config file")
	cmd.Flags().StringVar(
		&opts.ProfilerAddr,
		"pprof-addr",
		strings.TrimSpace(os.Getenv("MIDDLEMAN_PPROF_ADDR")),
		"address for optional net/http/pprof listener (empty disables)",
	)
	return cmd
}

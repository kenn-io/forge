package serve

import (
	"flag"
	"io"
	"os"
	"strings"

	"go.kenn.io/middleman/internal/config"
)

type Options struct {
	ConfigPath   string
	ProfilerAddr string
}

type Runner func(opts Options) error

func Run(args []string, run Runner) error {
	fs := flag.NewFlagSet("middleman serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String(
		"config", config.DefaultConfigPath(),
		"path to config file",
	)
	profilerAddr := fs.String(
		"pprof-addr",
		strings.TrimSpace(os.Getenv("MIDDLEMAN_PPROF_ADDR")),
		"address for optional net/http/pprof listener (empty disables)",
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return run(Options{
		ConfigPath:   *configPath,
		ProfilerAddr: strings.TrimSpace(*profilerAddr),
	})
}

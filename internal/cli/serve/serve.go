package serve

import (
	"flag"
	"io"

	"go.kenn.io/middleman/internal/config"
)

type Runner func(configPath string) error

func Run(args []string, run Runner) error {
	fs := flag.NewFlagSet("middleman serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String(
		"config", config.DefaultConfigPath(),
		"path to config file",
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	return run(*configPath)
}

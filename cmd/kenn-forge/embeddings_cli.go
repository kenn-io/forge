package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/kit/embedconfig"
	"go.kenn.io/kit/embedlocal"
)

type localEmbeddingDeps struct {
	endpoint func(context.Context, embedlocal.Options) (string, error)
	install  func(context.Context, embedlocal.InstallOptions) (embedlocal.Installation, error)
}

func defaultLocalEmbeddingDeps() localEmbeddingDeps {
	return localEmbeddingDeps{embedlocal.Endpoint, embedlocal.Install}
}

func (d localEmbeddingDeps) resolve(ctx context.Context, builtin bool, confirm func(context.Context, int64) (bool, error)) (string, error) {
	opts := embedlocal.Options{SkipOllama: builtin}
	endpoint, err := d.endpoint(ctx, opts)
	if !errors.Is(err, embedlocal.ErrNotFound) {
		return endpoint, err
	}
	dir, err := embedlocal.DefaultDir()
	if err != nil {
		return "", err
	}
	installation, err := d.install(ctx, embedlocal.InstallOptions{DataDir: dir, Confirm: confirm})
	if err != nil {
		return "", err
	}
	opts.DataDir, opts.Executable = installation.DataDir, installation.Executable
	return d.endpoint(ctx, opts)
}

func newEmbeddingsCommand(stdin io.Reader, stdout, stderr io.Writer, deps localEmbeddingDeps) *cobra.Command {
	root := &cobra.Command{Use: "embeddings", Short: "Configure semantic search"}
	var path string
	var yes bool
	local := &cobra.Command{Use: "local", Short: "Set up local embeddings", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		// Create a fresh config only after setup succeeds.
		cfg, err := config.Load(path)
		fresh := errors.Is(err, os.ErrNotExist)
		if err != nil && !fresh {
			return err
		}
		if fresh {
			cfg = &config.Config{}
		}
		switching := strings.TrimSpace(cfg.Embeddings.BaseURL) != "" || cfg.LocalEmbeddings == "auto"
		recipe := cfg.Embeddings
		local := cfg.LocalEmbeddings
		rebuild := switching && !config.LocalEmbeddingCompatible(recipe)
		if rebuild || !cfg.Embeddings.Enabled() {
			recipe = config.DefaultLocalEmbeddingRecipe()
		}
		if local == "" {
			local = "auto"
		}
		if switching {
			recipe.BaseURL = ""
			recipe.APIKey = embedconfig.Embedder{}.APIKey
			local = "builtin"
		}
		confirmed := false
		confirm := func(_ context.Context, size int64) (bool, error) {
			if rebuild {
				if _, err := fmt.Fprintln(stderr, "Switching to the built-in server will rebuild the semantic index."); err != nil {
					return false, err
				}
			} else if switching {
				if _, err := fmt.Fprintln(stderr, "Switching to the built-in server keeps the semantic index."); err != nil {
					return false, err
				}
			}
			if size > 0 {
				if _, err := fmt.Fprintf(stderr, "Download approximately %d MB.\n", size/1_000_000); err != nil {
					return false, err
				}
			}
			if yes {
				confirmed = true
				return true, nil
			}
			if !stdinIsTerminal(stdin) {
				return false, errors.New("non-interactive setup requires --yes")
			}
			ok, err := confirmFleetSetup(stdin, stderr)
			confirmed = ok
			return ok, err
		}
		// Discover first, so a switch and any download share one confirmation.
		endpoint, err := deps.resolve(cmd.Context(), local == "builtin", confirm)
		if err != nil {
			return err
		}
		if switching && !confirmed {
			ok, err := confirm(cmd.Context(), 0)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("setup canceled")
			}
		}
		if fresh {
			if err := config.EnsureDefault(path); err != nil {
				return err
			}
		}
		cfg, err = config.Load(path)
		if err != nil {
			return err
		}
		cfg.Embeddings, cfg.LocalEmbeddings = recipe, local
		if err := cfg.Save(path); err != nil {
			return err
		}
		// Kit discovers Ollama at this fixed URL; its built-in server uses a dynamic port.
		if endpoint != "http://127.0.0.1:11434/v1" {
			if err := embedlocal.RecordChoice(); err != nil {
				fmt.Fprintf(stderr, "Warning: search by meaning choice write reported an error: %v\n", err)
			}
		}
		_, err = fmt.Fprintf(stdout, "Local embeddings configured. Apply with:\n  %s\nIf a service manager runs Forge, restart it through the service manager.\n", commandHint(cmd, []string{"kenn-forge", "daemon", "restart"}, "config"))
		return err
	}}
	local.Flags().StringVar(&path, "config", config.DefaultConfigPath(), "path to config file")
	local.Flags().BoolVar(&yes, "yes", false, "accept setup and downloads")
	root.AddCommand(local)
	return root
}

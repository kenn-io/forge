package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/semantic"
	"go.kenn.io/kit/embedlocal"
)

// openSemanticSearch opens the embedding index for the configured endpoint.
// The caller closes it after the indexer and every search have stopped.
func openSemanticSearch(ctx context.Context, cfg *config.Config, database *db.DB, deps localEmbeddingDeps) (*semantic.Indexer, error) {
	recipe := cfg.Embeddings
	builtin := cfg.LocalEmbeddings == "builtin"
	if !recipe.Enabled() {
		recorded, err := embedlocal.ChoiceRecorded()
		if err != nil {
			return nil, fmt.Errorf("read search by meaning choice: %w", err)
		}
		if !recorded {
			return nil, nil
		}
		recipe = config.DefaultLocalEmbeddingRecipe()
		builtin = true
	}
	var resolve func(context.Context) (string, bool, error)
	if strings.TrimSpace(recipe.BaseURL) == "" {
		recipe = config.LocalEmbeddingRecipe(recipe)
		resolve = func(ctx context.Context) (string, bool, error) {
			return deps.resolveSemantic(ctx, builtin)
		}
	}
	encoder, err := semantic.NewEncoder(recipe, resolve)
	if err != nil {
		return nil, err
	}
	return semantic.Open(ctx, filepath.Join(cfg.DataDir, semantic.SidecarFile), encoder, database, cfg.SyncDuration())
}

func (d localEmbeddingDeps) resolveSemantic(ctx context.Context, builtin bool) (string, bool, error) {
	endpoint, err := d.resolve(ctx, builtin, nil)
	if err != nil {
		return "", false, fmt.Errorf("local embeddings: run kenn-forge embeddings local: %w", err)
	}
	if builtin {
		return endpoint, true, nil
	}
	local, err := d.endpoint(ctx, embedlocal.Options{SkipOllama: true})
	if err != nil && !errors.Is(err, embedlocal.ErrNotFound) {
		return "", false, fmt.Errorf("local embeddings: %w", err)
	}
	return endpoint, err == nil && endpoint == local, nil
}

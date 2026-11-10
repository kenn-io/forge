package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/semantic"
)

// openSemanticSearch opens the embedding index for the configured endpoint.
// The caller closes it after the indexer and every search have stopped.
func openSemanticSearch(ctx context.Context, cfg *config.Config, database *db.DB) (*semantic.Indexer, error) {
	recipe := cfg.Embeddings
	var resolve func(context.Context) (string, error)
	if cfg.LocalEmbeddings && strings.TrimSpace(recipe.BaseURL) == "" {
		recipe = config.LocalEmbeddingRecipe(recipe)
		resolve = func(ctx context.Context) (string, error) {
			endpoint, err := defaultLocalEmbeddingDeps().resolve(ctx, true, nil)
			if err != nil {
				return "", fmt.Errorf("local embeddings: run kenn-forge embeddings local: %w", err)
			}
			return endpoint, nil
		}
	}
	encoder, err := semantic.NewEncoderWithResolver(recipe, resolve)
	if err != nil {
		return nil, err
	}
	return semantic.Open(ctx, filepath.Join(cfg.DataDir, semantic.SidecarFile), encoder, database, cfg.SyncDuration())
}

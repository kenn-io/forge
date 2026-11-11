package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
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
	var resolve func(context.Context) (string, error)
	if strings.TrimSpace(recipe.BaseURL) == "" {
		recipe = config.LocalEmbeddingRecipe(recipe)
		resolve = func(ctx context.Context) (string, error) {
			endpoint, err := deps.resolve(ctx, builtin, nil)
			if err != nil {
				return "", fmt.Errorf("local embeddings: run kenn-forge embeddings local: %w", err)
			}
			return endpoint, nil
		}
	}
	encoder, err := semantic.NewEncoder(recipe, resolve)
	if err != nil {
		return nil, err
	}
	return semantic.Open(ctx, filepath.Join(cfg.DataDir, semantic.SidecarFile), encoder, database, cfg.SyncDuration())
}

func semanticSyncCompleted(indexer *semantic.Indexer, next func([]ghclient.RepoSyncResult)) func([]ghclient.RepoSyncResult) {
	return func(results []ghclient.RepoSyncResult) {
		next(results)
		indexer.Wake()
	}
}

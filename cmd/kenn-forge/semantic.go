package main

import (
	"context"
	"path/filepath"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/semantic"
)

// openSemanticSearch opens the embedding index for the configured endpoint.
// The caller closes it after the indexer and every search have stopped.
func openSemanticSearch(ctx context.Context, cfg *config.Config, database *db.DB) (*semantic.Indexer, error) {
	encoder, err := semantic.NewEncoder(cfg.Embeddings)
	if err != nil {
		return nil, err
	}
	return semantic.Open(ctx, filepath.Join(cfg.DataDir, semantic.SidecarFile), encoder, database, cfg.SyncDuration())
}

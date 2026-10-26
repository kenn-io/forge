package main

import (
	"context"
	"path/filepath"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/semantic"
)

// openSemanticSearch opens the embedding index for the configured endpoint.
// The caller closes the returned index after the indexer and every search
// have stopped.
func openSemanticSearch(
	ctx context.Context, cfg *config.Config, database *db.DB,
) (*semantic.Index, *semantic.Indexer, error) {
	encoder, err := semantic.NewEncoder(cfg.Embeddings)
	if err != nil {
		return nil, nil, err
	}
	index, err := semantic.Open(ctx, filepath.Join(cfg.DataDir, semantic.SidecarFile))
	if err != nil {
		return nil, nil, err
	}
	return index, semantic.NewIndexer(index, encoder, database, cfg.SyncDuration()), nil
}

package main

import (
	"context"
	"log/slog"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/semantic"
	"go.kenn.io/kit/embedlocal"
)

func semanticStartupConfig(cfg *config.Config, recorded bool) *config.Config {
	result := *cfg
	if cfg.LocalEmbeddings == "" && !cfg.Embeddings.Enabled() && recorded {
		result.Embeddings = config.DefaultLocalEmbeddingRecipe()
		result.LocalEmbeddings = "builtin"
	}
	return &result
}

func recordSavedBuiltinChoice(cfg *config.Config) {
	if strings.TrimSpace(cfg.Embeddings.BaseURL) != "" || cfg.LocalEmbeddings != "builtin" || !cfg.Embeddings.Enabled() {
		return
	}
	recorded, err := embedlocal.ChoiceRecorded()
	if err != nil {
		slog.Warn("could not read search by meaning choice", "err", err)
		return
	}
	if !recorded {
		if err := embedlocal.RecordChoice(); err != nil {
			slog.Warn("could not record search by meaning choice", "err", err)
		}
	}
}

func openStartupSemanticSearch(ctx context.Context, cfg *config.Config, database *db.DB) (*semantic.Indexer, error) {
	recordSavedBuiltinChoice(cfg)
	recorded := false
	if cfg.LocalEmbeddings == "" && !cfg.Embeddings.Enabled() {
		var err error
		recorded, err = embedlocal.ChoiceRecorded()
		if err != nil {
			slog.Warn("could not read search by meaning choice", "err", err)
		}
	}
	effective := semanticStartupConfig(cfg, recorded)
	if !effective.Embeddings.Enabled() {
		return nil, nil
	}
	return openSemanticSearch(ctx, effective, database)
}

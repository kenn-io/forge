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
			slog.Warn("search by meaning choice write reported an error", "err", err)
		}
	}
}

func openStartupSemanticSearch(ctx context.Context, cfg *config.Config, database *db.DB) (*semantic.Indexer, error) {
	recordSavedBuiltinChoice(cfg)
	effective := *cfg
	if cfg.LocalEmbeddings == "" && !cfg.Embeddings.Enabled() {
		recorded, err := embedlocal.ChoiceRecorded()
		if err != nil {
			slog.Warn("could not read search by meaning choice", "err", err)
		}
		if recorded {
			effective.Embeddings = config.DefaultLocalEmbeddingRecipe()
			effective.LocalEmbeddings = "builtin"
		}
	}
	if !effective.Embeddings.Enabled() {
		return nil, nil
	}
	return openSemanticSearch(ctx, &effective, database)
}

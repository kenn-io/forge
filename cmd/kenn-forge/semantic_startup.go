package main

import (
	"log/slog"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/kit/embedlocal"
)

func semanticStartupConfig(cfg *config.Config) *config.Config {
	result := *cfg
	if strings.TrimSpace(cfg.Embeddings.BaseURL) != "" {
		return &result
	}
	if cfg.LocalEmbeddings == "builtin" && cfg.Embeddings.Enabled() {
		recorded, err := embedlocal.ChoiceRecorded()
		if err != nil {
			slog.Warn("could not read search by meaning choice", "err", err)
		} else if !recorded {
			if err := embedlocal.RecordChoice(); err != nil {
				slog.Warn("could not record search by meaning choice", "err", err)
			}
		}
		return &result
	}
	if cfg.LocalEmbeddings != "" || cfg.Embeddings.Enabled() {
		return &result
	}
	recorded, err := embedlocal.ChoiceRecorded()
	if err != nil {
		slog.Warn("could not read search by meaning choice", "err", err)
		return &result
	}
	if recorded {
		result.Embeddings = config.DefaultLocalEmbeddingRecipe()
		result.LocalEmbeddings = "builtin"
	}
	return &result
}

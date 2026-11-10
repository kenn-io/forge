package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/kit/embedconfig"
	"go.kenn.io/kit/embedlocal"
)

func TestSemanticStartupChoice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg := &config.Config{}
	got := semanticStartupConfig(cfg)
	assert.False(t, got.Embeddings.Enabled())
	require.NoError(t, embedlocal.RecordChoice())
	got = semanticStartupConfig(cfg)
	assert.Equal(t, "builtin", got.LocalEmbeddings)
	assert.Equal(t, "nomic-embed-text", got.Embeddings.Model)
	assert.Equal(t, 768, got.Embeddings.Dims)
	assert.Empty(t, cfg.LocalEmbeddings)
	assert.False(t, cfg.Embeddings.Enabled())
	configured := &config.Config{Embeddings: embedconfig.Embedder{BaseURL: "http://127.0.0.1:1234/v1", Model: "other", Dims: 12}}
	assert.Equal(t, configured.Embeddings, semanticStartupConfig(configured).Embeddings)
	auto := &config.Config{LocalEmbeddings: "auto", Embeddings: embedconfig.Embedder{Model: "other", Dims: 12}}
	assert.Equal(t, auto, semanticStartupConfig(auto))
}

func TestSemanticStartupPromotesBuiltin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg := &config.Config{LocalEmbeddings: "builtin", Embeddings: embedconfig.Embedder{Model: "nomic-embed-text", Dims: 768}}
	semanticStartupConfig(cfg)
	recorded, err := embedlocal.ChoiceRecorded()
	require.NoError(t, err)
	assert.True(t, recorded)
}

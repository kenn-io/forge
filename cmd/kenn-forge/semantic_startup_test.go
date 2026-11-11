package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/kit/embedconfig"
	"go.kenn.io/kit/embedlocal"
)

func TestSemanticStartupChoice(t *testing.T) {
	cfg := &config.Config{}
	got := semanticStartupConfig(cfg, false)
	assert.False(t, got.Embeddings.Enabled())
	got = semanticStartupConfig(cfg, true)
	assert.Equal(t, "builtin", got.LocalEmbeddings)
	assert.Equal(t, "nomic-embed-text", got.Embeddings.Model)
	assert.Equal(t, 768, got.Embeddings.Dims)
	assert.Empty(t, cfg.LocalEmbeddings)
	assert.False(t, cfg.Embeddings.Enabled())
	configured := &config.Config{Embeddings: embedconfig.Embedder{BaseURL: "http://127.0.0.1:1234/v1", Model: "other", Dims: 12}}
	assert.Equal(t, configured.Embeddings, semanticStartupConfig(configured, true).Embeddings)
	auto := &config.Config{LocalEmbeddings: "auto"}
	assert.Equal(t, auto, semanticStartupConfig(auto, true))
}

func TestRecordSavedBuiltinChoice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg := &config.Config{LocalEmbeddings: "builtin", Embeddings: embedconfig.Embedder{BaseURL: "http://127.0.0.1:1234/v1", Model: "other", Dims: 12}}
	recordSavedBuiltinChoice(cfg)
	recorded, err := embedlocal.ChoiceRecorded()
	require.NoError(t, err)
	assert.False(t, recorded)
	cfg.Embeddings = embedconfig.Embedder{Model: "nomic-embed-text", Dims: 768}
	recordSavedBuiltinChoice(cfg)
	recorded, err = embedlocal.ChoiceRecorded()
	require.NoError(t, err)
	assert.True(t, recorded)
}

func TestSemanticStartupOpensRecordedChoice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	require.NoError(t, embedlocal.RecordChoice())
	cfg := &config.Config{DataDir: t.TempDir()}
	indexer, err := openStartupSemanticSearch(t.Context(), cfg, dbtest.Open(t))
	require.NoError(t, err)
	require.NotNil(t, indexer)
	require.NoError(t, indexer.Close())
}

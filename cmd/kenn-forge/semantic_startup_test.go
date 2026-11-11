package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/kit/embedconfig"
	"go.kenn.io/kit/embedlocal"
)

func TestRecordSavedBuiltinChoice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, config.EnsureDefault(path))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	cfg.LocalEmbeddings = "builtin"
	cfg.Embeddings = embedconfig.Embedder{BaseURL: "http://127.0.0.1:1234/v1", Model: "other", Dims: 12}
	require.NoError(t, cfg.Validate())
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

func TestSemanticStartupRecordedChoice(t *testing.T) {
	t.Run("saved built-in", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		path := filepath.Join(t.TempDir(), "config.toml")
		require.NoError(t, config.EnsureDefault(path))
		cfg, err := config.Load(path)
		require.NoError(t, err)
		cfg.DataDir = t.TempDir()
		cfg.LocalEmbeddings = "builtin"
		cfg.Embeddings = embedconfig.Embedder{Model: "nomic-embed-text", Dims: 768}
		indexer, err := openStartupSemanticSearch(t.Context(), cfg, dbtest.Open(t))
		require.NoError(t, err)
		require.NotNil(t, indexer)
		require.NoError(t, indexer.Close())
		recorded, err := embedlocal.ChoiceRecorded()
		require.NoError(t, err)
		assert.True(t, recorded)
	})
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	database := dbtest.Open(t)
	disabled, err := openStartupSemanticSearch(t.Context(), &config.Config{DataDir: t.TempDir()}, database)
	require.NoError(t, err)
	assert.Nil(t, disabled)
	require.NoError(t, embedlocal.RecordChoice())
	cfg := &config.Config{DataDir: t.TempDir()}
	effective := semanticStartupConfig(cfg, true)
	assert.Equal(t, "builtin", effective.LocalEmbeddings)
	assert.Equal(t, embedconfig.Embedder{Model: "nomic-embed-text", Dims: 768, DocumentPrefix: "search_document: ", QueryPrefix: "search_query: "}, effective.Embeddings)
	assert.False(t, cfg.Embeddings.Enabled())
	indexer, err := openStartupSemanticSearch(t.Context(), cfg, database)
	require.NoError(t, err)
	require.NotNil(t, indexer)
	require.NoError(t, indexer.Close())
}

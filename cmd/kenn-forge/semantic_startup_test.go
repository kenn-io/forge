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

func TestSemanticStartupRecordedChoice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	database := dbtest.Open(t)
	disabled, err := openStartupSemanticSearch(t.Context(), &config.Config{DataDir: t.TempDir()}, database)
	require.NoError(t, err)
	assert.Nil(t, disabled)
	require.NoError(t, embedlocal.RecordChoice())
	cfg := &config.Config{DataDir: t.TempDir()}
	indexer, err := openStartupSemanticSearch(t.Context(), cfg, database)
	require.NoError(t, err)
	require.NotNil(t, indexer)
	require.NoError(t, indexer.Close())
}

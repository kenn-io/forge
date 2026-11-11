package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/embedfake"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/kit/embedconfig"
	"go.kenn.io/kit/embedlocal"
)

func TestSemanticStartup(t *testing.T) {
	database := dbtest.Open(t)
	repoID, err := reposeed.Seed(t.Context(), database, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 301, Number: 2, URL: "https://github.com/acme/widget/pull/2", Title: "Cache is slow", Author: "user-a", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
	require.NoError(t, err)

	topics := make([]string, 767)
	for i := range topics {
		topics[i] = "unused"
	}
	topics[0] = "cache"
	endpoint := embedfake.New(t, topics...)
	deps := localEmbeddingDeps{endpoint: func(_ context.Context, opts embedlocal.Options) (string, error) {
		assert.True(t, opts.SkipOllama)
		return endpoint.Embedder("nomic-embed-text").BaseURL, nil
	}}

	t.Run("no choice", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		indexer, err := openSemanticSearch(t.Context(), &config.Config{DataDir: t.TempDir()}, database, localEmbeddingDeps{})
		require.NoError(t, err)
		assert.Nil(t, indexer)
	})
	t.Run("recorded choice", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		require.NoError(t, embedlocal.RecordChoice())
		indexer, err := openSemanticSearch(t.Context(), &config.Config{DataDir: t.TempDir(), SyncInterval: "1h"}, database, deps)
		require.NoError(t, err)
		require.NotNil(t, indexer)
		defer func() { assert.NoError(t, indexer.Close()) }()
		require.NoError(t, indexer.Refresh(t.Context()))
		assert.Contains(t, endpoint.Inputs(), "search_document: Cache is slow")
		assert.Equal(t, "nomic-embed-text", endpoint.LastModel())
	})
	t.Run("configured endpoint", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		require.NoError(t, os.WriteFile(filepath.Join(home, ".kenn"), []byte("block"), 0o600))
		endpoint := embedfake.New(t, "cache")
		cfg := &config.Config{DataDir: t.TempDir(), Embeddings: endpoint.Embedder("custom-model")}
		indexer, err := openSemanticSearch(t.Context(), cfg, database, localEmbeddingDeps{})
		require.NoError(t, err)
		require.NotNil(t, indexer)
		defer func() { assert.NoError(t, indexer.Close()) }()
		require.NoError(t, indexer.Refresh(t.Context()))
		assert.Equal(t, "custom-model", endpoint.LastModel())
	})
	t.Run("saved built-in", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		cfg := &config.Config{DataDir: t.TempDir(), LocalEmbeddings: "builtin", Embeddings: embedconfig.Embedder{Model: "nomic-embed-text", Dims: 768}}
		indexer, err := openSemanticSearch(t.Context(), cfg, database, deps)
		require.NoError(t, err)
		require.NotNil(t, indexer)
		defer func() { assert.NoError(t, indexer.Close()) }()
		require.NoError(t, indexer.Refresh(t.Context()))
		assert.Contains(t, endpoint.Inputs(), "Cache is slow")
		recorded, err := embedlocal.ChoiceRecorded()
		require.NoError(t, err)
		assert.False(t, recorded)
	})
}

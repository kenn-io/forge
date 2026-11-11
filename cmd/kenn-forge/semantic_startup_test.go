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
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/semantic"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/embedfake"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
	"go.kenn.io/kit/embedconfig"
	"go.kenn.io/kit/embedlocal"
)

func TestSemanticStartupChoice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	database := dbtest.Open(t)
	cfg := &config.Config{DataDir: t.TempDir(), SyncInterval: "1h"}
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
	indexer, err := openSemanticSearch(t.Context(), cfg, database, deps)
	require.NoError(t, err)
	assert.Nil(t, indexer)
	require.NoError(t, embedlocal.RecordChoice())
	key := platform.RepositoryIDKey(101)
	identity := db.RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", Owner: "acme", Name: "widget", Key: key}
	repoID, err := reposeed.Seed(t.Context(), database, identity)
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 301, Number: 2, URL: "https://gitlab.com/acme/widget/-/merge_requests/2", Title: "Cache is slow", Author: "user-a", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
	require.NoError(t, err)
	indexer, err = openSemanticSearch(t.Context(), cfg, database, deps)
	require.NoError(t, err)
	require.NotNil(t, indexer)
	defer func() { assert.NoError(t, indexer.Close()) }()
	want := []ghclient.RepoSyncResult{{Owner: "acme", Name: "widget"}}
	var results []ghclient.RepoSyncResult
	woken := false
	semanticSyncCompleted(func() { woken = true }, func(got []ghclient.RepoSyncResult) {
		assert.False(t, woken)
		results = got
	})(want)
	assert.Equal(t, want, results)
	assert.True(t, woken)
	require.NoError(t, indexer.Refresh(t.Context()))
	item := db.RepoItemNumber{RepoID: repoID, Number: 2}
	rows, err := semantic.Rank(t.Context(), indexer, "cache", "pr", []db.RepoItemNumber{item}, func(row db.RepoItemNumber) db.RepoItemNumber { return row }, 0, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Contains(t, endpoint.Inputs(), "search_document: Cache is slow")
	assert.Equal(t, "nomic-embed-text", endpoint.LastModel())
	assert.Contains(t, endpoint.Inputs(), "search_query: cache")
	assert.False(t, cfg.Embeddings.Enabled())
	assert.Empty(t, cfg.LocalEmbeddings)
}

func TestSemanticStartupConfigured(t *testing.T) {
	t.Run("configured endpoint", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		require.NoError(t, os.WriteFile(filepath.Join(home, ".kenn"), []byte("block"), 0o600))
		database := dbtest.Open(t)
		endpoint := embedfake.New(t, "cache")
		cfg := &config.Config{DataDir: t.TempDir(), Embeddings: endpoint.Embedder("custom-model")}
		repoID, err := reposeed.Seed(t.Context(), database, db.GitHubRepoIdentity("github.com", "acme", "widget"))
		require.NoError(t, err)
		now := time.Now().UTC()
		_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{RepoID: repoID, PlatformID: 301, Number: 2, URL: "https://github.com/acme/widget/pull/2", Title: "Cache is slow", Author: "user-a", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
		require.NoError(t, err)
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
		indexer, err := openSemanticSearch(t.Context(), cfg, dbtest.Open(t), localEmbeddingDeps{})
		require.NoError(t, err)
		require.NotNil(t, indexer)
		require.NoError(t, indexer.Close())
		recorded, err := embedlocal.ChoiceRecorded()
		require.NoError(t, err)
		assert.False(t, recorded)
	})
}

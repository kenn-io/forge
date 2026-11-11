package main

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/embedfake"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/kit/daemon"
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
	automatic, err := openStartupSemanticSearch(t.Context(), &config.Config{DataDir: t.TempDir(), LocalEmbeddings: "auto"}, database)
	require.NoError(t, err)
	assert.Nil(t, automatic)
	topics := make([]string, 767)
	for i := range topics {
		topics[i] = "unused"
	}
	topics[0] = "cache"
	embeddings := embedfake.New(t, topics...)
	target, err := url.Parse(strings.TrimSuffix(embeddings.Embedder("nomic-embed-text").BaseURL, "/v1"))
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/api/ping", daemon.NewPingHandler(daemon.PingHandlerOptions{Service: embedlocal.Service, Version: embedlocal.Version}))
	mux.Handle("/v1/embeddings", httputil.NewSingleHostReverseProxy(target))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	ep, err := daemon.ParseEndpoint(server.URL, daemon.ParseEndpointOptions{TCPPolicy: daemon.RequireLoopback})
	require.NoError(t, err)
	dir, err := embedlocal.DefaultDir()
	require.NoError(t, err)
	_, err = (daemon.RuntimeStore{Dir: dir, Prefix: embedlocal.Service}).Write(daemon.NewRuntimeRecord(embedlocal.Service, embedlocal.Version, ep))
	require.NoError(t, err)
	repoID, err := reposeed.Seed(t.Context(), database, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = database.UpsertIssue(t.Context(), &db.Issue{RepoID: repoID, PlatformID: 301, Number: 2, URL: "https://github.com/acme/widget/issues/2", Title: "Cache is slow", Author: "user-a", State: "open", CreatedAt: now, UpdatedAt: now, LastActivityAt: now})
	require.NoError(t, err)
	cfg := &config.Config{DataDir: t.TempDir()}
	indexer, err := openStartupSemanticSearch(t.Context(), cfg, database)
	require.NoError(t, err)
	require.NotNil(t, indexer)
	defer func() { assert.NoError(t, indexer.Close()) }()
	require.NoError(t, indexer.Refresh(t.Context()))
	assert.Contains(t, embeddings.Inputs(), "search_document: Cache is slow")
}

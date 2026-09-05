package github_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/github"
)

func TestLandingSnapshotUsesCompleteInventoryAndPreservesProofInputs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var paths []string
	mismatch := ""
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		var body string
		switch req.URL.Path {
		case "/repos/team-a/project-a":
			body = `{"id":9007199254740993,"name":"project-a","owner":{"login":"team-a"},"default_branch":"main","clone_url":"https://github.com/team-a/project-a.git"}`
		case "/repos/team-a/project-a/branches/main":
			body = `{"commit":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`
		case "/repos/team-a/project-a/pulls":
			if mismatch == "credential" {
				return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"message":"bad credentials"}`)), Request: req}, nil
			}
			assert.Equal("all", req.URL.Query().Get("state"))
			assert.Equal("asc", req.URL.Query().Get("direction"))
			assert.Empty(req.URL.Query().Get("since"))
			body = `[{"id":51,"number":3,"merged_at":"2025-01-01T00:00:00Z","base":{"ref":"main"}}]`
		case "/repos/team-a/project-a/pulls/3":
			body = `{"id":51,"number":3,"merged":true,"merged_at":"2025-01-01T00:00:00Z","user":{"id":61,"login":"user-a","type":"User"},"merged_by":{"id":62,"login":"build-agent","type":"Bot"},"base":{"ref":"main","repo":{"id":9007199254740993}},"head":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","repo":{"id":41}},"merge_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","commits":1,"additions":0}`
			if mismatch == "change" {
				body = strings.Replace(body, `"id":51`, `"id":52`, 1)
			}
			if mismatch == "repository" {
				body = strings.Replace(body, `"id":9007199254740993`, `"id":17`, 1)
			}
		case "/repos/team-a/project-a/pulls/3/commits":
			body = `[{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]`
		default:
			require.FailNow("unexpected request", req.URL.String())
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	client, err := github.NewClient(github.ClientConfig{Host: "github.com", Read: hc, Write: hc, Notifications: hc, Clock: func() time.Time { return clock }})
	require.NoError(err)
	provider, err := github.NewProvider(github.ProviderConfig{Host: "github.com", Client: client, Clock: func() time.Time { return clock }})
	require.NoError(err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	snapshot, err := provider.CollectLandingEvidence(ctx, platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "team-a", Name: "project-a"}, platform.Budget{MaxRecords: 100, MaxNodes: 100, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20})
	require.NoError(err)
	assert.True(snapshot.Coverage.Complete)
	assert.Equal("9007199254740993", snapshot.Repository.Identity.ID)
	assert.Equal(clock, snapshot.StartedAt)
	require.Len(snapshot.Candidates, 1)
	candidate := snapshot.Candidates[0]
	assert.True(candidate.SourceComplete)
	assert.Equal([]string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, candidate.SourceCommits)
	assert.NotEmpty(candidate.TerminalProof)
	assert.Empty(candidate.Method, "a terminal SHA is not a method")
	assert.Equal(platform.AccountBot, candidate.Merger.Type)
	assert.Equal(new(int64(0)), candidate.Additions)
	assert.Nil(candidate.Deletions)
	assert.Equal("9007199254740993", candidate.BaseRepository.ID)
	assert.Equal(2, strings.Count(strings.Join(paths, "\n"), "/branches/main"), "identity/head are observed on both sides of collection")
	for _, kind := range []string{"change", "repository"} {
		mismatch = kind
		snapshot, err := provider.CollectLandingEvidence(ctx, platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "team-a", Name: "project-a"}, platform.Budget{MaxRecords: 100, MaxNodes: 100, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20})
		require.NoError(err)
		assert.Equal("candidate_identity_changed", snapshot.Coverage.Reason)
		assert.Empty(snapshot.Candidates)
	}
	mismatch = "credential"
	_, err = provider.CollectLandingEvidence(ctx, platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "team-a", Name: "project-a"}, platform.Budget{MaxRecords: 100, MaxNodes: 100, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20})
	require.ErrorIs(err, platform.ErrCredentialRejected)
}

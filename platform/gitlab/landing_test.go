package gitlab_test

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
	"go.kenn.io/forge/platform/gitlab"
)

type landingCredential struct{}

func (landingCredential) Token(context.Context) (string, error) { return "fixture-token", nil }
func (landingCredential) Invalidate(string)                     {}

func TestGitLabLandingRetainsSeparateSquashAndMergeSHAs(t *testing.T) {
	assert := assert.New(t)
	client, err := gitlab.NewClient("gitlab.example.org:8443", landingCredential{}, gitlab.WithClock(time.Now), gitlab.WithTransport(platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal("fixture-token", req.Header.Get("Private-Token"))
		body := ""
		switch req.URL.Path {
		case "/api/v4/projects/team-a/project-a":
			body = `{"id":17,"name":"project-a","path":"project-a","path_with_namespace":"team-a/project-a","default_branch":"main","http_url_to_repo":"https://gitlab.example.org:8443/team-a/project-a.git"}`
		case "/api/v4/projects/17/repository/branches/main":
			body = `{"commit":{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`
		case "/api/v4/projects/17/merge_requests":
			body = `[{"id":51,"iid":3,"state":"merged"}]`
			assert.Equal("all", req.URL.Query().Get("state"))
		case "/api/v4/projects/17/merge_requests/3":
			body = `{"id":51,"iid":3,"state":"merged","source_project_id":19,"target_project_id":17,"target_branch":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","merge_commit_sha":null,"squash_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","author":{"id":61,"username":"user-a","bot":false},"merge_user":{"id":62,"username":"build-agent","bot":true},"merged_at":"2026-01-01T00:00:00Z"}`
		case "/api/v4/projects/17/merge_requests/3/commits":
			body = `[{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]`
		default:
			require.FailNow(t, "unexpected request", req.URL.String())
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	snapshot, err := client.CollectLandingEvidence(ctx, platform.RepoRef{Platform: platform.KindGitLab, Host: "gitlab.example.org:8443", Owner: "team-a", Name: "project-a"}, platform.Budget{MaxRecords: 100, MaxNodes: 100, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20})
	require.NoError(t, err)
	require.Len(t, snapshot.Candidates, 1)
	candidate := snapshot.Candidates[0]
	assert.True(candidate.MergeSHA.Present)
	assert.Empty(candidate.MergeSHA.Value)
	assert.Equal("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", candidate.SquashSHA.Value)
	assert.Equal(platform.LandingSquash, candidate.Method)
	assert.True(candidate.SourceComplete)
	assert.Equal(platform.AccountBot, candidate.Merger.Type)
	assert.False(snapshot.Capabilities.CompleteCandidateInventory, "offset inventory has no proven tie-safe completeness")
	assert.False(snapshot.Coverage.Complete)
	assert.False(snapshot.Capabilities.FastForwardRange, "a source head is not a proven landed terminal")
}

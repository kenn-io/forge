package gitealike_test

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
	"go.kenn.io/forge/platform/forgejo"
	"go.kenn.io/forge/platform/gitea"
)

func TestGitealikeLandingPreservesUnknownAccountsAndUnprovenMethods(t *testing.T) {
	for _, kind := range []platform.Kind{platform.KindForgejo, platform.KindGitea} {
		t.Run(string(kind), func(t *testing.T) {
			assert := assert.New(t)
			transport := platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Equal("token test-token", req.Header.Get("Authorization"))
				body := ""
				switch req.URL.Path {
				case "/api/v1/repos/team-a/project-a":
					body = `{"id":17,"name":"project-a","owner":{"id":41,"login":"team-a"},"default_branch":"main","clone_url":"https://code.example.org/team-a/project-a.git"}`
				case "/api/v1/repos/team-a/project-a/branches/main":
					body = `{"commit":{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`
				case "/api/v1/repos/team-a/project-a/pulls":
					body = `[{"id":51,"number":3,"merged":true}]`
					assert.Equal("all", req.URL.Query().Get("state"))
				case "/api/v1/repos/team-a/project-a/pulls/3":
					body = `{"id":51,"number":3,"merged":true,"merged_at":"2026-01-01T00:00:00Z","user":{"id":61,"login":"user-a","is_admin":true},"merged_by":{"id":62,"login":"build-agent"},"base":{"ref":"main","repo":{"id":17}},"head":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","repo":{"id":19}},"merge_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","additions":0,"deletions":4,"changed_files":1}`
				case "/api/v1/repos/team-a/project-a/pulls/3/commits":
					body = `[{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]`
					if req.URL.Query().Get("page") == "2" {
						body = `[]`
					}
				default:
					require.FailNow(t, "unexpected request", req.URL.String())
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			var reader platform.LandingReader
			var err error
			if kind == platform.KindForgejo {
				reader, err = forgejo.NewClient("code.example.org", accountToken{}, forgejo.WithClock(time.Now), forgejo.WithTransport(transport))
			} else {
				reader, err = gitea.NewClient("code.example.org", accountToken{}, gitea.WithClock(time.Now), gitea.WithTransport(transport))
			}
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			snapshot, err := reader.CollectLandingEvidence(ctx, platform.RepoRef{Platform: kind, Host: "code.example.org", Owner: "team-a", Name: "project-a"}, platform.Budget{MaxRecords: 100, MaxNodes: 100, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20})
			require.NoError(t, err)
			require.Len(t, snapshot.Candidates, 1)
			candidate := snapshot.Candidates[0]
			assert.Equal(platform.AccountUnknown, candidate.Author.Type)
			assert.Equal(platform.AccountUnknown, candidate.Merger.Type)
			assert.Equal(new(int64(0)), candidate.Additions)
			assert.Equal(new(int64(4)), candidate.Deletions)
			assert.True(candidate.SourceComplete)
			assert.Empty(candidate.Method)
			assert.False(snapshot.Capabilities.AccountType)
			assert.False(snapshot.Capabilities.Squash)
			assert.False(snapshot.Coverage.Complete)
		})
	}
}

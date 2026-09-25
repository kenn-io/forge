package ghshim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/procutil"
)

func TestUnsupportedInvocationsDelegate(t *testing.T) {
	assert := assert.New(t)
	t.Parallel()
	for _, args := range [][]string{
		{"auth", "token"},
		{"pr", "checks", "1", "--json", "state"},
		{"pr", "list", "--json", "author"},
		{"pr", "list", "--json", "title", "--jq", ".[].title"},
		{"pr", "list", "--json", "number", "--search", "draft:true"},
		{"pr", "list", "--json", "number", "--limit", "0"},
		{"pr", "view", "branch", "--json", "number"},
		{"pr", "view", "1", "--json", "number", "--comments"},
		{"pr", "list"},
		{"pr", "list", "--json", "number", "extra"},
	} {
		_, _, ok := Parse(args)
		assert.False(ok, args)
	}
}

// Exercise the real gh exporter against a synthetic GraphQL endpoint. Expected
// bytes come from gh, not a second implementation of its JSON conventions.
func TestJSONMatchesRealGH(t *testing.T) {
	binary, err := exec.LookPath("gh")
	if err != nil {
		t.Skip("gh is not installed")
	}
	fields := Fields
	for _, tc := range []struct{ name, command, graph, rest string }{
		{"open list", "list", `{"number":7,"title":"Fix <tag> & quote \"","body":"hello\nworld","state":"OPEN","url":"https://github.com/acme/widget/pull/7","isDraft":true,"headRefName":"topic","headRefOid":"abc","baseRefName":"main","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z","closedAt":null,"mergedAt":null}`, `{"number":7,"title":"Fix <tag> & quote \"","body":"hello\nworld","state":"open","html_url":"https://github.com/acme/widget/pull/7","draft":true,"head":{"ref":"topic","sha":"abc"},"base":{"ref":"main"},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`},
		{"merged view", "view", `{"number":7,"title":"Merged","body":"","state":"MERGED","url":"https://github.com/acme/widget/pull/7","isDraft":false,"headRefName":"topic","headRefOid":"abc","baseRefName":"main","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z","closedAt":"2026-01-02T00:00:00Z","mergedAt":"2026-01-02T00:00:00Z"}`, `{"number":7,"title":"Merged","body":"","state":"closed","html_url":"https://github.com/acme/widget/pull/7","head":{"ref":"topic","sha":"abc"},"base":{"ref":"main"},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","closed_at":"2026-01-02T00:00:00Z","merged_at":"2026-01-02T00:00:00Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Host != "api.github.localhost" || r.URL.Path != "/graphql" {
					http.Error(w, "unexpected endpoint", 500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.command == "list" {
					fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"nodes":[%s],"totalCount":1,"pageInfo":{"hasNextPage":false}}}}}`, tc.graph)
				} else {
					fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":%s}}}`, tc.graph)
				}
			}))
			defer server.Close()
			args := []string{"pr", tc.command, "--repo", "github.localhost/acme/widget", "--json", fields, "--json", "number"}
			if tc.command == "view" {
				args = append(args, "7")
			}
			cmd := procutil.CommandContext(t.Context(), binary, args...)
			// The HTTP proxy is also the fake provider; no request can reach GitHub.
			cmd.Env = append(os.Environ(), "GH_TOKEN=fixture", "GH_ENTERPRISE_TOKEN=fixture", "GH_CONFIG_DIR="+t.TempDir(), "HTTP_PROXY="+server.URL, "HTTPS_PROXY="+server.URL, "NO_PROXY=", "GH_DEBUG=", "GH_FORCE_TTY=", "CLICOLOR_FORCE=", "NO_COLOR=1", "GH_PROMPT_DISABLED=1")
			expected, err := cmd.Output()
			require.NoError(err)
			var pr gh.PullRequest
			require.NoError(json.Unmarshal([]byte(tc.rest), &pr))
			q, _, ok := Parse(args)
			require.True(ok)
			stored := db.MergeRequest{Number: pr.GetNumber(), Title: pr.GetTitle(), Body: pr.GetBody(), State: db.MergeRequestState(pr.GetState()), URL: pr.GetHTMLURL(), IsDraft: pr.GetDraft(), HeadBranch: pr.GetHead().GetRef(), PlatformHeadSHA: pr.GetHead().GetSHA(), BaseBranch: pr.GetBase().GetRef(), CreatedAt: pr.GetCreatedAt().Time, UpdatedAt: pr.GetUpdatedAt().Time}
			if pr.ClosedAt != nil {
				stored.ClosedAt = &pr.ClosedAt.Time
			}
			if pr.MergedAt != nil {
				stored.MergedAt = &pr.MergedAt.Time
				stored.State = db.MergeRequestStateMerged
			}
			actual, err := Encode(q, []db.MergeRequest{stored})
			require.NoError(err)
			assert.Equal(string(expected), string(actual))
		})
	}
}

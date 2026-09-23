package ghshim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
)

func TestUnsupportedInvocationsDelegate(t *testing.T) {
	assert := assert.New(t)
	t.Parallel()
	for _, args := range [][]string{
		{"auth", "token"}, {"pr", "checks", "1", "--json", "state"},
		{"pr", "list", "--json", "author"}, {"pr", "list", "--json", "title", "--jq", ".[].title"},
		{"pr", "list", "--json", "number", "--search", "draft:true"}, {"pr", "list", "--json", "number", "--limit", "0"},
		{"pr", "view", "branch", "--json", "number"}, {"pr", "view", "1", "--json", "number", "--comments"},
		{"pr", "list"}, {"pr", "list", "--json", "number", "extra"},
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
			actual, err := Encode(q, []*gh.PullRequest{&pr})
			require.NoError(err)
			assert.Equal(string(expected), string(actual))
		})
	}
}

type provider struct {
	pages    int
	requests int
	fail     bool
}

func (p *provider) GetPullRequest(context.Context, string, string, int) (*gh.PullRequest, error) {
	p.requests++
	return &gh.PullRequest{Number: new(7), State: new("open")}, nil
}
func (p *provider) ListPullRequestsPage(_ context.Context, owner, repo, state string, page int) ([]*gh.PullRequest, bool, error) {
	p.pages++
	if owner != "acme" || repo != "widget" || state != "closed" {
		return nil, false, fmt.Errorf("wrong query")
	}
	if page == 2 && p.fail {
		return nil, false, fmt.Errorf("provider failed")
	}
	pr := &gh.PullRequest{Number: new(page), State: new("closed"), Head: &gh.PullRequestBranch{Ref: new("topic")}, CreatedAt: &gh.Timestamp{Time: time.Date(2026, 1, page, 0, 0, 0, 0, time.UTC)}}
	if page == 2 {
		pr.MergedAt = &gh.Timestamp{Time: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)}
	}
	return []*gh.PullRequest{pr}, page == 1, nil
}

func TestCacheCompleteListsFilterAfterSorting(t *testing.T) {
	require := require.New(t)
	t.Parallel()
	assert := assert.New(t)
	p := &provider{}
	var cache Cache
	q, _, ok := Parse(strings.Fields("pr list --state closed --head topic --limit 1 --json number,state"))
	require.True(ok)
	q.Owner = "acme"
	q.Repo = "widget"
	data, err := cache.Query(t.Context(), p, "stable-id/route-1", q)
	require.NoError(err)
	assert.JSONEq(`[{"number":2,"state":"MERGED"}]`, string(data))
	q.State = "merged"
	data, err = cache.Query(t.Context(), p, "stable-id/route-1", q)
	require.NoError(err)
	assert.JSONEq(`[{"number":2,"state":"MERGED"}]`, string(data))
	assert.Equal(2, p.pages)
	q.Head = "absent"
	data, err = cache.Query(t.Context(), p, "stable-id/route-1", q)
	require.NoError(err)
	assert.Equal("[]\n", string(data))
	assert.Equal(2, p.pages)
	_, err = cache.Query(t.Context(), p, "stable-id/route-2", q)
	require.NoError(err)
	assert.Equal(4, p.pages)
}

func TestIncompleteHydrationNeverReturnsOrCachesPartialOutput(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Parallel()
	p := &provider{fail: true}
	var cache Cache
	q, _, ok := Parse(strings.Fields("pr list --state closed --json number"))
	require.True(ok)
	q.Owner = "acme"
	q.Repo = "widget"
	data, err := cache.Query(t.Context(), p, "repo", q)
	require.Error(err)
	assert.Empty(data)
	p.fail = false
	data, err = cache.Query(t.Context(), p, "repo", q)
	require.NoError(err)
	assert.Equal("[{\"number\":2},{\"number\":1}]\n", string(data))
	assert.Equal(4, p.pages)
}

func TestExpiredViewHydratesAgain(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Parallel()
	p := &provider{}
	var cache Cache
	q, _, ok := Parse(strings.Fields("pr view 7 --json number"))
	require.True(ok)
	_, err := cache.Query(t.Context(), p, "repo", q)
	require.NoError(err)
	for key, value := range cache.entries {
		value.until = time.Time{}
		cache.entries[key] = value
	}
	_, err = cache.Query(t.Context(), p, "repo", q)
	require.NoError(err)
	assert.Equal(2, p.requests)
}

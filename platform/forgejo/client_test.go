package forgejo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/gitealike"
)

var (
	_ gitealike.Transport                     = (*transport)(nil)
	_ gitealike.ActionsTransport              = (*transport)(nil)
	_ platform.RepositoryReader               = (*Client)(nil)
	_ platform.MergeRequestReader             = (*Client)(nil)
	_ platform.IssueReader                    = (*Client)(nil)
	_ platform.ReleaseReader                  = (*Client)(nil)
	_ platform.TagReader                      = (*Client)(nil)
	_ platform.CIReader                       = (*Client)(nil)
	_ platform.CommentMutator                 = (*Client)(nil)
	_ platform.StateMutator                   = (*Client)(nil)
	_ platform.MergeMutator                   = (*Client)(nil)
	_ platform.ReviewMutator                  = (*Client)(nil)
	_ platform.IssueMutator                   = (*Client)(nil)
	_ platform.IssuePageReader                = (*Client)(nil)
	_ platform.MergeRequestPageReader         = (*Client)(nil)
	_ platform.DiffReviewDraftMutator         = (*Client)(nil)
	_ platform.MergeRequestReviewThreadReader = (*Client)(nil)
)

func TestClientReadsIssuePullReferenceTimelineEvents(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var timelinePages []string

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("token forgejo-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/issues/3/comments":
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{}))
		case "/api/v1/repos/owner/repo/issues/3/timeline":
			page := r.URL.Query().Get("page")
			timelinePages = append(timelinePages, page)
			if page == "1" {
				w.Header().Set("Link", fmt.Sprintf(
					`<%s/api/v1/repos/owner/repo/issues/3/timeline?page=2&limit=100>; rel="next"`,
					server.URL,
				))
				assert.NoError(json.NewEncoder(w).Encode([]map[string]any{}))
				return
			}
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{{
				"id": 13, "type": "pull_ref", "user": map[string]any{"login": "dana"},
				"created_at": "2026-05-01T10:03:00Z",
				"ref_issue": map[string]any{
					"number": 9, "title": "Fix the issue",
					"html_url":     "https://codeberg.test/acme/tools/pulls/9",
					"pull_request": map[string]any{"merged": false},
					"repository":   map[string]any{"owner": "acme", "name": "tools", "full_name": "acme/tools"},
				},
			}}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient("codeberg.test", testTokenSource("forgejo-token"), WithBaseURLForTesting(server.URL), WithTransport(http.DefaultTransport))
	require.NoError(err)
	events, err := client.ListIssueEvents(t.Context(), platform.RepoRef{
		Host: "codeberg.test", Owner: "owner", Name: "repo", RepoPath: "owner/repo",
	}, 3)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("cross_referenced", events[0].EventType)
	assert.Contains(events[0].MetadataJSON, `"source_owner":"acme"`)
	assert.Contains(events[0].MetadataJSON, `"source_repo":"tools"`)
	assert.Contains(events[0].MetadataJSON, `"source_number":9`)
	assert.Equal([]string{"1", "2"}, timelinePages)
}

func TestClientReadsEveryIssueCommentInOneResponse(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	var commentRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("token forgejo-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/issues/3/comments":
			commentRequests++
			assert.Empty(r.URL.Query().Get("page"))
			assert.Empty(r.URL.Query().Get("limit"))
			// A next link cannot select another page of this route.
			w.Header().Set("Link", `<https://codeberg.test/api/v1/repos/owner/repo/issues/3/comments?page=2>; rel="next"`)
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{
				{
					"id": 11, "body": "first note",
					"html_url":   "https://codeberg.test/owner/repo/issues/3#issuecomment-11",
					"user":       map[string]any{"login": "ada"},
					"created_at": "2026-05-01T10:00:00Z",
					"updated_at": "2026-05-01T10:00:00Z",
				},
				{
					"id": 12, "body": "second note",
					"html_url":   "https://codeberg.test/owner/repo/issues/3#issuecomment-12",
					"user":       map[string]any{"login": "bea"},
					"created_at": "2026-05-01T10:01:00Z",
					"updated_at": "2026-05-01T10:01:00Z",
				},
			}))
		case "/api/v1/repos/owner/repo/issues/3/timeline":
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient("codeberg.test", testTokenSource("forgejo-token"), WithBaseURLForTesting(server.URL), WithTransport(http.DefaultTransport))
	require.NoError(err)
	events, err := client.ListIssueEvents(t.Context(), platform.RepoRef{
		Host: "codeberg.test", Owner: "owner", Name: "repo", RepoPath: "owner/repo",
	}, 3)
	require.NoError(err)
	require.Len(events, 2)
	assert.Equal("issue_comment", events[0].EventType)
	assert.Equal("first note", events[0].Body)
	assert.Equal("ada", events[0].Author)
	assert.Equal("second note", events[1].Body)
	assert.Equal("bea", events[1].Author)
	assert.Equal(1, commentRequests)
}

func TestClientReadsForgejoActionsChecks(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	var sawStatuses, sawActions bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("token forgejo-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/commits/abc/statuses":
			sawStatuses = true
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{{
				"id": 31, "context": "build", "state": "success", "target_url": "https://ci.test/build",
			}}))
		case "/api/v1/repos/owner/repo/actions/runs":
			sawActions = true
			assert.Equal("abc", r.URL.Query().Get("head_sha"))
			assert.NoError(json.NewEncoder(w).Encode(map[string]any{
				"total_count": 1,
				"workflow_runs": []map[string]any{{
					"id": 41, "workflow_id": "ci.yml", "title": "CI", "status": "success",
					"commit_sha": "abc", "html_url": "https://forgejo.test/owner/repo/actions/runs/41",
				}},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient("forgejo.test", testTokenSource("forgejo-token"), WithBaseURLForTesting(server.URL), WithTransport(http.DefaultTransport))
	require.NoError(err)
	checks, err := client.ListCIChecks(t.Context(), platform.RepoRef{Owner: "owner", Name: "repo"}, "abc")
	require.NoError(err)

	assert.True(sawStatuses)
	assert.True(sawActions)
	assert.Len(checks, 2)
}

func TestClientReadsCommitStatusesWhenActionsEndpointUnavailable(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	testCases := []struct {
		name       string
		statusCode int
	}{
		{name: "not found", statusCode: http.StatusNotFound},
		{name: "method not allowed", statusCode: http.StatusMethodNotAllowed},
		{name: "not implemented", statusCode: http.StatusNotImplemented},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal("token forgejo-token", r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/repos/owner/repo/commits/abc/statuses":
					assert.NoError(json.NewEncoder(w).Encode([]map[string]any{{
						"id": 31, "context": "build", "status": "success", "target_url": "https://ci.test/build",
					}}))
				case "/api/v1/repos/owner/repo/actions/runs":
					assert.Equal("abc", r.URL.Query().Get("head_sha"))
					w.WriteHeader(testCase.statusCode)
					assert.NoError(json.NewEncoder(w).Encode(map[string]string{"message": "actions unavailable"}))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			client, err := NewClient("codeberg.test", testTokenSource("forgejo-token"), WithBaseURLForTesting(server.URL), WithTransport(http.DefaultTransport))
			require.NoError(err)
			ref := platform.RepoRef{Owner: "owner", Name: "repo"}

			checks, err := client.ListCIChecks(t.Context(), ref, "abc")

			require.NoError(err)
			require.Len(checks, 1)
			assert.Equal("build", checks[0].Name)
			assert.Equal("success", checks[0].Conclusion)
		})
	}
}

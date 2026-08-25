package gitea

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	giteasdk "code.gitea.io/sdk/gitea"
	"github.com/stretchr/testify/assert"
	Require "github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/platform/gitealike"
)

const testGiteaServerVersion = "1.26.0"

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
	_ platform.MergeRequestReviewThreadReader = (*Client)(nil)
)

func TestClientReviewThreadCapabilitiesUseValidatedVersionFloor(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		supported bool
	}{
		{name: "below floor", version: "1.24.5", supported: false},
		{name: "at floor", version: "1.24.6", supported: true},
		{name: "newer", version: "1.26.0", supported: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := Require.New(t)
			server := httptest.NewServer(http.NotFoundHandler())
			defer server.Close()
			client, err := NewClient(
				"gitea.test",
				testTokenSource("token"),
				WithBaseURLForTesting(server.URL),
				WithServerVersionForTesting(tt.version),
			)
			require.NoError(err)

			caps := client.Capabilities()
			assert.Equal(tt.supported, caps.ReadReviewThreads)
			assert.Equal(tt.supported, caps.Archive.InlineReviewComments)
			if !tt.supported {
				_, err = client.ListMergeRequestReviewThreads(
					t.Context(), platform.RepoRef{Owner: "owner", Name: "repo"}, 42,
				)
				assert.ErrorIs(err, platform.ErrUnsupportedCapability)
			}
		})
	}
}

func TestClientDiscoversVersionAtTestingBaseURL(t *testing.T) {
	assert := assert.New(t)
	require := Require.New(t)
	versionRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v1/version", r.URL.Path)
		versionRequested = true
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(json.NewEncoder(w).Encode(map[string]string{"version": "1.24.6"}))
	}))
	defer server.Close()

	client, err := NewClient(
		"gitea.test",
		testTokenSource("token"),
		WithBaseURLForTesting(server.URL),
	)
	require.NoError(err)
	assert.True(versionRequested)
	assert.True(client.Capabilities().ReadReviewThreads)
	assert.True(client.Capabilities().Archive.InlineReviewComments)
}

func TestClientReadsGiteaActionsChecks(t *testing.T) {
	assert := assert.New(t)
	require := Require.New(t)

	var sawStatuses, sawActions bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("token gitea-token", r.Header.Get("Authorization"))
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
					"id": 41, "display_title": "CI", "status": "completed", "conclusion": "success",
					"head_sha": "abc", "html_url": "https://gitea.test/owner/repo/actions/runs/41",
				}},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(
		"gitea.test", testTokenSource("gitea-token"),
		WithBaseURLForTesting(server.URL), WithServerVersionForTesting(testGiteaServerVersion),
	)
	require.NoError(err)
	checks, err := client.ListCIChecks(context.Background(), platform.RepoRef{Owner: "owner", Name: "repo"}, "abc")
	require.NoError(err)

	assert.True(sawStatuses)
	assert.True(sawActions)
	assert.Len(checks, 2)
}

func TestClientReadsTimelineAssignmentAndTitleEvents(t *testing.T) {
	assert := assert.New(t)
	require := Require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("token gitea-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/issues/3/comments":
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{{
				"id": 10, "body": "comment", "user": map[string]any{"login": "alice"},
				"created_at": "2026-05-01T10:00:00Z",
			}}))
		case "/api/v1/repos/owner/repo/pulls/3/reviews":
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{}))
		case "/api/v1/repos/owner/repo/pulls/3/commits":
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{}))
		case "/api/v1/repos/owner/repo/issues/3/timeline":
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{
				{
					"id": 11, "type": "assigned", "user": map[string]any{"login": "bob"},
					"label":      map[string]any{"id": 1, "name": "bug"},
					"created_at": "2026-05-01T10:01:00Z",
				},
				{
					"id": 12, "type": "change_title", "user": map[string]any{"login": "carol"},
					"old_title": "Old title", "new_title": "New title",
					"created_at": "2026-05-01T10:02:00Z",
				},
				{
					"id": 13, "type": "pull_ref", "user": map[string]any{"login": "dana"},
					"ref_action": "closes", "created_at": "2026-05-01T10:03:00Z",
					"ref_issue": map[string]any{
						"number": 9, "title": "Fix the issue",
						"html_url":     "https://gitea.test/acme/tools/pulls/9",
						"pull_request": map[string]any{"merged": false},
						"repository":   map[string]any{"owner": "acme", "name": "tools", "full_name": "acme/tools"},
					},
				},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient("gitea.test", testTokenSource("gitea-token"), WithBaseURLForTesting(server.URL), WithServerVersionForTesting(testGiteaServerVersion))
	require.NoError(err)
	ref := platform.RepoRef{Host: "gitea.test", Owner: "owner", Name: "repo", RepoPath: "owner/repo"}

	mrEvents, err := client.ListMergeRequestEvents(context.Background(), ref, 3)
	require.NoError(err)
	require.Len(mrEvents, 4)
	assert.Equal("issue_comment", mrEvents[0].EventType)
	assert.Equal("assigned", mrEvents[1].EventType)
	assert.Equal("bob", mrEvents[1].Author)
	assert.Equal("assigned someone", mrEvents[1].Summary)
	assert.Equal("renamed_title", mrEvents[2].EventType)
	assert.Equal("carol", mrEvents[2].Author)
	assert.Equal(`"Old title" -> "New title"`, mrEvents[2].Summary)
	assert.JSONEq(`{"previous_title":"Old title","current_title":"New title"}`, mrEvents[2].MetadataJSON)
	assert.Equal("cross_referenced", mrEvents[3].EventType)

	issueEvents, err := client.ListIssueEvents(context.Background(), ref, 3)
	require.NoError(err)
	require.Len(issueEvents, 4)
	assert.Equal("issue_comment", issueEvents[0].EventType)
	assert.Equal("assigned", issueEvents[1].EventType)
	assert.Equal("bob", issueEvents[1].Author)
	assert.Equal("assigned someone", issueEvents[1].Summary)
	assert.Equal("renamed_title", issueEvents[2].EventType)
	assert.Equal("carol", issueEvents[2].Author)
	assert.Equal(`"Old title" -> "New title"`, issueEvents[2].Summary)
	assert.JSONEq(`{"previous_title":"Old title","current_title":"New title"}`, issueEvents[2].MetadataJSON)
	assert.Equal("cross_referenced", issueEvents[3].EventType)
	assert.JSONEq(`{
		"source_type":"PullRequest",
		"source_owner":"acme",
		"source_repo":"tools",
		"source_number":9,
		"source_title":"Fix the issue",
		"source_url":"https://gitea.test/acme/tools/pulls/9",
		"is_cross_repository":true,
		"will_close_target":true
	}`, issueEvents[3].MetadataJSON)
}

func TestClientFallsBackToStatusesWhenActionsRequireNewerGitea(t *testing.T) {
	assert := assert.New(t)
	require := Require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/version":
			assert.NoError(json.NewEncoder(w).Encode(map[string]any{
				"version": "1.25.0",
			}))
		case "/api/v1/repos/owner/repo/commits/abc/statuses":
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{{
				"id": 31, "context": "build", "status": "success", "target_url": "https://ci.test/build",
			}}))
		case "/api/v1/repos/owner/repo/actions/runs":
			assert.Fail("older Gitea actions endpoint should not be called")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api, err := giteasdk.NewClient(
		server.URL,
		giteasdk.SetToken("gitea-token"),
		giteasdk.SetUserAgent("kenn-forge"),
	)
	require.NoError(err)
	provider := gitealike.NewProvider(
		platform.KindGitea,
		"gitea.test",
		&transport{api: api, requestContextLock: make(chan struct{}, 1)},
		gitealike.WithReadActions(),
	)

	checks, err := provider.ListCIChecks(
		context.Background(),
		platform.RepoRef{Owner: "owner", Name: "repo"},
		"abc",
	)
	require.NoError(err)
	require.Len(checks, 1)
	assert.Equal("build", checks[0].Name)
	assert.Equal("success", checks[0].Conclusion)
}

func TestClientRequestChangesSubmitsReview(t *testing.T) {
	assert := assert.New(t)
	require := Require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(http.MethodPost, r.Method)
		assert.Equal("/api/v1/repos/owner/repo/pulls/7/reviews", r.URL.Path)
		var body struct {
			Event    string `json:"event"`
			Body     string `json:"body"`
			CommitID string `json:"commit_id"`
		}
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal("REQUEST_CHANGES", body.Event)
		assert.Equal("needs work", body.Body)
		assert.Equal("reviewed-head", body.CommitID)
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(json.NewEncoder(w).Encode(map[string]any{
			"id": 41, "state": "REQUEST_CHANGES", "body": "needs work",
			"user": map[string]any{"login": "dana"}, "submitted_at": "2026-05-01T12:00:00Z",
		}))
	}))
	defer server.Close()

	client, err := NewClient("gitea.test", testTokenSource("gitea-token"), WithBaseURLForTesting(server.URL), WithServerVersionForTesting(testGiteaServerVersion))
	require.NoError(err)
	require.NoError(client.RequestChanges(
		context.Background(), platform.RepoRef{Owner: "owner", Name: "repo"},
		7, "needs work", "reviewed-head",
	))
}

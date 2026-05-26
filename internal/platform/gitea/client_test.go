package gitea

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	giteasdk "code.gitea.io/sdk/gitea"
	Assert "github.com/stretchr/testify/assert"
	Require "github.com/stretchr/testify/require"
	ghsync "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/platform/gitealike"
)

var (
	_ gitealike.Transport         = (*transport)(nil)
	_ gitealike.ActionsTransport  = (*transport)(nil)
	_ platform.RepositoryReader   = (*Client)(nil)
	_ platform.MergeRequestReader = (*Client)(nil)
	_ platform.IssueReader        = (*Client)(nil)
	_ platform.ReleaseReader      = (*Client)(nil)
	_ platform.TagReader          = (*Client)(nil)
	_ platform.CIReader           = (*Client)(nil)
	_ platform.CommentMutator     = (*Client)(nil)
	_ platform.StateMutator       = (*Client)(nil)
	_ platform.MergeMutator       = (*Client)(nil)
	_ platform.ReviewMutator      = (*Client)(nil)
	_ platform.IssueMutator       = (*Client)(nil)
)

func TestClientLooksUpRepositoryAndSendsToken(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(http.MethodGet, r.Method)
		assert.Equal("/api/v1/repos/owner/repo", r.URL.Path)
		assert.Equal("token gitea-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(json.NewEncoder(w).Encode(map[string]any{
			"id":        1,
			"name":      "repo",
			"full_name": "owner/repo",
			"owner": map[string]any{
				"id":         2,
				"login":      "owner",
				"full_name":  "Owner",
				"avatar_url": "",
				"html_url":   "",
			},
		}))
	}))
	defer server.Close()

	client, err := NewClient(
		"gitea.test",
		"gitea-token",
		WithBaseURLForTesting(server.URL),
	)
	require.NoError(err)

	repo, err := client.transport.getRepositoryRaw(context.Background(), "owner", "repo")
	require.NoError(err)
	assert.Equal("repo", repo.Name)
}

func TestClientLookupUsesForegroundTimeout(t *testing.T) {
	require := Require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewClient(
		"gitea.test",
		"gitea-token",
		WithBaseURLForTesting(server.URL),
		WithForegroundTimeoutForTesting(20*time.Millisecond),
	)
	require.NoError(err)

	_, err = client.transport.getRepositoryRaw(context.Background(), "owner", "repo")
	require.Error(err)
}

func TestTransportGetRepositoryRawCancelsInFlightRequest(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	requestStarted := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v1/repos/owner/repo", r.URL.Path)
		close(requestStarted)
		<-r.Context().Done()
	}))
	defer server.Close()

	client, err := NewClient(
		"gitea.test",
		"gitea-token",
		WithBaseURLForTesting(server.URL),
		WithForegroundTimeoutForTesting(time.Minute),
	)
	require.NoError(err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := client.transport.getRepositoryRaw(ctx, "owner", "repo")
		done <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		require.FailNow("request did not start")
	}
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(err, context.Canceled)
	case <-time.After(time.Second):
		require.FailNow("request was not canceled")
	}
}

func TestTransportGetRepositoryRawCancelsWhileWaitingForRequestContext(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v1/repos/owner/repo", r.URL.Path)
		close(requestStarted)
		<-releaseRequest
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(json.NewEncoder(w).Encode(map[string]any{
			"id":        1,
			"name":      "repo",
			"full_name": "owner/repo",
			"owner": map[string]any{
				"id":    2,
				"login": "owner",
			},
		}))
	}))
	defer server.Close()

	client, err := NewClient(
		"gitea.test",
		"gitea-token",
		WithBaseURLForTesting(server.URL),
		WithForegroundTimeoutForTesting(time.Minute),
	)
	require.NoError(err)
	firstDone := make(chan error, 1)
	go func() {
		_, err := client.transport.getRepositoryRaw(context.Background(), "owner", "repo")
		firstDone <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		require.FailNow("request did not start")
	}

	waitingCtx, cancelWaiting := context.WithCancel(context.Background())
	waitingDone := make(chan error, 1)
	go func() {
		_, err := client.transport.getRepositoryRaw(waitingCtx, "owner", "repo")
		waitingDone <- err
	}()
	cancelWaiting()

	select {
	case err := <-waitingDone:
		require.ErrorIs(err, context.Canceled)
	case <-time.After(time.Second):
		require.FailNow("waiting request was not canceled")
	}

	close(releaseRequest)
	select {
	case err := <-firstDone:
		require.NoError(err)
	case <-time.After(time.Second):
		require.FailNow("first request did not finish")
	}
}

func TestClientLookupCountsSyncBudget(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(json.NewEncoder(w).Encode(map[string]any{
			"id":        1,
			"name":      "repo",
			"full_name": "owner/repo",
			"owner":     map[string]any{"login": "owner"},
		}))
	}))
	defer server.Close()

	budget := ghsync.NewSyncBudget(20)
	client, err := NewClient(
		"gitea.test",
		"gitea-token",
		WithBaseURLForTesting(server.URL),
		WithSyncBudget(budget),
	)
	require.NoError(err)

	_, err = client.transport.getRepositoryRaw(
		ghsync.WithSyncBudget(context.Background()),
		"owner",
		"repo",
	)
	require.NoError(err)
	require.Equal(1, budget.Spent())
}

func TestClientProviderIdentityExposesReadCapabilities(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	client, err := NewClient(
		"gitea.test",
		"gitea-token",
		WithBaseURLForTesting(server.URL),
	)
	require.NoError(err)

	assert.Equal(platform.KindGitea, client.Platform())
	assert.Equal("gitea.test", client.Host())
	assert.Equal(platform.Capabilities{
		ReadRepositories:  true,
		ReadMergeRequests: true,
		ReadIssues:        true,
		ReadComments:      true,
		ReadReleases:      true,
		ReadCI:            true,
		CommentMutation:   true,
		StateMutation:     true,
		MergeMutation:     true,
		ReviewMutation:    true,
		IssueMutation:     true,
	}, client.Capabilities())
}

func TestClientReadsOpenPullRequestsIssuesAndCIChecks(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)

	var sawPulls, sawIssues, sawStatuses, sawActions bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("token gitea-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/repos/owner/repo/pulls":
			sawPulls = true
			assert.Equal("open", r.URL.Query().Get("state"))
			assert.Equal("1", r.URL.Query().Get("page"))
			assert.Equal("100", r.URL.Query().Get("limit"))
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{{
				"id": 11, "number": 3, "url": "https://gitea.test/owner/repo/pulls/3",
				"html_url": "https://gitea.test/owner/repo/pulls/3", "mergeable": false,
				"title": "review me", "state": "open", "user": map[string]any{"login": "alice"},
				"head": map[string]any{"ref": "feature", "sha": "abc"},
				"base": map[string]any{"ref": "main", "sha": "def"},
			}}))
		case "/api/v1/repos/owner/repo/issues":
			sawIssues = true
			assert.Equal("open", r.URL.Query().Get("state"))
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{{
				"id": 21, "number": 4, "url": "https://gitea.test/owner/repo/issues/4",
				"title": "bug", "state": "open", "user": map[string]any{"login": "bob"},
			}}))
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

	client, err := NewClient("gitea.test", "gitea-token", WithBaseURLForTesting(server.URL))
	require.NoError(err)
	ref := platform.RepoRef{Owner: "owner", Name: "repo"}

	mrs, err := client.ListOpenMergeRequests(context.Background(), ref)
	require.NoError(err)
	issues, err := client.ListOpenIssues(context.Background(), ref)
	require.NoError(err)
	checks, err := client.ListCIChecks(context.Background(), ref, "abc")
	require.NoError(err)

	assert.True(sawPulls)
	assert.True(sawIssues)
	assert.True(sawStatuses)
	assert.True(sawActions)
	assert.Len(mrs, 1)
	assert.Equal("dirty", mrs[0].MergeableState)
	assert.Len(issues, 1)
	assert.Len(checks, 2)
}

func TestClientReadsTimelineAssignmentAndTitleEvents(t *testing.T) {
	assert := Assert.New(t)
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
					"created_at": "2026-05-01T10:01:00Z",
				},
				{
					"id": 12, "type": "change_title", "user": map[string]any{"login": "carol"},
					"old_title": "Old title", "new_title": "New title",
					"created_at": "2026-05-01T10:02:00Z",
				},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient("gitea.test", "gitea-token", WithBaseURLForTesting(server.URL))
	require.NoError(err)
	ref := platform.RepoRef{Owner: "owner", Name: "repo", RepoPath: "owner/repo"}

	mrEvents, err := client.ListMergeRequestEvents(context.Background(), ref, 3)
	require.NoError(err)
	require.Len(mrEvents, 3)
	assert.Equal("issue_comment", mrEvents[0].EventType)
	assert.Equal("assigned", mrEvents[1].EventType)
	assert.Equal("bob", mrEvents[1].Author)
	assert.Equal("assigned someone", mrEvents[1].Summary)
	assert.Equal("renamed_title", mrEvents[2].EventType)
	assert.Equal("carol", mrEvents[2].Author)
	assert.Equal(`"Old title" -> "New title"`, mrEvents[2].Summary)
	assert.JSONEq(`{"previous_title":"Old title","current_title":"New title"}`, mrEvents[2].MetadataJSON)

	issueEvents, err := client.ListIssueEvents(context.Background(), ref, 3)
	require.NoError(err)
	require.Len(issueEvents, 3)
	assert.Equal("issue_comment", issueEvents[0].EventType)
	assert.Equal("assigned", issueEvents[1].EventType)
	assert.Equal("bob", issueEvents[1].Author)
	assert.Equal("assigned someone", issueEvents[1].Summary)
	assert.Equal("renamed_title", issueEvents[2].EventType)
	assert.Equal("carol", issueEvents[2].Author)
	assert.Equal(`"Old title" -> "New title"`, issueEvents[2].Summary)
	assert.JSONEq(`{"previous_title":"Old title","current_title":"New title"}`, issueEvents[2].MetadataJSON)
}

func TestClientFallsBackToStatusesWhenActionsRequireNewerGitea(t *testing.T) {
	assert := Assert.New(t)
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
		giteasdk.SetUserAgent("middleman"),
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

func TestClientMutationCapabilityUsesGiteaEndpoints(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("token gitea-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/repos/owner/repo/issues/7/comments",
			"PATCH /api/v1/repos/owner/repo/issues/comments/10":
			assert.NoError(json.NewEncoder(w).Encode(map[string]any{
				"id": 10, "body": "comment", "user": map[string]any{"login": "alice"},
			}))
		case "POST /api/v1/repos/owner/repo/issues",
			"PATCH /api/v1/repos/owner/repo/issues/8":
			assert.NoError(json.NewEncoder(w).Encode(map[string]any{
				"id": 20, "number": 8, "title": "issue", "state": "closed", "user": map[string]any{"login": "bob"},
			}))
		case "PATCH /api/v1/repos/owner/repo/pulls/7":
			assert.NoError(json.NewEncoder(w).Encode(map[string]any{
				"id": 30, "number": 7, "title": "pr", "state": "closed", "user": map[string]any{"login": "carol"},
				"head": map[string]any{"ref": "feature", "sha": "abc"},
				"base": map[string]any{"ref": "main", "sha": "def"},
			}))
		case "POST /api/v1/repos/owner/repo/pulls/7/merge":
			assert.NoError(json.NewEncoder(w).Encode(map[string]any{}))
		case "POST /api/v1/repos/owner/repo/pulls/7/reviews":
			assert.NoError(json.NewEncoder(w).Encode(map[string]any{
				"id": 40, "state": "APPROVED", "body": "ship it", "user": map[string]any{"login": "dana"},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient("gitea.test", "gitea-token", WithBaseURLForTesting(server.URL))
	require.NoError(err)
	ref := platform.RepoRef{Owner: "owner", Name: "repo"}

	_, err = client.CreateMergeRequestComment(context.Background(), ref, 7, "comment")
	require.NoError(err)
	_, err = client.EditIssueComment(context.Background(), ref, 8, 10, "comment")
	require.NoError(err)
	_, err = client.CreateIssue(context.Background(), ref, "issue", "body")
	require.NoError(err)
	_, err = client.SetIssueState(context.Background(), ref, 8, "closed")
	require.NoError(err)
	_, err = client.SetMergeRequestState(context.Background(), ref, 7, "closed")
	require.NoError(err)
	prTitle := "pr"
	prBody := "body"
	_, err = client.EditMergeRequestContent(context.Background(), ref, 7, &prTitle, &prBody)
	require.NoError(err)
	_, err = client.MergeMergeRequest(context.Background(), ref, 7, "title", "message", "squash")
	require.NoError(err)
	_, err = client.ApproveMergeRequest(context.Background(), ref, 7, "ship it")
	require.NoError(err)

	assert.Equal([]string{
		"POST /api/v1/repos/owner/repo/issues/7/comments",
		"PATCH /api/v1/repos/owner/repo/issues/comments/10",
		"POST /api/v1/repos/owner/repo/issues",
		"PATCH /api/v1/repos/owner/repo/issues/8",
		"PATCH /api/v1/repos/owner/repo/pulls/7",
		"PATCH /api/v1/repos/owner/repo/pulls/7",
		"POST /api/v1/repos/owner/repo/pulls/7/merge",
		"POST /api/v1/repos/owner/repo/pulls/7/reviews",
	}, seen)
}

func TestClientMapsNotFoundResponsesToPlatformError(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v1/repos/owner/repo/pulls/99", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		assert.NoError(json.NewEncoder(w).Encode(map[string]string{"message": "not found"}))
	}))
	defer server.Close()

	client, err := NewClient("gitea.test", "gitea-token", WithBaseURLForTesting(server.URL))
	require.NoError(err)

	_, err = client.GetMergeRequest(
		context.Background(),
		platform.RepoRef{Owner: "owner", Name: "repo"},
		99,
	)
	require.Error(err)
	assert.ErrorIs(err, platform.ErrNotFound)
}

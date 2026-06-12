package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/ratelimit"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestClientLooksUpProjectByRawPathAndUsesNumericIDAfterLookup(t *testing.T) {
	assert := assert.New(t)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/group%2Fsubgroup%2Fproject":
			writeJSON(w, `{
				"id": 42,
				"path": "project",
				"path_with_namespace": "group/subgroup/project",
				"name": "Project",
				"description": "tracked",
				"default_branch": "main",
				"web_url": "https://gitlab.example.com/group/subgroup/project",
				"http_url_to_repo": "https://gitlab.example.com/group/subgroup/project.git",
				"created_at": "2026-04-01T10:00:00Z",
				"updated_at": "2026-04-02T10:00:00Z"
			}`)
		case "/api/v4/projects/42/merge_requests":
			assert.Equal("opened", r.URL.Query().Get("state"))
			assert.Equal("true", r.URL.Query().Get("with_merge_status_recheck"))
			writeJSON(w, `[{"id": 1001, "iid": 7, "project_id": 42, "title": "Use ids", "state": "opened"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ref := platform.RepoRef{Platform: platform.KindGitLab, Host: "gitlab.example.com", RepoPath: "group/subgroup/project"}

	repo, err := client.GetRepository(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(int64(42), repo.Ref.PlatformID)
	assert.Equal("group/subgroup", repo.Ref.Owner)
	assert.Equal("project", repo.Ref.Name)

	mrs, err := client.ListOpenMergeRequests(context.Background(), repo.Ref)
	require.NoError(t, err)
	require.Len(t, mrs, 1)
	assert.Equal(7, mrs[0].Number)
	assert.Equal([]string{
		"/api/v4/projects/group%2Fsubgroup%2Fproject",
		"/api/v4/projects/42/merge_requests",
	}, paths)
}

func TestClientRecordsRateLimitRequests(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)

	resetAt := time.Now().Add(30 * time.Minute).Unix()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Limit", "600")
		w.Header().Set("RateLimit-Remaining", "599")
		w.Header().Set("RateLimit-Reset", strconv.FormatInt(resetAt, 10))
		writeJSON(w, `{
			"id": 42,
			"path": "project",
			"path_with_namespace": "group/project",
			"name": "Project"
		}`)
	}))
	defer server.Close()

	rt := ratelimit.NewPlatformRateTracker(database, "gitlab", "gitlab.example.com", "rest")
	client := newTestClient(t, server.URL, WithRateTracker(rt))
	_, err := client.GetRepository(context.Background(), platform.RepoRef{
		Platform: platform.KindGitLab,
		Host:     "gitlab.example.com",
		RepoPath: "group/project",
	})
	require.NoError(err)

	row, err := database.GetPlatformRateLimit("gitlab", "gitlab.example.com", "rest")
	require.NoError(err)
	require.NotNil(row)
	assert.Equal("gitlab", row.Platform)
	assert.Equal(1, row.RequestsHour)
	assert.Equal(600, row.RateLimit)
	assert.Equal(599, row.RateRemaining)
	require.NotNil(row.RateResetAt)
	assert.Equal(resetAt, row.RateResetAt.Unix())
}

func TestClientReadsTokenSourceForEachRequest(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	source := newMutableTestTokenSource("first-token")
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens = append(tokens, r.Header.Get("Private-Token"))
		assert.Equal("/api/v4/projects/group%2Fproject", r.URL.EscapedPath())
		writeJSON(w, `{
			"id": 42,
			"path": "project",
			"path_with_namespace": "group/project",
			"name": "Project"
		}`)
	}))
	defer server.Close()

	client, err := NewClient(
		"gitlab.example.com",
		source,
		WithBaseURLForTesting(server.URL+"/api/v4"),
	)
	require.NoError(err)
	ref := platform.RepoRef{
		Platform: platform.KindGitLab,
		Host:     "gitlab.example.com",
		RepoPath: "group/project",
	}

	_, err = client.GetRepository(context.Background(), ref)
	require.NoError(err)
	source.token = "second-token"
	_, err = client.GetRepository(context.Background(), ref)
	require.NoError(err)

	assert.Equal([]string{"first-token", "second-token"}, tokens)
}

func TestClientRejectsAlreadyEscapedProjectPathBeforeDoubleEscaping(t *testing.T) {
	client := newTestClient(t, "http://127.0.0.1")

	_, err := client.GetRepository(context.Background(), platform.RepoRef{
		Platform: platform.KindGitLab,
		Host:     "gitlab.example.com",
		RepoPath: "group%2Fproject",
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, platform.ErrInvalidRepoRef)
}

func TestPreviewNamespaceUsesGroupFirstFallbackPaginatesAndFiltersArchived(t *testing.T) {
	assert := assert.New(t)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		assert.Equal("false", r.URL.Query().Get("archived"))
		assert.Equal("true", r.URL.Query().Get("include_subgroups"))
		switch r.URL.Query().Get("page") {
		case "", "1":
			w.Header().Set("X-Next-Page", "2")
			writeJSON(w, `[
				{"id": 1, "path": "one", "path_with_namespace": "middleman/one", "archived": false},
				{"id": 2, "path": "old", "path_with_namespace": "middleman/old", "archived": true}
			]`)
		case "2":
			writeJSON(w, `[{"id": 3, "path": "two", "path_with_namespace": "middleman/subgroup/two", "archived": false}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	preview, err := client.PreviewNamespace(context.Background(), "middleman", PreviewOptions{Limit: 10})
	require.NoError(t, err)

	require.Len(t, preview.Repositories, 2)
	assert.Equal(3, preview.ScannedCount)
	assert.Equal(2, preview.ReturnedCount)
	assert.False(preview.Truncated)
	assert.Empty(preview.PartialErrors)
	assert.Equal([]string{"middleman/one", "middleman/subgroup/two"}, []string{
		preview.Repositories[0].Ref.RepoPath,
		preview.Repositories[1].Ref.RepoPath,
	})
	assert.Equal([]string{
		"/api/v4/groups/middleman/projects",
		"/api/v4/groups/middleman/projects",
	}, paths)
}

func TestPreviewNamespaceFallsBackToUserProjectsAfterGroupNotFound(t *testing.T) {
	assert := assert.New(t)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		switch r.URL.EscapedPath() {
		case "/api/v4/groups/alice/projects":
			http.NotFound(w, r)
		case "/api/v4/users/alice/projects":
			writeJSON(w, `[
				{"id": 10, "path": "tool", "path_with_namespace": "alice/tool", "archived": false},
				{"id": 11, "path": "foreign", "path_with_namespace": "someone/foreign", "archived": false}
			]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	preview, err := client.PreviewNamespace(context.Background(), "alice", PreviewOptions{})
	require.NoError(t, err)

	require.Len(t, preview.Repositories, 1)
	assert.Equal("alice/tool", preview.Repositories[0].Ref.RepoPath)
	assert.Equal(2, preview.ScannedCount)
	assert.Equal(1, preview.ReturnedCount)
	assert.Equal([]string{"/api/v4/groups/alice/projects", "/api/v4/users/alice/projects"}, paths)
}

func TestPreviewNamespaceHonorsCancellationAndForegroundTimeout(t *testing.T) {
	t.Run("canceled context returns before request", func(t *testing.T) {
		requested := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requested = true
			writeJSON(w, `[]`)
		}))
		defer server.Close()

		client := newTestClient(t, server.URL)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := client.PreviewNamespace(ctx, "middleman", PreviewOptions{})

		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
		assert.False(t, requested)
	})

	t.Run("foreground timeout cancels slow request", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
			writeJSON(w, `[]`)
		}))
		defer server.Close()

		client := newTestClient(t, server.URL, WithForegroundTimeoutForTesting(time.Nanosecond))
		_, err := client.PreviewNamespace(context.Background(), "middleman", PreviewOptions{})

		require.Error(t, err)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestPreviewNamespaceTruncatesAtLimitAndCapsAtHardLimit(t *testing.T) {
	assert := assert.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit, err := strconv.Atoi(r.URL.Query().Get("per_page"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if limit > maxPreviewLimit {
			http.Error(w, "limit too high", http.StatusBadRequest)
			return
		}

		writeJSON(w, `[
			{"id": 1, "path": "one", "path_with_namespace": "middleman/one"},
			{"id": 2, "path": "two", "path_with_namespace": "middleman/two"},
			{"id": 3, "path": "three", "path_with_namespace": "middleman/three"}
		]`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	preview, err := client.PreviewNamespace(context.Background(), "middleman", PreviewOptions{Limit: 2_000})
	require.NoError(t, err)

	assert.Equal(maxPreviewLimit, preview.Limit)
	assert.Equal(3, preview.ScannedCount)
	assert.Equal(3, preview.ReturnedCount)
	assert.True(preview.Truncated)
}

func TestPreviewNamespaceReturnsPartialMetadataAfterLaterPageFailure(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "", "1":
			w.Header().Set("X-Next-Page", "2")
			writeJSON(w, `[{"id": 1, "path": "one", "path_with_namespace": "middleman/one"}]`)
		case "2":
			http.Error(w, "temporary upstream failure", http.StatusTeapot)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	preview, err := client.PreviewNamespace(context.Background(), "middleman", PreviewOptions{Limit: 10})
	require.NoError(err)

	require.Len(preview.Repositories, 1)
	assert.True(preview.Truncated)
	require.Len(preview.PartialErrors, 1)
	assert.Equal("middleman", preview.PartialErrors[0].Namespace)
	assert.Equal(int64(2), preview.PartialErrors[0].Page)
	assert.Equal("upstream_error", preview.PartialErrors[0].Code)
}

func TestReadClientFetchesMergeRequestsIssuesEventsReleasesTagsAndPipelines(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42/merge_requests":
			assert.Equal("true", r.URL.Query().Get("with_merge_status_recheck"))
			if r.URL.Query().Get("page") == "2" {
				writeJSON(w, `[]`)
				return
			}
			w.Header().Set("X-Next-Page", "2")
			writeJSON(w, `[{"id": 1001, "iid": 7, "project_id": 42, "title": "MR one", "state": "opened", "pipeline": {"id": 501, "status": "running"}}]`)
		case "/api/v4/projects/42/merge_requests/7":
			writeJSON(w, `{"id": 1001, "iid": 7, "project_id": 42, "title": "MR detail", "state": "opened", "source_branch": "feature", "target_branch": "main", "sha": "abc", "draft": false, "work_in_progress": true, "pipeline": {"id": 501, "status": "success"}}`)
		case "/api/v4/projects/42/merge_requests/7/discussions":
			writeJSON(w, `[
				{"id": "disc1", "notes": [
					{"id": 1, "body": "visible", "system": false, "author": {"username": "alice"}, "created_at": "2026-04-01T10:00:00Z"},
					{"id": 2, "body": "merged", "system": true, "author": {"username": "maintainer"}, "created_at": "2026-04-01T10:30:00Z"}
				]}
			]`)
		case "/api/v4/projects/42/merge_requests/7/commits":
			writeJSON(w, `[{"id": "abcdef123456", "title": "commit title", "message": "commit body", "author_name": "Alice", "created_at": "2026-04-01T09:00:00Z"}]`)
		case "/api/v4/projects/42/issues":
			writeJSON(w, `[{"id": 2001, "iid": 5, "project_id": 42, "title": "Issue one", "state": "opened", "user_notes_count": 2}]`)
		case "/api/v4/projects/42/issues/5":
			writeJSON(w, `{"id": 2001, "iid": 5, "project_id": 42, "title": "Issue detail", "state": "opened"}`)
		case "/api/v4/projects/42/issues/5/discussions":
			writeJSON(w, `[
				{"id": "disc10", "notes": [{"id": 10, "body": "issue note", "system": false, "author": {"username": "bob"}, "created_at": "2026-04-02T10:00:00Z"}]}
			]`)
		case "/api/v4/projects/42/releases":
			writeJSON(w, `[{"tag_name": "v1.0.0", "name": "One", "released_at": "2026-04-03T10:00:00Z", "created_at": "2026-04-03T09:00:00Z", "commit": {"id": "abc"}}]`)
		case "/api/v4/projects/42/repository/tags":
			writeJSON(w, `[{"name": "v1.0.0", "target": "abc", "commit": {"web_url": "https://gitlab.example.com/project/-/commit/abc"}}]`)
		case "/api/v4/projects/42/pipelines":
			writeJSON(w, `[{"id": 501, "sha": "abc", "status": "running", "web_url": "https://gitlab.example.com/project/-/pipelines/501"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ref := platform.RepoRef{Platform: platform.KindGitLab, Host: "gitlab.example.com", RepoPath: "middleman/project", PlatformID: 42}

	mrs, err := client.ListOpenMergeRequests(context.Background(), ref)
	require.NoError(err)
	require.Len(mrs, 1)
	assert.Equal(7, mrs[0].Number)
	assert.Empty(mrs[0].CIStatus)

	mr, err := client.GetMergeRequest(context.Background(), ref, 7)
	require.NoError(err)
	assert.Equal("MR detail", mr.Title)
	assert.True(mr.IsDraft)
	assert.Equal("success", mr.CIStatus)

	mrEvents, err := client.ListMergeRequestEvents(context.Background(), ref, 7)
	require.NoError(err)
	require.Len(mrEvents, 3)
	assert.Equal("issue_comment", mrEvents[0].EventType)
	assert.Equal("merged", mrEvents[1].EventType)
	assert.Equal("maintainer", mrEvents[1].Author)
	assert.Equal("commit", mrEvents[2].EventType)

	issues, err := client.ListOpenIssues(context.Background(), ref)
	require.NoError(err)
	require.Len(issues, 1)
	assert.Equal(5, issues[0].Number)

	issue, err := client.GetIssue(context.Background(), ref, 5)
	require.NoError(err)
	assert.Equal("Issue detail", issue.Title)

	issueEvents, err := client.ListIssueEvents(context.Background(), ref, 5)
	require.NoError(err)
	require.Len(issueEvents, 1)
	assert.Equal("issue_comment", issueEvents[0].EventType)

	releases, err := client.ListReleases(context.Background(), ref)
	require.NoError(err)
	require.Len(releases, 1)
	assert.Equal("v1.0.0", releases[0].TagName)

	tags, err := client.ListTags(context.Background(), ref)
	require.NoError(err)
	require.Len(tags, 1)
	assert.Equal("abc", tags[0].SHA)

	checks, err := client.ListCIChecks(context.Background(), ref, "abc")
	require.NoError(err)
	require.Len(checks, 1)
	assert.Equal("in_progress", checks[0].Status)
	assert.Empty(checks[0].Conclusion)
}

func TestReadClientSeparatesDiscussionEventsFromReviewThreads(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	discussionsJSON := `[
		{
			"id": "discussion-1",
			"notes": [
				{
					"id": 101,
					"body": "inline note",
					"system": false,
					"author": {"username": "reviewer"},
					"resolvable": true,
					"resolved": false,
					"created_at": "2026-05-10T12:00:00Z",
					"updated_at": "2026-05-10T12:00:00Z",
					"position": {
						"base_sha": "base",
						"start_sha": "base",
						"head_sha": "head",
						"position_type": "text",
						"new_path": "src/main.go",
						"new_line": 9
					}
				},
				{
					"id": 102,
					"body": "discussion reply",
					"system": false,
					"author": {"username": "author"},
					"resolvable": true,
					"resolved": false,
					"created_at": "2026-05-10T12:01:00Z",
					"updated_at": "2026-05-10T12:01:00Z"
				}
			]
		}
	]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42/merge_requests/7/discussions":
			writeJSON(w, discussionsJSON)
		case "/api/v4/projects/42/merge_requests/7/commits":
			writeJSON(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ref := platform.RepoRef{Platform: platform.KindGitLab, Host: "gitlab.example.com", PlatformID: 42}

	events, err := client.ListMergeRequestEvents(context.Background(), ref, 7)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("issue_comment", events[0].EventType)
	assert.Equal("discussion reply", events[0].Body)
	assert.Equal("discussion-1", events[0].ThreadID)

	threads, err := client.ListMergeRequestReviewThreads(context.Background(), ref, 7)
	require.NoError(err)
	require.Len(threads, 1)
	assert.Equal("discussion-1", threads[0].ProviderThreadID)
	assert.Equal("101", threads[0].ProviderCommentID)
	assert.Equal("inline note", threads[0].Body)
	assert.Equal("src/main.go", threads[0].Range.Path)
	assert.Equal(9, threads[0].Range.Line)
}

func TestListCIChecksReturnsEmptyWhenNoPipelineExists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v4/projects/42/pipelines", r.URL.EscapedPath())
		writeJSON(w, `[]`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	checks, err := client.ListCIChecks(context.Background(), platform.RepoRef{
		Platform:   platform.KindGitLab,
		Host:       "gitlab.example.com",
		RepoPath:   "middleman/project",
		PlatformID: 42,
	}, "missing")

	require.NoError(t, err)
	assert.Empty(t, checks)
}

func TestSelfHostedBaseURLConstruction(t *testing.T) {
	client, err := NewClient("gitlab.example.com:8443", testTokenSource("token"))
	require.NoError(t, err)

	assert.Equal(t, "https://gitlab.example.com:8443/api/v4", client.baseURL)
}

func newTestClient(t *testing.T, serverURL string, opts ...ClientOption) *Client {
	t.Helper()
	allOpts := append([]ClientOption{WithBaseURLForTesting(serverURL + "/api/v4")}, opts...)
	client, err := NewClient("gitlab.example.com", testTokenSource("token"), allOpts...)
	require.NoError(t, err)
	return client
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, body)
}

func TestMapGitLabError(t *testing.T) {
	err := mapGitLabError("get_repository", errors.New("plain failure"))

	var platformErr *platform.Error
	require.ErrorAs(t, err, &platformErr)
	assert.Equal(t, platform.ErrCodeInvalidRepoRef, platformErr.Code)
	assert.Equal(t, "get_repository", platformErr.Capability)
}

func TestProjectPathRejectsEscapedSlashVariants(t *testing.T) {
	badPaths := []string{
		"group%2Fproject",
		"group%2fproject",
		"group%252Fsubgroup/project",
		"group%252fsubgroup/project",
		url.PathEscape("group/project"),
	}
	for _, path := range badPaths {
		t.Run(path, func(t *testing.T) {
			_, err := rawProjectPath(platform.RepoRef{RepoPath: path})
			require.Error(t, err)
			require.ErrorIs(t, err, platform.ErrInvalidRepoRef)
		})
	}
}

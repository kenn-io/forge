package gitlab

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ghsync "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
)

func TestGitLabArchiveInventoryUsesAllStatesOldestFirstAndBoundedPages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal("all", r.URL.Query().Get("state"))
		assert.Equal("asc", r.URL.Query().Get("sort"))
		assert.Equal("100", r.URL.Query().Get("per_page"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42/issues":
			assert.Equal("created_at", r.URL.Query().Get("order_by"))
			if r.URL.Query().Get("page") == "1" {
				w.Header().Set("X-Next-Page", "2")
			}
			writeJSON(w, `[{"id":101,"iid":1,"title":"issue","state":"closed","web_url":"https://gitlab.example.com/group/project/-/issues/1","author":{"username":"ada"},"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-02T00:00:00Z"}]`)
		case "/api/v4/projects/42/merge_requests":
			assert.Equal("created_at", r.URL.Query().Get("order_by"))
			writeJSON(w, `[{"id":201,"iid":2,"title":"merge request","state":"merged","web_url":"https://gitlab.example.com/group/project/-/merge_requests/2","author":{"username":"lin"},"created_at":"2025-01-03T00:00:00Z","updated_at":"2025-01-04T00:00:00Z"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabArchiveTestRef()

	issues, err := client.ListHistoricalIssues(t.Context(), ref, "")
	require.NoError(err)
	require.Len(issues.Items, 1)
	assert.Equal(1, issues.Items[0].Number)
	assert.False(issues.Exhausted)
	assert.NotEmpty(issues.NextCursor)
	issues2, err := client.ListHistoricalIssues(t.Context(), ref, issues.NextCursor)
	require.NoError(err)
	assert.True(issues2.Exhausted)

	mrs, err := client.ListHistoricalMergeRequests(t.Context(), ref, "")
	require.NoError(err)
	require.Len(mrs.Items, 1)
	assert.Equal(2, mrs.Items[0].Number)
	assert.True(mrs.Exhausted)
	assert.Equal(3, requests)
}

func TestGitLabArchiveUpdatedInventoryUsesInclusiveWatermark(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	watermark := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		assert.Equal("updated_at", r.URL.Query().Get("order_by"))
		assert.Equal("desc", r.URL.Query().Get("sort"))
		updatedAfter, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("updated_after"))
		assert.NoError(err)
		assert.True(updatedAfter.Before(watermark), "updated_after must overlap an inclusive watermark")
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			w.Header().Set("X-Next-Page", "2")
		}
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42/issues":
			writeJSON(w, `[{"id":101,"iid":1,"title":"issue","state":"opened","created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T02:03:04Z"}]`)
		case "/api/v4/projects/42/merge_requests":
			writeJSON(w, `[{"id":201,"iid":2,"title":"mr","state":"opened","created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T02:03:04Z"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabArchiveTestRef()

	issues, err := client.ListUpdatedIssues(t.Context(), ref, watermark, "")
	require.NoError(err)
	_, err = client.ListUpdatedIssues(t.Context(), ref, watermark.Add(time.Second), issues.NextCursor)
	require.ErrorIs(err, platform.ErrProviderContract)
	otherRef := ref
	otherRef.Name = "other"
	otherRef.RepoPath = "group/other"
	_, err = client.ListUpdatedIssues(t.Context(), otherRef, watermark, issues.NextCursor)
	require.ErrorIs(err, platform.ErrProviderContract)
	otherHost := ref
	otherHost.Host = "other.gitlab.example.com"
	_, err = client.ListUpdatedIssues(t.Context(), otherHost, watermark, issues.NextCursor)
	require.ErrorIs(err, platform.ErrProviderContract)
	_, err = client.ListUpdatedIssues(t.Context(), ref, watermark, issues.NextCursor)
	require.NoError(err)
	mrs, err := client.ListUpdatedMergeRequests(t.Context(), ref, watermark, "")
	require.NoError(err)
	assert.Equal(watermark, issues.Items[0].UpdatedAt)
	assert.Equal(watermark, mrs.Items[0].UpdatedAt)
	assert.Equal(3, requests)
}

func TestGitLabArchiveUpdatedInventoryDoesNotSkipItemsMovedBetweenPages(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		listPage func(*Client, platform.RepoRef, time.Time, string) (int, string, bool, error)
	}{
		{
			name: "issues",
			path: "/api/v4/projects/42/issues",
			listPage: func(client *Client, ref platform.RepoRef, since time.Time, cursor string) (int, string, bool, error) {
				page, err := client.ListUpdatedIssues(t.Context(), ref, since, cursor)
				if err != nil || len(page.Items) == 0 {
					return 0, page.NextCursor, page.Exhausted, err
				}
				return page.Items[0].Number, page.NextCursor, page.Exhausted, nil
			},
		},
		{
			name: "merge requests",
			path: "/api/v4/projects/42/merge_requests",
			listPage: func(client *Client, ref platform.RepoRef, since time.Time, cursor string) (int, string, bool, error) {
				page, err := client.ListUpdatedMergeRequests(t.Context(), ref, since, cursor)
				if err != nil || len(page.Items) == 0 {
					return 0, page.NextCursor, page.Exhausted, err
				}
				return page.Items[0].Number, page.NextCursor, page.Exhausted, nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				assert.Equal(tt.path, r.URL.EscapedPath())
				assert.Equal("desc", r.URL.Query().Get("sort"))
				w.Header().Set("Content-Type", "application/json")
				if requests == 1 {
					w.Header().Set("X-Next-Page", "2")
					writeJSON(w, `[{"id":202,"iid":2,"title":"newest","state":"opened","created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T02:03:06Z"}]`)
					return
				}
				// Item 2 changed after page one was read. Descending pagination
				// keeps it in the consumed prefix, so item 1 remains on page two.
				writeJSON(w, `[{"id":201,"iid":1,"title":"unseen","state":"opened","created_at":"2025-01-01T00:00:00Z","updated_at":"2026-07-01T02:03:05Z"}]`)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL)
			ref := gitLabArchiveTestRef()
			watermark := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)

			firstNumber, cursor, exhausted, err := tt.listPage(client, ref, watermark, "")
			require.NoError(err)
			assert.Equal(2, firstNumber)
			assert.False(exhausted)
			secondNumber, _, exhausted, err := tt.listPage(client, ref, watermark, cursor)
			require.NoError(err)
			assert.Equal(1, secondNumber)
			assert.True(exhausted)
			assert.Equal(2, requests)
		})
	}
}

func TestGitLabArchivePaginationChargesEveryMarkedPage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			w.Header().Set("X-Next-Page", "2")
		}
		writeJSON(w, `[{"id":101,"iid":1,"title":"issue","state":"opened","created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-02T00:00:00Z"}]`)
	}))
	defer server.Close()
	budget := ghsync.NewSyncBudget(100)
	client := newTestClient(t, server.URL, WithSyncBudget(budget))
	ctx := ghsync.WithSyncBudget(t.Context())

	first, err := client.ListHistoricalIssues(ctx, gitLabArchiveTestRef(), "")
	require.NoError(err)
	_, err = client.ListHistoricalIssues(ctx, gitLabArchiveTestRef(), first.NextCursor)
	require.NoError(err)
	assert.Equal(2, requests)
	assert.Equal(2, budget.Spent())
}

func TestGitLabArchiveDiscussionPagesSeparateOrdinaryAndInlineComments(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
		}
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42/issues/7/discussions":
			writeJSON(w, `[{"id":"issue-thread","notes":[{"id":301,"body":"issue comment","author":{"username":"ivy"},"created_at":"2026-07-01T00:00:00Z"}]}]`)
		case "/api/v4/projects/42/merge_requests/8/discussions":
			writeJSON(w, `[
				{"id":"ordinary","notes":[{"id":401,"body":"ordinary comment","author":{"username":"omar"},"created_at":"2026-07-02T00:00:00Z"}]},
				{"id":"inline","notes":[
					{"id":402,"body":"inline comment","author":{"username":"rhea"},"created_at":"2026-07-03T00:00:00Z","updated_at":"2026-07-03T01:00:00Z","position":{"base_sha":"base","start_sha":"start","head_sha":"head","new_path":"main.go","new_line":9}},
					{"id":403,"body":"inline reply","author":{"username":"sam"},"created_at":"2026-07-03T02:00:00Z","updated_at":"2026-07-03T02:00:00Z"}
				]}
			]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabArchiveTestRef()

	issueComments, err := client.ListArchiveIssueComments(t.Context(), ref, 7, "")
	require.NoError(err)
	require.Len(issueComments.Items, 1)
	assert.Equal("issue comment", issueComments.Items[0].Body)
	assert.False(issueComments.Exhausted)

	mrComments, err := client.ListArchiveMergeRequestComments(t.Context(), ref, 8, "")
	require.NoError(err)
	require.Len(mrComments.Items, 1)
	assert.Equal("ordinary comment", mrComments.Items[0].Body)
	assert.Equal("issue_comment", mrComments.Items[0].EventType)
	threads, err := client.ListArchiveReviewThreads(t.Context(), ref, 8, "")
	require.NoError(err)
	require.Len(threads.Items, 2)
	assert.Equal([]string{"402", "403"}, []string{threads.Items[0].ProviderCommentID, threads.Items[1].ProviderCommentID})
	assert.Equal("inline", threads.Items[1].ProviderThreadID)
	assert.Equal("main.go", threads.Items[1].Range.Path)
	assert.Equal(9, threads.Items[1].Range.Line)
	assert.Equal(3, requests)

	_, err = client.ListArchiveSubmittedReviews(t.Context(), ref, 8, "")
	require.ErrorIs(err, platform.ErrUnsupportedCapability)
}

func TestGitLabArchiveFilteredDiscussionPageDeclaresProgress(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Next-Page", "2")
		writeJSON(w, `[{"id":"ordinary","notes":[{"id":401,"body":"ordinary","author":{"username":"omar"},"created_at":"2026-07-02T00:00:00Z"}]}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)

	page, err := client.ListArchiveReviewThreads(t.Context(), gitLabArchiveTestRef(), 8, "")
	require.NoError(err)
	assert.Empty(page.Items)
	assert.True(page.ProgressOnly)
	assert.NotEmpty(page.NextCursor)
	assert.NoError(platform.ValidateArchivePage(platform.KindGitLab, "gitlab.example.com", "", page))
}

func TestGitLabArchiveLookupClassifiesRemovalAndAccessLoss(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42/issues/7":
			http.NotFound(w, r)
		case "/api/v4/projects/42/issues/8":
			http.Error(w, "forbidden", http.StatusForbidden)
		case "/api/v4/projects/42/merge_requests/9":
			writeJSON(w, `{"id":209,"iid":9,"title":"present","state":"opened","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`)
		case "/api/v4/projects/42":
			writeJSON(w, `{"id":42,"path":"project","path_with_namespace":"group/project","name":"Project"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	ref := gitLabArchiveTestRef()

	// GitLab can return an item 404 for confidential content even while the
	// source project is readable, so the lookup must retain cached content.
	removed, err := client.GetArchiveIssue(t.Context(), ref, 7)
	require.NoError(err)
	assert.Equal(platform.ArchiveLookupInaccessible, removed.Outcome)
	inaccessible, err := client.GetArchiveIssue(t.Context(), ref, 8)
	require.NoError(err)
	assert.Equal(platform.ArchiveLookupInaccessible, inaccessible.Outcome)
	present, err := client.GetArchiveMergeRequest(t.Context(), ref, 9)
	require.NoError(err)
	assert.Equal(platform.ArchiveLookupPresent, present.Outcome)
	assert.Equal(9, present.Item.Number)
}

func TestGitLabArchiveLookupRetainsItemWhenRepositoryIsInaccessible(t *testing.T) {
	require := require.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL)
	_, err := client.GetArchiveIssue(t.Context(), gitLabArchiveTestRef(), 7)
	require.ErrorIs(err, platform.ErrPermissionDenied)
}

func TestGitLabArchiveCapabilitiesAreHonestAboutSubmittedReviews(t *testing.T) {
	assert.Equal(t, platform.ArchiveCapabilities{
		HistoricalIssues: true, HistoricalMergeRequests: true,
		OrdinaryComments: true, InlineReviewComments: true,
	}, newTestClient(t, "http://127.0.0.1:1").Capabilities().Archive)
}

func gitLabArchiveTestRef() platform.RepoRef {
	return platform.RepoRef{
		Platform: platform.KindGitLab, Host: "gitlab.example.com",
		Owner: "group", Name: "project", RepoPath: "group/project", PlatformID: 42,
		PlatformExternalID: "42", WebURL: "https://gitlab.example.com/group/project",
	}
}

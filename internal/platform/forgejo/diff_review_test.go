package forgejo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	Require "github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/platform"
)

func TestPublishDiffReviewDraftCreatesForgejoReview(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	submitted := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(http.MethodPost, r.Method)
		assert.Equal("/api/v1/repos/acme/widgets/pulls/42/reviews", r.URL.Path)
		var body struct {
			Event    string `json:"event"`
			Body     string `json:"body"`
			CommitID string `json:"commit_id"`
			Comments []struct {
				Path        string `json:"path"`
				Body        string `json:"body"`
				NewPosition int64  `json:"new_position"`
				OldPosition int64  `json:"old_position"`
			} `json:"comments"`
		}
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.Equal("REQUEST_CHANGES", body.Event)
		assert.Equal("summary", body.Body)
		assert.Equal("head-sha", body.CommitID)
		if assert.Len(body.Comments, 1) {
			assert.Equal(int64(5), body.Comments[0].NewPosition)
			assert.Zero(body.Comments[0].OldPosition)
		}

		w.Header().Set("Content-Type", "application/json")
		assert.NoError(json.NewEncoder(w).Encode(map[string]any{
			"id":           99,
			"state":        "REQUEST_CHANGES",
			"body":         "summary",
			"submitted_at": submitted.Format(time.RFC3339),
			"user":         map[string]any{"login": "reviewer"},
		}))
	}))
	defer server.Close()

	client, err := NewClient("codeberg.test", "token", WithBaseURLForTesting(server.URL))
	require.NoError(err)
	result, err := client.PublishDiffReviewDraft(context.Background(), platform.RepoRef{
		Owner: "acme",
		Name:  "widgets",
	}, 42, platform.PublishDiffReviewDraftInput{
		Body:   "summary",
		Action: platform.ReviewActionRequestChanges,
		Comments: []platform.LocalDiffReviewDraftComment{{
			Body: "range note",
			Range: platform.DiffReviewLineRange{
				Path:        "src/main.go",
				Side:        "right",
				Line:        5,
				DiffHeadSHA: "head-sha",
			},
		}},
	})

	require.NoError(err)
	require.NotNil(result)
	assert.Equal("99", result.ProviderReviewID)
	assert.Equal(submitted, result.SubmittedAt)
}

func TestListMergeRequestReviewThreadsReadsForgejoReviewComments(t *testing.T) {
	assert := Assert.New(t)
	require := Require.New(t)
	created := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/repos/acme/widgets/pulls/42/reviews":
			assert.Equal(http.MethodGet, r.Method)
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{{
				"id":           99,
				"state":        "COMMENT",
				"submitted_at": created.Format(time.RFC3339),
				"user":         map[string]any{"login": "reviewer"},
			}}))
		case "/api/v1/repos/acme/widgets/pulls/42/reviews/99/comments":
			assert.Equal(http.MethodGet, r.Method)
			assert.NoError(json.NewEncoder(w).Encode([]map[string]any{{
				"id":                     101,
				"body":                   "inline note",
				"user":                   map[string]any{"login": "reviewer"},
				"pull_request_review_id": 99,
				"path":                   "src/main.go",
				"commit_id":              "head-sha",
				"position":               7,
				"created_at":             created.Format(time.RFC3339),
				"updated_at":             created.Add(time.Minute).Format(time.RFC3339),
			}}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient("codeberg.test", "token", WithBaseURLForTesting(server.URL))
	require.NoError(err)
	threads, err := client.ListMergeRequestReviewThreads(context.Background(), platform.RepoRef{
		Owner: "acme",
		Name:  "widgets",
	}, 42)

	require.NoError(err)
	require.Len(threads, 1)
	assert.Equal("101", threads[0].ProviderThreadID)
	assert.Equal("99", threads[0].ProviderReviewID)
	assert.Equal("inline note", threads[0].Body)
	assert.Equal("reviewer", threads[0].AuthorLogin)
	assert.Equal("right", threads[0].Range.Side)
	assert.Equal(7, threads[0].Range.Line)
	assert.Equal("head-sha", threads[0].Range.CommitSHA)
}

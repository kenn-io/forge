package e2etest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
)

func TestGitHubReviewThreadsE2E(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	srv, database := setupTestServer(t)

	// Insert a GitHub repo
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)

	// Insert a PR
	prID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1001,
		Number:         42,
		URL:            "https://github.com/acme/widget/pull/42",
		Title:          "Review threads test",
		Author:         "author",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)

	// Insert review_comment events with ThreadID
	threadID1 := "thread-abc123"
	threadID2 := "thread-def456"
	platformID1 := int64(101)
	platformID2 := int64(102)
	platformID3 := int64(103)

	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{
		{
			MergeRequestID: prID,
			PlatformID:     &platformID1,
			EventType:      "review_comment",
			Author:         "reviewer1",
			Body:           "This needs refactoring",
			CreatedAt:      time.Now().UTC().Add(-10 * time.Minute),
			DedupeKey:      "review-comment-101",
			ThreadID:       &threadID1,
			PositionJSON:   `{"path":"main.go","position":42,"line":42}`,
			Resolvable:     true,
			Resolved:       false,
		},
		{
			MergeRequestID: prID,
			PlatformID:     &platformID2,
			EventType:      "review_comment",
			Author:         "reviewer2",
			Body:           "I agree with reviewer1",
			CreatedAt:      time.Now().UTC().Add(-5 * time.Minute),
			DedupeKey:      "review-comment-102",
			ThreadID:       &threadID1, // Same thread as first comment
			PositionJSON:   `{"path":"main.go","position":42,"line":42}`,
			Resolvable:     true,
			Resolved:       false,
		},
		{
			MergeRequestID: prID,
			PlatformID:     &platformID3,
			EventType:      "review_comment",
			Author:         "reviewer3",
			Body:           "Different file needs attention",
			CreatedAt:      time.Now().UTC().Add(-2 * time.Minute),
			DedupeKey:      "review-comment-103",
			ThreadID:       &threadID2, // Different thread
			PositionJSON:   `{"path":"helper.go","position":15,"line":15}`,
			Resolvable:     true,
			Resolved:       true, // This thread is resolved
		},
	}))

	// Fetch PR details via API
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pulls/github/acme/widget/42", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

	var result struct {
		Events []db.MREvent `json:"events"`
	}
	err = json.NewDecoder(rr.Body).Decode(&result)
	require.NoError(err)

	// Verify we got all 3 events
	require.Len(result.Events, 3)

	// Find events by author for easier assertion
	eventsByAuthor := make(map[string]db.MREvent)
	for _, event := range result.Events {
		eventsByAuthor[event.Author] = event
	}

	// Verify first thread (2 comments, same ThreadID)
	event1 := eventsByAuthor["reviewer1"]
	require.NotNil(event1.ThreadID)
	assert.Equal(threadID1, *event1.ThreadID)
	assert.JSONEq(`{"path":"main.go","position":42,"line":42}`, event1.PositionJSON)
	assert.True(event1.Resolvable)
	assert.False(event1.Resolved)

	event2 := eventsByAuthor["reviewer2"]
	require.NotNil(event2.ThreadID)
	assert.Equal(threadID1, *event2.ThreadID, "both events should share the same ThreadID")
	assert.JSONEq(`{"path":"main.go","position":42,"line":42}`, event2.PositionJSON)
	assert.True(event2.Resolvable)
	assert.False(event2.Resolved)

	// Verify second thread (different ThreadID, resolved)
	event3 := eventsByAuthor["reviewer3"]
	require.NotNil(event3.ThreadID)
	assert.Equal(threadID2, *event3.ThreadID)
	assert.JSONEq(`{"path":"helper.go","position":15,"line":15}`, event3.PositionJSON)
	assert.True(event3.Resolvable)
	assert.True(event3.Resolved)
}

func TestGitHubReviewThreadsWithNullThreadID(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	srv, database := setupTestServer(t)

	// Insert a GitHub repo
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)

	// Insert a PR
	prID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1001,
		Number:         43,
		URL:            "https://github.com/acme/widget/pull/43",
		Title:          "Null ThreadID test",
		Author:         "author",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)

	// Insert events: one with ThreadID, one without
	threadID := "thread-xyz789"
	platformID1 := int64(201)
	platformID2 := int64(202)

	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{
		{
			MergeRequestID: prID,
			PlatformID:     &platformID1,
			EventType:      "review_comment",
			Author:         "reviewer1",
			Body:           "This is part of a thread",
			CreatedAt:      time.Now().UTC().Add(-10 * time.Minute),
			DedupeKey:      "review-comment-201",
			ThreadID:       &threadID,
			PositionJSON:   `{"path":"main.go","position":10,"line":10}`,
			Resolvable:     true,
			Resolved:       false,
		},
		{
			MergeRequestID: prID,
			PlatformID:     &platformID2,
			EventType:      "issue_comment", // Regular comment, not a review comment
			Author:         "commenter",
			Body:           "This is a standalone comment",
			CreatedAt:      time.Now().UTC().Add(-5 * time.Minute),
			DedupeKey:      "issue-comment-202",
			ThreadID:       nil, // No ThreadID
			PositionJSON:   "",
			Resolvable:     false,
			Resolved:       false,
		},
	}))

	// Fetch PR details via API
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pulls/github/acme/widget/43", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, "response: %s", rr.Body.String())

	var result struct {
		Events []db.MREvent `json:"events"`
	}
	err = json.NewDecoder(rr.Body).Decode(&result)
	require.NoError(err)

	// Verify we got both events
	require.Len(result.Events, 2)

	// Find events by author
	eventsByAuthor := make(map[string]db.MREvent)
	for _, event := range result.Events {
		eventsByAuthor[event.Author] = event
	}

	// Verify event with ThreadID
	event1 := eventsByAuthor["reviewer1"]
	require.NotNil(event1.ThreadID)
	assert.Equal(threadID, *event1.ThreadID)
	assert.JSONEq(`{"path":"main.go","position":10,"line":10}`, event1.PositionJSON)

	// Verify event without ThreadID
	event2 := eventsByAuthor["commenter"]
	assert.Nil(event2.ThreadID, "standalone comment should have nil ThreadID")
	assert.Empty(event2.PositionJSON)
}

func TestGitHubReviewThreadsMultiplePRs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	srv, database := setupTestServer(t)

	// Insert a GitHub repo
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)

	// Insert two PRs
	pr1ID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1001,
		Number:         44,
		URL:            "https://github.com/acme/widget/pull/44",
		Title:          "PR 44",
		Author:         "author1",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)

	pr2ID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1002,
		Number:         45,
		URL:            "https://github.com/acme/widget/pull/45",
		Title:          "PR 45",
		Author:         "author2",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)

	// Insert events with the same ThreadID value but in different PRs
	// (They should be independent threads since they're in different PRs)
	sharedThreadIDValue := "thread-shared"
	platformID1 := int64(301)
	platformID2 := int64(302)

	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{
		{
			MergeRequestID: pr1ID,
			PlatformID:     &platformID1,
			EventType:      "review_comment",
			Author:         "reviewer1",
			Body:           "Comment in PR 44",
			CreatedAt:      time.Now().UTC(),
			DedupeKey:      "review-comment-301",
			ThreadID:       &sharedThreadIDValue,
			PositionJSON:   `{"path":"file1.go","position":1,"line":1}`,
			Resolvable:     true,
			Resolved:       false,
		},
		{
			MergeRequestID: pr2ID,
			PlatformID:     &platformID2,
			EventType:      "review_comment",
			Author:         "reviewer2",
			Body:           "Comment in PR 45",
			CreatedAt:      time.Now().UTC(),
			DedupeKey:      "review-comment-302",
			ThreadID:       &sharedThreadIDValue, // Same value, different PR
			PositionJSON:   `{"path":"file2.go","position":2,"line":2}`,
			Resolvable:     true,
			Resolved:       false,
		},
	}))

	// Fetch PR 44 details
	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/pulls/github/acme/widget/44", nil)
	rr1 := httptest.NewRecorder()
	srv.ServeHTTP(rr1, req1)

	require.Equal(http.StatusOK, rr1.Code)

	var result1 struct {
		Events []db.MREvent `json:"events"`
	}
	err = json.NewDecoder(rr1.Body).Decode(&result1)
	require.NoError(err)
	require.Len(result1.Events, 1)
	assert.Equal("reviewer1", result1.Events[0].Author)
	require.NotNil(result1.Events[0].ThreadID)
	assert.Equal(sharedThreadIDValue, *result1.Events[0].ThreadID)

	// Fetch PR 45 details
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/pulls/github/acme/widget/45", nil)
	rr2 := httptest.NewRecorder()
	srv.ServeHTTP(rr2, req2)

	require.Equal(http.StatusOK, rr2.Code)

	var result2 struct {
		Events []db.MREvent `json:"events"`
	}
	err = json.NewDecoder(rr2.Body).Decode(&result2)
	require.NoError(err)
	require.Len(result2.Events, 1)
	assert.Equal("reviewer2", result2.Events[0].Author)
	require.NotNil(result2.Events[0].ThreadID)
	assert.Equal(sharedThreadIDValue, *result2.Events[0].ThreadID)
}

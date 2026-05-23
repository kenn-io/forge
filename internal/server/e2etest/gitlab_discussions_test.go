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

func TestGetPRDetailIncludesDiscussionID(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	srv, database := setupTestServer(t)

	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)

	mrID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1001,
		Number:         7,
		URL:            "https://gitlab.com/acme/widget/-/merge_requests/7",
		Title:          "Discussion test",
		Author:         "author",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)

	discussionID := "disc-abc123"
	platformID := int64(101)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &platformID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "needs fix",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "note-101",
		DiscussionID:   &discussionID,
		PositionJSON:   `{"new_path":"main.go","new_line":42}`,
		Resolvable:     true,
		Resolved:       false,
	}}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pulls/gitlab/acme/widget/7", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)

	var result struct {
		Events []struct {
			DiscussionID *string `json:"DiscussionID"`
			PositionJSON string  `json:"PositionJSON"`
			Resolvable   bool    `json:"Resolvable"`
			Resolved     bool    `json:"Resolved"`
		} `json:"events"`
	}
	err = json.NewDecoder(rr.Body).Decode(&result)
	require.NoError(err)

	require.Len(result.Events, 1)
	assert.NotNil(result.Events[0].DiscussionID)
	assert.Equal("disc-abc123", *result.Events[0].DiscussionID)
	assert.Equal(`{"new_path":"main.go","new_line":42}`, result.Events[0].PositionJSON)
	assert.True(result.Events[0].Resolvable)
	assert.False(result.Events[0].Resolved)
}

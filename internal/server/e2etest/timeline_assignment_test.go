package e2etest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
)

type timelineDetailResponse struct {
	Events []struct {
		EventType    string `json:"EventType"`
		Author       string `json:"Author"`
		Summary      string `json:"Summary"`
		MetadataJSON string `json:"MetadataJSON"`
		DirectURL    string `json:"DirectURL"`
	} `json:"events"`
}

func TestE2E_DetailTimelineReturnsAssignmentEvents(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, database := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	mrID, issueID := seedAssignmentTimelineItems(t, database)

	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: mrID,
		EventType:      "assigned",
		Author:         "wesm",
		Summary:        "self-assigned this",
		MetadataJSON:   `{"assignee":"wesm"}`,
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "timeline-pr-assigned",
	}}))
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:      issueID,
		EventType:    "assigned",
		Author:       "alice",
		Summary:      "assigned bob",
		MetadataJSON: `{"assignee":"bob"}`,
		CreatedAt:    time.Now().UTC(),
		DedupeKey:    "timeline-issue-assigned",
	}}))

	prDetail := getTimelineDetail(t, ts, "/api/v1/pulls/github/acme/widget/1")
	require.Len(prDetail.Events, 1)
	assert.Equal("assigned", prDetail.Events[0].EventType)
	assert.Equal("wesm", prDetail.Events[0].Author)
	assert.Equal("self-assigned this", prDetail.Events[0].Summary)
	assert.JSONEq(`{"assignee":"wesm"}`, prDetail.Events[0].MetadataJSON)

	issueDetail := getTimelineDetail(t, ts, "/api/v1/issues/github/acme/widget/5")
	require.Len(issueDetail.Events, 1)
	assert.Equal("assigned", issueDetail.Events[0].EventType)
	assert.Equal("alice", issueDetail.Events[0].Author)
	assert.Equal("assigned bob", issueDetail.Events[0].Summary)
	assert.JSONEq(`{"assignee":"bob"}`, issueDetail.Events[0].MetadataJSON)
}

func TestE2E_DetailTimelineReturnsCommentDirectURLs(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, database := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	mrID, issueID := seedAssignmentTimelineItems(t, database)

	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: mrID,
		PlatformID:     ptrInt64(101),
		EventType:      "issue_comment",
		Author:         "wesm",
		Body:           "PR comment",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "timeline-pr-comment",
		DirectURL:      "https://github.com/acme/widget/pull/1#issuecomment-101",
	}}))
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:    issueID,
		PlatformID: ptrInt64(202),
		EventType:  "issue_comment",
		Author:     "alice",
		Body:       "Issue comment",
		CreatedAt:  time.Now().UTC(),
		DedupeKey:  "timeline-issue-comment",
		DirectURL:  "https://github.com/acme/widget/issues/5#issuecomment-202",
	}}))

	prDetail := getTimelineDetail(t, ts, "/api/v1/pulls/github/acme/widget/1")
	require.Len(prDetail.Events, 1)
	assert.Equal("https://github.com/acme/widget/pull/1#issuecomment-101", prDetail.Events[0].DirectURL)

	issueDetail := getTimelineDetail(t, ts, "/api/v1/issues/github/acme/widget/5")
	require.Len(issueDetail.Events, 1)
	assert.Equal("https://github.com/acme/widget/issues/5#issuecomment-202", issueDetail.Events[0].DirectURL)
}

func TestE2E_DetailTimelineReturnsForcePushCommitOrderingMetadata(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	srv, database := setupTestServer(t)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	mrID, _ := seedAssignmentTimelineItems(t, database)
	oldHead := "cccccccccccccccccccccccccccccccccccccccc"
	newHead := "ffffffffffffffffffffffffffffffffffffffff"
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{
		{
			MergeRequestID: mrID,
			EventType:      "force_push",
			Author:         "alice",
			Summary:        "ccccccc -> fffffff",
			MetadataJSON:   `{"before_sha":"` + oldHead + `","after_sha":"` + newHead + `"}`,
			CreatedAt:      time.Now().UTC(),
			DedupeKey:      "timeline-force-push",
		},
		{
			MergeRequestID: mrID,
			EventType:      "commit",
			Summary:        newHead,
			Body:           "new head after same-length rebase",
			MetadataJSON:   `{"commit_order":2,"commit_order_key":4}`,
			CreatedAt:      time.Now().UTC().Add(-time.Minute),
			DedupeKey:      "timeline-commit-new-head",
		},
	}))

	prDetail := getTimelineDetail(t, ts, "/api/v1/pulls/github/acme/widget/1")
	var commitEvent *struct {
		EventType    string `json:"EventType"`
		Author       string `json:"Author"`
		Summary      string `json:"Summary"`
		MetadataJSON string `json:"MetadataJSON"`
		DirectURL    string `json:"DirectURL"`
	}
	for i := range prDetail.Events {
		if prDetail.Events[i].EventType == "commit" && prDetail.Events[i].Summary == newHead {
			commitEvent = &prDetail.Events[i]
		}
	}
	require.NotNil(commitEvent)
	assert.JSONEq(`{"commit_order":2,"commit_order_key":4}`, commitEvent.MetadataJSON)
}

func seedAssignmentTimelineItems(t *testing.T, database *db.DB) (int64, int64) {
	t.Helper()
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	mrID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1001,
		Number:         1,
		URL:            "https://github.com/acme/widget/pull/1",
		Title:          "Assignment PR",
		Author:         "testuser",
		State:          "open",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(t, err)
	require.NoError(t, database.EnsureKanbanState(ctx, mrID))

	issueNumber := 5
	issueID, err := database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     5001,
		Number:         issueNumber,
		URL:            "https://github.com/acme/widget/issues/" + strconv.Itoa(issueNumber),
		Title:          "Assignment issue",
		Author:         "testuser",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(t, err)

	return mrID, issueID
}

//go:fix inline
func ptrInt64(v int64) *int64 {
	return new(v)
}

func getTimelineDetail(
	t *testing.T,
	ts *httptest.Server,
	path string,
) timelineDetailResponse {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var detail timelineDetailResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&detail))
	return detail
}

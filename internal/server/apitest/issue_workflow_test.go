package apitest

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
)

func decodeIssueWorkflowBody(t *testing.T, body []byte, target any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(body, target))
}

func TestIssueWorkflowStatusWire(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 5, "open")
	client := setupTestClient(t, srv)

	listResp, err := client.HTTP.ListIssuesWithResponse(ctx, nil)
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode())
	var listRows []map[string]any
	decodeIssueWorkflowBody(t, listResp.Body, &listRows)
	require.Len(listRows, 1)
	assert.Equal("new", listRows[0]["WorkflowStatus"])

	detailResp, err := client.HTTP.GetIssueWithResponse(ctx, "gh", "acme", "widget", 5)
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode())
	var detail map[string]any
	decodeIssueWorkflowBody(t, detailResp.Body, &detail)
	issue, ok := detail["issue"].(map[string]any)
	require.True(ok)
	assert.Equal("new", issue["WorkflowStatus"])
	workflow, ok := detail["workflow"].(map[string]any)
	require.True(ok)
	assert.Equal("new", workflow["status"])

	repo, err := database.GetRepoByIdentity(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	_, err = database.SetItemWorkflowState(ctx, db.SetItemWorkflowStateParams{
		RepoID:     repo.ID,
		ItemType:   db.ItemTypeIssue,
		ItemNumber: 5,
		Status:     string(db.KanbanStatusWaiting),
		Source:     "api",
		Actor:      "agent-a",
		Reason:     "needs maintainer input",
	})
	require.NoError(err)

	listResp, err = client.HTTP.ListIssuesWithResponse(ctx, nil)
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode())
	decodeIssueWorkflowBody(t, listResp.Body, &listRows)
	require.Len(listRows, 1)
	assert.Equal("waiting", listRows[0]["WorkflowStatus"])

	detailResp, err = client.HTTP.GetIssueWithResponse(ctx, "gh", "acme", "widget", 5)
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode())
	decodeIssueWorkflowBody(t, detailResp.Body, &detail)
	issue, ok = detail["issue"].(map[string]any)
	require.True(ok)
	assert.Equal("waiting", issue["WorkflowStatus"])
	workflow, ok = detail["workflow"].(map[string]any)
	require.True(ok)
	assert.Equal("waiting", workflow["status"])
	assert.NotEmpty(workflow["updated_at"])
	assert.Equal("api", workflow["updated_source"])
	assert.Equal("agent-a", workflow["updated_actor"])
	assert.Equal("needs maintainer input", workflow["updated_reason"])
}

func TestIssueWorkflowStatusMetadataNormalizesInvalidStoredStatus(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 6, "open")

	repo, err := database.GetRepoByIdentity(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	_, err = database.WriteDB().ExecContext(ctx, `INSERT INTO middleman_item_workflow_state (
			repo_id, item_type, item_number, status, updated_source
		) VALUES (?, 'issue', 6, 'bogus', 'api')`, repo.ID)
	require.NoError(err)

	client := setupTestClient(t, srv)
	detailResp, err := client.HTTP.GetIssueWithResponse(ctx, "gh", "acme", "widget", 6)
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode())

	var detail map[string]any
	decodeIssueWorkflowBody(t, detailResp.Body, &detail)
	issue, ok := detail["issue"].(map[string]any)
	require.True(ok)
	workflow, ok := detail["workflow"].(map[string]any)
	require.True(ok)
	assert.Equal("new", issue["WorkflowStatus"])
	assert.Equal(issue["WorkflowStatus"], workflow["status"])
	assert.Equal("api", workflow["updated_source"])
}

func TestIssueSyncResponseIncludesWorkflow(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	srv, database, providerClient, _ := setupTestServerWithFixtureClient(t)

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.SetItemWorkflowState(ctx, db.SetItemWorkflowStateParams{
		RepoID:     repoID,
		ItemType:   db.ItemTypeIssue,
		ItemNumber: 8,
		Status:     string(db.KanbanStatusReviewing),
		Source:     "api",
	})
	require.NoError(err)

	issueID := int64(8000)
	issueNumber := 8
	title := "Synced issue"
	state := "open"
	url := "https://github.com/acme/widget/issues/8"
	author := "octocat"
	createdAt := gh.Timestamp{Time: now}
	updatedAt := gh.Timestamp{Time: now}
	providerClient.Issues["acme/widget"] = []*gh.Issue{{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &title,
		State:     &state,
		HTMLURL:   &url,
		User:      &gh.User{Login: &author},
		CreatedAt: &createdAt,
		UpdatedAt: &updatedAt,
	}}

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.SyncIssueWithResponse(ctx, "gh", "acme", "widget", 8)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	var body map[string]any
	decodeIssueWorkflowBody(t, resp.Body, &body)
	issue, ok := body["issue"].(map[string]any)
	require.True(ok)
	assert.Equal("reviewing", issue["WorkflowStatus"])
	workflow, ok := body["workflow"].(map[string]any)
	require.True(ok)
	assert.Equal("reviewing", workflow["status"])
	assert.Equal("api", workflow["updated_source"])
}

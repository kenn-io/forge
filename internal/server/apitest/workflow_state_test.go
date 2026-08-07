package apitest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server"
)

func workflowStateRequest(
	t *testing.T,
	srv *server.Server,
	method string,
	path string,
	body any,
) (int, map[string]any) {
	t.Helper()
	var reader io.Reader = http.NoBody
	if body != nil {
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, "/api/v1"+path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	decoded := map[string]any{}
	if rr.Body.Len() > 0 && json.Valid(rr.Body.Bytes()) {
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &decoded))
	} else {
		decoded["raw_body"] = rr.Body.String()
	}
	return rr.Code, decoded
}

func workflowStateItems(t *testing.T, body map[string]any) []any {
	t.Helper()
	items, ok := body["items"].([]any)
	require.True(t, ok)
	return items
}

func TestWorkflowStatePutAndGet(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 42)
	seedIssue(t, database, "acme", "widget", 7, "open")
	_, err := database.WriteDB().ExecContext(t.Context(),
		`UPDATE forge_issues SET last_activity_at = '2026-07-01 10:00:00' WHERE number = 7`)
	require.NoError(err)

	code, body := workflowStateRequest(t, srv, http.MethodPut,
		"/workflow-state/pr/gh/acme/widget/42",
		map[string]any{
			"status":          "reviewing",
			"expected_status": "new",
			"source":          "mcp",
			"actor":           "agent-a",
			"reason":          "claiming for review",
		})
	require.Equal(http.StatusOK, code)
	assert.Equal("new", body["previous_status"])
	assert.Equal("reviewing", body["status"])
	assert.Equal("mcp", body["updated_source"])
	assert.Equal("agent-a", body["updated_actor"])
	assert.Equal("claiming for review", body["updated_reason"])
	assert.NotEmpty(body["updated_at"])

	code, body = workflowStateRequest(t, srv, http.MethodGet, "/workflow-state", nil)
	require.Equal(http.StatusOK, code)
	items := workflowStateItems(t, body)
	require.Len(items, 2)

	pr, ok := items[0].(map[string]any)
	require.True(ok)
	assert.Equal("github", pr["provider"])
	assert.Equal("github.com", pr["platform_host"])
	assert.Equal("acme", pr["owner"])
	assert.Equal("widget", pr["name"])
	assert.Equal("acme/widget", pr["repo_path"])
	assert.Equal("pr", pr["item_type"])
	number, ok := pr["number"].(float64)
	require.True(ok)
	assert.Equal(42, int(number))
	assert.NotEmpty(pr["last_activity_at"])
	workflow, ok := pr["workflow"].(map[string]any)
	require.True(ok)
	assert.Equal("reviewing", workflow["status"])
	assert.Equal("mcp", workflow["updated_source"])
	assert.Equal("agent-a", workflow["updated_actor"])
	assert.Equal("claiming for review", workflow["updated_reason"])

	issue, ok := items[1].(map[string]any)
	require.True(ok)
	assert.Equal("issue", issue["item_type"])
	number, ok = issue["number"].(float64)
	require.True(ok)
	assert.Equal(7, int(number))
	workflow, ok = issue["workflow"].(map[string]any)
	require.True(ok)
	assert.Equal("new", workflow["status"])
	assert.NotContains(workflow, "updated_at")
}

func TestWorkflowStatePutConflict(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 42)

	code, _ := workflowStateRequest(t, srv, http.MethodPut,
		"/workflow-state/pr/gh/acme/widget/42",
		map[string]any{"status": "reviewing", "expected_status": "new"})
	require.Equal(http.StatusOK, code)

	code, body := workflowStateRequest(t, srv, http.MethodPut,
		"/workflow-state/pr/gh/acme/widget/42",
		map[string]any{"status": "waiting", "expected_status": "new"})
	require.Equal(http.StatusConflict, code)
	assert.Equal("conflict", body["code"])
	details, ok := body["details"].(map[string]any)
	require.True(ok)
	assert.Equal("reviewing", details["current_status"])
	assert.Equal("new", details["expected_status"])

	code, body = workflowStateRequest(t, srv, http.MethodGet,
		"/workflow-state?item_type=pr&state=reviewing", nil)
	require.Equal(http.StatusOK, code)
	items := workflowStateItems(t, body)
	require.Len(items, 1)
	item, ok := items[0].(map[string]any)
	require.True(ok)
	workflow, ok := item["workflow"].(map[string]any)
	require.True(ok)
	assert.Equal("reviewing", workflow["status"])
}

func TestWorkflowStatePutValidation(t *testing.T) {
	req := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 42)

	tests := []struct {
		name  string
		path  string
		body  map[string]any
		field string
		want  int
	}{
		{
			name:  "invalid status",
			path:  "/workflow-state/pr/gh/acme/widget/42",
			body:  map[string]any{"status": "triage"},
			field: "body.status",
		},
		{
			name:  "invalid source",
			path:  "/workflow-state/pr/gh/acme/widget/42",
			body:  map[string]any{"status": "reviewing", "force": true, "source": "MCP"},
			field: "body.source",
		},
		{
			name:  "actor too long",
			path:  "/workflow-state/pr/gh/acme/widget/42",
			body:  map[string]any{"status": "reviewing", "force": true, "actor": string(bytes.Repeat([]byte("a"), 121))},
			field: "body.actor",
		},
		{
			name:  "reason too long",
			path:  "/workflow-state/pr/gh/acme/widget/42",
			body:  map[string]any{"status": "reviewing", "force": true, "reason": string(bytes.Repeat([]byte("r"), 501))},
			field: "body.reason",
		},
		{
			name:  "missing expected status without force",
			path:  "/workflow-state/pr/gh/acme/widget/42",
			body:  map[string]any{"status": "reviewing"},
			field: "body.expected_status",
			want:  http.StatusBadRequest,
		},
		{
			name: "expected status with force",
			path: "/workflow-state/pr/gh/acme/widget/42",
			body: map[string]any{
				"status":          "reviewing",
				"expected_status": "new",
				"force":           true,
			},
			field: "body.force",
			want:  http.StatusBadRequest,
		},
		{
			name: "expected status with force false",
			path: "/workflow-state/pr/gh/acme/widget/42",
			body: map[string]any{
				"status":          "reviewing",
				"expected_status": "new",
				"force":           false,
			},
			field: "body.force",
			want:  http.StatusBadRequest,
		},
		{
			name:  "force false without expected status",
			path:  "/workflow-state/pr/gh/acme/widget/42",
			body:  map[string]any{"status": "reviewing", "force": false},
			field: "body.force",
			want:  http.StatusBadRequest,
		},
		{
			name:  "unexpected field",
			path:  "/workflow-state/pr/gh/acme/widget/42",
			body:  map[string]any{"status": "reviewing", "force": true, "unexpected": "field"},
			field: "body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			code, body := workflowStateRequest(t, srv, http.MethodPut, tt.path, tt.body)
			want := tt.want
			if want == 0 {
				want = http.StatusUnprocessableEntity
			}
			require.Equal(want, code)
			assert.Equal("validationError", body["code"])
			if errors, ok := body["errors"].([]any); ok {
				require.NotEmpty(errors)
				detail, ok := errors[0].(map[string]any)
				require.True(ok)
				assert.Equal(tt.field, detail["location"])
			} else {
				details, ok := body["details"].(map[string]any)
				require.True(ok)
				assert.Equal(tt.field, details["field"])
			}
		})
	}

	code, body := workflowStateRequest(t, srv, http.MethodPut,
		"/workflow-state/task/gh/acme/widget/42",
		map[string]any{"status": "reviewing", "force": true})
	req.Equal(http.StatusUnprocessableEntity, code)
	assert.Equal(t, "validationError", body["code"])
}

func TestWorkflowStatePutMissingItem(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	seedIssue(t, database, "acme", "widget", 2, "open")

	code, body := workflowStateRequest(t, srv, http.MethodPut,
		"/workflow-state/pr/gh/acme/widget/99",
		map[string]any{"status": "reviewing", "force": true})
	require.Equal(http.StatusNotFound, code)
	assert.Equal("pullNotFound", body["code"])

	code, body = workflowStateRequest(t, srv, http.MethodPut,
		"/workflow-state/issue/gh/acme/widget/99",
		map[string]any{"status": "reviewing", "force": true})
	require.Equal(http.StatusNotFound, code)
	assert.Equal("issueNotFound", body["code"])

	code, body = workflowStateRequest(t, srv, http.MethodPut,
		"/workflow-state/pr/gh/acme/missing/1",
		map[string]any{"status": "reviewing", "force": true})
	require.Equal(http.StatusNotFound, code)
	assert.Equal("repoNotFound", body["code"])
}

func TestWorkflowStateHostVariant(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPROnHost(t, database, "ghe.example.com", "acme", "widget", 42)

	code, body := workflowStateRequest(t, srv, http.MethodPut,
		"/host/ghe.example.com/workflow-state/pr/gh/acme/widget/42",
		map[string]any{"status": "waiting", "force": true, "source": "api"})
	require.Equal(http.StatusOK, code)
	assert.Equal("waiting", body["status"])

	code, body = workflowStateRequest(t, srv, http.MethodGet,
		"/workflow-state?repo=github|ghe.example.com/acme/widget", nil)
	require.Equal(http.StatusOK, code)
	items := workflowStateItems(t, body)
	require.Len(items, 1)
	item, ok := items[0].(map[string]any)
	require.True(ok)
	assert.Equal("ghe.example.com", item["platform_host"])
	workflow, ok := item["workflow"].(map[string]any)
	require.True(ok)
	assert.Equal("waiting", workflow["status"])
}

func TestWorkflowStateHostVariantUsesEscapedNestedRepoPath(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	srv, database := setupTestServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	repoPath := "Group/SubGroup/My_Project"
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "420",
		Owner:          "Group/SubGroup",
		Name:           "My_Project",
		RepoPath:       repoPath,
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     42000,
		Number:         42,
		URL:            "https://gitlab.example.com/Group/SubGroup/My_Project/-/merge_requests/42",
		Title:          "Nested path PR",
		Author:         "testuser",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	code, body := workflowStateRequest(t, srv, http.MethodPut,
		"/host/gitlab.example.com/workflow-state/pr/gl/Group%2FSubGroup/My_Project/42",
		map[string]any{"status": "reviewing", "force": true, "source": "mcp"})
	require.Equal(http.StatusOK, code, body)
	assert.Equal("reviewing", body["status"])

	code, body = workflowStateRequest(t, srv, http.MethodGet,
		"/workflow-state?repo=gitlab|gitlab.example.com/Group/SubGroup/My_Project", nil)
	require.Equal(http.StatusOK, code)
	items := workflowStateItems(t, body)
	require.Len(items, 1)
	item, ok := items[0].(map[string]any)
	require.True(ok)
	assert.Equal(repoPath, item["repo_path"])
	workflow, ok := item["workflow"].(map[string]any)
	require.True(ok)
	assert.Equal("reviewing", workflow["status"])
}

func TestWorkflowStateListFilters(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	seedPR(t, database, "acme", "widget", 2)
	seedPR(t, database, "acme", "widget", 3)
	seedIssue(t, database, "acme", "widget", 4, "open")

	repo, err := database.GetRepoByIdentity(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC()
	require.NoError(database.UpdateMRState(ctx, repo.ID, 3, "closed", nil, &now))
	_, err = database.WriteDB().ExecContext(ctx,
		`UPDATE forge_merge_requests SET last_activity_at = '2026-07-01 10:00:00' WHERE repo_id = ? AND number = 1`,
		repo.ID)
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(ctx,
		`DELETE FROM forge_item_workflow_state WHERE repo_id = ? AND item_type = 'pr' AND item_number = 1`,
		repo.ID)
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(ctx,
		`UPDATE forge_issues SET last_activity_at = '2026-07-01 11:00:00' WHERE repo_id = ? AND number = 4`,
		repo.ID)
	require.NoError(err)
	_, err = database.SetItemWorkflowState(ctx, db.SetItemWorkflowStateParams{
		RepoID:     repo.ID,
		ItemType:   db.ItemTypePR,
		ItemNumber: 2,
		Status:     "new",
		Source:     "api",
	})
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(ctx,
		`UPDATE forge_item_workflow_state SET updated_at = '2026-07-01 12:00:00'
		  WHERE repo_id = ? AND item_type = 'pr' AND item_number = 2`,
		repo.ID)
	require.NoError(err)

	code, body := workflowStateRequest(t, srv, http.MethodGet, "/workflow-state", nil)
	require.Equal(http.StatusOK, code)
	assert.Len(workflowStateItems(t, body), 3)

	code, body = workflowStateRequest(t, srv, http.MethodGet, "/workflow-state?include_closed=true", nil)
	require.Equal(http.StatusOK, code)
	assert.Len(workflowStateItems(t, body), 4)

	code, body = workflowStateRequest(t, srv, http.MethodGet, "/workflow-state?state=new", nil)
	require.Equal(http.StatusOK, code)
	assert.Len(workflowStateItems(t, body), 3)

	code, body = workflowStateRequest(t, srv, http.MethodGet, "/workflow-state?item_type=issue", nil)
	require.Equal(http.StatusOK, code)
	items := workflowStateItems(t, body)
	require.Len(items, 1)
	item, ok := items[0].(map[string]any)
	require.True(ok)
	assert.Equal("issue", item["item_type"])

	var got []int
	cursorPath := "/workflow-state?limit=2"
	for {
		code, body = workflowStateRequest(t, srv, http.MethodGet, cursorPath, nil)
		require.Equal(http.StatusOK, code)
		for _, raw := range workflowStateItems(t, body) {
			item, ok := raw.(map[string]any)
			require.True(ok)
			number, ok := item["number"].(float64)
			require.True(ok)
			got = append(got, int(number))
		}
		next, _ := body["next_cursor"].(string)
		if next == "" {
			break
		}
		cursorPath = "/workflow-state?limit=2&cursor=" + next
	}
	assert.Equal([]int{2, 4, 1}, got)
}

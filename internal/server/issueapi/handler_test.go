package issueapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestHandlerRegistersOnlyIssueRoutes(t *testing.T) {
	t.Parallel()

	api := humago.New(http.NewServeMux(), huma.DefaultConfig("test", "0"))
	New(Deps{}).Register(api)
	assert := assert.New(t)

	type routeContract struct {
		method string
		path   string
		status int
	}
	issueRepo := "/issues/{provider}/{owner}/{name}"
	hostIssueRepo := "/host/{platform_host}" + issueRepo
	issue := issueRepo + "/{number}"
	hostIssue := hostIssueRepo + "/{number}"
	want := map[string]routeContract{
		"list-issues": {http.MethodGet, "/issues", http.StatusOK},
	}
	addPair := func(id, method, suffix string, status int) {
		want[id] = routeContract{method, issue + suffix, status}
		want[id+"-on-host"] = routeContract{method, hostIssue + suffix, status}
	}
	want["create-issue"] = routeContract{http.MethodPost, issueRepo, http.StatusCreated}
	want["create-issue-on-host"] = routeContract{http.MethodPost, hostIssueRepo, http.StatusCreated}
	addPair("get-issue", http.MethodGet, "", http.StatusOK)
	addPair("post-issue-comment", http.MethodPost, "/comments", http.StatusCreated)
	addPair("edit-issue-content", http.MethodPatch, "", http.StatusOK)
	addPair("edit-issue-comment", http.MethodPatch, "/comments/{comment_id}", http.StatusOK)
	addPair("delete-issue-comment", http.MethodDelete, "/comments/{comment_id}", http.StatusNoContent)
	addPair("set-issue-labels", http.MethodPut, "/labels", http.StatusOK)
	addPair("set-issue-assignees", http.MethodPut, "/assignees", http.StatusOK)
	addPair("set-issue-github-state", http.MethodPost, "/github-state", http.StatusOK)

	gotByID := make(map[string]*huma.Operation)
	gotPathByID := make(map[string]string)
	for path, item := range api.OpenAPI().Paths {
		for _, operation := range []*huma.Operation{
			item.Get, item.Put, item.Post, item.Delete, item.Patch,
		} {
			if operation != nil {
				gotByID[operation.OperationID] = operation
				gotPathByID[operation.OperationID] = path
			}
		}
	}
	assert.Len(gotByID, len(want))
	for operationID, expected := range want {
		gotOperation := gotByID[operationID]
		if assert.NotNil(gotOperation, operationID) {
			assert.Equal(expected.method, gotOperation.Method, operationID)
			assert.Equal(expected.status, gotOperation.DefaultStatus, operationID)
		}
		assert.Equal(expected.path, gotPathByID[operationID], operationID)
	}
	for _, operationID := range []string{
		"sync-issue", "sync-issue-on-host",
		"enqueue-issue-sync", "enqueue-issue-sync-on-host",
		"create-issue-workspace", "create-issue-workspace-on-host",
		"list-pulls", "get-pull", "get-repo", "list-repo-labels",
	} {
		assert.NotContains(gotByID, operationID)
	}
}

func TestIssueDetailTreatsWorkspaceSnapshotFailureAsBestEffort(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	identity.PlatformRepoID = "repo-acme-widget"
	repoID, err := database.UpsertRepo(t.Context(), identity)
	require.NoError(err)
	_, err = database.UpsertIssue(t.Context(), &db.Issue{
		RepoID: repoID, PlatformID: 43, Number: 43, Title: "Available issue",
		Author: "bob", State: "open",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	repo, err := database.GetRepoByID(t.Context(), repoID)
	require.NoError(err)
	require.NotNil(repo)
	issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 43)
	require.NoError(err)
	require.NotNil(issue)
	handler := New(Deps{
		DB:       database,
		Resolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: database}),
		WorkspaceSubjects: func(context.Context) (workspaceapi.WorkspaceSubjectSnapshot, error) {
			return workspaceapi.WorkspaceSubjectSnapshot{}, errors.New("snapshot unavailable")
		},
	})

	response, err := handler.BuildDetail(t.Context(), repo, issue)
	require.NoError(err)
	require.NotNil(response.Issue)
	assert.Equal(t, 43, response.Issue.Number)
	assert.Nil(t, response.Workspace)
}

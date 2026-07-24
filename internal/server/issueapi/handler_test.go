package issueapi

import (
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
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

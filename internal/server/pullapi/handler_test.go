package pullapi

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandlerRegistersPullRoutes(t *testing.T) {
	t.Parallel()

	api := humago.New(http.NewServeMux(), huma.DefaultConfig("test", "0"))
	New(Deps{}).Register(api)
	assert := assert.New(t)

	type routeContract struct {
		method string
		path   string
		status int
	}
	pull := "/pulls/{provider}/{owner}/{name}/{number}"
	hostPull := "/host/{platform_host}" + pull
	want := map[string]routeContract{
		"list-pulls":  {http.MethodGet, "/pulls", http.StatusOK},
		"list-stacks": {http.MethodGet, "/stacks", http.StatusOK},
	}
	addPair := func(id, method, suffix string, status int) {
		want[id] = routeContract{method, pull + suffix, status}
		want[id+"-on-host"] = routeContract{method, hostPull + suffix, status}
	}
	addPair("get-pull", http.MethodGet, "", http.StatusOK)
	addPair("get-pull-import-metadata", http.MethodGet, "/import-metadata", http.StatusOK)
	addPair("get-pull-commits", http.MethodGet, "/commits", http.StatusOK)
	addPair("get-pull-diff", http.MethodGet, "/diff", http.StatusOK)
	addPair("get-pull-files", http.MethodGet, "/files", http.StatusOK)
	addPair("get-pull-file-preview", http.MethodGet, "/file-preview", http.StatusOK)
	addPair("get-pull-stack", http.MethodGet, "/stack", http.StatusOK)
	addPair("set-kanban-state", http.MethodPut, "/state", http.StatusOK)
	addPair("edit-pr-content", http.MethodPatch, "", http.StatusOK)
	addPair("post-pr-comment", http.MethodPost, "/comments", http.StatusCreated)
	addPair("edit-pr-comment", http.MethodPatch, "/comments/{comment_id}", http.StatusOK)
	addPair("delete-pr-comment", http.MethodDelete, "/comments/{comment_id}", http.StatusNoContent)
	addPair("reply-to-discussion", http.MethodPost, "/discussions/{discussion_id}/reply", http.StatusCreated)
	addPair("resolve-discussion", http.MethodPost, "/discussions/{discussion_id}/resolve", http.StatusOK)
	addPair("set-pr-labels", http.MethodPut, "/labels", http.StatusOK)
	addPair("set-pr-assignees", http.MethodPut, "/assignees", http.StatusOK)
	addPair("set-pr-reviewers", http.MethodPut, "/reviewers", http.StatusOK)
	addPair("approve-pull", http.MethodPost, "/approve", http.StatusOK)
	addPair("request-pull-changes", http.MethodPost, "/request-changes", http.StatusOK)
	addPair("approve-pull-workflows", http.MethodPost, "/approve-workflows", http.StatusOK)
	addPair("mark-pull-ready-for-review", http.MethodPost, "/ready-for-review", http.StatusOK)
	addPair("merge-pull", http.MethodPost, "/merge", http.StatusOK)
	addPair("defer-merge-pull", http.MethodPost, "/merge/deferred", http.StatusAccepted)
	addPair("set-pr-github-state", http.MethodPost, "/github-state", http.StatusOK)
	addPair("get-pr-review-draft", http.MethodGet, "/review-draft", http.StatusOK)
	addPair("create-pr-review-draft-comment", http.MethodPost, "/review-draft/comments", http.StatusCreated)
	addPair("edit-pr-review-draft-comment", http.MethodPatch, "/review-draft/comments/{draft_comment_id}", http.StatusOK)
	addPair("delete-pr-review-draft-comment", http.MethodDelete, "/review-draft/comments/{draft_comment_id}", http.StatusOK)
	addPair("publish-pr-review-draft", http.MethodPost, "/review-draft/publish", http.StatusOK)
	addPair("discard-pr-review-draft", http.MethodDelete, "/review-draft", http.StatusOK)
	addPair("apply-pr-review-suggestions", http.MethodPost, "/review-suggestions/apply", http.StatusOK)
	addPair("resolve-pr-review-thread", http.MethodPost, "/review-threads/{thread_id}/resolve", http.StatusOK)
	addPair("unresolve-pr-review-thread", http.MethodPost, "/review-threads/{thread_id}/unresolve", http.StatusOK)

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
		"sync-pull", "sync-pull-on-host",
		"refresh-pull-ci", "refresh-pull-ci-on-host",
		"enqueue-pr-sync", "enqueue-pr-sync-on-host",
	} {
		assert.NotContains(gotByID, operationID)
	}
}

func TestHandlerStopClosesAdmissionAndShutdownWaitsForWorkers(t *testing.T) {
	require := require.New(t)
	handler := New(Deps{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	require.True(handler.runBackground(func(ctx context.Context) {
		<-ctx.Done()
		close(canceled)
		<-release
	}))

	handler.Stop()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		require.FailNow("Stop did not cancel active Pull worker")
	}
	require.False(handler.runBackground(func(context.Context) {}), "Stop must close admission")

	shortCtx, shortCancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer shortCancel()
	require.ErrorIs(handler.Shutdown(shortCtx), context.DeadlineExceeded)

	close(release)
	longCtx, longCancel := context.WithTimeout(t.Context(), time.Second)
	defer longCancel()
	require.NoError(handler.Shutdown(longCtx))
}

func TestApplyConfigCanRaceWithMidStackReads(t *testing.T) {
	handler := New(Deps{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range 100 {
				handler.ApplyConfig(ConfigSnapshot{AllowMidStackMerges: i%2 == 0})
			}
		}()
		go func() {
			defer wg.Done()
			for range 100 {
				_ = handler.allowMidStackMerges()
			}
		}()
	}
	wg.Wait()
	handler.ApplyConfig(ConfigSnapshot{AllowMidStackMerges: true})
	assert.True(t, handler.allowMidStackMerges())
}

package pullapi

import (
	"net/http"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
)

func TestHandlerRegistersPullRoutes(t *testing.T) {
	t.Parallel()

	api := humago.New(http.NewServeMux(), huma.DefaultConfig("test", "0"))
	New(Deps{}).Register(api)
	assert := assert.New(t)

	want := map[string]struct {
		method string
		path   string
		status int
	}{
		"list-pulls":                {http.MethodGet, "/pulls", http.StatusOK},
		"get-pull":                  {http.MethodGet, "/pulls/{provider}/{owner}/{name}/{number}", http.StatusOK},
		"get-pull-on-host":          {http.MethodGet, "/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}", http.StatusOK},
		"post-pr-comment":           {http.MethodPost, "/pulls/{provider}/{owner}/{name}/{number}/comments", http.StatusCreated},
		"post-pr-comment-on-host":   {http.MethodPost, "/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/comments", http.StatusCreated},
		"edit-pr-comment":           {http.MethodPatch, "/pulls/{provider}/{owner}/{name}/{number}/comments/{comment_id}", http.StatusOK},
		"edit-pr-comment-on-host":   {http.MethodPatch, "/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/comments/{comment_id}", http.StatusOK},
		"delete-pr-comment":         {http.MethodDelete, "/pulls/{provider}/{owner}/{name}/{number}/comments/{comment_id}", http.StatusNoContent},
		"delete-pr-comment-on-host": {http.MethodDelete, "/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/comments/{comment_id}", http.StatusNoContent},
		"reply-to-discussion":       {http.MethodPost, "/pulls/{provider}/{owner}/{name}/{number}/discussions/{discussion_id}/reply", http.StatusCreated},
		"reply-to-discussion-on-host": {
			http.MethodPost,
			"/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/discussions/{discussion_id}/reply",
			http.StatusCreated,
		},
		"approve-pull":         {http.MethodPost, "/pulls/{provider}/{owner}/{name}/{number}/approve", http.StatusOK},
		"approve-pull-on-host": {http.MethodPost, "/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/approve", http.StatusOK},
		"merge-pull":           {http.MethodPost, "/pulls/{provider}/{owner}/{name}/{number}/merge", http.StatusOK},
		"merge-pull-on-host":   {http.MethodPost, "/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/merge", http.StatusOK},
		"defer-merge-pull":     {http.MethodPost, "/pulls/{provider}/{owner}/{name}/{number}/merge/deferred", http.StatusAccepted},
		"defer-merge-pull-on-host": {
			http.MethodPost,
			"/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/merge/deferred",
			http.StatusAccepted,
		},
		"get-pull-diff":         {http.MethodGet, "/pulls/{provider}/{owner}/{name}/{number}/diff", http.StatusOK},
		"get-pull-diff-on-host": {http.MethodGet, "/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/diff", http.StatusOK},
		"get-pr-review-draft":   {http.MethodGet, "/pulls/{provider}/{owner}/{name}/{number}/review-draft", http.StatusOK},
		"get-pr-review-draft-on-host": {
			http.MethodGet,
			"/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/review-draft",
			http.StatusOK,
		},
		"create-pr-review-draft-comment": {
			http.MethodPost,
			"/pulls/{provider}/{owner}/{name}/{number}/review-draft/comments",
			http.StatusCreated,
		},
		"create-pr-review-draft-comment-on-host": {
			http.MethodPost,
			"/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/review-draft/comments",
			http.StatusCreated,
		},
		"apply-pr-review-suggestions": {
			http.MethodPost,
			"/pulls/{provider}/{owner}/{name}/{number}/review-suggestions/apply",
			http.StatusOK,
		},
		"apply-pr-review-suggestions-on-host": {
			http.MethodPost,
			"/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}/review-suggestions/apply",
			http.StatusOK,
		},
	}
	for operationID, expected := range want {
		var gotPath string
		var gotOperation *huma.Operation
		for path, item := range api.OpenAPI().Paths {
			for _, operation := range []*huma.Operation{
				item.Get, item.Put, item.Post, item.Delete, item.Patch,
			} {
				if operation != nil && operation.OperationID == operationID {
					gotPath = path
					gotOperation = operation
				}
			}
		}
		if assert.NotNil(gotOperation, operationID) {
			assert.Equal(expected.method, gotOperation.Method, operationID)
			assert.Equal(expected.status, gotOperation.DefaultStatus, operationID)
		}
		assert.Equal(expected.path, gotPath, operationID)
	}
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

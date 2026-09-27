package bitbucketdc_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These are the documented current repository inventory/settings envelopes.
func TestRepositoryRecoversMovedIDAndObservesWritePermission(t *testing.T) {
	c := client(t, func(r *http.Request) (int, string) {
		switch r.URL.Path {
		case "/rest/api/latest/projects/PROJECT/repos/widgets":
			return 200, `{"id":99,"slug":"widgets","project":{"key":"PROJECT"}}`
		case "/rest/api/latest/repos":
			assert.Equal(t, "ALL", r.URL.Query().Get("archived"))
			if r.URL.Query().Get("permission") == "REPO_WRITE" {
				return 200, `{"values":[{"id":42,"slug":"renamed","project":{"key":"NEW"}}],"isLastPage":true}`
			}
			if r.URL.Query().Get("start") == "0" {
				return 200, `{"values":[{"id":99,"slug":"widgets","project":{"key":"PROJECT"}}],"isLastPage":false,"nextPageStart":17}`
			}
			assert.Equal(t, "17", r.URL.Query().Get("start"))
			return 200, `{"values":[{"id":42,"slug":"renamed","archived":true,"project":{"key":"NEW"}}],"isLastPage":true}`
		case "/rest/api/latest/projects/NEW/repos/renamed/default-branch":
			return 200, `{"id":"refs/heads/main","displayId":"main"}`
		case "/rest/api/latest/projects/NEW/repos/renamed/settings/pull-requests":
			return 200, `{"mergeConfig":{"strategies":[{"id":"no-ff","enabled":true},{"id":"squash","enabled":false},{"id":"rebase-no-ff","enabled":true}]}}`
		default:
			require.FailNow(t, "unexpected endpoint", "%s", r.URL)
			return 500, `{}`
		}
	})
	repo, err := c.GetRepository(t.Context(), ref)
	require.NoError(t, err)
	assert.Equal(t, "NEW/renamed", repo.Ref.RepoPath)
	assert.Equal(t, "42", repo.PlatformExternalID)
	assert.True(t, repo.Archived)
	require.NotNil(t, repo.ViewerCanMerge)
	assert.True(t, *repo.ViewerCanMerge)
	require.NotNil(t, repo.MergeSettings)
	assert.True(t, repo.MergeSettings.AllowMergeCommit)
	assert.True(t, repo.MergeSettings.AllowRebaseMerge)
	assert.False(t, repo.MergeSettings.AllowSquashMerge)
}

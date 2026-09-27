package bitbucketdc_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/bitbucketdc"
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

type changingCredential struct{ value string }

func (c *changingCredential) Token(context.Context) (string, error) { return c.value, nil }
func (*changingCredential) Invalidate(string)                       {}

func TestPermissionInventoryReuseExpiryAndCredentialChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		source := &changingCredential{value: "user:first"}
		inventories, settings := 0, 0
		c, err := bitbucketdc.NewClient(ref.Host, source, platform.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := `{}`
			switch r.URL.Path {
			case "/rest/api/latest/projects/PROJECT/repos":
				body = `{"values":[{"id":42,"slug":"widgets","project":{"key":"PROJECT"}},{"id":43,"slug":"tools","project":{"key":"PROJECT"}}],"isLastPage":true}`
			case "/rest/api/latest/repos":
				inventories++
				assert.Equal("REPO_WRITE", r.URL.Query().Get("permission"))
				_, token, _ := r.BasicAuth()
				if token == "second" {
					body = `{"values":[],"isLastPage":true}`
				} else if r.URL.Query().Get("start") == "0" {
					body = `{"values":[{"id":42}],"isLastPage":false,"nextPageStart":17}`
				} else {
					assert.Equal("17", r.URL.Query().Get("start"))
					body = `{"values":[{"id":43}],"isLastPage":true}`
				}
			case "/rest/api/latest/projects/PROJECT/repos/widgets":
				body = `{"id":42,"slug":"widgets","project":{"key":"PROJECT"}}`
			case "/rest/api/latest/projects/PROJECT/repos/widgets/default-branch":
				body = `{"displayId":"main"}`
			case "/rest/api/latest/projects/PROJECT/repos/widgets/settings/pull-requests", "/rest/api/latest/projects/PROJECT/repos/tools/settings/pull-requests":
				settings++
				body = `{"mergeConfig":{"strategies":[{"id":"no-ff","enabled":true}]}}`
			default:
				require.FailNow(t, "unexpected endpoint", "%s", r.URL)
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}), nil)
		require.NoError(t, err)
		repos, err := c.ListRepositories(t.Context(), "PROJECT", platform.RepositoryListOptions{})
		require.NoError(t, err)
		require.Len(t, repos, 2)
		assert.True(*repos[0].ViewerCanMerge)
		assert.True(*repos[1].ViewerCanMerge)
		assert.Equal(2, inventories, "one complete two-page inventory for the entire listing")
		assert.Equal(2, settings, "merge strategies remain repository-specific")
		_, err = c.GetRepository(t.Context(), ref)
		require.NoError(t, err)
		assert.Equal(2, inventories, "individual lookup reuses the inventory")
		time.Sleep(5 * time.Minute)
		_, err = c.GetRepository(t.Context(), ref)
		require.NoError(t, err)
		assert.Equal(4, inventories, "expired inventory is fetched again")
		source.value = "user:second"
		repo, err := c.GetRepository(t.Context(), ref)
		require.NoError(t, err)
		assert.False(*repo.ViewerCanMerge)
		assert.Equal(5, inventories, "new credentials must fetch their own permissions")
		source.value = "bearer-token"
		repo, err = c.GetRepository(t.Context(), ref)
		require.NoError(t, err)
		assert.False(*repo.ViewerCanMerge)
		assert.Equal(5, inventories, "bearer credentials cannot merge")
	})
}

func TestPermissionInventoryDoesNotCachePartialFailure(t *testing.T) {
	calls := 0
	c := client(t, func(r *http.Request) (int, string) {
		switch r.URL.Path {
		case "/rest/api/latest/projects/PROJECT/repos/widgets":
			return 200, `{"id":42,"slug":"widgets","project":{"key":"PROJECT"}}`
		case "/rest/api/latest/projects/PROJECT/repos/widgets/default-branch":
			return 200, `{"displayId":"main"}`
		case "/rest/api/latest/repos":
			calls++
			if r.URL.Query().Get("start") == "0" {
				return 200, `{"values":[{"id":42}],"isLastPage":false,"nextPageStart":17}`
			}
			if calls == 2 {
				return 500, `{}`
			}
			return 200, `{"values":[],"isLastPage":true}`
		case "/rest/api/latest/projects/PROJECT/repos/widgets/settings/pull-requests":
			return 200, `{}`
		default:
			require.FailNow(t, "unexpected endpoint", "%s", r.URL)
			return 500, `{}`
		}
	})
	_, err := c.GetRepository(t.Context(), ref)
	require.Error(t, err)
	repo, err := c.GetRepository(t.Context(), ref)
	require.NoError(t, err)
	assert.True(t, *repo.ViewerCanMerge)
	assert.Equal(t, 4, calls)
}

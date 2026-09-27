package bitbucket_test

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
	"go.kenn.io/forge/platform/bitbucket"
)

type changingPermissionCredential struct{ token string }

func (c *changingPermissionCredential) Token(context.Context) (string, error) { return c.token, nil }
func (*changingPermissionCredential) Invalidate(string)                       {}

func TestPermissionInventoryCachedForWorkspaceAndCredential(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source := &changingPermissionCredential{token: "first@example.test:token"}
		permissionCalls := 0
		grant := "write"
		c, err := bitbucket.NewClient("bitbucket.org", source, platform.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := ""
			switch r.URL.Path {
			case "/2.0/repositories/team":
				body = `{"values":[{"uuid":"{11111111-1111-4111-8111-111111111111}","full_name":"team/widgets"},{"uuid":"{22222222-2222-4222-8222-222222222222}","full_name":"team/other"}]}`
			case "/2.0/repositories/team/{11111111-1111-4111-8111-111111111111}":
				body = `{"uuid":"{11111111-1111-4111-8111-111111111111}","full_name":"team/widgets"}`
			case "/2.0/user/workspaces/team/permissions/repositories":
				permissionCalls++
				body = `{"values":[{"repository":{"uuid":"{22222222-2222-4222-8222-222222222222}"},"permission":"read"}],"next":"https://api.bitbucket.org/2.0/permission-next"}`
			case "/2.0/permission-next":
				permissionCalls++
				body = `{"values":[{"repository":{"uuid":"{11111111-1111-4111-8111-111111111111}"},"permission":"` + grant + `"}]}`
			case "/2.0/repositories/other-team":
				body = `{"values":[{"uuid":"{33333333-3333-4333-8333-333333333333}","full_name":"other-team/widgets"}]}`
			case "/2.0/user/workspaces/other-team/permissions/repositories":
				permissionCalls++
				body = `{"values":[{"repository":{"uuid":"{33333333-3333-4333-8333-333333333333}"},"permission":"admin"}]}`
			default:
				require.FailNow(t, "unexpected endpoint", "%s", r.URL)
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}), nil)
		require.NoError(t, err)
		repos, err := c.ListRepositories(t.Context(), "team", platform.RepositoryListOptions{})
		require.NoError(t, err)
		require.Len(t, repos, 2)
		assert.True(t, *repos[0].ViewerCanMerge)
		assert.False(t, *repos[1].ViewerCanMerge)
		assert.Equal(t, 2, permissionCalls, "one complete inventory for both repositories")
		grant = "read"
		time.Sleep(4 * time.Minute)
		repo, err := c.GetRepository(t.Context(), ref)
		require.NoError(t, err)
		assert.True(t, *repo.ViewerCanMerge)
		assert.Equal(t, 2, permissionCalls)
		time.Sleep(time.Minute)
		repo, err = c.GetRepository(t.Context(), ref)
		require.NoError(t, err)
		assert.False(t, *repo.ViewerCanMerge)
		assert.Equal(t, 4, permissionCalls)
		grant = "write"
		source.token = "second@example.test:token"
		repo, err = c.GetRepository(t.Context(), ref)
		require.NoError(t, err)
		assert.True(t, *repo.ViewerCanMerge)
		assert.Equal(t, 6, permissionCalls)
		repos, err = c.ListRepositories(t.Context(), "other-team", platform.RepositoryListOptions{})
		require.NoError(t, err)
		require.Len(t, repos, 1)
		assert.True(t, *repos[0].ViewerCanMerge)
		assert.Equal(t, 7, permissionCalls)
		source.token = "resource-token"
		repo, err = c.GetRepository(t.Context(), ref)
		require.NoError(t, err)
		assert.False(t, *repo.ViewerCanMerge)
		assert.Equal(t, 7, permissionCalls)
	})
}

func TestIncompletePermissionInventoryIsNotCached(t *testing.T) {
	calls := 0
	broken := true
	c := client(t, "user@example.test:token", func(r *http.Request) (int, string) {
		switch r.URL.Path {
		case "/2.0/repositories/team/{11111111-1111-4111-8111-111111111111}":
			return 200, `{"uuid":"{11111111-1111-4111-8111-111111111111}","full_name":"team/widgets"}`
		case "/2.0/user/workspaces/team/permissions/repositories":
			calls++
			return 200, `{"values":[{"repository":{"uuid":"{11111111-1111-4111-8111-111111111111}"},"permission":"write"}],"next":"https://api.bitbucket.org/2.0/permission-next"}`
		case "/2.0/permission-next":
			if broken {
				return 500, `{}`
			}
			return 200, `{"values":[]}`
		default:
			require.FailNow(t, "unexpected endpoint", "%s", r.URL)
			return 500, `{}`
		}
	})
	_, err := c.GetRepository(t.Context(), ref)
	require.Error(t, err)
	broken = false
	repo, err := c.GetRepository(t.Context(), ref)
	require.NoError(t, err)
	assert.True(t, *repo.ViewerCanMerge)
	assert.Equal(t, 2, calls, "failed pagination must retry the full inventory")
}

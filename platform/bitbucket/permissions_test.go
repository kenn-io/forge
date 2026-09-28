package bitbucket_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shapes and strategy names are from the current Cloud repositories/refs API.
func TestRepositoryMergeObservesPermissionAndDestinationStrategies(t *testing.T) {
	for _, permission := range []string{"read", "write", "admin"} {
		t.Run(permission, func(t *testing.T) {
			c := client(t, "user@example.com:token", func(r *http.Request) (int, string) {
				switch r.URL.Path {
				case "/2.0/repositories/{}/{11111111-1111-4111-8111-111111111111}":
					return 200, `{"uuid":"{11111111-1111-4111-8111-111111111111}","full_name":"team/widgets","mainbranch":{"name":"main"}}`
				case "/2.0/user/workspaces/team/permissions/repositories":
					return 200, `{"values":[{"permission":"admin","repository":{"uuid":"{22222222-2222-4222-8222-222222222222}"}}],"next":"https://api.bitbucket.org/2.0/permission-page-two"}`
				case "/2.0/permission-page-two":
					return 200, `{"values":[{"permission":"` + permission + `","repository":{"uuid":"{11111111-1111-4111-8111-111111111111}"}}]}`
				case "/2.0/repositories/team/{11111111-1111-4111-8111-111111111111}/refs/branches/main":
					return 200, `{"merge_strategies":["squash","fast_forward","rebase_fast_forward"],"default_merge_strategy":"squash"}`
				default:
					require.FailNow(t, "unexpected endpoint", "%s", r.URL)
					return 500, `{}`
				}
			})
			repo, err := c.GetRepository(t.Context(), ref)
			require.NoError(t, err)
			require.NotNil(t, repo.ViewerCanMerge)
			assert.Equal(t, permission != "read", *repo.ViewerCanMerge)
			require.NotNil(t, repo.MergeSettings)
			assert.True(t, repo.MergeSettings.AllowSquashMerge)
			assert.True(t, repo.MergeSettings.AllowRebaseMerge)
			assert.False(t, repo.MergeSettings.AllowMergeCommit)
		})
	}
}

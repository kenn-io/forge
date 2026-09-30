package bitbucketdc_test

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/bitbucketdc"
)

type credential string

func (c credential) Token(context.Context) (string, error) { return string(c), nil }
func (credential) Invalidate(string)                       {}

var ref = platform.RepoRef{Platform: platform.KindBitbucket, Host: "bitbucket.example.com", Owner: "PROJECT", Name: "widgets", PlatformID: 42}

func client(t *testing.T, handler func(*http.Request) (int, string)) *bitbucketdc.Client {
	assert := assert.New(t)
	t.Helper()
	c, err := bitbucketdc.NewClient(ref.Host, credential("user:token"), platform.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		assert.Equal("bitbucket.example.com", r.URL.Host)
		user, token, ok := r.BasicAuth()
		require.True(t, ok)
		assert.Equal("user", user)
		assert.Equal("token", token)
		code, body := handler(r)
		return &http.Response{StatusCode: code, Status: http.StatusText(code), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}), nil)
	require.NoError(t, err)
	return c
}

func TestRepositoryIdentityAndDefaultBranch(t *testing.T) {
	assert := assert.New(t)
	c := client(t, func(r *http.Request) (int, string) {
		switch r.URL.Path {
		case "/rest/api/latest/projects/PROJECT/repos/widgets":
			return 200, `{"id":42,"slug":"widgets","project":{"key":"PROJECT"},"links":{"clone":[{"name":"http","href":"https://bitbucket.example.com/scm/project/widgets.git"}]}}`
		case "/rest/api/latest/repos":
			return 200, `{"values":[],"isLastPage":true}`
		case "/rest/api/latest/projects/PROJECT/repos/widgets/default-branch":
			return 200, `{"id":"refs/heads/main","displayId":"main"}`
		default:
			return 404, `{}`
		}
	})
	repo, err := c.GetRepository(t.Context(), ref)
	require.NoError(t, err)
	assert.Equal(int64(42), repo.Ref.PlatformID)
	assert.Equal("main", repo.DefaultBranch)
	assert.Equal("https://bitbucket.example.com/scm/project/widgets.git", repo.CloneURL)
	wrong := ref
	wrong.PlatformID = 99
	_, err = c.GetRepository(t.Context(), wrong)
	require.ErrorIs(t, err, platform.ErrProviderContract)
}

func TestPullPaginationUsesNextPageStart(t *testing.T) {
	assert := assert.New(t)
	c := client(t, func(r *http.Request) (int, string) {
		assert.Equal("/rest/api/latest/projects/PROJECT/repos/widgets/pull-requests", r.URL.Path)
		assert.Equal("OPEN", r.URL.Query().Get("state"))
		switch r.URL.Query().Get("start") {
		case "0":
			return 200, `{"values":[{"id":7,"state":"OPEN","title":"First","fromRef":{"id":"refs/heads/feature","latestCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repository":{"links":{"clone":[{"name":"http","href":"https://bitbucket.example.com/scm/project/widgets.git"}]}}},"createdDate":1788220800000}],"isLastPage":false,"nextPageStart":37}`
		case "37":
			return 200, `{"values":[{"id":8,"state":"OPEN","title":"Second"}],"isLastPage":true}`
		default:
			return 400, `{}`
		}
	})
	rows, err := c.ListOpenMergeRequests(t.Context(), ref)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal("feature", rows[0].HeadBranch)
	assert.Equal("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", rows[0].HeadSHA)
	assert.Equal(8, rows[1].Number)
}

func TestIncompletePageFailsInventory(t *testing.T) {
	assert := assert.New(t)
	c := client(t, func(*http.Request) (int, string) {
		return 200, `{"values":[{"id":7,"state":"OPEN"}],"isLastPage":false}`
	})
	rows, err := c.ListOpenMergeRequests(t.Context(), ref)
	require.ErrorIs(t, err, platform.ErrProviderContract)
	assert.Nil(rows)
}

func TestMergeBindsVersionAndRejectsMovedHead(t *testing.T) {
	assert := assert.New(t)
	for _, head := range []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"} {
		t.Run(head, func(t *testing.T) {
			writes := 0
			c := client(t, func(r *http.Request) (int, string) {
				if r.Method == http.MethodGet {
					return 200, `{"id":7,"version":13,"state":"OPEN","fromRef":{"latestCommit":"` + head + `"}}`
				}
				writes++
				assert.Equal("/rest/api/latest/projects/PROJECT/repos/widgets/pull-requests/7/merge", r.URL.Path)
				assert.Equal("13", r.URL.Query().Get("version"))
				var body map[string]any
				require.NoError(t, json.UnmarshalRead(r.Body, &body))
				assert.Equal("squash", body["strategyId"])
				return 200, `{"id":7,"state":"MERGED","properties":{"mergeCommit":{"id":"cccccccccccccccccccccccccccccccccccccccc"}}}`
			})
			result, err := c.MergeMergeRequest(t.Context(), ref, 7, "Title", "Body", "squash", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			if head[0] == 'b' {
				require.ErrorIs(t, err, platform.ErrStaleState)
				assert.Zero(writes)
			} else {
				require.NoError(t, err)
				assert.True(result.Merged)
				assert.Equal(1, writes)
			}
		})
	}
}

func TestCIChecksUseStatusInventory(t *testing.T) {
	assert := assert.New(t)
	c := client(t, func(r *http.Request) (int, string) {
		assert.Equal("/rest/build-status/latest/commits/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", r.URL.Path)
		return 200, `{"values":[{"key":"build","name":"Build","state":"SUCCESSFUL","createdDate":1788220800000,"updatedDate":1788220860000}],"isLastPage":true}`
	})
	checks, err := c.ListCIChecks(t.Context(), ref, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.NoError(t, err)
	require.Len(t, checks, 1)
	assert.Equal("success", checks[0].Conclusion)
	assert.Equal(int64(1788220800000), checks[0].StartedAt.UnixMilli())
	assert.Equal(int64(1788220860000), checks[0].CompletedAt.UnixMilli())
}

func TestEmptyRepositoryHasNoDefaultBranch(t *testing.T) {
	c := client(t, func(r *http.Request) (int, string) {
		if r.URL.Path == "/rest/api/latest/repos" {
			return 200, `{"values":[],"isLastPage":true}`
		}
		if strings.HasSuffix(r.URL.Path, "/default-branch") {
			return 404, `{}`
		}
		return 200, `{"id":42,"slug":"widgets","project":{"key":"PROJECT"}}`
	})
	repo, err := c.GetRepository(t.Context(), ref)
	require.NoError(t, err)
	assert.Empty(t, repo.DefaultBranch)
	assert.Equal(t, int64(42), repo.Ref.PlatformID)
}

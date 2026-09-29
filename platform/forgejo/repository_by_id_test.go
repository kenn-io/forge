package forgejo

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func TestGetRepositoryWithPinnedIDFetchesByIDAndReturnsRenamedRoute(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var paths []string
	transport := platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		body := `{"id":1001,"name":"new-name","full_name":"new-owner/new-name","owner":{"login":"new-owner"}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	client, err := NewClient("code.example.org", testTokenSource("fixture-token"),
		WithServerVersion("13.0.0+gitea-1.26.0"), WithTransport(transport))
	require.NoError(err)

	repo, err := client.GetRepository(t.Context(), platform.RepoRef{
		Platform: platform.KindForgejo, Host: "code.example.org",
		Owner: "old-owner", Name: "old-name", PlatformID: 1001,
	})
	require.NoError(err)

	assert.Equal([]string{"/api/v1/repositories/1001"}, paths)
	assert.Equal("new-owner", repo.Ref.Owner)
	assert.Equal("new-name", repo.Ref.Name)
	assert.Equal(int64(1001), repo.Ref.PlatformID)
}

package github_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/github"
)

func TestProviderGetRepositoryWithPinnedIDFollowsRenameAndUsesRESTID(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var urls []string
	transport := platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		urls = append(urls, req.URL.String())
		body := `{"id":1001,"node_id":"R_kgDOsynthetic","name":"new-name","owner":{"login":"new-owner"}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	httpClient := &http.Client{Transport: transport}
	client, err := github.NewClient(github.ClientConfig{
		Read: httpClient, Write: httpClient, Notifications: httpClient, Clock: time.Now,
	})
	require.NoError(err)
	provider, err := github.NewProvider(github.ProviderConfig{Host: "github.com", Client: client, Clock: time.Now})
	require.NoError(err)

	repo, err := provider.GetRepository(t.Context(), platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.com",
		Owner: "old-owner", Name: "old-name", PlatformID: 1001,
	})
	require.NoError(err)

	assert.Equal([]string{"https://api.github.com/repositories/1001"}, urls)
	assert.Equal("new-owner", repo.Ref.Owner)
	assert.Equal("new-name", repo.Ref.Name)
	assert.Equal("new-owner/new-name", repo.Ref.RepoPath)
	assert.Equal(int64(1001), repo.Ref.PlatformID)
}

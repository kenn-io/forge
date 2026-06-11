package github

import (
	"context"
	"net/http"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestLiveGitHubRateLimitSnapshotUsesGoGitHub(t *testing.T) {
	skipUnlessLiveGitHubTests(t)
	require := require.New(t)
	assert := Assert.New(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := gh.NewClient(nil)
	if token := liveGitHubToken(); token != "" {
		client = gh.NewClient(oauthHTTPClient(token))
	}

	limits, resp, err := client.RateLimit.Get(ctx)
	require.NoError(err)
	require.NotNil(resp)
	require.NotNil(limits)
	require.NotNil(limits.Core)

	assert.Equal(http.StatusOK, resp.StatusCode)
	assert.Positive(limits.Core.Limit)
	assert.GreaterOrEqual(limits.Core.Remaining, 0)
	assert.LessOrEqual(limits.Core.Remaining, limits.Core.Limit)
	assert.True(limits.Core.Reset.After(time.Now().Add(-time.Minute)))
}

func oauthHTTPClient(token string) *http.Client {
	return oauth2.NewClient(
		context.Background(),
		oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
	)
}

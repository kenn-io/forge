package github

import (
	"net/http"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicGitHubAPIGuardTransportBlocksAPIGitHub(t *testing.T) {
	assert := Assert.New(t)
	baseCalls := 0
	transport := publicGitHubAPIGuardTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		baseCalls++
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})}
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/rate_limit", nil)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)

	require.ErrorIs(t, err, ErrPublicGitHubAPIBlocked)
	assert.Nil(resp)
	assert.Equal(0, baseCalls)
}

func TestPublicGitHubAPIGuardTransportAllowsOtherHosts(t *testing.T) {
	assert := Assert.New(t)
	baseCalls := 0
	transport := publicGitHubAPIGuardTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		baseCalls++
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})}
	req, err := http.NewRequest(http.MethodGet, "https://github.example.com/api/v3/rate_limit", nil)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(http.StatusNoContent, resp.StatusCode)
	assert.Equal(1, baseCalls)
}

func TestNewClientBlocksPublicGitHubAPIInDefaultTests(t *testing.T) {
	client, err := NewClient("fake-token", "github.com", nil, nil)
	require.NoError(t, err)
	live, ok := client.(*liveClient)
	require.True(t, ok)
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/rate_limit", nil)
	require.NoError(t, err)

	resp, err := live.httpClient.Do(req)

	require.ErrorIs(t, err, ErrPublicGitHubAPIBlocked)
	require.Nil(t, resp)
}

func TestNewGraphQLFetcherBlocksPublicGitHubAPIInDefaultTests(t *testing.T) {
	fetcher := NewGraphQLFetcher("fake-token", "github.com", nil, nil)

	_, err := fetcher.FetchRepoPRs(t.Context(), "acme", "widgets")

	require.ErrorIs(t, err, ErrPublicGitHubAPIBlocked)
}

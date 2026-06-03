package testenv

import (
	"net/http"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestGitHubAPIGuardTransportBlocksAPIGitHub(t *testing.T) {
	assert := Assert.New(t)
	baseCalls := 0
	transport := githubAPIGuardTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		baseCalls++
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})}
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/rate_limit", nil)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)

	require.ErrorIs(t, err, ErrGitHubAPIBlocked)
	assert.Nil(resp)
	assert.Equal(0, baseCalls)
}

func TestGitHubAPIGuardTransportAllowsOtherHosts(t *testing.T) {
	assert := Assert.New(t)
	baseCalls := 0
	transport := githubAPIGuardTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		baseCalls++
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})}
	req, err := http.NewRequest(http.MethodGet, "https://example.com/status", nil)
	require.NoError(t, err)

	resp, err := transport.RoundTrip(req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(http.StatusNoContent, resp.StatusCode)
	assert.Equal(1, baseCalls)
}

func TestInstalledGitHubAPIGuardBlocksDefaultClient(t *testing.T) {
	assert := Assert.New(t)
	require.True(t, GitHubAPIGuardInstalled())

	resp, err := http.Get("https://api.github.com/rate_limit")

	require.ErrorIs(t, err, ErrGitHubAPIBlocked)
	assert.Nil(resp)
}

func TestInstallGitHubAPIGuardWrapsDefaultTransportOnce(t *testing.T) {
	assert := Assert.New(t)
	original := http.DefaultTransport
	defer func() {
		http.DefaultTransport = original
	}()
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})
	http.DefaultTransport = base

	InstallGitHubAPIGuard()
	InstallGitHubAPIGuard()

	assert.True(GitHubAPIGuardInstalled())
	guard, ok := http.DefaultTransport.(githubAPIGuardTransport)
	require.True(t, ok)
	_, nested := guard.base.(githubAPIGuardTransport)
	assert.False(nested)
}

func TestInstallGitHubAPIGuardSkipsWhenLiveGitHubTestsAreEnabled(t *testing.T) {
	assert := Assert.New(t)
	original := http.DefaultTransport
	defer func() {
		http.DefaultTransport = original
	}()
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	})
	http.DefaultTransport = base
	t.Setenv("MIDDLEMAN_LIVE_GITHUB_TESTS", "1")

	InstallGitHubAPIGuard()

	assert.False(GitHubAPIGuardInstalled())
	_, guarded := http.DefaultTransport.(githubAPIGuardTransport)
	assert.False(guarded)
	_, stillBase := http.DefaultTransport.(roundTripFunc)
	assert.True(stillBase)
}

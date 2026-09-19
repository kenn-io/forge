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

func TestPublicClientKeepsReadAndViewerTransportsSeparate(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	readCalls, writeCalls := 0, 0
	read := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		readCalls++
		assert.Equal("https://api.github.com/repos/team-a/project-a", req.URL.String())
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":17,"name":"project-a","owner":{"login":"team-a"},"permissions":{"push":true}}`)), Request: req}, nil
	})}
	write := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		writeCalls++
		assert.Equal("https://api.github.com/repos/team-a/project-a", req.URL.String())
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":17,"permissions":{"push":false}}`)), Request: req}, nil
	})}
	client, err := github.NewClient(github.ClientConfig{
		Host: "github.com", Read: read, Write: write, Notifications: write,
		Clock:          time.Now,
		Authentication: github.Authentication{InstallationActive: func(string) bool { return true }},
	})
	require.NoError(err)
	assert.Zero(readCalls + writeCalls)
	repo, err := client.GetRepository(t.Context(), "team-a", "project-a")
	require.NoError(err)
	assert.Equal(int64(17), repo.GetID())
	assert.False(repo.Permissions.GetPush())
}

func TestPublicClientEmptyHostUsesGitHubDotCom(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	requests := 0
	transport := platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		assert.Equal("https://api.github.com/repos/team-a/project-a", req.URL.String())
		return &http.Response{
			StatusCode: 200,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"id":17,"name":"project-a","owner":{"login":"team-a"}}`,
			)),
			Request: req,
		}, nil
	})
	httpClient := &http.Client{Transport: transport}
	client, err := github.NewClient(github.ClientConfig{
		Read: httpClient, Write: httpClient, Notifications: httpClient, Clock: time.Now,
	})
	require.NoError(err)

	repo, err := client.GetRepository(t.Context(), "team-a", "project-a")
	require.NoError(err)
	assert.Equal(int64(17), repo.GetID())
	assert.Equal(1, requests)
}

func TestRateLimitSnapshotPrefersCoreResponseHeaders(t *testing.T) {
	for _, tc := range []struct {
		name      string
		resource  string
		remaining int
		reset     int64
	}{
		{"core headers override optimistic body", "core", 0, 2000000000},
		{"body without headers", "", 5000, 2000003600},
		{"other resource does not replace core", "search", 5000, 2000003600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			transport := platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				assert.Equal("/rate_limit", req.URL.Path)
				header := make(http.Header)
				if tc.resource != "" {
					header.Set("X-RateLimit-Resource", tc.resource)
					header.Set("X-RateLimit-Limit", "5000")
					header.Set("X-RateLimit-Remaining", "0")
					header.Set("X-RateLimit-Reset", "2000000000")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     header,
					Body: io.NopCloser(strings.NewReader(`{"resources":{
						"core":{"limit":5000,"remaining":5000,"reset":2000003600},
						"graphql":{"limit":5000,"remaining":4321,"reset":2000001800}
					}}`)),
					Request: req,
				}, nil
			})
			httpClient := &http.Client{Transport: transport}
			client, err := github.NewClient(github.ClientConfig{
				Read: httpClient, Write: httpClient, Notifications: httpClient, Clock: time.Now,
			})
			require.NoError(err)
			snapshot, err := client.GetRateLimitSnapshot(t.Context())
			require.NoError(err)
			require.NotNil(snapshot.Core)
			require.NotNil(snapshot.GraphQL)
			assert.Equal(tc.remaining, snapshot.Core.Remaining)
			assert.Equal(tc.reset, snapshot.Core.Reset.Unix())
			assert.Equal(4321, snapshot.GraphQL.Remaining)
		})
	}
}

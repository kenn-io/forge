package github_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestViewerPermissionOverlayRevalidatesWithETag(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	viewerETag := `W/"v1"`
	viewerPush := true
	var validators []string
	read := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
			`{"id":17,"name":"project-a","owner":{"login":"team-a"},"permissions":{"push":false}}`,
		)), Request: req}, nil
	})}
	write := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		validator := req.Header.Get("If-None-Match")
		validators = append(validators, validator)
		if validator == viewerETag {
			return &http.Response{StatusCode: http.StatusNotModified, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
		}
		header := make(http.Header)
		header.Set("ETag", viewerETag)
		body := fmt.Sprintf(
			`{"id":17,"permissions":{"push":%t},"allow_squash_merge":true,"allow_merge_commit":false,"allow_rebase_merge":false}`,
			viewerPush,
		)
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	client, err := github.NewClient(github.ClientConfig{
		Host: "github.com", Read: read, Write: write, Notifications: write, Clock: time.Now,
		Authentication: github.Authentication{InstallationActive: func(string) bool { return true }},
	})
	require.NoError(err)

	repo, err := client.GetRepository(t.Context(), "team-a", "project-a")
	require.NoError(err)
	assert.True(repo.GetPermissions().GetPush())

	repo, err = client.GetRepository(t.Context(), "Team-A", "Project-A")
	require.NoError(err)
	assert.True(repo.GetPermissions().GetPush(), "a 304 reuses the cached viewer overlay")
	assert.True(repo.GetAllowSquashMerge())
	assert.False(repo.GetAllowMergeCommit())

	viewerETag = `W/"v2"`
	viewerPush = false
	repo, err = client.GetRepository(t.Context(), "team-a", "project-a")
	require.NoError(err)
	assert.False(repo.GetPermissions().GetPush(), "a changed repository returns fresh permissions")

	assert.Equal([]string{"", `W/"v1"`, `W/"v1"`}, validators,
		"every overlay read revalidates the cached ETag")
}

func TestListNotificationPageUsesUserScopedConditionalRequest(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	const lastModified = "Wed, 01 May 2026 10:00:00 GMT"
	var requests []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r)
		if r.Header.Get("If-Modified-Since") == lastModified {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Last-Modified", lastModified)
		_, _ = w.Write([]byte(`[{"id":"1","unread":true,"reason":"mention",
			"repository":{"name":"project-a","owner":{"login":"team-a"}},
			"subject":{"title":"Review","type":"PullRequest",
			"url":"https://api.github.com/repos/team-a/project-a/pulls/7"}}]`))
	}))
	defer server.Close()
	httpClient := server.Client()
	client, err := github.NewClient(github.ClientConfig{
		Read: httpClient, Write: httpClient, Notifications: httpClient, Clock: time.Now,
		APIBase: server.URL + "/api/v3/", UploadBase: server.URL + "/api/uploads/",
	})
	require.NoError(err)
	since := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

	page, err := client.ListNotificationPage(t.Context(), platform.NotificationListOptions{
		All: true, Participating: true, Since: &since, Page: 1,
		RepoOwner: "team-a", RepoName: "project-a",
	}, "")
	require.NoError(err)
	assert.False(page.NotModified)
	assert.Equal(lastModified, page.LastModified)
	require.Len(page.Threads, 1)
	assert.Equal("project-a", page.Threads[0].RepoName)

	page, err = client.ListNotificationPage(t.Context(), platform.NotificationListOptions{
		All: true, Page: 1,
	}, lastModified)
	require.NoError(err)
	assert.True(page.NotModified)
	assert.Empty(page.Threads)

	require.Len(requests, 2)
	assert.Equal("/api/v3/notifications", requests[0].URL.Path,
		"the listing is host-wide even when a repository is supplied")
	assert.Equal("true", requests[0].URL.Query().Get("participating"))
	assert.Equal("2026-05-01T09:00:00Z", requests[0].URL.Query().Get("since"))
	assert.Empty(requests[0].Header.Get("If-Modified-Since"))
	assert.Equal(lastModified, requests[1].Header.Get("If-Modified-Since"))
}

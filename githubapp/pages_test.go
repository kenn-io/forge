package githubapp_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/githubapp"
	"go.kenn.io/forge/platform"
)

func TestInstallationPagesKeepCursorScopeAndExhaustion(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	calls := 0
	hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		assert.Equal("/app/installations", req.URL.Path)
		assert.Equal("2", req.URL.Query().Get("per_page"))
		header := make(http.Header)
		body := `[{"id":21,"app_id":11,"account":{"id":31,"login":"team-a","type":"Organization"},"repository_selection":"selected"},{"id":22,"app_id":11,"account":{"id":32}}]`
		if req.URL.Query().Get("page") == "1" {
			header.Set("Link", `<https://api.github.com/app/installations?per_page=2&page=2>; rel=next`)
		} else {
			body = `[{"id":23,"app_id":11,"account":{"id":33}}]`
		}
		return &http.Response{StatusCode: 200, Header: header, Request: req, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	meter, err := platform.NewMeter(ctx, platform.Budget{MaxRecords: 3, MaxNodes: 1, MaxBytes: 4096, MaxOutputBytes: 4096})
	require.NoError(err)
	client := githubapp.NewClient("github.com", hc)
	first, err := client.ListInstallationsPage(ctx, "fixture-jwt", 11, githubapp.PageQuery{Size: 2}, meter)
	require.NoError(err)
	require.Len(first.Items, 2)
	assert.False(first.Exhausted)
	assert.NotEmpty(first.NextCursor)
	_, err = client.ListInstallationsPage(ctx, "fixture-jwt", 12, githubapp.PageQuery{Size: 2, Cursor: first.NextCursor}, meter)
	require.ErrorIs(err, platform.ErrInvalidArgument)
	require.Equal(1, calls)
	second, err := client.ListInstallationsPage(ctx, "fixture-jwt", 11, githubapp.PageQuery{Size: 2, Cursor: first.NextCursor}, meter)
	require.NoError(err)
	require.Len(second.Items, 1)
	assert.Equal(int64(23), second.Items[0].ID)
	assert.True(second.Exhausted)
}

func TestRepositoryPageReturnsStableIDsAndDoesNotDrain(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	calls := 0
	hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		assert.Equal("/installation/repositories", req.URL.Path)
		header := make(http.Header)
		header.Set("Link", `<https://api.github.com/installation/repositories?per_page=1&page=2>; rel="next"`)
		return &http.Response{StatusCode: 200, Header: header, Request: req, Body: io.NopCloser(strings.NewReader(`{"total_count":2,"repositories":[{"id":9007199254740993,"full_name":"team-a/project-a","name":"project-a","default_branch":"main","owner":{"id":31,"login":"team-a"}}]}`))}, nil
	})}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	meter, err := platform.NewMeter(ctx, platform.Budget{MaxRecords: 1, MaxNodes: 1, MaxBytes: 4096, MaxOutputBytes: 4096})
	require.NoError(err)
	page, err := githubapp.NewClient("github.com", hc).ListInstallationRepositoriesPage(ctx, "fixture-token", 21, githubapp.PageQuery{Size: 1}, meter)
	require.NoError(err)
	require.Len(page.Items, 1)
	assert.Equal(int64(9007199254740993), page.Items[0].ID)
	assert.False(page.Exhausted)
	assert.NotEmpty(page.NextCursor)
	assert.Equal(1, calls)
}

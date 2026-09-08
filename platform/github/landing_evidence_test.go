package github_test

import (
	"context"
	"fmt"
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

func TestLandingEvidencePages(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	sha := strings.Repeat("a", 40)
	calls := 0
	hc := &http.Client{Transport: platform.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		assert.Equal("2022-11-28", r.Header.Get("X-GitHub-Api-Version"))
		header := make(http.Header)
		body := ""
		switch r.URL.Path {
		case "/repos/example/project/commits/" + sha + "/pulls":
			assert.Equal("100", r.URL.Query().Get("per_page"))
			body = `[{"id":7,"number":3,"base":{"repo":{"id":12}}}]`
			if r.URL.Query().Get("page") == "1" {
				header.Set("Link", fmt.Sprintf(`<https://api.github.com%s?page=2&per_page=100>; rel="next"`, r.URL.Path))
			}
		case "/repos/example/project/pulls/3":
			body = fmt.Sprintf(`{"id":7,"number":3,"merged":true,"merge_commit_sha":%q,"commits":1,"base":{"ref":"main","repo":{"id":12}},"head":{"sha":%q,"repo":{"id":15}}}`, sha, sha)
		case "/repos/example/project/pulls/3/commits":
			body = fmt.Sprintf(`[{"sha":%q}]`, sha)
		default:
			require.Fail("unexpected path", r.URL.String())
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	c, err := github.NewClient(github.ClientConfig{Read: hc, Write: hc, Notifications: hc, Clock: time.Now})
	require.NoError(err)
	p, err := github.NewProvider(github.ProviderConfig{Host: "github.com", Client: c, Clock: time.Now})
	require.NoError(err)
	var reader platform.LandingEvidenceReader = p
	route := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "example", Name: "project"}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	page, err := reader.ListLandingAssociations(ctx, route, sha, "")
	require.NoError(err)
	assert.False(page.Exhausted)
	require.NotEmpty(page.NextCursor)
	last, err := reader.ListLandingAssociations(ctx, route, sha, page.NextCursor)
	require.NoError(err)
	assert.True(last.Exhausted)
	_, err = reader.ListLandingAssociations(ctx, route, strings.Repeat("b", 40), page.NextCursor)
	require.Error(err)
	detail, err := reader.GetLandingChange(ctx, route, page.Items[0])
	require.NoError(err)
	assert.Equal(int64(12), detail.TargetID)
	assert.Equal(new(int64(15)), detail.SourceID)
	assert.Equal(sha, detail.Terminal)
	assert.Nil(detail.SquashSHA)
	sources, err := reader.ListLandingSource(ctx, route, detail.Ref, "")
	require.NoError(err)
	assert.Equal([]string{sha}, sources.Items)
	assert.True(sources.Exhausted)
	assert.Equal(4, calls)
}

func TestLandingMissingHeadStaysAbsent(t *testing.T) {
	hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":7,"number":3,"merged":true,"base":{"repo":{"id":12}}}`)), Request: req}, nil
	})}
	c, err := github.NewClient(github.ClientConfig{Read: hc, Write: hc, Notifications: hc, Clock: time.Now})
	require.NoError(t, err)
	d, err := c.GetLandingChange(t.Context(), platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "example", Name: "project"}, platform.LandingChangeRef{ID: 7, Number: 3})
	require.NoError(t, err)
	assert.Nil(t, d.SourceHead)
}

func TestLandingRejectsCrossInstanceSource(t *testing.T) {
	hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":7,"number":3,"merged":true,"base":{"repo":{"id":12}},"head":{"repo":{"id":12,"html_url":"https://other.example/project"}}}`)), Request: req}, nil
	})}
	c, err := github.NewClient(github.ClientConfig{Read: hc, Write: hc, Notifications: hc, Clock: time.Now})
	require.NoError(t, err)
	_, err = c.GetLandingChange(t.Context(), platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "example", Name: "project"}, platform.LandingChangeRef{ID: 7, Number: 3})
	assert.ErrorIs(t, err, platform.ErrLandingIdentityMismatch)
}

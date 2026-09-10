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

func TestLandingRoles(t *testing.T) {
	for _, tc := range []struct {
		name, fields   string
		author, merger *platform.Account
	}{
		{"human author bot merger", `"user":{"id":21,"login":"user-a","type":"User"},"merged_by":{"id":22,"login":"merge-app","type":"Bot"}`,
			&platform.Account{ID: new(int64(21)), Login: new("user-a"), Type: platform.AccountTypeUser},
			&platform.Account{ID: new(int64(22)), Login: new("merge-app"), Type: platform.AccountTypeBot}},
		{"same ID distinct roles", `"user":{"id":21,"type":"User"},"merged_by":{"id":21,"login":"user-a","type":"User"}`,
			&platform.Account{ID: new(int64(21)), Type: platform.AccountTypeUser},
			&platform.Account{ID: new(int64(21)), Login: new("user-a"), Type: platform.AccountTypeUser}},
		{"absent roles", `"user":null,"merged_by":null`, nil, nil},
		{"missing ID empty login", `"user":{"login":"","type":"Organization"}`,
			&platform.Account{Login: new(""), Type: platform.AccountTypeOrganization}, nil},
		{"invalid IDs unknown types", `"user":{"id":0,"login":"robot[bot]"},"merged_by":{"id":-1,"type":"FutureType"}`,
			&platform.Account{ID: new(int64(0)), Login: new("robot[bot]"), Type: platform.AccountTypeUnknown},
			&platform.Account{ID: new(int64(-1)), Type: platform.AccountTypeUnknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			calls := 0
			hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				assert.Equal("/repos/example/project/pulls/3", req.URL.Path)
				body := `{"id":7,"number":3,` + tc.fields + `}`
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})}
			c, err := github.NewClient(github.ClientConfig{Read: hc, Write: hc, Notifications: hc, Clock: time.Now})
			require.NoError(err)
			d, err := c.GetLandingChange(t.Context(), platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "example", Name: "project"}, platform.LandingChangeRef{ID: 7, Number: 3})
			require.NoError(err)
			assert.Equal(tc.author, d.Author)
			assert.Equal(tc.merger, d.Merger)
			assert.Nil(d.OpenedAt)
			assert.Nil(d.MergedAt)
			assert.Equal(1, calls)
		})
	}
}

func TestLandingTimes(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	hc := &http.Client{Transport: platform.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"id":7,"number":3,"created_at":"2026-01-02T03:04:05+02:00","merged_at":"2026-01-03T18:04:05-07:00"}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	c, err := github.NewClient(github.ClientConfig{Read: hc, Write: hc, Notifications: hc, Clock: time.Now})
	require.NoError(err)
	d, err := c.GetLandingChange(t.Context(), platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "example", Name: "project"}, platform.LandingChangeRef{ID: 7, Number: 3})
	require.NoError(err)
	assert.Equal(new(time.Date(2026, 1, 2, 1, 4, 5, 0, time.UTC)), d.OpenedAt)
	assert.Equal(new(time.Date(2026, 1, 4, 1, 4, 5, 0, time.UTC)), d.MergedAt)
}

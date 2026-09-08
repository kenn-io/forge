package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

func TestRoutedProviderLandingEvidence(t *testing.T) {
	for _, archive := range []bool{false, true} {
		name := "foreground"
		if archive {
			name = "archive"
		}
		t.Run(name, func(t *testing.T) {
			require, assert := require.New(t), assert.New(t)
			var paths []string
			hc := &http.Client{Transport: platform.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
				paths = append(paths, r.URL.Path)
				var body string
				switch r.URL.Path {
				case "/repos/example/project/commits/abc/pulls":
					body = `[{"id":7,"number":3,"base":{"repo":{"id":12}}}]`
				case "/repos/example/project/pulls/3":
					body = `{"id":7,"number":3,"merged":true,"merge_commit_sha":"abc","base":{"repo":{"id":12}}}`
				case "/repos/example/project/pulls/3/commits":
					body = `[{"sha":"def"}]`
				default:
					require.Fail("unexpected request", r.URL.String())
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			client, err := platformgithub.NewClient(platformgithub.ClientConfig{Read: hc, Write: hc, Notifications: hc, Clock: time.Now})
			require.NoError(err)
			exact := &Route{Key: RouteKey{Host: "github.com", Owner: "example", Name: "project"}, Client: client}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			if archive {
				exact.Client, exact.ArchiveClient = &mockClient{}, client
				ctx = WithArchiveSyncBudget(ctx)
			}
			router, err := NewHostRouter("github.com",
				&Route{Key: RouteKey{Host: "github.com"}, Client: &mockClient{}},
				&Route{Key: RouteKey{Host: "github.com", Owner: "example"}, Client: &mockClient{}}, exact)
			require.NoError(err)
			routed, err := NewRoutedClient(router)
			require.NoError(err)
			provider := newTestGitHubProvider(t, "github.com", routed)
			require.True(provider.LandingEvidenceSupport().Inventory)
			ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "example", Name: "project"}
			page, err := provider.ListLandingAssociations(ctx, ref, "abc", "")
			require.NoError(err)
			require.Equal([]platform.LandingChangeRef{{ID: 7, Number: 3, TargetID: 12}}, page.Items)
			detail, err := provider.GetLandingChange(ctx, ref, page.Items[0])
			require.NoError(err)
			assert.Equal("abc", detail.Terminal)
			sources, err := provider.ListLandingSource(ctx, ref, page.Items[0], "")
			require.NoError(err)
			assert.Equal([]string{"def"}, sources.Items)
			assert.True(sources.Exhausted)
			assert.Equal([]string{"/repos/example/project/commits/abc/pulls", "/repos/example/project/pulls/3", "/repos/example/project/pulls/3/commits"}, paths)
		})
	}
}

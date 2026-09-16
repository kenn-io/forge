package generated_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
)

func TestResponseClientArrayQueryEncoding(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		query      url.Values
		request    func(context.Context, *generated.Client) error
	}{
		{
			name: "explode false joins activity filters",
			body: `{}`,
			query: url.Values{
				"types":      {"commit,comment"},
				"item_types": {"pr,issue"},
			},
			request: func(ctx context.Context, client *generated.Client) error {
				_, err := client.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{
					Types: []string{"commit", "comment"}, ItemTypes: []string{"pr", "issue"},
				}})
				return err
			},
		},
		{
			name:  "explode true repeats archive repositories",
			body:  `[]`,
			query: url.Values{"repo": {"github|github.com/team/repo-a", "gitlab|gitlab.com/group/repo-b"}},
			request: func(ctx context.Context, client *generated.Client) error {
				_, err := client.ListArchiveStatusWithResponse(ctx, &generated.ListArchiveStatusRequestOptions{Query: &generated.ListArchiveStatusQuery{
					Repo: []string{"github|github.com/team/repo-a", "gitlab|gitlab.com/group/repo-b"},
				}})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, tc.query, r.URL.Query())
				w.Header().Set("Content-Type", "application/json")
				_, err := io.WriteString(w, tc.body)
				assert.NoError(t, err)
			}))
			defer server.Close()
			client, err := apiclient.NewWithHTTPClient(server.URL, server.Client())
			require.NoError(err)
			require.NoError(tc.request(t.Context(), client.HTTP))
		})
	}
}

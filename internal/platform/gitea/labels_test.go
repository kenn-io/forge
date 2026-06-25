package gitea

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	Require "github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/platform/gitealike"
)

var (
	_ gitealike.LabelTransport = (*transport)(nil)
	_ platform.LabelReader     = (*Client)(nil)
	_ platform.LabelMutator    = (*Client)(nil)
)

func giteaLabelTestRef() platform.RepoRef {
	return platform.RepoRef{
		Platform: platform.KindGitea,
		Host:     "gitea.test",
		Owner:    "acme",
		Name:     "widget",
		RepoPath: "acme/widget",
	}
}

func TestClientListLabelsFetchesRepoLabelCatalog(t *testing.T) {
	require := Require.New(t)
	assert := assert.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(http.MethodGet, r.Method)
		assert.Equal("/api/v1/repos/acme/widget/labels", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(json.NewEncoder(w).Encode([]map[string]any{
			{"id": 11, "name": "bug", "color": "d73a4a", "description": "Something is broken"},
			{"id": 12, "name": "triage", "color": "fbca04", "description": ""},
		}))
	}))
	defer server.Close()

	client, err := NewClient("gitea.test", testTokenSource("gitea-token"), WithBaseURLForTesting(server.URL))
	require.NoError(err)

	catalog, err := client.ListLabels(context.Background(), giteaLabelTestRef())
	require.NoError(err)
	require.Len(catalog.Labels, 2)
	assert.Equal("bug", catalog.Labels[0].Name)
	assert.Equal("d73a4a", catalog.Labels[0].Color)
	assert.Equal("Something is broken", catalog.Labels[0].Description)
	assert.Equal(int64(11), catalog.Labels[0].PlatformID)
	assert.Equal("triage", catalog.Labels[1].Name)
}

func TestClientSetLabelsReplacesByResolvedIDs(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Client, context.Context) ([]platform.Label, error)
	}{
		{
			name: "merge request",
			set: func(client *Client, ctx context.Context) ([]platform.Label, error) {
				return client.SetMergeRequestLabels(ctx, giteaLabelTestRef(), 7, []string{"triage"})
			},
		},
		{
			name: "issue",
			set: func(client *Client, ctx context.Context) ([]platform.Label, error) {
				return client.SetIssueLabels(ctx, giteaLabelTestRef(), 7, []string{"triage"})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := Require.New(t)
			assert := assert.New(t)
			var putBody map[string][]int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/acme/widget/labels":
					assert.NoError(json.NewEncoder(w).Encode([]map[string]any{
						{"id": 11, "name": "bug", "color": "d73a4a"},
						{"id": 12, "name": "triage", "color": "fbca04"},
					}))
				case r.Method == http.MethodPut && r.URL.Path == "/api/v1/repos/acme/widget/issues/7/labels":
					assert.NoError(json.NewDecoder(r.Body).Decode(&putBody))
					assert.NoError(json.NewEncoder(w).Encode([]map[string]any{
						{"id": 12, "name": "triage", "color": "fbca04"},
					}))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			client, err := NewClient("gitea.test", testTokenSource("gitea-token"), WithBaseURLForTesting(server.URL))
			require.NoError(err)

			labels, err := tt.set(client, context.Background())
			require.NoError(err)
			assert.Equal([]int64{12}, putBody["labels"])
			require.Len(labels, 1)
			assert.Equal("triage", labels[0].Name)
			assert.Equal("fbca04", labels[0].Color)
		})
	}
}

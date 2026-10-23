package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

func TestSyncPreservesThreadResolutionWrittenDuringFetch(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"detail", "bulk"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			assert := assert.New(t)
			ctx := t.Context()
			d := openTestDB(t)
			repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
			repoID, err := reposeed.Seed(ctx, d, verifiedGitHubRepoIdentity("github.com", "owner", "repo"))
			require.NoError(err)
			now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
			parent, err := NormalizePR(repoID, buildOpenPR(1, now))
			require.NoError(err)
			parent.DetailFetchedAt = &now
			mrID, err := d.UpsertMergeRequest(ctx, parent)
			require.NoError(err)
			require.NoError(d.UpsertMRReviewThreads(ctx, mrID, []db.MRReviewThread{{
				ProviderThreadID: "PRRT_1", Body: "inline note", CreatedAt: now, UpdatedAt: now,
			}}))
			threads, err := d.ListMRReviewThreads(ctx, mrID)
			require.NoError(err)
			require.Len(threads, 1)
			threadID := threads[0].ID

			if mode == "detail" {
				provider := &syncTestReadProvider{
					kind: platform.KindGitHub, host: "github.com", readReviewThreads: true,
					mergeRequests: []platform.MergeRequest{{
						Repo: platformRepoRef(repo), PlatformID: parent.PlatformID, Number: 1,
						URL: parent.URL, Title: parent.Title, Author: parent.Author, State: "open",
						CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
					}},
					listReviewThreadsFn: func(ctx context.Context, _ platform.RepoRef, _ int) ([]platform.MergeRequestReviewThread, error) {
						// The provider read predates the local resolution.
						err := d.SetMRReviewThreadResolved(ctx, mrID, threadID, true, &now)
						return []platform.MergeRequestReviewThread{{
							ProviderThreadID: "PRRT_1", Body: "inline note", Resolved: false,
							CreatedAt: now, UpdatedAt: now,
						}}, err
					},
				}
				registry, err := platform.NewRegistry(provider)
				require.NoError(err)
				syncer := NewSyncerWithRegistry(registry, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
				_, err = syncer.fetchProviderMRDetail(ctx, provider, repo, repoID, 1)
				require.NoError(err)
				assert.Equal(int32(1), provider.listReviewThreads.Load())
			} else {
				nodes := testGQLPRNodes(1)
				nodes[0]["reviewThreads"] = map[string]any{
					"nodes": []map[string]any{{
						"id": "PRRT_1", "isResolved": false, "path": "main.go", "line": 1, "diffSide": "RIGHT",
						"comments": map[string]any{
							"nodes": []map[string]any{{
								"id": "PRRC_1", "databaseId": 101, "body": "inline note",
								"createdAt": now.Format(time.RFC3339), "updatedAt": now.Format(time.RFC3339),
							}},
						},
					}},
				}
				var fetches int
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request struct{ Query string }
					assert.NoError(json.NewDecoder(r.Body).Decode(&request))
					if !strings.Contains(request.Query, "pullRequests(") {
						_, err := w.Write([]byte(`{"data":{"repository":{"pullRequest":null}}}`))
						assert.NoError(err)
						return
					}
					fetches++
					assert.NoError(d.SetMRReviewThreadResolved(ctx, mrID, threadID, true, &now))
					assert.NoError(json.NewEncoder(w).Encode(map[string]any{
						"data": map[string]any{"repository": map[string]any{"pullRequests": map[string]any{
							"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false},
						}}},
					}))
				}))
				t.Cleanup(srv.Close)
				mock := &mockClient{listOpenIssuesErr: notModifiedErr()}
				mock.openPRs = append(mock.openPRs, buildOpenPR(1, now))
				syncer := NewSyncer(map[string]Client{"github.com": mock}, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
				syncer.SetFetchers(map[string]*GraphQLFetcher{
					"github.com": NewGraphQLFetcherWithClient(githubv4.NewEnterpriseClient(srv.URL, srv.Client()), nil),
				})
				require.NoError(syncer.indexSyncRepo(ctx, repo, repoID, false))
				require.Equal(1, fetches)
			}

			threads, err = d.ListMRReviewThreads(ctx, mrID)
			require.NoError(err)
			require.Len(threads, 1)
			assert.True(threads[0].Resolved)
			assert.Equal(&now, threads[0].ResolvedAt)
		})
	}
}

package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

func TestBulkSyncPreservesCommentsWrittenDuringFetch(t *testing.T) {
	t.Parallel()
	for _, issue := range []bool{false, true} {
		t.Run(fmt.Sprintf("issue=%t", issue), func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			assert := assert.New(t)
			ctx := t.Context()
			d := openTestDB(t)
			repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
			repoID, err := reposeed.Seed(ctx, d, verifiedGitHubRepoIdentity("github.com", "owner", "repo"))
			require.NoError(err)
			now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
			var parentID int64
			if issue {
				parent, err := NormalizeIssue(repoID, buildOpenIssue(1, now))
				require.NoError(err)
				parent.DetailFetchedAt = &now
				parentID, err = d.UpsertIssue(ctx, parent)
				require.NoError(err)
			} else {
				parent, err := NormalizePR(repoID, buildOpenPR(1, now))
				require.NoError(err)
				parent.DetailFetchedAt = &now
				parentID, err = d.UpsertMergeRequest(ctx, parent)
				require.NoError(err)
			}

			// The provider's page predates both a new comment and an edit.
			nodes := testGQLPRNodes(1)
			connection := "pullRequests"
			if issue {
				nodes = testGQLIssueNodes(1)
				connection = "issues"
			}
			comments := map[string]any{
				"totalCount": 1,
				"nodes": []map[string]any{{
					"databaseId": 11, "body": "old body", "author": map[string]any{"login": "alice"},
					"createdAt": now.Format(time.RFC3339), "updatedAt": now.Format(time.RFC3339),
				}},
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
			}
			if !issue {
				delete(comments, "totalCount")
			}
			nodes[0]["comments"] = comments
			var fetches int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				assert.NoError(err)
				if !bytes.Contains(body, []byte(connection+"(")) {
					_, err := w.Write([]byte(`{"data":{"repository":{"pullRequest":null}}}`))
					assert.NoError(err)
					return
				}
				fetches++
				if issue {
					err = d.UpsertIssueEvents(ctx, []db.IssueEvent{
						{IssueID: parentID, PlatformID: new(int64(11)), EventType: "issue_comment", Body: "edited body", CreatedAt: now, DedupeKey: "issue-comment-11"},
						{IssueID: parentID, PlatformID: new(int64(12)), EventType: "issue_comment", Body: "posted comment", CreatedAt: now, DedupeKey: "issue-comment-12"},
					})
				} else {
					err = d.UpsertMREvents(ctx, []db.MREvent{
						{MergeRequestID: parentID, PlatformID: new(int64(11)), EventType: "issue_comment", Body: "edited body", CreatedAt: now, DedupeKey: "comment-11"},
						{MergeRequestID: parentID, PlatformID: new(int64(12)), EventType: "issue_comment", Body: "posted comment", CreatedAt: now, DedupeKey: "comment-12"},
						{MergeRequestID: parentID, PlatformID: new(int64(13)), EventType: "review_comment", Body: "posted review reply", CreatedAt: now, DedupeKey: "review-comment-13"},
					})
				}
				assert.NoError(err)
				w.Header().Set("Content-Type", "application/json")
				assert.NoError(json.NewEncoder(w).Encode(map[string]any{
					"data": map[string]any{"repository": map[string]any{connection: map[string]any{
						"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false},
					}}},
				}))
			}))
			t.Cleanup(srv.Close)
			mock := &mockClient{listOpenPRsErr: notModifiedErr(), listOpenIssuesErr: notModifiedErr()}
			if issue {
				mock.listOpenIssuesErr = nil
				mock.openIssues = append(mock.openIssues, buildOpenIssue(1, now))
			} else {
				mock.listOpenPRsErr = nil
				mock.openPRs = append(mock.openPRs, buildOpenPR(1, now))
			}
			syncer := NewSyncer(map[string]Client{"github.com": mock}, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
			syncer.SetFetchers(map[string]*GraphQLFetcher{
				"github.com": NewGraphQLFetcherWithClient(githubv4.NewEnterpriseClient(srv.URL, srv.Client()), nil),
			})
			require.NoError(syncer.indexSyncRepo(ctx, repo, repoID, false))
			require.Equal(1, fetches)
			if issue {
				events, err := d.ListIssueEvents(ctx, parentID)
				require.NoError(err)
				require.Len(events, 2)
				assert.ElementsMatch([]string{"edited body", "posted comment"}, []string{events[0].Body, events[1].Body})
				parent, err := d.GetIssueByRepoIDAndNumber(ctx, repoID, 1)
				require.NoError(err)
				assert.Nil(parent.DetailFetchedAt)
			} else {
				events, err := d.ListMREvents(ctx, parentID)
				require.NoError(err)
				require.Len(events, 3)
				assert.ElementsMatch([]string{"edited body", "posted comment", "posted review reply"}, []string{events[0].Body, events[1].Body, events[2].Body})
				parent, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
				require.NoError(err)
				assert.Nil(parent.DetailFetchedAt)
			}
		})
	}
}

func snapshotRevisions(t *testing.T, database *db.DB, repoID int64, itemType db.ArchiveItemType) map[int]int64 {
	t.Helper()
	revisions, err := database.SnapshotRevisions(t.Context(), repoID, itemType)
	require.NoError(t, err)
	return revisions
}

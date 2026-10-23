package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gh "github.com/google/go-github/v92/github"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
)

func TestSnapshotRevisionFailureContinuesRepoSync(t *testing.T) {
	t.Parallel()

	for _, issue := range []bool{false, true} {
		t.Run(map[bool]string{false: "pull request", true: "issue"}[issue], func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			ctx := t.Context()
			d := openTestDB(t)
			repo := RepoRef{Owner: "acme", Name: "widgets", PlatformHost: "github.com"}
			repoID, err := reposeed.Seed(ctx, d, verifiedGitHubRepoIdentity("github.com", repo.Owner, repo.Name))
			require.NoError(err)
			issueListCalled := false
			client := &partialFailureMock{
				listOpenIssuesFn: func(context.Context, string, string) ([]*gh.Issue, error) {
					issueListCalled = true
					if issue {
						// Model an unavailable reader without changing the schema or writer.
						require.NoError(d.ReadDB().Close())
						return []*gh.Issue{buildOpenIssue(1, time.Now().UTC())}, nil
					}
					return nil, notModifiedErr()
				},
			}
			if issue {
				client.listOpenPRsErr = notModifiedErr()
			} else {
				client.listOpenPRsFn = func(context.Context, string, string) ([]*gh.PullRequest, error) {
					require.NoError(d.ReadDB().Close())
					return []*gh.PullRequest{buildOpenPR(1, time.Now().UTC())}, nil
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				assert.Fail("bulk fetch must wait for snapshot revisions")
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, testBudget(1000))
			syncer.SetFetchers(map[string]*GraphQLFetcher{"github.com": {client: githubv4.NewEnterpriseClient(server.URL, server.Client())}})
			err = syncer.indexSyncRepo(ctx, repo, repoID, false)
			var partial *PartialSyncError
			require.ErrorAs(err, &partial)
			assert.Equal(!issue, partial.MergeRequests)
			assert.Equal(issue, partial.Issues)
			assert.True(issueListCalled, "a failed PR bulk step must allow issue sync to continue")
			expectedScope := failMR
			if issue {
				expectedScope = failIssues
			}
			assert.Equal(expectedScope, syncer.consumeRepoFailed(repo))
		})
	}
}

func TestRejectedCommentRefreshInvalidatesETag(t *testing.T) {
	t.Parallel()

	for _, issue := range []bool{false, true} {
		for _, unchanged := range []bool{false, true} {
			name := map[bool]string{false: "pull request", true: "issue"}[issue]
			name += map[bool]string{false: "/comment poll", true: "/unchanged detail"}[unchanged]
			t.Run(name, func(t *testing.T) {
				assert := assert.New(t)
				require := require.New(t)
				ctx := t.Context()
				d := openTestDB(t)
				repo := RepoRef{Owner: "acme", Name: "widgets", PlatformHost: "github.com"}
				repoID, err := reposeed.Seed(ctx, d, verifiedGitHubRepoIdentity("github.com", repo.Owner, repo.Name))
				require.NoError(err)
				now := time.Now().UTC().Truncate(time.Second)
				fetched := now.Add(-25 * time.Hour)
				mr := &db.MergeRequest{RepoID: repoID, PlatformID: 101, Number: 1, State: "open", Title: "PR", CreatedAt: now, UpdatedAt: now, LastActivityAt: now, DetailFetchedAt: &fetched}
				item := &db.Issue{RepoID: repoID, PlatformID: 101, Number: 1, State: "open", Title: "Issue", CreatedAt: now, UpdatedAt: now, LastActivityAt: now, DetailFetchedAt: &fetched}
				if issue {
					_, err = d.UpsertIssue(ctx, item)
					require.NoError(err)
					item, err = d.GetIssueByRepoIDAndNumber(ctx, repoID, 1)
				} else {
					_, err = d.UpsertMergeRequest(ctx, mr)
					require.NoError(err)
					mr, err = d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
				}
				require.NoError(err)

				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					assert.Equal("/api/v3/repos/acme/widgets/issues/1/comments", r.URL.Path)
					if calls == 1 {
						var writeErr error
						if issue {
							writeErr = d.UpsertIssueEvents(ctx, []db.IssueEvent{{IssueID: item.ID, EventType: "issue_comment", Body: "local post", CreatedAt: now, DedupeKey: "issue_comment:1"}})
						} else {
							writeErr = d.UpsertMREvents(ctx, []db.MREvent{{MergeRequestID: mr.ID, EventType: "issue_comment", Body: "local post", CreatedAt: now, DedupeKey: "issue_comment:1"}})
						}
						assert.NoError(writeErr)
					}
					if calls == 2 {
						assert.Empty(r.Header.Get("If-None-Match"), "a rejected save must force the next poll to fetch comments")
					}
					if r.Header.Get("If-None-Match") != "" {
						w.WriteHeader(http.StatusNotModified)
						return
					}
					w.Header().Set("ETag", `"comments-v1"`)
					_, _ = io.WriteString(w, `[{"id":1,"body":"local post"},{"id":2,"body":"provider edit"}]`)
				}))
				defer server.Close()
				client, err := NewClient(testTokenSource("token"), "github.com", nil, nil, WithBaseURLForTesting(server.URL))
				require.NoError(err)
				syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{repo}, time.Minute, nil, testBudget(1000))
				if issue {
					if unchanged {
						_, err = syncer.markUnchangedIssueDetailFetched(ctx, repo, 1, item, 0)
						require.NoError(err)
					} else {
						attempted, _ := syncer.refreshIssueCommentsForItem(ctx, client, repo, item)
						require.True(attempted)
					}
				} else if unchanged {
					_, err = syncer.markUnchangedMRDetailFetched(ctx, repo, repoID, 1, mr, 0)
					require.NoError(err)
				} else {
					attempted, _ := syncer.refreshPRCommentsForItem(ctx, client, repo, mr)
					require.True(attempted)
				}
				_, err = client.ListIssueCommentsIfChanged(ctx, repo.Owner, repo.Name, 1)
				require.NoError(err)
			})
		}
	}
}

package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

func bulkRefreshTestNode(number int, at time.Time, issue bool) map[string]any {
	node := testGQLIssueNodes(number)[0]
	if !issue {
		node = testGQLPRNodes(number)[0]
		node["headRefOid"], node["baseRefOid"] = "abc123def456", "base123"
		node["mergeable"] = "CONFLICTING"
	}
	node["title"], node["createdAt"], node["updatedAt"] = "Updated item", at, at
	return node
}

func TestRunOnceBulkRefreshTargets(t *testing.T) {
	for _, issue := range []bool{false, true} {
		for _, mode := range []string{"changed", "unchanged", "stale", "cold", "recovery", "locked", "closed", "empty"} {
			t.Run(fmt.Sprintf("issue=%t/%s", issue, mode), func(t *testing.T) {
				t.Parallel()
				assert := assert.New(t)
				require := require.New(t)
				ctx := t.Context()
				d := openTestDB(t)
				repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com", Key: platform.RepositoryIDKey(testRepoID("owner", "repo"))}
				repoID, err := reposeed.Seed(ctx, d, db.RepoIdentity{Platform: "github", PlatformHost: repo.PlatformHost, Key: repo.Key, Owner: repo.Owner, Name: repo.Name})
				require.NoError(err)
				now := time.Now().UTC().Truncate(time.Second)
				old := now.Add(-2 * time.Hour)
				mc := &detailTrackingClient{}
				count := 12
				if mode == "empty" {
					count = 1
				}
				for number := 1; number <= count; number++ {
					updated := old
					if number == 1 && (mode == "changed" || mode == "closed" || mode == "locked") {
						updated = now
					}
					if mode != "empty" {
						if issue {
							listed := buildOpenIssue(number, updated)
							listed.NodeID = new(fmt.Sprintf("NODE_%d", number))
							mc.openIssues = append(mc.openIssues, listed)
						} else {
							listed := buildOpenPR(number, updated)
							listed.NodeID, listed.Base.SHA = new(fmt.Sprintf("NODE_%d", number)), new("base123")
							mc.openPRs = append(mc.openPRs, listed)
						}
					}
					if mode == "cold" {
						continue
					}
					fetched := now
					if mode == "stale" && number == 1 {
						fetched = old
					}
					if issue {
						_, err = d.UpsertIssue(ctx, &db.Issue{RepoID: repoID, Number: number, PlatformID: int64(number * 1000), Title: "Stored issue", State: "open", CreatedAt: old, UpdatedAt: old, LastActivityAt: now, DetailFetchedAt: &fetched})
					} else {
						_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{RepoID: repoID, Number: number, PlatformID: int64(number * 1000), Title: "Stored PR", IsLocked: mode == "locked" && number == 2, State: "open", CreatedAt: old, UpdatedAt: old, LastActivityAt: old, PlatformHeadSHA: "abc123def456", PlatformBaseSHA: "base123", MergeableState: "clean", DetailFetchedAt: &fetched})
					}
					require.NoError(err)
				}
				if mode == "unchanged" && !issue {
					row, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
					require.NoError(err)
					require.NoError(d.UpsertMREvents(ctx, []db.MREvent{
						{MergeRequestID: row.ID, PlatformID: new(int64(1)), EventType: "issue_comment", Body: "Recent comment", CreatedAt: now, DedupeKey: "comment-1"},
					}))
				}
				if mode == "closed" {
					if issue {
						_, err = d.UpsertIssue(ctx, &db.Issue{RepoID: repoID, Number: 13, PlatformID: 13000, Title: "Closed issue", State: "open", CreatedAt: old, UpdatedAt: old, LastActivityAt: old})
					} else {
						_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{RepoID: repoID, Number: 13, PlatformID: 13000, Title: "Closed PR", State: "open", CreatedAt: old, UpdatedAt: old, LastActivityAt: old})
					}
					require.NoError(err)
				}
				closedNumber := 13
				if mode == "empty" {
					closedNumber = 1
				}
				mc.singlePR = buildOpenPR(closedNumber, now)
				mc.singlePR.State = new("closed")
				mc.singlePR.ClosedAt = makeTimestamp(now)
				mc.getIssueFn = func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
					assert.Equal(closedNumber, number)
					closed := buildOpenIssue(number, now)
					closed.State, closed.ClosedAt = new("closed"), makeTimestamp(now)
					return closed, nil
				}
				if issue {
					mc.listOpenPRsErr = notModifiedErr()
				} else {
					mc.listOpenIssuesErr = notModifiedErr()
				}
				var queries []string
				var selected [][]string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request struct {
						Query     string
						Variables struct {
							IDs      []string `json:"ids"`
							PageSize int      `json:"pageSize"`
							Cursor   *string  `json:"cursor"`
						}
					}
					if err := json.NewDecoder(r.Body).Decode(&request); !assert.NoError(err) {
						return
					}
					if !strings.Contains(request.Query, "nodes(ids:") && !strings.Contains(request.Query, "pullRequests(first:") && !strings.Contains(request.Query, "issues(first:") {
						field := "pullRequest"
						if strings.Contains(request.Query, "issue(number:") {
							field = "issue"
						}
						assert.NoError(json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{field: nil}}}))
						return
					}
					queries = append(queries, request.Query)
					ids := request.Variables.IDs
					var nodes []any
					if ids != nil {
						selected = append(selected, ids)
						for _, id := range ids {
							number, err := strconv.Atoi(strings.TrimPrefix(id, "NODE_"))
							assert.NoError(err)
							nodes = append(nodes, bulkRefreshTestNode(number, now, issue))
						}
						assert.NoError(json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"nodes": nodes}}))
						return
					}
					start := 0
					if request.Variables.Cursor != nil {
						start, _ = strconv.Atoi(*request.Variables.Cursor)
					}
					total := count
					if mode == "empty" {
						total = 0
					}
					end := min(start+request.Variables.PageSize, total)
					for number := start + 1; number <= end; number++ {
						nodes = append(nodes, bulkRefreshTestNode(number, now, issue))
					}
					connection := "pullRequests"
					if issue {
						connection = "issues"
					}
					assert.NoError(json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{connection: map[string]any{"nodes": nodes, "totalCount": total, "pageInfo": map[string]any{"hasNextPage": end < total, "endCursor": strconv.Itoa(end)}}}}}))
				}))
				t.Cleanup(server.Close)
				syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{repo}, time.Minute, nil, testBudget(10000))
				syncer.SetFetchers(map[string]*GraphQLFetcher{"github.com": NewGraphQLFetcherWithClient(githubv4.NewEnterpriseClient(server.URL, server.Client()), nil)})
				if mode == "recovery" {
					scope := failMR
					if issue {
						scope = failIssues
					}
					syncer.markRepoFailed(repo, scope)
				}
				syncer.RunOnce(ctx)
				switch {
				case mode == "empty" && !issue:
					require.Len(queries, 1)
					assert.Empty(selected)
					assert.Contains(queries[0], "repository(")
				case mode == "cold" || mode == "recovery" || (mode == "closed" && !issue):
					require.Len(queries, 2)
					assert.Empty(selected)
					assert.Contains(queries[0], "repository(")
				case mode == "unchanged" || mode == "empty":
					assert.Empty(queries)
				default:
					assert.Equal([][]string{{"NODE_1"}}, selected)
					require.Len(queries, 1)
					assert.Contains(queries[0], "nodes(ids: $ids)")
				}
				if issue {
					rows, err := d.ListIssues(ctx, db.ListIssuesOpts{AllowedRepoIDs: []int64{repoID}, State: "open"})
					require.NoError(err)
					if mode == "empty" {
						count = 0
					}
					assert.Len(rows, count)
					if mode == "changed" {
						row, err := d.GetIssueByRepoIDAndNumber(ctx, repoID, 1)
						require.NoError(err)
						assert.Equal("Updated item", row.Title)
					}
				} else {
					rows, err := d.ListMergeRequests(ctx, db.ListMergeRequestsOpts{RepoID: repoID, State: "open", ProviderState: true})
					require.NoError(err)
					if mode == "empty" {
						count = 0
					}
					assert.Len(rows, count)
					if mode == "changed" {
						row, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
						require.NoError(err)
						assert.Equal("Updated item", row.Title)
					}
					if mode == "closed" {
						row, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 2)
						require.NoError(err)
						assert.Equal("dirty", row.MergeableState)
					}
					if mode != "closed" && mode != "empty" {
						assert.Zero(mc.getPRCalls.Load())
					}
				}
			})
		}
	}
}

func TestBulkRefreshTargets(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, mode := range []string{"fresh", "changed", "head", "base", "state", "unknown", "missing", "no-detail", "expired", "force"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			listed := []platform.MergeRequest{{Number: 1, PlatformExternalID: "PR_1", UpdatedAt: now, State: "open", HeadSHA: "head", BaseSHA: "base"}}
			stored := []db.MergeRequest{{Number: 1, LastActivityAt: now, State: "open", PlatformHeadSHA: "head", PlatformBaseSHA: "base", MergeableState: "clean", DetailFetchedAt: &now}}
			switch mode {
			case "changed":
				stored[0].LastActivityAt = now.Add(-time.Minute)
			case "head":
				listed[0].HeadSHA = "new"
			case "base":
				listed[0].BaseSHA = "new"
			case "state":
				stored[0].State = "closed"
			case "unknown":
				stored[0].MergeableState = "unknown"
			case "missing":
				stored = nil
			case "no-detail":
				stored[0].DetailFetchedAt = nil
			case "expired":
				stored[0].DetailFetchedAt = new(now.Add(-30 * time.Minute))
			}
			ids, unread, full := bulkPRRefreshTargets(listed, stored, mode == "force", now)
			if mode == "fresh" {
				assert.Empty(t, ids)
				assert.Equal(t, stored, unread)
				assert.False(t, full)
			} else {
				assert.True(t, full)
				assert.Empty(t, unread)
				if mode != "force" {
					assert.Equal(t, []string{"PR_1"}, ids)
				}
			}
		})
	}
}

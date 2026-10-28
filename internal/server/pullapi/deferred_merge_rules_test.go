package pullapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

// mergeCredentialGraphQL answers merge state reads as one credential sees the
// pull request.
type mergeCredentialGraphQL struct {
	state   string
	head    string
	failing bool
}

func (g mergeCredentialGraphQL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case !bytes.Contains(body, []byte("mergeStateStatus")):
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":null}}}`)
	case g.failing:
		_, _ = io.WriteString(w, `{"errors":[{"message":"Something went wrong while executing your query."}]}`)
	default:
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":{"mergeStateStatus":"`+
			g.state+`","headRefOid":"`+g.head+`"}}}}`)
	}
}

func fakeGraphQLFetcher(srv *httptest.Server) *ghclient.GraphQLFetcher {
	//nolint:forbidigo // Test fixture points one credential's fetcher at a fake GitHub GraphQL server.
	return ghclient.NewGraphQLFetcherWithClient(githubv4.NewEnterpriseClient(srv.URL, srv.Client()), nil)
}

// TestDeferredMergeRulesAskTheMergeCredential pins that a deferred merge
// whose sync App sees the pull request blocked waits only when the separate
// merge credential is blocked too or cannot confirm otherwise for the head it
// would merge.
func TestDeferredMergeRulesAskTheMergeCredential(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		user    mergeCredentialGraphQL
		bypass  bool
		blocked bool
	}{
		{name: "merge credential clean", user: mergeCredentialGraphQL{state: "CLEAN", head: "head-sha"}},
		{name: "merge credential blocked", user: mergeCredentialGraphQL{state: "BLOCKED", head: "head-sha"}, blocked: true},
		{name: "merge credential read failed", user: mergeCredentialGraphQL{failing: true}, blocked: true},
		{name: "merge credential saw another head", user: mergeCredentialGraphQL{state: "CLEAN", head: "newer-sha"}, blocked: true},
		{name: "bypass consent", user: mergeCredentialGraphQL{state: "BLOCKED", head: "head-sha"}, bypass: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			ref := platform.RepoRef{
				Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widgets",
				RepoPath: "acme/widgets", Key: platform.RepositoryIDKey(4242), DefaultBranch: "main",
			}
			database := dbtest.Open(t)
			repoID, err := reposeed.Seed(ctx, database, platformdb.DBRepoIdentity(ref))
			require.NoError(t, err)
			repo, err := database.GetRepoByID(ctx, repoID)
			require.NoError(t, err)
			require.NotNil(t, repo)

			appSrv := httptest.NewServer(mergeCredentialGraphQL{state: "BLOCKED", head: "head-sha"})
			t.Cleanup(appSrv.Close)
			userSrv := httptest.NewServer(tt.user)
			t.Cleanup(userSrv.Close)
			router, err := ghclient.NewHostRouter("github.com", &ghclient.Route{
				Key:           ghclient.RouteKey{Host: "github.com", Owner: "acme"},
				Fetcher:       fakeGraphQLFetcher(appSrv),
				WriteFetcher:  fakeGraphQLFetcher(userSrv),
				ReadIdentity:  ghclient.IdentityKey{Host: "github.com", Principal: "app:1"},
				WriteIdentity: ghclient.IdentityKey{Host: "github.com", Principal: "user:7"},
			})
			require.NoError(t, err)
			syncer := ghclient.NewSyncer(nil, database, nil, []ghclient.RepoRef{{
				Platform: ref.Platform, PlatformHost: ref.Host, Owner: ref.Owner, Name: ref.Name,
				RepoPath: ref.RepoPath, Key: ref.Key,
			}}, time.Minute, nil, nil)
			t.Cleanup(syncer.Stop)
			syncer.SetGitHubRouters(map[string]*ghclient.HostRouter{ref.Host: router})
			h := &Handler{db: database, syncer: syncer}

			blocked := h.deferredMergeBlockedByRules(ctx, *repo, platform.MergeRequest{
				Repo: ref, Number: 7, State: "open", HeadSHA: "head-sha",
				BaseBranch: "main", MergeableState: "blocked",
			}, mergePRInputBody{BypassRules: tt.bypass})

			assert.Equal(t, tt.blocked, blocked)
		})
	}
}

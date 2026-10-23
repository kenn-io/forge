package issuetest

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/forge/platform"
	platformgitlab "go.kenn.io/forge/platform/gitlab"
)

func TestAPISyncIssueIfChanged(t *testing.T) {
	for _, tc := range []struct {
		name         string
		onHost       bool
		provider     string
		host         string
		skipProvider bool
	}{
		{name: "plain", provider: "gh", host: "github.com"},
		{name: "host", onHost: true, provider: "gh", host: "github.com"},
		{name: "gitlab", onHost: true, provider: "gitlab", host: "gitlab.test", skipProvider: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverfake.RunParallelServerTest(t)
			require := require.New(t)
			assert := assert.New(t)
			database := dbtest.Open(t)
			var issueID int64
			var syncer *ghclient.Syncer
			budget := ghclient.NewSyncBudget(100)
			var providerCalls int
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				providerCalls++
				assert.Equal(`"cached"`, r.Header.Get("If-None-Match"))
				assert.Equal(1, budget.Spent(), "the handler must budget the poll before wire I/O")
				w.WriteHeader(http.StatusNotModified)
			}))
			defer provider.Close()
			if tc.skipProvider {
				gitlab, err := platformgitlab.NewClient("gitlab.test", serverfake.TestTokenSource("token"), platformgitlab.WithBaseURLForTesting(provider.URL+"/api/v4"), platformgitlab.WithoutRetriesForTesting(), platformgitlab.WithTransport(http.DefaultTransport))
				require.NoError(err)
				registry, err := platform.NewRegistry(gitlab)
				require.NoError(err)
				repoID, err := reposeed.Seed(t.Context(), database, db.RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.test", Key: platform.RepositoryIDKey(42), Owner: "acme", Name: "widget"})
				require.NoError(err)
				issueID = serverfake.SeedIssueForRepo(t, database, repoID, "gitlab.test", "acme", "widget", 7, "open", "cached issue")
				syncer = ghclient.NewSyncerWithRegistry(registry, database, nil, []ghclient.RepoRef{{Platform: platform.KindGitLab, PlatformHost: "gitlab.test", Owner: "acme", Name: "widget"}}, time.Minute, nil, nil)
			} else {
				issueID = serverfake.SeedIssue(t, database, "acme", "widget", 7, "open")
				require.NoError(database.UpsertHTTPEtag(t.Context(), "github", "github.com", "acme", "widget", "issue", 7, `"cached"`))
				gh, err := ghclient.NewClient(serverfake.TestTokenSource("token"), "github.com", nil, budget, ghclient.WithBaseURLForTesting(provider.URL))
				require.NoError(err)
				syncer = ghclient.NewSyncer(map[string]ghclient.Client{"github.com": gh}, database, nil, []ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}}, time.Minute, nil, nil)
			}
			t.Cleanup(syncer.Stop)
			srv := servertest.New(t, database, syncer, nil, "/", nil, server.ServerOptions{})
			client := servertest.SetupTestClient(t, srv)
			if tc.onHost {
				resp, err := client.HTTP.SyncIssueOnHostWithResponse(t.Context(), &generated.SyncIssueOnHostRequestOptions{
					PathParams: &generated.SyncIssueOnHostPath{PlatformHost: tc.host, Provider: tc.provider, Owner: "acme", Name: "widget", Number: 7},
					Query:      &generated.SyncIssueOnHostQuery{IfChanged: new(true)},
				})
				require.NoError(err)
				require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
				require.NotNil(resp.JSON200)
				assert.Equal(issueID, resp.JSON200.Issue.ID)
			} else {
				resp, err := client.HTTP.SyncIssueWithResponse(t.Context(), &generated.SyncIssueRequestOptions{
					PathParams: &generated.SyncIssuePath{Provider: tc.provider, Owner: "acme", Name: "widget", Number: 7},
					Query:      &generated.SyncIssueQuery{IfChanged: new(true)},
				})
				require.NoError(err)
				require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
				require.NotNil(resp.JSON200)
				assert.Equal(issueID, resp.JSON200.Issue.ID)
			}
			if tc.skipProvider {
				assert.Zero(providerCalls)
			} else {
				assert.Equal(1, providerCalls)
			}
			assert.Zero(budget.Spent(), "304 refunds the minute poll")
		})
	}
}

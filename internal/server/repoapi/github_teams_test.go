package repoapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

type teamFixture struct {
	ghclient.Client
	calls int
}

func (f *teamFixture) ListOrgTeams(context.Context, string) ([]*gh.Team, error) {
	f.calls++
	return []*gh.Team{{Slug: new("payments"), Name: new("Payments")}}, nil
}

func (f *teamFixture) ListTeamRepositories(context.Context, string, string) ([]*gh.Repository, error) {
	f.calls++
	return []*gh.Repository{
		{ID: new(int64(1)), FullName: new("acme/new")}, {ID: new(int64(2)), FullName: new("acme/other")}, {ID: new(int64(3)), FullName: new("acme/hidden")}, {ID: new(int64(4)), FullName: new("acme/team-api")}, {ID: new(int64(5)), FullName: new("acme/peer")},
	}, nil
}

func TestGitHubTeamRepositoriesConfiguredVisibleIdentity(t *testing.T) {
	database := dbtest.Open(t)
	refs := []ghclient.RepoRef{}
	for id, name := range []string{"new", "other", "hidden", "team-api", "peer"} {
		key := platform.RepositoryIDKey(int64(id + 1))
		rowID, err := reposeed.Seed(t.Context(), database, db.RepoIdentity{Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: name, Key: key})
		require.NoError(t, err)
		if name == "hidden" {
			require.NoError(t, database.SetRepoHiddenFromUI(t.Context(), rowID, true))
		}
		ref := ghclient.RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "acme", Name: name, Key: key}
		if name == "new" {
			ref.ConfiguredRepoPath = "acme/old"
		}
		refs = append(refs, ref)
	}
	fake := &teamFixture{}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": fake}, database, nil, refs, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	cfg := &config.Config{Repos: []config.Repo{{Owner: "acme", Name: "old"}, {Owner: "acme", Name: "hidden"}, {Owner: "acme", Name: "team-*"}}}
	mu := &sync.Mutex{}
	h := &Handlers{Cfg: &cfg, CfgMu: mu, Db: database, Syncer: &syncer}
	out, err := h.ListGitHubTeamRepositories(t.Context(), &githubTeamReposInput{PlatformHost: "github.com", Org: "acme", Slug: "payments"})
	require.NoError(t, err)
	require.Equal(t, []githubTeamRepo{{PlatformRepoID: 1, RepoPath: "acme/new"}, {PlatformRepoID: 4, RepoPath: "acme/team-api"}}, out.Body)
	_, err = h.ListGitHubTeams(t.Context(), &githubTeamInput{PlatformHost: "github.com", Org: "other"})
	require.Error(t, err)
	require.Equal(t, 1, fake.calls)
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "1"))
	huma.Get(api, "/repos/github-teams/{slug}/repositories", h.ListGitHubTeamRepositories)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/repos/github-teams/payments/repositories?platform_host=github.com&org=acme", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `[{"platform_repo_id":1,"repo_path":"acme/new"},{"platform_repo_id":4,"repo_path":"acme/team-api"}]`, response.Body.String())
}

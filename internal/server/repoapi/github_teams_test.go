package repoapi

import (
	"context"
	"errors"
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
	err   error
}

func (f *teamFixture) ListOrgTeams(context.Context, string) ([]*gh.Team, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return []*gh.Team{{Slug: new("payments"), Name: new("Payments")}}, nil
}

func (f *teamFixture) ListTeamRepositories(context.Context, string, string) ([]*gh.Repository, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return []*gh.Repository{
		{ID: new(int64(1)), FullName: new("acme/new")}, {ID: new(int64(2)), FullName: new("acme/other")}, {ID: new(int64(3)), FullName: new("acme/hidden")}, {ID: new(int64(4)), FullName: new("acme/team-api")}, {ID: new(int64(5)), FullName: new("acme/peer")}, {ID: new(int64(6)), FullName: new("forks/project")}, {ID: new(int64(7)), FullName: new("acme/pinned")},
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
	key := platform.RepositoryIDKey(6)
	_, err := reposeed.Seed(t.Context(), database, db.RepoIdentity{Platform: "github", PlatformHost: "github.com", Owner: "forks", Name: "project", Key: key})
	require.NoError(t, err)
	refs = append(refs, ghclient.RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "forks", Name: "project", Key: key, ConfiguredRepoPath: "other/old"})
	key = platform.RepositoryIDKey(7)
	_, err = reposeed.Seed(t.Context(), database, db.RepoIdentity{Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: "pinned", Key: key})
	require.NoError(t, err)
	refs = append(refs, ghclient.RepoRef{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "acme", Name: "pinned", Key: key})
	fake := &teamFixture{}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": fake}, database, nil, refs, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	pinned := config.Repo{Owner: "acme", Name: "pinned"}
	pinned.SetRepositoryKey(platform.RepositoryIDKey(8))
	cfg := &config.Config{Repos: []config.Repo{{Owner: "acme", Name: "old"}, {Owner: "acme", Name: "hidden"}, {Owner: "acme", Name: "team-*"}, {Owner: "other", Name: "alias", RepoPath: "other/old"}, pinned}}
	mu := &sync.Mutex{}
	h := &Handlers{Cfg: &cfg, CfgMu: mu, Db: database, Syncer: &syncer}
	for _, slug := range []string{"", ".", "..", "team/other", "team?other", "team%2Fother"} {
		_, err := h.ListGitHubTeamRepositories(t.Context(), &githubTeamReposInput{PlatformHost: "github.com", Org: "acme", Slug: slug})
		require.Error(t, err, "slug %q", slug)
		require.Zero(t, fake.calls, "slug %q must not reach the provider", slug)
	}
	out, err := h.ListGitHubTeamRepositories(t.Context(), &githubTeamReposInput{PlatformHost: "github.com", Org: "acme", Slug: "payments"})
	require.NoError(t, err)
	require.Equal(t, []githubTeamRepo{{PlatformRepoID: 1, RepoPath: "acme/new"}, {PlatformRepoID: 4, RepoPath: "acme/team-api"}, {PlatformRepoID: 6, RepoPath: "forks/project"}}, out.Body)
	_, err = h.ListGitHubTeams(t.Context(), &githubTeamInput{PlatformHost: "github.com", Org: "unconfigured"})
	require.Error(t, err)
	_, err = h.ListGitHubTeams(t.Context(), &githubTeamInput{PlatformHost: "other.example", Org: "acme"})
	require.Error(t, err)
	require.Equal(t, 1, fake.calls)
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "1"))
	huma.Get(api, "/repos/github-teams/{slug}/repositories", h.ListGitHubTeamRepositories)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/repos/github-teams/payments/repositories?platform_host=github.com&org=acme", nil))
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `[{"platform_repo_id":1,"repo_path":"acme/new"},{"platform_repo_id":4,"repo_path":"acme/team-api"},{"platform_repo_id":6,"repo_path":"forks/project"}]`, response.Body.String())
}

func TestGitHubTeamHandlerErrors(t *testing.T) {
	for _, tc := range []struct {
		name         string
		err          error
		status       int
		code, detail string
	}{
		{"missing user", ghclient.ErrMissingWriteIdentity, 400, "badRequest", "run gh auth login"},
		{"missing route", &ghclient.MissingRouteError{Host: "github.com", Owner: "acme"}, 400, "badRequest", "a token set for a single repository is not used"},
		{"denied", &platform.Error{Code: platform.ErrCodePermissionDenied, Provider: platform.KindGitHub}, 403, "forbidden", "read:org"},
		{"not found", &gh.ErrorResponse{Response: &http.Response{StatusCode: 404}, Message: "not found"}, 404, "notFound", "personal account"},
		{"rate limited", &platform.Error{Code: platform.ErrCodeRateLimited, Provider: platform.KindGitHub}, 429, "rateLimited", ""},
		{"other", errors.New("broken upstream"), 502, "upstreamError", "broken upstream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &teamFixture{err: tc.err}
			syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": fake}, nil, nil, nil, time.Minute, nil, nil)
			t.Cleanup(syncer.Stop)
			cfg := &config.Config{Repos: []config.Repo{{Owner: "acme", Name: "repo"}}}
			h := &Handlers{Cfg: &cfg, CfgMu: &sync.Mutex{}, Syncer: &syncer}
			mux := http.NewServeMux()
			api := humago.New(mux, huma.DefaultConfig("test", "1"))
			huma.Get(api, "/repos/github-teams", h.ListGitHubTeams)
			huma.Get(api, "/repos/github-teams/{slug}/repositories", h.ListGitHubTeamRepositories)
			for _, route := range []string{"/repos/github-teams", "/repos/github-teams/team/repositories"} {
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, route+"?platform_host=github.com&org=acme", nil))
				require.Equal(t, tc.status, response.Code, response.Body.String())
				require.Contains(t, response.Body.String(), tc.code)
				if tc.detail != "" {
					require.Contains(t, response.Body.String(), tc.detail)
				}
			}
		})
	}
}

package repoapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"go.kenn.io/forge/internal/config"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/settingsapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

type githubTeamInput struct {
	PlatformHost string `query:"platform_host" required:"true"`
	Org          string `query:"org" required:"true"`
}
type githubTeamReposInput struct {
	PlatformHost string `query:"platform_host" required:"true"`
	Org          string `query:"org" required:"true"`
	Slug         string `path:"slug"`
}
type githubTeam struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}
type githubTeamRepo struct {
	PlatformRepoID int64  `json:"platform_repo_id"`
	RepoPath       string `json:"repo_path"`
}
type (
	githubTeamsOutput     struct{ Body []githubTeam }
	githubTeamReposOutput struct{ Body []githubTeamRepo }
)

func (s *Handlers) teamReader(input githubTeamInput) (ghclient.TeamReader, []config.Repo, error) {
	host := strings.ToLower(strings.TrimSpace(input.PlatformHost))
	org := strings.TrimSpace(input.Org)
	s.CfgMu.Lock()
	var repos []config.Repo
	if *s.Cfg != nil {
		repos = append(repos, (*s.Cfg).Repos...)
	}
	s.CfgMu.Unlock()
	found := false
	for _, r := range repos {
		if strings.EqualFold(r.PlatformOrDefault(), "github") && strings.EqualFold(r.PlatformHostOrDefault(), host) && strings.EqualFold(r.Owner, org) {
			found = true
			break
		}
	}
	if !found {
		return nil, nil, httpapi.Validation("query.org", "organization and host must match a configured GitHub repository")
	}
	client, err := (*s.Syncer).DirectClientForHost(host)
	if err != nil {
		return nil, nil, httpapi.ProviderCallProblem(err, "github", host)
	}
	reader, ok := client.(ghclient.TeamReader)
	if !ok {
		return nil, nil, httpapi.NewProblem(http.StatusConflict, httpapi.CodeUnsupportedCapability, "Unsupported provider capability", map[string]any{"capability": "github-teams", "provider": "github", "platformHost": host})
	}
	return reader, repos, nil
}

func teamProblem(err error, host string) error {
	if errors.Is(err, ghclient.ErrMissingWriteIdentity) {
		return httpapi.BadRequest(httpapi.CodeBadRequest, "Forge team lookup uses your GitHub user credential. Configure a personal token or gh login; Forge does not use its App credential for this lookup.", nil)
	}
	if errors.Is(err, platform.ErrPermissionDenied) || errors.Is(err, platform.ErrNotFound) || platformgithub.StatusCode(err) == http.StatusNotFound {
		const hint = "GitHub could not access the organization or team. Check that it exists and is available to your credential, and that the credential has classic read:org scope or fine-grained organization Members read permission"
		if pe, ok := errors.AsType[*platform.Error](err); ok {
			mapped := *pe
			mapped.Err = fmt.Errorf("%s: %w", hint, err)
			return httpapi.ProviderCallProblem(&mapped, "github", host)
		}
		return httpapi.ProviderCallProblem(&platform.Error{Code: platform.ErrCodeNotFound, Provider: platform.KindGitHub, PlatformHost: host, Err: fmt.Errorf("%s: %w", hint, err)}, "github", host)
	}
	return httpapi.ProviderCallProblem(err, "github", host)
}

func (s *Handlers) ListGitHubTeams(ctx context.Context, input *githubTeamInput) (*githubTeamsOutput, error) {
	reader, _, err := s.teamReader(*input)
	if err != nil {
		return nil, err
	}
	teams, err := reader.ListOrgTeams(ctx, input.Org)
	if err != nil {
		return nil, teamProblem(err, input.PlatformHost)
	}
	out := &githubTeamsOutput{Body: make([]githubTeam, 0, len(teams))}
	for _, team := range teams {
		if team != nil {
			out.Body = append(out.Body, githubTeam{Slug: team.GetSlug(), Name: team.GetName()})
		}
	}
	return out, nil
}

func (s *Handlers) ListGitHubTeamRepositories(ctx context.Context, input *githubTeamReposInput) (*githubTeamReposOutput, error) {
	reader, configured, err := s.teamReader(githubTeamInput{PlatformHost: input.PlatformHost, Org: input.Org})
	if err != nil {
		return nil, err
	}
	teams, err := reader.ListTeamRepositories(ctx, input.Org, input.Slug)
	if err != nil {
		return nil, teamProblem(err, input.PlatformHost)
	}
	repos, err := s.Db.ListRepos(ctx)
	if err != nil {
		return nil, httpapi.Internal("list repos failed")
	}
	repos = s.FilterConfiguredRepos(repos)
	repos, err = s.FilterHiddenRepos(ctx, repos)
	if err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	configuredKeys := make(map[platform.RepositoryKey]struct{})
	for _, ref := range (*s.Syncer).TrackedRepos() {
		if ref.Key.IsZero() || !strings.EqualFold(spokeapi.RepoProvider(ref), "github") || !spokeapi.SamePlatformHost(ref.PlatformHost, input.PlatformHost) {
			continue
		}
		for _, cfg := range configured {
			if pinned := cfg.RepositoryKey(); !pinned.IsZero() {
				if pinned == ref.Key && strings.EqualFold(cfg.PlatformOrDefault(), spokeapi.RepoProvider(ref)) && spokeapi.SamePlatformHost(cfg.PlatformHostOrDefault(), ref.PlatformHost) {
					configuredKeys[ref.Key] = struct{}{}
					break
				}
			} else if settingsapi.RepoMatchesConfig(ref, cfg) {
				configuredKeys[ref.Key] = struct{}{}
				break
			}
		}
	}
	allowed := make(map[int64]struct{})
	for _, repo := range repos {
		if httpapi.ProviderKind(repo) != platform.KindGitHub || !strings.EqualFold(httpapi.ProviderHost(repo), input.PlatformHost) {
			continue
		}
		if _, ok := configuredKeys[repo.Key]; ok {
			if id, ok := repo.Key.ID(); ok {
				allowed[id] = struct{}{}
			}
		}
	}
	out := &githubTeamReposOutput{Body: make([]githubTeamRepo, 0)}
	for _, repo := range teams {
		if repo != nil {
			if _, ok := allowed[repo.GetID()]; ok {
				out.Body = append(out.Body, githubTeamRepo{PlatformRepoID: repo.GetID(), RepoPath: repo.GetFullName()})
			}
		}
	}
	return out, nil
}

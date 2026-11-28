package repoapi

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"

	"go.kenn.io/forge/internal/config"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/platform"
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
		return httpapi.BadRequest(httpapi.CodeBadRequest, "Team lookup needs your own GitHub credential (a personal token or gh login). The Forge App alone can't read teams.", nil)
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
	tracked := (*s.Syncer).TrackedRepos()
	allowed := make(map[int64]struct{})
	for _, repo := range repos {
		if httpapi.ProviderKind(repo) != platform.KindGitHub || !strings.EqualFold(httpapi.ProviderHost(repo), input.PlatformHost) {
			continue
		}
		// Tracked repositories can include peer workspace entries; require a matching
		// configured exact route or pattern as well as the catalog visibility filters.
		for _, cfg := range configured {
			if !strings.EqualFold(cfg.PlatformOrDefault(), "github") || !strings.EqualFold(cfg.PlatformHostOrDefault(), input.PlatformHost) || !strings.EqualFold(cfg.Owner, input.Org) {
				continue
			}
			matched, _ := path.Match(strings.ToLower(cfg.Name), strings.ToLower(repo.Name))
			matched = matched && strings.EqualFold(repo.Owner, cfg.Owner)
			pinned := cfg.RepositoryKey()
			provenance := false
			for _, ref := range tracked {
				if !repo.Key.IsZero() && (ref.Platform == "" || ref.Platform == platform.KindGitHub) && ref.Key == repo.Key && strings.EqualFold(ref.PlatformHost, input.PlatformHost) && strings.EqualFold(ref.ConfiguredRepoPath, cfg.Owner+"/"+cfg.Name) && ref.ConfiguredRepoPath != "" {
					provenance = true
					break
				}
			}
			if (!pinned.IsZero() && pinned == repo.Key) || (pinned.IsZero() && (matched || provenance)) {
				if id, ok := repo.Key.ID(); ok {
					allowed[id] = struct{}{}
				}
				break
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

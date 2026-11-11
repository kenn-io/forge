package providerapi

import (
	"context"
	"strings"

	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/platform"
)

type repositoryWebURLInput struct {
	Provider                string `path:"provider"`
	Owner                   string `path:"owner"`
	Name                    string `path:"name"`
	PlatformRepoID          int64  `query:"platform_repo_id"`
	BitbucketRepositoryUUID string `query:"bitbucket_repository_uuid"`
}

type repositoryWebURLHostInput struct {
	Provider                string `path:"provider"`
	PlatformHost            string `path:"platform_host"`
	Owner                   string `path:"owner"`
	Name                    string `path:"name"`
	PlatformRepoID          int64  `query:"platform_repo_id"`
	BitbucketRepositoryUUID string `query:"bitbucket_repository_uuid"`
}

type repositoryWebURLResponse struct {
	WebURL string `json:"web_url"`
}
type repositoryWebURLOutput = httpapi.BodyOutput[repositoryWebURLResponse]

func (s *Handlers) repositoryWebURL(ctx context.Context, input *repositoryWebURLInput) (*repositoryWebURLOutput, error) {
	return s.readRepositoryWebURL(ctx, input.Provider, "", input.Owner, input.Name, input.PlatformRepoID, input.BitbucketRepositoryUUID)
}

func (s *Handlers) repositoryWebURLOnHost(ctx context.Context, input *repositoryWebURLHostInput) (*repositoryWebURLOutput, error) {
	return s.readRepositoryWebURL(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.PlatformRepoID, input.BitbucketRepositoryUUID)
}

func (s *Handlers) readRepositoryWebURL(ctx context.Context, provider, host, owner, name string, id int64, rawUUID string) (*repositoryWebURLOutput, error) {
	repositoryUUID, err := platform.ParseRepositoryUUID(rawUUID)
	if err != nil {
		return nil, httpapi.Validation("query.bitbucket_repository_uuid", err.Error())
	}
	key, err := httpapi.RequestRepositoryKey("query.platform_repo_id", id, repositoryUUID)
	if err != nil {
		return nil, err
	}
	if key.IsZero() {
		return nil, httpapi.Validation("query.platform_repo_id", "repository identity is required")
	}
	repo, err := s.RepoResolver.LookupSelection(ctx, provider, host, owner, name, key)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	reader, err := (*s.Syncer).RepositoryReader(httpapi.ProviderKind(repo.Repo), httpapi.ProviderHost(repo.Repo))
	if err != nil {
		return nil, httpapi.ProviderCallProblem(err, provider, host)
	}
	observed, err := reader.GetRepository(ctx, httpapi.PlatformRepoRef(repo.Repo))
	if err != nil {
		return nil, httpapi.ProviderCallProblem(err, provider, host)
	}
	if observed.Ref.Identity() != repo.Identity() || strings.TrimSpace(observed.WebURL) == "" {
		return nil, httpapi.Upstream("repository web address unavailable", provider, host)
	}
	return &repositoryWebURLOutput{Body: repositoryWebURLResponse{WebURL: observed.WebURL}}, nil
}

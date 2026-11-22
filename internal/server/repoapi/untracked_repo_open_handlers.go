package repoapi

import (
	"context"

	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/platform"
)

type UntrackedOpenInput struct {
	Body struct {
		Provider string `json:"provider"`
		Host     string `json:"host"`
		Owner    string `json:"owner"`
		Name     string `json:"name"`
		ItemType string `json:"item_type,omitempty"`
		Number   int    `json:"number,omitempty"`
	}
}

type UntrackedOpensOutput = httpapi.BodyOutput[[]db.UntrackedRepoOpen]

func untrackedRepoKey(repo db.UntrackedRepoOpen) string {
	return spokeapi.TrackedRepoKey(ghclient.RepoRef{Platform: platform.Kind(repo.Provider), PlatformHost: repo.Host, Owner: repo.Owner, Name: repo.Name})
}

func (s *Handlers) RecordUntrackedOpen(ctx context.Context, input *UntrackedOpenInput) (*struct{}, error) {
	body := input.Body
	repo, err := httpapi.NormalizeUntrackedRepo(body.Provider, body.Host, body.Owner, body.Name)
	if err != nil {
		return nil, httpapi.Validation("body", "valid provider, host, owner and name are required")
	}
	if _, tracked := s.trackedConfiguredRepoSet()[untrackedRepoKey(repo)]; !tracked {
		if err := httpapi.RecordNormalizedUntrackedRepoOpen(ctx, s.Db, repo, body.ItemType, body.Number); err != nil {
			return nil, httpapi.Internal("record untracked repository open: " + err.Error())
		}
	}
	return nil, nil
}

func (s *Handlers) ListUntrackedOpens(ctx context.Context, _ *struct{}) (*UntrackedOpensOutput, error) {
	repos, err := s.Db.ListUntrackedRepoOpens(ctx)
	if err != nil {
		return nil, httpapi.Internal("list untracked repository opens: " + err.Error())
	}
	tracked := s.trackedConfiguredRepoSet()
	result := make([]db.UntrackedRepoOpen, 0, len(repos))
	for _, repo := range repos {
		if _, ok := tracked[untrackedRepoKey(repo)]; !ok {
			result = append(result, repo)
		}
	}
	return &UntrackedOpensOutput{Body: result}, nil
}

package pullapi

import (
	"context"
	"strings"

	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/platform"
)

type updateBranchBody struct {
	ExpectedHeadSHA string `json:"expected_head_sha" minLength:"1"`
}

type updateBranchInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         updateBranchBody
}

type updateBranchHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         updateBranchBody
}

func (s *Handler) updateBranchOnHost(ctx context.Context, input *updateBranchHostInput) (*actionStatusOutput, error) {
	return s.updateBranch(ctx, &updateBranchInput{
		Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner, Name: input.Name, Number: input.Number, Body: input.Body,
	})
}

func (s *Handler) updateBranch(ctx context.Context, input *updateBranchInput) (*actionStatusOutput, error) {
	repo, err := s.requireRepoRouteCapability(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, "update_branch")
	if err != nil {
		return nil, err
	}
	if err := s.requireSyncerCapability(repo.Repo, "update_branch"); err != nil {
		return nil, err
	}
	mr, err := s.requireVisibleMergeRequest(ctx, repo.Row(), input.Number)
	if err != nil {
		return nil, err
	}
	if !httpapi.BranchUpdateEligible(*mr) {
		return nil, httpapi.Conflict(httpapi.CodeConflict, "Refresh this pull request; only an open branch behind its base can be updated.", nil)
	}
	if strings.TrimSpace(input.Body.ExpectedHeadSHA) == "" {
		return nil, httpapi.Validation("body.expected_head_sha", "required")
	}
	if err := verifyClientReviewedHeadWithoutRefresh(input.Body.ExpectedHeadSHA, mr.PlatformHeadSHA); err != nil {
		return nil, err
	}
	provider, err := s.syncer.Registry().Provider(repoProviderKind(repo.Repo), repoProviderHost(repo.Repo))
	mutator, ok := provider.(platform.BranchUpdateMutator)
	if err != nil || !ok {
		return nil, unsupportedCapabilityProblem(repo.Repo, "update_branch")
	}
	if err := mutator.UpdateBranch(ctx, platformRepoRefFromDB(repo.Repo), input.Number, input.Body.ExpectedHeadSHA); err != nil {
		return nil, httpapi.ProviderMutationProblem(err, string(repoProviderKind(repo.Repo)), repoProviderHost(repo.Repo))
	}
	// register makes 202 Accepted this operation's default status.
	return &actionStatusOutput{Body: ActionStatusBody{Status: "update_requested"}}, nil
}

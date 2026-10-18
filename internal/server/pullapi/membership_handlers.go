package pullapi

import (
	"context"
	"strings"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
)

type updatePullMembershipInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.UpdateMembershipRequest
}

type updatePullMembershipHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.UpdateMembershipRequest
}

func (in *updatePullMembershipHostInput) route() *updatePullMembershipInput {
	return &updatePullMembershipInput{
		Provider:     in.Provider,
		PlatformHost: in.PlatformHost,
		Owner:        in.Owner,
		Name:         in.Name,
		Number:       in.Number,
		Body:         in.Body,
	}
}

// storedPull reads the pull request whose stored labels and assignees a
// membership update applies to.
func (s *Handler) storedPull(ctx context.Context, input *updatePullMembershipInput) (*db.MergeRequest, error) {
	repo, err := s.lookupRepoByProviderRoute(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	return s.requireVisibleMergeRequest(ctx, repo.Row(), input.Number)
}

// updatePullLabels adds and removes labels on a pull request, keeping every
// label the request does not mention.
func (s *Handler) updatePullLabels(
	ctx context.Context,
	input *updatePullMembershipInput,
) (*setLabelsOutput, error) {
	mr, err := s.storedPull(ctx, input)
	if err != nil {
		return nil, err
	}
	current := make([]string, 0, len(mr.Labels))
	for _, label := range mr.Labels {
		current = append(current, label.Name)
	}
	labels := httpapi.ApplyMembership(current, input.Body, func(a, b string) bool { return a == b })
	return s.setPullLabels(ctx, &setPullLabelsInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         httpapi.SetLabelsRequest{Labels: &labels},
	})
}

func (s *Handler) updatePullLabelsOnHost(
	ctx context.Context,
	input *updatePullMembershipHostInput,
) (*setLabelsOutput, error) {
	return s.updatePullLabels(ctx, input.route())
}

// updatePullAssignees adds and removes assignees on a pull request, keeping
// every assignee the request does not mention. Usernames compare without
// case, as the providers treat them.
func (s *Handler) updatePullAssignees(
	ctx context.Context,
	input *updatePullMembershipInput,
) (*setAssigneesOutput, error) {
	mr, err := s.storedPull(ctx, input)
	if err != nil {
		return nil, err
	}
	assignees := httpapi.ApplyMembership(db.ParseUserNamesJSON(mr.AssigneesJSON), input.Body, strings.EqualFold)
	return s.setPullAssignees(ctx, &setPullAssigneesInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         httpapi.SetAssigneesRequest{Assignees: &assignees},
	})
}

func (s *Handler) updatePullAssigneesOnHost(
	ctx context.Context,
	input *updatePullMembershipHostInput,
) (*setAssigneesOutput, error) {
	return s.updatePullAssignees(ctx, input.route())
}

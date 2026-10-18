package pullapi

import (
	"context"
	"strings"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/platform"
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

// livePull reads the pull request from the provider, so a membership change
// applies to what the provider has now rather than to Forge's last sync. The
// item must still be visible in Forge, and the capability enabled, before any
// provider call.
func (s *Handler) livePull(
	ctx context.Context,
	input *updatePullMembershipInput,
	capability string,
) (*db.MergeRequest, platform.MergeRequest, error) {
	active, err := s.lookupRepoByProviderRoute(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name)
	if err != nil {
		return nil, platform.MergeRequest{}, providerRouteLookupError(err)
	}
	if s.syncer == nil || !capabilityEnabled(s.capabilitiesForRepo(active.Repo), capability) {
		return nil, platform.MergeRequest{}, unsupportedCapabilityProblem(active.Repo, capability)
	}
	repo := active.Row()
	stored, err := s.requireVisibleMergeRequest(ctx, repo, input.Number)
	if err != nil {
		return nil, platform.MergeRequest{}, err
	}
	reader, err := s.syncer.MergeRequestReader(repoProviderKind(*repo), repoProviderHost(*repo))
	if err != nil {
		return nil, platform.MergeRequest{}, unsupportedCapabilityProblem(*repo, capability)
	}
	live, err := reader.GetMergeRequest(ctx, platformRepoRefFromDB(*repo), input.Number)
	if err != nil {
		return nil, platform.MergeRequest{}, httpapi.ProviderCallProblemWithDetail(
			err,
			string(repoProviderKind(*repo)), repoProviderHost(*repo),
			"provider API error: "+err.Error(),
		)
	}
	return stored, live, nil
}

// updatePullLabels adds and removes labels on a pull request, keeping every
// label the request does not mention.
func (s *Handler) updatePullLabels(
	ctx context.Context,
	input *updatePullMembershipInput,
) (*setLabelsOutput, error) {
	_, live, err := s.livePull(ctx, input, capabilityLabelMutation)
	if err != nil {
		return nil, err
	}
	current := make([]string, 0, len(live.Labels))
	for _, label := range live.Labels {
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
	stored, live, err := s.livePull(ctx, input, capabilityAssigneeMutation)
	if err != nil {
		return nil, err
	}
	current := live.Assignees
	if current == nil {
		// The provider did not report assignees; the synced set is the best known.
		current = db.ParseUserNamesJSON(stored.AssigneesJSON)
	}
	assignees := httpapi.ApplyMembership(current, input.Body, strings.EqualFold)
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

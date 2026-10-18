package issueapi

import (
	"context"
	"strings"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/platform"
)

type updateIssueMembershipInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.UpdateMembershipRequest
}

type updateIssueMembershipHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.UpdateMembershipRequest
}

func (in *updateIssueMembershipHostInput) route() *updateIssueMembershipInput {
	return &updateIssueMembershipInput{
		Provider:     in.Provider,
		PlatformHost: in.PlatformHost,
		Owner:        in.Owner,
		Name:         in.Name,
		Number:       in.Number,
		Body:         in.Body,
	}
}

// liveIssue reads the issue from the provider, so a membership change applies
// to what the provider has now rather than to Forge's last sync. The item must
// still be visible in Forge, and the capability enabled, before any provider
// call.
func (s *Handler) liveIssue(
	ctx context.Context,
	input *updateIssueMembershipInput,
	capability string,
) (*db.Issue, platform.Issue, error) {
	active, err := s.resolver.LookupRoute(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name)
	if err != nil {
		return nil, platform.Issue{}, httpapi.ProviderRouteLookupError(err)
	}
	if s.syncer == nil || !httpapi.CapabilityEnabled(s.resolver.CapabilitiesForRepo(active.Repo), capability) {
		return nil, platform.Issue{}, httpapi.UnsupportedCapability(active.Repo, capability)
	}
	repo := active.Row()
	stored, err := s.requireVisibleIssue(ctx, repo, input.Number)
	if err != nil {
		return nil, platform.Issue{}, err
	}
	reader, err := s.syncer.IssueReader(httpapi.ProviderKind(*repo), httpapi.ProviderHost(*repo))
	if err != nil {
		return nil, platform.Issue{}, httpapi.UnsupportedCapability(*repo, capability)
	}
	live, err := reader.GetIssue(ctx, httpapi.PlatformRepoRef(*repo), input.Number)
	if err != nil {
		return nil, platform.Issue{}, httpapi.ProviderCallProblemWithDetail(
			err, string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo), "provider API error: "+err.Error(),
		)
	}
	return stored, live, nil
}

// updateIssueLabels adds and removes labels on an issue, keeping every label
// the request does not mention.
func (s *Handler) updateIssueLabels(
	ctx context.Context,
	input *updateIssueMembershipInput,
) (*setLabelsOutput, error) {
	_, live, err := s.liveIssue(ctx, input, capabilityLabelMutation)
	if err != nil {
		return nil, err
	}
	current := make([]string, 0, len(live.Labels))
	for _, label := range live.Labels {
		current = append(current, label.Name)
	}
	labels := httpapi.ApplyMembership(current, input.Body, func(a, b string) bool { return a == b })
	return s.setIssueLabels(ctx, &setIssueLabelsInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         httpapi.SetLabelsRequest{Labels: &labels},
	})
}

func (s *Handler) updateIssueLabelsOnHost(
	ctx context.Context,
	input *updateIssueMembershipHostInput,
) (*setLabelsOutput, error) {
	return s.updateIssueLabels(ctx, input.route())
}

// updateIssueAssignees adds and removes assignees on an issue, keeping every
// assignee the request does not mention. Usernames compare without case, as
// the providers treat them.
func (s *Handler) updateIssueAssignees(
	ctx context.Context,
	input *updateIssueMembershipInput,
) (*setAssigneesOutput, error) {
	stored, live, err := s.liveIssue(ctx, input, capabilityAssigneeMutation)
	if err != nil {
		return nil, err
	}
	current := live.Assignees
	if current == nil {
		// The provider did not report assignees; the synced set is the best known.
		current = stored.Assignees
	}
	assignees := httpapi.ApplyMembership(current, input.Body, strings.EqualFold)
	return s.setIssueAssignees(ctx, &setIssueAssigneesInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         httpapi.SetAssigneesRequest{Assignees: &assignees},
	})
}

func (s *Handler) updateIssueAssigneesOnHost(
	ctx context.Context,
	input *updateIssueMembershipHostInput,
) (*setAssigneesOutput, error) {
	return s.updateIssueAssignees(ctx, input.route())
}

package issueapi

import (
	"context"
	"fmt"
	"strings"

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
// call. The caller must call the returned release after writing.
func (s *Handler) liveIssue(
	ctx context.Context,
	input *updateIssueMembershipInput,
	capability string,
	field string,
) (platform.Issue, func(), error) {
	active, err := s.resolver.LookupRoute(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name)
	if err != nil {
		return platform.Issue{}, nil, httpapi.ProviderRouteLookupError(err)
	}
	if s.syncer == nil || !httpapi.CapabilityEnabled(s.resolver.CapabilitiesForRepo(active.Repo), capability) {
		return platform.Issue{}, nil, httpapi.UnsupportedCapability(active.Repo, capability)
	}
	repo := active.Row()
	if _, err := s.requireVisibleIssue(ctx, repo, input.Number); err != nil {
		return platform.Issue{}, nil, err
	}
	reader, err := s.syncer.IssueReader(httpapi.ProviderKind(*repo), httpapi.ProviderHost(*repo))
	if err != nil {
		return platform.Issue{}, nil, httpapi.UnsupportedCapability(*repo, capability)
	}
	// Held until the caller has written the new set, so concurrent changes to
	// the same field cannot read the same starting set and drop each other.
	release := s.membershipLocks.Lock(fmt.Sprintf("%d\x00%d\x00%s", repo.ID, input.Number, field))
	live, err := reader.GetIssue(ctx, httpapi.PlatformRepoRef(*repo), input.Number)
	if err != nil {
		release()
		return platform.Issue{}, nil, httpapi.ProviderCallProblemWithDetail(
			err, string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo), "provider API error: "+err.Error(),
		)
	}
	return live, release, nil
}

// updateIssueLabels adds and removes labels on an issue, keeping every label
// the request does not mention.
func (s *Handler) updateIssueLabels(
	ctx context.Context,
	input *updateIssueMembershipInput,
) (*setLabelsOutput, error) {
	live, release, err := s.liveIssue(ctx, input, capabilityLabelMutation, "labels")
	if err != nil {
		return nil, err
	}
	defer release()
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
	live, release, err := s.liveIssue(ctx, input, capabilityAssigneeMutation, "assignees")
	if err != nil {
		return nil, err
	}
	defer release()
	// A single-item provider read always carries assignees, so a missing list
	// means nobody is assigned, never "unknown".
	assignees := httpapi.ApplyMembership(live.Assignees, input.Body, strings.EqualFold)
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

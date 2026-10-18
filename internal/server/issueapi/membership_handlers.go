package issueapi

import (
	"context"
	"strings"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
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

// storedIssue reads the issue whose stored labels and assignees a membership
// update applies to.
func (s *Handler) storedIssue(ctx context.Context, input *updateIssueMembershipInput) (*db.Issue, error) {
	repo, err := s.resolver.LookupRoute(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	return s.requireVisibleIssue(ctx, repo.Row(), input.Number)
}

// updateIssueLabels adds and removes labels on an issue, keeping every label
// the request does not mention.
func (s *Handler) updateIssueLabels(
	ctx context.Context,
	input *updateIssueMembershipInput,
) (*setLabelsOutput, error) {
	issue, err := s.storedIssue(ctx, input)
	if err != nil {
		return nil, err
	}
	current := make([]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
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
	issue, err := s.storedIssue(ctx, input)
	if err != nil {
		return nil, err
	}
	assignees := httpapi.ApplyMembership(issue.Assignees, input.Body, strings.EqualFold)
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

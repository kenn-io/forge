package pullapi

import (
	"context"
	"fmt"
	"strings"

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
// provider call. The caller must call the returned release after writing.
func (s *Handler) livePull(
	ctx context.Context,
	input *updatePullMembershipInput,
	capability string,
	field string,
) (platform.MergeRequest, func(), error) {
	active, err := s.lookupRepoByProviderRoute(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name)
	if err != nil {
		return platform.MergeRequest{}, nil, providerRouteLookupError(err)
	}
	if s.syncer == nil || !capabilityEnabled(s.capabilitiesForRepo(active.Repo), capability) {
		return platform.MergeRequest{}, nil, unsupportedCapabilityProblem(active.Repo, capability)
	}
	repo := active.Row()
	if _, err := s.requireVisibleMergeRequest(ctx, repo, input.Number); err != nil {
		return platform.MergeRequest{}, nil, err
	}
	reader, err := s.syncer.MergeRequestReader(repoProviderKind(*repo), repoProviderHost(*repo))
	if err != nil {
		return platform.MergeRequest{}, nil, unsupportedCapabilityProblem(*repo, capability)
	}
	// Held until the caller has written the new set, so concurrent changes to
	// the same field cannot read the same starting set and drop each other.
	release := s.membershipLocks.Lock(membershipLockKey(repo.ID, input.Number, field))
	live, err := reader.GetMergeRequest(ctx, platformRepoRefFromDB(*repo), input.Number)
	if err != nil {
		release()
		return platform.MergeRequest{}, nil, httpapi.ProviderCallProblemWithDetail(
			err,
			string(repoProviderKind(*repo)), repoProviderHost(*repo),
			"provider API error: "+err.Error(),
		)
	}
	return live, release, nil
}

// updatePullLabels adds and removes labels on a pull request, keeping every
// label the request does not mention.
func (s *Handler) updatePullLabels(
	ctx context.Context,
	input *updatePullMembershipInput,
) (*setLabelsOutput, error) {
	live, release, err := s.livePull(ctx, input, capabilityLabelMutation, "labels")
	if err != nil {
		return nil, err
	}
	defer release()
	current := make([]string, 0, len(live.Labels))
	for _, label := range live.Labels {
		current = append(current, label.Name)
	}
	labels := httpapi.ApplyMembership(current, input.Body, func(a, b string) bool { return a == b })
	return s.writePullLabels(ctx, &setPullLabelsInput{
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
	live, release, err := s.livePull(ctx, input, capabilityAssigneeMutation, "assignees")
	if err != nil {
		return nil, err
	}
	defer release()
	// A single-item provider read always carries assignees, so a missing list
	// means nobody is assigned, never "unknown".
	assignees := httpapi.ApplyMembership(live.Assignees, input.Body, strings.EqualFold)
	return s.writePullAssignees(ctx, &setPullAssigneesInput{
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

// membershipLockKey identifies one field of one item for the membership lock.
func membershipLockKey(repoID int64, number int, field string) string {
	return fmt.Sprintf("%d\x00%d\x00%s", repoID, number, field)
}

// lockPullMembership takes the lock the PATCH handlers hold, so a full
// replacement cannot land between a PATCH's provider read and its write. A
// route that does not resolve takes no lock; the write reports the error.
func (s *Handler) lockPullMembership(
	ctx context.Context,
	provider, platformHost, owner, name string,
	number int,
	field string,
) func() {
	active, err := s.lookupRepoByProviderRoute(ctx, provider, platformHost, owner, name)
	if err != nil {
		return func() {}
	}
	return s.membershipLocks.Lock(membershipLockKey(active.Row().ID, number, field))
}

// setPullLabels replaces the pull request's full labels set.
func (s *Handler) setPullLabels(ctx context.Context, input *setPullLabelsInput) (*setLabelsOutput, error) {
	defer s.lockPullMembership(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Number, "labels")()
	return s.writePullLabels(ctx, input)
}

// setPullAssignees replaces the pull request's full assignees set.
func (s *Handler) setPullAssignees(ctx context.Context, input *setPullAssigneesInput) (*setAssigneesOutput, error) {
	defer s.lockPullMembership(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Number, "assignees")()
	return s.writePullAssignees(ctx, input)
}

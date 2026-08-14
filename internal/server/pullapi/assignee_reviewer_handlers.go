package pullapi

import (
	"context"
	"fmt"
	"strings"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
)

type setAssigneesOutput = httpapi.BodyOutput[httpapi.ItemAssigneesResponse]
type setReviewersOutput = httpapi.BodyOutput[itemReviewersResponse]

type setPullAssigneesInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.SetAssigneesRequest
}

type setPullReviewersInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         setReviewersRequest
}

type setReviewersRequest struct {
	Reviewers *[]string `json:"reviewers" required:"true" nullable:"false"`
}

type itemReviewersResponse struct {
	Reviewers []string `json:"reviewers"`
}

// setPullAssignees replaces the full assignee set on a pull request.
func (s *Handler) setPullAssignees(
	ctx context.Context,
	input *setPullAssigneesInput,
) (*setAssigneesOutput, error) {
	repo, names, err := s.resolveUserMutationRequest(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityAssigneeMutation, "body.assignees", input.Body.Assignees,
	)
	if err != nil {
		return nil, err
	}

	mr, err := s.visibleMergeRequest(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get pull failed")
	}
	if mr == nil {
		return nil, httpapi.NotFound(httpapi.CodePullNotFound, "pull not found", nil)
	}

	mutator, err := s.syncer.AssigneeMutator(repoProviderKind(*repo), repoProviderHost(*repo))
	if err != nil {
		return nil, unsupportedCapabilityProblem(*repo, capabilityAssigneeMutation)
	}
	assignees, err := mutator.SetMergeRequestAssignees(
		ctx, platformRepoRefFromDB(*repo), input.Number, names,
	)
	if err != nil {
		return nil, httpapi.ProviderCallProblemWithDetail(
			err,
			string(repoProviderKind(*repo)), repoProviderHost(*repo),
			"provider API error: "+err.Error(),
		)
	}
	if err := s.db.UpdateMergeRequestAssignees(ctx, repo.ID, mr.ID, assignees); err != nil {
		return nil, httpapi.Internal("save pull assignees failed")
	}
	return &setAssigneesOutput{Body: httpapi.ItemAssigneesResponse{Assignees: emptyIfNil(assignees)}}, nil
}

// setIssueAssignees replaces the full assignee set on an issue.
func (s *Handler) setPullReviewers(
	ctx context.Context,
	input *setPullReviewersInput,
) (*setReviewersOutput, error) {
	repo, names, err := s.resolveUserMutationRequest(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityReviewerMutation, "body.reviewers", input.Body.Reviewers,
	)
	if err != nil {
		return nil, err
	}

	mr, err := s.visibleMergeRequest(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get pull failed")
	}
	if mr == nil {
		return nil, httpapi.NotFound(httpapi.CodePullNotFound, "pull not found", nil)
	}

	mutator, err := s.syncer.ReviewerMutator(repoProviderKind(*repo), repoProviderHost(*repo))
	if err != nil {
		return nil, unsupportedCapabilityProblem(*repo, capabilityReviewerMutation)
	}

	// Resolve the current requested-reviewer set from the provider, not
	// from the last synced row: the synced state can be stale (drift
	// from edits made outside kenn-forge) or unknown (reviewers_json was
	// never reported), and either would make the diff below silently
	// skip removals. An empty request is the providers' read primitive.
	ref := platformRepoRefFromDB(*repo)
	current, err := mutator.RequestMergeRequestReviewers(ctx, ref, input.Number, nil)
	if err != nil {
		return nil, httpapi.ProviderCallProblemWithDetail(
			err,
			string(repoProviderKind(*repo)), repoProviderHost(*repo),
			"provider API error: "+err.Error(),
		)
	}

	toAdd := diffUserNames(names, current)
	toRemove := diffUserNames(current, names)
	reviewers := current
	if len(toAdd) > 0 {
		reviewers, err = mutator.RequestMergeRequestReviewers(ctx, ref, input.Number, toAdd)
		// Persist any successful provider change immediately so a
		// failure in the later removal step cannot leave the DB
		// describing pre-mutation state while the provider moved on.
		if err == nil {
			if dbErr := s.db.UpdateMergeRequestReviewers(ctx, repo.ID, mr.ID, reviewers); dbErr != nil {
				return nil, httpapi.Internal("save pull reviewers failed")
			}
		}
		if err != nil {
			return nil, httpapi.ProviderCallProblemWithDetail(
				err,
				string(repoProviderKind(*repo)), repoProviderHost(*repo),
				"provider API error: "+err.Error(),
			)
		}
	}
	if len(toRemove) > 0 {
		reviewers, err = mutator.RemoveMergeRequestReviewers(ctx, ref, input.Number, toRemove)
		if err != nil {
			return nil, httpapi.ProviderCallProblemWithDetail(
				err,
				string(repoProviderKind(*repo)), repoProviderHost(*repo),
				"provider API error: "+err.Error(),
			)
		}
	}
	if err := s.db.UpdateMergeRequestReviewers(ctx, repo.ID, mr.ID, reviewers); err != nil {
		return nil, httpapi.Internal("save pull reviewers failed")
	}
	return &setReviewersOutput{Body: itemReviewersResponse{Reviewers: emptyIfNil(reviewers)}}, nil
}

// resolveUserMutationRequest performs the shared route lookup, capability
// check, and username validation for assignee/reviewer mutations.
func (s *Handler) resolveUserMutationRequest(
	ctx context.Context,
	provider, platformHost, owner, name string,
	capability string,
	field string,
	raw *[]string,
) (*db.Repo, []string, error) {
	repo, err := s.lookupRepoByProviderRoute(ctx, provider, platformHost, owner, name)
	if err != nil {
		return nil, nil, providerRouteLookupError(err)
	}
	if !capabilityEnabled(s.capabilitiesForRepo(*repo), capability) {
		return nil, nil, unsupportedCapabilityProblem(*repo, capability)
	}
	if s.syncer == nil {
		return nil, nil, unsupportedCapabilityProblem(*repo, capability)
	}
	if raw == nil {
		return nil, nil, httpapi.Validation(field, "value must be an array of usernames")
	}

	seen := make(map[string]struct{}, len(*raw))
	resolved := make([]string, 0, len(*raw))
	for _, value := range *raw {
		username := strings.TrimSpace(value)
		if username == "" {
			return nil, nil, httpapi.Validation(field, "usernames must not be empty")
		}
		key := strings.ToLower(username)
		if _, ok := seen[key]; ok {
			return nil, nil, httpapi.Validation(field, fmt.Sprintf("duplicate username %q", username))
		}
		seen[key] = struct{}{}
		resolved = append(resolved, username)
	}
	return repo, resolved, nil
}

// diffUserNames returns the entries of want that are absent from have,
// comparing case-insensitively because provider usernames are
// case-preserving but not case-sensitive.
func diffUserNames(want, have []string) []string {
	haveSet := make(map[string]struct{}, len(have))
	for _, name := range have {
		haveSet[strings.ToLower(name)] = struct{}{}
	}
	var out []string
	for _, name := range want {
		if _, ok := haveSet[strings.ToLower(name)]; !ok {
			out = append(out, name)
		}
	}
	return out
}

func emptyIfNil(names []string) []string {
	if names == nil {
		return []string{}
	}
	return names
}

package server

import (
	"context"
	"fmt"
	"strings"

	"go.kenn.io/middleman/internal/db"
)

type setAssigneesOutput = bodyOutput[itemAssigneesResponse]
type setReviewersOutput = bodyOutput[itemReviewersResponse]

type setPullAssigneesInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         setAssigneesRequest
}

type setIssueAssigneesInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         setAssigneesRequest
}

type setPullReviewersInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         setReviewersRequest
}

// The pointer distinguishes a missing field from an empty array at
// decode time, but null is not a valid wire value: an empty array is
// the only way to clear the set.
type setAssigneesRequest struct {
	Assignees *[]string `json:"assignees" required:"true" nullable:"false"`
}

type setReviewersRequest struct {
	Reviewers *[]string `json:"reviewers" required:"true" nullable:"false"`
}

type itemAssigneesResponse struct {
	Assignees []string `json:"assignees"`
}

type itemReviewersResponse struct {
	Reviewers []string `json:"reviewers"`
}

// setPullAssignees replaces the full assignee set on a pull request.
func (s *Server) setPullAssignees(
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

	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, problemInternal("get pull failed")
	}
	if mr == nil {
		return nil, problemNotFound(CodePullNotFound, "pull not found", nil)
	}

	mutator, err := s.syncer.AssigneeMutator(repoProviderKind(*repo), repoProviderHost(*repo))
	if err != nil {
		return nil, unsupportedCapabilityProblem(*repo, capabilityAssigneeMutation)
	}
	assignees, err := mutator.SetMergeRequestAssignees(
		ctx, platformRepoRefFromDB(*repo), input.Number, names,
	)
	if err != nil {
		return nil, providerCallProblemWithDetail(
			err,
			string(repoProviderKind(*repo)), repoProviderHost(*repo),
			"provider API error: "+err.Error(),
		)
	}
	if err := s.db.UpdateMergeRequestAssignees(ctx, repo.ID, mr.ID, assignees); err != nil {
		return nil, problemInternal("save pull assignees failed")
	}
	return &setAssigneesOutput{Body: itemAssigneesResponse{Assignees: emptyIfNil(assignees)}}, nil
}

// setIssueAssignees replaces the full assignee set on an issue.
func (s *Server) setIssueAssignees(
	ctx context.Context,
	input *setIssueAssigneesInput,
) (*setAssigneesOutput, error) {
	repo, names, err := s.resolveUserMutationRequest(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityAssigneeMutation, "body.assignees", input.Body.Assignees,
	)
	if err != nil {
		return nil, err
	}

	issue, err := s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, problemInternal("get issue failed")
	}
	if issue == nil {
		return nil, problemNotFound(CodeIssueNotFound, "issue not found", nil)
	}

	mutator, err := s.syncer.AssigneeMutator(repoProviderKind(*repo), repoProviderHost(*repo))
	if err != nil {
		return nil, unsupportedCapabilityProblem(*repo, capabilityAssigneeMutation)
	}
	assignees, err := mutator.SetIssueAssignees(
		ctx, platformRepoRefFromDB(*repo), input.Number, names,
	)
	if err != nil {
		return nil, providerCallProblemWithDetail(
			err,
			string(repoProviderKind(*repo)), repoProviderHost(*repo),
			"provider API error: "+err.Error(),
		)
	}
	if err := s.db.UpdateIssueAssignees(ctx, repo.ID, issue.ID, assignees); err != nil {
		return nil, problemInternal("save issue assignees failed")
	}
	return &setAssigneesOutput{Body: itemAssigneesResponse{Assignees: emptyIfNil(assignees)}}, nil
}

// setPullReviewers replaces the requested-reviewer set on a pull request.
// Providers expose request/remove operations, so the handler diffs the
// desired set against the last synced set and issues both calls.
func (s *Server) setPullReviewers(
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

	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, problemInternal("get pull failed")
	}
	if mr == nil {
		return nil, problemNotFound(CodePullNotFound, "pull not found", nil)
	}

	mutator, err := s.syncer.ReviewerMutator(repoProviderKind(*repo), repoProviderHost(*repo))
	if err != nil {
		return nil, unsupportedCapabilityProblem(*repo, capabilityReviewerMutation)
	}

	// Resolve the current requested-reviewer set from the provider, not
	// from the last synced row: the synced state can be stale (drift
	// from edits made outside middleman) or unknown (reviewers_json was
	// never reported), and either would make the diff below silently
	// skip removals. An empty request is the providers' read primitive.
	ref := platformRepoRefFromDB(*repo)
	current, err := mutator.RequestMergeRequestReviewers(ctx, ref, input.Number, nil)
	if err != nil {
		return nil, providerCallProblemWithDetail(
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
				return nil, problemInternal("save pull reviewers failed")
			}
		}
		if err != nil {
			return nil, providerCallProblemWithDetail(
				err,
				string(repoProviderKind(*repo)), repoProviderHost(*repo),
				"provider API error: "+err.Error(),
			)
		}
	}
	if len(toRemove) > 0 {
		reviewers, err = mutator.RemoveMergeRequestReviewers(ctx, ref, input.Number, toRemove)
		if err != nil {
			return nil, providerCallProblemWithDetail(
				err,
				string(repoProviderKind(*repo)), repoProviderHost(*repo),
				"provider API error: "+err.Error(),
			)
		}
	}
	if err := s.db.UpdateMergeRequestReviewers(ctx, repo.ID, mr.ID, reviewers); err != nil {
		return nil, problemInternal("save pull reviewers failed")
	}
	return &setReviewersOutput{Body: itemReviewersResponse{Reviewers: emptyIfNil(reviewers)}}, nil
}

// resolveUserMutationRequest performs the shared route lookup, capability
// check, and username validation for assignee/reviewer mutations.
func (s *Server) resolveUserMutationRequest(
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
		return nil, nil, problemValidation(field, "value must be an array of usernames")
	}

	seen := make(map[string]struct{}, len(*raw))
	resolved := make([]string, 0, len(*raw))
	for _, value := range *raw {
		username := strings.TrimSpace(value)
		if username == "" {
			return nil, nil, problemValidation(field, "usernames must not be empty")
		}
		key := strings.ToLower(username)
		if _, ok := seen[key]; ok {
			return nil, nil, problemValidation(field, fmt.Sprintf("duplicate username %q", username))
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

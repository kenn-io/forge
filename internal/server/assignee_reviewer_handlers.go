package server

import (
	"context"
	"fmt"
	"strings"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/server/httpapi"
)

type setAssigneesOutput = httpapi.BodyOutput[httpapi.ItemAssigneesResponse]
type setIssueAssigneesInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.SetAssigneesRequest
}

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
		return nil, httpapi.Internal("get issue failed")
	}
	if issue == nil {
		return nil, httpapi.NotFound(httpapi.CodeIssueNotFound, "issue not found", nil)
	}

	mutator, err := s.syncer.AssigneeMutator(httpapi.ProviderKind(*repo), httpapi.ProviderHost(*repo))
	if err != nil {
		return nil, httpapi.UnsupportedCapability(*repo, capabilityAssigneeMutation)
	}
	assignees, err := mutator.SetIssueAssignees(
		ctx, httpapi.PlatformRepoRef(*repo), input.Number, names,
	)
	if err != nil {
		return nil, httpapi.ProviderCallProblemWithDetail(
			err,
			string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo),
			"provider API error: "+err.Error(),
		)
	}
	if err := s.db.UpdateIssueAssignees(ctx, repo.ID, issue.ID, assignees); err != nil {
		return nil, httpapi.Internal("save issue assignees failed")
	}
	return &setAssigneesOutput{Body: httpapi.ItemAssigneesResponse{Assignees: emptyIfNil(assignees)}}, nil
}

// setPullReviewers replaces the requested-reviewer set on a pull request.
// Providers expose request/remove operations, so the handler diffs the
// desired set against the last synced set and issues both calls.
func (s *Server) resolveUserMutationRequest(
	ctx context.Context,
	provider, platformHost, owner, name string,
	capability string,
	field string,
	raw *[]string,
) (*db.Repo, []string, error) {
	repo, err := s.repoResolver.LookupRoute(ctx, provider, platformHost, owner, name)
	if err != nil {
		return nil, nil, httpapi.ProviderRouteLookupError(err)
	}
	if !httpapi.CapabilityEnabled(s.repoResolver.CapabilitiesForRepo(*repo), capability) {
		return nil, nil, httpapi.UnsupportedCapability(*repo, capability)
	}
	if s.syncer == nil {
		return nil, nil, httpapi.UnsupportedCapability(*repo, capability)
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
func emptyIfNil(names []string) []string {
	if names == nil {
		return []string{}
	}
	return names
}

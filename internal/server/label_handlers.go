package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/server/httpapi"
)

type listRepoLabelsOutput = httpapi.BodyOutput[repoLabelsResponse]
type setLabelsOutput = httpapi.BodyOutput[httpapi.ItemLabelsResponse]

type setIssueLabelsInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.SetLabelsRequest
}

type repoLabelsResponse struct {
	Labels    []db.Label `json:"labels"`
	Stale     bool       `json:"stale"`
	Syncing   bool       `json:"syncing"`
	SyncedAt  string     `json:"synced_at,omitempty"`
	CheckedAt string     `json:"checked_at,omitempty"`
	SyncError string     `json:"sync_error"`
}

func (s *Server) enqueueRepoLabelCatalogRefresh(repo db.Repo) bool {
	if s.syncer == nil {
		return false
	}
	s.labelCatalogRefreshMu.Lock()
	if _, ok := s.labelCatalogRefreshIDs[repo.ID]; ok {
		s.labelCatalogRefreshMu.Unlock()
		return true
	}
	s.labelCatalogRefreshIDs[repo.ID] = struct{}{}
	s.labelCatalogRefreshMu.Unlock()

	started := s.runBackground(func(ctx context.Context) {
		defer s.finishRepoLabelCatalogRefresh(repo.ID)
		_ = s.syncer.RefreshRepoLabelCatalog(ctx, repo)
	})
	if !started {
		s.finishRepoLabelCatalogRefresh(repo.ID)
		return false
	}
	return true
}

func (s *Server) finishRepoLabelCatalogRefresh(repoID int64) {
	s.labelCatalogRefreshMu.Lock()
	delete(s.labelCatalogRefreshIDs, repoID)
	s.labelCatalogRefreshMu.Unlock()
}

func (s *Server) listRepoLabels(
	ctx context.Context,
	input *getRepoInput,
) (*listRepoLabelsOutput, error) {
	repo, err := s.repoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	if !httpapi.CapabilityEnabled(s.repoResolver.CapabilitiesForRepo(*repo), capabilityReadLabels) {
		return nil, httpapi.UnsupportedCapability(*repo, capabilityReadLabels)
	}

	labels, freshness, err := s.db.ListRepoLabelCatalog(ctx, repo.ID)
	if err != nil {
		return nil, httpapi.Internal("list repo labels failed")
	}
	syncing := false
	if labelCatalogStale(freshness, time.Now().UTC()) {
		syncing = s.enqueueRepoLabelCatalogRefresh(*repo)
	}

	return &listRepoLabelsOutput{Body: repoLabelsResponse{
		Labels:    labels,
		Stale:     labelCatalogStale(freshness, time.Now().UTC()),
		Syncing:   syncing,
		SyncedAt:  optionalTimeString(freshness.SyncedAt),
		CheckedAt: optionalTimeString(freshness.CheckedAt),
		SyncError: freshness.SyncError,
	}}, nil
}

func (s *Server) setIssueLabels(
	ctx context.Context,
	input *setIssueLabelsInput,
) (*setLabelsOutput, error) {
	repo, names, err := s.resolveRequestedLabelNames(
		ctx,
		input.Provider,
		input.PlatformHost,
		input.Owner,
		input.Name,
		input.Body.LabelNames(),
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

	if s.syncer == nil {
		return nil, httpapi.UnsupportedCapability(*repo, capabilityLabelMutation)
	}
	mutator, err := s.syncer.LabelMutator(httpapi.ProviderKind(*repo), httpapi.ProviderHost(*repo))
	if err != nil {
		return nil, httpapi.UnsupportedCapability(*repo, capabilityLabelMutation)
	}
	providerLabels, err := mutator.SetIssueLabels(
		ctx, httpapi.PlatformRepoRef(*repo), input.Number, names,
	)
	if err != nil {
		return nil, httpapi.ProviderCallProblemWithDetail(
			err,
			string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo),
			"provider API error: "+err.Error(),
		)
	}
	labels := platform.DBLabels(providerLabels, time.Now().UTC())
	if err := s.db.ReplaceIssueLabels(ctx, repo.ID, issue.ID, labels); err != nil {
		return nil, httpapi.Internal("save issue labels failed")
	}
	// Re-read the stored rows: the label store merges provider responses
	// with the repo label catalog, so providers that return bare names
	// (GitLab) still yield color and description here.
	stored, err := s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil || stored == nil {
		return nil, httpapi.Internal("get issue failed")
	}
	return &setLabelsOutput{Body: httpapi.ItemLabelsResponse{Labels: stored.Labels}}, nil
}

func (s *Server) resolveRequestedLabelNames(
	ctx context.Context,
	provider string,
	platformHost string,
	owner string,
	name string,
	names []string,
) (*db.Repo, []string, error) {
	repo, err := s.repoResolver.LookupRoute(ctx, provider, platformHost, owner, name)
	if err != nil {
		return nil, nil, httpapi.ProviderRouteLookupError(err)
	}
	caps := s.repoResolver.CapabilitiesForRepo(*repo)
	if !httpapi.CapabilityEnabled(caps, capabilityReadLabels) {
		return nil, nil, httpapi.UnsupportedCapability(*repo, capabilityReadLabels)
	}
	if !httpapi.CapabilityEnabled(caps, capabilityLabelMutation) {
		return nil, nil, httpapi.UnsupportedCapability(*repo, capabilityLabelMutation)
	}
	if names == nil {
		return nil, nil, httpapi.Validation("body.labels", "labels must be an array")
	}

	catalog, freshness, err := s.db.ListRepoLabelCatalog(ctx, repo.ID)
	if err != nil {
		return nil, nil, httpapi.Internal("list repo labels failed")
	}
	if labelCatalogStale(freshness, time.Now().UTC()) && s.syncer != nil {
		_ = s.syncer.RefreshRepoLabelCatalog(ctx, *repo)
		catalog, _, err = s.db.ListRepoLabelCatalog(ctx, repo.ID)
		if err != nil {
			return nil, nil, httpapi.Internal("list repo labels failed")
		}
	}
	catalogByName := make(map[string]struct{}, len(catalog))
	for _, label := range catalog {
		catalogByName[label.Name] = struct{}{}
	}

	seen := make(map[string]struct{}, len(names))
	resolved := make([]string, 0, len(names))
	for _, raw := range names {
		labelName := strings.TrimSpace(raw)
		if labelName == "" {
			return nil, nil, httpapi.Validation("body.labels", "label names must not be empty")
		}
		if _, ok := seen[labelName]; ok {
			return nil, nil, httpapi.Validation(
				"body.labels", fmt.Sprintf("duplicate label %q", labelName),
			)
		}
		if _, ok := catalogByName[labelName]; !ok {
			return nil, nil, httpapi.NewProblem(
				http.StatusBadRequest,
				httpapi.CodeValidationError,
				fmt.Sprintf("label %q is not in the repository label catalog", labelName),
				map[string]any{"field": "body.labels", "label": labelName},
			)
		}
		seen[labelName] = struct{}{}
		resolved = append(resolved, labelName)
	}
	return repo, resolved, nil
}

func labelCatalogStale(freshness db.LabelCatalogFreshness, now time.Time) bool {
	if freshness.CheckedAt == nil {
		return true
	}
	return freshness.CheckedAt.Before(now.Add(-10 * time.Minute))
}

func optionalTimeString(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatUTCRFC3339(*t)
}

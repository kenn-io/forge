package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wesm/middleman/internal/db"
	"github.com/wesm/middleman/internal/platform"
)

type listRepoLabelsOutput = bodyOutput[repoLabelsResponse]
type setLabelsOutput = bodyOutput[itemLabelsResponse]

type setPullLabelsInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         setLabelsRequest
}

type setIssueLabelsInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         setLabelsRequest
}

type setLabelsRequest struct {
	Labels *[]string `json:"labels" required:"true"`
}

type repoLabelsResponse struct {
	Labels    []db.Label `json:"labels"`
	Stale     bool       `json:"stale"`
	Syncing   bool       `json:"syncing"`
	SyncedAt  string     `json:"synced_at,omitempty"`
	CheckedAt string     `json:"checked_at,omitempty"`
	SyncError string     `json:"sync_error"`
}

type itemLabelsResponse struct {
	Labels []db.Label `json:"labels"`
}

func (r setLabelsRequest) labelNames() []string {
	if r.Labels == nil {
		return nil
	}
	return *r.Labels
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
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	if !capabilityEnabled(s.capabilitiesForRepo(*repo), capabilityReadLabels) {
		return nil, unsupportedCapabilityProblem(*repo, capabilityReadLabels)
	}

	labels, freshness, err := s.db.ListRepoLabelCatalog(ctx, repo.ID)
	if err != nil {
		return nil, problemInternal("list repo labels failed")
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

func (s *Server) setPullLabels(
	ctx context.Context,
	input *setPullLabelsInput,
) (*setLabelsOutput, error) {
	repo, names, err := s.resolveRequestedLabelNames(
		ctx,
		input.Provider,
		input.PlatformHost,
		input.Owner,
		input.Name,
		input.Body.labelNames(),
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

	if s.syncer == nil {
		return nil, unsupportedCapabilityProblem(*repo, capabilityLabelMutation)
	}
	mutator, err := s.syncer.LabelMutator(repoProviderKind(*repo), repoProviderHost(*repo))
	if err != nil {
		return nil, unsupportedCapabilityProblem(*repo, capabilityLabelMutation)
	}
	providerLabels, err := mutator.SetMergeRequestLabels(
		ctx, platformRepoRefFromDB(*repo), input.Number, names,
	)
	if err != nil {
		return nil, providerCallProblemWithDetail(
			err,
			string(repoProviderKind(*repo)), repoProviderHost(*repo),
			"provider API error: "+err.Error(),
		)
	}
	labels := platform.DBLabels(providerLabels, time.Now().UTC())
	if err := s.db.ReplaceMergeRequestLabels(ctx, repo.ID, mr.ID, labels); err != nil {
		return nil, problemInternal("save pull labels failed")
	}
	return &setLabelsOutput{Body: itemLabelsResponse{Labels: labels}}, nil
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
		input.Body.labelNames(),
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

	if s.syncer == nil {
		return nil, unsupportedCapabilityProblem(*repo, capabilityLabelMutation)
	}
	mutator, err := s.syncer.LabelMutator(repoProviderKind(*repo), repoProviderHost(*repo))
	if err != nil {
		return nil, unsupportedCapabilityProblem(*repo, capabilityLabelMutation)
	}
	providerLabels, err := mutator.SetIssueLabels(
		ctx, platformRepoRefFromDB(*repo), input.Number, names,
	)
	if err != nil {
		return nil, providerCallProblemWithDetail(
			err,
			string(repoProviderKind(*repo)), repoProviderHost(*repo),
			"provider API error: "+err.Error(),
		)
	}
	labels := platform.DBLabels(providerLabels, time.Now().UTC())
	if err := s.db.ReplaceIssueLabels(ctx, repo.ID, issue.ID, labels); err != nil {
		return nil, problemInternal("save issue labels failed")
	}
	return &setLabelsOutput{Body: itemLabelsResponse{Labels: labels}}, nil
}

func (s *Server) resolveRequestedLabelNames(
	ctx context.Context,
	provider string,
	platformHost string,
	owner string,
	name string,
	names []string,
) (*db.Repo, []string, error) {
	repo, err := s.lookupRepoByProviderRoute(ctx, provider, platformHost, owner, name)
	if err != nil {
		return nil, nil, providerRouteLookupError(err)
	}
	caps := s.capabilitiesForRepo(*repo)
	if !capabilityEnabled(caps, capabilityReadLabels) {
		return nil, nil, unsupportedCapabilityProblem(*repo, capabilityReadLabels)
	}
	if !capabilityEnabled(caps, capabilityLabelMutation) {
		return nil, nil, unsupportedCapabilityProblem(*repo, capabilityLabelMutation)
	}
	if names == nil {
		return nil, nil, problemValidation("body.labels", "labels must be an array")
	}

	catalog, freshness, err := s.db.ListRepoLabelCatalog(ctx, repo.ID)
	if err != nil {
		return nil, nil, problemInternal("list repo labels failed")
	}
	if labelCatalogStale(freshness, time.Now().UTC()) && s.syncer != nil {
		_ = s.syncer.RefreshRepoLabelCatalog(ctx, *repo)
		catalog, _, err = s.db.ListRepoLabelCatalog(ctx, repo.ID)
		if err != nil {
			return nil, nil, problemInternal("list repo labels failed")
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
			return nil, nil, problemValidation("body.labels", "label names must not be empty")
		}
		if _, ok := seen[labelName]; ok {
			return nil, nil, problemValidation(
				"body.labels", fmt.Sprintf("duplicate label %q", labelName),
			)
		}
		if _, ok := catalogByName[labelName]; !ok {
			return nil, nil, newProblem(
				http.StatusBadRequest,
				CodeValidationError,
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

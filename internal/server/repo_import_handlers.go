package server

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"go.kenn.io/forge/internal/config"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/repoapi"
	"go.kenn.io/forge/internal/server/settingsapi"
	"go.kenn.io/forge/internal/server/spokeapi"
)

func (s *Server) applyBulkExactRepos(
	ctx context.Context,
	resolved []repoapi.ResolvedBulkRepo,
) (spokeapi.SettingsResponse, error) {
	s.configReloadMu.Lock()
	s.cfgMu.Lock()
	existing := repoapi.ExactConfiguredRepoSet(s.cfg.Repos)
	addConfigs := make([]config.Repo, 0, len(resolved))
	addRefs := make([]ghclient.RepoRef, 0, len(resolved))
	for _, repo := range resolved {
		key := repoapi.ConfiguredRepoImportKey(repo.Config)
		if _, ok := existing[key]; ok {
			continue
		}
		existing[key] = struct{}{}
		addConfigs = append(addConfigs, repo.Config)
		addRefs = append(addRefs, repo.Ref)
	}
	if len(addConfigs) == 0 {
		s.cfgMu.Unlock()
		s.configReloadMu.Unlock()
		return spokeapi.SettingsResponse{}, &repoapi.BulkApplyError{Problem: httpapi.BadRequest(
			httpapi.CodeBadRequest,
			"all selected repositories are already configured",
			nil,
		)}
	}

	prev := slices.Clone(s.cfg.Repos)
	s.cfg.Repos = append(s.cfg.Repos, addConfigs...)
	if err := s.cfg.Validate(); err != nil {
		s.cfg.Repos = prev
		s.cfgMu.Unlock()
		s.configReloadMu.Unlock()
		return spokeapi.SettingsResponse{}, &repoapi.BulkApplyError{Problem: httpapi.BadRequest(
			httpapi.CodeBadRequest, err.Error(), nil,
		)}
	}
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.Repos = prev
		s.cfgMu.Unlock()
		s.configReloadMu.Unlock()
		return spokeapi.SettingsResponse{}, &repoapi.BulkApplyError{Problem: httpapi.Internal(
			"save config: " + err.Error(),
		)}
	}
	if err := s.settingsapi.PersistResolvedRepos(ctx, addRefs); err != nil {
		s.cfg.Repos = prev
		s.cfgMu.Unlock()
		s.configReloadMu.Unlock()
		return spokeapi.SettingsResponse{}, &repoapi.BulkApplyError{Problem: httpapi.Internal(err.Error())}
	}
	s.settingsapi.MergeTrackedRepos(addRefs)
	s.streamapi.ApplyWorkspaceConfigLocked()
	s.cfgMu.Unlock()
	s.configReloadMu.Unlock()

	body, err := s.buildLocalSettingsResponse(ctx)
	if err != nil {
		return spokeapi.SettingsResponse{}, &repoapi.BulkApplyError{
			Problem: httpapi.Internal(err.Error()),
		}
	}
	return body, nil
}

func (s *Server) bulkAddRepos(
	ctx context.Context,
	input *repoapi.BulkAddReposInput,
) (*repoapi.BulkAddReposOutput, error) {
	if s.cfgPath == "" {
		return nil, httpapi.NotFound(httpapi.CodeSettingsUnavailable, "settings not available", nil)
	}

	if len(input.Body.Repos) == 0 {
		return nil, httpapi.Validation("body.repos", "repos are required")
	}

	candidates := make([]config.Repo, 0, len(input.Body.Repos))
	s.cfgMu.Lock()
	existing := repoapi.ExactConfiguredRepoSet(s.cfg.Repos)
	s.cfgMu.Unlock()
	for _, raw := range input.Body.Repos {
		repo, err := repoapi.NormalizeExactRepoInput(raw)
		if err != nil {
			return nil, httpapi.Validation("body.repos", err.Error())
		}
		key := repoapi.ConfiguredRepoImportKey(repo)
		if _, ok := existing[key]; ok {
			continue
		}
		candidates = append(candidates, repo)
	}
	if len(candidates) == 0 {
		return nil, httpapi.BadRequest(
			httpapi.CodeBadRequest,
			"all selected repositories are already configured",
			nil,
		)
	}

	resolved, err := repoapi.ValidateBulkExactRepos(ctx, s.syncer, candidates)
	if err != nil {
		return nil, settingsapi.ClassifyResolveProblem(err)
	}
	resp, err := s.applyBulkExactRepos(ctx, resolved)
	if err != nil {
		if bae, ok := errors.AsType[*repoapi.BulkApplyError](err); ok {
			return nil, bae.Problem
		}
		return nil, httpapi.Internal(err.Error())
	}

	s.syncer.TriggerRun(context.WithoutCancel(ctx))
	return &repoapi.BulkAddReposOutput{Status: http.StatusCreated, Body: resp}, nil
}

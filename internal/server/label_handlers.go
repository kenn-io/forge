package server

import (
	"context"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
)

type listRepoLabelsOutput = httpapi.BodyOutput[repoLabelsResponse]

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

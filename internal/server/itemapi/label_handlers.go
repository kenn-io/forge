package itemapi

import (
	"context"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
)

type ListRepoLabelsOutput = httpapi.BodyOutput[repoLabelsResponse]

type repoLabelsResponse struct {
	Labels    []db.Label `json:"labels"`
	Stale     bool       `json:"stale"`
	Syncing   bool       `json:"syncing"`
	SyncedAt  string     `json:"synced_at,omitempty"`
	CheckedAt string     `json:"checked_at,omitempty"`
	SyncError string     `json:"sync_error"`
}

func (s *Handlers) enqueueRepoLabelCatalogRefresh(repo db.Repo) bool {
	if (*s.Syncer) == nil {
		return false
	}
	s.LabelCatalogRefreshMu.Lock()
	if _, ok := s.LabelCatalogRefreshIDs[repo.ID]; ok {
		s.LabelCatalogRefreshMu.Unlock()
		return true
	}
	s.LabelCatalogRefreshIDs[repo.ID] = struct{}{}
	s.LabelCatalogRefreshMu.Unlock()

	started := s.RunBackground(func(ctx context.Context) {
		defer s.finishRepoLabelCatalogRefresh(repo.ID)
		_ = (*s.Syncer).RefreshRepoLabelCatalog(ctx, repo)
	})
	if !started {
		s.finishRepoLabelCatalogRefresh(repo.ID)
		return false
	}
	return true
}

func (s *Handlers) finishRepoLabelCatalogRefresh(repoID int64) {
	s.LabelCatalogRefreshMu.Lock()
	delete(s.LabelCatalogRefreshIDs, repoID)
	s.LabelCatalogRefreshMu.Unlock()
}

func (s *Handlers) ListRepoLabels(
	ctx context.Context,
	input *GetRepoInput,
) (*ListRepoLabelsOutput, error) {
	repo, err := s.RepoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	if !httpapi.CapabilityEnabled(s.RepoResolver.CapabilitiesForRepo(*repo), CapabilityReadLabels) {
		return nil, httpapi.UnsupportedCapability(*repo, CapabilityReadLabels)
	}

	labels, freshness, err := s.Db.ListRepoLabelCatalog(ctx, repo.ID)
	if err != nil {
		return nil, httpapi.Internal("list repo labels failed")
	}
	syncing := false
	if labelCatalogStale(freshness, time.Now().UTC()) {
		syncing = s.enqueueRepoLabelCatalogRefresh(*repo)
	}

	return &ListRepoLabelsOutput{Body: repoLabelsResponse{
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
	return FormatUTCRFC3339(*t)
}

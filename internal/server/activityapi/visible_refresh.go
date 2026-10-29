package activityapi

import (
	"context"
	"net/http"
	"strconv"

	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/platform"
)

// VisiblePullRef names one pull request the client shows.
type VisiblePullRef struct {
	Provider     string `json:"provider" doc:"Provider kind, such as github."`
	PlatformHost string `json:"platform_host,omitempty" doc:"Provider host; the provider's default host when empty."`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Number       int    `json:"number" minimum:"1"`
}

type refreshVisiblePullsInput struct {
	Body struct {
		Focus   *VisiblePullRef  `json:"focus,omitempty" doc:"The pull request the user opened. Its stack is refreshed too."`
		Visible []VisiblePullRef `json:"visible" maxItems:"200" doc:"Pull requests the client currently shows."`
	}
}

// RefreshVisiblePulls refreshes the pull requests a user is probably
// looking at: the visible rows, the opened pull request and its stack, and
// pull requests linked to a worktree. Batched probes find the stale ones,
// and only those get a background detail sync.
func (s *Handlers) RefreshVisiblePulls(
	ctx context.Context, input *refreshVisiblePullsInput,
) (*itemapi.AcceptedOutput, error) {
	if err := s.RequireSync(); err != nil {
		return nil, err
	}
	syncer := *s.Syncer
	// Airplane mode pauses speculative refreshes; the opened pull request
	// still syncs through its own item visit.
	if !syncer.AutomaticSyncEnabled() {
		return &itemapi.AcceptedOutput{Status: http.StatusAccepted}, nil
	}
	var focus *ghclient.WatchedMR
	if input.Body.Focus != nil {
		mr := visiblePullWatchedMR(*input.Body.Focus)
		focus = &mr
	}
	visible := make([]ghclient.WatchedMR, 0, len(input.Body.Visible))
	for _, ref := range input.Body.Visible {
		visible = append(visible, visiblePullWatchedMR(ref))
	}
	s.RunBackground(func(ctx context.Context) {
		ctx = ghclient.WithSyncBudget(ctx)
		for _, stale := range syncer.StaleVisibleMRs(ctx, focus, visible) {
			s.enqueueStaleVisiblePull(ctx, syncer, stale)
		}
	})
	return &itemapi.AcceptedOutput{Status: http.StatusAccepted}, nil
}

func (s *Handlers) enqueueStaleVisiblePull(
	ctx context.Context, syncer *ghclient.Syncer, stale ghclient.ProbedMR,
) {
	kind := stale.Platform
	if kind == "" {
		kind = platform.KindGitHub
	}
	repo, err := s.RepoResolver.LookupRoute(ctx, string(kind), stale.PlatformHost, stale.Owner, stale.Name)
	if err != nil {
		return
	}
	host := httpapi.ProviderHost(repo.Repo)
	s.EnqueueDetailSync(
		prDetailSyncKey(repo, stale.Number),
		[]any{
			"type", "visible-pr",
			"provider", string(kind),
			"platform_host", host,
			"repo_path", repo.RepoPath,
			"number", stale.Number,
		},
		func(ctx context.Context) error {
			err := syncer.SyncMROnProviderIfChanged(
				ghclient.WithSyncBudget(ctx), kind, host, repo.Owner, repo.Name, stale.Number,
			)
			if err == nil {
				syncer.RecordProbedMRRead(stale)
			}
			return err
		},
	)
}

// prDetailSyncKey names a pull request's background detail sync, so an item
// visit and a visible-set refresh share one in-flight job.
func prDetailSyncKey(repo *db.ActiveRepo, number int) string {
	return "pr:" + string(httpapi.ProviderKind(repo.Repo)) + ":" + httpapi.ProviderHost(repo.Repo) +
		":" + repo.RepoPath + "#" + strconv.Itoa(number)
}

func visiblePullWatchedMR(ref VisiblePullRef) ghclient.WatchedMR {
	return ghclient.WatchedMR{
		Platform:     platform.Kind(ref.Provider),
		PlatformHost: ref.PlatformHost,
		Owner:        ref.Owner,
		Name:         ref.Name,
		Number:       ref.Number,
	}
}

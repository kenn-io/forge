package syncevents

import (
	"context"
	"errors"
	"log/slog"

	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/platform"
)

type DetailSyncJob struct {
	attrs  []any
	fn     func(context.Context) error
	repo   *RepositoryEvent
	after  func(context.Context)
	repoID int64
	number int
}

type detailSyncKey struct{}

type detailSyncTarget struct {
	repoID int64
	number int
}

// TracksDetailSync reports whether the job will announce this PR's changes.
func TracksDetailSync(ctx context.Context, repoID int64, number int) bool {
	target, ok := ctx.Value(detailSyncKey{}).(detailSyncTarget)
	return ok && target.repoID == repoID && target.number == number
}

// RepositoryEvent identifies the repository whose stored data changed.
type RepositoryEvent struct {
	Provider     string                 `json:"provider"`
	PlatformHost string                 `json:"platform_host"`
	RepoPath     string                 `json:"repo_path"`
	Key          platform.RepositoryKey `json:"-" repokey:"platform_repo_id,bitbucket_repository_uuid,omitempty"`
}

func (r RepositoryEvent) MarshalJSON() ([]byte, error) {
	type plain RepositoryEvent
	return platform.MarshalKeyedJSON(plain(r))
}

func (r *RepositoryEvent) UnmarshalJSON(data []byte) error {
	type plain RepositoryEvent
	return platform.UnmarshalKeyedJSON(data, (*plain)(r))
}

type RepositoryChanges struct {
	Repositories []RepositoryEvent `json:"repositories"`
}

func (s *Handlers) EnqueueDetailSync(
	key string,
	attrs []any,
	repo db.Repo,
	number int,
	fn func(context.Context) error,
) bool {
	return s.enqueueDetailSyncOpts(key, attrs, repo, number, fn, nil, false)
}

func (s *Handlers) EnqueueDetailSyncWithCompletion(
	key string,
	attrs []any,
	repo db.Repo,
	number int,
	fn func(context.Context) error,
	after func(context.Context),
) bool {
	return s.enqueueDetailSyncOpts(key, attrs, repo, number, fn, after, false)
}

// enqueueDetailSyncOrRerun schedules one rerun after the in-flight sync
// for the same key completes instead of dropping the request. Post-mutation
// refreshes need this: an already-running sync may have read provider state
// from before the mutation, so deduping against it would leave the local
// row stale until the next periodic sync.
func (s *Handlers) EnqueueDetailSyncOrRerun(
	key string,
	attrs []any,
	repo db.Repo,
	number int,
	fn func(context.Context) error,
) bool {
	return s.enqueueDetailSyncOpts(key, attrs, repo, number, fn, nil, true)
}

// EnqueueDetailSyncOrRerunWithCompletion is EnqueueDetailSyncOrRerun with
// work that runs after a successful sync. A later request for the same key
// replaces a pending rerun's sync but keeps its completion work, so a
// mutation's follow-up still runs after the one sync that replaces it.
func (s *Handlers) EnqueueDetailSyncOrRerunWithCompletion(
	key string,
	attrs []any,
	repo db.Repo,
	number int,
	fn func(context.Context) error,
	after func(context.Context),
) bool {
	return s.enqueueDetailSyncOpts(key, attrs, repo, number, fn, after, true)
}

func (s *Handlers) enqueueDetailSyncOpts(
	key string,
	attrs []any,
	repo db.Repo,
	number int,
	fn func(context.Context) error,
	after func(context.Context),
	rerun bool,
) bool {
	event := RepositoryEvent{Provider: repo.Platform, PlatformHost: repo.PlatformHost, RepoPath: repo.RepoPath, Key: repo.Key}
	job := DetailSyncJob{attrs: attrs, repo: &event, repoID: repo.ID, number: number, fn: fn, after: after}
	s.DetailSyncMu.Lock()
	if (*s.DetailSyncInFlight) == nil {
		(*s.DetailSyncInFlight) = make(map[string]struct{})
	}
	if _, ok := (*s.DetailSyncInFlight)[key]; ok {
		if rerun {
			if (*s.DetailSyncPending) == nil {
				(*s.DetailSyncPending) = make(map[string]DetailSyncJob)
			}
			if replaced, ok := (*s.DetailSyncPending)[key]; ok && replaced.after != nil {
				job.after = chainDetailSyncCompletions(replaced.after, job.after)
			}
			(*s.DetailSyncPending)[key] = job
		}
		s.DetailSyncMu.Unlock()
		return false
	}
	(*s.DetailSyncInFlight)[key] = struct{}{}
	s.DetailSyncMu.Unlock()

	return s.startDetailSyncJob(key, job)
}

func chainDetailSyncCompletions(first, second func(context.Context)) func(context.Context) {
	if second == nil {
		return first
	}
	return func(ctx context.Context) {
		first(ctx)
		second(ctx)
	}
}

func (s *Handlers) startDetailSyncJob(key string, job DetailSyncJob) bool {
	started := s.RunBackground(func(ctx context.Context) {
		defer func() {
			var pending DetailSyncJob
			var hasPending bool
			s.DetailSyncMu.Lock()
			if (*s.DetailSyncPending) != nil {
				pending, hasPending = (*s.DetailSyncPending)[key]
				delete((*s.DetailSyncPending), key)
			}
			if !hasPending {
				delete((*s.DetailSyncInFlight), key)
			}
			s.DetailSyncMu.Unlock()
			if hasPending {
				s.startDetailSyncJob(key, pending)
			}
		}()

		var before db.DetailSnapshot
		var snapshotErr error
		if job.repoID != 0 {
			before, snapshotErr = s.Db.DetailChangeSnapshot(ctx, job.repoID, job.number)
			ctx = context.WithValue(ctx, detailSyncKey{}, detailSyncTarget{repoID: job.repoID, number: job.number})
		}
		err := job.fn(ctx)
		diffErr, isDiffErr := errors.AsType[*ghclient.DiffSyncError](err)
		if err != nil && !isDiffErr {
			slog.Warn("background detail sync failed", append(job.attrs, "err", err)...)
		}
		if diffErr != nil {
			slog.Warn("background PR diff sync failed", append(job.attrs, "code", diffErr.Code, "err", diffErr.Err)...)
		}
		if job.after != nil && (err == nil || isDiffErr) {
			job.after(ctx)
		}
		if job.repoID == 0 {
			if err == nil || isDiffErr {
				(*s.Hub).Broadcast(Event{Type: "data_changed", Data: struct{}{}})
			}
			return
		}
		after, afterErr := s.Db.DetailChangeSnapshot(ctx, job.repoID, job.number)
		if snapshotErr != nil || afterErr != nil {
			slog.Warn("background detail snapshot failed", append(job.attrs, "err", errors.Join(snapshotErr, afterErr))...)
			return
		}
		if before != after {
			(*s.Hub).Broadcast(Event{Type: "data_changed", Data: RepositoryChanges{Repositories: []RepositoryEvent{*job.repo}}})
		}
	})
	if started {
		return true
	}

	s.DetailSyncMu.Lock()
	delete((*s.DetailSyncInFlight), key)
	delete((*s.DetailSyncPending), key)
	s.DetailSyncMu.Unlock()
	return false
}

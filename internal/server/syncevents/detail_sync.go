package syncevents

import (
	"context"
	"errors"
	"log/slog"

	ghclient "go.kenn.io/forge/internal/github"
)

type DetailSyncJob struct {
	attrs []any
	fn    func(context.Context) error
	after func(context.Context)
}

func (s *Handlers) EnqueueDetailSync(
	key string,
	attrs []any,
	fn func(context.Context) error,
) bool {
	return s.enqueueDetailSyncOpts(key, attrs, fn, nil, false)
}

func (s *Handlers) EnqueueDetailSyncWithCompletion(
	key string,
	attrs []any,
	fn func(context.Context) error,
	after func(context.Context),
) bool {
	return s.enqueueDetailSyncOpts(key, attrs, fn, after, false)
}

// enqueueDetailSyncOrRerun schedules one rerun after the in-flight sync
// for the same key completes instead of dropping the request. Post-mutation
// refreshes need this: an already-running sync may have read provider state
// from before the mutation, so deduping against it would leave the local
// row stale until the next periodic sync.
func (s *Handlers) EnqueueDetailSyncOrRerun(
	key string,
	attrs []any,
	fn func(context.Context) error,
) bool {
	return s.enqueueDetailSyncOpts(key, attrs, fn, nil, true)
}

func (s *Handlers) enqueueDetailSyncOpts(
	key string,
	attrs []any,
	fn func(context.Context) error,
	after func(context.Context),
	rerun bool,
) bool {
	job := DetailSyncJob{attrs: attrs, fn: fn, after: after}
	s.DetailSyncMu.Lock()
	if (*s.DetailSyncInFlight) == nil {
		(*s.DetailSyncInFlight) = make(map[string]struct{})
	}
	if _, ok := (*s.DetailSyncInFlight)[key]; ok {
		if rerun {
			if (*s.DetailSyncPending) == nil {
				(*s.DetailSyncPending) = make(map[string]DetailSyncJob)
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

		err := job.fn(ctx)
		diffErr, isDiffErr := errors.AsType[*ghclient.DiffSyncError](err)
		if err != nil && !isDiffErr {
			slog.Warn("background detail sync failed", append(job.attrs, "err", err)...)
			return
		}
		if diffErr != nil {
			slog.Warn(
				"background PR diff sync failed",
				append(job.attrs, "code", diffErr.Code, "err", diffErr.Err)...,
			)
		}
		if job.after != nil {
			job.after(ctx)
		}
		(*s.Hub).Broadcast(Event{
			Type: "data_changed",
			Data: struct{}{},
		})
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

package server

import (
	"context"
	"errors"
	"log/slog"

	ghclient "go.kenn.io/middleman/internal/github"
)

type detailSyncJob struct {
	attrs []any
	fn    func(context.Context) error
	after func(context.Context)
}

func (s *Server) enqueueDetailSync(
	key string,
	attrs []any,
	fn func(context.Context) error,
) bool {
	return s.enqueueDetailSyncOpts(key, attrs, fn, nil, false)
}

func (s *Server) enqueueDetailSyncWithCompletion(
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
func (s *Server) enqueueDetailSyncOrRerun(
	key string,
	attrs []any,
	fn func(context.Context) error,
) bool {
	return s.enqueueDetailSyncOpts(key, attrs, fn, nil, true)
}

func (s *Server) enqueueDetailSyncOpts(
	key string,
	attrs []any,
	fn func(context.Context) error,
	after func(context.Context),
	rerun bool,
) bool {
	job := detailSyncJob{attrs: attrs, fn: fn, after: after}
	s.detailSyncMu.Lock()
	if s.detailSyncInFlight == nil {
		s.detailSyncInFlight = make(map[string]struct{})
	}
	if _, ok := s.detailSyncInFlight[key]; ok {
		if rerun {
			if s.detailSyncPending == nil {
				s.detailSyncPending = make(map[string]detailSyncJob)
			}
			s.detailSyncPending[key] = job
		}
		s.detailSyncMu.Unlock()
		return false
	}
	s.detailSyncInFlight[key] = struct{}{}
	s.detailSyncMu.Unlock()

	return s.startDetailSyncJob(key, job)
}

func (s *Server) startDetailSyncJob(key string, job detailSyncJob) bool {
	started := s.runBackground(func(ctx context.Context) {
		defer func() {
			var pending detailSyncJob
			var hasPending bool
			s.detailSyncMu.Lock()
			if s.detailSyncPending != nil {
				pending, hasPending = s.detailSyncPending[key]
				delete(s.detailSyncPending, key)
			}
			if !hasPending {
				delete(s.detailSyncInFlight, key)
			}
			s.detailSyncMu.Unlock()
			if hasPending {
				s.startDetailSyncJob(key, pending)
			}
		}()

		err := job.fn(ctx)
		var diffErr *ghclient.DiffSyncError
		if err != nil && !errors.As(err, &diffErr) {
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
		s.hub.Broadcast(Event{
			Type: "data_changed",
			Data: struct{}{},
		})
	})
	if started {
		return true
	}

	s.detailSyncMu.Lock()
	delete(s.detailSyncInFlight, key)
	delete(s.detailSyncPending, key)
	s.detailSyncMu.Unlock()
	return false
}

package streamapi

import (
	"context"
	"sync"
)

type WorkspaceDependencyShutdown struct {
	waitForConsumers   func(context.Context) error
	ShutdownWorkspace  func(context.Context) error
	ShutdownDependents func()
	dependentsOnce     sync.Once
}

func NewWorkspaceDependencyShutdown(
	waitForConsumers func(context.Context) error,
	shutdownWorkspace func(context.Context) error,
	shutdownDependents func(),
) *WorkspaceDependencyShutdown {
	return &WorkspaceDependencyShutdown{
		waitForConsumers:   waitForConsumers,
		ShutdownWorkspace:  shutdownWorkspace,
		ShutdownDependents: shutdownDependents,
	}
}

func (s *WorkspaceDependencyShutdown) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.waitForConsumers != nil {
		if err := s.waitForConsumers(ctx); err != nil {
			return err
		}
	}
	if s.ShutdownWorkspace != nil {
		if err := s.ShutdownWorkspace(ctx); err != nil {
			return err
		}
	}
	s.dependentsOnce.Do(func() {
		if s.ShutdownDependents != nil {
			s.ShutdownDependents()
		}
	})
	return nil
}

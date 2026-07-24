package server

import (
	"context"
	"sync"
)

type workspaceDependencyShutdown struct {
	shutdownWorkspace  func(context.Context) error
	shutdownDependents func()
	dependentsOnce     sync.Once
}

func newWorkspaceDependencyShutdown(
	shutdownWorkspace func(context.Context) error,
	shutdownDependents func(),
) *workspaceDependencyShutdown {
	return &workspaceDependencyShutdown{
		shutdownWorkspace:  shutdownWorkspace,
		shutdownDependents: shutdownDependents,
	}
}

func (s *workspaceDependencyShutdown) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.shutdownWorkspace != nil {
		if err := s.shutdownWorkspace(ctx); err != nil {
			return err
		}
	}
	s.dependentsOnce.Do(func() {
		if s.shutdownDependents != nil {
			s.shutdownDependents()
		}
	})
	return nil
}

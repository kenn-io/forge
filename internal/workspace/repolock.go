package workspace

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/sync/semaphore"
)

// Locker is a held repository lock returned by FileLockManager.Acquire.
// Unlock releases the lock and must be called exactly once.
type Locker interface {
	Unlock() error
}

// FileLockManager serializes worktree mutations on a single bare clone.
//
// git worktree add/remove/prune all update the bare clone's worktrees
// metadata, HEAD references, and the local branch namespace. Concurrent
// operations against the same clone — two creates against the same branch,
// or a retry cleanup overlapping a fresh setup — can wedge the worktree
// or leave dangling branches. FileLockManager funnels every such
// mutation through Acquire so the bare clone sees one mutation at a
// time, both within one middleman process and across processes that
// share the on-disk state.
type FileLockManager struct {
	mu     sync.Mutex
	states map[string]*repoLockState
}

// repoLockState holds the per-repo serialization primitives.
//
// The semaphore is the in-process mutex: it enforces exclusion between
// goroutines while still allowing Acquire(ctx, ...) to return promptly
// when ctx is canceled while waiting. The Flock enforces exclusion
// between this process and any other middleman process holding the same
// on-disk lock file. Both are required: gofrs/flock returns success
// immediately when the same *Flock instance is already locked (see
// flock_unix.go:48), so a shared Flock alone does not serialize
// concurrent goroutines, and flock(2) on Linux locks the open file
// description, so two fresh Flock instances in one process also fail to
// serialize.
type repoLockState struct {
	local *semaphore.Weighted
	file  *flock.Flock
}

// fileLockRetryDelay is the poll interval inside TryLockContext when the
// file lock is held by another process. Short enough to feel responsive,
// long enough to avoid burning CPU in the wait loop.
const fileLockRetryDelay = 25 * time.Millisecond

// NewFileLockManager constructs an empty FileLockManager.
func NewFileLockManager() *FileLockManager {
	return &FileLockManager{
		states: make(map[string]*repoLockState),
	}
}

func (m *FileLockManager) stateFor(lockPath string) *repoLockState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.states[lockPath]; ok {
		return s
	}
	s := &repoLockState{
		local: semaphore.NewWeighted(1),
		file:  flock.New(lockPath),
	}
	m.states[lockPath] = s
	return s
}

// Acquire blocks until both the in-process semaphore and the on-disk
// file lock are held, or ctx is done. On success the returned Locker
// holds the lock; Unlock must be called exactly once to release it.
//
// repoRoot is the bare clone directory; the lock file lives inside it
// so the lock travels with the clone and disappears with it.
func (m *FileLockManager) Acquire(
	ctx context.Context, repoRoot string,
) (Locker, error) {
	lockPath := filepath.Join(repoRoot, ".middleman-worktree.lock")
	state := m.stateFor(lockPath)

	if err := state.local.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("acquire worktree lock %q: %w", repoRoot, err)
	}

	locked, err := state.file.TryLockContext(ctx, fileLockRetryDelay)
	if err != nil {
		state.local.Release(1)
		return nil, fmt.Errorf("acquire worktree lock %q: %w", repoRoot, err)
	}
	if !locked {
		state.local.Release(1)
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("acquire worktree lock %q: %w", repoRoot, err)
		}
		return nil, fmt.Errorf("acquire worktree lock %q: lock not acquired", repoRoot)
	}
	return &fileLockHandle{state: state}, nil
}

type fileLockHandle struct {
	state    *repoLockState
	released bool
	mu       sync.Mutex
}

// Unlock releases both the file lock and the in-process semaphore.
// Calling Unlock more than once returns an error.
func (h *fileLockHandle) Unlock() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return errors.New("worktree lock already released")
	}
	h.released = true
	err := h.state.file.Unlock()
	h.state.local.Release(1)
	if err != nil {
		return fmt.Errorf("release worktree lock: %w", err)
	}
	return nil
}

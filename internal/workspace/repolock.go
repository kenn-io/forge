package workspace

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// Locker represents a file-based lock for a repository.
type Locker interface {
	// Unlock releases the lock, or errors if already released.
	Unlock() error
}

// FileLockManager manages per-repository file locks keyed by clone path.
type FileLockManager struct {
	mu    sync.Mutex
	locks map[string]*flock.Flock
}

// NewFileLockManager creates a new FileLockManager.
func NewFileLockManager() *FileLockManager {
	return &FileLockManager{
		locks: make(map[string]*flock.Flock),
	}
}

// Acquire acquires a lock for the given repository path, blocking until
// the lock is available or the context is cancelled/times out.
// The returned Locker must be unlocked via Unlock().
func (m *FileLockManager) Acquire(ctx context.Context, repoPath string) (Locker, error) {
	lockPath := filepath.Join(repoPath, ".middleman-worktree.lock")

	m.mu.Lock()
	fileLock := m.locks[lockPath]
	if fileLock == nil {
		fileLock = flock.New(lockPath)
		m.locks[lockPath] = fileLock
	}
	m.mu.Unlock()

	// Use TryLockContext which respects the context deadline and retries.
	// TryLockContext returns (false, nil) when the context deadline is exceeded.
	locked, err := fileLock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("lock error: %w", err)
	}
	if !locked {
		// Determine the error reason: either context cancelled/deadline or general failure.
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("lock acquisition failed: %w", err)
		}
		return nil, errors.New("lock acquisition failed: unable to acquire lock")
	}
	return &fileLockHandle{lock: fileLock}, nil
}

type fileLockHandle struct {
	lock *flock.Flock
}

func (h *fileLockHandle) Unlock() error {
	return h.lock.Unlock()
}

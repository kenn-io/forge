package runtimelock

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/gofrs/flock"
)

// Handle wraps a held runtime lock. It is returned by Acquire and
// surrendered by Release. The zero value is not usable.
//
// Handle is goroutine-safe for Release (a sync.Once protects the
// teardown path) but WriteMetadata is intended to be called once during
// startup before any other goroutine has a reference; it does not lock
// against itself.
type Handle struct {
	dataDir string
	lock    *flock.Flock

	releaseOnce sync.Once
	releaseErr  error
}

// Acquire takes the runtime lock under dataDir. The caller must have
// already ensured dataDir exists (Acquire does not create it).
//
// On a collision with another running daemon, Acquire returns a
// *CollisionError describing the holder; use errors.As to detect it.
// Stale metadata left behind by an unclean shutdown is removed under
// the held lock before Acquire returns; a slog.Warn announces the
// cleanup so operators see it in the logs.
func Acquire(dataDir string) (*Handle, error) {
	lock := flock.New(LockPath(dataDir))

	ok, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquire runtime lock: %w", err)
	}
	if !ok {
		cerr := &CollisionError{
			DataDir:  dataDir,
			LockPath: LockPath(dataDir),
		}
		meta, readErr := readMetadata(dataDir)
		switch {
		case readErr == nil:
			cerr.Metadata = &meta
		case errors.Is(readErr, errMetadataMissing):
			cerr.MetadataUnavailable = ReasonMetadataMissing
		default:
			cerr.MetadataUnavailable = ReasonMetadataCorrupt
		}
		return nil, cerr
	}

	// Clean up stale metadata from a previous unclean shutdown under
	// the held lock so a partially-started daemon does not see another's
	// PID.
	if _, statErr := os.Stat(MetadataPath(dataDir)); statErr == nil {
		slog.Warn("previous middleman run terminated uncleanly; removing stale metadata",
			"data_dir", dataDir,
			"metadata_path", MetadataPath(dataDir),
		)
		if err := removeMetadata(dataDir); err != nil {
			slog.Warn("remove stale runtime metadata", "err", err)
		}
	}

	return &Handle{dataDir: dataDir, lock: lock}, nil
}

// WriteMetadata persists meta to the metadata file under the held
// lock. Call this once the listener has bound so the recorded port
// matches the kernel-assigned value.
func (h *Handle) WriteMetadata(meta Metadata) error {
	return writeMetadata(h.dataDir, meta)
}

// Release removes the metadata file (best effort) and unlocks the
// runtime lock. The lock file itself is left on disk by design
// (removing it would race against a concurrent Acquire on the same
// path). Release is safe to call from a defer and is idempotent.
//
// Any best-effort failure is logged via slog.Warn so a deferred caller
// that ignores the return value still surfaces the problem.
func (h *Handle) Release() error {
	h.releaseOnce.Do(func() {
		if err := removeMetadata(h.dataDir); err != nil {
			slog.Warn("remove runtime metadata on release",
				"data_dir", h.dataDir, "err", err)
			h.releaseErr = err
		}
		if err := h.lock.Unlock(); err != nil {
			slog.Warn("unlock runtime lock",
				"data_dir", h.dataDir, "err", err)
			if h.releaseErr == nil {
				h.releaseErr = err
			}
		}
	})
	return h.releaseErr
}

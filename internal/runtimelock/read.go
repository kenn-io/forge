package runtimelock

import (
	"errors"
	"fmt"

	"github.com/gofrs/flock"
)

// Status describes the lock state of a data_dir at a moment in time.
// It is the return shape of Read.
type Status struct {
	// DataDir is the data_dir argument passed to Read.
	DataDir string

	// LockPath is the absolute path of the lock file under DataDir.
	LockPath string

	// Running is true when another process holds the runtime lock at
	// the moment of the probe.
	Running bool

	// Metadata is the decoded run metadata when Running is true and the
	// metadata file is present and parseable. Nil otherwise.
	Metadata *Metadata

	// MetadataUnavailable is non-empty when Running is true but the
	// metadata file could not be read. ReasonMetadataMissing is the
	// expected value during the startup window between Acquire and
	// WriteMetadata.
	MetadataUnavailable MetadataUnavailableReason
}

// Read probes the runtime lock under dataDir without holding it. The
// implementation TryLocks the file, releases immediately if it
// acquires, and reports either "not running" or "running with metadata
// X / unavailable for reason Y" otherwise.
//
// Errors are returned only for operational failures (e.g., parent
// directory missing, permission denied opening the lock file path). A
// busy lock is reported via Status.Running and is not an error.
func Read(dataDir string) (Status, error) {
	st := Status{
		DataDir:  dataDir,
		LockPath: LockPath(dataDir),
	}

	lock := flock.New(LockPath(dataDir))
	ok, err := lock.TryLock()
	if err != nil {
		return Status{}, fmt.Errorf("probe runtime lock: %w", err)
	}
	if ok {
		// We acquired it ourselves; release and report not-running.
		if unlockErr := lock.Unlock(); unlockErr != nil {
			return Status{}, fmt.Errorf("release runtime lock after probe: %w", unlockErr)
		}
		return st, nil
	}

	st.Running = true
	meta, readErr := readMetadata(dataDir)
	switch {
	case readErr == nil:
		st.Metadata = &meta
	case errors.Is(readErr, errMetadataMissing):
		st.MetadataUnavailable = ReasonMetadataMissing
	default:
		st.MetadataUnavailable = ReasonMetadataCorrupt
	}
	return st, nil
}

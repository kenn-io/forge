package runtimelock

import "fmt"

// MetadataUnavailableReason explains why the runtime metadata file
// could not be read when reporting a CollisionError or Status. The
// banner and `middleman status` use it for the metadata-unavailable
// branch.
type MetadataUnavailableReason string

const (
	// ReasonMetadataMissing indicates the metadata file does not exist.
	// Most commonly seen when the daemon is mid-startup between Acquire
	// and WriteMetadata.
	ReasonMetadataMissing MetadataUnavailableReason = "missing"

	// ReasonMetadataCorrupt indicates the metadata file exists but
	// could not be decoded as JSON.
	ReasonMetadataCorrupt MetadataUnavailableReason = "corrupt"
)

// String returns a human-readable form for inclusion in error messages
// and banners.
func (r MetadataUnavailableReason) String() string {
	switch r {
	case ReasonMetadataMissing:
		return "missing (daemon may be early in startup)"
	case ReasonMetadataCorrupt:
		return "corrupt (file present but could not be parsed)"
	default:
		return string(r)
	}
}

// CollisionError is returned by Acquire when another process already
// holds the lock. It carries enough context for the caller to render a
// banner without re-reading any files.
//
// Metadata is nil when the running daemon has not yet written its
// metadata file (or when the file is corrupt). In that case
// MetadataUnavailable explains why.
type CollisionError struct {
	DataDir             string
	LockPath            string
	Metadata            *Metadata
	MetadataUnavailable MetadataUnavailableReason
}

func (e *CollisionError) Error() string {
	return fmt.Sprintf("another middleman is already running on %s", e.DataDir)
}

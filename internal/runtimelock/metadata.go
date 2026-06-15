package runtimelock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Metadata is the on-disk shape of middleman.run.json. JSON tags are
// the wire format; do not rename keys without a migration story.
//
// Decoders accept unknown keys so future fields don't break older
// readers; the default encoding/json behavior already does this.
type Metadata struct {
	PID        int    `json:"pid"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	ListenAddr string `json:"listen_addr"`
	StartedAt  string `json:"started_at"`
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	// TokenPath locates the API auth token file thin clients read
	// to authenticate (see EnsureAuthToken). The daemon always mints
	// the token at startup, so the path is always readable by the
	// daemon's user; RequireAuth says whether requests must present
	// it.
	TokenPath string `json:"token_path,omitempty"`

	// BasePath is the URL prefix API routes are mounted under
	// ("/" when the daemon serves at the root). Thin clients join it
	// before /api/... paths.
	BasePath string `json:"base_path,omitempty"`
	// RequireAuth reports whether the daemon enforces bearer-token
	// auth on API routes, so clients know to send the token.
	RequireAuth bool `json:"require_auth,omitempty"`
}

// errMetadataMissing is the typed reason returned by readMetadata when
// the metadata file is absent. Distinguished from a decode failure so
// callers can render "metadata unavailable: missing" vs "metadata
// unavailable: corrupt".
var errMetadataMissing = errors.New("runtime metadata is missing")

// writeMetadata writes meta atomically to MetadataPath(dataDir).
//
// Pattern (mirrors internal/ptyowner/paths.go:writeState):
//  1. Marshal meta to JSON.
//  2. Open <dataDir>/.middleman.run.json.tmp with O_CREATE|O_WRONLY|O_TRUNC mode 0o600.
//     Truncating, rather than O_EXCL, ensures a leftover temp file from a
//     previous crash is overwritten rather than blocking us.
//  3. Write, fsync, close.
//  4. os.Rename onto MetadataPath. On Go 1.26 + Windows this maps to
//     MoveFileEx with MOVEFILE_REPLACE_EXISTING.
//
// Any failure removes the temp file before returning so we never leak.
func writeMetadata(dataDir string, meta Metadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime metadata: %w", err)
	}

	tmpPath := metadataTmpPath(dataDir)
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open runtime metadata temp file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write runtime metadata temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync runtime metadata temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close runtime metadata temp file: %w", err)
	}
	if err := os.Rename(tmpPath, MetadataPath(dataDir)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename runtime metadata: %w", err)
	}
	return nil
}

// readMetadata reads and decodes the metadata file under dataDir.
// Returns errMetadataMissing when the file does not exist, and a
// wrapped JSON error when present-but-undecodable.
func readMetadata(dataDir string) (Metadata, error) {
	data, err := os.ReadFile(MetadataPath(dataDir))
	if err != nil {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) && errors.Is(pathErr.Err, fs.ErrNotExist) {
			return Metadata{}, errMetadataMissing
		}
		return Metadata{}, fmt.Errorf("read runtime metadata: %w", err)
	}
	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return Metadata{}, fmt.Errorf("decode runtime metadata: %w", err)
	}
	return meta, nil
}

// removeMetadata removes the metadata file. Missing-file is not an
// error; the caller treats it as "already clean".
func removeMetadata(dataDir string) error {
	if err := os.Remove(MetadataPath(dataDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove runtime metadata: %w", err)
	}
	return nil
}

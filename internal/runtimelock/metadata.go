package runtimelock

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"go.kenn.io/kit/atomicfile"
)

// Metadata is the on-disk shape of kenn-forge.run.json. JSON tags are
// the wire format; do not rename keys without a migration story.
//
// Decoders accept unknown keys so future fields don't break older
// readers; the default encoding/json behavior already does this.
type Metadata struct {
	PID           int    `json:"pid"`
	NodeID        string `json:"node_id,omitempty"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	ListenAddr    string `json:"listen_addr"`
	MCPListenAddr string `json:"mcp_listen_addr,omitempty"`
	StartedAt     string `json:"started_at"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	ConfigPath    string `json:"config_path,omitempty"`
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
	RequireAuth bool `json:"require_auth,omitzero"`
}

// errMetadataMissing is the typed reason returned by readMetadata when
// the metadata file is absent. Distinguished from a decode failure so
// callers can render "metadata unavailable: missing" vs "metadata
// unavailable: corrupt".
var errMetadataMissing = errors.New("runtime metadata is missing")

// writeMetadata writes meta atomically to MetadataPath(dataDir) with
// mode 0600. The runtime flock held by Handle serializes writers.
func writeMetadata(dataDir string, meta Metadata) error {
	data, err := json.Marshal(meta, jsontext.WithIndent("  "))
	if err != nil {
		return fmt.Errorf("marshal runtime metadata: %w", err)
	}
	// ErrPublished means the metadata is already in place and only a
	// later directory fsync failed.
	err = atomicfile.WriteFile(MetadataPath(dataDir), data)
	if err != nil && !errors.Is(err, atomicfile.ErrPublished) {
		return fmt.Errorf("write runtime metadata: %w", err)
	}
	return nil
}

// readMetadata reads and decodes the metadata file under dataDir.
// Returns errMetadataMissing when the file does not exist, and a
// wrapped JSON error when present-but-undecodable.
func readMetadata(dataDir string) (Metadata, error) {
	data, err := os.ReadFile(MetadataPath(dataDir))
	if err != nil {
		if pathErr, ok := errors.AsType[*fs.PathError](err); ok && errors.Is(pathErr.Err, fs.ErrNotExist) {
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

// Package daemonruntime owns Kenn Forge's standard kit daemon discovery record.
package daemonruntime

import (
	"crypto/sha256"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/kit/daemon"
)

const (
	Service       = "kenn-forge"
	ProofPingPath = "/api/ping/proof"

	metadataHost          = "host"
	metadataPort          = "port"
	metadataReadOnly      = "read_only"
	metadataRequireAuth   = "require_auth"
	metadataDataDir       = "data_dir"
	metadataAuthTokenPath = "auth_token_path"
	metadataBasePath      = "base_path"
)

// IdentityOptions contains the startup-bound values shared by both runtime
// discovery surfaces.
type IdentityOptions struct {
	Version     string
	Commit      string
	DataDir     string
	BasePath    string
	RequireAuth bool
}

// Identity is the single startup identity serialized to the authoritative
// data-directory status and the generic kit discovery store.
type Identity struct {
	Record       daemon.RuntimeRecord
	LockMetadata runtimelock.Metadata
}

// Store returns the predictable config-home discovery store.
func Store() (daemon.RuntimeStore, error) {
	runtimeDir, err := filepath.Abs(config.DefaultDataDir())
	if err != nil {
		return daemon.RuntimeStore{}, fmt.Errorf("resolve runtime directory: %w", err)
	}
	return daemon.RuntimeStore{Dir: runtimeDir}, nil
}

// StartLockStore returns the kit lock identity for one resolved data directory.
// The digest avoids exposing filesystem paths in config-home filenames.
func StartLockStore(discoveryStore daemon.RuntimeStore, dataDir string) daemon.RuntimeStore {
	digest := sha256.Sum256([]byte(dataDir))
	return daemon.RuntimeStore{
		Dir:    discoveryStore.Dir,
		Prefix: fmt.Sprintf("%s-start-%x", Service, digest[:16]),
	}
}

// NewIdentity derives both discovery representations from one bound TCP
// listener address and process snapshot.
func NewIdentity(address net.Addr, opts IdentityOptions) (Identity, error) {
	tcpAddress, ok := address.(*net.TCPAddr)
	if !ok {
		return Identity{}, fmt.Errorf("listener returned non-TCP address %T", address)
	}
	host := tcpAddress.IP.String()
	port := strconv.Itoa(tcpAddress.Port)
	basePath := canonicalBasePath(opts.BasePath)
	record := daemon.NewRuntimeRecord(
		Service,
		opts.Version,
		daemon.Endpoint{Network: daemon.NetworkTCP, Address: address.String()},
	)
	record.Metadata = map[string]string{
		metadataHost:        host,
		metadataPort:        port,
		metadataReadOnly:    strconv.FormatBool(false),
		metadataRequireAuth: strconv.FormatBool(opts.RequireAuth),
		metadataDataDir:     opts.DataDir,
		metadataBasePath:    basePath,
	}
	tokenPath := runtimelock.AuthTokenPath(opts.DataDir)
	if opts.RequireAuth {
		record.Metadata[metadataAuthTokenPath] = tokenPath
	}
	return Identity{
		Record: record,
		LockMetadata: runtimelock.Metadata{
			PID:         record.PID,
			Host:        host,
			Port:        tcpAddress.Port,
			ListenAddr:  record.Address,
			StartedAt:   record.StartedAt.UTC().Format(time.RFC3339),
			Version:     record.Version,
			Commit:      opts.Commit,
			TokenPath:   tokenPath,
			BasePath:    basePath,
			RequireAuth: opts.RequireAuth,
		},
	}, nil
}

// URL returns the browser location advertised by a verified runtime record.
func URL(record daemon.RuntimeRecord) (string, error) {
	basePath := record.Metadata[metadataBasePath]
	if basePath == "" || !strings.HasPrefix(basePath, "/") ||
		strings.HasPrefix(basePath, "//") || strings.ContainsAny(basePath, "?#") ||
		canonicalBasePath(basePath) != basePath {
		return "", fmt.Errorf("daemon runtime record has invalid base_path %q", basePath)
	}
	baseURL := record.Endpoint().BaseURL()
	if basePath == "/" {
		return baseURL, nil
	}
	return baseURL + basePath, nil
}

// Publish writes identity's standard discovery record and returns its exact
// path for cleanup when the server exits.
func Publish(record daemon.RuntimeRecord) (string, error) {
	store, err := Store()
	if err != nil {
		return "", err
	}
	if _, err := store.CleanupDead(); err != nil {
		return "", fmt.Errorf("clean stale daemon runtime records: %w", err)
	}
	path, err := store.Write(record)
	return path, err
}

func canonicalBasePath(basePath string) string {
	if basePath == "" {
		return "/"
	}
	if trimmed := strings.TrimSuffix(basePath, "/"); trimmed != "" {
		return trimmed
	}
	return "/"
}

// Compatible reports whether a discovery record can represent the expected
// local instance before the caller performs its authenticated readiness probe.
func Compatible(record daemon.RuntimeRecord, dataDir string) bool {
	if record.Service != Service || record.Network != daemon.NetworkTCP ||
		record.Metadata[metadataDataDir] != dataDir ||
		!daemon.ProcessAlive(record.PID) {
		return false
	}
	if err := daemon.RequireLoopback(record.Address); err != nil {
		return false
	}
	host, port, err := net.SplitHostPort(record.Address)
	if err != nil || record.Metadata[metadataHost] != host ||
		record.Metadata[metadataPort] != port {
		return false
	}
	readOnly, err := strconv.ParseBool(record.Metadata[metadataReadOnly])
	if err != nil || readOnly {
		return false
	}
	requireAuth, err := strconv.ParseBool(record.Metadata[metadataRequireAuth])
	if err != nil {
		return false
	}
	if _, err := URL(record); err != nil {
		return false
	}
	if requireAuth {
		return record.Metadata[metadataAuthTokenPath] == runtimelock.AuthTokenPath(dataDir)
	}
	return record.Metadata[metadataAuthTokenPath] == ""
}

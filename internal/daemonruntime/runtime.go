// Package daemonruntime owns Middleman's standard kit daemon discovery record.
package daemonruntime

import (
	"crypto/sha256"
	"fmt"
	"net"
	"path/filepath"
	"strconv"

	"go.kenn.io/kit/daemon"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/runtimelock"
)

const (
	Service       = "middleman"
	ProofPingPath = "/api/ping/proof"

	metadataHost          = "host"
	metadataPort          = "port"
	metadataReadOnly      = "read_only"
	metadataRequireAuth   = "require_auth"
	metadataDataDir       = "data_dir"
	metadataAuthTokenPath = "auth_token_path"
)

// PublishOptions contains the startup-bound identity written for discovery.
type PublishOptions struct {
	Address     string
	Version     string
	DataDir     string
	RequireAuth bool
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

// Publish writes the standard discovery record after the listener is bound and
// returns the exact identity that credential-free endpoint proof must bind.
func Publish(opts PublishOptions) (daemon.RuntimeRecord, string, error) {
	host, port, err := net.SplitHostPort(opts.Address)
	if err != nil {
		return daemon.RuntimeRecord{}, "", fmt.Errorf("listener address is not TCP: %w", err)
	}
	store, err := Store()
	if err != nil {
		return daemon.RuntimeRecord{}, "", err
	}
	if _, err := store.CleanupDead(); err != nil {
		return daemon.RuntimeRecord{}, "", fmt.Errorf("clean stale daemon runtime records: %w", err)
	}
	record := daemon.NewRuntimeRecord(
		Service,
		opts.Version,
		daemon.Endpoint{Network: daemon.NetworkTCP, Address: opts.Address},
	)
	record.Metadata = map[string]string{
		metadataHost:        host,
		metadataPort:        port,
		metadataReadOnly:    strconv.FormatBool(false),
		metadataRequireAuth: strconv.FormatBool(opts.RequireAuth),
		metadataDataDir:     opts.DataDir,
	}
	if opts.RequireAuth {
		record.Metadata[metadataAuthTokenPath] = runtimelock.AuthTokenPath(opts.DataDir)
	}
	path, err := store.Write(record)
	return record, path, err
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
	if requireAuth {
		return record.Metadata[metadataAuthTokenPath] == runtimelock.AuthTokenPath(dataDir)
	}
	return record.Metadata[metadataAuthTokenPath] == ""
}

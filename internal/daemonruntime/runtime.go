// Package daemonruntime owns Kenn Forge's standard kit daemon discovery record.
package daemonruntime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/pathidentity"
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
	metadataConfigPath    = "config_path"
	metadataAuthTokenPath = "auth_token_path"
	metadataBasePath      = "base_path"
)

// IdentityOptions contains the startup-bound values shared by both runtime
// discovery surfaces.
type IdentityOptions struct {
	Version     string
	Commit      string
	DataDir     string
	ConfigPath  string
	BasePath    string
	RequireAuth bool
}

// Identity is the single startup identity serialized to the authoritative
// data-directory status and the generic kit discovery store.
type Identity struct {
	Record       daemon.RuntimeRecord
	LockMetadata runtimelock.Metadata
}

// ConfigRuntime is an unverified discovery candidate relevant to one canonical
// config path. Callers must authenticate Record before signaling it.
type ConfigRuntime struct {
	Record     daemon.RuntimeRecord
	DataDir    string
	ConfigPath string
}

// Store returns the predictable config-home discovery store.
func Store() (daemon.RuntimeStore, error) {
	runtimeDir, err := filepath.Abs(config.DefaultDataDir())
	if err != nil {
		return daemon.RuntimeStore{}, fmt.Errorf("resolve runtime directory: %w", err)
	}
	return daemon.RuntimeStore{Dir: runtimeDir}, nil
}

// StartLockStore returns the kit lock identity for one canonical data directory.
// The digest avoids exposing filesystem paths in config-home filenames.
func StartLockStore(
	discoveryStore daemon.RuntimeStore,
	dataDir string,
) (daemon.RuntimeStore, error) {
	canonicalDataDir, err := config.CanonicalDataDir(dataDir)
	if err != nil {
		return daemon.RuntimeStore{}, err
	}
	return scopedLockStore(
		discoveryStore, Service+"-start", canonicalDataDir,
	), nil
}

// LifecycleLockStore returns the lock identity serializing start, stop, and
// restart for one canonical data directory.
func LifecycleLockStore(
	discoveryStore daemon.RuntimeStore,
	dataDir string,
) (daemon.RuntimeStore, error) {
	canonicalDataDir, err := config.CanonicalDataDir(dataDir)
	if err != nil {
		return daemon.RuntimeStore{}, err
	}
	return scopedLockStore(
		discoveryStore, Service+"-lifecycle", canonicalDataDir,
	), nil
}

// ConfigLifecycleLockStore returns the lock identity serializing lifecycle
// mutations that originate from one canonical config path.
func ConfigLifecycleLockStore(
	discoveryStore daemon.RuntimeStore,
	configPath string,
) (daemon.RuntimeStore, error) {
	canonicalPath, err := CanonicalConfigPath(configPath)
	if err != nil {
		return daemon.RuntimeStore{}, err
	}
	return scopedLockStore(
		discoveryStore, Service+"-config-lifecycle", canonicalPath,
	), nil
}

func scopedLockStore(
	discoveryStore daemon.RuntimeStore,
	prefix, identity string,
) daemon.RuntimeStore {
	digest := sha256.Sum256([]byte(identity))
	return daemon.RuntimeStore{
		Dir:    discoveryStore.Dir,
		Prefix: fmt.Sprintf("%s-%x", prefix, digest[:16]),
	}
}

// LifecycleLock is a held cross-process daemon lifecycle lock.
type LifecycleLock struct {
	file *flock.Flock
}

// AcquireLifecycleLock waits until it exclusively owns the per-data-directory
// lifecycle lock or ctx is canceled.
func AcquireLifecycleLock(
	ctx context.Context,
	store daemon.RuntimeStore,
	dataDir string,
) (*LifecycleLock, error) {
	lockStore, err := LifecycleLockStore(store, dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve daemon lifecycle lock: %w", err)
	}
	return acquireLifecycleLock(
		ctx, lockStore, "daemon lifecycle",
	)
}

// AcquireConfigLifecycleLock waits until it exclusively owns the lifecycle
// lock for a canonical config identity or ctx is canceled.
func AcquireConfigLifecycleLock(
	ctx context.Context,
	store daemon.RuntimeStore,
	configPath string,
) (*LifecycleLock, error) {
	lockStore, err := ConfigLifecycleLockStore(store, configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve daemon config lifecycle lock: %w", err)
	}
	return acquireLifecycleLock(ctx, lockStore, "daemon config lifecycle")
}

func acquireLifecycleLock(
	ctx context.Context,
	store daemon.RuntimeStore,
	description string,
) (*LifecycleLock, error) {
	path, err := store.LockPath()
	if err != nil {
		return nil, fmt.Errorf("resolve %s lock: %w", description, err)
	}
	file := flock.New(path)
	locked, err := file.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("acquire %s lock: %w", description, err)
	}
	if !locked {
		return nil, fmt.Errorf("acquire %s lock: lock not acquired", description)
	}
	return &LifecycleLock{file: file}, nil
}

// Release unlocks the lifecycle file without unlinking its stable path.
func (l *LifecycleLock) Release() error {
	return l.file.Unlock()
}

// NewIdentity derives both discovery representations from one bound TCP
// listener address and process snapshot.
func NewIdentity(address net.Addr, opts IdentityOptions) (Identity, error) {
	tcpAddress, ok := address.(*net.TCPAddr)
	if !ok {
		return Identity{}, fmt.Errorf("listener returned non-TCP address %T", address)
	}
	configPath, err := CanonicalConfigPath(opts.ConfigPath)
	if err != nil {
		return Identity{}, err
	}
	dataDir, err := config.CanonicalDataDir(opts.DataDir)
	if err != nil {
		return Identity{}, err
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
		metadataDataDir:     dataDir,
		metadataConfigPath:  configPath,
		metadataBasePath:    basePath,
	}
	tokenPath := runtimelock.AuthTokenPath(dataDir)
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
			ConfigPath:  configPath,
			TokenPath:   tokenPath,
			BasePath:    basePath,
			RequireAuth: opts.RequireAuth,
		},
	}, nil
}

// ConfigRuntimes returns live records relevant to resolving one canonical config.
// Config-identified records take precedence over legacy records. Legacy
// records for another data directory are returned only when no closer match
// exists so the caller can authenticate or safely discard them.
// The returned records remain untrusted until authenticated by proof.
func ConfigRuntimes(
	store daemon.RuntimeStore,
	configPath, currentDataDir string,
) ([]ConfigRuntime, error) {
	return configRuntimes(store, configPath, currentDataDir, true)
}

// IdentifiedConfigRuntimes returns live records that explicitly name one
// canonical config path. Legacy records without config identity are ignored.
func IdentifiedConfigRuntimes(
	store daemon.RuntimeStore,
	configPath string,
) ([]ConfigRuntime, error) {
	return configRuntimes(store, configPath, "", false)
}

func configRuntimes(
	store daemon.RuntimeStore,
	configPath, currentDataDir string,
	includeLegacy bool,
) ([]ConfigRuntime, error) {
	canonicalPath, err := CanonicalConfigPath(configPath)
	if err != nil {
		return nil, err
	}
	records, err := store.List()
	if err != nil {
		return nil, err
	}
	var identified []ConfigRuntime
	var currentLegacy []ConfigRuntime
	var unmatchedLegacy []ConfigRuntime
	for _, record := range records {
		if record.Service != Service || !daemon.ProcessAlive(record.PID) {
			continue
		}
		recordConfigPath := record.Metadata[metadataConfigPath]
		if recordConfigPath != "" {
			rawConfigPath := recordConfigPath
			recordConfigPath, err = CanonicalConfigPath(rawConfigPath)
			if err != nil {
				if filepath.Clean(rawConfigPath) == canonicalPath {
					return nil, fmt.Errorf(
						"canonicalize daemon runtime config path for pid %d: %w",
						record.PID, err,
					)
				}
				continue
			}
			if recordConfigPath != canonicalPath {
				continue
			}
		}
		if recordConfigPath == "" && !includeLegacy {
			continue
		}
		dataDir, err := runtimeRecordDataDir(record)
		if err != nil {
			return nil, err
		}
		candidate := ConfigRuntime{
			Record: record, DataDir: dataDir, ConfigPath: recordConfigPath,
		}
		if recordConfigPath != "" {
			identified = append(identified, candidate)
			continue
		}
		if dataDir == currentDataDir {
			currentLegacy = append(currentLegacy, candidate)
		} else {
			unmatchedLegacy = append(unmatchedLegacy, candidate)
		}
	}
	if len(identified) > 0 {
		return identified, nil
	}
	if len(currentLegacy) > 0 {
		return currentLegacy, nil
	}
	if len(unmatchedLegacy) > 0 {
		return unmatchedLegacy, nil
	}
	return nil, nil
}

func runtimeRecordDataDir(record daemon.RuntimeRecord) (string, error) {
	dataDir := record.Metadata[metadataDataDir]
	if dataDir == "" || !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) != dataDir {
		return "", fmt.Errorf(
			"daemon runtime record for pid %d has invalid data_dir %q",
			record.PID, dataDir,
		)
	}
	canonicalDataDir, err := config.CanonicalDataDir(dataDir)
	if err != nil {
		return "", fmt.Errorf(
			"canonicalize daemon runtime data_dir for pid %d: %w",
			record.PID, err,
		)
	}
	return canonicalDataDir, nil
}

// CanonicalConfigPath returns one stable config identity across absolute,
// relative, and symlinked aliases. Missing suffixes are retained after the
// nearest existing ancestor is resolved.
func CanonicalConfigPath(configPath string) (string, error) {
	if configPath == "" {
		return "", errors.New("daemon config path is empty")
	}
	absolutePath, err := filepath.Abs(configPath)
	if err != nil {
		return "", fmt.Errorf("resolve daemon config path: %w", err)
	}
	currentPath := filepath.Clean(absolutePath)
	missingSuffix := make([]string, 0, 1)
	for {
		resolvedPath, err := filepath.EvalSymlinks(currentPath)
		if err == nil {
			resolvedPath, err = pathidentity.CanonicalExisting(resolvedPath)
			if err != nil {
				return "", fmt.Errorf(
					"resolve daemon config path casing: %w", err,
				)
			}
			for index := len(missingSuffix) - 1; index >= 0; index-- {
				resolvedPath = filepath.Join(resolvedPath, missingSuffix[index])
			}
			return filepath.Clean(resolvedPath), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve daemon config path symlinks: %w", err)
		}
		parent := filepath.Dir(currentPath)
		if parent == currentPath {
			return "", fmt.Errorf("resolve daemon config path symlinks: %w", err)
		}
		missingSuffix = append(missingSuffix, filepath.Base(currentPath))
		currentPath = parent
	}
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
		!daemon.ProcessAlive(record.PID) {
		return false
	}
	recordDataDir, err := runtimeRecordDataDir(record)
	if err != nil {
		return false
	}
	canonicalDataDir, err := config.CanonicalDataDir(dataDir)
	if err != nil {
		return false
	}
	if recordDataDir != canonicalDataDir {
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
		return record.Metadata[metadataAuthTokenPath] == runtimelock.AuthTokenPath(
			record.Metadata[metadataDataDir],
		)
	}
	return record.Metadata[metadataAuthTokenPath] == ""
}

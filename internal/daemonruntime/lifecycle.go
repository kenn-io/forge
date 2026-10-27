package daemonruntime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.kenn.io/forge/internal/apiclient/health"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/kit/daemon"
)

const probeTimeout = 750 * time.Millisecond

type discovery struct {
	store   daemon.RuntimeStore
	dataDir string
	version string
}

// VersionMismatchError reports a proven daemon that cannot be reused by the
// current binary. Lifecycle operations may still stop the authenticated
// process before starting the expected version.
type VersionMismatchError struct {
	Running  string
	Expected string
}

func (e *VersionMismatchError) Error() string {
	return fmt.Sprintf(
		"running kenn-forge version %q is incompatible with %q",
		e.Running, e.Expected,
	)
}

// NewManager returns the shared lifecycle manager with Kenn Forge's discovery
// identity and per-canonical-data-directory start lock.
func NewManager(
	store daemon.RuntimeStore,
	dataDir, expectedVersion string,
	start daemon.StartFunc,
) (daemon.Manager, error) {
	canonicalDataDir, err := config.CanonicalDataDir(dataDir)
	if err != nil {
		return daemon.Manager{}, fmt.Errorf("canonicalize daemon data directory: %w", err)
	}
	startStore := scopedLockStore(store, Service+"-start", canonicalDataDir)
	discovery := discovery{
		store: store, dataDir: canonicalDataDir, version: expectedVersion,
	}
	return daemon.Manager{
		Store:    startStore,
		FindFunc: discovery.find,
		Start: func(ctx context.Context) error {
			if start == nil {
				return errors.New("kenn-forge daemon is not running")
			}
			if err := start(ctx); err != nil {
				return fmt.Errorf("start kenn-forge daemon: %w", err)
			}
			return nil
		},
	}, nil
}

func (d discovery) find(
	ctx context.Context,
) (daemon.RuntimeRecord, daemon.PingInfo, bool, error) {
	return d.findVerified(ctx, true)
}

// FindVerified returns the daemon record only after its endpoint proves
// possession of the token for dataDir. It deliberately leaves version policy
// to the caller so lifecycle commands can stop an older daemon safely.
func FindVerified(
	ctx context.Context,
	store daemon.RuntimeStore,
	dataDir string,
) (daemon.RuntimeRecord, daemon.PingInfo, bool, error) {
	return (discovery{store: store, dataDir: dataDir}).findVerified(ctx, false)
}

// FindVerifiedRecord authenticates one exact discovery candidate rather than
// accepting any compatible daemon published for the same data directory.
func FindVerifiedRecord(
	ctx context.Context,
	record daemon.RuntimeRecord,
	dataDir string,
) (daemon.PingInfo, bool, error) {
	proof, found, err := proofFor(dataDir)
	if err != nil || !found {
		return daemon.PingInfo{}, false, err
	}
	return (discovery{dataDir: dataDir}).probe(ctx, proof, record)
}

func proofFor(dataDir string) (*daemon.Proof, bool, error) {
	token, err := runtimelock.ReadAuthToken(dataDir)
	if err != nil || token == "" {
		return nil, false, err
	}
	proof, err := daemon.NewProof([]byte(token))
	if err != nil {
		return nil, false, fmt.Errorf("initialize daemon proof: %w", err)
	}
	return proof, true, nil
}

// Stop proves the endpoint, requests shutdown, and waits for process exit.
// It refuses a record whose PID is not proven to be in this PID namespace,
// because request receives the PID as this namespace sees it.
func Stop(ctx context.Context, record daemon.RuntimeRecord, dataDir string, request func(int) error, timeout time.Duration) error {
	if daemon.RuntimeProcessScope(record) != daemon.ProcessScopeLocal {
		return fmt.Errorf(
			"daemon pid %d is not proven to run in this PID namespace; stop it from the namespace that started it; no process was signaled",
			record.PID,
		)
	}
	proof, _, err := proofFor(dataDir)
	if err != nil {
		return err
	}
	return daemon.Stop(ctx, record, daemon.StopOptions{
		Proof: proof,
		Probe: daemon.ProbeOptions{Path: ProofPingPath, Timeout: probeTimeout},
		Request: func(_ context.Context, rec daemon.RuntimeRecord) error {
			return request(rec.PID)
		},
		Exited: func(_ context.Context, rec daemon.RuntimeRecord) (bool, error) {
			return !daemon.RuntimeProcessAlive(rec), nil
		},
		Timeout: timeout,
	})
}

// IsVerifiedReady reports whether record serves the ready health endpoint and
// still proves the same token-bound runtime identity afterward.
func IsVerifiedReady(
	ctx context.Context,
	record daemon.RuntimeRecord,
	dataDir string,
) (bool, error) {
	if !Compatible(record, dataDir) {
		return false, nil
	}
	ep := record.Endpoint()
	client := ep.HTTPClient(daemon.HTTPClientOptions{
		Timeout:           probeTimeout,
		DisableKeepAlives: true,
	})
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	req, err := health.NewGetHealthzRequest(ctx, ep.BaseURL())
	if err != nil {
		return false, fmt.Errorf("build daemon readiness request: %w", err)
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	if err := response.Body.Close(); err != nil {
		return false, fmt.Errorf("close daemon readiness response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return false, nil
	}
	_, found, err := FindVerifiedRecord(ctx, record, dataDir)
	return found, err
}

func (d discovery) findVerified(
	ctx context.Context,
	requireVersion bool,
) (daemon.RuntimeRecord, daemon.PingInfo, bool, error) {
	proof, found, err := proofFor(d.dataDir)
	if err != nil || !found {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, err
	}
	records, err := d.store.List()
	if err != nil {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, err
	}
	for _, record := range records {
		ping, compatible, err := d.probe(ctx, proof, record)
		if err != nil {
			return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, err
		}
		if compatible {
			if requireVersion && record.Version != d.version {
				return daemon.RuntimeRecord{}, daemon.PingInfo{}, false,
					&VersionMismatchError{
						Running: record.Version, Expected: d.version,
					}
			}
			return record, ping, true, nil
		}
	}
	return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, nil
}

func (d discovery) probe(
	ctx context.Context,
	proof *daemon.Proof,
	record daemon.RuntimeRecord,
) (daemon.PingInfo, bool, error) {
	if !Compatible(record, d.dataDir) {
		return daemon.PingInfo{}, false, nil
	}
	ping, err := proof.Probe(ctx, record, daemon.ProbeOptions{
		Path: ProofPingPath, Timeout: probeTimeout,
	})
	if err != nil {
		if ctx.Err() != nil {
			return daemon.PingInfo{}, false, ctx.Err()
		}
		return daemon.PingInfo{}, false, nil
	}
	return ping, true, nil
}

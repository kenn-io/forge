package daemonruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/kit/daemon"
)

const probeTimeout = 750 * time.Millisecond

type discovery struct {
	store   daemon.RuntimeStore
	dataDir string
	version string
}

// NewManager returns the shared lifecycle manager with Kenn Forge's discovery
// identity and per-data-directory start lock.
func NewManager(
	store daemon.RuntimeStore,
	dataDir, expectedVersion string,
	start daemon.StartFunc,
) daemon.Manager {
	discovery := discovery{store: store, dataDir: dataDir, version: expectedVersion}
	return daemon.Manager{
		Store:    StartLockStore(store, dataDir),
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
	}
}

func (d discovery) find(
	ctx context.Context,
) (daemon.RuntimeRecord, daemon.PingInfo, bool, error) {
	token, err := runtimelock.ReadAuthToken(d.dataDir)
	if err != nil {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, err
	}
	if token == "" {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, nil
	}
	proof, err := daemon.NewProof([]byte(token))
	if err != nil {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, false,
			fmt.Errorf("initialize daemon proof: %w", err)
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
	if record.Version != d.version {
		return daemon.PingInfo{}, false, fmt.Errorf(
			"running kenn-forge version %q is incompatible with %q",
			record.Version, d.version,
		)
	}
	return ping, true, nil
}

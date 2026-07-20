package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"go.kenn.io/middleman/internal/kata"
)

var (
	errKataAuthorityStale        = errors.New("kata authority invalidated while loading")
	errKataAuthorityInconsistent = errors.New("kata authority responses are inconsistent")
)

const kataAuthorityConsistencyRetries = 1

type kataAuthoritySnapshotLoader interface {
	Load(context.Context, kataAuthorityRequest) (kataAuthoritySnapshot, error)
}

type kataSnapshotCoordinatorDeps struct {
	cache               *kataSnapshotCache
	resolveDaemon       func(string) (kata.Daemon, *ProblemError)
	newLoader           func(context.Context, kata.Daemon) (kataAuthoritySnapshotLoader, error)
	newServerInstanceID func() string
}

type kataSnapshotCoordinator struct {
	root             context.Context
	cache            *kataSnapshotCache
	resolveDaemon    func(string) (kata.Daemon, *ProblemError)
	newLoader        func(context.Context, kata.Daemon) (kataAuthoritySnapshotLoader, error)
	serverInstanceID string
	generation       atomic.Uint64
	group            singleflight.Group
	loadsMu          sync.Mutex
	loadsStopping    bool
	loads            sync.WaitGroup
}

type kataCoordinatedAuthority struct {
	ServerInstanceID  string
	DaemonID          string
	Key               kataSnapshotKey
	Generation        uint64
	InvalidationEpoch uint64
	Snapshot          kataAuthoritySnapshot
}

func newKataSnapshotCoordinator(root context.Context, deps kataSnapshotCoordinatorDeps) *kataSnapshotCoordinator {
	if root == nil {
		root = context.Background()
	}
	if deps.cache == nil {
		deps.cache = newKataSnapshotCache()
	}
	if deps.resolveDaemon == nil {
		deps.resolveDaemon = selectKataDaemonForID
	}
	if deps.newLoader == nil {
		deps.newLoader = func(ctx context.Context, daemon kata.Daemon) (kataAuthoritySnapshotLoader, error) {
			client, err := newKataAPIClient(ctx, daemon)
			if err != nil {
				return nil, err
			}
			return &kataSnapshotLoader{client: client}, nil
		}
	}
	if deps.newServerInstanceID == nil {
		deps.newServerInstanceID = newKataServerInstanceID
	}
	return &kataSnapshotCoordinator{
		root:             root,
		cache:            deps.cache,
		resolveDaemon:    deps.resolveDaemon,
		newLoader:        deps.newLoader,
		serverInstanceID: deps.newServerInstanceID(),
	}
}

func (c *kataSnapshotCoordinator) run(ctx context.Context) {
	defer c.close()
	c.cache.run(ctx)
}

func (c *kataSnapshotCoordinator) close() {
	c.loadsMu.Lock()
	c.loadsStopping = true
	c.loadsMu.Unlock()
	c.loads.Wait()
	c.cache.close()
}

func (c *kataSnapshotCoordinator) invalidateDaemon(daemonID string) uint64 {
	return c.cache.invalidateDaemon(daemonID)
}

func (c *kataSnapshotCoordinator) daemonEpoch(daemonID string) uint64 {
	return c.cache.daemonEpoch(daemonID)
}

func (c *kataSnapshotCoordinator) loadAuthority(
	ctx context.Context,
	daemonID string,
	request kataAuthorityRequest,
) (kataCoordinatedAuthority, error) {
	if err := validateKataAuthorityRequest(request); err != nil {
		return kataCoordinatedAuthority{}, err
	}
	consistencyRetries := 0
	for {
		if err := ctx.Err(); err != nil {
			return kataCoordinatedAuthority{}, err
		}
		daemon, problem := c.resolveDaemon(daemonID)
		if problem != nil {
			return kataCoordinatedAuthority{}, problem
		}
		fingerprint := kataDaemonTargetFingerprint(daemon)
		epoch := c.cache.observeDaemonFingerprint(daemon.ID, fingerprint)
		key := kataSnapshotKey{
			DaemonID:          daemon.ID,
			DaemonFingerprint: fingerprint,
			Scope:             request.Scope,
			ProjectUID:        request.ProjectUID,
			Authority:         request.Authority,
		}
		if snapshot, ok := c.cache.get(key); ok && c.cache.daemonEpoch(daemon.ID) == epoch {
			matches, err := c.targetMatches(key)
			if err != nil {
				return kataCoordinatedAuthority{}, err
			}
			if !matches {
				c.cache.invalidateDaemonIfEpoch(daemon.ID, epoch)
				continue
			}
			if c.cache.daemonEpoch(daemon.ID) != epoch {
				continue
			}
			return c.coordinated(key, epoch, snapshot), nil
		}

		result := make(chan singleflight.Result, 1)
		if !c.runTrackedLoad(func() {
			value, err, shared := c.group.Do(kataAuthoritySingleflightKey(key, epoch), func() (any, error) {
				if snapshot, ok := c.cache.get(key); ok && c.cache.daemonEpoch(daemon.ID) == epoch {
					return snapshot, nil
				}
				if c.cache.daemonEpoch(daemon.ID) != epoch {
					return nil, errKataAuthorityStale
				}

				loadCtx, cancel := context.WithTimeout(c.root, kataDaemonReadTimeout)
				defer cancel()
				loader, err := c.newLoader(loadCtx, daemon)
				if err != nil {
					return nil, kataSnapshotUpstreamError("create client", err)
				}
				snapshot, err := loader.Load(loadCtx, request)
				if err != nil {
					return nil, err
				}

				currentDaemon, currentProblem := c.resolveDaemon(daemon.ID)
				if currentProblem != nil {
					return nil, currentProblem
				}
				currentFingerprint := kataDaemonTargetFingerprint(currentDaemon)
				currentEpoch := c.cache.observeDaemonFingerprint(daemon.ID, currentFingerprint)
				if currentFingerprint != key.DaemonFingerprint || currentEpoch != epoch {
					return nil, errKataAuthorityStale
				}

				snapshot.Generation = c.generation.Add(1)
				if !c.cache.setIfDaemonEpoch(key, snapshot, epoch) {
					return nil, errKataAuthorityStale
				}
				return snapshot, nil
			})
			result <- singleflight.Result{Val: value, Err: err, Shared: shared}
		}) {
			if err := c.root.Err(); err != nil {
				return kataCoordinatedAuthority{}, err
			}
			return kataCoordinatedAuthority{}, context.Canceled
		}

		select {
		case <-ctx.Done():
			return kataCoordinatedAuthority{}, ctx.Err()
		case completed := <-result:
			if errors.Is(completed.Err, errKataAuthorityStale) {
				continue
			}
			if errors.Is(completed.Err, errKataAuthorityInconsistent) {
				if consistencyRetries < kataAuthorityConsistencyRetries {
					consistencyRetries++
					continue
				}
				return kataCoordinatedAuthority{}, kataSnapshotUpstreamError("load consistent authority snapshot", completed.Err)
			}
			if completed.Err != nil {
				return kataCoordinatedAuthority{}, completed.Err
			}
			matches, err := c.targetMatches(key)
			if err != nil {
				return kataCoordinatedAuthority{}, err
			}
			if !matches {
				c.cache.invalidateDaemonIfEpoch(daemon.ID, epoch)
				continue
			}
			if c.cache.daemonEpoch(daemon.ID) != epoch {
				continue
			}
			snapshot := cloneKataAuthoritySnapshot(completed.Val.(kataAuthoritySnapshot))
			return c.coordinated(key, epoch, snapshot), nil
		}
	}
}

func (c *kataSnapshotCoordinator) runTrackedLoad(load func()) bool {
	c.loadsMu.Lock()
	defer c.loadsMu.Unlock()
	if c.loadsStopping {
		return false
	}
	c.loads.Go(load)
	return true
}

func (c *kataSnapshotCoordinator) targetMatches(key kataSnapshotKey) (bool, error) {
	daemon, problem := c.resolveDaemon(key.DaemonID)
	if problem != nil {
		return false, problem
	}
	fingerprint := kataDaemonTargetFingerprint(daemon)
	c.cache.observeDaemonFingerprint(daemon.ID, fingerprint)
	return fingerprint == key.DaemonFingerprint, nil
}

func (c *kataSnapshotCoordinator) coordinated(
	key kataSnapshotKey,
	epoch uint64,
	snapshot kataAuthoritySnapshot,
) kataCoordinatedAuthority {
	return kataCoordinatedAuthority{
		ServerInstanceID:  c.serverInstanceID,
		DaemonID:          key.DaemonID,
		Key:               key,
		Generation:        snapshot.Generation,
		InvalidationEpoch: epoch,
		Snapshot:          snapshot,
	}
}

func validateKataAuthorityRequest(request kataAuthorityRequest) error {
	switch request.Scope {
	case "global":
		if request.ProjectUID != "" {
			return problemValidation("project_uid", "project_uid is only valid for project scope")
		}
	case "project":
		trimmedProjectUID := strings.TrimSpace(request.ProjectUID)
		if trimmedProjectUID == "" {
			return problemValidation("project_uid", "project_uid is required for project scope")
		}
		if trimmedProjectUID != request.ProjectUID {
			return problemValidation("project_uid", "project_uid must not contain leading or trailing whitespace")
		}
	default:
		return problemValidation("scope", "unsupported Kata scope", "global", "project")
	}
	switch request.Authority {
	case "open", "ready", "closed", "all":
		return nil
	default:
		return problemValidation("authority", "unsupported Kata authority", "open", "ready", "closed", "all")
	}
}

func kataAuthoritySingleflightKey(key kataSnapshotKey, epoch uint64) string {
	return strings.Join([]string{
		key.DaemonID,
		key.DaemonFingerprint,
		key.Scope,
		key.ProjectUID,
		key.Authority,
		strconv.FormatUint(epoch, 10),
	}, kataDaemonCacheKeyDelim)
}

func kataDaemonTargetFingerprint(daemon kata.Daemon) string {
	mode := "remote"
	if daemon.Local {
		mode = "local"
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{
		daemon.URL,
		mode,
		strconv.FormatBool(daemon.AllowInsecure),
		kataDaemonForwardToken(daemon),
	}, kataDaemonCacheKeyDelim)))
	return hex.EncodeToString(digest[:])
}

func newKataServerInstanceID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err == nil {
		return hex.EncodeToString(data[:])
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

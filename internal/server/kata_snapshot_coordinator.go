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
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"go.kenn.io/middleman/internal/kata"
)

var errKataAuthorityStale = errors.New("kata authority invalidated while loading")

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
	c.cache.close()
}

func (c *kataSnapshotCoordinator) loadAuthority(
	ctx context.Context,
	daemonID string,
	request kataAuthorityRequest,
) (kataCoordinatedAuthority, error) {
	if err := validateKataAuthorityRequest(request); err != nil {
		return kataCoordinatedAuthority{}, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return kataCoordinatedAuthority{}, err
		}
		daemon, problem := c.resolveDaemon(daemonID)
		if problem != nil {
			return kataCoordinatedAuthority{}, problem
		}
		key := kataSnapshotKey{
			DaemonID:          daemon.ID,
			DaemonFingerprint: kataDaemonTargetFingerprint(daemon),
			Scope:             request.Scope,
			ProjectUID:        request.ProjectUID,
			Authority:         request.Authority,
		}
		epoch := c.cache.daemonEpoch(daemon.ID)
		if snapshot, ok := c.cache.get(key); ok && c.cache.daemonEpoch(daemon.ID) == epoch {
			matches, err := c.targetMatches(key)
			if err != nil {
				return kataCoordinatedAuthority{}, err
			}
			if !matches {
				c.cache.invalidateDaemon(daemon.ID)
				continue
			}
			return c.coordinated(key, epoch, snapshot), nil
		}

		result := c.group.DoChan(kataAuthoritySingleflightKey(key, epoch), func() (any, error) {
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
			if kataDaemonTargetFingerprint(currentDaemon) != key.DaemonFingerprint {
				c.cache.invalidateDaemon(daemon.ID)
				return nil, errKataAuthorityStale
			}

			snapshot.Generation = c.generation.Add(1)
			if !c.cache.setIfDaemonEpoch(key, snapshot, epoch) {
				return nil, errKataAuthorityStale
			}
			return snapshot, nil
		})

		select {
		case <-ctx.Done():
			return kataCoordinatedAuthority{}, ctx.Err()
		case completed := <-result:
			if errors.Is(completed.Err, errKataAuthorityStale) {
				continue
			}
			if completed.Err != nil {
				return kataCoordinatedAuthority{}, completed.Err
			}
			matches, err := c.targetMatches(key)
			if err != nil {
				return kataCoordinatedAuthority{}, err
			}
			if !matches {
				c.cache.invalidateDaemon(daemon.ID)
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

func (c *kataSnapshotCoordinator) targetMatches(key kataSnapshotKey) (bool, error) {
	daemon, problem := c.resolveDaemon(key.DaemonID)
	if problem != nil {
		return false, problem
	}
	return kataDaemonTargetFingerprint(daemon) == key.DaemonFingerprint, nil
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
		if strings.TrimSpace(request.ProjectUID) == "" {
			return problemValidation("project_uid", "project_uid is required for project scope")
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

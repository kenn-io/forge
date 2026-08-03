package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/projects"
)

func TestRoborevRepositoryProbeCachesDefinitiveResultsAndDeduplicatesIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var inventoryCalls atomic.Int32
	var hookPathCalls atomic.Int32
	var inspectCalls atomic.Int32
	probe := newRoborevRepositoryProbeWithDeps(
		[]projects.KnownPlatformHost{{Platform: "github", Host: "github.com"}},
		roborevRepositoryProbeDeps{
			now: time.Now,
			loadInventory: func(context.Context) ([]roborevTrackedRepository, error) {
				inventoryCalls.Add(1)
				return []roborevTrackedRepository{
					{RootPath: "/checkout/main", Identity: "https://github.com/acme/widgets.git"},
					{RootPath: "/checkout/worktree", Identity: "git@github.com:acme/widgets.git"},
				}, nil
			},
			resolveHookPath: func(_ context.Context, root string) (string, error) {
				hookPathCalls.Add(1)
				return "/shared/hooks/post-commit", nil
			},
			inspectHook: func(string) (bool, error) {
				inspectCalls.Add(1)
				return true, nil
			},
		},
	)

	first, err := probe.configuredRepositories(t.Context())
	require.NoError(err)
	second, err := probe.configuredRepositories(t.Context())
	require.NoError(err)

	require.Len(first, 1)
	assert.Equal(roborevConfiguredRepositoryResponse{
		Provider:     "github",
		PlatformHost: "github.com",
		RepoPath:     "acme/widgets",
		Owner:        "acme",
		Name:         "widgets",
	}, first[0])
	assert.Equal(first, second)
	assert.Equal(int32(1), inventoryCalls.Load())
	assert.Equal(int32(2), hookPathCalls.Load())
	assert.Equal(int32(1), inspectCalls.Load())
}

func TestRoborevRepositoryProbeCoalescesConcurrentRequests(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	started := make(chan struct{})
	release := make(chan struct{})
	waiterJoined := make(chan struct{})
	var calls atomic.Int32
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	probe := newRoborevRepositoryProbeWithDeps(nil, roborevRepositoryProbeDeps{
		now: time.Now,
		onWaitForInFlight: func() {
			close(waiterJoined)
		},
		loadInventory: func(context.Context) ([]roborevTrackedRepository, error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-release
			return []roborevTrackedRepository{}, nil
		},
		resolveHookPath: func(context.Context, string) (string, error) { return "", nil },
		inspectHook:     func(string) (bool, error) { return false, nil },
	})

	results := make(chan error, 2)
	go func() {
		_, err := probe.configuredRepositories(t.Context())
		results <- err
	}()
	<-started
	go func() {
		_, err := probe.configuredRepositories(t.Context())
		results <- err
	}()
	select {
	case <-waiterJoined:
	case <-time.After(time.Second):
		require.Fail("second request did not join the in-flight probe")
	}
	releaseOnce.Do(func() { close(release) })
	require.NoError(<-results)
	require.NoError(<-results)
	assert.Equal(int32(1), calls.Load())
}

func TestRoborevRepositoryProbeBoundsHookResolution(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var active atomic.Int32
	var maximum atomic.Int32
	repositories := make([]roborevTrackedRepository, 12)
	for i := range repositories {
		repositories[i] = roborevTrackedRepository{
			RootPath: fmt.Sprintf("/checkout/%d", i),
			Identity: fmt.Sprintf("https://github.com/acme/repo-%d.git", i),
		}
	}
	probe := newRoborevRepositoryProbeWithDeps(nil, roborevRepositoryProbeDeps{
		now:           time.Now,
		loadInventory: func(context.Context) ([]roborevTrackedRepository, error) { return repositories, nil },
		resolveHookPath: func(_ context.Context, root string) (string, error) {
			current := active.Add(1)
			for {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			active.Add(-1)
			return root + "/post-commit", nil
		},
		inspectHook: func(string) (bool, error) { return true, nil },
	})

	configured, err := probe.configuredRepositories(t.Context())
	require.NoError(err)
	assert.Len(configured, 12)
	assert.Greater(maximum.Load(), int32(1))
	assert.LessOrEqual(maximum.Load(), int32(roborevHookProbeWorkers))
}

func TestRoborevRepositoryProbeRetriesTransientCheckoutFailureAfterCooldown(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	var failingCalls atomic.Int32
	probe := newRoborevRepositoryProbeWithDeps(nil, roborevRepositoryProbeDeps{
		now: func() time.Time { return now },
		loadInventory: func(context.Context) ([]roborevTrackedRepository, error) {
			return []roborevTrackedRepository{
				{RootPath: "/positive", Identity: "https://github.com/acme/widgets.git"},
				{RootPath: "/transient", Identity: "https://github.com/acme/tools.git"},
			}, nil
		},
		resolveHookPath: func(_ context.Context, root string) (string, error) {
			if root == "/transient" && failingCalls.Add(1) == 1 {
				return "", errors.New("temporary git failure")
			}
			return root + "/post-commit", nil
		},
		inspectHook: func(string) (bool, error) { return true, nil },
	})

	first, err := probe.configuredRepositories(t.Context())
	require.NoError(err)
	require.Len(first, 1)
	assert.Equal("acme/widgets", first[0].RepoPath)
	now = now.Add(29 * time.Second)
	second, err := probe.configuredRepositories(t.Context())
	require.NoError(err)
	assert.Len(second, 1)
	assert.Equal(int32(1), failingCalls.Load())
	now = now.Add(time.Second)
	third, err := probe.configuredRepositories(t.Context())
	require.NoError(err)
	assert.Len(third, 2)
	assert.Equal(int32(2), failingCalls.Load())
}

func TestInspectRoborevPostCommitHook(t *testing.T) {
	tests := []struct {
		name    string
		content string
		mode    os.FileMode
		want    bool
	}{
		{name: "generated marker", content: "#!/bin/sh\n# roborev post-commit hook v4\n", mode: 0o755, want: true},
		{name: "current variable command", content: "#!/bin/sh\n\"$ROBOREV\" post-commit\n", mode: 0o755, want: true},
		{name: "current direct command", content: "#!/bin/sh\nroborev post-commit\n", mode: 0o755, want: true},
		{name: "legacy variable command", content: "#!/bin/sh\n\"$ROBOREV\" enqueue --quiet\n", mode: 0o755, want: true},
		{name: "legacy direct command", content: "#!/bin/sh\nroborev enqueue --quiet\n", mode: 0o755, want: true},
		{name: "unrelated executable", content: "#!/bin/sh\necho hello\n", mode: 0o755, want: false},
		{name: "non executable", content: "#!/bin/sh\nroborev post-commit\n", mode: 0o644, want: runtime.GOOS == "windows"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			path := filepath.Join(t.TempDir(), "post-commit")
			require.NoError(os.WriteFile(path, []byte(tt.content), tt.mode))
			got, err := inspectRoborevPostCommitHook(path)
			require.NoError(err)
			assert.Equal(tt.want, got)
		})
	}

	assert := assert.New(t)
	require := require.New(t)
	missing, err := inspectRoborevPostCommitHook(filepath.Join(t.TempDir(), "missing"))
	require.NoError(err)
	assert.False(missing)
	directory, err := inspectRoborevPostCommitHook(t.TempDir())
	require.NoError(err)
	assert.False(directory)
}

func TestListRoborevConfiguredRepositories(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv := setupTestServerWithRoborev(t, "http://127.0.0.1:1")
	srv.roborevRepositories = newRoborevRepositoryProbeWithDeps(nil, roborevRepositoryProbeDeps{
		now: time.Now,
		loadInventory: func(context.Context) ([]roborevTrackedRepository, error) {
			return []roborevTrackedRepository{
				{RootPath: "/checkout/widgets", Identity: "https://github.com/acme/widgets.git"},
			}, nil
		},
		resolveHookPath: func(context.Context, string) (string, error) { return "/hooks/post-commit", nil },
		inspectHook:     func(string) (bool, error) { return true, nil },
	})

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/roborev/configured-repositories", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		Repositories []roborevConfiguredRepositoryResponse `json:"repositories"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Repositories, 1)
	assert.Equal("github", body.Repositories[0].Provider)
	assert.Equal("github.com", body.Repositories[0].PlatformHost)
	assert.Equal("acme/widgets", body.Repositories[0].RepoPath)
	assert.NotContains(rr.Body.String(), "/checkout/widgets")
}

func TestListRoborevConfiguredRepositoriesReturnsTypedUnavailableWithoutBlockingSummaries(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv := setupTestServerWithRoborev(t, "http://private.invalid:7373")
	srv.roborevRepositories = newRoborevRepositoryProbeWithDeps(nil, roborevRepositoryProbeDeps{
		now: time.Now,
		loadInventory: func(context.Context) ([]roborevTrackedRepository, error) {
			return nil, errors.New("private daemon /private/checkout")
		},
	})

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/roborev/configured-repositories", nil)
	require.Equal(http.StatusServiceUnavailable, rr.Code)
	assert.Equal("application/problem+json", rr.Header().Get("Content-Type"))
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("serviceUnavailable", problem.Code)
	assert.Equal("roborev repository configuration unavailable", problem.Detail)
	assert.NotContains(rr.Body.String(), "private.invalid")
	assert.NotContains(rr.Body.String(), "/private/checkout")

	summaries := doJSON(t, srv, http.MethodGet, "/api/v1/repos/summary", nil)
	assert.Equal(http.StatusOK, summaries.Code)
}

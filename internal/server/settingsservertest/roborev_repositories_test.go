package settingsservertest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/projects"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/roborevapi"
)

func TestRoborevRepositoryProbeCachesDefinitiveResultsAndDeduplicatesIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var inventoryCalls atomic.Int32
	var hookPathCalls atomic.Int32
	var inspectCalls atomic.Int32
	probe := roborevapi.NewRoborevRepositoryProbeWithDeps(
		[]projects.KnownPlatformHost{{Platform: "github", Host: "github.com"}},
		roborevapi.RoborevRepositoryProbeDeps{
			Now: time.Now,
			LoadInventory: func(context.Context) ([]roborevapi.RoborevTrackedRepository, error) {
				inventoryCalls.Add(1)
				return []roborevapi.RoborevTrackedRepository{
					{RootPath: "/checkout/main", Identity: "https://github.com/acme/widgets.git"},
					{RootPath: "/checkout/worktree", Identity: "git@github.com:acme/widgets.git"},
				}, nil
			},
			ResolveHookPath: func(_ context.Context, root string) (string, error) {
				hookPathCalls.Add(1)
				return "/shared/hooks/post-commit", nil
			},
			InspectHook: func(string) (bool, error) {
				inspectCalls.Add(1)
				return true, nil
			},
		},
	)

	first, err := probe.ConfiguredRepositories(t.Context())
	require.NoError(err)
	second, err := probe.ConfiguredRepositories(t.Context())
	require.NoError(err)

	require.Len(first, 1)
	assert.Equal(itemapi.RoborevConfiguredRepositoryResponse{
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

func TestRoborevRepositoryProbeInvalidateReloadsInventoryAndDefinitiveResults(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var calls atomic.Int32
	probe := roborevapi.NewRoborevRepositoryProbeWithDeps(
		[]projects.KnownPlatformHost{{Platform: "github", Host: "github.com"}},
		roborevapi.RoborevRepositoryProbeDeps{
			Now: time.Now,
			LoadInventory: func(context.Context) ([]roborevapi.RoborevTrackedRepository, error) {
				if calls.Add(1) == 1 {
					return []roborevapi.RoborevTrackedRepository{{
						RootPath: "/first", Identity: "https://github.com/acme/first.git",
					}}, nil
				}
				return []roborevapi.RoborevTrackedRepository{{
					RootPath: "/second", Identity: "https://github.com/acme/second.git",
				}}, nil
			},
			ResolveHookPath: func(_ context.Context, root string) (string, error) {
				return root + "/post-commit", nil
			},
			InspectHook: func(string) (bool, error) { return true, nil },
		},
	)

	first, err := probe.ConfiguredRepositories(t.Context())
	require.NoError(err)
	require.Len(first, 1)
	assert.Equal("acme/first", first[0].RepoPath)

	probe.Invalidate()
	second, err := probe.ConfiguredRepositories(t.Context())
	require.NoError(err)
	require.Len(second, 1)
	assert.Equal("acme/second", second[0].RepoPath)
	assert.Equal(int32(2), calls.Load())
}

func TestRoborevRepositoryProbeInvalidateFencesInFlightRefresh(t *testing.T) {
	require := require.New(t)
	started := make(chan struct{})
	freshStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var calls atomic.Int32
	probe := roborevapi.NewRoborevRepositoryProbeWithDeps(
		[]projects.KnownPlatformHost{{Platform: "github", Host: "github.com"}},
		roborevapi.RoborevRepositoryProbeDeps{
			Now: time.Now,
			LoadInventory: func(context.Context) ([]roborevapi.RoborevTrackedRepository, error) {
				if calls.Add(1) == 1 {
					close(started)
					<-release
					return []roborevapi.RoborevTrackedRepository{{
						RootPath: "/stale", Identity: "https://github.com/acme/stale.git",
					}}, nil
				}
				close(freshStarted)
				return []roborevapi.RoborevTrackedRepository{{
					RootPath: "/fresh", Identity: "https://github.com/acme/fresh.git",
				}}, nil
			},
			ResolveHookPath: func(_ context.Context, root string) (string, error) {
				return root + "/post-commit", nil
			},
			InspectHook: func(string) (bool, error) { return true, nil },
		},
	)

	result := make(chan []itemapi.RoborevConfiguredRepositoryResponse, 1)
	go func() {
		configured, _ := probe.ConfiguredRepositories(t.Context())
		result <- configured
	}()
	<-started
	probe.Invalidate()
	select {
	case <-freshStarted:
	case <-time.After(time.Second):
		require.Fail("invalidation did not start a fresh probe")
	}
	configured := <-result
	releaseOnce.Do(func() { close(release) })
	require.Len(configured, 1)
	require.Equal("acme/fresh", configured[0].RepoPath)
	require.Equal(int32(2), calls.Load())
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
	probe := roborevapi.NewRoborevRepositoryProbeWithDeps(nil, roborevapi.RoborevRepositoryProbeDeps{
		Now: time.Now,
		OnWaitForInFlight: func() {
			close(waiterJoined)
		},
		LoadInventory: func(context.Context) ([]roborevapi.RoborevTrackedRepository, error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-release
			return []roborevapi.RoborevTrackedRepository{}, nil
		},
		ResolveHookPath: func(context.Context, string) (string, error) { return "", nil },
		InspectHook:     func(string) (bool, error) { return false, nil },
	})

	results := make(chan error, 2)
	go func() {
		_, err := probe.ConfiguredRepositories(t.Context())
		results <- err
	}()
	<-started
	go func() {
		_, err := probe.ConfiguredRepositories(t.Context())
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

func TestRoborevRepositoryProbeCallerCancellationDoesNotPoisonWaiters(t *testing.T) {
	require := require.New(t)
	started := make(chan struct{})
	release := make(chan struct{})
	waiterJoined := make(chan struct{})
	probe := roborevapi.NewRoborevRepositoryProbeWithDeps(nil, roborevapi.RoborevRepositoryProbeDeps{
		Now:               time.Now,
		OnWaitForInFlight: func() { close(waiterJoined) },
		LoadInventory: func(ctx context.Context) ([]roborevapi.RoborevTrackedRepository, error) {
			close(started)
			select {
			case <-release:
				return []roborevapi.RoborevTrackedRepository{}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		ResolveHookPath: func(context.Context, string) (string, error) { return "", nil },
		InspectHook:     func(string) (bool, error) { return false, nil },
	})

	leaderCtx, cancelLeader := context.WithCancel(t.Context())
	leader := make(chan error, 1)
	go func() {
		_, err := probe.ConfiguredRepositories(leaderCtx)
		leader <- err
	}()
	<-started
	waiter := make(chan error, 1)
	go func() {
		_, err := probe.ConfiguredRepositories(t.Context())
		waiter <- err
	}()
	<-waiterJoined
	cancelLeader()
	require.ErrorIs(<-leader, context.Canceled)
	close(release)
	require.NoError(<-waiter)
}

func TestRoborevRepositoryProbeBoundsHookResolution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		var active atomic.Int32
		var maximum atomic.Int32
		repositories := make([]roborevapi.RoborevTrackedRepository, 12)
		for i := range repositories {
			repositories[i] = roborevapi.RoborevTrackedRepository{
				RootPath: fmt.Sprintf("/checkout/%d", i),
				Identity: fmt.Sprintf("https://github.com/acme/repo-%d.git", i),
			}
		}
		probe := roborevapi.NewRoborevRepositoryProbeWithDeps(nil, roborevapi.RoborevRepositoryProbeDeps{
			Now:           time.Now,
			LoadInventory: func(context.Context) ([]roborevapi.RoborevTrackedRepository, error) { return repositories, nil },
			ResolveHookPath: func(_ context.Context, root string) (string, error) {
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
			InspectHook: func(string) (bool, error) { return true, nil },
		})

		configured, err := probe.ConfiguredRepositories(t.Context())
		require.NoError(err)
		assert.Len(configured, 12)
		assert.Equal(int32(roborevapi.RoborevHookProbeWorkers), maximum.Load())
	})
}

func TestRoborevRepositoryProbeRetriesTransientCheckoutFailureAfterCooldown(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	var failingCalls atomic.Int32
	probe := roborevapi.NewRoborevRepositoryProbeWithDeps(nil, roborevapi.RoborevRepositoryProbeDeps{
		Now: func() time.Time { return now },
		LoadInventory: func(context.Context) ([]roborevapi.RoborevTrackedRepository, error) {
			return []roborevapi.RoborevTrackedRepository{
				{RootPath: "/positive", Identity: "https://github.com/acme/widgets.git"},
				{RootPath: "/transient", Identity: "https://github.com/acme/tools.git"},
			}, nil
		},
		ResolveHookPath: func(_ context.Context, root string) (string, error) {
			if root == "/transient" && failingCalls.Add(1) == 1 {
				return "", errors.New("temporary git failure")
			}
			return root + "/post-commit", nil
		},
		InspectHook: func(string) (bool, error) { return true, nil },
	})

	first, err := probe.ConfiguredRepositories(t.Context())
	require.NoError(err)
	require.Len(first, 1)
	assert.Equal("acme/widgets", first[0].RepoPath)
	now = now.Add(29 * time.Second)
	second, err := probe.ConfiguredRepositories(t.Context())
	require.NoError(err)
	assert.Len(second, 1)
	assert.Equal(int32(1), failingCalls.Load())
	now = now.Add(time.Second)
	third, err := probe.ConfiguredRepositories(t.Context())
	require.NoError(err)
	assert.Len(third, 2)
	assert.Equal(int32(2), failingCalls.Load())
}

func TestRoborevRepositoryProbeStartsCheckoutCooldownWhenFailureCompletes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	start := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	var nowNanos atomic.Int64
	nowNanos.Store(start.UnixNano())
	var calls atomic.Int32
	probe := roborevapi.NewRoborevRepositoryProbeWithDeps(nil, roborevapi.RoborevRepositoryProbeDeps{
		Now: func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() },
		LoadInventory: func(context.Context) ([]roborevapi.RoborevTrackedRepository, error) {
			return []roborevapi.RoborevTrackedRepository{
				{RootPath: "/slow", Identity: "https://github.com/acme/widgets.git"},
			}, nil
		},
		ResolveHookPath: func(context.Context, string) (string, error) {
			if calls.Add(1) == 1 {
				nowNanos.Store(start.Add(roborevapi.RoborevProbeRetryCooldown + time.Second).UnixNano())
				return "", errors.New("slow git failure")
			}
			return "/hooks/post-commit", nil
		},
		InspectHook: func(string) (bool, error) { return true, nil },
	})

	first, err := probe.ConfiguredRepositories(t.Context())
	require.NoError(err)
	assert.Empty(first)
	assert.Equal(int32(1), calls.Load())

	nowNanos.Store(start.Add(2*roborevapi.RoborevProbeRetryCooldown + time.Second).UnixNano())
	second, err := probe.ConfiguredRepositories(t.Context())
	require.NoError(err)
	assert.Len(second, 1)
	assert.Equal(int32(2), calls.Load())
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
		{name: "partial post commit command", content: "#!/bin/sh\nroborev post-commit-disabled\n", mode: 0o755, want: false},
		{name: "partial enqueue command", content: "#!/bin/sh\nroborev enqueue-old\n", mode: 0o755, want: false},
		{name: "commented command", content: "#!/bin/sh\n# roborev post-commit\n", mode: 0o755, want: false},
		{name: "command in string", content: "#!/bin/sh\necho 'roborev post-commit'\n", mode: 0o755, want: false},
		{name: "non executable", content: "#!/bin/sh\nroborev post-commit\n", mode: 0o644, want: runtime.GOOS == "windows"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			path := filepath.Join(t.TempDir(), "post-commit")
			require.NoError(os.WriteFile(path, []byte(tt.content), tt.mode))
			got, err := roborevapi.InspectRoborevPostCommitHook(path)
			require.NoError(err)
			assert.Equal(tt.want, got)
		})
	}

	assert := assert.New(t)
	require := require.New(t)
	missing, err := roborevapi.InspectRoborevPostCommitHook(filepath.Join(t.TempDir(), "missing"))
	require.NoError(err)
	assert.False(missing)
	directory, err := roborevapi.InspectRoborevPostCommitHook(t.TempDir())
	require.NoError(err)
	assert.False(directory)
}

func TestLoadRoborevRepositoryInventoryValidatesCompleteEnvelope(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []roborevapi.RoborevTrackedRepository
	}{
		{
			name: "valid repo without review jobs",
			body: `{"repos":[{"root_path":"/repo","identity":"https://github.com/acme/widgets.git"}],"total_count":0}`,
			want: []roborevapi.RoborevTrackedRepository{{RootPath: "/repo", Identity: "https://github.com/acme/widgets.git"}},
		},
		{
			name: "valid mixed identity availability",
			body: `{"repos":[{"root_path":"/repo","identity":"https://github.com/acme/widgets.git"},{"root_path":"/local"}],"total_count":3}`,
			want: []roborevapi.RoborevTrackedRepository{
				{RootPath: "/repo", Identity: "https://github.com/acme/widgets.git"},
				{RootPath: "/local"},
			},
		},
		{name: "valid null repos", body: `{"repos":null,"total_count":0}`, want: []roborevapi.RoborevTrackedRepository{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			got, err := roborevapi.LoadRoborevRepositoryInventory(server.Client(), server.URL)(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	invalid := []string{
		`{}`,
		`{"repos":[{"identity":"https://github.com/acme/widgets.git"}],"total_count":1}`,
		`{"repos":[],"total_count":0} trailing`,
	}
	for _, body := range invalid {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
		_, err := roborevapi.LoadRoborevRepositoryInventory(server.Client(), server.URL)(t.Context())
		server.Close()
		require.Error(t, err, body)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat(" ", roborevapi.RoborevInventoryMaxBytes+1))
	}))
	defer server.Close()
	_, err := roborevapi.LoadRoborevRepositoryInventory(server.Client(), server.URL)(t.Context())
	assert.Error(t, err, "oversized inventory must be rejected")
}

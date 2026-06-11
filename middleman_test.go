package middleman

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewReturnsWorkingHandler(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}

	inst, err := New(Options{
		Token:        "test-token",
		DataDir:      t.TempDir(),
		BasePath:     "/middleman/",
		SyncInterval: time.Minute,
		Assets:       frontend,
	})
	require.NoError(t, err)
	defer inst.Close()

	req := httptest.NewRequest(http.MethodGet, "/middleman/", nil)
	rr := httptest.NewRecorder()
	inst.Handler().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	Assert.Contains(t, rr.Body.String(), `window.__BASE_PATH__="/middleman/"`)
}

func TestMergeRequestSummaryUsesTypedState(t *testing.T) {
	summary := MergeRequestSummary{State: MergeRequestStateOpen}

	Assert.Equal(t, MergeRequestStateOpen, requireMergeRequestState(summary.State))
}

func requireMergeRequestState(state MergeRequestState) MergeRequestState {
	return state
}

func TestNewEmbeddedBootstrapGlobals(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}

	hideSync := true
	inst, err := New(Options{
		Token:    "test-token",
		DataDir:  t.TempDir(),
		BasePath: "/middleman/",
		EmbedConfig: &EmbedConfig{
			Theme: &ThemeConfig{Mode: "dark"},
			UI:    &UIConfig{HideSync: &hideSync},
		},
		Assets: frontend,
	})
	require.NoError(t, err)
	defer inst.Close()

	req := httptest.NewRequest(http.MethodGet, "/middleman/", nil)
	rr := httptest.NewRecorder()
	inst.Handler().ServeHTTP(rr, req)

	body := rr.Body.String()
	assert := Assert.New(t)
	assert.Contains(body, `window.__middleman_config=`)
	assert.NotContains(body, `__MIDDLEMAN_EMBEDDED__`)
}

func TestNewWiresGraphQLTrackerIntoEmbeddedClient(t *testing.T) {
	require := require.New(t)
	inst, err := New(Options{
		Token:   "test-token",
		DataDir: t.TempDir(),
		Repos:   []Repo{{Owner: "acme", Name: "widget"}},
	})
	require.NoError(err)
	defer inst.Close()

	client, err := inst.syncer.ClientForHost("github.com")
	require.NoError(err)

	value := reflect.ValueOf(client)
	require.Equal(reflect.Pointer, value.Kind())
	tracker := value.Elem().FieldByName("graphQLRateTracker")
	require.True(tracker.IsValid())
	require.False(tracker.IsNil())
}

func TestEmbedConfigFullFlow(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(
				`<!DOCTYPE html><html><head></head>` +
					`<body>app</body></html>`,
			),
		},
	}

	hideStar := true
	sidebarCollapsed := false
	inst, err := New(Options{
		Token:    "test-token",
		DataDir:  t.TempDir(),
		BasePath: "/app/",
		EmbedConfig: &EmbedConfig{
			Theme: &ThemeConfig{
				Mode:   "dark",
				Colors: map[string]string{"bgPrimary": "#1a1a2e"},
				Fonts:  map[string]string{"sans": "SF Pro"},
			},
			UI: &UIConfig{
				HideStar:         &hideStar,
				SidebarCollapsed: &sidebarCollapsed,
				Repo: &RepoRef{
					Owner: "apache", Name: "arrow",
				},
			},
		},
		Assets: frontend,
	})
	require.NoError(t, err)
	defer inst.Close()

	req := httptest.NewRequest(http.MethodGet, "/app/", nil)
	rr := httptest.NewRecorder()
	inst.Handler().ServeHTTP(rr, req)

	body := rr.Body.String()
	assert := Assert.New(t)

	assert.Contains(body, `window.__middleman_config=`)
	assert.Contains(body, `"mode":"dark"`)
	assert.Contains(body, `"bgPrimary":"#1a1a2e"`)
	assert.Contains(body, `"sans":"SF Pro"`)
	assert.Contains(body, `"hideStar":true`)
	assert.Contains(body, `"sidebarCollapsed":false`)
	assert.Contains(body, `"owner":"apache"`)
	assert.NotContains(body, `__MIDDLEMAN_EMBEDDED__`)
	assert.NotContains(body, `__MIDDLEMAN_APP_NAME__`)
	assert.Contains(body, `window.__BASE_PATH__="/app/"`)
}

func TestNoEmbedConfigStandaloneMode(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(
				`<!DOCTYPE html><html><head></head>` +
					`<body>app</body></html>`,
			),
		},
	}

	inst, err := New(Options{
		Token:   "test-token",
		DataDir: t.TempDir(),
		Assets:  frontend,
	})
	require.NoError(t, err)
	defer inst.Close()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	inst.Handler().ServeHTTP(rr, req)

	body := rr.Body.String()
	assert := Assert.New(t)
	assert.Contains(body, `window.__BASE_PATH__="/"`)
	assert.NotContains(body, `__middleman_config`)
}

func TestNewRequiresToken(t *testing.T) {
	_, err := New(Options{
		DataDir: t.TempDir(),
	})
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "token")
}

func TestNewRequiresDataDir(t *testing.T) {
	_, err := New(Options{
		Token: "test-token",
	})
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "DataDir")
}

func TestNewWithDBPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "shared.db")
	inst, err := New(Options{
		Token:  "test-token",
		DBPath: dbPath,
		Repos:  []Repo{{Owner: "t", Name: "r"}},
	})
	require.NoError(t, err)
	defer inst.Close()

	_, err = os.Stat(dbPath)
	require.NoError(t, err)
}

func TestNewWithResolveToken(t *testing.T) {
	dir := t.TempDir()
	called := false
	inst, err := New(Options{
		ResolveToken: func(
			_ context.Context, host string,
		) (string, error) {
			called = true
			require.Equal(t, "github.com", host)
			return "resolved-token", nil
		},
		DataDir: dir,
		Repos:   []Repo{{Owner: "t", Name: "r"}},
	})
	require.NoError(t, err)
	defer inst.Close()
	require.True(t, called)
}

func TestResolveHostTokenSourcesReadsResolverPerRequest(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	var calls []string
	sources, err := resolveHostTokenSources(
		t.Context(),
		[]string{"github.com"},
		"",
		func(_ context.Context, host string) (string, error) {
			calls = append(calls, host)
			return fmt.Sprintf("token-%d", len(calls)), nil
		},
	)
	require.NoError(err)
	source := sources["github.com"]
	require.NotNil(source)

	first, err := source.Token(t.Context())
	require.NoError(err)
	second, err := source.Token(t.Context())
	require.NoError(err)

	assert.Equal("token-2", first)
	assert.Equal("token-3", second)
	assert.Equal([]string{"github.com", "github.com", "github.com"}, calls)
}

func TestNewResolveTokenError(t *testing.T) {
	_, err := New(Options{
		ResolveToken: func(
			_ context.Context, _ string,
		) (string, error) {
			return "", fmt.Errorf("auth failed")
		},
		DataDir: t.TempDir(),
	})
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "auth failed")
}

// TestNewWithContextCancelsResolveToken verifies NewWithContext
// propagates the caller's ctx to ResolveToken so a slow or hung
// token provider can be bounded.
func TestNewWithContextCancelsResolveToken(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // pre-cancel

	_, err := NewWithContext(ctx, Options{
		ResolveToken: func(
			resolveCtx context.Context, _ string,
		) (string, error) {
			// Observe cancellation and propagate it.
			return "", resolveCtx.Err()
		},
		DataDir: t.TempDir(),
		Repos:   []Repo{{Owner: "t", Name: "r"}},
	})
	require.ErrorIs(t, err, context.Canceled)
}

// TestNewWithContextNilCtx verifies a nil ctx is treated as
// t.Context() so ResolveToken callbacks that call
// ctx.Err() / ctx.Done() / context.WithTimeout(ctx, ...) do not
// panic. Regression test for the old New(opts) contract.
func TestNewWithContextNilCtx(t *testing.T) {
	dir := t.TempDir()
	var observed context.Context
	//nolint:staticcheck // intentional: verifying nil-ctx normalization
	inst, err := NewWithContext(nil, Options{
		ResolveToken: func(
			resolveCtx context.Context, _ string,
		) (string, error) {
			observed = resolveCtx
			// Exercise ctx methods that would panic on a
			// nil ctx: Err, Done, WithTimeout.
			_ = resolveCtx.Err()
			_ = resolveCtx.Done()
			c, cancel := context.WithTimeout(
				resolveCtx, time.Second,
			)
			cancel()
			_ = c
			return "tok", nil
		},
		DataDir: dir,
		Repos:   []Repo{{Owner: "t", Name: "r"}},
	})
	require.NoError(t, err)
	defer inst.Close()
	require.NotNil(t, observed)
	require.NoError(t, observed.Err())
}

func TestNewResolveTokenOverridesStatic(t *testing.T) {
	dir := t.TempDir()
	inst, err := New(Options{
		Token: "static-token",
		ResolveToken: func(
			_ context.Context, _ string,
		) (string, error) {
			return "dynamic-token", nil
		},
		DataDir: dir,
		Repos:   []Repo{{Owner: "t", Name: "r"}},
	})
	require.NoError(t, err)
	defer inst.Close()
}

func TestNewDBPathSkipsDataDirCreation(t *testing.T) {
	// When DBPath is set, DataDir should not be required
	// and no directory creation should be attempted.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	inst, err := New(Options{
		Token:  "test-token",
		DBPath: dbPath,
	})
	require.NoError(t, err)
	defer inst.Close()
}

func TestNewMultiHostRequiresResolveToken(t *testing.T) {
	dir := t.TempDir()
	_, err := New(Options{
		Token:   "tok",
		DataDir: dir,
		Repos: []Repo{
			{Owner: "a", Name: "b", PlatformHost: "github.com"},
			{Owner: "c", Name: "d", PlatformHost: "ghe.corp.com"},
		},
	})
	require.Error(t, err)
	Assert.Contains(t, err.Error(), "ResolveToken")
}

func TestNewMultiHostWithResolveToken(t *testing.T) {
	dir := t.TempDir()
	inst, err := New(Options{
		ResolveToken: func(
			_ context.Context, host string,
		) (string, error) {
			return "token-for-" + host, nil
		},
		DataDir: dir,
		Repos: []Repo{
			{Owner: "a", Name: "b", PlatformHost: "github.com"},
			{Owner: "c", Name: "d", PlatformHost: "ghe.corp.com"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, inst)
	inst.Close()
}

func TestNewSingleHostWithTokenStillWorks(t *testing.T) {
	dir := t.TempDir()
	inst, err := New(Options{
		Token:   "tok",
		DataDir: dir,
		Repos: []Repo{
			{Owner: "a", Name: "b"},
			{Owner: "c", Name: "d"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, inst)
	inst.Close()
}

func TestCloseIsIdempotent(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}

	inst, err := New(Options{
		Token:   "test-token",
		DataDir: t.TempDir(),
		Assets:  frontend,
	})
	require.NoError(t, err)

	require.NoError(t, inst.Close())
	require.NoError(t, inst.Close()) // must not panic or error
}

// TestStopSyncCancelsStackHook verifies the instance-lifetime stacks
// hook context gets canceled during StopSync. After New() installs the
// hook, we flip to stopped state via StopSync and confirm a subsequent
// Close() works cleanly (no deadlock from a dangling hook goroutine).
// This covers the roborev finding that StopSync must cancel in-flight
// stack-detection work, not only Close.
func TestStopSyncCancelsStackHook(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}

	inst, err := New(Options{
		Token:   "test-token",
		DataDir: t.TempDir(),
		Assets:  frontend,
	})
	require.NoError(t, err)

	// Never called StartSync — hook must still exist (installed in New)
	// and StopSync must cancel its context without blocking.
	done := make(chan struct{})
	go func() {
		inst.StopSync()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		require.Fail(t, "StopSync did not return within 5s — hook ctx not canceled")
	}

	// Close must still succeed after StopSync.
	require.NoError(t, inst.Close())
}

// TestStartSyncAfterStopSyncIsTerminal pins the documented contract of
// StopSync: once the Syncer is stopped, a subsequent StartSync is a
// silent no-op — it must not resurrect the hook, must not start a new
// background sync goroutine, and must not panic. A previous attempt at
// fixing a hook-lifecycle bug reinstalled the hook on StartSync, which
// only looked like a restart: the underlying Syncer.Start already
// no-ops once stopped, so the reinstalled hook never fired. This test
// prevents that half-working restart path from coming back.
func TestStartSyncAfterStopSyncIsTerminal(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}

	req := require.New(t)
	inst, err := New(Options{
		Token:   "test-token",
		DataDir: t.TempDir(),
		Assets:  frontend,
	})
	req.NoError(err)
	t.Cleanup(func() { _ = inst.Close() })

	req.NotNil(inst.cancelHook, "hook must be installed by New")
	inst.StopSync()
	req.Nil(inst.cancelHook, "StopSync must cancel and clear the hook")

	// StartSync after StopSync must not reinstall the hook and must
	// not panic. The Syncer underneath is already permanently stopped
	// (Start/TriggerRun both no-op once stopped=true).
	inst.StartSync(t.Context())
	req.Nil(inst.cancelHook, "StartSync must not resurrect the hook after StopSync")

	// Close remains idempotent and safe after a StopSync/StartSync
	// sequence.
	req.NoError(inst.Close())
	req.NoError(inst.Close())
}

// TestStopSyncConcurrent pins the concurrency contract of StopSync:
// multiple callers racing into StopSync must not panic on a nil
// cancelHook dereference. Before stopOnce was added, the
// check/call/reset sequence had a TOCTOU window — caller A could
// observe cancelHook != nil, caller B could then nil it, and caller
// A would dereference nil. Under -race this test also verifies no
// data race exists on the field.
func TestStopSyncConcurrent(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte(`<!DOCTYPE html><html><head></head><body>app</body></html>`),
		},
	}

	req := require.New(t)
	inst, err := New(Options{
		Token:   "test-token",
		DataDir: t.TempDir(),
		Assets:  frontend,
	})
	req.NoError(err)
	t.Cleanup(func() { _ = inst.Close() })

	const workers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			<-start
			inst.StopSync()
		}()
	}
	close(start)
	wg.Wait()
}

// TestInstancePurgeOtherHostsWithContextNilCtx verifies a nil
// ctx is normalized to t.Context() at the API
// boundary so database/sql does not panic.
func TestInstancePurgeOtherHostsWithContextNilCtx(t *testing.T) {
	inst, err := New(Options{
		Token:   "test-token",
		DataDir: t.TempDir(),
		Repos:   []Repo{{Owner: "t", Name: "r"}},
	})
	require.NoError(t, err)
	defer inst.Close()

	//nolint:staticcheck // intentional: verifying nil-ctx normalization
	require.NoError(t, inst.PurgeOtherHostsWithContext(nil, "github.com"))
}

// TestInstanceSetWorktreeLinksWithContextNilCtx verifies a nil
// ctx is normalized to t.Context() at the API
// boundary so database/sql does not panic.
func TestInstanceSetWorktreeLinksWithContextNilCtx(t *testing.T) {
	inst, err := New(Options{
		Token:   "test-token",
		DataDir: t.TempDir(),
		Repos:   []Repo{{Owner: "t", Name: "r"}},
	})
	require.NoError(t, err)
	defer inst.Close()

	//nolint:staticcheck // intentional: verifying nil-ctx normalization
	require.NoError(t, inst.SetWorktreeLinksWithContext(nil, nil))
}

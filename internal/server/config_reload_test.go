package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"

	ghclient "go.kenn.io/forge/internal/github"

	ptyownerruntime "go.kenn.io/forge/internal/ptyowner/runtime"
	"go.kenn.io/forge/internal/ptysize"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/configreload"
	"go.kenn.io/forge/internal/server/settingsapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.kenn.io/forge/platform"
)

type reloadArchiveLifecycleRecorder struct {
	ensured []platform.RepoRef
	retried []platform.RepoRef
}

func (*reloadArchiveLifecycleRecorder) RunPass(context.Context) (bool, error) { return false, nil }

func (r *reloadArchiveLifecycleRecorder) EnsureConfigured(_ context.Context, refs []platform.RepoRef) ([]platform.RepoRef, error) {
	r.ensured = append([]platform.RepoRef(nil), refs...)
	return refs, nil
}

func (r *reloadArchiveLifecycleRecorder) RetryAuthentication(_ context.Context, refs []platform.RepoRef) error {
	r.retried = append([]platform.RepoRef(nil), refs...)
	return nil
}

// waitForConfigWatcher blocks until the server's config watcher has
// registered the directory with fsnotify, or the timeout elapses. Tests
// that mutate the config file must call this first; otherwise an
// fsnotify race can drop the event and the test will hang.
func waitForConfigWatcher(t *testing.T, srv *Server, timeout time.Duration) {
	t.Helper()
	require.NotNil(t, srv.configWatcher, "server has no config watcher")
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	require.NoError(t, srv.configWatcher.WaitReady(ctx))
}

func writeConfigToml(t *testing.T, path string, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func atomicRenameConfigToml(t *testing.T, path string, content string) {
	t.Helper()
	dir := filepath.Dir(path)
	tmp := filepath.Join(dir, ".config-watcher.tmp")
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o644))
	require.NoError(t, os.Rename(tmp, path))
}

// configEventStream wraps a live SSE HTTP connection and yields
// config.changed events on a channel. Callers must call Close to stop
// the goroutine; the channel is closed when the stream ends.
type configEventStream struct {
	resp   *http.Response
	cancel context.CancelFunc
	events chan configreload.ConfigChangedEvent
}

func (s *configEventStream) Close() {
	s.cancel()
	_ = s.resp.Body.Close()
}

// streamConfigEvents subscribes to /api/v1/events via a real httptest
// server and forwards every config.changed event onto the returned
// channel. The goroutine drains the SSE stream until the test context
// (or the explicit cancel) fires.
func streamConfigEvents(t *testing.T, srv *Server) *configEventStream {
	t.Helper()
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet, ts.URL+"/api/v1/events", http.NoBody,
	)
	require.NoError(t, err)
	setAcceptedHostForServerTest(req, srv)

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	})

	stream := &configEventStream{
		resp:   resp,
		cancel: cancel,
		events: make(chan configreload.ConfigChangedEvent, 8),
	}

	// Wait for the handler to register before returning, so the test
	// does not race the watcher's first event against subscriber setup.
	require.Eventually(t, func() bool {
		srv.hub.Mu.Lock()
		defer srv.hub.Mu.Unlock()
		return len(srv.hub.Subscribers) >= 1
	}, 2*time.Second, 10*time.Millisecond)

	go func() {
		defer close(stream.events)
		scanner := bufio.NewScanner(resp.Body)
		// SSE frames can contain newlines inside the data: line in
		// theory; in practice this server marshals JSON to a single
		// line so a default bufio.Scanner is enough.
		buf := make([]byte, 0, 1024)
		scanner.Buffer(buf, 1024*1024)
		var eventType, dataLine string
		for scanner.Scan() {
			line := scanner.Text()
			if rest, ok := strings.CutPrefix(line, "event: "); ok {
				eventType = rest
				continue
			}
			if rest, ok := strings.CutPrefix(line, "data: "); ok {
				dataLine = rest
				continue
			}
			if line != "" {
				continue
			}
			if eventType == "config.changed" && dataLine != "" {
				var ev configreload.ConfigChangedEvent
				if err := json.Unmarshal([]byte(dataLine), &ev); err == nil {
					select {
					case stream.events <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
			eventType, dataLine = "", ""
		}
	}()

	return stream
}

func waitForConfigEvent(
	t *testing.T,
	stream *configEventStream,
	timeout time.Duration,
) configreload.ConfigChangedEvent {
	t.Helper()
	select {
	case ev, ok := <-stream.events:
		require.True(t, ok, "events channel closed before an event arrived")
		return ev
	case <-time.After(timeout):
		require.FailNow(t, "timed out waiting for config.changed event")
		return configreload.ConfigChangedEvent{}
	}
}

const validReloadConfig = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`

const validReloadConfigExtraRepo = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
owner = "globex"
name = "engine"
`

const validReloadConfigRepoTokenEnv = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
token_env = "KENN_FORGE_REPO_TOKEN"
`

const validReloadConfigChangedGitHubTokenEnv = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_NEW_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`

const validReloadConfigPlatformTokenEnv = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[platforms]]
type = "github"
host = "github.com"
token_env = "KENN_FORGE_PLATFORM_TOKEN"

[[repos]]
owner = "acme"
name = "widget"
`

const validReloadConfigPlatformAndRepoTokenEnv = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[platforms]]
type = "github"
host = "github.com"
token_env = "KENN_FORGE_PLATFORM_TOKEN"

[[repos]]
owner = "acme"
name = "widget"
token_env = "KENN_FORGE_REPO_TOKEN"
`

const validReloadConfigGlobRepo = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget-*"
`

const validReloadConfigExactPlusGlob = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
owner = "acme"
name = "*"
`

const validReloadConfigExactPlusGlobChangedActivity = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
owner = "acme"
name = "*"

[activity]
view_mode = "flat"
time_range = "30d"
`

const validReloadConfigChangedActivity = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[activity]
view_mode = "flat"
time_range = "30d"
`

const validReloadConfigChangedBranchActivityLimits = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[activity]
default_branch_retention_days = 14
default_branch_max_commits = 2
`

const validReloadConfigChangedModes = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[modes]
docs = true
workspaces = false
`

const validReloadConfigRestartRequired = `
sync_interval = "10m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`

const validReloadConfigHostCheckPolicy = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091
allowed_hosts = ["forge.example"]
trust_reverse_proxy = true

[[repos]]
owner = "acme"
name = "widget"
`

const invalidReloadConfig = `
sync_interval = "5m"
host = "not-an-ip"
port = 8091
`

const malformedTomlConfig = `
sync_interval = "5m
host = "127.0.0.1"
`

func validReloadConfigWithDocFolder(id, name, root string) string {
	return validReloadConfig + fmt.Sprintf(`
[[doc_folders]]
id = %q
name = %q
path = %q
`, id, name, root)
}

func TestConfigReload_WatcherFiresOnInPlaceEdit(t *testing.T) {
	assert := assert.New(t)

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigChangedActivity)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid, "expected valid reload")
	assert.Empty(ev.Error)
	assert.False(ev.RestartRequired)

	srv.cfgMu.Lock()
	gotActivity := srv.cfg.Activity
	srv.cfgMu.Unlock()
	assert.Equal("flat", gotActivity.ViewMode)
	assert.Equal("30d", gotActivity.TimeRange)
}

func TestConfigReloadAppliesMouseToDedicatedTmuxServer(t *testing.T) {
	require := require.New(t)
	record := installSettingsTmuxRecorder(t)
	srv, _, cfgPath, _ := setupTestServerWithConfigContentAndOptions(t, validReloadConfig, &serverfake.MockGH{}, ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
		WorktreeDir:                   t.TempDir(),
	})
	require.NoError(os.WriteFile(record, nil, 0o600))
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfig+`
[terminal]
tmux_mouse = false
`)

	event := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(event.Valid, "reload error: %s", event.Error)
	assert.Equal(t, []string{
		"-L kenn-forge list-sessions -F #{session_name}:#{@forge_owner}",
		"-L kenn-forge set-option -q -g mouse off",
	}, readSettingsTmuxMouseCommands(t, record))
}

func TestConfigReloadAppliesGraphicsToDedicatedTmuxServer(t *testing.T) {
	require := require.New(t)
	record := installSettingsTmuxRecorder(t)
	srv, _, cfgPath, _ := setupTestServerWithConfigContentAndOptions(t, validReloadConfig, &serverfake.MockGH{}, ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
		WorktreeDir:                   t.TempDir(),
	})
	require.NoError(os.WriteFile(record, nil, 0o600))
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfig+`
[terminal]
graphics = false
`)

	event := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(event.Valid, "reload error: %s", event.Error)
	assert.Equal(t, []string{
		"-L kenn-forge set-option -q -g allow-passthrough off",
		"-L kenn-forge set-option -q -s -u terminal-features[100]",
		"-L kenn-forge set-option -q -p -u -t pane-A allow-passthrough",
		"-L kenn-forge set-option -q -p -u -t pane-B allow-passthrough",
	}, readSettingsTmuxGraphicsCommands(t, record))
}

func TestConfigReloadPublishesPullConfigOnlyAfterSuccessfulReload(t *testing.T) {
	require := require.New(t)
	srv, _, _, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	require.False(srv.pullAPI.ConfigSnapshot().AllowMidStackMerges)
	require.False(srv.pullAPI.ConfigSnapshot().UseWorkspaceActivityForRecency)
	require.False(srv.issueAPI.ConfigSnapshot().UseWorkspaceActivityForRecency)

	reloadPath := filepath.Join(t.TempDir(), "reload.toml")
	srv.cfgPath = reloadPath
	writeConfigToml(t, reloadPath, validReloadConfig+`
[pull_requests]
allow_mid_stack_merges = true

[activity]
use_workspace_activity_for_recency = true
`)
	event := srv.configreload.ApplyConfigChange(t.Context())
	require.True(event.Valid, event.Error)
	require.True(srv.pullAPI.ConfigSnapshot().AllowMidStackMerges)
	require.True(srv.pullAPI.ConfigSnapshot().UseWorkspaceActivityForRecency)
	require.True(srv.issueAPI.ConfigSnapshot().UseWorkspaceActivityForRecency)

	writeConfigToml(t, reloadPath, malformedTomlConfig)
	event = srv.configreload.ApplyConfigChange(t.Context())
	require.False(event.Valid)
	require.True(
		srv.pullAPI.ConfigSnapshot().AllowMidStackMerges,
		"failed reload published an invalid Pull config",
	)
	require.True(srv.pullAPI.ConfigSnapshot().UseWorkspaceActivityForRecency)
	require.True(srv.issueAPI.ConfigSnapshot().UseWorkspaceActivityForRecency)
}

// A server constructed without a syncer (Server.New permits nil; embedded
// and docs-only setups use it) must hot-reload non-sync surfaces
// instead of panicking in the watcher goroutine. Regression test for a nil
// TrackedRepos dereference that crashed the whole test binary in CI.
func TestConfigReload_NilSyncerAppliesHotReloadWithoutPanic(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	writeConfigToml(t, cfgPath, validReloadConfig)
	cfg, err := config.Load(cfgPath)
	require.NoError(err)

	srv := NewWithConfig(
		serverfake.OpenTestDB(t), nil, nil, nil, cfg, cfgPath, ServerOptions{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigChangedActivity)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid, "expected valid reload on a syncer-less server")
	assert.Empty(ev.Error)
	assert.False(ev.RestartRequired)

	srv.cfgMu.Lock()
	gotActivity := srv.cfg.Activity
	srv.cfgMu.Unlock()
	assert.Equal("flat", gotActivity.ViewMode)
	assert.Equal("30d", gotActivity.TimeRange)
}

func TestConfigReloadPreservesCanonicalDataDirIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	root := t.TempDir()
	realDir := filepath.Join(root, "state")
	require.NoError(os.Mkdir(realDir, 0o700))
	link := filepath.Join(root, "state-link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	content := fmt.Sprintf("data_dir = %q\n", link) + validReloadConfig
	srv, _, cfgPath, _ := setupTestServerWithConfigContent(t, content, &serverfake.MockGH{})
	canonicalDir, err := filepath.EvalSymlinks(realDir)
	require.NoError(err)

	writeConfigToml(t, cfgPath, content)
	event := srv.configreload.ApplyConfigChange(t.Context())

	require.True(event.Valid, event.Error)
	assert.False(event.RestartRequired)
	srv.cfgMu.Lock()
	assert.Equal(canonicalDir, srv.cfg.DataDir)
	srv.cfgMu.Unlock()
}

func TestConfigReload_UpdatesBranchActivityLimits(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, _, cfgPath, syncer := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigChangedBranchActivityLimits)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)
	assert.False(ev.RestartRequired)

	retention, maxCommits := syncer.BranchActivityLimits()
	assert.Equal(14*24*time.Hour, retention)
	assert.Equal(2, maxCommits)
}

func TestConfigReload_UpdatesModes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigChangedModes)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)
	assert.False(ev.RestartRequired)

	srv.cfgMu.Lock()
	gotModes := spokeapi.CloneModeVisibility(srv.cfg.Modes)
	originalActions := srv.cfg.Modes.Actions
	srv.cfgMu.Unlock()
	assert.True(*gotModes.Docs)
	assert.False(*gotModes.Workspaces)
	assert.True(*gotModes.Activity)
	assert.True(*gotModes.Repos)
	assert.True(*gotModes.Pulls)
	assert.True(*gotModes.Issues)
	assert.False(*gotModes.Actions)

	*gotModes.Actions = true
	assert.False(*originalActions, "cloned Actions pointer must not alias live config")
}

func TestConfigReload_UpdatesDocFoldersAndRegistry(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	initialRoot := t.TempDir()
	updatedRoot := t.TempDir()
	require.NoError(os.WriteFile(filepath.Join(initialRoot, "old.md"), []byte("old\n"), 0o644))
	require.NoError(os.WriteFile(filepath.Join(updatedRoot, "guide.md"), []byte("# Guide\n"), 0o644))
	initialConfig := validReloadConfigWithDocFolder("notes", "Notes", initialRoot)
	updatedConfig := validReloadConfigWithDocFolder("handbook", "Handbook", updatedRoot)

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, initialConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, updatedConfig)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)
	assert.False(ev.RestartRequired)

	srv.cfgMu.Lock()
	gotCfgFolders := append([]config.DocFolder(nil), srv.cfg.DocFolders...)
	srv.cfgMu.Unlock()
	require.Len(gotCfgFolders, 1)
	assert.Equal("handbook", gotCfgFolders[0].ID)
	assert.Equal("Handbook", gotCfgFolders[0].Name)
	assert.Equal(updatedRoot, gotCfgFolders[0].Path)

	gotRegistryFolders := srv.docsAPI.Folders()
	require.Len(gotRegistryFolders, 1)
	assert.Equal("handbook", gotRegistryFolders[0].ID)
	assert.Equal("Handbook", gotRegistryFolders[0].Name)
	wantRegistryRoot, err := filepath.EvalSymlinks(updatedRoot)
	require.NoError(err)
	assert.Equal(wantRegistryRoot, gotRegistryFolders[0].Path)
	httpServer := httptest.NewServer(srv)
	t.Cleanup(httpServer.Close)
	listResponseReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, httpServer.URL+"/api/v1/docs/folders", nil)
	require.NoError(err)
	httpClient := httpServer.Client()
	httpClient.Timeout = 5 * time.Second
	listResponse, err := httpClient.Do(listResponseReq)
	require.NoError(err)
	t.Cleanup(func() { listResponse.Body.Close() })
	require.Equal(http.StatusOK, listResponse.StatusCode)
	var listBody generated.ListDocsFoldersOutputBody
	require.NoError(json.NewDecoder(listResponse.Body).Decode(&listBody))
	require.NotNil(listBody.Folders)
	require.Len(listBody.Folders, 1)
	assert.Equal("handbook", listBody.Folders[0].ID)
	assert.Equal("Handbook", listBody.Folders[0].Name)

	updatedReadResponseReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, httpServer.URL+"/api/v1/docs/folders/handbook/file?path=guide.md", nil)
	require.NoError(err)
	httpClient = httpServer.Client()
	httpClient.Timeout = 5 * time.Second
	updatedReadResponse, err := httpClient.Do(updatedReadResponseReq)
	require.NoError(err)
	t.Cleanup(func() { updatedReadResponse.Body.Close() })
	require.Equal(http.StatusOK, updatedReadResponse.StatusCode)
	var readBody generated.DocsReadFileOutputBody
	require.NoError(json.NewDecoder(updatedReadResponse.Body).Decode(&readBody))
	assert.Equal("# Guide\n", readBody.Content)

	oldReadResponseReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, httpServer.URL+"/api/v1/docs/folders/notes/file?path=old.md", nil)
	require.NoError(err)
	httpClient = httpServer.Client()
	httpClient.Timeout = 5 * time.Second
	oldReadResponse, err := httpClient.Do(oldReadResponseReq)
	require.NoError(err)
	t.Cleanup(func() { oldReadResponse.Body.Close() })
	assert.Equal(http.StatusNotFound, oldReadResponse.StatusCode)
}

func TestConfigReloadSerializesDocsFolderMutation(t *testing.T) {
	require := require.New(t)
	initialRoot := t.TempDir()
	reloadedRoot := t.TempDir()
	createdRoot := t.TempDir()
	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t,
		validReloadConfigWithDocFolder("initial", "Initial", initialRoot),
		&serverfake.MockGH{},
	)
	writeConfigToml(t, cfgPath, validReloadConfigWithDocFolder("reloaded", "Reloaded", reloadedRoot))

	start := make(chan struct{})
	var wg sync.WaitGroup
	var mutation *httptest.ResponseRecorder
	mutationBody := strings.NewReader(fmt.Sprintf(
		`{"id":"created","name":"Created","path":%q}`,
		createdRoot,
	))
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		srv.configreload.HandleConfigFileChanged()
	}()
	go func() {
		defer wg.Done()
		<-start
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/docs/folders", mutationBody)
		setAcceptedHostForServerTest(req, srv)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Content-Type", "application/json")
		mutation = httptest.NewRecorder()
		srv.ServeHTTP(mutation, req)
	}()
	close(start)
	wg.Wait()
	require.NotNil(mutation)
	require.Equal(http.StatusCreated, mutation.Code, mutation.Body.String())

	disk, err := config.Load(cfgPath)
	require.NoError(err)
	srv.cfgMu.Lock()
	inMemory := slices.Clone(srv.cfg.DocFolders)
	srv.cfgMu.Unlock()
	registry := srv.docsAPI.Folders()
	assert.Equal(t, disk.DocFolders, inMemory)
	assert.Equal(t, disk.DocFolders, registry)
}

func TestConfigReload_WatcherFiresOnAtomicRename(t *testing.T) {
	assert := assert.New(t)

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	atomicRenameConfigToml(t, cfgPath, validReloadConfigChangedActivity)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid)
	assert.False(ev.RestartRequired)
}

func TestConfigReload_RestartRequiredOnStartupFieldChange(t *testing.T) {
	assert := assert.New(t)

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigRestartRequired)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid)
	assert.True(ev.RestartRequired, "sync_interval change should mark restart_required")
}

func TestConfigReload_RestartRequiredOnHostCheckPolicyChange(t *testing.T) {
	assert := assert.New(t)

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigHostCheckPolicy)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid)
	assert.True(ev.RestartRequired, "host-check policy change should mark restart_required")
}

func TestConfigReload_TokenSourceChangeForExistingHostUpdatesSource(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "old")
	t.Setenv("KENN_FORGE_REPO_TOKEN", "new")

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	sourceSet := tokenauth.NewSourceSet(tokenauth.Options{})
	srv.cfgMu.Lock()
	desc := srv.cfg.ResolveRepoTokenSource(srv.cfg.Repos[0])
	srv.cfgMu.Unlock()
	src := sourceSet.Upsert(desc)
	srv.tokenSources = sourceSet
	oldToken, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("old", oldToken)

	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigRepoTokenEnv)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid)
	assert.True(ev.RestartRequired,
		"adding an exact-repository route requires rebuilding the client pool")
	currentToken, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("old", currentToken)
}

func TestConfigReload_GitHubTokenEnvChangeUpdatesConfigSnapshot(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "old")
	t.Setenv("KENN_FORGE_NEW_GITHUB_TOKEN", "new")

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	sourceSet := tokenauth.NewSourceSet(tokenauth.Options{})
	srv.cfgMu.Lock()
	desc := srv.cfg.ResolveRepoTokenSource(srv.cfg.Repos[0])
	srv.cfgMu.Unlock()
	src := sourceSet.Upsert(desc)
	srv.tokenSources = sourceSet
	oldToken, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("old", oldToken)

	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigChangedGitHubTokenEnv)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid)
	assert.True(ev.RestartRequired,
		"changing a bounded GitHub route descriptor requires identity re-resolution")
	newToken, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("old", newToken)

	srv.cfgMu.Lock()
	currentTokenEnv := srv.cfg.GitHubTokenEnv
	savePath := filepath.Join(t.TempDir(), "saved.toml")
	saveErr := srv.cfg.Save(savePath)
	srv.cfgMu.Unlock()
	require.NoError(saveErr)
	assert.Equal("KENN_FORGE_NEW_GITHUB_TOKEN", currentTokenEnv)

	saved, err := config.Load(savePath)
	require.NoError(err)
	assert.Equal("KENN_FORGE_NEW_GITHUB_TOKEN", saved.GitHubTokenEnv)
}

func TestConfigReloadPublishesCommittedWorkspaceSnapshot(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, cfgPath, _ := setupTestServerWithConfigContent(t, `
host = "127.0.0.1"
port = 8091

[[agents]]
key = "before"
command = ["sh"]
`, &serverfake.MockGH{})
	project, err := database.CreateProject(t.Context(), db.CreateProjectInput{
		DisplayName: "Workspace config snapshot",
		LocalPath:   t.TempDir(),
	})
	require.NoError(err)

	require.NoError(os.WriteFile(cfgPath, []byte(`
host = "127.0.0.1"
port = 8091

[[agents]]
key = "after"
command = ["sh"]
`), 0o644))
	event := srv.configreload.ApplyConfigChange(t.Context())
	require.True(event.Valid, event.Error)

	rr := testutil.DoJSON(
		t, srv, http.MethodGet,
		"/api/v1/projects/"+project.ID+"/launch-targets", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body generated.ListLaunchTargetsOutputBody
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.NotNil(body.LaunchTargets)
	keys := make([]string, 0, len(body.LaunchTargets))
	for _, target := range body.LaunchTargets {
		keys = append(keys, target.Key)
	}
	assert.Contains(keys, "after")
	assert.NotContains(keys, "before")
}

func TestConfigReload_InvalidTokenSourceKeepsLastKnownGoodSource(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "")
	t.Setenv("KENN_FORGE_REPO_TOKEN", "old")

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfigRepoTokenEnv, &serverfake.MockGH{},
	)
	sourceSet := tokenauth.NewSourceSet(tokenauth.Options{})
	srv.cfgMu.Lock()
	desc := srv.cfg.ResolveRepoTokenSource(srv.cfg.Repos[0])
	srv.cfgMu.Unlock()
	src := sourceSet.Upsert(desc)
	srv.tokenSources = sourceSet
	oldToken, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("old", oldToken)

	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
token_env = "KENN_FORGE_MISSING_REPO_TOKEN"
`)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.False(ev.Valid)
	assert.NotEmpty(ev.Error)

	currentToken, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("old", currentToken)

	srv.cfgMu.Lock()
	currentTokenEnv := srv.cfg.Repos[0].TokenEnv
	srv.cfgMu.Unlock()
	assert.Equal("KENN_FORGE_REPO_TOKEN", currentTokenEnv)
}

func TestConfigReload_AirplaneModeOmitsUnresolvedPinnedRepository(t *testing.T) {
	require := require.New(t)
	initialConfig := "airplane_mode = true\n" + validReloadConfig +
		"platform_repo_id = \"repo-acme-widget\"\n"
	srv, _, cfgPath, syncer := setupTestServerWithConfigContentAndOptions(
		t, initialConfig, &serverfake.MockGH{}, ServerOptions{
			HostCheckAllowLoopbackAnyPort:      true,
			WorktreeDir:                        t.TempDir(),
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	previous := syncer.TrackedRepos()
	require.Len(previous, 1)
	require.Equal("repo-acme-widget", previous[0].PlatformExternalID)
	previous[0].Name = "widget-next"
	previous[0].RepoPath = "acme/widget-next"
	previous[0].ConfiguredRepoPath = ""
	syncer.SetRepos(previous)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, initialConfig+`
[[repos]]
owner = "acme"
name = "replacement"
platform_repo_id = "R_pinned"
`)
	event := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(event.Valid, event.Error)
	srv.cfgMu.Lock()
	configured := slices.Clone(srv.cfg.Repos)
	srv.cfgMu.Unlock()
	require.Len(configured, 2)
	require.Equal("R_pinned", configured[1].PlatformRepoID)
	previous[0].ConfiguredRepoPath = "acme/widget"
	assert.Equal(t, previous, syncer.TrackedRepos(),
		"keep the verified repository without adopting the unresolved pinned route")
	_, err := srv.settingsapi.DeleteConfiguredRepo(t.Context(), &settingsapi.RepoConfigInput{
		Provider: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "replacement",
	})
	require.NoError(err)
	event = waitForConfigEvent(t, stream, 2*time.Second)
	require.True(event.Valid, event.Error)
	require.Equal(previous, syncer.TrackedRepos(),
		"removing another entry must keep the renamed pinned repository")
}

func TestConfigReload_PreservesCachedReposForProviderMissingAtStartup(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "github-token")
	t.Setenv("KENN_FORGE_FAILED_GITLAB_TOKEN", "")
	const failedProvider = `
[[platforms]]
type = "gitlab"
host = "gitlab.example.com"
token_env = "KENN_FORGE_FAILED_GITLAB_TOKEN"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.example.com"
owner = "acme"
name = "backend"
platform_repo_id = "gid://gitlab/Project/42"

[[repos]]
platform = "gitlab"
platform_host = "gitlab.example.com"
owner = "acme"
name = "service-*"
`

	srv, database, cfgPath, syncer := setupTestServerWithConfigContent(
		t, validReloadConfig+failedProvider, &serverfake.MockGH{},
	)
	set := tokenauth.NewSourceSet(tokenauth.Options{})
	for _, plan := range srv.cfg.ProviderTokenSources() {
		set.Upsert(plan.Descriptor)
	}
	srv.tokenSources = set
	startupFallbacks := []ghclient.RepoRef{
		{
			Platform: platform.KindGitLab, PlatformHost: "gitlab.example.com",
			Owner: "acme", Name: "backend", RepoPath: "acme/backend",
			PlatformExternalID: "gid://gitlab/Project/42",
		},
		{
			Platform: platform.KindGitLab, PlatformHost: "gitlab.example.com",
			Owner: "acme", Name: "service-api", RepoPath: "acme/service-api",
		},
	}
	syncer.SetRepos(append(syncer.TrackedRepos(), startupFallbacks...))
	for i, repo := range startupFallbacks {
		serverfake.SeedVerifiedRepo(t, database, db.RepoIdentity{
			Platform: string(repo.Platform), PlatformHost: repo.PlatformHost,
			PlatformRepoID: fmt.Sprintf("gid://gitlab/Project/%d", 42+i),
			Owner:          repo.Owner, Name: repo.Name, RepoPath: repo.RepoPath,
		})
	}
	assert.ElementsMatch([]string{"backend", "service-api"}, listRepoNames(t, srv))

	writeConfigToml(t, cfgPath, validReloadConfigChangedActivity+failedProvider)
	event := srv.configreload.ApplyConfigChange(t.Context())
	require.True(event.Valid, "unrelated reload failed: %s", event.Error)
	assert.True(event.RestartRequired)
	tracked := syncer.TrackedRepos()
	startupFallbacks[0].ConfiguredRepoPath = "acme/backend"
	for _, repo := range startupFallbacks {
		assert.Contains(tracked, repo)
	}
	assert.ElementsMatch([]string{"backend", "service-api"}, listRepoNames(t, srv))
}

// reloadTestTokenSources registers every provider token plan of the config
// at cfgPath into a fresh SourceSet, mirroring startup registration, and
// returns the set plus the source for the given key. Hosts whose plans
// resolve a token also get the host-level clone source under
// tokenauth.CloneKey, as buildProviderStartup registers at boot.
func reloadTestTokenSources(
	t *testing.T,
	cfgPath string,
	key tokenauth.Key,
) (*tokenauth.SourceSet, tokenauth.Source) {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	sourceSet := tokenauth.NewSourceSet(tokenauth.Options{})
	resolvedHosts := make(map[string]struct{})
	for _, plan := range cfg.ProviderTokenSources() {
		src := sourceSet.Upsert(plan.Descriptor)
		if _, err := src.Token(t.Context()); err == nil {
			resolvedHosts[plan.Descriptor.Key.Host] = struct{}{}
		}
	}
	for _, desc := range cfg.CloneTokenDescriptors() {
		if _, ok := resolvedHosts[desc.Key.Host]; !ok {
			continue
		}
		sourceSet.Upsert(desc)
	}
	src, ok := sourceSet.Get(key)
	require.True(t, ok, "no source registered for %v", key)
	return sourceSet, src
}

func TestConfigReload_RemovingGitHubOwnerTokenClearsLiveRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	t.Setenv("OWNER_PAT", "owner-token")
	withOwner := `
sync_interval = "5m"
host = "127.0.0.1"
port = 8091

[[github_owner_tokens]]
owner = "acme"
token_env = "OWNER_PAT"
`
	withoutOwner := `
sync_interval = "5m"
host = "127.0.0.1"
port = 8091
`
	srv, _, cfgPath, _ := setupTestServerWithConfigContent(t, withOwner, &serverfake.MockGH{})
	key := tokenauth.Key{
		Platform: "github", Host: "github.com", Scope: "owner:acme",
	}
	sourceSet, src := reloadTestTokenSources(t, cfgPath, key)
	srv.tokenSources = sourceSet
	bootCfg, err := config.Load(cfgPath)
	require.NoError(err)
	srv.bootCfgSnapshot = configreload.SnapshotStartupConfig(bootCfg)
	token, err := src.Token(t.Context())
	require.NoError(err)
	require.Equal("owner-token", token)

	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()
	writeConfigToml(t, cfgPath, withoutOwner)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid, "reload error: %s", ev.Error)
	assert.True(ev.RestartRequired, "removing a bounded route requires restart")
	token, err = src.Token(t.Context())
	require.NoError(err)
	assert.Equal("owner-token", token,
		"the live bounded router keeps its boot credential until restart")
}

func TestConfigReload_ChangingGitHubOwnerSourceFreezesBootRoute(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	t.Setenv("OWNER_PAT", "owner-token")
	t.Setenv("NEW_OWNER_PAT", "new-owner-token")
	bootConfig := `
sync_interval = "5m"
host = "127.0.0.1"
port = 8091

[[github_owner_tokens]]
owner = "acme"
token_env = "OWNER_PAT"
`
	changedConfig := strings.ReplaceAll(bootConfig, "OWNER_PAT", "NEW_OWNER_PAT")
	srv, _, cfgPath, _ := setupTestServerWithConfigContent(t, bootConfig, &serverfake.MockGH{})
	key := tokenauth.Key{
		Platform: "github", Host: "github.com", Scope: "owner:acme",
	}
	sourceSet, src := reloadTestTokenSources(t, cfgPath, key)
	srv.tokenSources = sourceSet
	bootCfg, err := config.Load(cfgPath)
	require.NoError(err)
	srv.bootCfgSnapshot = configreload.SnapshotStartupConfig(bootCfg)

	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()
	writeConfigToml(t, cfgPath, changedConfig)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid, "reload error: %s", ev.Error)
	assert.True(ev.RestartRequired)
	token, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("owner-token", token,
		"identity-changing descriptors remain frozen until restart")
}

const reloadPlatformTokenConfig = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[platforms]]
type = "gitlab"
host = "gitlab.example.com"
token_env = "KENN_FORGE_PLATFORM_TOKEN"

[[repos]]
owner = "acme"
name = "widget"
`

const reloadPlatformTokenlessConfig = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[platforms]]
type = "gitlab"
host = "gitlab.example.com"

[[repos]]
owner = "acme"
name = "widget"
`

func TestConfigReload_RemovingPlatformTokenClearsLiveSource(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "github-token")
	t.Setenv("KENN_FORGE_PLATFORM_TOKEN", "platform-token")

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, reloadPlatformTokenConfig, &serverfake.MockGH{},
	)
	sourceSet, src := reloadTestTokenSources(t, cfgPath, tokenauth.Key{
		Platform: "gitlab", Host: "gitlab.example.com",
	})
	srv.tokenSources = sourceSet
	bootToken, err := src.Token(t.Context())
	require.NoError(err)
	require.Equal("platform-token", bootToken)

	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, reloadPlatformTokenlessConfig)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid, "reload error: %s", ev.Error)
	assert.False(ev.RestartRequired)
	// The removal is hot-applied: the live source no longer resolves the
	// credential that was deleted from the config file.
	_, err = src.Token(t.Context())
	require.ErrorIs(err, tokenauth.ErrMissingToken)
}

func TestConfigReload_TokenAddedForUnbuiltClientRequiresRestart(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "github-token")
	t.Setenv("KENN_FORGE_PLATFORM_TOKEN", "platform-token")

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, reloadPlatformTokenlessConfig, &serverfake.MockGH{},
	)
	sourceSet, src := reloadTestTokenSources(t, cfgPath, tokenauth.Key{
		Platform: "gitlab", Host: "gitlab.example.com",
	})
	srv.tokenSources = sourceSet
	_, err := src.Token(t.Context())
	require.ErrorIs(err, tokenauth.ErrMissingToken)

	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, reloadPlatformTokenConfig)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid, "reload error: %s", ev.Error)
	// The token now resolves, but the gitlab host booted without a
	// provider client and the reload cannot construct one — the event
	// must say a restart is needed rather than report a clean hot apply.
	assert.True(ev.RestartRequired)
	newToken, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("platform-token", newToken)
}

func TestConfigReload_GitHubAppAddedRequiresRestart(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "github-token")

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	// An app appearing for a host changes the split-credential
	// topology: write trackers and the write client chain are wired at
	// startup, so the reload must demand a restart instead of leaving
	// mutation availability gating on the wrong bucket.
	keyPath := filepath.Join(filepath.Dir(cfgPath), "app.pem")
	require.NoError(os.WriteFile(keyPath, []byte("pem"), 0o600))
	writeConfigToml(t, cfgPath, validReloadConfig+`
[[github_apps]]
host = "github.com"
app_id = 4242
private_key_path = "app.pem"
installation_id = 7
installation_account = "acme"
repository_selection = "all"
`)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid, "reload error: %s", ev.Error)
	assert.True(ev.RestartRequired,
		"github app split topology is startup-bound and must flag a restart")

	// The in-memory config must mirror the file even though the new
	// topology only takes effect after restart: the github-app CLI
	// edits the file while the server runs, and a settings save from
	// a stale view would silently drop the [[github_apps]] entry.
	srv.cfgMu.Lock()
	apps := slices.Clone(srv.cfg.GitHubApps)
	srv.cfgMu.Unlock()
	require.Len(apps, 1)
	assert.Equal(int64(4242), apps[0].AppID)
}

func TestValidateReloadProviderTokenSourcesReusesGitHubAppTokenAcrossRoutes(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(filepath.Join(dir, "app.pem"), []byte("pem"), 0o600))
	writeConfigToml(t, cfgPath, `
sync_interval = "5m"
github_token_env = "UNUSED_RELOAD_GITHUB_TOKEN"

[[repos]]
owner = "acme"
name = "widget-one"

[[repos]]
owner = "acme"
name = "widget-two"

[[github_apps]]
host = "github.com"
app_id = 4242
private_key_path = "app.pem"
installation_id = 7
installation_account = "acme"
repository_selection = "selected"
selected_repos = ["acme/widget-one", "acme/widget-two"]
`)
	cfg, err := config.Load(cfgPath)
	require.NoError(err)

	var minted []tokenauth.Candidate
	set := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubApp: func(_ context.Context, c tokenauth.Candidate) (string, time.Time, error) {
			minted = append(minted, c)
			return "app-token", time.Now().Add(time.Hour), nil
		},
	})
	for _, plan := range cfg.ProviderTokenSources() {
		set.Upsert(plan.Descriptor)
	}

	srv := wiredServer(&Server{tokenSources: set})
	require.NoError(srv.configreload.ValidateReloadProviderTokenSources(t.Context(), cfg))
	require.Len(minted, 1)
	assert.Equal(t, int64(4242), minted[0].AppID)
	assert.Equal(t, "acme", minted[0].InstallationAccount)
}

func TestValidateReloadProviderSourcesUsesArchiveDescriptorForArchiveOnlyRoute(t *testing.T) {
	require := require.New(t)
	cfg := &config.Config{
		SyncInterval: "5m",
		Host:         "127.0.0.1",
		Port:         8091,
		BasePath:     "/",
		Activity:     config.Activity{ViewMode: "flat", TimeRange: "7d"},
		Repos:        []config.Repo{{Owner: "acme", Name: "widget"}},
		GitHubApps: []config.GitHubAppConfig{{
			Host: "github.com", AppID: 2, Role: config.GitHubAppRoleArchive,
			PrivateKeyPath: "/keys/archive.pem", InstallationID: 20,
			InstallationAccount: "acme", RepositorySelection: "all",
		}},
	}
	require.NoError(cfg.Validate())
	set := tokenauth.NewSourceSet(tokenauth.Options{
		GitHubApp: func(_ context.Context, candidate tokenauth.Candidate) (string, time.Time, error) {
			if candidate.AppID != 2 {
				return "", time.Time{}, errors.New("unexpected App")
			}
			return "archive-token", time.Now().Add(time.Hour), nil
		},
	})
	for _, plan := range cfg.ProviderTokenSources() {
		if plan.ArchiveOnly {
			set.Upsert(plan.ArchiveDescriptor)
		}
	}
	srv := wiredServer(&Server{tokenSources: set})
	require.NoError(srv.configreload.ValidateReloadProviderTokenSources(t.Context(), cfg))
}

// newReloadServerWithTokenSources mirrors startup: one source per
// provider token plan, registered in a SourceSet the server reloads
// against.
func newReloadServerWithTokenSources(
	t *testing.T, cfg *config.Config, cfgPath string,
) (*Server, *tokenauth.SourceSet) {
	t.Helper()
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, nil, nil, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	set := tokenauth.NewSourceSet(tokenauth.Options{})
	for _, plan := range cfg.ProviderTokenSources() {
		set.Upsert(plan.Descriptor)
	}
	for _, desc := range cfg.CloneTokenDescriptors() {
		set.Upsert(desc)
	}
	srv := NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		ServerOptions{TokenSources: set},
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	return srv, set
}

func TestConfigReloadFreezesGitHubChainOnSplitTopologyChange(t *testing.T) {
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "github-token")

	githubKey := tokenauth.Key{Platform: "github", Host: "github.com"}
	ownerKey := tokenauth.Key{
		Platform: "github", Host: "github.com", Scope: "owner:acme",
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.pem"), []byte("pem"), 0o600))
	withApp := validReloadConfig + `
[[github_apps]]
host = "github.com"
app_id = 4242
private_key_path = "app.pem"
installation_id = 7
installation_account = "acme"
repository_selection = "all"
`
	loadCfg := func(t *testing.T, name, content string) (*config.Config, string) {
		t.Helper()
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		cfg, err := config.Load(path)
		require.NoError(t, err)
		return cfg, path
	}

	t.Run("app added on reload keeps the boot PAT chain live", func(t *testing.T) {
		assert := assert.New(t)
		bootCfg, bootPath := loadCfg(t, "boot.toml", validReloadConfig)
		srv, set := newReloadServerWithTokenSources(t, bootCfg, bootPath)
		newCfg, _ := loadCfg(t, "new.toml", withApp)

		// RestartRequired must fire, and the live chain must not flip
		// reads onto the app token while write trackers are missing.
		assert.True(srv.bootCfgSnapshot.RestartRequiredFor(newCfg))
		srv.configreload.UpdateTokenSourcesForReload(newCfg)
		src, ok := set.Get(githubKey)
		require.True(t, ok)
		assert.False(src.Descriptor().HasActiveGitHubApp(),
			"a reload that adds an app must not re-point reads before restart")
		// The host-level clone chain carries the same candidates and
		// authenticates workspace git fetches; it must stay frozen too.
		cloneSrc, ok := set.Get(tokenauth.CloneKey("github.com"))
		require.True(t, ok)
		assert.False(cloneSrc.Descriptor().HasActiveGitHubApp(),
			"clone auth must not switch to the app token before restart")
	})

	t.Run("app removed on reload keeps the boot app chain live", func(t *testing.T) {
		assert := assert.New(t)
		bootCfg, bootPath := loadCfg(t, "boot-app.toml", withApp)
		srv, set := newReloadServerWithTokenSources(t, bootCfg, bootPath)
		newCfg, _ := loadCfg(t, "new-no-app.toml", validReloadConfig)

		assert.True(srv.bootCfgSnapshot.RestartRequiredFor(newCfg),
			"removing an app changes split topology and must flag a restart")
		srv.configreload.UpdateTokenSourcesForReload(newCfg)
		src, ok := set.Get(ownerKey)
		require.True(t, ok)
		assert.True(src.Descriptor().HasActiveGitHubApp(),
			"a reload that removes an app must not drop the owner route the write trackers were built for")
		fallbackSrc, ok := set.Get(githubKey)
		require.True(t, ok)
		assert.False(fallbackSrc.Descriptor().HasActiveGitHubApp(),
			"the ownerless fallback must remain PAT-only")
		cloneSrc, ok := set.Get(tokenauth.CloneKey("github.com"))
		require.True(t, ok)
		assert.False(cloneSrc.Descriptor().HasActiveGitHubApp(),
			"ownerless clone auth must remain PAT-only")
	})

	t.Run("non-topology token change still hot-applies", func(t *testing.T) {
		t.Setenv("KENN_FORGE_NEW_GITHUB_TOKEN", "rotated")
		bootCfg, bootPath := loadCfg(t, "boot-plain.toml", validReloadConfig)
		srv, set := newReloadServerWithTokenSources(t, bootCfg, bootPath)
		newCfg, _ := loadCfg(t, "new-env.toml", validReloadConfigChangedGitHubTokenEnv)

		srv.configreload.UpdateTokenSourcesForReload(newCfg)
		src, ok := set.Get(githubKey)
		require.True(t, ok)
		assert.Contains(t, src.Descriptor().SafeString(), "KENN_FORGE_NEW_GITHUB_TOKEN",
			"hosts whose split classification is unchanged must keep hot-reloading")
	})
}

func reloadForgejoHostConfig(tokenLine string) string {
	return fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[platforms]]
type = "forgejo"
host = "code.example.com"
%s

[[repos]]
owner = "acme"
name = "widget"
`, tokenLine)
}

func TestConfigReload_ForgejoHostCloneSourceFollowsRotatedToken(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "github-token")
	t.Setenv("KENN_FORGE_FORGEJO_TOKEN_A", "forgejo-token")
	t.Setenv("KENN_FORGE_FORGEJO_TOKEN_B", "rotated-token")

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, reloadForgejoHostConfig(`token_env = "KENN_FORGE_FORGEJO_TOKEN_A"`), &serverfake.MockGH{},
	)
	sourceSet, cloneSrc := reloadTestTokenSources(
		t, cfgPath, tokenauth.CloneKey("code.example.com"),
	)
	srv.tokenSources = sourceSet
	bootToken, err := cloneSrc.Token(t.Context())
	require.NoError(err)
	require.Equal("forgejo-token", bootToken)

	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, reloadForgejoHostConfig(
		`token_env = "KENN_FORGE_FORGEJO_TOKEN_B"`,
	))

	// RestartRequired is not asserted: this fixture's syncer has no
	// readers for code.example.com, so the resolving forgejo token trips
	// the client-rebuild flag. The forgejo-host e2e covers the flag with
	// a live provider client.
	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid, "reload error: %s", ev.Error)
	// Clone auth must follow the rotated host chain without a restart.
	newToken, err := cloneSrc.Token(t.Context())
	require.NoError(err)
	assert.Equal("rotated-token", newToken)
}

func TestConfigReload_ForgejoHostCloneSourceClearsWhenTokenRemoved(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "github-token")
	t.Setenv("KENN_FORGE_FORGEJO_TOKEN_A", "forgejo-token")

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, reloadForgejoHostConfig(`token_env = "KENN_FORGE_FORGEJO_TOKEN_A"`), &serverfake.MockGH{},
	)
	sourceSet, cloneSrc := reloadTestTokenSources(
		t, cfgPath, tokenauth.CloneKey("code.example.com"),
	)
	srv.tokenSources = sourceSet
	bootToken, err := cloneSrc.Token(t.Context())
	require.NoError(err)
	require.Equal("forgejo-token", bootToken)

	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, reloadForgejoHostConfig(""))

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid, "reload error: %s", ev.Error)
	assert.False(ev.RestartRequired)
	// The host went credential-less, so clone auth fails closed instead
	// of keeping the removed credential.
	_, err = cloneSrc.Token(t.Context())
	require.ErrorIs(err, tokenauth.ErrMissingToken)
}

func TestConfigReload_RepoTokenOverrideWithPlatformFallbackUpdatesSource(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("KENN_FORGE_PLATFORM_TOKEN", "platform-token")
	t.Setenv("KENN_FORGE_REPO_TOKEN", "repo-token")

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfigPlatformTokenEnv, &serverfake.MockGH{},
	)
	sourceSet := tokenauth.NewSourceSet(tokenauth.Options{})
	srv.cfgMu.Lock()
	desc := srv.cfg.ResolveRepoTokenSource(srv.cfg.Repos[0])
	srv.cfgMu.Unlock()
	src := sourceSet.Upsert(desc)
	srv.tokenSources = sourceSet
	oldToken, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("platform-token", oldToken)

	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigPlatformAndRepoTokenEnv)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid)
	assert.True(ev.RestartRequired,
		"adding an exact-repository route requires rebuilding the client pool")
	currentToken, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("platform-token", currentToken)
}

type fakeRuntimeOwner struct {
	startedStripEnvVars []string
	pty                 *fakeRuntimePTY
}

type fakeRuntimePTY struct {
	output chan []byte
	done   chan struct{}
}

func (m *fakeRuntimeOwner) HasState(string) bool {
	return m.pty != nil
}

func (m *fakeRuntimeOwner) Attach(context.Context, string) (ptyownerruntime.PTY, error) {
	return m.pty, nil
}

func (m *fakeRuntimeOwner) Start(
	_ context.Context,
	_ string,
	_ string,
	_ []string,
	stripEnvVars []string,
	_ map[string]string,
) (ptyownerruntime.PTY, error) {
	m.startedStripEnvVars = append([]string(nil), stripEnvVars...)
	m.pty = &fakeRuntimePTY{
		output: make(chan []byte),
		done:   make(chan struct{}),
	}
	return m.pty, nil
}

func (m *fakeRuntimeOwner) Stop(context.Context, string) error {
	if m.pty != nil {
		m.pty.Close()
	}
	return nil
}

func (p *fakeRuntimePTY) Output() <-chan []byte         { return p.output }
func (p *fakeRuntimePTY) Done() <-chan struct{}         { return p.done }
func (p *fakeRuntimePTY) Write([]byte) error            { return nil }
func (p *fakeRuntimePTY) Resize(ptysize.Geometry) error { return nil }
func (p *fakeRuntimePTY) ExitCode() int                 { return 0 }

func (p *fakeRuntimePTY) Close() {
	select {
	case <-p.done:
	default:
		close(p.done)
		close(p.output)
	}
}

func TestConfigReload_RuntimeStripsBootAndReloadedStartupBoundTokenEnvs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	initialConfig := strings.ReplaceAll(
		validReloadConfigRepoTokenEnv,
		"KENN_FORGE_REPO_TOKEN",
		"KENN_FORGE_REPO_OLD_TOKEN",
	) + `
[[agents]]
key = "helper"
label = "Helper"
command = ["/bin/echo"]
`
	updatedConfig := strings.ReplaceAll(
		validReloadConfigRepoTokenEnv,
		"KENN_FORGE_REPO_TOKEN",
		"KENN_FORGE_REPO_NEW_TOKEN",
	) + `
[[agents]]
key = "helper"
label = "Helper"
command = ["/bin/echo"]
`

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, initialConfig, &serverfake.MockGH{},
	)
	owner := &fakeRuntimeOwner{}
	srv.runtime = localruntime.NewManager(localruntime.Options{
		Targets: []localruntime.LaunchTarget{{
			Key:       "helper",
			Label:     "Helper",
			Kind:      localruntime.LaunchTargetAgent,
			Source:    "test",
			Command:   []string{"/bin/echo"},
			Available: true,
		}},
		PtyOwnerRuntime: owner,
		StripEnvVars:    srv.cfg.TokenEnvNames(),
	})
	t.Cleanup(srv.runtime.Shutdown)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, updatedConfig)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)
	// A bounded GitHub route descriptor rename requires restart so the
	// authenticated identity can be re-resolved. Both names are still stripped
	// from future launches while the boot descriptor remains active.
	assert.True(ev.RestartRequired)

	_, err := srv.runtime.Launch(t.Context(), "ws-1", t.TempDir(), "helper")
	require.NoError(err)
	assert.Contains(owner.startedStripEnvVars, "KENN_FORGE_REPO_OLD_TOKEN")
	assert.Contains(owner.startedStripEnvVars, "KENN_FORGE_REPO_NEW_TOKEN")
}

func TestConfigReload_InvalidConfigKeepsLastKnownGood(t *testing.T) {
	assert := assert.New(t)

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	// Capture the original config so we can confirm it stays put.
	srv.cfgMu.Lock()
	prevHost := srv.cfg.Host
	prevPort := srv.cfg.Port
	prevSyncInterval := srv.cfg.SyncInterval
	srv.cfgMu.Unlock()

	writeConfigToml(t, cfgPath, invalidReloadConfig)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.False(ev.Valid)
	assert.NotEmpty(ev.Error)

	// Daemon still holds the prior cfg snapshot.
	srv.cfgMu.Lock()
	defer srv.cfgMu.Unlock()
	assert.Equal(prevHost, srv.cfg.Host)
	assert.Equal(prevPort, srv.cfg.Port)
	assert.Equal(prevSyncInterval, srv.cfg.SyncInterval)
}

func TestConfigReload_MalformedTomlDoesNotCrash(t *testing.T) {
	assert := assert.New(t)

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, malformedTomlConfig)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.False(ev.Valid)
	assert.Contains(strings.ToLower(ev.Error), "config.toml",
		"parse error should reference the sanitized config path")
}

func TestConfigReload_NewRepoEntersSyncerTrackedSet(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, _, cfgPath, syncer := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	archiveLifecycle := &reloadArchiveLifecycleRecorder{}
	syncer.SetArchiveService(archiveLifecycle)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigExtraRepo)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)

	tracked := syncer.TrackedRepos()
	owners := make(map[string]struct{}, len(tracked))
	for _, r := range tracked {
		owners[strings.ToLower(r.Owner)+"/"+strings.ToLower(r.Name)] = struct{}{}
	}
	assert.Contains(owners, "globex/engine",
		"new repo from config edit should appear in syncer tracked set")
	require.Len(archiveLifecycle.ensured, 2)
	assert.Equal(archiveLifecycle.ensured, archiveLifecycle.retried)
	assert.Equal("acme/widget", archiveLifecycle.ensured[0].RepoPath)
	assert.Equal("globex/engine", archiveLifecycle.ensured[1].RepoPath)
}

func TestConfigReload_ResolvedArchivedStateReplacesFallbackDuplicate(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	var listedRepos sync.Map
	srv, database, cfgPath, syncer := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{
			// The exact entry cannot resolve, so it falls back to the
			// previously tracked (stale, live) ref.
			GetRepositoryFn: func(
				context.Context, string, string,
			) (*gh.Repository, error) {
				return nil, errors.New("temporary repo lookup failure")
			},
			// The overlapping glob resolves the same repo as archived.
			ListReposByOwnerFn: func(
				_ context.Context, owner string,
			) ([]*gh.Repository, error) {
				return []*gh.Repository{{
					NodeID:   new("repo-acme-widget"),
					Name:     new("widget"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(true),
				}}, nil
			},
			ListNotificationsFn: func(
				_ context.Context, opts ghclient.NotificationListOptions,
			) ([]ghclient.NotificationThread, bool, error) {
				if opts.RepoName != "" {
					listedRepos.Store(opts.RepoName, true)
				}
				return nil, false, nil
			},
		},
	)
	_, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget",
	})
	require.NoError(err)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:        "acme",
		Name:         "widget",
		PlatformHost: "github.com",
		RepoPath:     "acme/widget",
	}})

	writeConfigToml(t, cfgPath, validReloadConfigExactPlusGlob)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)

	require.True(trackedRepoArchived(syncer, "acme", "widget"),
		"resolved archived metadata must replace the fallback duplicate")

	require.NoError(syncer.SyncNotifications(t.Context()))
	_, listedWidget := listedRepos.Load("widget")
	assert.False(listedWidget,
		"repo resolved as archived must be excluded from notification polling")
}

func TestConfigReload_FallbackKeepsRenamedArchivedTrackedRepo(t *testing.T) {
	require := require.New(t)

	srv, _, cfgPath, syncer := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{
			// The exact entry cannot resolve during the reload; the
			// previously tracked repo was renamed provider-side, so its
			// route no longer matches the configured path.
			GetRepositoryFn: func(
				context.Context, string, string,
			) (*gh.Repository, error) {
				return nil, errors.New("temporary repo lookup failure")
			},
		},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:              "acme",
		Name:               "widget-next",
		PlatformHost:       "github.com",
		RepoPath:           "acme/widget-next",
		PlatformExternalID: "repo-acme-widget",
		ConfiguredRepoPath: "acme/widget",
		Archived:           true,
	}})

	writeConfigToml(t, cfgPath, validReloadConfigChangedActivity)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)

	require.True(trackedRepoArchived(syncer, "acme", "widget-next"),
		"fallback must keep the renamed archived tracked repo, not"+
			" synthesize a live duplicate under the stale configured route")
	for _, repo := range syncer.TrackedRepos() {
		require.NotEqual("widget", repo.Name,
			"stale configured route must not be tracked as a duplicate")
	}
}

func TestConfigReload_RouteReuseRefreshThenFailedReloadTracksRenamedRepoOnce(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	// Phase 0: acme/widget resolves normally. Phase 1: the provider renamed
	// widget to widget-next and a different repository reused the old route;
	// exact lookups fail transiently while the glob still lists both.
	renamed := atomic.Bool{}
	mock := &serverfake.MockGH{
		GetRepositoryFn: func(
			_ context.Context, owner, repo string,
		) (*gh.Repository, error) {
			if renamed.Load() {
				return nil, errors.New("temporary repo lookup failure")
			}
			return &gh.Repository{
				NodeID:   new("repo-x"),
				Name:     new(repo),
				Owner:    &gh.User{Login: new(owner)},
				Archived: new(false),
			}, nil
		},
		ListReposByOwnerFn: func(
			_ context.Context, owner string,
		) ([]*gh.Repository, error) {
			if !renamed.Load() {
				return []*gh.Repository{{
					NodeID:   new("repo-x"),
					Name:     new("widget"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				}}, nil
			}
			return []*gh.Repository{
				{
					NodeID:   new("repo-x"),
					Name:     new("widget-next"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
				{
					NodeID:   new("repo-y"),
					Name:     new("widget"),
					Owner:    &gh.User{Login: new(owner)},
					Archived: new(false),
				},
			}, nil
		},
	}
	srv, _, cfgPath, syncer := setupTestServerWithConfigContent(
		t, validReloadConfigExactPlusGlob, mock,
	)
	require.True(syncer.IsTrackedRepo("acme", "widget"))
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	renamed.Store(true)
	rr := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/repo/gh/acme/*/refresh", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	writeConfigToml(t, cfgPath, validReloadConfigExactPlusGlobChangedActivity)
	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)

	tracked := syncer.TrackedRepos()
	require.Len(tracked, 2,
		"renamed repo and route successor, no synthetic duplicate")
	byName := make(map[string]ghclient.RepoRef, len(tracked))
	for _, repo := range tracked {
		byName[repo.Name] = repo
	}
	assert.Equal("repo-x", byName["widget-next"].PlatformExternalID)
	assert.Equal("acme/widget", byName["widget-next"].ConfiguredRepoPath,
		"the renamed repo keeps the exact entry's provenance through the"+
			" API refresh and the failed reload")
	assert.Equal("repo-y", byName["widget"].PlatformExternalID)
	assert.Empty(byName["widget"].ConfiguredRepoPath,
		"the route successor must not claim the exact entry")
}

func TestConfigReload_GlobFailureKeepsPreviouslyTrackedMatches(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, _, cfgPath, syncer := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{
			ListReposByOwnerFn: func(context.Context, string) ([]*gh.Repository, error) {
				return nil, errors.New("temporary repo listing failure")
			},
		},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:        "acme",
		Name:         "widget-api",
		PlatformHost: "github.com",
		RepoPath:     "acme/widget-api",
	}})

	writeConfigToml(t, cfgPath, validReloadConfigGlobRepo)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)

	tracked := syncer.TrackedRepos()
	require.Len(tracked, 1)
	assert.Equal("acme", tracked[0].Owner)
	assert.Equal("widget-api", tracked[0].Name)
}

func TestConfigReload_DebouncesBurstedWrites(t *testing.T) {
	assert := assert.New(t)

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	// Multiple rapid writes within the 100 ms debounce window should
	// coalesce into one config.changed event.
	for i := range 4 {
		var content string
		switch i % 2 {
		case 0:
			content = validReloadConfig
		case 1:
			content = validReloadConfigChangedActivity
		}
		writeConfigToml(t, cfgPath, content)
		time.Sleep(10 * time.Millisecond) //nolint:kennlint // waits for subprocess/HTTP fixture for tmux/e2e waits
	}

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid)

	// Drain any extra events that arrive within a short window — the
	// debounce should have prevented them, but we don't assert "no
	// extras at all" since fsnotify ordering on some kernels can
	// flush a second event after the rename burst.
	select {
	case extra, ok := <-stream.events:
		if ok {
			// A second event is acceptable but should be valid and quick.
			assert.True(extra.Valid)
		}
	case <-time.After(200 * time.Millisecond):
	}
}

func TestConfigReload_SubscriberAfterParseErrorGetsCachedEvent(t *testing.T) {
	assert := assert.New(t)

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)

	// Drive an invalid edit and let the daemon broadcast.
	earlyStream := streamConfigEvents(t, srv)
	writeConfigToml(t, cfgPath, invalidReloadConfig)
	ev := waitForConfigEvent(t, earlyStream, 2*time.Second)
	earlyStream.Close()
	assert.False(ev.Valid)

	// A new subscriber connecting now should still observe the parse
	// error via the cached config_status slot, not silently miss it.
	lateStream := streamConfigEvents(t, srv)
	defer lateStream.Close()
	cached := waitForConfigEvent(t, lateStream, 2*time.Second)
	assert.False(cached.Valid)
	assert.NotEmpty(cached.Error)
}

func TestActiveFleetConfigSnapshotDefersHotEnableUntilRuntimeAuth(t *testing.T) {
	assert := assert.New(t)
	boot := &config.Config{}
	srv := wiredServer(&Server{
		cfg: &config.Config{
			API:   config.API{RequireAuth: true},
			Fleet: config.Fleet{Enabled: true, Role: config.FleetRoleHub},
		},
		bootCfgSnapshot: configreload.SnapshotStartupConfig(boot),
	})

	assert.False(srv.streamapi.ActiveFleetConfigSnapshotLocked().Fleet.Enabled)
	assert.False(srv.streamapi.FederationEnabled())

	// A restart installs the requested API authentication policy before
	// federation becomes active.
	srv.daemonRequests = authapi.NewDaemonRequestPolicy(authapi.DaemonAccessOptions{
		Token: "local-secret", RequireAPIAuth: true,
	})
	assert.True(srv.streamapi.ActiveFleetConfigSnapshotLocked().Fleet.Enabled)
	assert.True(srv.streamapi.FederationEnabled())
}

func TestActiveFleetConfigSnapshotKeepsBootIdentity(t *testing.T) {
	assert := assert.New(t)
	boot := &config.Config{Fleet: config.Fleet{
		Role: config.FleetRoleSpoke, BaseURL: "https://spoke.example",
		Hub: &config.FleetHub{
			NodeID: "11111111111111111111111111111111",
			Name:   "Hub", BaseURL: "https://hub.example",
		},
	}}
	srv := wiredServer(&Server{
		cfg: &config.Config{Fleet: config.Fleet{
			Role: config.FleetRoleHub, BaseURL: "https://new-spoke.example",
			Hub: &config.FleetHub{
				NodeID: "22222222222222222222222222222222",
				Name:   "Renamed hub", BaseURL: "https://new-hub.example",
			},
			Members: []config.FleetMember{{
				NodeID:  "33333333333333333333333333333333",
				BaseURL: "https://member.example", State: "active",
			}},
			PeerTimeout: "4s",
		}},
		bootCfgSnapshot: configreload.SnapshotStartupConfig(boot),
	})

	snapshot := srv.streamapi.ActiveFleetConfigSnapshotLocked()

	assert.Equal(config.FleetRoleSpoke, snapshot.Fleet.Role)
	assert.Equal(boot.Fleet.BaseURL, snapshot.Fleet.BaseURL)
	require.NotNil(t, snapshot.Fleet.Hub)
	assert.Equal(boot.Fleet.Hub.NodeID, snapshot.Fleet.Hub.NodeID)
	assert.Equal(boot.Fleet.Hub.BaseURL, snapshot.Fleet.Hub.BaseURL)
	assert.Equal("Renamed hub", snapshot.Fleet.Hub.Name)
	assert.Equal(srv.cfg.Fleet.Members, snapshot.Fleet.Members)
	assert.Equal("4s", snapshot.Fleet.PeerTimeout)
}

func TestFleetMemberPersistenceUsesBootIdentityAfterReload(t *testing.T) {
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfigContent(t, `
host = "127.0.0.1"
port = 8091

[api]
require_auth = true

[fleet]
enabled = true
role = "hub"
base_url = "https://hub.example"
`, &serverfake.MockGH{})

	writeConfigToml(t, cfgPath, `
host = "127.0.0.1"
port = 8091

[api]
require_auth = true

[fleet]
enabled = true
role = "spoke"
base_url = "https://spoke.example"

[fleet.hub]
node_id = "11111111111111111111111111111111"
base_url = "https://replacement-hub.example"
`)
	event := srv.configreload.ApplyConfigChange(t.Context())
	require.True(event.Valid, event.Error)
	require.True(event.RestartRequired)
	fleetCfg := srv.streamapi.ActiveFleetConfigSnapshotLocked().Fleet
	require.Equal(config.FleetRoleHub, fleetCfg.RoleOrDefault())

	member := config.FleetMember{
		NodeID:  "22222222222222222222222222222222",
		BaseURL: "https://spoke-a.example",
		State:   "active",
	}
	require.NoError(srv.settingsapi.PersistFleetMember(t.Context(), member))

	persisted, err := config.Load(cfgPath)
	require.NoError(err)
	require.Equal(config.FleetRoleHub, persisted.Fleet.RoleOrDefault())
	require.Equal("https://hub.example", persisted.Fleet.BaseURL)
	require.Nil(persisted.Fleet.Hub)
	require.Equal([]config.FleetMember{member}, persisted.Fleet.Members)

	persisted.Fleet.Members[0].OutboundDisabled = true
	require.NoError(persisted.Save(cfgPath))
	require.True(srv.configreload.ApplyConfigChange(t.Context()).Valid)
	member.Name = "Renamed spoke"
	require.NoError(srv.settingsapi.PersistFleetMember(t.Context(), member))
	persisted, err = config.Load(cfgPath)
	require.NoError(err)
	require.True(persisted.Fleet.Members[0].OutboundDisabled)
	require.Equal(member.Name, persisted.Fleet.Members[0].Name)
}

const validReloadConfigAuthGate = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[api]
require_auth = true

[fleet]
enabled = true
role = "hub"
base_url = "https://hub.example"
`

const validReloadConfigFleetSessions = `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[fleet.sessions]
include_unmanaged_details = true
`

const validReloadConfigRestartRequiredFields = `
sync_interval = "10m"
github_token_env = "KENN_FORGE_RELOADED_GITHUB_TOKEN"
host = "127.0.0.2"
port = 9191
base_path = "/kenn-forge"
allowed_hosts = ["forge.test:9191"]
trust_reverse_proxy = true

[[repos]]
owner = "acme"
name = "widget"

[api]
require_auth = true

[fleet.sessions]
include_unmanaged_details = true

[roborev]
endpoint = "http://127.0.0.1:7374"

[tmux]
command = ["systemd-run", "--user", "--scope", "tmux"]

[shell]
command = ["systemd-run", "--user", "--scope", "--pty", "bash"]
`

// The auth gate is wired in newServer; editing it mid-run must surface
// restart_required on the user-visible config.changed event, not
// silently apply nothing.
func TestConfigReload_RestartRequiredOnAuthGateChange(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigAuthGate)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid)
	assert.True(ev.RestartRequired,
		"[api].require_auth change should mark restart_required")
	assert.False(srv.streamapi.FederationEnabled(),
		"fleet activation must wait for the requested auth policy to install")

	srv.cfgMu.Lock()
	savedCfg := *srv.cfg
	srv.cfgMu.Unlock()
	savePath := filepath.Join(t.TempDir(), "saved.toml")
	require.NoError(savedCfg.Save(savePath))
	reloaded, err := config.Load(savePath)
	require.NoError(err)
	assert.True(reloaded.API.RequireAuth,
		"later settings saves must preserve externally reloaded API auth")
}

func TestConfigReload_RestartRequiredOnFleetSessionsChange(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigFleetSessions)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid)
	assert.True(ev.RestartRequired,
		"[fleet.sessions].include_unmanaged_details change should mark restart_required")

	srv.cfgMu.Lock()
	savedCfg := *srv.cfg
	srv.cfgMu.Unlock()
	savePath := filepath.Join(t.TempDir(), "saved.toml")
	require.NoError(savedCfg.Save(savePath))
	reloaded, err := config.Load(savePath)
	require.NoError(err)
	assert.True(reloaded.Fleet.Sessions.IncludeUnmanagedDetails,
		"later settings saves must preserve externally reloaded fleet session settings")
}

func TestConfigReload_SettingsSavePreservesRestartRequiredFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, _, cfgPath, _ := setupTestServerWithConfigContent(
		t, validReloadConfig, &serverfake.MockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigRestartRequiredFields)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid, "reload error: %s", ev.Error)
	require.True(ev.RestartRequired)

	rr := testutil.DoJSON(t, srv, http.MethodPut, "/api/v1/settings", spokeapi.UpdateSettingsRequest{
		Activity: &config.Activity{
			ViewMode:  "flat",
			TimeRange: "30d",
		},
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	assert.Equal("10m", reloaded.SyncInterval)
	assert.Equal("KENN_FORGE_RELOADED_GITHUB_TOKEN", reloaded.GitHubTokenEnv)
	assert.Equal("127.0.0.2", reloaded.Host)
	assert.Equal(9191, reloaded.Port)
	assert.Equal("/kenn-forge/", reloaded.BasePath)
	assert.Equal([]string{"forge.test:9191"}, reloaded.AllowedHosts)
	assert.True(reloaded.TrustReverseProxy)
	assert.True(reloaded.API.RequireAuth)
	assert.True(reloaded.Fleet.Sessions.IncludeUnmanagedDetails)
	assert.Equal("http://127.0.0.1:7374", reloaded.Roborev.Endpoint)
	assert.Equal(
		[]string{"systemd-run", "--user", "--scope", "tmux"},
		reloaded.Tmux.Command,
	)
	assert.Equal(
		[]string{"systemd-run", "--user", "--scope", "--pty", "bash"},
		reloaded.Shell.Command,
	)
	assert.Equal("flat", reloaded.Activity.ViewMode)
	assert.Equal("30d", reloaded.Activity.TimeRange)
}

// A rejected reload must still accumulate the candidate's token env
// names: the user just declared them credentials, and a later base
// terminal pane must not inherit them from the daemon environment.
func TestConfigReloadRejectedCandidateStillStripsItsTokenNames(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "")
	t.Setenv("KENN_FORGE_REPO_TOKEN", "old")

	srv, _, cfgPath, _ := setupTestServerWithConfigContentAndOptions(
		t, validReloadConfigRepoTokenEnv, &serverfake.MockGH{}, ServerOptions{
			HostCheckAllowLoopbackAnyPort:      true,
			WorktreeDir:                        t.TempDir(),
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	sourceSet := tokenauth.NewSourceSet(tokenauth.Options{})
	srv.cfgMu.Lock()
	desc := srv.cfg.ResolveRepoTokenSource(srv.cfg.Repos[0])
	srv.cfgMu.Unlock()
	sourceSet.Upsert(desc)
	srv.tokenSources = sourceSet

	writeConfigToml(t, cfgPath, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
token_env = "WKSP_CANDIDATE_ONLY_TOKEN"
`)
	event := srv.configreload.ApplyConfigChange(t.Context())
	require.False(event.Valid, "reload with an unset token env must be rejected")
	assert.Contains(srv.workspaces.TmuxStripEnvVars(),
		"WKSP_CANDIDATE_ONLY_TOKEN",
		"a rejected candidate's token names must still be stripped from panes")
}

// A structurally invalid candidate still parses — config.Load returns
// the parsed config alongside validation errors — so its newly declared
// token names must be stripped from future panes even though the reload
// is rejected before any other validation runs.
func TestConfigReloadStructurallyInvalidCandidateStillStripsItsTokenNames(
	t *testing.T,
) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfigContentAndOptions(
		t, validReloadConfig, &serverfake.MockGH{}, ServerOptions{
			HostCheckAllowLoopbackAnyPort:      true,
			WorktreeDir:                        t.TempDir(),
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	writeConfigToml(t, cfgPath, `
sync_interval = "5m"
github_token_env = "WKSP_INVALID_CANDIDATE_TOKEN"
host = "not-an-ip"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`)
	event := srv.configreload.ApplyConfigChange(t.Context())
	require.False(event.Valid, "structurally invalid candidate must be rejected")
	assert.Contains(srv.workspaces.TmuxStripEnvVars(),
		"WKSP_INVALID_CANDIDATE_TOKEN",
		"an invalid candidate's token names must still be stripped from panes")
}

// A candidate rejected at the load stage for deprecated keys still
// decodes, so its newly declared token names must reach strip
// accumulation like validation-stage rejections.
func TestConfigReloadDeprecatedKeyCandidateStillStripsItsTokenNames(
	t *testing.T,
) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfigContentAndOptions(
		t, validReloadConfig, &serverfake.MockGH{}, ServerOptions{
			HostCheckAllowLoopbackAnyPort:      true,
			WorktreeDir:                        t.TempDir(),
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	writeConfigToml(t, cfgPath, `
sync_interval = "5m"
github_token_env = "WKSP_DEPRECATED_CANDIDATE_TOKEN"
host = "127.0.0.1"
port = 8091

[[notebooks]]
id = "notes"
path = "/tmp/notes"
`)
	event := srv.configreload.ApplyConfigChange(t.Context())
	require.False(event.Valid, "deprecated keys must reject the reload")
	assert.Contains(srv.workspaces.TmuxStripEnvVars(),
		"WKSP_DEPRECATED_CANDIDATE_TOKEN",
		"a load-stage-rejected candidate's token names must still be stripped")
}

// A rejected candidate declaring a non-secret terminal variable as a
// token must not poison the strip sets: stripping PATH or TMUX_TMPDIR
// would break terminals and tmux socket routing while the
// last-known-good config stays active.
func TestConfigReloadRejectedCollisionDoesNotPoisonStripSets(
	t *testing.T,
) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _, cfgPath, _ := setupTestServerWithConfigContentAndOptions(
		t, validReloadConfig, &serverfake.MockGH{}, ServerOptions{
			HostCheckAllowLoopbackAnyPort:      true,
			WorktreeDir:                        t.TempDir(),
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	writeConfigToml(t, cfgPath, `
sync_interval = "5m"
github_token_env = "TMUX_TMPDIR"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`)
	event := srv.configreload.ApplyConfigChange(t.Context())
	require.False(event.Valid, "terminal-variable token names must be rejected")
	assert.NotContains(srv.workspaces.TmuxStripEnvVars(), "TMUX_TMPDIR",
		"rejected collisions must never enter the strip sets")
}

func TestInitializeProviderRepositoriesKeepsHTTPReadyDuringDiscovery(t *testing.T) {
	require := require.New(t)
	srv, _, _, _ := setupTestServerWithConfigContent(t, validReloadConfig, &serverfake.MockGH{})
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- srv.InitializeProviderRepositories(t.Context(), func(ctx context.Context, cfg *config.Config) []ghclient.RepoRef {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return []ghclient.RepoRef{{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "acme", Name: "discovered", PlatformExternalID: "12345"}}
		})
	}()
	<-entered
	response := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:8091/healthz", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	srv.ServeHTTP(response, request)
	assert.Equal(t, http.StatusOK, response.Code)
	close(release)
	require.NoError(<-done)
	repos := srv.syncer.TrackedRepos()
	require.Len(repos, 1)
	require.Equal("12345", repos[0].PlatformExternalID)
}

func TestInitializeProviderRepositoriesKeepsRepoAddedDuringDiscovery(t *testing.T) {
	require := require.New(t)
	srv, _, _, _ := setupTestServerWithConfigContent(t, validReloadConfig, &serverfake.MockGH{})
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- srv.InitializeProviderRepositories(t.Context(), func(ctx context.Context, cfg *config.Config) []ghclient.RepoRef {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return []ghclient.RepoRef{{Platform: platform.KindGitHub, PlatformHost: "github.com", Owner: "acme", Name: "widget", PlatformExternalID: "12345"}}
		})
	}()
	<-entered
	added := make(chan int, 1)
	go func() {
		rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repos", map[string]string{
			"provider": "github", "host": "github.com", "owner": "other-org", "name": "other-repo",
		})
		added <- rr.Code
	}()
	require.Never(func() bool { return len(added) > 0 }, time.Second, 10*time.Millisecond,
		"an add must wait for discovery instead of being overwritten by its stale snapshot")
	close(release)
	require.NoError(<-done)
	require.Equal(http.StatusCreated, <-added)
	var names []string
	for _, repo := range srv.syncer.TrackedRepos() {
		names = append(names, repo.Owner+"/"+repo.Name)
	}
	require.ElementsMatch([]string{"acme/widget", "other-org/other-repo"}, names)
}

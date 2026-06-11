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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/docs"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/tokenauth"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

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
	events chan configChangedEvent
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

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)

	stream := &configEventStream{
		resp:   resp,
		cancel: cancel,
		events: make(chan configChangedEvent, 8),
	}

	// Wait for the handler to register before returning, so the test
	// does not race the watcher's first event against subscriber setup.
	require.Eventually(t, func() bool {
		srv.hub.mu.Lock()
		defer srv.hub.mu.Unlock()
		return len(srv.hub.subscribers) >= 1
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
				var ev configChangedEvent
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
) configChangedEvent {
	t.Helper()
	select {
	case ev, ok := <-stream.events:
		require.True(t, ok, "events channel closed before an event arrived")
		return ev
	case <-time.After(timeout):
		require.FailNow(t, "timed out waiting for config.changed event")
		return configChangedEvent{}
	}
}

const validReloadConfig = `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`

const validReloadConfigExtraRepo = `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
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
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
token_env = "MIDDLEMAN_REPO_TOKEN"
`

const validReloadConfigChangedGitHubTokenEnv = `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_NEW_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`

const validReloadConfigPlatformTokenEnv = `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[platforms]]
type = "github"
host = "github.com"
token_env = "MIDDLEMAN_PLATFORM_TOKEN"

[[repos]]
owner = "acme"
name = "widget"
`

const validReloadConfigPlatformAndRepoTokenEnv = `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[platforms]]
type = "github"
host = "github.com"
token_env = "MIDDLEMAN_PLATFORM_TOKEN"

[[repos]]
owner = "acme"
name = "widget"
token_env = "MIDDLEMAN_REPO_TOKEN"
`

const validReloadConfigGlobRepo = `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget-*"
`

const validReloadConfigChangedActivity = `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
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
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
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
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[modes]
kata = true
docs = true
messages = true
workspaces = false
`

const validReloadConfigRestartRequired = `
sync_interval = "10m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

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

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfig, &mockGH{},
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

func TestConfigReload_UpdatesBranchActivityLimits(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfig, &mockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigChangedBranchActivityLimits)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)
	assert.False(ev.RestartRequired)

	retention, maxCommits := srv.syncer.BranchActivityLimits()
	assert.Equal(14*24*time.Hour, retention)
	assert.Equal(2, maxCommits)
}

func TestConfigReload_UpdatesModes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfig, &mockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigChangedModes)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)
	assert.False(ev.RestartRequired)

	srv.cfgMu.Lock()
	gotModes := cloneModeVisibility(srv.cfg.Modes)
	srv.cfgMu.Unlock()
	assert.True(*gotModes.Kata)
	assert.True(*gotModes.Docs)
	assert.True(*gotModes.Messages)
	assert.False(*gotModes.Workspaces)
	assert.True(*gotModes.Activity)
	assert.True(*gotModes.Repos)
	assert.True(*gotModes.Pulls)
	assert.True(*gotModes.Issues)
	assert.True(*gotModes.Board)
	assert.True(*gotModes.Reviews)
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

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, initialConfig, &mockGH{},
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

	gotRegistryFolders := srv.docsRegistry.Folders()
	require.Len(gotRegistryFolders, 1)
	assert.Equal("handbook", gotRegistryFolders[0].ID)
	assert.Equal("Handbook", gotRegistryFolders[0].Name)
	wantRegistryRoot, err := filepath.EvalSymlinks(updatedRoot)
	require.NoError(err)
	assert.Equal(wantRegistryRoot, gotRegistryFolders[0].Path)
	_, err = srv.docsRegistry.Lookup("notes")
	require.ErrorIs(err, docs.ErrFolderNotFound)

	listRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders", nil)
	require.Equal(http.StatusOK, listRR.Code, listRR.Body.String())
	var listBody docsFolderListWire
	require.NoError(json.NewDecoder(listRR.Body).Decode(&listBody))
	require.Len(listBody.Folders, 1)
	assert.Equal("handbook", listBody.Folders[0].ID)
	assert.Equal("Handbook", listBody.Folders[0].Name)

	updatedReadRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/handbook/file?path=guide.md", nil)
	require.Equal(http.StatusOK, updatedReadRR.Code, updatedReadRR.Body.String())
	var readBody struct {
		Content string `json:"content"`
	}
	require.NoError(json.NewDecoder(updatedReadRR.Body).Decode(&readBody))
	assert.Equal("# Guide\n", readBody.Content)

	oldReadRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/notes/file?path=old.md", nil)
	assert.Equal(http.StatusNotFound, oldReadRR.Code, oldReadRR.Body.String())
}

func TestConfigReload_UpdatesMsgvaultHealthHandler(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MSGVAULT_API_KEY_TEST", "secret-key")
	var firstStats, secondStats atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stats":
			firstStats.Add(1)
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/stats":
			secondStats.Add(1)
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer second.Close()

	initialConfig := validReloadConfig + fmt.Sprintf(`
[msgvault]
url = %q
api_key_env = "MSGVAULT_API_KEY_TEST"
`, first.URL)
	updatedConfig := validReloadConfig + fmt.Sprintf(`
[msgvault]
url = %q
api_key_env = "MSGVAULT_API_KEY_TEST"
`, second.URL)

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, initialConfig, &mockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	firstRR := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)
	require.Equal(http.StatusOK, firstRR.Code, firstRR.Body.String())
	firstBody := decodeMsgvaultHealth(t, firstRR)
	require.True(firstBody.OK)
	assert.Equal(first.URL, firstBody.URL)
	assert.Equal(int32(1), firstStats.Load())

	writeConfigToml(t, cfgPath, updatedConfig)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)
	assert.False(ev.RestartRequired)

	secondRR := doMsgvaultJSON(t, srv, http.MethodGet, "/api/v1/msgvault/health", nil)
	require.Equal(http.StatusOK, secondRR.Code, secondRR.Body.String())
	secondBody := decodeMsgvaultHealth(t, secondRR)
	require.True(secondBody.OK)
	assert.Equal(second.URL, secondBody.URL)
	assert.Equal(int32(1), secondStats.Load())
}

func TestConfigReload_UpdatesMsgvaultTokenEnvWithoutRestart(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer second.Close()

	initialConfig := validReloadConfig + fmt.Sprintf(`
[msgvault]
url = %q
api_key_env = "MSGVAULT_OLD_KEY"

[[agents]]
key = "helper"
label = "Helper"
command = ["/bin/echo"]
`, first.URL)
	updatedConfig := validReloadConfig + fmt.Sprintf(`
[msgvault]
url = %q
api_key_env = "MSGVAULT_NEW_KEY"

[[agents]]
key = "helper"
label = "Helper"
command = ["/bin/echo"]
`, second.URL)

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, initialConfig, &mockGH{},
	)
	owner := &msgvaultRuntimeOwner{}
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
		StripEnvVars:    []string{"MSGVAULT_OLD_KEY"},
	})
	t.Cleanup(srv.runtime.Shutdown)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, updatedConfig)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)
	assert.False(ev.RestartRequired)

	_, err := srv.runtime.Launch(context.Background(), "ws-1", t.TempDir(), "helper")
	require.NoError(err)
	assert.Contains(owner.startedStripEnvVars, "MSGVAULT_NEW_KEY")
	assert.Contains(owner.startedStripEnvVars, "MSGVAULT_OLD_KEY")
}

func TestConfigReload_WatcherFiresOnAtomicRename(t *testing.T) {
	assert := assert.New(t)

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfig, &mockGH{},
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

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfig, &mockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigRestartRequired)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid)
	assert.True(ev.RestartRequired, "sync_interval change should mark restart_required")
}

func TestConfigReload_TokenSourceChangeForExistingHostUpdatesSource(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "old")
	t.Setenv("MIDDLEMAN_REPO_TOKEN", "new")

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfig, &mockGH{},
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
	assert.False(ev.RestartRequired,
		"repo token_env change for a known provider host should hot-update")
	newToken, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("new", newToken)
}

func TestConfigReload_GitHubTokenEnvChangeUpdatesConfigSnapshot(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "old")
	t.Setenv("MIDDLEMAN_NEW_GITHUB_TOKEN", "new")

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfig, &mockGH{},
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
	assert.False(ev.RestartRequired)
	newToken, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("new", newToken)

	srv.cfgMu.Lock()
	currentTokenEnv := srv.cfg.GitHubTokenEnv
	savePath := filepath.Join(t.TempDir(), "saved.toml")
	saveErr := srv.cfg.Save(savePath)
	srv.cfgMu.Unlock()
	require.NoError(saveErr)
	assert.Equal("MIDDLEMAN_NEW_GITHUB_TOKEN", currentTokenEnv)

	saved, err := config.Load(savePath)
	require.NoError(err)
	assert.Equal("MIDDLEMAN_NEW_GITHUB_TOKEN", saved.GitHubTokenEnv)
}

func TestConfigReload_InvalidTokenSourceKeepsLastKnownGoodSource(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "")
	t.Setenv("MIDDLEMAN_REPO_TOKEN", "old")

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfigRepoTokenEnv, &mockGH{},
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
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
token_env = "MIDDLEMAN_MISSING_REPO_TOKEN"
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
	assert.Equal("MIDDLEMAN_REPO_TOKEN", currentTokenEnv)
}

func TestValidateReloadCloneTokenSourcesUsesRepoDescriptorForProviderHost(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	writeConfigToml(t, cfgPath, `
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"

[[platforms]]
type = "github"
host = "github.com"
token_env = "PLATFORM_TOKEN"

[[repos]]
owner = "acme"
name = "widget"
platform = "github"
platform_host = "github.com"
token_env = "REPO_TOKEN"
`)
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	require.NoError(t, validateReloadCloneTokenSources(cfg))
}

func TestValidateReloadCloneTokenSourcesAllowsEquivalentChainsOnSameHost(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	// Two providers share a self-hosted host. The forgejo repo's token_env
	// repeats its platform fallback, producing the chain env:SHARED ->
	// env:SHARED, while gitlab resolves to a plain env:SHARED. They name the
	// same token, so the per-host clone-token check must compare canonical
	// chains and accept the reload rather than flag a conflict.
	writeConfigToml(t, cfgPath, `
[[platforms]]
type = "forgejo"
host = "code.example.com"
token_env = "SHARED"

[[platforms]]
type = "gitlab"
host = "code.example.com"
token_env = "SHARED"

[[repos]]
owner = "acme"
name = "widget"
platform = "forgejo"
platform_host = "code.example.com"
token_env = "SHARED"
`)
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	require.NoError(t, validateReloadCloneTokenSources(cfg))
}

func TestValidateReloadCloneTokenSourcesIgnoresCredentiallessPlatformHosts(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	// The forgejo entry has no token config and a non-default host, so its
	// candidate chain is empty. It imposes no clone credential and must not
	// conflict with the tokened gitlab entry on the same host.
	writeConfigToml(t, cfgPath, `
[[platforms]]
type = "forgejo"
host = "code.example.com"

[[platforms]]
type = "gitlab"
host = "code.example.com"
token_env = "SHARED"
`)
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	require.NoError(t, validateReloadCloneTokenSources(cfg))
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

const reloadPlatformTokenConfig = `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[platforms]]
type = "gitlab"
host = "gitlab.example.com"
token_env = "MIDDLEMAN_PLATFORM_TOKEN"

[[repos]]
owner = "acme"
name = "widget"
`

const reloadPlatformTokenlessConfig = `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
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
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "github-token")
	t.Setenv("MIDDLEMAN_PLATFORM_TOKEN", "platform-token")

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, reloadPlatformTokenConfig, &mockGH{},
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
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "github-token")
	t.Setenv("MIDDLEMAN_PLATFORM_TOKEN", "platform-token")

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, reloadPlatformTokenlessConfig, &mockGH{},
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

// Two providers share one host with the same credential chain — the only
// multi-provider-per-host layout clone-token validation accepts.
const reloadSharedHostBothTokensConfig = `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[platforms]]
type = "forgejo"
host = "code.example.com"
token_env = "MIDDLEMAN_SHARED_TOKEN"

[[platforms]]
type = "gitea"
host = "code.example.com"
token_env = "MIDDLEMAN_SHARED_TOKEN"

[[repos]]
owner = "acme"
name = "widget"
`

// The forgejo entry went credential-less while gitea rotated to a new env
// var, so the host's effective clone chain is gitea's surviving chain.
const reloadSharedHostSurvivorRotatedConfig = `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[platforms]]
type = "forgejo"
host = "code.example.com"

[[platforms]]
type = "gitea"
host = "code.example.com"
token_env = "MIDDLEMAN_ROTATED_TOKEN"

[[repos]]
owner = "acme"
name = "widget"
`

const reloadSharedHostAllTokenlessConfig = `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[platforms]]
type = "forgejo"
host = "code.example.com"

[[platforms]]
type = "gitea"
host = "code.example.com"

[[repos]]
owner = "acme"
name = "widget"
`

func TestConfigReload_SharedHostCloneSourceFollowsSurvivingProviderChain(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "github-token")
	t.Setenv("MIDDLEMAN_SHARED_TOKEN", "shared-token")
	t.Setenv("MIDDLEMAN_ROTATED_TOKEN", "rotated-token")

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, reloadSharedHostBothTokensConfig, &mockGH{},
	)
	sourceSet, cloneSrc := reloadTestTokenSources(
		t, cfgPath, tokenauth.CloneKey("code.example.com"),
	)
	srv.tokenSources = sourceSet
	bootToken, err := cloneSrc.Token(t.Context())
	require.NoError(err)
	require.Equal("shared-token", bootToken)

	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, reloadSharedHostSurvivorRotatedConfig)

	// RestartRequired is not asserted: this fixture's syncer has no
	// readers for code.example.com, so the resolving gitea token trips
	// the client-rebuild flag. The shared-host e2e covers the flag with
	// live provider clients.
	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid, "reload error: %s", ev.Error)
	// Clone auth must follow the host's surviving effective chain, not
	// stay pinned to the forgejo entry that lost its token.
	newToken, err := cloneSrc.Token(t.Context())
	require.NoError(err)
	assert.Equal("rotated-token", newToken)
}

func TestConfigReload_SharedHostCloneSourceClearsWhenAllTokensRemoved(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "github-token")
	t.Setenv("MIDDLEMAN_SHARED_TOKEN", "shared-token")

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, reloadSharedHostBothTokensConfig, &mockGH{},
	)
	sourceSet, cloneSrc := reloadTestTokenSources(
		t, cfgPath, tokenauth.CloneKey("code.example.com"),
	)
	srv.tokenSources = sourceSet
	bootToken, err := cloneSrc.Token(t.Context())
	require.NoError(err)
	require.Equal("shared-token", bootToken)

	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, reloadSharedHostAllTokenlessConfig)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	assert.True(ev.Valid, "reload error: %s", ev.Error)
	assert.False(ev.RestartRequired)
	// Every provider on the host went credential-less, so clone auth
	// fails closed instead of keeping the removed credential.
	_, err = cloneSrc.Token(t.Context())
	require.ErrorIs(err, tokenauth.ErrMissingToken)
}

func TestConfigReload_RepoTokenOverrideWithPlatformFallbackUpdatesSource(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv("MIDDLEMAN_PLATFORM_TOKEN", "platform-token")
	t.Setenv("MIDDLEMAN_REPO_TOKEN", "repo-token")

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfigPlatformTokenEnv, &mockGH{},
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
	assert.False(ev.RestartRequired)
	newToken, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("repo-token", newToken)
}

func TestConfigReload_RuntimeStripsBootAndReloadedStartupBoundTokenEnvs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	initialConfig := strings.ReplaceAll(
		validReloadConfigRepoTokenEnv,
		"MIDDLEMAN_REPO_TOKEN",
		"MIDDLEMAN_REPO_OLD_TOKEN",
	) + `
[[agents]]
key = "helper"
label = "Helper"
command = ["/bin/echo"]
`
	updatedConfig := strings.ReplaceAll(
		validReloadConfigRepoTokenEnv,
		"MIDDLEMAN_REPO_TOKEN",
		"MIDDLEMAN_REPO_NEW_TOKEN",
	) + `
[[agents]]
key = "helper"
label = "Helper"
command = ["/bin/echo"]
`

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, initialConfig, &mockGH{},
	)
	owner := &msgvaultRuntimeOwner{}
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
	// Token env changes hot-reload through the token sources, so the
	// rename alone must not demand a restart — but both the boot-bound
	// and the reloaded env names must be stripped from future launches.
	assert.False(ev.RestartRequired)

	_, err := srv.runtime.Launch(context.Background(), "ws-1", t.TempDir(), "helper")
	require.NoError(err)
	assert.Contains(owner.startedStripEnvVars, "MIDDLEMAN_REPO_OLD_TOKEN")
	assert.Contains(owner.startedStripEnvVars, "MIDDLEMAN_REPO_NEW_TOKEN")
}

func TestConfigReload_InvalidConfigKeepsLastKnownGood(t *testing.T) {
	assert := assert.New(t)

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfig, &mockGH{},
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

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfig, &mockGH{},
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

func TestSanitizeConfigErrorRedactsTokenMaterial(t *testing.T) {
	assert := assert.New(t)

	got := sanitizeConfigError(
		errors.New("open /home/me/.config/middleman/config.toml: https://x-access-token:ghp_config_secret@github.com/acme/widgets.git failed"),
		"/home/me/.config/middleman/config.toml",
	)

	assert.Contains(got, "config.toml")
	assert.Contains(got, "[REDACTED]")
	assert.NotContains(got, "ghp_config_secret")
	assert.NotContains(got, "x-access-token")
}

func TestConfigReload_NewRepoEntersSyncerTrackedSet(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfig, &mockGH{},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	writeConfigToml(t, cfgPath, validReloadConfigExtraRepo)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)

	tracked := srv.syncer.TrackedRepos()
	owners := make(map[string]struct{}, len(tracked))
	for _, r := range tracked {
		owners[strings.ToLower(r.Owner)+"/"+strings.ToLower(r.Name)] = struct{}{}
	}
	assert.Contains(owners, "globex/engine",
		"new repo from config edit should appear in syncer tracked set")
}

func TestConfigReload_GlobFailureKeepsPreviouslyTrackedMatches(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfig, &mockGH{
			listReposByOwnerFn: func(context.Context, string) ([]*gh.Repository, error) {
				return nil, errors.New("temporary repo listing failure")
			},
		},
	)
	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	srv.syncer.SetRepos([]ghclient.RepoRef{{
		Owner:        "acme",
		Name:         "widget-api",
		PlatformHost: "github.com",
		RepoPath:     "acme/widget-api",
	}})

	writeConfigToml(t, cfgPath, validReloadConfigGlobRepo)

	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid)

	tracked := srv.syncer.TrackedRepos()
	require.Len(tracked, 1)
	assert.Equal("acme", tracked[0].Owner)
	assert.Equal("widget-api", tracked[0].Name)
}

func TestConfigReload_DebouncesBurstedWrites(t *testing.T) {
	assert := assert.New(t)

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfig, &mockGH{},
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
		time.Sleep(10 * time.Millisecond)
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

	srv, _, cfgPath := setupTestServerWithConfigContent(
		t, validReloadConfig, &mockGH{},
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

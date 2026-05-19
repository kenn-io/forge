package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wesm/middleman/internal/apiclient"
	"github.com/wesm/middleman/internal/apiclient/generated"
	"github.com/wesm/middleman/internal/config"
	"github.com/wesm/middleman/internal/db"
	"github.com/wesm/middleman/internal/gitclone"
	ghclient "github.com/wesm/middleman/internal/github"
	"github.com/wesm/middleman/internal/testutil/dbtest"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// writeTmuxRecorder creates an executable fake-tmux script at a
// fresh temp path. The script appends NUL-delimited argv to
// record. For has-session it emits tmux's "can't find session"
// stderr and exits 1 (so EnsureTmux's isTmuxSessionAbsent check
// sees the canonical signal and proceeds to new-session); all
// other invocations exit 0. Returns the script path and the record
// path.
func writeTmuxRecorder(t *testing.T) (script, record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "record")
	script = filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`session_file="${TMUX_RECORD}.sessions"` + "\n" +
		`new_session=""` + "\n" +
		`prev=""` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$prev" = "-s" ]; then new_session="$a"; fi` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    [ -f "$session_file" ] && cat "$session_file"` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "display-message" ]; then` + "\n" +
		`    printf '%s\n' "$TMUX_PANE_TITLE"` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "capture-pane" ]; then` + "\n" +
		`    printf '%s\n' "$TMUX_PANE_OUTPUT"` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		`  prev="$a"` + "\n" +
		"done\n" +
		`if [ -n "$new_session" ]; then printf '%s\n' "$new_session" >> "$session_file"; fi` + "\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)
	return script, record
}

func readTmuxRecord(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	// Split on NUL. Each record is "<argc>\0<arg0>\0<arg1>\0...\0",
	// so a flushed stream always ends with a trailing \0 and Split
	// produces a final empty element after it. Strip exactly one
	// trailing empty so we don't mistake it for part of the next
	// record. Interior empty elements are real args (the NUL framing
	// exists to preserve them) and must NOT be skipped.
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	var out [][]string
	for i := 0; i < len(parts); {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			// Trailing record is mid-write: argc isn't a valid
			// integer yet. Stop; the next poll will see the full
			// record once the recorder script flushes.
			break
		}
		if i+1+n > len(parts) {
			// argc is parsed but not all args are on disk yet.
			// Same treatment: defer to the next poll.
			break
		}
		i++
		argv := parts[i : i+n]
		out = append(out, argv)
		i += n
	}
	return out
}

func containsArg(argv []string, want string) bool {
	return slices.Contains(argv, want)
}

func argAfter(argv []string, flag string) (string, bool) {
	for i, arg := range argv {
		if arg == flag && i+1 < len(argv) {
			return argv[i+1], true
		}
	}
	return "", false
}

// setupWrapperServer constructs a full server wired with a
// recording-script tmux command, a bare repo, and a seeded PR.
// Returns a generated API client pointed at the httptest server,
// the httptest baseURL (needed for WebSocket dialing), and the
// record-file path.
func setupWrapperServer(t *testing.T) (client *apiclient.Client, baseURL, record string) {
	t.Helper()
	script, record := writeTmuxRecorder(t)
	client, baseURL = setupWrapperServerWithScript(t, script)
	return client, baseURL, record
}

// setupWrapperServerWithScript is setupWrapperServer parameterized
// by the tmux-command path. Tests that want a non-default wrapper
// (e.g. one that fails has-session with a non-1 exit code) write
// their own script first and call this helper instead.
func setupWrapperServerWithScript(
	t *testing.T, script string,
) (client *apiclient.Client, baseURL string) {
	t.Helper()
	client, baseURL, _ = setupWrapperServerWithScriptAndDB(
		t, script,
	)
	return client, baseURL
}

func setupWrapperServerWithScriptAndDB(
	t *testing.T, script string,
) (client *apiclient.Client, baseURL string, database *db.DB) {
	t.Helper()
	client, baseURL, database, _ = setupWrapperServerWithScriptAndDBAndServer(
		t, script,
	)
	return client, baseURL, database
}

func setupWrapperServerWithScriptAndDBAndServer(
	t *testing.T, script string,
) (
	client *apiclient.Client,
	baseURL string,
	database *db.DB,
	srv *Server,
) {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e tests skipped in short mode")
	}

	dir := t.TempDir()
	database = dbtest.Open(t)

	bareDir := filepath.Join(dir, "clones")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	bare := filepath.Join(
		bareDir, "github.com", "acme", "widget.git",
	)
	tmpWork := filepath.Join(dir, "work")
	runGit(t, dir, "init", "--bare", "--initial-branch=main", bare)
	runGit(t, dir, "clone", bare, tmpWork)
	runGit(t, tmpWork, "config", "user.email", "test@test.com")
	runGit(t, tmpWork, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpWork, "base.txt"),
		[]byte("base\n"), 0o644,
	))
	runGit(t, tmpWork, "add", ".")
	runGit(t, tmpWork, "commit", "-m", "base commit")
	runGit(t, tmpWork, "push", "origin", "main")
	runGit(t, tmpWork, "checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpWork, "new.txt"),
		[]byte("new\n"), 0o644,
	))
	runGit(t, tmpWork, "add", ".")
	runGit(t, tmpWork, "commit", "-m", "feature commit")
	runGit(t, tmpWork, "push", "origin", "feature")

	// Point bare origin at itself so EnsureClone fetch works.
	runGit(t, bare, "remote", "add", "origin", bare)

	clones := gitclone.New(bareDir, nil)
	worktreeDir := filepath.Join(dir, "worktrees")

	repos := []ghclient.RepoRef{
		{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
	}
	mock := &mockGH{}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	cfg := &config.Config{
		Tmux: config.Tmux{
			Command: []string{script, "wrap"},
		},
	}
	srv = New(database, syncer, nil, "/", cfg, ServerOptions{
		Clones:      clones,
		WorktreeDir: worktreeDir,
	})
	seedPR(t, database, "acme", "widget", 1)

	// Real listener — WebSocket Dial needs a real TCP endpoint.
	// The generated API client also points at this URL rather than
	// the in-process roundtripper used elsewhere, because we cannot
	// split HTTP and WebSocket transports per-request.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()
	baseURL = "http://" + ln.Addr().String()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()
		require.NoError(t, srv.Shutdown(ctx))
		select {
		case err := <-serveErr:
			require.ErrorIs(t, err, http.ErrServerClosed)
		case <-ctx.Done():
			require.FailNow(t, "workspace wrapper server did not stop")
		}
	})

	// Wrap the underlying TCP transport with the same Content-Type
	// shim setupTestClient uses — the server's CSRF check rejects
	// non-GET requests without Content-Type (e.g. DELETE with no
	// body) as 415 Unsupported Media Type. The shim runs in addition
	// to the normal transport, which still reaches the httptest
	// server over TCP so WebSocket upgrades continue to work.
	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet && req.Header.Get("Content-Type") == "" {
				req.Header.Set("Content-Type", "application/json")
			}
			return http.DefaultTransport.RoundTrip(req)
		}),
	}
	client, err = apiclient.NewWithHTTPClient(baseURL, httpClient)
	require.NoError(t, err)

	return client, baseURL, database, srv
}

func TestTmuxWrapperNewSession(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	client, _, record := setupWrapperServer(t)

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		t.Context(),
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)

	// Workspace setup runs asynchronously. Poll the record file
	// until the new-session invocation shows up, up to ~5s.
	var argvs [][]string
	require.Eventually(
		func() bool {
			argvs = readTmuxRecord(t, record)
			for _, argv := range argvs {
				if len(argv) >= 2 && argv[1] == "new-session" {
					return true
				}
			}
			return false
		},
		5*time.Second, 50*time.Millisecond,
		"new-session argv not recorded",
	)

	var newSession []string
	for _, argv := range argvs {
		if len(argv) >= 2 && argv[1] == "new-session" {
			newSession = argv
			break
		}
	}

	// "wrap" prefix, then "new-session -d -s <id> -c <path> <shell> -l"
	require.GreaterOrEqual(len(newSession), 9)
	assert.Equal("wrap", newSession[0])
	assert.Equal("new-session", newSession[1])
	assert.Equal("-d", newSession[2])
	assert.Equal("-s", newSession[3])
	assert.NotEmpty(newSession[4])
	assert.Equal("-c", newSession[5])
	assert.NotEmpty(newSession[6])
	assert.NotEmpty(newSession[7])
	assert.Equal("-l", newSession[8])
}

func TestWorkspaceResponseIncludesTmuxWorkingState(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	t.Setenv("TMUX_PANE_TITLE", "⠴ t3code-b5014b03")
	t.Setenv("TMUX_PANE_OUTPUT", "stable output")

	client, _, _ := setupWrapperServer(t)
	ctx := context.Background()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	waitForWorkspaceReady(t, ctx, client, wsID)

	getResp, err := client.HTTP.GetWorkspace(ctx, wsID)
	require.NoError(err)
	defer getResp.Body.Close()
	require.Equal(http.StatusOK, getResp.StatusCode)

	var got struct {
		TmuxPaneTitle *string `json:"tmux_pane_title"`
		TmuxWorking   bool    `json:"tmux_working"`
	}
	require.NoError(json.NewDecoder(getResp.Body).Decode(&got))
	require.NotNil(got.TmuxPaneTitle)
	assert.Equal("⠴ t3code-b5014b03", *got.TmuxPaneTitle)
	assert.True(got.TmuxWorking)

	listResp, err := client.HTTP.ListWorkspaces(ctx)
	require.NoError(err)
	defer listResp.Body.Close()
	require.Equal(http.StatusOK, listResp.StatusCode)

	var listed struct {
		Workspaces []struct {
			ID            string  `json:"id"`
			TmuxPaneTitle *string `json:"tmux_pane_title"`
			TmuxWorking   bool    `json:"tmux_working"`
		} `json:"workspaces"`
	}
	require.NoError(json.NewDecoder(listResp.Body).Decode(&listed))
	require.Len(listed.Workspaces, 1)
	assert.Equal(wsID, listed.Workspaces[0].ID)
	require.NotNil(listed.Workspaces[0].TmuxPaneTitle)
	assert.Equal("⠴ t3code-b5014b03", *listed.Workspaces[0].TmuxPaneTitle)
	assert.True(listed.Workspaces[0].TmuxWorking)
}

func TestWorkspaceResponseTracksTmuxOutputActivity(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "pane-output")
	require.NoError(os.WriteFile(outputPath, []byte("initial\n"), 0o644))

	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    printf '%s\n' "$TMUX_LIVE_SESSIONS"` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "display-message" ]; then` + "\n" +
		`    printf '%s\n' "$TMUX_PANE_TITLE"` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "capture-pane" ]; then` + "\n" +
		`    cat "$TMUX_PANE_OUTPUT_FILE"` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_PANE_TITLE", "workspace")
	t.Setenv("TMUX_PANE_OUTPUT_FILE", outputPath)

	client, _, _, srv := setupWrapperServerWithScriptAndDBAndServer(
		t, script,
	)
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	var clockNow atomic.Int64
	clockNow.Store(now.UnixNano())
	setClock := func(t time.Time) {
		clockNow.Store(t.UTC().UnixNano())
	}
	srv.tmuxActivity = newTmuxActivityTracker(func() time.Time {
		return time.Unix(0, clockNow.Load()).UTC()
	})
	ctx := context.Background()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	waitForWorkspaceReady(t, ctx, client, wsID)

	first := getRawWorkspaceActivity(t, client, ctx, wsID)
	require.NotNil(first.TmuxPaneTitle)
	assert.Equal("workspace", *first.TmuxPaneTitle)
	assert.False(first.TmuxWorking)
	assert.Equal(tmuxActivitySourceNone, first.TmuxActivitySource)
	assert.Nil(first.TmuxLastOutputAt)

	require.NoError(os.WriteFile(
		outputPath,
		[]byte("initial\nnew output\n"),
		0o644,
	))
	probeAt := now.Add(tmuxSampleMinInterval + time.Second)
	setClock(probeAt)
	var second struct {
		TmuxPaneTitle      *string `json:"tmux_pane_title"`
		TmuxWorking        bool    `json:"tmux_working"`
		TmuxActivitySource string  `json:"tmux_activity_source"`
		TmuxLastOutputAt   *string `json:"tmux_last_output_at"`
	}
	require.Eventually(func() bool {
		second = getRawWorkspaceActivity(t, client, ctx, wsID)
		if second.TmuxWorking &&
			second.TmuxActivitySource == tmuxActivitySourceOutput &&
			second.TmuxLastOutputAt != nil {
			return true
		}
		probeAt = probeAt.Add(tmuxSampleMinInterval + time.Second)
		setClock(probeAt)
		return false
	}, time.Second, 10*time.Millisecond)
	assert.True(second.TmuxWorking)
	assert.Equal(tmuxActivitySourceOutput, second.TmuxActivitySource)
	require.NotNil(second.TmuxLastOutputAt)
	assert.Equal(probeAt.Format(time.RFC3339), *second.TmuxLastOutputAt)

	lastOutputAt, err := time.Parse(time.RFC3339, *second.TmuxLastOutputAt)
	require.NoError(err)
	setClock(lastOutputAt.Add(tmuxActivityTTL + time.Second))
	expired := getRawWorkspaceActivity(t, client, ctx, wsID)
	assert.False(expired.TmuxWorking)
	assert.Equal(tmuxActivitySourceNone, expired.TmuxActivitySource)
	require.NotNil(expired.TmuxLastOutputAt)
	assert.Equal(*second.TmuxLastOutputAt, *expired.TmuxLastOutputAt)
}

func TestListWorkspacesFetchesTmuxActivityConcurrently(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	activeDir := filepath.Join(dir, "active")
	overlapPath := filepath.Join(dir, "overlap")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    printf '%s\n' "$TMUX_LIVE_SESSIONS"` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "display-message" ] || [ "$a" = "capture-pane" ]; then` + "\n" +
		`    mkdir -p "$TMUX_ACTIVE_DIR"` + "\n" +
		`    marker="$TMUX_ACTIVE_DIR/$$"` + "\n" +
		`    : > "$marker"` + "\n" +
		`    set -- "$TMUX_ACTIVE_DIR"/*` + "\n" +
		`    if [ "$#" -gt 1 ]; then` + "\n" +
		`      : > "$TMUX_OVERLAP_FILE"` + "\n" +
		`    fi` + "\n" +
		`    sleep 0.2` + "\n" +
		`    rm -f "$marker"` + "\n" +
		`    if [ "$a" = "display-message" ]; then` + "\n" +
		`      printf 'workspace\n'` + "\n" +
		`      exit 0` + "\n" +
		`    fi` + "\n" +
		`    printf 'output\n'` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_ACTIVE_DIR", activeDir)
	t.Setenv("TMUX_OVERLAP_FILE", overlapPath)

	client, _, database, srv := setupWrapperServerWithScriptAndDBAndServer(
		t, script,
	)
	srv.tmuxActivity = newTmuxActivityTracker(func() time.Time {
		return time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	})
	ctx := context.Background()

	seedPR(t, database, "acme", "widget", 2)

	createResp1, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp1.StatusCode())
	require.NotNil(createResp1.JSON202)
	waitForWorkspaceReady(t, ctx, client, createResp1.JSON202.Id)

	createResp2, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     2,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp2.StatusCode())
	require.NotNil(createResp2.JSON202)
	waitForWorkspaceReady(t, ctx, client, createResp2.JSON202.Id)
	t.Setenv(
		"TMUX_LIVE_SESSIONS",
		strings.Join([]string{
			"middleman-" + createResp1.JSON202.Id,
			"middleman-" + createResp2.JSON202.Id,
		}, "\n"),
	)
	srv.tmuxActivity = newTmuxActivityTracker(func() time.Time {
		return time.Date(2026, 4, 23, 12, 0, 5, 0, time.UTC)
	})
	require.NoError(os.RemoveAll(activeDir))
	err = os.Remove(overlapPath)
	if err != nil && !os.IsNotExist(err) {
		require.NoError(err)
	}

	resp, err := client.HTTP.ListWorkspaces(ctx)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)

	var listed struct {
		Workspaces []struct {
			ID string `json:"id"`
		} `json:"workspaces"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&listed))
	require.Len(listed.Workspaces, 2)

	_, err = os.Stat(overlapPath)
	assert.NoError(err, "expected overlapping tmux activity probes")
}

func TestWorkspaceListReturnsUnknownWhenTmuxActivityProbeTimesOut(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "display-message" ] || [ "$a" = "capture-pane" ]; then` + "\n" +
		`    exec sleep 5` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	client, _, database, srv := setupWrapperServerWithScriptAndDBAndServer(
		t, script,
	)
	ctx := context.Background()
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:              "ws-probe-timeout",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      1,
		GitHeadRef:      "feature",
		WorkspaceBranch: "feature",
		WorktreePath:    filepath.Join(dir, "worktree"),
		TmuxSession:     "middleman-ws-timeout",
		Status:          "ready",
	}))
	srv.tmuxActivity = newTmuxActivityTracker(func() time.Time {
		return time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	})

	requestCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	resp, err := client.HTTP.ListWorkspaces(requestCtx)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)

	var listed struct {
		Workspaces []struct {
			ID                 string  `json:"id"`
			TmuxPaneTitle      *string `json:"tmux_pane_title"`
			TmuxWorking        bool    `json:"tmux_working"`
			TmuxActivitySource string  `json:"tmux_activity_source"`
		} `json:"workspaces"`
	}
	require.NoError(json.NewDecoder(resp.Body).Decode(&listed))
	require.Len(listed.Workspaces, 1)
	assert.Equal("ws-probe-timeout", listed.Workspaces[0].ID)
	assert.Nil(listed.Workspaces[0].TmuxPaneTitle)
	assert.False(listed.Workspaces[0].TmuxWorking)
	assert.Equal(tmuxActivitySourceUnknown, listed.Workspaces[0].TmuxActivitySource)
}

func TestConcurrentWorkspaceListsCoalesceTmuxActivityProbe(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    printf '%s\n' "$TMUX_LIVE_SESSIONS"` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		`done` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "display-message" ]; then` + "\n" +
		`    sleep 0.2` + "\n" +
		`    printf 'workspace\n'` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "capture-pane" ]; then` + "\n" +
		`    sleep 0.2` + "\n" +
		`    printf 'output\n'` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)
	t.Setenv("TMUX_LIVE_SESSIONS", "middleman-ws-coalesce")

	client, _, database, srv := setupWrapperServerWithScriptAndDBAndServer(
		t, script,
	)
	ctx := context.Background()
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:              "ws-coalesce",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      1,
		GitHeadRef:      "feature",
		WorkspaceBranch: "feature",
		WorktreePath:    filepath.Join(dir, "worktree"),
		TmuxSession:     "middleman-ws-coalesce",
		Status:          "ready",
	}))

	srv.tmuxActivity = newTmuxActivityTracker(func() time.Time {
		return time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	})

	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	errs := make(chan error, 2)
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			resp, getErr := client.HTTP.ListWorkspaces(ctx)
			errs <- getErr
			if resp != nil {
				resp.Body.Close()
				statuses <- resp.StatusCode
			}
		}()
	}
	wg.Wait()
	close(errs)
	close(statuses)

	for getErr := range errs {
		require.NoError(getErr)
	}
	for status := range statuses {
		assert.Equal(http.StatusOK, status)
	}

	var displayMessageCalls int
	var capturePaneCalls int
	for _, argv := range readTmuxRecord(t, record) {
		if containsArg(argv, "display-message") {
			displayMessageCalls++
		}
		if containsArg(argv, "capture-pane") {
			capturePaneCalls++
		}
	}
	assert.Equal(1, displayMessageCalls)
	assert.Equal(1, capturePaneCalls)
}

func TestWorkspaceListTmuxActivityRefreshesEveryReadyWorkspace(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    printf '%s\n' "$TMUX_LIVE_SESSIONS"` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		`done` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "display-message" ]; then` + "\n" +
		`    printf 'workspace\n'` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "capture-pane" ]; then` + "\n" +
		`    printf 'output\n'` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	client, _, database, srv := setupWrapperServerWithScriptAndDBAndServer(
		t, script,
	)
	ctx := context.Background()
	wantSessions := make(map[string]bool)
	for i := range tmuxProbeMaxConcurrency + 4 {
		session := "middleman-ws-refresh-" + strconv.Itoa(i)
		wantSessions[session] = true
		require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
			ID:              "ws-refresh-" + strconv.Itoa(i),
			PlatformHost:    "github.com",
			RepoOwner:       "acme",
			RepoName:        "widget",
			ItemType:        db.WorkspaceItemTypePullRequest,
			ItemNumber:      200 + i,
			GitHeadRef:      "feature",
			WorkspaceBranch: "feature",
			WorktreePath: filepath.Join(
				dir, "refresh-worktree-"+strconv.Itoa(i),
			),
			TmuxSession: session,
			Status:      "ready",
		}))
	}
	liveSessions := make([]string, 0, len(wantSessions))
	for session := range wantSessions {
		liveSessions = append(liveSessions, session)
	}
	t.Setenv("TMUX_LIVE_SESSIONS", strings.Join(liveSessions, "\n"))
	srv.tmuxActivity = newTmuxActivityTracker(func() time.Time {
		return time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	})

	resp, err := client.HTTP.ListWorkspaces(ctx)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)

	gotSessions := make(map[string]bool)
	for _, argv := range readTmuxRecord(t, record) {
		if !containsArg(argv, "capture-pane") {
			continue
		}
		session, ok := argAfter(argv, "-t")
		if ok {
			gotSessions[session] = true
		}
	}
	assert.Equal(wantSessions, gotSessions)
}

func TestWorkspaceListTmuxActivityStressDoesNotLeakProcesses(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	activeDir := filepath.Join(dir, "active")
	violationPath := filepath.Join(dir, "violation")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "display-message" ] || [ "$a" = "capture-pane" ]; then` + "\n" +
		`    mkdir -p "$TMUX_ACTIVE_DIR"` + "\n" +
		`    marker="$TMUX_ACTIVE_DIR/$$"` + "\n" +
		`    : > "$marker"` + "\n" +
		`    trap 'rm -f "$marker"' EXIT INT TERM` + "\n" +
		`    active=$(find "$TMUX_ACTIVE_DIR" -type f | wc -l | tr -d ' ')` + "\n" +
		`    if [ "$active" -gt "$TMUX_MAX_ACTIVE" ]; then` + "\n" +
		`      printf '%s\n' "$active" >> "$TMUX_VIOLATION"` + "\n" +
		`    fi` + "\n" +
		`    sleep 0.005` + "\n" +
		`    if [ "$a" = "display-message" ]; then` + "\n" +
		`      printf 'workspace\n'` + "\n" +
		`      exit 0` + "\n" +
		`    fi` + "\n" +
		`    printf 'output\n'` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_ACTIVE_DIR", activeDir)
	t.Setenv("TMUX_MAX_ACTIVE", strconv.Itoa(tmuxProbeMaxConcurrency))
	t.Setenv("TMUX_VIOLATION", violationPath)

	client, _, database, srv := setupWrapperServerWithScriptAndDBAndServer(
		t, script,
	)
	ctx := context.Background()
	for i := range 12 {
		mrNumber := 100 + i
		require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
			ID:              "ws-stress-" + strconv.Itoa(i),
			PlatformHost:    "github.com",
			RepoOwner:       "acme",
			RepoName:        "widget",
			ItemType:        db.WorkspaceItemTypePullRequest,
			ItemNumber:      mrNumber,
			GitHeadRef:      "feature",
			WorkspaceBranch: "feature",
			WorktreePath: filepath.Join(
				dir, "worktree-"+strconv.Itoa(i),
			),
			TmuxSession: "middleman-ws-stress-" + strconv.Itoa(i),
			Status:      "ready",
		}))
	}
	srv.tmuxActivity = newTmuxActivityTracker(func() time.Time {
		return time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	})

	var wg sync.WaitGroup
	errs := make(chan error, 48)
	statuses := make(chan int, 48)
	wg.Add(48)
	for range 48 {
		go func() {
			defer wg.Done()
			resp, getErr := client.HTTP.ListWorkspaces(ctx)
			errs <- getErr
			if resp != nil {
				resp.Body.Close()
				statuses <- resp.StatusCode
			}
		}()
	}
	wg.Wait()
	close(errs)
	close(statuses)

	for getErr := range errs {
		require.NoError(getErr)
	}
	for status := range statuses {
		assert.Equal(http.StatusOK, status)
	}

	assert.NoFileExists(violationPath)
	require.Eventually(func() bool {
		entries, err := os.ReadDir(activeDir)
		if os.IsNotExist(err) {
			return true
		}
		require.NoError(err)
		return len(entries) == 0
	}, time.Second, 10*time.Millisecond)
}

func getRawWorkspaceActivity(
	t *testing.T,
	client *apiclient.Client,
	ctx context.Context,
	wsID string,
) struct {
	TmuxPaneTitle      *string `json:"tmux_pane_title"`
	TmuxWorking        bool    `json:"tmux_working"`
	TmuxActivitySource string  `json:"tmux_activity_source"`
	TmuxLastOutputAt   *string `json:"tmux_last_output_at"`
} {
	t.Helper()
	resp, err := client.HTTP.GetWorkspace(ctx, wsID)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got struct {
		TmuxPaneTitle      *string `json:"tmux_pane_title"`
		TmuxWorking        bool    `json:"tmux_working"`
		TmuxActivitySource string  `json:"tmux_activity_source"`
		TmuxLastOutputAt   *string `json:"tmux_last_output_at"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	return got
}

func TestIsWorkingTmuxTitleDetectsCodexSpinner(t *testing.T) {
	assert := Assert.New(t)

	cases := []struct {
		name    string
		title   string
		working bool
	}{
		{
			name:    "codex spinner frame",
			title:   "⠴ t3code-b5014b03",
			working: true,
		},
		{
			name:    "another codex spinner frame",
			title:   "⠦ t3code-b5014b03",
			working: true,
		},
		{
			name:    "settled codex title",
			title:   "t3code-b5014b03",
			working: false,
		},
		{
			name:    "english busy title is not protocol",
			title:   "codex working",
			working: false,
		},
		{
			name:    "opencode style title is not protocol",
			title:   "OC | Run sleep 10",
			working: false,
		},
		{
			name:    "pi style title is not protocol",
			title:   "π - tmp.foo",
			working: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(tc.working, isWorkingTmuxTitle(tc.title))
		})
	}
}

func TestWorkspaceCreateFailureLogsAndPersistsAuditEvent(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "new-session" ]; then` + "\n" +
		`    echo "wrapper failed" >&2` + "\n" +
		`    exit 42` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	var logBuf lockedBuffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })

	client, _, database := setupWrapperServerWithScriptAndDB(
		t, script,
	)
	ctx := t.Context()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	failed := waitForWorkspaceStatus(t, ctx, client, wsID, "error")
	require.NotNil(failed.ErrorMessage)
	assert.Contains(*failed.ErrorMessage, "tmux new-session")
	assert.Contains(*failed.ErrorMessage, "wrapper failed")

	rows, err := database.ReadDB().QueryContext(ctx, `
		SELECT stage, outcome, message
		FROM middleman_workspace_setup_events
		WHERE workspace_id = ?
		ORDER BY id`, wsID,
	)
	require.NoError(err)
	defer rows.Close()

	type auditEvent struct {
		stage   string
		outcome string
		message string
	}

	var events []auditEvent
	for rows.Next() {
		var ev auditEvent
		require.NoError(rows.Scan(&ev.stage, &ev.outcome, &ev.message))
		events = append(events, ev)
	}
	require.NoError(rows.Err())
	require.NotEmpty(events)
	last := events[len(events)-1]
	assert.Equal("tmux_session", last.stage)
	assert.Equal("failure", last.outcome)
	assert.Contains(last.message, "wrapper failed")

	logs := logBuf.String()
	assert.Contains(logs, "workspace setup failed")
	assert.Contains(logs, wsID)
	assert.Contains(logs, "tmux_session")
}

func TestWorkspaceShutdownCancellationPersistsFailureViaAPI(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "new-session" ]; then` + "\n" +
		`    while :; do sleep 1; done` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	client, _, database, srv := setupWrapperServerWithScriptAndDBAndServer(
		t, script,
	)
	ctx := t.Context()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	require.Eventually(
		func() bool {
			argvs := readTmuxRecord(t, record)
			for _, argv := range argvs {
				if len(argv) >= 2 && argv[1] == "new-session" {
					return true
				}
			}
			return false
		},
		5*time.Second,
		50*time.Millisecond,
	)

	shutdownCtx, cancel := context.WithTimeout(
		t.Context(), 5*time.Second,
	)
	defer cancel()
	require.NoError(srv.Shutdown(shutdownCtx))

	restartSyncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(restartSyncer.Stop)
	restarted := New(
		database, restartSyncer, nil, "/",
		nil, ServerOptions{WorktreeDir: filepath.Join(dir, "restart-worktrees")},
	)
	t.Cleanup(func() { gracefulShutdown(t, restarted) })
	restartedClient := setupTestClient(t, restarted)

	getResp, err := restartedClient.HTTP.GetWorkspaceWithResponse(
		ctx, wsID,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, getResp.StatusCode())
	require.NotNil(getResp.JSON200)
	assert.Equal("error", getResp.JSON200.Status)
	require.NotNil(getResp.JSON200.ErrorMessage)
	assert.Contains(*getResp.JSON200.ErrorMessage, "tmux new-session")

	rows, err := database.ReadDB().QueryContext(ctx, `
		SELECT stage, outcome, message
		FROM middleman_workspace_setup_events
		WHERE workspace_id = ?
		ORDER BY id`, wsID,
	)
	require.NoError(err)
	defer rows.Close()

	type auditEvent struct {
		stage   string
		outcome string
		message string
	}

	var events []auditEvent
	for rows.Next() {
		var ev auditEvent
		require.NoError(rows.Scan(&ev.stage, &ev.outcome, &ev.message))
		events = append(events, ev)
	}
	require.NoError(rows.Err())
	require.Len(events, 2)
	assert.Equal("setup", events[0].stage)
	assert.Equal("started", events[0].outcome)
	assert.Equal("tmux_session", events[1].stage)
	assert.Equal("failure", events[1].outcome)
	assert.Contains(events[1].message, "tmux new-session")
}

func TestWorkspaceSetupFailureRollbackCleansWorktreeViaAPI(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "new-session" ]; then` + "\n" +
		`    echo "wrapper failed" >&2` + "\n" +
		`    exit 42` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	client, _, database, srv := setupWrapperServerWithScriptAndDBAndServer(
		t, script,
	)
	ctx := t.Context()
	clonePath, err := srv.clones.ClonePath("github.com", "acme", "widget")
	require.NoError(err)
	featureSHA := testGitSHA(t, clonePath, "refs/heads/feature")

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	var failed *generated.WorkspaceResponse
	require.Eventually(
		func() bool {
			getResp, getErr := client.HTTP.GetWorkspaceWithResponse(
				ctx, wsID,
			)
			require.NoError(getErr)
			if getResp.StatusCode() != http.StatusOK || getResp.JSON200 == nil {
				return false
			}
			if getResp.JSON200.Status != "error" {
				return false
			}
			failed = getResp.JSON200
			return true
		},
		5*time.Second,
		50*time.Millisecond,
	)

	require.NotNil(failed)
	assert.Equal(featureSHA, testGitSHA(t, clonePath, "refs/heads/feature"))
	assert.Eventually(
		func() bool {
			_, err := os.Stat(failed.WorktreePath)
			return os.IsNotExist(err)
		},
		5*time.Second,
		50*time.Millisecond,
	)

	stored, err := database.GetWorkspace(ctx, wsID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("error", stored.Status)
	assert.Empty(stored.WorkspaceBranch)
}

func TestWorkspaceRetryWhileCreatingQueuesAndRunsAfterFailureViaAPI(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	release := filepath.Join(dir, "release-first")
	countFile := filepath.Join(dir, "new-session-count")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "new-session" ]; then` + "\n" +
		`    count=0` + "\n" +
		`    if [ -f "$TMUX_COUNT" ]; then count=$(cat "$TMUX_COUNT"); fi` + "\n" +
		`    count=$((count + 1))` + "\n" +
		`    printf '%s' "$count" > "$TMUX_COUNT"` + "\n" +
		`    if [ "$count" = "1" ]; then` + "\n" +
		`      while [ ! -f "$TMUX_RELEASE" ]; do sleep 0.05; done` + "\n" +
		`      echo "first setup failed" >&2` + "\n" +
		`      exit 42` + "\n" +
		`    fi` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)
	t.Setenv("TMUX_RELEASE", release)
	t.Setenv("TMUX_COUNT", countFile)

	client, _, database := setupWrapperServerWithScriptAndDB(
		t, script,
	)
	ctx := context.Background()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	require.Eventually(
		func() bool {
			argvs := readTmuxRecord(t, record)
			for _, argv := range argvs {
				if len(argv) >= 2 && argv[1] == "new-session" {
					return true
				}
			}
			return false
		},
		5*time.Second,
		50*time.Millisecond,
	)

	retryResp, err := client.HTTP.RetryWorkspaceWithResponse(ctx, wsID)
	require.NoError(err)
	require.Equal(http.StatusAccepted, retryResp.StatusCode())
	require.NotNil(retryResp.JSON202)
	assert.Equal("creating", retryResp.JSON202.Status)

	require.NoError(os.WriteFile(release, []byte("go\n"), 0o644))

	var ready *generated.WorkspaceResponse
	require.Eventually(
		func() bool {
			getResp, getErr := client.HTTP.GetWorkspaceWithResponse(
				ctx, wsID,
			)
			require.NoError(getErr)
			if getResp.StatusCode() != http.StatusOK || getResp.JSON200 == nil {
				return false
			}
			if getResp.JSON200.Status != "ready" {
				return false
			}
			ready = getResp.JSON200
			return true
		},
		5*time.Second,
		50*time.Millisecond,
	)
	require.NotNil(ready)
	assert.Nil(ready.ErrorMessage)

	argvs := readTmuxRecord(t, record)
	var newSessionCount int
	for _, argv := range argvs {
		if len(argv) >= 2 && argv[1] == "new-session" {
			newSessionCount++
		}
	}
	assert.Equal(2, newSessionCount)

	events, err := database.ListWorkspaceSetupEvents(ctx, wsID)
	require.NoError(err)
	var retryEvents int
	for _, event := range events {
		if event.Stage == "setup" && event.Outcome == "retrying" {
			retryEvents++
		}
	}
	assert.Equal(1, retryEvents)
}

func TestWorkspaceShutdownCancellationDoesNotPersistAfterDeadlineBudgetExhausted(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "new-session" ]; then` + "\n" +
		`    while :; do sleep 1; done` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	client, baseURL, database, srv := setupWrapperServerWithScriptAndDBAndServer(
		t, script,
	)

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		t.Context(),
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	require.Eventually(
		func() bool {
			argvs := readTmuxRecord(t, record)
			for _, argv := range argvs {
				if len(argv) >= 2 && argv[1] == "new-session" {
					return true
				}
			}
			return false
		},
		5*time.Second,
		50*time.Millisecond,
	)

	tx, err := database.WriteDB().BeginTx(t.Context(), nil)
	require.NoError(err)
	t.Cleanup(func() { _ = tx.Rollback() })

	origHandler := srv.handler
	blockStarted := make(chan struct{}, 1)
	blockRelease := make(chan struct{})
	srv.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/block" {
			select {
			case blockStarted <- struct{}{}:
			default:
			}
			<-blockRelease
			w.WriteHeader(http.StatusOK)
			return
		}
		origHandler.ServeHTTP(w, r)
	})

	blockErrCh := make(chan error, 1)
	go func() {
		resp, err := http.Get(baseURL + "/block")
		if err == nil && resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		blockErrCh <- err
	}()

	select {
	case <-blockStarted:
	case <-time.After(2 * time.Second):
		require.FailNow("blocking request never started")
	}

	time.AfterFunc(250*time.Millisecond, func() {
		close(blockRelease)
	})

	shutdownCtx, cancel := context.WithTimeout(
		t.Context(), 400*time.Millisecond,
	)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	require.ErrorIs(err, context.DeadlineExceeded)

	require.NoError(tx.Rollback())

	require.NoError(<-blockErrCh)

	ws, err := database.GetWorkspace(t.Context(), wsID)
	require.NoError(err)
	require.NotNil(ws)
	assert.Equal("creating", ws.Status)
	assert.Nil(ws.ErrorMessage)

	events, err := database.ListWorkspaceSetupEvents(
		t.Context(), wsID,
	)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("setup", events[0].Stage)
	assert.Equal("started", events[0].Outcome)

	longCtx, longCancel := context.WithTimeout(
		t.Context(), 2*time.Second,
	)
	defer longCancel()
	require.NoError(srv.Shutdown(longCtx))
}

func TestTmuxWrapperAttachSession(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	client, baseURL, record := setupWrapperServer(t)
	ctx := t.Context()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	// Poll for status == "ready".
	require.Eventually(
		func() bool {
			getResp, getErr := client.HTTP.GetWorkspaceWithResponse(
				ctx, wsID,
			)
			if getErr != nil || getResp.JSON200 == nil {
				return false
			}
			return getResp.JSON200.Status == "ready"
		},
		5*time.Second, 50*time.Millisecond,
	)

	// Connect to the WebSocket terminal endpoint using the
	// httptest baseURL (the generated client cannot upgrade to
	// WebSocket, so we dial directly with coder/websocket).
	wsURL := strings.Replace(baseURL, "http://", "ws://", 1) +
		"/ws/v1/workspaces/" + wsID + "/terminal"
	dialCtx, dialCancel := context.WithTimeout(
		ctx, 3*time.Second,
	)
	defer dialCancel()
	u, err := url.Parse(wsURL)
	require.NoError(err)
	conn, httpResp, err := websocket.Dial(
		dialCtx, u.String(), nil,
	)
	require.NoError(err)
	if httpResp != nil && httpResp.Body != nil {
		httpResp.Body.Close()
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// The recording script exits 0 immediately, so the PTY
	// closes and the handler sends an "exited" message. Read
	// until the connection closes or 3s elapses.
	readCtx, readCancel := context.WithTimeout(
		ctx, 3*time.Second,
	)
	defer readCancel()
	for {
		_, _, readErr := conn.Read(readCtx)
		if readErr != nil {
			break
		}
	}

	// The recorded argv should contain an attach-session invocation
	// with our "wrap" prefix.
	var attach []string
	for _, argv := range readTmuxRecord(t, record) {
		if len(argv) >= 2 && argv[1] == "attach-session" {
			attach = argv
			break
		}
	}
	require.NotNil(attach, "attach-session argv not recorded")
	require.Len(attach, 4)
	assert.Equal("wrap", attach[0])
	assert.Equal("attach-session", attach[1])
	assert.Equal("-t", attach[2])
	assert.NotEmpty(attach[3])
}

func TestTerminalRouteE2EPropagatesWorkspaceID(t *testing.T) {
	assert := Assert.New(t)
	_, baseURL, _ := setupWrapperServer(t)

	resp, err := http.Get(
		baseURL + "/api/v1/workspaces/not-present/terminal",
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(http.StatusNotFound, resp.StatusCode)
	assert.Contains(string(body), "workspace not found")
}

func TestWorkspaceSetupResourceExhaustionGetsHelpfulErrorViaAPI(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "new-session" ]; then` + "\n" +
		`    echo "fork/exec /opt/homebrew/bin/tmux: resource temporarily unavailable" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	client, _, _ := setupWrapperServerWithScriptAndDB(t, script)
	ctx := context.Background()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	var failed *generated.WorkspaceResponse
	require.Eventually(
		func() bool {
			getResp, getErr := client.HTTP.GetWorkspaceWithResponse(
				ctx, wsID,
			)
			require.NoError(getErr)
			if getResp.StatusCode() != http.StatusOK || getResp.JSON200 == nil {
				return false
			}
			if getResp.JSON200.Status != "error" {
				return false
			}
			failed = getResp.JSON200
			return true
		},
		5*time.Second, 50*time.Millisecond,
	)
	require.NotNil(failed)
	require.NotNil(failed.ErrorMessage)
	assert.Contains(*failed.ErrorMessage, "host process limit reached")
}

// TestReadTmuxRecordPreservesEmptyArgs pins down the parser's
// empty-arg handling. The NUL-delimited record format was chosen to
// round-trip argv with empty-string elements unambiguously; the
// parser must keep interior and trailing empties rather than
// collapsing them.
func TestReadTmuxRecordPreservesEmptyArgs(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	path := filepath.Join(t.TempDir(), "record")

	// First record: 3 args with an interior empty ("a", "", "b").
	// Second record: 2 args with a trailing empty ("x", "").
	body := "3\x00a\x00\x00b\x00" + "2\x00x\x00\x00"
	require.NoError(os.WriteFile(path, []byte(body), 0o644))

	argvs := readTmuxRecord(t, path)
	require.Len(argvs, 2)
	assert.Equal([]string{"a", "", "b"}, argvs[0])
	assert.Equal([]string{"x", ""}, argvs[1])
}

// TestTmuxWrapperKillSession proves the configured tmux.command
// prefix reaches the kill-session exec issued by DELETE /workspaces/{id}.
// This complements TestTmuxWrapperNewSession and TestTmuxWrapperAttachSession —
// together they cover all three tmux verbs that cross the HTTP boundary.
func TestTmuxWrapperKillSession(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	client, _, record := setupWrapperServer(t)
	ctx := t.Context()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	// Poll for status == "ready" before deleting so the tmux
	// session is known to exist from the manager's perspective.
	require.Eventually(
		func() bool {
			getResp, getErr := client.HTTP.GetWorkspaceWithResponse(
				ctx, wsID,
			)
			if getErr != nil || getResp.JSON200 == nil {
				return false
			}
			return getResp.JSON200.Status == "ready"
		},
		5*time.Second, 50*time.Millisecond,
	)

	force := true
	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, wsID, &generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, delResp.StatusCode())

	// The recorded argv should contain a kill-session invocation
	// with our "wrap" prefix.
	var kill []string
	for _, argv := range readTmuxRecord(t, record) {
		if len(argv) >= 2 && argv[1] == "kill-session" {
			kill = argv
			break
		}
	}
	require.NotNil(kill, "kill-session argv not recorded")
	require.Len(kill, 4)
	assert.Equal("wrap", kill[0])
	assert.Equal("kill-session", kill[1])
	assert.Equal("-t", kill[2])
	assert.NotEmpty(kill[3])
}

func TestDeleteWorkspacePreservesRowWhenTmuxKillFails(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    echo "permission denied" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	client, _, _ := setupWrapperServerWithScriptAndDB(t, script)
	ctx := context.Background()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	require.Eventually(
		func() bool {
			getResp, getErr := client.HTTP.GetWorkspaceWithResponse(
				ctx, wsID,
			)
			if getErr != nil || getResp.JSON200 == nil {
				return false
			}
			return getResp.JSON200.Status == "ready"
		},
		5*time.Second, 50*time.Millisecond,
	)

	force := true
	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, wsID, &generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusInternalServerError, delResp.StatusCode())
	require.NotNil(delResp.ApplicationproblemJSONDefault)
	require.NotNil(delResp.ApplicationproblemJSONDefault.Detail)
	assert.Contains(
		*delResp.ApplicationproblemJSONDefault.Detail,
		"kill tmux session",
	)
	assert.Contains(
		*delResp.ApplicationproblemJSONDefault.Detail,
		"permission denied",
	)

	getResp, err := client.HTTP.GetWorkspaceWithResponse(ctx, wsID)
	require.NoError(err)
	require.Equal(http.StatusOK, getResp.StatusCode())
	require.NotNil(getResp.JSON200)
	assert.Equal(wsID, getResp.JSON200.Id)
}

func TestDeleteWorkspaceTreatsTmuxServerExitAsGoneE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    echo "server exited unexpectedly" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	client, _, database := setupWrapperServerWithScriptAndDB(t, script)

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		t.Context(),
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	require.Eventually(
		func() bool {
			getResp, getErr := client.HTTP.GetWorkspaceWithResponse(
				t.Context(), wsID,
			)
			if getErr != nil || getResp.JSON200 == nil {
				return false
			}
			return getResp.JSON200.Status == "ready"
		},
		5*time.Second, 50*time.Millisecond,
	)

	force := true
	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		t.Context(), wsID, &generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, delResp.StatusCode())

	getResp, err := client.HTTP.GetWorkspaceWithResponse(t.Context(), wsID)
	require.NoError(err)
	require.Equal(http.StatusNotFound, getResp.StatusCode())

	stored, err := database.GetWorkspace(t.Context(), wsID)
	require.NoError(err)
	assert.Nil(stored)
}

func TestDeleteErroredWorkspaceAllowsUnavailableTmux(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "new-session" ]; then` + "\n" +
		`    echo "tmux unavailable" >&2` + "\n" +
		`    exit 127` + "\n" +
		`  fi` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    echo "tmux unavailable" >&2` + "\n" +
		`    exit 127` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	client, _, _ := setupWrapperServerWithScriptAndDB(t, script)
	ctx := context.Background()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	require.Eventually(
		func() bool {
			getResp, getErr := client.HTTP.GetWorkspaceWithResponse(
				ctx, wsID,
			)
			if getErr != nil || getResp.JSON200 == nil {
				return false
			}
			return getResp.JSON200.Status == "error"
		},
		5*time.Second, 50*time.Millisecond,
	)

	force := true
	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, wsID, &generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, delResp.StatusCode())

	getResp, err := client.HTTP.GetWorkspaceWithResponse(ctx, wsID)
	require.NoError(err)
	assert.Equal(http.StatusNotFound, getResp.StatusCode())
}

// TestTmuxWrapperAttachSurfacesWrapperFailure exercises the
// error-propagation path end-to-end. Workspace setup uses a wrapper
// that succeeds for new-session (so the workspace reaches "ready")
// but fails has-session with exit code 127 — the kind of exit a
// broken wrapper like systemd-run would produce. Under the old
// boolean-only tmuxSessionExists, this silently passed through as
// "session absent" and the bug hid behind a confusing new-session
// failure. With the bool/error split plus the exit-code-1 carve-out,
// the terminal handler sees the error and closes the WebSocket with
// StatusInternalError.
func TestTmuxWrapperAttachSurfacesWrapperFailure(t *testing.T) {
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then exit 127; fi` + "\n" +
		"done\n" +
		"exit 0\n"
	attachWebsocketAndExpectInternalError(t, body)
}

// attachWebsocketAndExpectInternalError drives the end-to-end
// attach path with a custom fake-tmux script, asserting the
// WebSocket is closed by the handler with StatusInternalError
// rather than attaching to a session. Callers provide the script
// body; the helper handles server setup, workspace creation,
// ready-polling, dial, and close-status assertion.
func attachWebsocketAndExpectInternalError(t *testing.T, scriptBody string) {
	t.Helper()
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	require.NoError(os.WriteFile(script, []byte(scriptBody), 0o755))
	t.Setenv("TMUX_RECORD", record)

	client, baseURL := setupWrapperServerWithScript(t, script)
	ctx := t.Context()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	require.Eventually(
		func() bool {
			getResp, getErr := client.HTTP.GetWorkspaceWithResponse(
				ctx, wsID,
			)
			if getErr != nil || getResp.JSON200 == nil {
				return false
			}
			return getResp.JSON200.Status == "ready"
		},
		5*time.Second, 50*time.Millisecond,
	)

	wsURL := strings.Replace(baseURL, "http://", "ws://", 1) +
		"/ws/v1/workspaces/" + wsID + "/terminal"
	dialCtx, dialCancel := context.WithTimeout(
		ctx, 3*time.Second,
	)
	defer dialCancel()
	u, err := url.Parse(wsURL)
	require.NoError(err)
	conn, httpResp, err := websocket.Dial(
		dialCtx, u.String(), nil,
	)
	require.NoError(err)
	if httpResp != nil && httpResp.Body != nil {
		httpResp.Body.Close()
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	readCtx, readCancel := context.WithTimeout(
		ctx, 3*time.Second,
	)
	defer readCancel()
	_, _, readErr := conn.Read(readCtx)
	require.Error(readErr)
	assert.Equal(
		websocket.StatusInternalError,
		websocket.CloseStatus(readErr),
	)
}

// TestTmuxWrapperAttachSurfacesExit1Failure covers the second half
// of the session-absent heuristic at the HTTP layer: exit code 1
// without tmux's "can't find session" or "no server running"
// stderr must be treated as a real wrapper failure, not as
// "session absent, please create one." This is the common case the
// reviewer flagged — shell wrappers often exit 1 for their own
// generic errors.
func TestTmuxWrapperAttachSurfacesExit1Failure(t *testing.T) {
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "wrapper failed" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	attachWebsocketAndExpectInternalError(t, body)
}

// TestTmuxWrapperAttachIgnoresAbsencePhraseOnStdout verifies that
// the absent-session heuristic is stderr-only at the HTTP layer:
// a wrapper that exits 1 with the tmux phrase on stdout (and an
// unrelated stderr message) must surface as an error, not as
// "session absent." Pairs with the unit-level
// TestManagerEnsureTmuxIgnoresAbsencePhraseOnStdout.
func TestTmuxWrapperAttachIgnoresAbsencePhraseOnStdout(t *testing.T) {
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim"` + "\n" + // stdout only
		`    echo "real failure" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	attachWebsocketAndExpectInternalError(t, body)
}

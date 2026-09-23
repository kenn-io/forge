package runtimeservertest

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty/v2"
	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/streamapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/internal/testutil/processjob"
	"go.kenn.io/forge/internal/testutil/testsignal"
	"go.kenn.io/forge/internal/testutil/testtmux"
	"go.kenn.io/forge/internal/workspace"
	"golang.org/x/sync/semaphore"
)

const (
	serverRuntimeHelperMarker        = "kenn-forge-runtime-helper"
	serverPtyOwnerParentHelperMarker = "kenn-forge-pty-owner-parent-helper"
)

var privateTmuxOwner *testtmux.Owner

var parallelServerTestSlots = semaphore.NewWeighted(4)

func TestMain(m *testing.M) {
	if code, ok := testtmux.CommandWrapperExitCode(); ok {
		os.Exit(code)
	}
	if isServerHelperProcess() {
		os.Exit(m.Run())
	}
	if err := processjob.ContainCurrentProcessTree(); err != nil {
		fmt.Fprintf(os.Stderr, "contain server test process tree: %v\n", err)
		os.Exit(1)
	}
	if testtmux.Supported() {
		var ownerErr error
		privateTmuxOwner, ownerErr = testtmux.New()
		if ownerErr != nil {
			fmt.Fprintf(os.Stderr, "initialize private test tmux owner: %v\n", ownerErr)
			os.Exit(1)
		}
	}
	envDir, envDirErr := os.MkdirTemp("", "kenn-forge-server-tmux-env-*")
	if envDirErr == nil {
		_ = os.Setenv("KENN_FORGE_TMUX_ENV_DIR", envDir)
	}
	runCleanup, stopSignalCleanup := testsignal.Install(func() error {
		return cleanupServerTestTmux(privateTmuxOwner)
	}, func(err error) {
		fmt.Fprintf(os.Stderr, "cleanup kenn-forge test tmux sessions: %v\n", err)
	})
	code := gitsafe.RunIsolatedMain(m)
	if err := runCleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "cleanup kenn-forge test tmux sessions: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	stopSignalCleanup()
	if envDirErr == nil {
		_ = os.RemoveAll(envDir)
	}
	os.Exit(code)
}

func isServerHelperProcess() bool {
	if os.Getenv("KENN_FORGE_SERVER_RUNTIME_HELPER") == "1" ||
		os.Getenv("KENN_FORGE_SERVER_PTY_OWNER_HELPER") == "1" {
		return true
	}
	args := os.Args
	if sep := slices.Index(args, "--"); sep >= 0 {
		args = args[sep+1:]
	}
	return len(args) > 0 &&
		(args[0] == serverRuntimeHelperMarker ||
			args[0] == serverPtyOwnerParentHelperMarker ||
			args[0] == "pty-owner")
}

func runParallelServerTest(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.NoError(t, parallelServerTestSlots.Acquire(t.Context(), 1))
	t.Cleanup(func() { parallelServerTestSlots.Release(1) })
}

func cleanupContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 15*time.Second)
}

func gracefulShutdown(t *testing.T, srv interface{ Shutdown(context.Context) error }) {
	t.Helper()
	ctx, cancel := cleanupContext(t)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
}

func verifiedGitHubRepoIdentity(host, owner, name string) db.RepoIdentity {
	identity := db.GitHubRepoIdentity(host, owner, name)
	identity.PlatformRepoID = "repo-" + strings.ToLower(owner+"-"+name)
	return identity
}

func seedRepoLaunchMetadata(t *testing.T, database *db.DB, repoID int64) {
	t.Helper()
	ctx := t.Context()
	repo, err := database.GetRepoByID(ctx, repoID)
	require.NoError(t, err)
	require.NotNil(t, repo)
	require.NotEmpty(t, repo.PlatformRepoID)

	cloneURL := strings.TrimSpace(repo.CloneURL)
	if cloneURL == "" {
		repoPath := strings.Trim(strings.TrimSpace(repo.RepoPath), "/")
		if repoPath == "" {
			repoPath = strings.Trim(repo.Owner, "/") + "/" + strings.Trim(repo.Name, "/")
		}
		cloneURL = "https://" + repo.PlatformHost + "/" + repoPath + ".git"
	}
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	err = database.UpdateRepoProviderMetadata(
		ctx, repoID,
		db.RepoProviderMetadata{
			PlatformRepoID: repo.PlatformRepoID,
			WebURL:         strings.TrimSuffix(cloneURL, ".git"),
			CloneURL:       cloneURL,
			DefaultBranch:  defaultBranch,
		},
	)
	require.NoError(t, err)
}

func setupTestClient(t *testing.T, srv *server.Server) *apiclient.Client {
	t.Helper()
	return setupTestClientWithBaseURL(t, srv, "http://forge.test")
}

func setupTestClientWithBaseURL(
	t *testing.T,
	srv *server.Server,
	baseURL string,
) *apiclient.Client {
	t.Helper()

	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body io.Reader = http.NoBody
			if req.Body != nil {
				payload, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				_ = req.Body.Close()
				body = strings.NewReader(string(payload))
			}

			serverReq := httptest.NewRequestWithContext(t.Context(), req.Method, req.URL.String(), body)
			serverReq.Header = req.Header.Clone()
			serverReq = serverReq.WithContext(req.Context())

			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, serverReq)
			return rr.Result(), nil
		}),
	}

	client, err := apiclient.NewWithHTTPClient(baseURL, httpClient)
	require.NoError(t, err)

	return client
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type seedPROpt func(*db.MergeRequest)

// seedPR inserts a repo and a PR into the DB, returning the PR's internal ID.
func seedPR(t *testing.T, database *db.DB, owner, name string, number int, opts ...seedPROpt) int64 {
	t.Helper()
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", owner, name))
	require.NoError(t, err)
	seedRepoLaunchMetadata(t, database, repoID)

	numberText := strconv.Itoa(number)
	now := time.Now().UTC().Truncate(time.Second)
	pr := &db.MergeRequest{
		RepoID:           repoID,
		PlatformID:       int64(number) * 1000,
		Number:           number,
		URL:              "https://github.com/" + owner + "/" + name + "/pull/" + numberText,
		Title:            "Test PR #" + numberText,
		Author:           "testuser",
		State:            "open",
		IsDraft:          false,
		Body:             "test body",
		HeadBranch:       "feature",
		HeadRepoCloneURL: "https://github.com/" + owner + "/" + name + ".git",
		BaseBranch:       "main",
		Additions:        5,
		Deletions:        2,
		CommentCount:     0,
		ReviewDecision:   "",
		CIStatus:         "",
		CreatedAt:        now,
		UpdatedAt:        now,
		LastActivityAt:   now,
	}
	for _, opt := range opts {
		opt(pr)
	}

	prID, err := database.UpsertMergeRequest(ctx, pr)
	require.NoError(t, err)
	if pr.PlatformHeadSHA != "" {
		require.NoError(t, database.UpdateDiffSHAs(
			ctx, repoID, number,
			pr.PlatformHeadSHA, pr.PlatformBaseSHA, "merge-base",
		))
	}
	if len(pr.Labels) > 0 {
		require.NoError(t, database.ReplaceMergeRequestLabels(ctx, repoID, prID, pr.Labels))
	}
	require.NoError(t, database.EnsureKanbanState(ctx, prID))

	return prID
}

func TestMergeWorkspaceActivityAuthorsDeduplicatesCaseInsensitively(t *testing.T) {
	runParallelServerTest(t)
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	since := now.Add(-time.Hour)
	activityAt := func(offset time.Duration) *time.Time {
		value := now.Add(offset)
		return &value
	}
	subject := func(repoID int64, number int, author string, at *time.Time) workspaceapi.SubjectActivity {
		key := db.WorkspaceSubjectKey{
			RepoID: repoID, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: number,
		}
		return workspaceapi.SubjectActivity{
			Subject: db.WorkspaceSubjectMetadata{
				Key: key, Platform: "github", PlatformHost: "github.com",
				RepoOwner: "acme", RepoName: "widget", RepoPath: "acme/widget",
				Author: author,
			},
			ActivityAt: at,
		}
	}
	snapshot := workspaceapi.WorkspaceSubjectSnapshot{
		Subjects: map[db.WorkspaceSubjectKey]workspaceapi.SubjectActivity{},
	}
	for _, item := range []workspaceapi.SubjectActivity{
		subject(7, 41, "workspace owner", activityAt(-30*time.Minute)),
		subject(7, 42, "Fresh Owner", activityAt(-10*time.Minute)),
		subject(7, 43, "FRESH OWNER", activityAt(-20*time.Minute)),
		subject(7, 44, "Old Owner", activityAt(-2*time.Hour)),
		subject(8, 45, "Other Repo Owner", activityAt(-5*time.Minute)),
	} {
		snapshot.Subjects[item.Subject.Key] = item
	}

	got := itemapi.MergeWorkspaceActivityAuthors(
		[]string{"Provider Owner", "Workspace Owner"},
		snapshot,
		db.ListActivityAuthorsOpts{
			AllowedRepoIDs: []int64{7},
			RepoFilters: []db.RepoFilter{{
				Platform: "github", PlatformHost: "github.com", RepoPath: "acme/widget",
			}},
			Since: &since,
		},
	)

	assert.Equal(t, []string{"Provider Owner", "Workspace Owner", "Fresh Owner"}, got)
}

func cleanupServerTestTmux(owner *testtmux.Owner) error {
	if owner == nil {
		return nil
	}
	return owner.Cleanup()
}

func TestServerStartupReapsUnrecordedRuntimeTmuxSessionE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runtime tmux startup cleanup is Unix-only")
	}

	require := require.New(t)
	assert := assert.New(t)
	previousStartupCleanupTimeout := streamapi.StartupTmuxCleanupTimeout
	streamapi.StartupTmuxCleanupTimeout = 10 * time.Second
	t.Cleanup(func() { streamapi.StartupTmuxCleanupTimeout = previousStartupCleanupTimeout })

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "fake-tmux")
	database := dbtest.Open(t)
	seedPR(t, database, "acme", "widget", 1)

	worktreeDir := filepath.Join(dir, "worktrees")
	ownerMarker := workspace.NewManager(database, worktreeDir).TmuxOwnerMarker()
	require.NoError(os.WriteFile(tmuxPath, fmt.Appendf(nil, `#!/bin/sh
TMUX_RECORD=%s
TMUX_TEST_OWNER_MARKER=%s
printf '%%s\0' "$#" "$@" >> "$TMUX_RECORD"
target=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-t" ]; then target="$a"; fi
  prev="$a"
done
case "$1" in
  list-sessions)
    printf 'middleman-0000000000000001:%%s\n' "$TMUX_TEST_OWNER_MARKER"
    printf 'forge-0000000000000001-0123456789abcdef:%%s\n' "$TMUX_TEST_OWNER_MARKER"
    exit 0
    ;;
  kill-session)
    exit 0
    ;;
esac
exit 0
`, shellquote.Join(record), shellquote.Join(ownerMarker)), 0o755))
	ws := &workspace.Workspace{
		ID:              "0000000000000001",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      1,
		GitHeadRef:      "feature",
		WorkspaceBranch: "feature",
		WorktreePath:    filepath.Join(worktreeDir, "acme-widget-1"),
		TmuxSession:     "middleman-0000000000000001",
		Status:          "ready",
	}
	require.NoError(database.InsertWorkspace(t.Context(), ws))

	cfg := &config.Config{Tmux: config.Tmux{Command: []string{tmuxPath}}}
	srv := server.New(database, nil, nil, "/", cfg, server.ServerOptions{
		WorktreeDir: worktreeDir,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetWorkspaceWithResponse(t.Context(), &generated.GetWorkspaceRequestOptions{PathParams: &generated.GetWorkspacePath{ID: ws.ID}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	require.Eventually(func() bool {
		return slices.ContainsFunc(
			readTmuxRecord(t, record),
			func(argv []string) bool {
				return slices.Equal(argv, []string{
					"kill-session", "-t",
					"forge-0000000000000001-0123456789abcdef",
				})
			},
		)
	}, 2*time.Second, 20*time.Millisecond)
	argvs := readTmuxRecord(t, record)
	assert.Contains(argvs, []string{
		"kill-session", "-t", "forge-0000000000000001-0123456789abcdef",
	})
	assert.NotContains(argvs, []string{
		"kill-session", "-t", "middleman-0000000000000001",
	})
}

func TestServerRuntimeHelperProcess(t *testing.T) {
	args := os.Args
	if sep := slices.Index(args, "--"); sep >= 0 {
		args = args[sep+1:]
	}
	if len(args) > 0 && args[0] == serverRuntimeHelperMarker {
		args = args[1:]
	} else if os.Getenv("KENN_FORGE_SERVER_RUNTIME_HELPER") != "1" {
		return
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	mode := args[0]
	switch mode {
	case "sleep":
		blockServerRuntimeHelper()
	case "echo":
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err == nil {
			fmt.Print("echo:" + line)
		}
		blockServerRuntimeHelper()
	case "size-live":
		reader := bufio.NewReader(os.Stdin)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			rows, cols, sizeErr := pty.Getsize(os.Stdin)
			if sizeErr == nil {
				fmt.Printf("size:%d:%d:%s", rows, cols, line)
			}
		}
	case "pty-close-then-sleep":
		// Simulate the systemd-run-wrapper window the bridge has to
		// survive: PTY EOF observed (drainOutput exits) well before
		// cmd.Wait returns. Ignoring SIGHUP keeps us alive when the
		// runtime closes the PTY master in response to our slave
		// close — without that, the kernel SIGHUPs the session leader
		// and cmd.Wait returns alongside drainOutput, which hides the
		// race we're trying to exercise. Closing stdin/stdout/stderr
		// drops every slave fd; the master sees EOF immediately. The
		// 2 s sleep gives the exit-frame promptness assertion clear
		// daylight: a regression that gates on cmd.Wait would deliver
		// at ~2 s, well past the test's promptness budget.
		signal.Ignore(syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
		_ = os.Stdin.Close()
		_ = os.Stdout.Close()
		_ = os.Stderr.Close()
		time.Sleep(2 * time.Second)
		os.Exit(7)
	default:
		os.Exit(2)
	}
}

func blockServerRuntimeHelper() {
	for {
		time.Sleep(time.Hour)
	}
}

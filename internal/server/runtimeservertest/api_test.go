package runtimeservertest

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty/v2"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/streamapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m))
}

func TestMergeWorkspaceActivityAuthorsDeduplicatesCaseInsensitively(t *testing.T) {
	serverfake.RunParallelServerTest(t)
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
	serverfake.SeedPR(t, database, "acme", "widget", 1)

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
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.GetWorkspaceWithResponse(t.Context(), &generated.GetWorkspaceRequestOptions{PathParams: &generated.GetWorkspacePath{ID: ws.ID}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	require.Eventually(func() bool {
		return slices.ContainsFunc(
			serverfake.ReadTmuxRecord(t, record),
			func(argv []string) bool {
				return slices.Equal(argv, []string{
					"kill-session", "-t",
					"forge-0000000000000001-0123456789abcdef",
				})
			},
		)
	}, 2*time.Second, 20*time.Millisecond)
	argvs := serverfake.ReadTmuxRecord(t, record)
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
	if len(args) > 0 && args[0] == serverfake.ServerRuntimeHelperMarker {
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
		serverfake.BlockServerRuntimeHelper()
	case "echo":
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err == nil {
			fmt.Print("echo:" + line)
		}
		serverfake.BlockServerRuntimeHelper()
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

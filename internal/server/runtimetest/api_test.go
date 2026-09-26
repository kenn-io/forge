package runtimetest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/testtmux"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m))
}

func requirePTYAvailable(t *testing.T) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable in this test environment: %v", err)
	}
	_ = ptmx.Close()
	_ = tty.Close()
}

func TestAPIListItemsIncludeWorkspaceRefs(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()

	serverfake.SeedPR(t, database, "acme", "widget", 1)
	serverfake.SeedIssue(t, database, "acme", "widget", 2, "open")
	serverfake.SeedWorkspace(
		t, database, "ws-pr-1", "acme", "widget",
		db.WorkspaceItemTypePullRequest, 1,
	)
	serverfake.SeedWorkspace(
		t, database, "ws-issue-2", "acme", "widget",
		db.WorkspaceItemTypeIssue, 2,
	)
	client := servertest.SetupTestClient(t, srv)

	pulls, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, pulls.StatusCode)
	require.NotNil(pulls.JSON200)
	require.Len(*pulls.JSON200, 1)
	require.NotNil((*pulls.JSON200)[0].Workspace)
	assert.Equal("ws-pr-1", (*pulls.JSON200)[0].Workspace.ID)
	assert.Equal("ready", (*pulls.JSON200)[0].Workspace.Status)

	issues, err := client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, issues.StatusCode)
	require.NotNil(issues.JSON200)
	require.Len(*issues.JSON200, 1)
	require.NotNil((*issues.JSON200)[0].Workspace)
	assert.Equal("ws-issue-2", (*issues.JSON200)[0].Workspace.ID)
	assert.Equal("ready", (*issues.JSON200)[0].Workspace.Status)

	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	activity, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since}})
	require.NoError(err)
	require.Equal(http.StatusOK, activity.StatusCode)
	require.NotNil(activity.JSON200)
	require.NotNil(activity.JSON200.Items)

	workspaceByItem := make(map[string]string)
	for _, item := range activity.JSON200.Items {
		if item.Workspace == nil {
			continue
		}
		workspaceByItem[item.ItemType+":"+strconv.FormatInt(item.ItemNumber, 10)] = item.Workspace.ID
	}
	assert.Equal("ws-pr-1", workspaceByItem["pr:1"])
	assert.Equal("ws-issue-2", workspaceByItem["issue:2"])
}

func TestAPIReadyForReviewReclassifiesWorkspaceHeadRepo(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	const forkURL = "https://github.com/contributor/widget.git"

	mock := &serverfake.MockGH{
		MarkReadyForReviewFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			title := "Ready PR"
			state := "open"
			url := "https://github.com/acme/widget/pull/1"
			author := "octocat"
			draft := false
			now := gh.Timestamp{Time: time.Now().UTC().Add(time.Minute)}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				Draft:     &draft,
				CreatedAt: &now,
				UpdatedAt: &now,
				User:      &gh.User{Login: &author},
				Head: &gh.PullRequestBranch{
					Ref:  new("feature"),
					Repo: &gh.Repository{CloneURL: new(forkURL)},
				},
				Base: &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	require.NoError(database.InsertWorkspace(t.Context(), &db.Workspace{
		ID:           "readyfork0000001",
		Platform:     "github",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature",
		WorktreePath: filepath.Join(t.TempDir(), "workspace"),
		TmuxSession:  "kenn-forge-readyfork0000001",
		Status:       "ready",
	}))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	stored, err := database.GetWorkspace(t.Context(), "readyfork0000001")
	require.NoError(err)
	require.NotNil(stored)
	require.NotNil(stored.MRHeadRepo)
	require.Equal(forkURL, *stored.MRHeadRepo)
}

func cleanupWorkspaceServerFixtureTmuxSessionsWithContext(
	ctx context.Context,
	tmuxCommand []string,
	root string,
) error {
	if len(tmuxCommand) == 0 {
		return nil
	}
	sessions, err := forgeTmuxSessions(ctx, tmuxCommand, func(path string) bool {
		return pathIsWithin(root, path)
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, session := range sessions {
		cmd := configuredTmuxCommandContext(
			ctx, tmuxCommand, "kill-session", "-t", session,
		)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := procutil.Run(ctx, cmd, "test tmux cleanup"); err != nil {
			errs = append(
				errs,
				fmt.Errorf(
					"kill leaked tmux session %s: %w: %s",
					session, err, strings.TrimSpace(stderr.String()),
				),
			)
		}
	}
	remaining, err := forgeTmuxSessions(ctx, tmuxCommand, func(path string) bool {
		return pathIsWithin(root, path)
	})
	if err != nil {
		errs = append(errs, err)
	}
	if len(remaining) > 0 {
		errs = append(errs, fmt.Errorf(
			"workspace fixture leaked tmux sessions under %s: %s",
			root, strings.Join(remaining, ", "),
		))
	}
	return errors.Join(errs...)
}

func forgeTmuxSessions(
	ctx context.Context,
	tmuxCommand []string,
	includePath func(string) bool,
) ([]string, error) {
	// Colon-separated because tmux 3.6+ sanitizes control characters in
	// -F output (a tab prints as "_"). Session names cannot contain ":"
	// (tmux replaces it with "_"), so cutting at the first colon is
	// unambiguous even when the session path contains colons.
	cmd := configuredTmuxCommandContext(
		ctx, tmuxCommand,
		"list-sessions", "-F", "#{session_name}:#{session_path}",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := procutil.Run(ctx, cmd, "test tmux cleanup list"); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		msg := strings.TrimSpace(stderr.String())
		if isTmuxServerAbsentMessage(msg) {
			return nil, nil
		}
		return nil, fmt.Errorf("list tmux sessions: %w: %s", err, msg)
	}

	var sessions []string
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		name, path, ok := strings.Cut(line, ":")
		if !ok || !strings.HasPrefix(name, "kenn-forge-") {
			continue
		}
		if includePath != nil && !includePath(path) {
			continue
		}
		sessions = append(sessions, name)
	}
	slices.Sort(sessions)
	return sessions, nil
}

func configuredTmuxCommandContext(
	ctx context.Context,
	tmuxCommand []string,
	args ...string,
) *exec.Cmd {
	commandArgs := append(slices.Clone(tmuxCommand[1:]), args...)
	return procutil.CommandContext(ctx, tmuxCommand[0], commandArgs...)
}

func isTmuxServerAbsentMessage(msg string) bool {
	return strings.Contains(msg, "no server running") ||
		(strings.Contains(msg, "error connecting to") &&
			strings.Contains(msg, "No such file or directory"))
}

func pathIsWithin(root string, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(
		rel, ".."+string(os.PathSeparator),
	)
}

func pathIsGoTestTempPath(path string) bool {
	rel, err := filepath.Rel(filepath.Clean(os.TempDir()), filepath.Clean(path))
	if err != nil ||
		rel == ".." ||
		strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	first, _, _ := strings.Cut(rel, string(os.PathSeparator))
	return strings.HasPrefix(first, "Test")
}

func TestForgeTmuxSessionsTreatsMissingTmuxSocketAsEmpty(t *testing.T) {
	require := require.New(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`if [ "$1" = "list-sessions" ]; then` + "\n" +
		`  echo "error connecting to /tmp/tmux-1001/default (No such file or directory)" >&2` + "\n" +
		`  exit 1` + "\n" +
		`fi` + "\n" +
		"exit 2\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	sessions, err := forgeTmuxSessions(
		context.Background(), []string{script}, pathIsGoTestTempPath,
	)

	require.NoError(err)
	require.Empty(sessions)
}

func TestForgeTmuxSessionsTreatsMissingConfiguredCommandAsEmpty(t *testing.T) {
	require := require.New(t)
	sessions, err := forgeTmuxSessions(
		context.Background(),
		[]string{filepath.Join(t.TempDir(), "missing-tmux")},
		pathIsGoTestTempPath,
	)

	require.NoError(err)
	require.Empty(sessions)
}

func TestWorkspaceFixtureTmuxCleanupUsesConfiguredCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tmux fixture uses Unix shell semantics")
	}
	require := require.New(t)
	dir := t.TempDir()
	defaultCalled := filepath.Join(dir, "default-called")
	defaultTmux := filepath.Join(dir, "tmux")
	require.NoError(os.WriteFile(
		defaultTmux,
		[]byte("#!/bin/sh\n: > \"$DEFAULT_TMUX_CALLED\"\nexit 99\n"),
		0o755,
	))
	t.Setenv("DEFAULT_TMUX_CALLED", defaultCalled)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	privateRecord := filepath.Join(dir, "private-record")
	privateKilled := filepath.Join(dir, "private-killed")
	privateTmux := filepath.Join(dir, "private-tmux")
	privateRoot := filepath.Join(dir, "fixture")
	privateSocket := filepath.Join(dir, "private.sock")
	privateBody := `#!/bin/sh
printf '%s\n' "$*" >> "$PRIVATE_TMUX_RECORD"
case "$*" in
  *list-sessions*)
    if [ ! -f "$PRIVATE_TMUX_KILLED" ]; then
      printf 'kenn-forge-fixture:%s\n' "$PRIVATE_TMUX_ROOT"
    fi
    ;;
  *kill-session*)
    : > "$PRIVATE_TMUX_KILLED"
    ;;
esac
`
	require.NoError(os.WriteFile(privateTmux, []byte(privateBody), 0o755))
	t.Setenv("PRIVATE_TMUX_RECORD", privateRecord)
	t.Setenv("PRIVATE_TMUX_KILLED", privateKilled)
	t.Setenv("PRIVATE_TMUX_ROOT", privateRoot)

	err := cleanupWorkspaceServerFixtureTmuxSessionsWithContext(
		context.Background(),
		[]string{privateTmux, "-S", privateSocket},
		privateRoot,
	)
	require.NoError(err)
	_, err = os.Stat(defaultCalled)
	require.ErrorIs(err, os.ErrNotExist)
	require.FileExists(privateKilled)
	record, err := os.ReadFile(privateRecord)
	require.NoError(err)
	for line := range strings.SplitSeq(strings.TrimSpace(string(record)), "\n") {
		require.True(
			strings.HasPrefix(line, "-S "+privateSocket+" "),
			"fixture cleanup lost private tmux prefix: %s", line,
		)
	}
}

func TestServerTestCleanupDoesNotInvokeDefaultTmux(t *testing.T) {
	require := require.New(t)
	var owner *testtmux.Owner
	if testtmux.Supported() {
		var err error
		owner, err = testtmux.New()
		require.NoError(err)
		t.Cleanup(func() { _ = owner.Cleanup() })
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux-called")
	tmuxPath := filepath.Join(dir, "tmux")
	require.NoError(os.WriteFile(
		tmuxPath,
		[]byte("#!/bin/sh\n: > \"$TMUX_CALLED\"\nexit 1\n"),
		0o755,
	))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_CALLED", logPath)

	require.NoError(serverfake.CleanupServerTestTmux(owner))
	_, err := os.Stat(logPath)
	require.ErrorIs(err, os.ErrNotExist)
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

func TestWorkspacePRDetailPlatformHost(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)

	// Seed same owner/name on different hosts to test ambiguity.
	serverfake.SeedPROnHost(
		t, database,
		"github.com", "acme", "widget", 10,
	)
	serverfake.SeedPROnHost(
		t, database,
		"ghe.example.com", "acme", "widget", 20,
	)

	mock := &serverfake.MockGH{}
	repos := []ghclient.RepoRef{
		{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		},
		{
			Owner: "acme", Name: "widget",
			PlatformHost: "ghe.example.com",
		},
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com":      mock,
			"ghe.example.com": mock,
		},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(
		database, syncer, nil, "/", nil, server.ServerOptions{},
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)
	ctx := t.Context()

	// PR on github.com
	r1, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.Equal(http.StatusOK, r1.StatusCode)
	require.NotNil(r1.JSON200)
	assert.Equal("github.com", r1.JSON200.PlatformHost)

	// PR on ghe.example.com (same owner/name, different number)
	r2, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "ghe.example.com", Provider: "gh", Owner: "acme", Name: "widget", Number: int64(20)}})
	require.NoError(err)
	require.Equal(http.StatusOK, r2.StatusCode)
	require.NotNil(r2.JSON200)
	assert.Equal("ghe.example.com", r2.JSON200.PlatformHost)
}

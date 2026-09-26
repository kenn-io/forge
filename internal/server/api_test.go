package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty/v2"
	"go.kenn.io/forge/internal/platformdb"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"

	gh "github.com/google/go-github/v91/github"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/ptyowner"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/server/syncevents"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfake"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/testtmux"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.kenn.io/forge/platform"

	gitcmd "go.kenn.io/kit/git/cmd"
	"golang.org/x/sync/semaphore"
)

var (
	parallelServerTestSlots = semaphore.NewWeighted(4)
	ptyE2ESemaphore         = semaphore.NewWeighted(1)
	serverPtyOwnerParentPID = flag.Int(
		"server-pty-owner-parent-pid",
		0,
		"PID of the test process that launched the PTY owner helper",
	)
	// Bound Git-heavy root-package tests independently from the workspacetest
	// binary.
	rootWorkspaceGitSemaphore = semaphore.NewWeighted(2)
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m))
}

func runParallelServerTest(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.NoError(t, parallelServerTestSlots.Acquire(t.Context(), 1))
	t.Cleanup(func() { parallelServerTestSlots.Release(1) })
}

func runSerialPTYE2E(t *testing.T) {
	t.Helper()
	releasePTYSlot := acquirePTYE2ESlot(t)
	t.Cleanup(releasePTYSlot)
}

func acquirePTYE2ESlot(t *testing.T) func() {
	t.Helper()
	require.NoError(t, ptyE2ESemaphore.Acquire(t.Context(), 1))
	return func() {
		ptyE2ESemaphore.Release(1)
	}
}

func acquireRootWorkspaceGitSlot(t *testing.T) {
	t.Helper()
	require.NoError(t, rootWorkspaceGitSemaphore.Acquire(t.Context(), 1))
	t.Cleanup(func() { rootWorkspaceGitSemaphore.Release(1) })
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

func recordRuntimeTmuxSessionForServerTest(
	t *testing.T,
	database *db.DB,
	workspaceID string,
	sessionKey string,
	targetKey string,
	tmuxSession string,
	createdAt time.Time,
) {
	t.Helper()
	if sessionKey == "" {
		sessionKey = runtimeSessionKeyForTest(
			workspaceID, targetKey, tmuxSession,
		)
	}
	label := targetKey
	kind := string(localruntime.LaunchTargetAgent)
	if targetKey == string(localruntime.LaunchTargetPlainShell) {
		label = "Shell"
		kind = string(localruntime.LaunchTargetPlainShell)
	}
	require.NoError(t, database.UpsertWorkspaceRuntimeSession(
		t.Context(),
		&db.WorkspaceRuntimeSession{
			WorkspaceID: workspaceID,
			SessionKey:  sessionKey,
			TargetKey:   targetKey,
			Label:       label,
			Kind:        kind,
			Scope:       "session",
			TmuxSession: tmuxSession,
			CreatedAt:   createdAt,
		},
	))
}

func runtimeSessionKeyForTest(
	workspaceID string,
	targetKey string,
	tmuxSession string,
) string {
	sum := sha256.Sum256([]byte(targetKey + "\x00" + tmuxSession))
	return workspaceID + "_" + hex.EncodeToString(sum[:8])
}

// setupTestServer opens a temp DB, builds a Server, and returns both.
func setupTestServer(t *testing.T) (*Server, *db.DB, *ghclient.Syncer) {
	t.Helper()
	return setupTestServerWithMock(t, &serverfake.MockGH{})
}

func setupNotificationsEnabledTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database,
		nil,
		serverfake.DefaultTestRepos,
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(
		database, syncer, nil, "/",
		notificationsEnabledConfig(), ServerOptions{},
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	return srv, database
}

func setupTestServerWithMock(t *testing.T, mock *serverfake.MockGH) (*Server, *db.DB, *ghclient.Syncer) {
	t.Helper()
	return setupTestServerWithRepos(t, mock, serverfake.DefaultTestRepos)
}

func setupTestServerWithRepos(
	t *testing.T, mock *serverfake.MockGH, repos []ghclient.RepoRef,
) (*Server, *db.DB, *ghclient.Syncer) {
	return setupTestServerWithReposAndOptions(t, mock, repos, ServerOptions{})
}

func setupTestServerWithReposAndOptions(
	t *testing.T, mock *serverfake.MockGH, repos []ghclient.RepoRef, options ServerOptions,
) (*Server, *db.DB, *ghclient.Syncer) {
	t.Helper()

	database := dbtest.Open(t)
	repos = append([]ghclient.RepoRef(nil), repos...)
	for i := range repos {
		repo := &repos[i]
		if repo.PlatformExternalID == "" {
			repo.PlatformExternalID = "repo-" + repo.Owner + "-" + repo.Name
		}
		_, err := database.UpsertRepo(
			t.Context(), platformdb.DBRepoIdentity(platform.RepoRef{
				Platform:           platform.Kind(repo.Platform),
				Host:               repo.PlatformHost,
				Owner:              repo.Owner,
				Name:               repo.Name,
				RepoPath:           repo.RepoPath,
				PlatformExternalID: repo.PlatformExternalID,
			}),
		)
		require.NoError(t, err)
	}

	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, repos, time.Minute, nil, nil)
	// Drain any TriggerRun goroutines (fired by handlers like
	// POST /sync) before tests tear down. Registered after the DB
	// cleanup so LIFO ordering runs Stop first: without this, a
	// leaked goroutine from one test's handler can outlive its DB.
	t.Cleanup(syncer.Stop)
	var cfg *config.Config
	if options.WorktreeDir != "" {
		cfg = &config.Config{Tmux: config.Tmux{
			Command: []string{filepath.Join(t.TempDir(), "missing-tmux")},
		}}
	}
	srv := New(
		database, syncer, nil, "/",
		cfg, options,
	)
	// Registered after the DB cleanup so LIFO ordering runs Shutdown
	// first and lets background goroutines finish before DB close.
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	return srv, database, syncer
}

func setupTestClient(t *testing.T, srv *Server) *apiclient.Client {
	t.Helper()
	return setupTestClientWithBaseURL(t, srv, "http://forge.test")
}

func setupTestClientWithBaseURL(
	t *testing.T,
	srv *Server,
	baseURL string,
) *apiclient.Client {
	t.Helper()

	httpClient := &http.Client{
		Transport: serverfake.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
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

func launchPlainShellRuntimeSession(
	t *testing.T,
	ctx context.Context,
	client *apiclient.Client,
	workspaceID string,
) *generated.SessionInfo {
	t.Helper()

	resp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(ctx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: workspaceID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{
		TargetKey: string(localruntime.LaunchTargetPlainShell),
	}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(resp.Body))
	require.NotNil(t, resp.JSON200)
	return resp.JSON200
}

func setTestServerNow(t *testing.T, srv *Server, now time.Time) {
	t.Helper()
	srv.now = func() time.Time { return now }
}

func testEDTTime(hour, minute int) time.Time {
	//nolint:forbidigo // Test fixture intentionally uses a non-UTC timestamp to verify UTC normalization.
	return time.Date(2026, 4, 11, hour, minute, 0, 0, time.FixedZone("EDT", -4*60*60))
}

func assertTimePtrUTC(t *testing.T, got *time.Time) {
	t.Helper()
	require.NotNil(t, got)
	assert.Equal(t, time.UTC, got.Location())
}

func assertTimePtrEqualsUTC(t *testing.T, got *time.Time, want time.Time) {
	t.Helper()
	assertTimePtrUTC(t, got)
	assert.Equal(t, want.UTC(), got.UTC())
}

func withSeedPRHeadRepoCloneURL(cloneURL string) serverfake.SeedPROpt {
	return func(pr *db.MergeRequest) { pr.HeadRepoCloneURL = cloneURL }
}

func withSeedPRHeadBranch(branch string) serverfake.SeedPROpt {
	return func(pr *db.MergeRequest) { pr.HeadBranch = branch }
}

func insertTestActivityPR(
	t *testing.T,
	database *db.DB,
	repoID int64,
	owner string,
	name string,
	number int,
	title string,
	createdAt time.Time,
) int64 {
	t.Helper()
	numberText := strconv.Itoa(number)
	prID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     int64(number) * 1000,
		Number:         number,
		URL:            "https://github.com/" + owner + "/" + name + "/pull/" + numberText,
		Title:          title,
		Author:         "testuser",
		State:          db.MergeRequestStateOpen,
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
		LastActivityAt: createdAt,
	})
	require.NoError(t, err)
	return prID
}

func TestAPIQueuedPRSyncRechecksRemovedUpstreamBeforeProviderCall(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	var providerCalls atomic.Int64
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
			providerCalls.Add(1)
			return nil, errors.New("removed pull must not be fetched")
		},
	}

	srv, database, _ := setupTestServerWithMock(t, mock)
	ctx := t.Context()
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	repo, err := database.GetRepoByIdentity(
		ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	client := setupTestClient(t, srv)

	key := "pr:github:github.com:acme/widget#1"
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	require.True(srv.syncevents.EnqueueDetailSyncOrRerun(
		key, nil, func(context.Context) error {
			close(firstStarted)
			<-releaseFirst
			return nil
		},
	))
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		require.Fail("Condition never satisfied")
	}

	resp, err := client.HTTP.EnqueuePrSyncWithResponse(ctx, &generated.EnqueuePrSyncRequestOptions{PathParams: &generated.EnqueuePrSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode, string(resp.Body))
	serverfake.MarkArchiveItemRemovedUpstreamForServerTest(
		t, database, repo.ID, db.ArchiveItemTypeMergeRequest, 1,
	)
	close(releaseFirst)

	require.Eventually(func() bool {
		srv.detailSyncMu.Lock()
		defer srv.detailSyncMu.Unlock()
		_, inFlight := srv.detailSyncInFlight[key]
		return !inFlight
	}, 10*time.Second, time.Millisecond)
	require.Zero(providerCalls.Load())
}

// TestAPIEnqueuePRSyncPersistsWorkflowApproval verifies that the
// background sync path (POST /sync/async) computes and persists
// workflow approval state so a subsequent DB-only GET sees it. The
// frontend's default detail-load flow uses this path, so without
// persistence the Approve Workflows button never appears.
func TestAPIEnqueuePRSyncPersistsWorkflowApproval(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(2001)
			sha := "abc123"
			state := "open"
			title := "Async synced PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Head:      &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		ListWorkflowRunsForHeadFn: func(_ context.Context, _, _, headSHA string) ([]*gh.WorkflowRun, error) {
			require.Equal("abc123", headSHA)
			return []*gh.WorkflowRun{
				{
					ID:           new(int64(77)),
					HeadSHA:      new("abc123"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
			}, nil
		},
	}

	srv, database, _ := setupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.EnqueuePrSyncWithResponse(t.Context(), &generated.EnqueuePrSyncRequestOptions{PathParams: &generated.EnqueuePrSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode)

	detailSyncKey := "pr:github:github.com:acme/widget#1"
	require.Eventually(func() bool {
		srv.detailSyncMu.Lock()
		defer srv.detailSyncMu.Unlock()
		_, inFlight := srv.detailSyncInFlight[detailSyncKey]
		return !inFlight
	}, 10*time.Second, time.Millisecond, "async detail sync should complete")

	detail, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.NotNil(detail.JSON200)
	assert.True(detail.JSON200.WorkflowApproval.Checked)
	assert.True(detail.JSON200.WorkflowApproval.Required)
	assert.Equal(int64(1), detail.JSON200.WorkflowApproval.Count)
}

func TestAPIGitLabConfiguredRepoSyncThroughProviderRegistry(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group/subgroup",
		Name:               "project",
		RepoPath:           "group/subgroup/project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/subgroup/project",
		CloneURL:           "https://gitlab.example.com/group/subgroup/project.git",
		DefaultBranch:      "main",
	}
	provider := &serverfake.ApiTestGitLabProvider{
		Ref: ref,
		MergeRequests: []platform.MergeRequest{{
			Repo:               ref,
			PlatformID:         7001,
			PlatformExternalID: "gid://gitlab/MergeRequest/7001",
			Number:             7,
			URL:                "https://gitlab.example.com/group/subgroup/project/-/merge_requests/7",
			Title:              "GitLab provider MR",
			Author:             "ada",
			State:              "open",
			HeadBranch:         "feature/gitlab",
			BaseBranch:         "main",
			HeadSHA:            "abc123",
			BaseSHA:            "def456",
			MergeableState:     "dirty",
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	cfg := &config.Config{
		BasePath: "/",
		Repos: []config.Repo{{
			Platform:     "gitlab",
			PlatformHost: "gitlab.example.com",
			Owner:        "group/subgroup",
			Name:         "project",
		}},
	}
	_, repos, err := ghclient.ResolveConfiguredRepoWithRegistry(
		ctx, registry, cfg.Repos[0],
	)
	require.NoError(err)
	require.Equal([]ghclient.RepoRef{{
		Platform:           platform.KindGitLab,
		Owner:              "group/subgroup",
		Name:               "project",
		PlatformHost:       "gitlab.example.com",
		RepoPath:           "group/subgroup/project",
		PlatformRepoID:     4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/subgroup/project",
		CloneURL:           "https://gitlab.example.com/group/subgroup/project.git",
		DefaultBranch:      "main",
		ConfiguredRepoPath: "group/subgroup/project",
	}}, repos)

	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := NewWithConfig(
		database, syncer, nil, nil, cfg,
		filepath.Join(t.TempDir(), "config.toml"), ServerOptions{},
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	repoRow, err := database.GetRepoByIdentity(ctx, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repoRow)
	require.NotNil(repoRow.LastSyncCompletedAt)
	assert.Equal("gitlab", repoRow.Platform)
	assert.Equal("gitlab.example.com", repoRow.PlatformHost)

	reposResp, err := client.HTTP.ListReposWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, reposResp.StatusCode)
	require.NotNil(reposResp.JSON200)
	require.Len(*reposResp.JSON200, 1)
	assert.Equal("gitlab", (*reposResp.JSON200)[0].Platform)
	assert.Equal("gitlab.example.com", (*reposResp.JSON200)[0].PlatformHost)
	assert.Equal("group/subgroup", (*reposResp.JSON200)[0].Owner)
	assert.Equal("project", (*reposResp.JSON200)[0].Name)

	pullsResp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{Query: &generated.ListPullsQuery{}})
	require.NoError(err)
	require.Equal(http.StatusOK, pullsResp.StatusCode)
	require.NotNil(pullsResp.JSON200)
	require.Len(*pullsResp.JSON200, 1)
	assert.Equal("gitlab.example.com", (*pullsResp.JSON200)[0].PlatformHost)
	assert.Equal("group/subgroup", (*pullsResp.JSON200)[0].RepoOwner)
	assert.Equal("project", (*pullsResp.JSON200)[0].RepoName)
	assert.Equal("GitLab provider MR", (*pullsResp.JSON200)[0].Title)
	assert.Equal("dirty", (*pullsResp.JSON200)[0].MergeableState)
}

func TestAPIGitLabClosedSyncPersistsMergedActorForImmediateDetail(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now.Add(time.Minute)

	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	openMR := platform.MergeRequest{
		Repo:               ref,
		PlatformID:         7001,
		PlatformExternalID: "gid://gitlab/MergeRequest/7001",
		Number:             7,
		URL:                "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:              "GitLab provider MR",
		Author:             "ada",
		State:              "open",
		HeadBranch:         "feature/gitlab",
		BaseBranch:         "main",
		HeadSHA:            "abc123",
		BaseSHA:            "def456",
		CreatedAt:          now,
		UpdatedAt:          now,
		LastActivityAt:     now,
	}
	mergedMR := openMR
	mergedMR.State = "merged"
	mergedMR.MergedAt = &mergedAt
	mergedMR.ClosedAt = &mergedAt
	mergedMR.MergedBy = "merge-admin"
	mergedMR.UpdatedAt = mergedAt
	mergedMR.LastActivityAt = mergedAt
	provider := &serverfake.ApiTestGitLabProvider{
		Ref:                ref,
		MergeRequests:      []platform.MergeRequest{openMR},
		MergeRequestDetail: map[int]platform.MergeRequest{7: mergedMR},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	repo := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		Owner:              ref.Owner,
		Name:               ref.Name,
		PlatformHost:       ref.Host,
		RepoPath:           ref.RepoPath,
		PlatformRepoID:     ref.PlatformID,
		PlatformExternalID: ref.PlatformExternalID,
		WebURL:             ref.WebURL,
		CloneURL:           ref.CloneURL,
		DefaultBranch:      ref.DefaultBranch,
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", &config.Config{Tmux: config.Tmux{
		Command: []string{filepath.Join(t.TempDir(), "missing-tmux")},
	}}, ServerOptions{
		WorktreeDir:                        t.TempDir(),
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)
	provider.MergeRequests = nil
	syncer.RunOnce(ctx)

	detailResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.Events)
	require.Len(detailResp.JSON200.Events, 1)
	event := detailResp.JSON200.Events[0]
	assert.Equal("merged", event.EventType)
	assert.Equal("merge-admin", event.Author)
	assert.Equal("merged this", event.Summary)
	assert.True(event.CreatedAt.Equal(mergedAt))
}

func TestAPIScheduledMergedActorRepairRefreshesOpenDetail(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now.Add(-time.Minute)

	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	repoID, err := database.UpsertRepo(ctx, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:             repoID,
		PlatformID:         7001,
		PlatformExternalID: "gid://gitlab/MergeRequest/7001",
		Number:             7,
		URL:                "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:              "Delayed merged actor",
		Author:             "ada",
		State:              db.MergeRequestStateMerged,
		HeadBranch:         "feature/gitlab",
		BaseBranch:         "main",
		PlatformHeadSHA:    "abc123",
		CreatedAt:          now.Add(-time.Hour),
		UpdatedAt:          mergedAt,
		LastActivityAt:     mergedAt,
		MergedAt:           &mergedAt,
		ClosedAt:           &mergedAt,
	})
	require.NoError(err)
	providerMR := platform.MergeRequest{
		Repo:               ref,
		PlatformID:         7001,
		PlatformExternalID: "gid://gitlab/MergeRequest/7001",
		Number:             7,
		URL:                "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:              "Delayed merged actor",
		Author:             "ada",
		State:              "merged",
		HeadBranch:         "feature/gitlab",
		BaseBranch:         "main",
		HeadSHA:            "abc123",
		CreatedAt:          now.Add(-time.Hour),
		UpdatedAt:          mergedAt,
		LastActivityAt:     mergedAt,
		MergedAt:           &mergedAt,
		ClosedAt:           &mergedAt,
	}
	provider := &serverfake.ApiTestGitLabProvider{
		Ref:                ref,
		MergeRequestDetail: map[int]platform.MergeRequest{7: providerMR},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	repo := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		Owner:              ref.Owner,
		Name:               ref.Name,
		PlatformHost:       ref.Host,
		RepoPath:           ref.RepoPath,
		PlatformRepoID:     ref.PlatformID,
		PlatformExternalID: ref.PlatformExternalID,
		WebURL:             ref.WebURL,
		CloneURL:           ref.CloneURL,
		DefaultBranch:      ref.DefaultBranch,
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	srv.now = func() time.Time { return now }
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	changed, err := syncer.BackfillMergedActorEventOnProvider(ctx, repoID, 7)
	require.NoError(err)
	require.False(changed, "the initial provider response has no merged actor")
	before, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, before.StatusCode, string(before.Body))
	require.NotNil(before.JSON200)
	require.NotNil(before.JSON200.Events)
	require.Len(before.JSON200.Events, 1)
	assert.Empty(before.JSON200.Events[0].Author)

	events, _ := srv.Hub().Subscribe(ctx, false)
	provider.Mu.Lock()
	providerMR.MergedBy = "merge-admin"
	provider.MergeRequestDetail[7] = providerMR
	provider.Mu.Unlock()
	syncer.RunOnce(ctx)

	var refreshPayload struct {
		Provider     string   `json:"provider"`
		PlatformHost string   `json:"platform_host"`
		RepoPath     string   `json:"repo_path"`
		Owner        string   `json:"owner"`
		Name         string   `json:"name"`
		Number       int      `json:"number"`
		HeadSHA      string   `json:"head_sha"`
		SyncedAt     string   `json:"synced_at"`
		Warnings     []string `json:"warnings"`
	}
	select {
	case event := <-events:
		require.Equal("pr_detail_refreshed", event.Event.Type)
		raw, marshalErr := json.Marshal(event.Event.Data)
		require.NoError(marshalErr)
		require.NoError(json.Unmarshal(raw, &refreshPayload))
	default:
		require.Fail("scheduled merged-actor repair did not refresh the open detail")
	}
	assert.Equal("gitlab", refreshPayload.Provider)
	assert.Equal(ref.Host, refreshPayload.PlatformHost)
	assert.Equal(ref.RepoPath, refreshPayload.RepoPath)
	assert.Equal(ref.Owner, refreshPayload.Owner)
	assert.Equal(ref.Name, refreshPayload.Name)
	assert.Equal(7, refreshPayload.Number)
	assert.Equal("abc123", refreshPayload.HeadSHA)
	assert.Equal(now.Format(time.RFC3339), refreshPayload.SyncedAt)
	assert.Empty(refreshPayload.Warnings)

	after, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, after.StatusCode, string(after.Body))
	require.NotNil(after.JSON200)
	require.NotNil(after.JSON200.Events)
	require.Len(after.JSON200.Events, 1)
	assert.Equal("merge-admin", after.JSON200.Events[0].Author)
}

func forgejoHostCloneSyncConfig(tokenLine string) string {
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
platform = "forgejo"
platform_host = "code.example.com"
owner = "acme"
name = "widget"
`, tokenLine)
}

// TestAPIForgejoHostCloneFetchFollowsReloadedToken drives the full
// stack the way main.go wires it: the gitclone.Manager holds the
// host-level clone source (tokenauth.CloneKey) from the shared
// SourceSet, sync runs are triggered over HTTP, and the credential git
// actually receives is captured per invocation. The reload rotates the
// Forgejo host's token, and git fetches must follow it.
func TestAPIForgejoHostCloneFetchFollowsReloadedToken(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	t.Setenv("KENN_FORGE_GITHUB_TOKEN", "github-token")
	t.Setenv("KENN_FORGE_FORGEJO_TOKEN_A", "forgejo-token")
	t.Setenv("KENN_FORGE_FORGEJO_TOKEN_B", "rotated-token")

	remote := filepath.Join(dir, "remote.git")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", remote)
	work := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "clone", remote, work)
	gitfixture.Run(t, work, "config", "user.email", "test@test.com")
	gitfixture.Run(t, work, "config", "user.name", "Test")
	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "base commit")
	gitfixture.Run(t, work, "push", "origin", "main")

	capturePath := installCredentialCapturingGit(t, dir)
	cloneURL := gitLocalRemoteURL(remote)

	cfgPath := filepath.Join(dir, "config.toml")
	writeConfigToml(t, cfgPath, forgejoHostCloneSyncConfig(
		`token_env = "KENN_FORGE_FORGEJO_TOKEN_A"`,
	))
	cfg, err := config.Load(cfgPath)
	require.NoError(err)

	// Boot registration mirrors collectProviderTokenSources and
	// buildProviderStartup: provider plans plus the host-level clone
	// source, with the clone manager keyed by that source.
	sourceSet, cloneSrc := reloadTestTokenSources(
		t, cfgPath, tokenauth.CloneKey("code.example.com"),
	)
	bootToken, err := cloneSrc.Token(ctx)
	require.NoError(err)
	require.Equal("forgejo-token", bootToken)

	repoRef := platform.RepoRef{
		Platform:           platform.KindForgejo,
		Host:               "code.example.com",
		Owner:              "acme",
		Name:               "widget",
		RepoPath:           "acme/widget",
		PlatformID:         42,
		PlatformExternalID: "42",
		WebURL:             "https://code.example.com/acme/widget",
		CloneURL:           cloneURL,
		DefaultBranch:      "main",
	}
	registry, err := platform.NewRegistry(&serverfake.ApiTestGitLabProvider{Ref: repoRef})
	require.NoError(err)

	database := dbtest.Open(t)
	clones := gitclone.New(filepath.Join(dir, "clones"), gitclone.HostSources{
		"code.example.com": cloneSrc,
	})
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, clones,
		[]ghclient.RepoRef{{
			Platform:           platform.KindForgejo,
			Owner:              "acme",
			Name:               "widget",
			PlatformHost:       "code.example.com",
			RepoPath:           "acme/widget",
			PlatformRepoID:     42,
			PlatformExternalID: "42",
			CloneURL:           cloneURL,
			DefaultBranch:      "main",
		}},
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := NewWithConfig(
		database, syncer, clones, nil, cfg, cfgPath,
		ServerOptions{Clones: clones, TokenSources: sourceSet},
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	waitForConfigWatcher(t, srv, 2*time.Second)
	stream := streamConfigEvents(t, srv)
	defer stream.Close()

	// Each phase triggers one sync run over HTTP and waits for the
	// repo's completion timestamp to advance: every networked git
	// operation of that run happens before UpdateRepoSyncCompleted, so
	// reading the capture file afterwards cannot race a slow trailing
	// fetch into the next phase's assertions.
	identity := platformdb.DBRepoIdentity(repoRef)
	runSync := func(prevCompleted *time.Time) *time.Time {
		rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/sync", nil)
		require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())
		var completed *time.Time
		require.Eventually(func() bool {
			row, err := database.GetRepoByIdentity(ctx, identity)
			if err != nil || row == nil || row.LastSyncCompletedAt == nil {
				return false
			}
			if prevCompleted != nil && row.LastSyncCompletedAt.Equal(*prevCompleted) {
				return false
			}
			completed = row.LastSyncCompletedAt
			return true
		}, 20*time.Second, 25*time.Millisecond, "sync run did not complete")
		return completed
	}

	completed := runSync(nil)
	bootCreds := readCapturedCredentials(t, capturePath)
	require.NotEmpty(bootCreds)
	for _, token := range bootCreds {
		assert.Equal("forgejo-token", token)
	}

	// The host rotates to a new env var. Git fetches must follow it
	// without a restart, not stay pinned to the credential startup
	// handed the clone manager.
	writeConfigToml(t, cfgPath, forgejoHostCloneSyncConfig(
		`token_env = "KENN_FORGE_FORGEJO_TOKEN_B"`,
	))
	ev := waitForConfigEvent(t, stream, 2*time.Second)
	require.True(ev.Valid, "reload error: %s", ev.Error)
	assert.False(ev.RestartRequired)

	completed = runSync(completed)
	rotatedCreds := readCapturedCredentials(t, capturePath)
	require.Greater(len(rotatedCreds), len(bootCreds))
	for _, token := range rotatedCreds[len(bootCreds):] {
		assert.Equal("rotated-token", token)
	}

	// Removing every credential for a host with tracked repos is an
	// invalid reload by design (the required repo plan no longer
	// resolves), so the daemon keeps last-known-good: git keeps using
	// the rotated token rather than a cleared or stale credential.
	writeConfigToml(t, cfgPath, forgejoHostCloneSyncConfig(""))
	ev = waitForConfigEvent(t, stream, 2*time.Second)
	require.False(ev.Valid)
	assert.NotEmpty(ev.Error)

	runSync(completed)
	finalCreds := readCapturedCredentials(t, capturePath)
	require.Greater(len(finalCreds), len(rotatedCreds))
	for _, token := range finalCreds[len(rotatedCreds):] {
		assert.Equal("rotated-token", token)
	}
}

// installCredentialCapturingGit prepends a git wrapper to PATH that, on
// every invocation carrying an injected credential.helper (i.e. every
// networked git command kenn-forge authenticates), runs the helper's get
// action and appends its output to the returned capture file before
// exec'ing the real git. Local reads run without a helper and leave no
// trace, so the file records exactly the credentials git was given.
func installCredentialCapturingGit(t *testing.T, dir string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	capturePath := filepath.Join(dir, "git-credentials.txt")
	gitWrapperDir := filepath.Join(dir, "git-wrapper")
	require.NoError(t, os.MkdirAll(gitWrapperDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gitWrapperDir, "git"), []byte(`#!/bin/sh
set -eu
`+gitfake.CredentialHelperRunner+`
out="${KENN_FORGE_TEST_GIT_CAPTURE:?}"
helper=""
i=0
count="${GIT_CONFIG_COUNT:-0}"
while [ "$i" -lt "$count" ]; do
	eval "key=\${GIT_CONFIG_KEY_$i:-}"
	eval "value=\${GIT_CONFIG_VALUE_$i:-}"
	if [ "$key" = "credential.helper" ]; then
		helper="$value"
	fi
	i=$((i + 1))
done
if [ -n "$helper" ]; then
	run_credential_helper "$helper" get >> "$out"
	echo "---" >> "$out"
fi
exec "$KENN_FORGE_TEST_REAL_GIT" "$@"
`), 0o755))
	t.Setenv("PATH", gitWrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KENN_FORGE_TEST_GIT_CAPTURE", capturePath)
	t.Setenv("KENN_FORGE_TEST_REAL_GIT", realGit)
	return capturePath
}

func readCapturedCredentials(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return parseCapturedCredentials(string(data))
}

func parseCapturedCredentials(raw string) []string {
	var tokens []string
	for line := range strings.SplitSeq(strings.TrimSpace(raw), "\n") {
		if token, ok := strings.CutPrefix(line, "password="); ok {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func TestAPICloseIssue(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	srv, database, _ := setupTestServer(t)
	handlerNow := testEDTTime(10, 45)
	setTestServerNow(t, srv, handlerNow)
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetIssueGithubStateWithResponse(t.Context(), &generated.SetIssueGithubStateRequestOptions{PathParams: &generated.SetIssueGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.SetIssueGithubStateBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	issue, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	require.Equal("closed", issue.State)
	assertTimePtrEqualsUTC(t, issue.ClosedAt, handlerNow)
}

func TestAPIGetIssueWorkspaceUsesProviderScopedLookup(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)
	for _, provider := range []string{"github", "gitlab"} {
		repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
			Platform:       provider,
			PlatformHost:   "forge.example.com",
			PlatformRepoID: "repo-" + provider + "-widget",
			Owner:          "acme",
			Name:           "widget",
		})
		require.NoError(err)
		_, err = database.UpsertIssue(ctx, &db.Issue{
			RepoID:         repoID,
			PlatformID:     int64(len(provider)) * 1000,
			Number:         7,
			URL:            "https://forge.example.com/acme/widget/issues/7",
			Title:          provider + " issue",
			Author:         "testuser",
			State:          "open",
			CreatedAt:      time.Now().UTC().Truncate(time.Second),
			UpdatedAt:      time.Now().UTC().Truncate(time.Second),
			LastActivityAt: time.Now().UTC().Truncate(time.Second),
		})
		require.NoError(err)
	}
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:              "github-issue-workspace",
		Platform:        "github",
		PlatformHost:    "forge.example.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypeIssue,
		ItemNumber:      7,
		GitHeadRef:      "kenn-forge/issue-7",
		WorkspaceBranch: "kenn-forge/issue-7",
		WorktreePath:    filepath.Join(t.TempDir(), "github"),
		TmuxSession:     "github-issue-workspace",
		Status:          "ready",
	}))
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:              "gitlab-issue-workspace",
		Platform:        "gitlab",
		PlatformHost:    "forge.example.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypeIssue,
		ItemNumber:      7,
		GitHeadRef:      "kenn-forge/issue-7",
		WorkspaceBranch: "kenn-forge/issue-7",
		WorktreePath:    filepath.Join(t.TempDir(), "gitlab"),
		TmuxSession:     "gitlab-issue-workspace",
		Status:          "ready",
	}))

	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", &config.Config{Tmux: config.Tmux{
		Command: []string{filepath.Join(t.TempDir(), "missing-tmux")},
	}}, ServerOptions{
		WorktreeDir:                        t.TempDir(),
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/host/forge.example.com/issues/gitlab/acme/widget/7",
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body rawIssueDetailResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	if assert.NotNil(body.Workspace) {
		assert.Equal("gitlab-issue-workspace", body.Workspace.ID)
	}
	if assert.NotNil(body.Issue) {
		assert.Equal("gitlab issue", body.Issue.Title)
	}
}

func TestAPIGetPRWorkspaceUsesProviderScopedLookup(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)
	for _, provider := range []string{"github", "gitlab"} {
		repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
			Platform:       provider,
			PlatformHost:   "forge.example.com",
			PlatformRepoID: "repo-" + provider + "-widget",
			Owner:          "acme",
			Name:           "widget",
		})
		require.NoError(err)
		_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
			RepoID:         repoID,
			PlatformID:     int64(len(provider)) * 1000,
			Number:         7,
			URL:            "https://forge.example.com/acme/widget/pull/7",
			Title:          provider + " PR",
			Author:         "testuser",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			CreatedAt:      time.Now().UTC().Truncate(time.Second),
			UpdatedAt:      time.Now().UTC().Truncate(time.Second),
			LastActivityAt: time.Now().UTC().Truncate(time.Second),
		})
		require.NoError(err)
	}
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:              "github-pr-workspace",
		Platform:        "github",
		PlatformHost:    "forge.example.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      7,
		GitHeadRef:      "feature",
		WorkspaceBranch: "kenn-forge/pr-7",
		WorktreePath:    filepath.Join(t.TempDir(), "github"),
		TmuxSession:     "github-pr-workspace",
		Status:          "ready",
	}))
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:              "gitlab-pr-workspace",
		Platform:        "gitlab",
		PlatformHost:    "forge.example.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      7,
		GitHeadRef:      "feature",
		WorkspaceBranch: "kenn-forge/pr-7",
		WorktreePath:    filepath.Join(t.TempDir(), "gitlab"),
		TmuxSession:     "gitlab-pr-workspace",
		Status:          "ready",
	}))

	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", &config.Config{Tmux: config.Tmux{
		Command: []string{filepath.Join(t.TempDir(), "missing-tmux")},
	}}, ServerOptions{
		WorktreeDir:                        t.TempDir(),
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/host/forge.example.com/pulls/gitlab/acme/widget/7",
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body pullapi.MergeRequestDetailResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	if assert.NotNil(body.Workspace) {
		assert.Equal("gitlab-pr-workspace", body.Workspace.ID)
	}
	if assert.NotNil(body.MergeRequest) {
		assert.Equal("gitlab PR", body.MergeRequest.Title)
	}
}

func TestAPICreateWorkspaceRejectsEmptyProviderForAmbiguousRepo(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	srv, database, _ := setupTestServerWithReposAndOptions(
		t, &serverfake.MockGH{}, serverfake.DefaultTestRepos,
		ServerOptions{
			WorktreeDir:                        t.TempDir(),
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	for _, provider := range []string{"github", "gitlab"} {
		repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
			Platform:     provider,
			PlatformHost: "forge.example.com",
			Owner:        "acme",
			Name:         "widget",
		})
		require.NoError(err)
		_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
			RepoID:         repoID,
			PlatformID:     int64(len(provider)) * 1000,
			Number:         7,
			URL:            "https://forge.example.com/acme/widget/pull/7",
			Title:          provider + " PR",
			Author:         "testuser",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			CreatedAt:      time.Now().UTC().Truncate(time.Second),
			UpdatedAt:      time.Now().UTC().Truncate(time.Second),
			LastActivityAt: time.Now().UTC().Truncate(time.Second),
		})
		require.NoError(err)
	}
	client := setupTestClient(t, srv)
	provider := ""

	resp, err := client.HTTP.CreateWorkspaceWithResponse(ctx, &generated.CreateWorkspaceRequestOptions{Body: &generated.CreateWorkspaceInputBody{
		Provider:     provider,
		PlatformHost: "forge.example.com",
		Owner:        "acme",
		Name:         "widget",
		MrNumber:     7,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadRequest, resp.StatusCode, string(resp.Body))

	var problem serverfake.RawProblemDetail
	require.NoError(json.Unmarshal(resp.Body, &problem))
	assert.Equal("validationError", problem.Code)
	assert.Equal("body.provider", problem.Details["field"])
}

func TestAPICreateWorkspaceRejectsOmittedProviderForUnambiguousRepo(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	ctx := t.Context()

	srv, database, _ := setupTestServerWithReposAndOptions(
		t, &serverfake.MockGH{}, serverfake.DefaultTestRepos,
		ServerOptions{
			WorktreeDir:                        t.TempDir(),
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     7000,
		Number:         7,
		URL:            "https://github.com/acme/widget/pull/7",
		Title:          "github PR",
		Author:         "testuser",
		State:          "open",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CreatedAt:      time.Now().UTC().Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Truncate(time.Second),
	})
	require.NoError(err)

	payload := strings.NewReader(`{
		"platform_host": "github.com",
		"owner": "acme",
		"name": "widget",
		"mr_number": 7
	}`)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/workspaces", payload)
	req.Header.Set("content-type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusUnprocessableEntity, rr.Code, rr.Body.String())
}

func TestMRListIncludesWorktreeLinks(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	prID := serverfake.SeedPR(t, database, "acme", "widget", 1)

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(database.SetWorktreeLinks(
		t.Context(),
		[]db.WorktreeLink{
			{
				MergeRequestID: prID,
				WorktreeKey:    "wt-abc",
				WorktreePath:   "/tmp/wt",
				WorktreeBranch: "feature",
				LinkedAt:       now,
			},
		}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/pulls", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(body, `"worktree_links"`)
	require.Contains(body, `"host_key":"`+srv.fleetAPI.SelfKey("")+`"`)
	require.Contains(body, `"worktree_key":"wt-abc"`)
	require.Contains(body, `"worktree_path":"/tmp/wt"`)
	require.Contains(body, `"worktree_branch":"feature"`)
}

func TestMRDetailIncludesWorktreeLinks(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	prID := serverfake.SeedPR(t, database, "acme", "widget", 1)

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(database.SetWorktreeLinks(
		t.Context(),
		[]db.WorktreeLink{
			{
				MergeRequestID: prID,
				WorktreeKey:    "wt-detail",
				WorktreePath:   "/tmp/detail",
				LinkedAt:       now,
			},
		}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/api/v1/pulls/gh/acme/widget/1", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(body, `"worktree_links"`)
	require.Contains(body, `"host_key":"`+srv.fleetAPI.SelfKey("")+`"`)
	require.Contains(body, `"worktree_key":"wt-detail"`)
}

func TestAPIGitLabProviderCapabilitiesExposeOnResponses(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := setupGitLabCapabilityServer(t)
	ctx := t.Context()

	rawPulls := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, rawPulls.Code, rawPulls.Body.String())
	var pulls []map[string]any
	require.NoError(json.NewDecoder(rawPulls.Body).Decode(&pulls))
	require.Len(pulls, 1)
	pullRepo := pulls[0]["repo"].(map[string]any)
	pullCaps := pullRepo["capabilities"].(map[string]any)
	assert.Equal(true, pullCaps["read_merge_requests"])
	assert.Equal(true, pullCaps["read_issues"])
	assert.Equal(false, pullCaps["comment_mutation"])
	assert.Equal(false, pullCaps["state_mutation"])
	assert.Equal(false, pullCaps["merge_mutation"])
	assert.Equal(false, pullCaps["review_mutation"])
	assert.Equal(false, pullCaps["workflow_approval"])
	assert.Equal(false, pullCaps["ready_for_review"])
	assert.Equal(false, pullCaps["issue_mutation"])
	assert.Equal(false, pullCaps["review_draft_mutation"])
	assert.Equal(false, pullCaps["review_thread_resolution"])
	assert.Equal(false, pullCaps["read_review_threads"])
	assert.Equal(false, pullCaps["native_multiline_ranges"])
	assert.Empty(pullCaps["supported_review_actions"])

	rawDetail := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7",
		nil)

	require.Equal(http.StatusOK, rawDetail.Code, rawDetail.Body.String())
	var detail map[string]any
	require.NoError(json.NewDecoder(rawDetail.Body).Decode(&detail))
	detailRepo := detail["repo"].(map[string]any)
	detailCaps := detailRepo["capabilities"].(map[string]any)
	assert.Equal(true, detailCaps["read_merge_requests"])
	assert.Equal(false, detailCaps["merge_mutation"])
	assert.Equal(false, detailCaps["review_draft_mutation"])

	rawSummaries := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/repos/summary", nil)
	require.Equal(http.StatusOK, rawSummaries.Code, rawSummaries.Body.String())
	var summaries []map[string]any
	require.NoError(json.NewDecoder(rawSummaries.Body).Decode(&summaries))
	require.Len(summaries, 1)
	summaryRepo := summaries[0]["repo"].(map[string]any)
	summaryCaps := summaryRepo["capabilities"].(map[string]any)
	assert.Equal(true, summaryCaps["read_repositories"])
	assert.Equal(false, summaryCaps["issue_mutation"])
	assert.Equal(false, summaryCaps["read_review_threads"])

	rawRepos := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/repos", nil)
	require.Equal(http.StatusOK, rawRepos.Code, rawRepos.Body.String())
	var repos []map[string]any
	require.NoError(json.NewDecoder(rawRepos.Body).Decode(&repos))
	require.Len(repos, 1)
	repoCaps := repos[0]["capabilities"].(map[string]any)
	assert.Equal(true, repoCaps["read_repositories"])
	assert.Equal(false, repoCaps["comment_mutation"])

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{
		{
			MergeRequestID: mr.ID,
			EventType:      "issue_comment",
			Author:         "ada",
			Body:           "GitLab activity comment",
			CreatedAt:      time.Now().UTC(),
			DedupeKey:      "gitlab-activity-comment",
		},
	}))

	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	rawActivity := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
	require.Equal(http.StatusOK, rawActivity.Code, rawActivity.Body.String())
	var activity map[string]any
	require.NoError(json.NewDecoder(rawActivity.Body).Decode(&activity))
	activityItems := activity["items"].([]any)
	require.NotEmpty(activityItems)
	activityRepo := activityItems[0].(map[string]any)["repo"].(map[string]any)
	assert.Equal("gitlab", activityRepo["provider"])
	assert.Equal("gitlab.example.com", activityRepo["platform_host"])
	assert.Equal("group/project", activityRepo["repo_path"])
	assert.NotContains(activityRepo, "capabilities")

	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:           "gitlabcap0000001",
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoOwner:    "group",
		RepoName:     "project",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   7,
		GitHeadRef:   "feature/gitlab",
		WorktreePath: filepath.Join(t.TempDir(), "workspace"),
		TmuxSession:  "kenn-forge-gitlabcap0000001",
		Status:       "creating",
	}))
	rawWorkspaces := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/workspaces", nil)
	require.Equal(http.StatusOK, rawWorkspaces.Code, rawWorkspaces.Body.String())
	var workspaces map[string]any
	require.NoError(json.NewDecoder(rawWorkspaces.Body).Decode(&workspaces))
	workspaceItems := workspaces["workspaces"].([]any)
	require.Len(workspaceItems, 1)
	workspaceRepo := workspaceItems[0].(map[string]any)["repo"].(map[string]any)
	workspaceCaps := workspaceRepo["capabilities"].(map[string]any)
	assert.Equal("gitlab", workspaceRepo["provider"])
	assert.Equal(true, workspaceCaps["read_repositories"])
	assert.Equal(false, workspaceCaps["merge_mutation"])
}

func TestAPIGitLabUnsupportedMutationsReturnCodedCapabilityErrors(t *testing.T) {
	runParallelServerTest(t)
	srv, _ := setupGitLabCapabilityServer(t)

	tests := []struct {
		name       string
		method     string
		path       string
		body       any
		capability string
	}{
		{
			name:       "PR comment",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/comments",
			body:       map[string]string{"body": "hello"},
			capability: "comment_mutation",
		},
		{
			name:       "PR content",
			method:     http.MethodPatch,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7",
			body:       map[string]string{"title": "Updated title"},
			capability: "state_mutation",
		},
		{
			name:       "review approval",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/approve",
			body:       map[string]string{"body": "looks good"},
			capability: "review_mutation",
		},
		{
			name:       "workflow approval",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/approve-workflows",
			body:       nil,
			capability: "workflow_approval",
		},
		{
			name:       "ready for review",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/ready-for-review",
			body:       nil,
			capability: "ready_for_review",
		},
		{
			name:   "merge",
			method: http.MethodPost,
			path:   "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/merge",
			body: map[string]string{
				"method":         "squash",
				"commit_title":   "Merge MR",
				"commit_message": "Merge GitLab MR",
			},
			capability: "merge_mutation",
		},
		{
			name:       "PR state",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/github-state",
			body:       map[string]string{"state": "closed"},
			capability: "state_mutation",
		},
		{
			name:       "publish review draft",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft/publish",
			body:       map[string]string{"action": "comment", "body": "review body"},
			capability: "review_draft_mutation",
		},
		{
			name:   "create review draft comment",
			method: http.MethodPost,
			path:   "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft/comments",
			body: map[string]any{
				"body": "review body",
				"range": map[string]any{
					"path":          "internal/server/api_test.go",
					"side":          "right",
					"line":          42,
					"new_line":      42,
					"line_type":     "add",
					"diff_head_sha": "abc123",
				},
			},
			capability: "review_draft_mutation",
		},
		{
			name:       "resolve review thread",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-threads/99/resolve",
			body:       nil,
			capability: "review_thread_resolution",
		},
		{
			name:       "unresolve review thread",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-threads/99/unresolve",
			body:       nil,
			capability: "review_thread_resolution",
		},
		{
			name:       "issue creation",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/issues/gl/group/project",
			body:       map[string]string{"title": "New issue", "body": "Issue body"},
			capability: "issue_mutation",
		},
		{
			name:       "issue comment",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/issues/gl/group/project/11/comments",
			body:       map[string]string{"body": "hello"},
			capability: "comment_mutation",
		},
		{
			name:       "issue state",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/issues/gl/group/project/11/github-state",
			body:       map[string]string{"state": "closed"},
			capability: "state_mutation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			rr := testutil.DoJSON(t, srv, tt.method, tt.path, tt.body)
			require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
			assertUnsupportedCapabilityProblem(
				t, rr.Body, "gitlab", "gitlab.example.com", tt.capability,
			)
		})
	}
}

// TestAPIUnsupportedCapabilityEnvelope is the wire-level guarantee that
// the gitlab capability gate emits an RFC 9457 envelope with a top-level
// `code = "unsupportedCapability"` and `details.capability` carrying the
// capability the route required. Frontend callers branch on `code`.
func TestAPIUnsupportedCapabilityEnvelope(t *testing.T) {
	runParallelServerTest(t)
	srv, _ := setupGitLabCapabilityServer(t)
	require := require.New(t)
	assert := assert.New(t)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/approve-workflows",
		nil)

	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())

	var problem serverfake.RawProblemDetail
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("unsupportedCapability", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal("workflow_approval", problem.Details["capability"])
	assert.Equal("gitlab", problem.Details["provider"])
	assert.Equal("gitlab.example.com", problem.Details["platformHost"])
}

func TestAPIDiffReviewDraftCRUDUsesLocalStorage(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReviewDraftMutation:    true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionComment},
	}
	srv, database := setupGitLabCapabilityServerWithCaps(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)

	basePath := "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft"
	getRR := testutil.DoJSON(t, srv, http.MethodGet, basePath, nil)
	require.Equal(http.StatusOK, getRR.Code, getRR.Body.String())
	var draft map[string]any
	require.NoError(json.NewDecoder(getRR.Body).Decode(&draft))
	assert.Nil(draft["draft_id"])
	assert.Empty(draft["comments"])
	assert.Equal([]any{"comment"}, draft["supported_actions"])
	assert.Equal(false, draft["native_multiline_ranges"])

	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "Please tighten this assertion.",
		"range": map[string]any{
			"path":          "internal/server/api_test.go",
			"side":          "right",
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "abc123",
			"commit_sha":    "abc123",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())
	var created map[string]any
	require.NoError(json.NewDecoder(createRR.Body).Decode(&created))
	commentID, ok := created["id"].(string)
	require.True(ok)
	assert.Equal("Please tighten this assertion.", created["body"])
	assert.Equal("internal/server/api_test.go", created["path"])
	assert.Equal("right", created["side"])
	assert.InDelta(42, created["line"], 0)

	getRR = testutil.DoJSON(t, srv, http.MethodGet, basePath, nil)
	require.Equal(http.StatusOK, getRR.Code, getRR.Body.String())
	require.NoError(json.NewDecoder(getRR.Body).Decode(&draft))
	assert.NotEmpty(draft["draft_id"])
	require.Len(draft["comments"], 1)

	editRR := testutil.DoJSON(
		t,
		srv,
		http.MethodPatch,
		basePath+"/comments/"+commentID,
		map[string]any{
			"body": "Updated draft body.",
			"range": map[string]any{
				"path":          "internal/server/api_test.go",
				"side":          "right",
				"line":          43,
				"new_line":      43,
				"line_type":     "add",
				"diff_head_sha": "abc123",
				"commit_sha":    "abc123",
			},
		})

	require.Equal(http.StatusOK, editRR.Code, editRR.Body.String())
	var edited map[string]any
	require.NoError(json.NewDecoder(editRR.Body).Decode(&edited))
	assert.Equal("Updated draft body.", edited["body"])
	assert.InDelta(43, edited["line"], 0)

	deleteRR := testutil.DoJSON(
		t,
		srv,
		http.MethodDelete,
		basePath+"/comments/"+commentID,
		nil)

	require.Equal(http.StatusOK, deleteRR.Code, deleteRR.Body.String())
	comments, err := database.ListMRReviewDraftComments(ctx, draftIDFromResponse(t, draft))
	require.NoError(err)
	assert.Empty(comments)

	discardRR := testutil.DoJSON(t, srv, http.MethodDelete, basePath, nil)
	require.Equal(http.StatusOK, discardRR.Code, discardRR.Body.String())
	storedDraft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	assert.Nil(storedDraft)
}

func TestAPIDiffReviewDraftRejectsInvalidLineCoordinates(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReviewDraftMutation:    true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionComment},
	}
	srv, database := setupGitLabCapabilityServerWithCaps(t, &caps)
	ctx := t.Context()

	basePath := "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft"
	validRange := map[string]any{
		"path":          "internal/server/api_test.go",
		"side":          "right",
		"line":          42,
		"new_line":      42,
		"line_type":     "add",
		"diff_head_sha": "abc123",
	}
	invalidRanges := []map[string]any{
		{
			"path":          "internal/server/api_test.go",
			"side":          "right",
			"line":          42,
			"new_line":      42,
			"line_type":     "added",
			"diff_head_sha": "abc123",
		},
		{
			"path":      "internal/server/api_test.go",
			"side":      "right",
			"line":      42,
			"new_line":  42,
			"line_type": "add",
		},
		{
			"path":          "internal/server/api_test.go",
			"side":          "right",
			"start_line":    41,
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "abc123",
		},
		{
			"path":          "internal/server/api_test.go",
			"side":          "right",
			"start_side":    "right",
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "abc123",
		},
		{
			"path":          "internal/server/api_test.go",
			"side":          "right",
			"start_side":    "right",
			"start_line":    43,
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "abc123",
		},
	}
	for _, lineRange := range invalidRanges {
		rr := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
			"body":  "Invalid range.",
			"range": lineRange,
		})

		require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	}

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	storedDraft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	assert.Nil(storedDraft)

	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body":  "Valid draft.",
		"range": validRange,
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())
	var created map[string]any
	require.NoError(json.NewDecoder(createRR.Body).Decode(&created))
	commentID, ok := created["id"].(string)
	require.True(ok)

	patchRR := testutil.DoJSON(t, srv, http.MethodPatch, basePath+"/comments/"+commentID, map[string]any{
		"body": "Invalid patch.",
		"range": map[string]any{
			"path":          "internal/server/api_test.go",
			"side":          "right",
			"start_side":    "right",
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "abc123",
		},
	})

	require.Equal(http.StatusBadRequest, patchRR.Code, patchRR.Body.String())
	storedDraft, err = database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	require.NotNil(storedDraft)
	comments, err := database.ListMRReviewDraftComments(ctx, storedDraft.ID)
	require.NoError(err)
	require.Len(comments, 1)
	assert.Equal("Valid draft.", comments[0].Body)
}

func TestAPIPublishReviewDraftRejectsStoredCommentWithoutDiffHeadSHA(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReadReleases:           true,
		ReadCI:                 true,
		ReviewDraftMutation:    true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionComment},
	}
	srv, database := setupGitLabCapabilityServerWithCaps(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 7, "current-head", "base", "merge"))
	draft, err := database.GetOrCreateMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	line := 42
	_, err = database.CreateMRReviewDraftComment(ctx, draft.ID, db.MRReviewDraftCommentInput{
		Body: "stored by older client",
		Range: db.ReviewLineRange{
			Path:     "internal/server/api_test.go",
			Side:     "right",
			Line:     42,
			NewLine:  &line,
			LineType: "add",
		},
	})
	require.NoError(err)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft/publish",
		map[string]string{"action": "comment"})

	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
}

func TestAPIPublishReviewDraftUsesPlatformHeadSHAWhenDiffHeadIsUnavailable(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReviewDraftMutation:    true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionComment, platform.ReviewActionApprove},
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	draft, err := database.GetOrCreateMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	line := 42
	_, err = database.CreateMRReviewDraftComment(ctx, draft.ID, db.MRReviewDraftCommentInput{
		Body: "ready to approve",
		Range: db.ReviewLineRange{
			Path:        "internal/server/api_test.go",
			Side:        "right",
			Line:        42,
			NewLine:     &line,
			LineType:    "add",
			DiffHeadSHA: mr.PlatformHeadSHA,
		},
	})
	require.NoError(err)

	publishRR := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft/publish",
		map[string]string{"action": "approve", "body": "looks good"})

	require.Equal(http.StatusOK, publishRR.Code, publishRR.Body.String())
	require.Len(provider.PublishedReviews, 1)
	assert.Equal(mr.PlatformHeadSHA, provider.PublishedReviews[0].HeadSHA)
}

func TestAPIPublishReviewDraftMapsStaleProviderErrorToConflict(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReviewDraftMutation:    true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionApprove},
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()
	provider.PublishReviewErr = &platform.Error{
		Code:         platform.ErrCodeStaleState,
		Provider:     platform.KindGitLab,
		PlatformHost: "gitlab.example.com",
		Capability:   "approve_merge_request",
		Hint:         "approval 99 was removed after the head moved",
	}
	provider.MergeRequests[0].HeadSHA = "fresh-head"
	provider.BlockNextMRFetch.Store(true)
	provider.MrFetchStarted = make(chan struct{}, 1)

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 7, "gitlab-head", "base", "merge-base"))
	draft, err := database.GetOrCreateMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	line := 42
	_, err = database.CreateMRReviewDraftComment(ctx, draft.ID, db.MRReviewDraftCommentInput{
		Body: "ready to approve",
		Range: db.ReviewLineRange{
			Path:        "internal/server/api_test.go",
			Side:        "right",
			Line:        42,
			NewLine:     &line,
			LineType:    "add",
			DiffHeadSHA: "gitlab-head",
		},
	})
	require.NoError(err)

	publishRR := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft/publish",
		map[string]string{"action": "approve", "body": "looks good"})

	require.Equal(http.StatusConflict, publishRR.Code, publishRR.Body.String())
	var problem serverfake.RawProblemDetail
	require.NoError(json.NewDecoder(publishRR.Body).Decode(&problem))
	assert.Equal("conflict", problem.Code)
	assert.Contains(problem.Detail, "approval 99 was removed")
	require.NotNil(problem.Details)
	details := problem.Details
	assert.Equal("stale_state", details["reason"])
	assert.Equal("gitlab", details["provider"])
	assert.Equal("gitlab.example.com", details["platformHost"])
	assert.Equal("approval 99 was removed after the head moved", details["context"])
	require.Len(provider.PublishedReviews, 1)
	select {
	case <-provider.MrFetchStarted:
	case <-time.After(10 * time.Second):
		require.Fail("background refresh did not reach the provider")
	}
	require.Eventually(func() bool {
		synced, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
		return err == nil && synced != nil && synced.PlatformHeadSHA == "fresh-head"
	}, 10*time.Second, 10*time.Millisecond)
}

func TestAPIPublishReviewDraftMapsPartialStaleProviderErrorToConflict(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReviewDraftMutation:    true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionApprove},
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()
	provider.PublishReviewErr = &platform.DiffReviewPublishPartialError{
		Err: &platform.Error{
			Code:         platform.ErrCodeStaleState,
			Provider:     platform.KindGitLab,
			PlatformHost: "gitlab.example.com",
			Capability:   "approve_merge_request",
			Hint:         "summary note already posted before stale approval",
		},
	}
	provider.MergeRequests[0].HeadSHA = "fresh-head"
	provider.BlockNextMRFetch.Store(true)
	provider.MrFetchStarted = make(chan struct{}, 1)

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 7, "gitlab-head", "base", "merge-base"))
	draft, err := database.GetOrCreateMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	line := 42
	_, err = database.CreateMRReviewDraftComment(ctx, draft.ID, db.MRReviewDraftCommentInput{
		Body: "ready to approve",
		Range: db.ReviewLineRange{
			Path:        "internal/server/api_test.go",
			Side:        "right",
			Line:        42,
			NewLine:     &line,
			LineType:    "add",
			DiffHeadSHA: "gitlab-head",
		},
	})
	require.NoError(err)

	publishRR := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft/publish",
		map[string]string{"action": "approve", "body": "looks good"})

	require.Equal(http.StatusConflict, publishRR.Code, publishRR.Body.String())
	var problem serverfake.RawProblemDetail
	require.NoError(json.NewDecoder(publishRR.Body).Decode(&problem))
	assert.Equal("conflict", problem.Code)
	require.NotNil(problem.Details)
	details := problem.Details
	assert.Equal("stale_state", details["reason"])
	assert.Equal(true, details["partialPublish"])
	assert.EqualValues(0, details["publishedCommentCount"])
	assert.Equal("summary note already posted before stale approval", details["context"])
	select {
	case <-provider.MrFetchStarted:
	case <-time.After(10 * time.Second):
		require.Fail("background refresh did not reach the provider")
	}
	require.Eventually(func() bool {
		synced, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
		return err == nil && synced != nil && synced.PlatformHeadSHA == "fresh-head"
	}, 10*time.Second, 10*time.Millisecond)
}

func TestAPIPublishReviewDraftRejectsMultilineRangeWithoutCapability(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReviewDraftMutation:    true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionComment},
		NativeMultilineRanges:  false,
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 7, "current-head", "base", "merge"))

	basePath := "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft"
	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "Please fix this range.",
		"range": map[string]any{
			"path":          "internal/server/api_test.go",
			"side":          "right",
			"start_side":    "right",
			"start_line":    41,
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "current-head",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	publishRR := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		basePath+"/publish",
		map[string]string{"action": "comment"})

	require.Equal(http.StatusBadRequest, publishRR.Code, publishRR.Body.String())
	require.Empty(provider.PublishedReviews)
}

func TestAPIGitLabPublishReviewDraftApprovesWithDiffPositionSHAs(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	caps := platform.Capabilities{
		ReadRepositories:      true,
		ReadMergeRequests:     true,
		ReadIssues:            true,
		ReadComments:          true,
		ReviewDraftMutation:   true,
		NativeMultilineRanges: false,
		SupportedReviewActions: []platform.ReviewAction{
			platform.ReviewActionComment,
			platform.ReviewActionApprove,
		},
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 7, "gitlab-head", "gitlab-base", "gitlab-merge-base"))

	basePath := "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft"
	getRR := testutil.DoJSON(t, srv, http.MethodGet, basePath, nil)
	require.Equal(http.StatusOK, getRR.Code, getRR.Body.String())
	var draft map[string]any
	require.NoError(json.NewDecoder(getRR.Body).Decode(&draft))
	assert.Equal([]any{"comment", "approve"}, draft["supported_actions"])
	assert.Equal(false, draft["native_multiline_ranges"])

	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "Please tighten this line.",
		"range": map[string]any{
			"path":          "internal/server/api_test.go",
			"side":          "right",
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "gitlab-head",
			"commit_sha":    "gitlab-head",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	rejectRR := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		basePath+"/publish",
		map[string]string{"action": "request_changes"})

	require.Equal(http.StatusConflict, rejectRR.Code, rejectRR.Body.String())
	assertUnsupportedCapabilityProblem(
		t, rejectRR.Body, "gitlab", "gitlab.example.com", "review_action_request_changes",
	)
	require.Empty(provider.PublishedReviews)

	publishRR := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		basePath+"/publish",
		map[string]string{"action": "approve", "body": "approved"})

	require.Equal(http.StatusOK, publishRR.Code, publishRR.Body.String())
	require.Len(provider.PublishedReviews, 1)
	published := provider.PublishedReviews[0]
	assert.Equal(platform.ReviewActionApprove, published.Action)
	assert.Equal("approved", published.Body)
	require.Len(published.Comments, 1)
	lineRange := published.Comments[0].Range
	assert.Equal("gitlab-head", lineRange.DiffHeadSHA)
	assert.Equal("gitlab-base", lineRange.DiffBaseSHA)
	assert.Equal("gitlab-merge-base", lineRange.MergeBaseSHA)
	assert.Empty(lineRange.StartSide)
	assert.Nil(lineRange.StartLine)
}

func TestAPIPublishReviewDraftPreservesDraftWhenPartialStatusIsUnknown(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReadReviewThreads:      true,
		ReviewDraftMutation:    true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionComment},
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()
	provider.PublishReviewErr = &platform.DiffReviewPublishPartialError{Err: errors.New("approval failed")}
	provider.ReviewThreadsErr = errors.New("transient review thread refresh failure")

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 7, "gitlab-head", "gitlab-base", "gitlab-merge-base"))

	basePath := "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft"
	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "Please tighten this line.",
		"range": map[string]any{
			"path":          "internal/server/api_test.go",
			"side":          "right",
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "gitlab-head",
			"commit_sha":    "gitlab-head",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	publishRR := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		basePath+"/publish",
		map[string]string{"action": "comment"})

	require.Equal(http.StatusOK, publishRR.Code, publishRR.Body.String())
	var publishStatus pullapi.ActionStatusBody
	require.NoError(json.NewDecoder(publishRR.Body).Decode(&publishStatus))
	require.Equal("partially_published", publishStatus.Status)
	require.Len(provider.PublishedReviews, 1)
	draft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	require.NotNil(draft)
	comments, err := database.ListMRReviewDraftComments(ctx, draft.ID)
	require.NoError(err)
	require.Len(comments, 1)
	require.Equal("Please tighten this line.", comments[0].Body)
}

func TestAPIPublishReviewDraftPersistsProviderReviewThreads(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReadReviewThreads:      true,
		ReviewDraftMutation:    true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionComment},
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 7, "current-head", "base", "merge"))
	staleLine := 41
	require.NoError(database.UpsertMRReviewThreads(ctx, mr.ID, []db.MRReviewThread{{
		ProviderThreadID:  "stale-thread",
		ProviderCommentID: "stale-comment",
		Body:              "stale inline comment",
		AuthorLogin:       "grace",
		Range: db.ReviewLineRange{
			Path:        "internal/server/api_test.go",
			Side:        "right",
			Line:        staleLine,
			NewLine:     &staleLine,
			LineType:    "add",
			DiffHeadSHA: "current-head",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}))
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID:     mr.ID,
		PlatformExternalID: "stale-thread",
		EventType:          "review_comment",
		Author:             "grace",
		Body:               "stale inline comment",
		CreatedAt:          time.Now().UTC(),
		DedupeKey:          "review_comment:stale-thread",
	}}))
	now := time.Now().UTC().Truncate(time.Second)
	providerUpdatedAt := now.Add(2 * time.Minute)
	provider.MergeRequests[0].UpdatedAt = providerUpdatedAt
	provider.MergeRequests[0].LastActivityAt = providerUpdatedAt
	line := 42
	replyLine := 43
	hiddenRootMetadata := `{"provider_hidden":true,"provider_hidden_reason":"OFF_TOPIC"}`
	hiddenReplyMetadata := `{"provider_hidden":true,"provider_hidden_reason":"ABUSE"}`
	provider.ReviewThreads = []platform.MergeRequestReviewThread{
		{
			ProviderThreadID:  "thread-7",
			ProviderCommentID: "comment-7",
			Body:              "Published inline comment",
			AuthorLogin:       "ada",
			MetadataJSON:      hiddenRootMetadata,
			Range: platform.DiffReviewLineRange{
				Path:        "internal/server/api_test.go",
				Side:        "right",
				Line:        42,
				NewLine:     &line,
				LineType:    "add",
				DiffHeadSHA: "current-head",
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ProviderThreadID:  "thread-7",
			ProviderCommentID: "comment-reply",
			Body:              "Reply should not replace original",
			AuthorLogin:       "grace",
			MetadataJSON:      hiddenReplyMetadata,
			Range: platform.DiffReviewLineRange{
				Path:        "internal/server/api_test.go",
				Side:        "right",
				Line:        43,
				NewLine:     &replyLine,
				LineType:    "add",
				DiffHeadSHA: "current-head",
			},
			CreatedAt: now.Add(time.Minute),
			UpdatedAt: now.Add(time.Minute),
		},
	}
	basePath := "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft"
	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "Please fix this.",
		"range": map[string]any{
			"path":          "internal/server/api_test.go",
			"side":          "right",
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "current-head",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	publishRR := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		basePath+"/publish",
		map[string]string{"action": "comment", "body": "review summary"})

	require.Equal(http.StatusOK, publishRR.Code, publishRR.Body.String())
	require.Len(provider.PublishedReviews, 1)
	assert.Equal(platform.ReviewActionComment, provider.PublishedReviews[0].Action)
	require.Len(provider.PublishedReviews[0].Comments, 1)
	assert.Equal("current-head", provider.PublishedReviews[0].Comments[0].Range.DiffHeadSHA)

	draft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	assert.Nil(draft)
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)
	assert.Equal("thread-7", threads[0].ProviderThreadID)
	assert.Equal("comment-7", threads[0].ProviderCommentID)
	assert.Equal("Published inline comment", threads[0].Body)
	assert.Equal("ada", threads[0].AuthorLogin)
	assert.Equal(42, threads[0].Range.Line)
	assert.JSONEq(hiddenRootMetadata, threads[0].MetadataJSON)
	events, err := database.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.NotEmpty(events)
	require.Len(events, 2)
	assert.Equal("review_comment", events[0].EventType)
	assert.Equal("comment-reply", events[0].PlatformExternalID)
	assert.Equal("Reply should not replace original", events[0].Body)
	assert.JSONEq(hiddenReplyMetadata, events[0].MetadataJSON)
	require.NotNil(events[0].ThreadID)
	assert.Equal("thread-7", *events[0].ThreadID)
	assert.Equal("review_comment", events[1].EventType)
	assert.Equal("comment-7", events[1].PlatformExternalID)
	assert.Equal("Published inline comment", events[1].Body)
	assert.JSONEq(hiddenRootMetadata, events[1].MetadataJSON)
	require.NotNil(events[1].ThreadID)
	assert.Equal("thread-7", *events[1].ThreadID)
	freshMR, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(freshMR)
	assert.Equal(providerUpdatedAt, freshMR.UpdatedAt)
	assert.Equal(providerUpdatedAt, freshMR.LastActivityAt)
}

func TestAPIGitLabSyncKeepsCanonicalReviewThreadWhenProviderReturnsReplies(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	caps := platform.Capabilities{
		ReadRepositories:  true,
		ReadMergeRequests: true,
		ReadIssues:        true,
		ReadComments:      true,
		ReadReviewThreads: true,
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)

	now := time.Now().UTC().Truncate(time.Second)
	originalLine := 9
	replyLine := 10
	provider.ReviewThreads = []platform.MergeRequestReviewThread{
		{
			ProviderThreadID:  "discussion-1",
			ProviderCommentID: "101",
			Body:              "original inline note",
			AuthorLogin:       "reviewer",
			Range: platform.DiffReviewLineRange{
				Path:        "src/main.go",
				Side:        "right",
				Line:        originalLine,
				NewLine:     &originalLine,
				LineType:    "add",
				DiffHeadSHA: "abc123",
			},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ProviderThreadID:  "discussion-1",
			ProviderCommentID: "102",
			Body:              "reply should not replace original",
			AuthorLogin:       "other-reviewer",
			Range: platform.DiffReviewLineRange{
				Path:        "src/main.go",
				Side:        "right",
				Line:        replyLine,
				NewLine:     &replyLine,
				LineType:    "add",
				DiffHeadSHA: "abc123",
			},
			CreatedAt: now.Add(time.Minute),
			UpdatedAt: now.Add(time.Minute),
		},
	}

	syncRR := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/sync", nil)
	require.Equal(http.StatusOK, syncRR.Code, syncRR.Body.String())

	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)
	assert.Equal("discussion-1", threads[0].ProviderThreadID)
	assert.Equal("101", threads[0].ProviderCommentID)
	assert.Equal("original inline note", threads[0].Body)
	assert.Equal("reviewer", threads[0].AuthorLogin)
	assert.Equal(originalLine, threads[0].Range.Line)

	detailRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7", nil)
	require.Equal(http.StatusOK, detailRR.Code, detailRR.Body.String())
	var detail pullapi.MergeRequestDetailResponse
	require.NoError(json.NewDecoder(detailRR.Body).Decode(&detail))
	require.Len(detail.Events, 2)
	require.NotNil(detail.Events[0].DiffThread)
	assert.Equal("review_comment", detail.Events[0].EventType)
	assert.Equal("reply should not replace original", detail.Events[0].Body)
	require.NotNil(detail.Events[0].ThreadID)
	assert.Equal("discussion-1", *detail.Events[0].ThreadID)
	require.NotNil(detail.Events[1].DiffThread)
	assert.Equal("review_comment", detail.Events[1].EventType)
	assert.Equal("original inline note", detail.Events[1].Body)
	require.NotNil(detail.Events[1].ThreadID)
	assert.Equal("discussion-1", *detail.Events[1].ThreadID)
	assert.Equal("original inline note", detail.Events[1].DiffThread.Body)
	assert.Equal("reviewer", detail.Events[1].DiffThread.AuthorLogin)
	assert.Equal("101", detail.Events[1].DiffThread.ProviderCommentID)
	assert.Equal(originalLine, detail.Events[1].DiffThread.Line)
}

func TestAPIGitLabSyncRemovesMissingReviewThreadTimelineEvents(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:  true,
		ReadMergeRequests: true,
		ReadIssues:        true,
		ReadComments:      true,
		ReadReviewThreads: true,
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)

	line := 12
	now := time.Now().UTC().Truncate(time.Second)
	providerUpdatedAt := now.Add(time.Minute)
	staleDerivedActivity := now.Add(2 * time.Minute)
	require.NoError(database.UpsertMRReviewThreads(ctx, mr.ID, []db.MRReviewThread{{
		ProviderThreadID:  "stale-thread",
		ProviderCommentID: "stale-comment",
		Body:              "stale inline note",
		AuthorLogin:       "reviewer",
		Range: db.ReviewLineRange{
			Path:        "src/stale.go",
			Side:        "right",
			Line:        line,
			NewLine:     &line,
			LineType:    "add",
			DiffHeadSHA: "abc123",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}}))
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID:     mr.ID,
		PlatformExternalID: "stale-thread",
		EventType:          "review_comment",
		Author:             "reviewer",
		Body:               "stale inline note",
		CreatedAt:          now,
		DedupeKey:          "review_comment:stale-thread",
	}}))
	_, err = database.WriteDB().ExecContext(ctx,
		`UPDATE forge_merge_requests SET last_activity_at = ? WHERE id = ?`,
		staleDerivedActivity, mr.ID,
	)
	require.NoError(err)
	provider.MergeRequests[0].UpdatedAt = providerUpdatedAt
	provider.MergeRequests[0].LastActivityAt = providerUpdatedAt
	provider.ReviewThreads = nil

	syncRR := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/sync", nil)
	require.Equal(http.StatusOK, syncRR.Code, syncRR.Body.String())

	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Empty(threads)
	detailRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7", nil)
	require.Equal(http.StatusOK, detailRR.Code, detailRR.Body.String())
	var detail pullapi.MergeRequestDetailResponse
	require.NoError(json.NewDecoder(detailRR.Body).Decode(&detail))
	require.Empty(detail.Events)
	assert.Equal(t, providerUpdatedAt, detail.MergeRequest.LastActivityAt)
}

func TestAPIGitLabSyncPrunesLegacyPositionedNoteCommentEvents(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	caps := platform.Capabilities{
		ReadRepositories:  true,
		ReadMergeRequests: true,
		ReadIssues:        true,
		ReadComments:      true,
		ReadReviewThreads: true,
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID:     mr.ID,
		PlatformExternalID: "2",
		EventType:          "issue_comment",
		Author:             "reviewer",
		Body:               "legacy inline diff note",
		CreatedAt:          now,
		DedupeKey:          "gitlab:gitlab.example.com:group/project:mr:7:note:2",
	}}))
	line := 9
	provider.ReviewThreads = []platform.MergeRequestReviewThread{{
		ProviderThreadID:  "discussion-2",
		ProviderCommentID: "2",
		Body:              "canonical inline diff note",
		AuthorLogin:       "reviewer",
		Range: platform.DiffReviewLineRange{
			Path:        "src/main.go",
			Side:        "right",
			Line:        line,
			NewLine:     &line,
			LineType:    "add",
			DiffHeadSHA: "abc123",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}}

	syncRR := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/sync", nil)
	require.Equal(http.StatusOK, syncRR.Code, syncRR.Body.String())

	detailRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7", nil)
	require.Equal(http.StatusOK, detailRR.Code, detailRR.Body.String())
	var detail pullapi.MergeRequestDetailResponse
	require.NoError(json.NewDecoder(detailRR.Body).Decode(&detail))
	require.Len(detail.Events, 1)
	assert.Equal("review_comment", detail.Events[0].EventType)
	require.NotNil(detail.Events[0].DiffThread)
	assert.Equal("canonical inline diff note", detail.Events[0].DiffThread.Body)

	events, err := database.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	for _, event := range events {
		assert.NotEqual("issue_comment", event.EventType)
	}
}

func TestAPIPublishReviewDraftReconcilesAfterTransientThreadIngestFailure(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReadReviewThreads:      true,
		ReviewDraftMutation:    true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionComment},
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 7, "current-head", "base", "merge"))
	now := time.Now().UTC().Truncate(time.Second)
	line := 42
	hiddenMetadata := `{"provider_hidden":true,"provider_hidden_reason":"ABUSE"}`
	provider.ReviewThreads = []platform.MergeRequestReviewThread{{
		ProviderThreadID:  "thread-atomic",
		ProviderCommentID: "comment-atomic",
		Body:              "hidden provider comment",
		AuthorLogin:       "reviewer",
		MetadataJSON:      hiddenMetadata,
		Range: platform.DiffReviewLineRange{
			Path: "internal/server/api_test.go", Side: "right", Line: line,
			NewLine: &line, LineType: "add", DiffHeadSHA: "current-head",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}}
	var reviewThreadCalls atomic.Int32
	reviewThreadRefreshStarted := make(chan struct{}, 1)
	provider.ReviewThreadsFn = func(
		context.Context,
		platform.RepoRef,
		int,
	) ([]platform.MergeRequestReviewThread, error) {
		call := reviewThreadCalls.Add(1)
		if call == 1 {
			return nil, errors.New("transient review thread refresh failure")
		}
		if call == 2 {
			reviewThreadRefreshStarted <- struct{}{}
		}
		return provider.ReviewThreads, nil
	}

	basePath := "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft"
	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "Please fix this.",
		"range": map[string]any{
			"path":          "internal/server/api_test.go",
			"side":          "right",
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "current-head",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	publishRR := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		basePath+"/publish",
		map[string]string{"action": "comment"})

	require.Equal(http.StatusOK, publishRR.Code, publishRR.Body.String())
	var publishStatus pullapi.ActionStatusBody
	require.NoError(json.NewDecoder(publishRR.Body).Decode(&publishStatus))
	require.Equal("published", publishStatus.Status)
	require.Len(provider.PublishedReviews, 1)
	draft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	require.Nil(draft)

	detailPath := "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7"
	select {
	case <-reviewThreadRefreshStarted:
	case <-time.After(10 * time.Second):
		require.Fail("background refresh did not reach the provider")
	}
	require.Eventually(func() bool {
		detailRR := testutil.DoJSON(t, srv, http.MethodGet, detailPath, nil)
		if detailRR.Code != http.StatusOK {
			return false
		}
		var detail pullapi.MergeRequestDetailResponse
		if err := json.NewDecoder(detailRR.Body).Decode(&detail); err != nil {
			return false
		}
		return len(detail.Events) == 1 &&
			detail.Events[0].EventType == "review_comment" &&
			detail.Events[0].Body == "hidden provider comment" &&
			detail.Events[0].MetadataJSON == hiddenMetadata
	}, 10*time.Second, 10*time.Millisecond)

	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)
	assert.Equal("thread-atomic", threads[0].ProviderThreadID)
	assert.JSONEq(hiddenMetadata, threads[0].MetadataJSON)
	events, err := database.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("review_comment", events[0].EventType)
	assert.JSONEq(hiddenMetadata, events[0].MetadataJSON)
	assert.GreaterOrEqual(reviewThreadCalls.Load(), int32(2))
	assert.Len(provider.PublishedReviews, 1)
}

func TestAPIResolveReviewThreadPersistsProviderState(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReviewThreadResolution: true,
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	line := 42
	require.NoError(database.UpsertMRReviewThreads(ctx, mr.ID, []db.MRReviewThread{{
		ProviderThreadID: "thread-7",
		Body:             "Inline comment",
		AuthorLogin:      "ada",
		Range: db.ReviewLineRange{
			Path:        "internal/server/api_test.go",
			Side:        "right",
			Line:        42,
			NewLine:     &line,
			LineType:    "add",
			DiffHeadSHA: "current-head",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}))
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)
	threadID := strconv.FormatInt(threads[0].ID, 10)

	resolveRR := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-threads/"+threadID+"/resolve",
		nil)

	require.Equal(http.StatusOK, resolveRR.Code, resolveRR.Body.String())
	assert.Equal([]string{"thread-7"}, provider.ResolvedThreads)
	thread, err := database.GetMRReviewThread(ctx, mr.ID, threads[0].ID)
	require.NoError(err)
	require.NotNil(thread)
	assert.True(thread.Resolved)

	unresolveRR := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-threads/"+threadID+"/unresolve",
		nil)

	require.Equal(http.StatusOK, unresolveRR.Code, unresolveRR.Body.String())
	assert.Equal([]string{"thread-7"}, provider.UnresolvedThreads)
	thread, err = database.GetMRReviewThread(ctx, mr.ID, threads[0].ID)
	require.NoError(err)
	require.NotNil(thread)
	assert.False(thread.Resolved)
}

func TestAPIResolveReviewThreadReturnsServerErrorForCorruptStoredThread(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReviewThreadResolution: true,
		ReviewDraftMutation:    true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionComment},
		NativeMultilineRanges:  true,
		ReadReviewThreads:      true,
	}
	srv, database, _, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	now := time.Now().UTC().Truncate(time.Second)
	line := 42
	require.NoError(database.UpsertMRReviewThreads(ctx, mr.ID, []db.MRReviewThread{{
		ProviderThreadID:  "thread-corrupt",
		ProviderCommentID: "comment-corrupt",
		Body:              "corrupt thread",
		Range: db.ReviewLineRange{
			Path:        "internal/server/api_test.go",
			Side:        "right",
			Line:        42,
			NewLine:     &line,
			LineType:    "add",
			DiffHeadSHA: "head",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}}))
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)
	_, err = database.WriteDB().ExecContext(
		ctx,
		`UPDATE forge_mr_review_threads SET line = 'not-an-int' WHERE id = ?`,
		threads[0].ID,
	)
	require.NoError(err)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-threads/"+
			strconv.FormatInt(threads[0].ID, 10)+"/resolve",
		nil)

	require.Equal(http.StatusInternalServerError, rr.Code, rr.Body.String())
}

func TestAPIPullDetailAttachesReviewThreadMetadata(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := setupGitLabCapabilityServer(t)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	line := 12
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID:     mr.ID,
		PlatformExternalID: "thread-1",
		EventType:          "review_comment",
		Author:             "ada",
		Body:               "Inline comment",
		CreatedAt:          time.Now().UTC(),
		DedupeKey:          "review-comment-thread-1",
	}}))
	require.NoError(database.UpsertMRReviewThreads(ctx, mr.ID, []db.MRReviewThread{{
		ProviderThreadID: "thread-1",
		Body:             "Inline comment",
		AuthorLogin:      "ada",
		Range: db.ReviewLineRange{
			Path:        "src/lib.rs",
			Side:        "right",
			Line:        12,
			NewLine:     &line,
			LineType:    "add",
			DiffHeadSHA: "abc123",
		},
		Resolved:  false,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}))

	rawDetail := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7",
		nil)

	require.Equal(http.StatusOK, rawDetail.Code, rawDetail.Body.String())
	var detail map[string]any
	require.NoError(json.NewDecoder(rawDetail.Body).Decode(&detail))
	events := detail["events"].([]any)
	require.NotEmpty(events)
	diffThread := events[0].(map[string]any)["diff_thread"].(map[string]any)
	assert.Equal("src/lib.rs", diffThread["path"])
	assert.Equal("right", diffThread["side"])
	assert.InDelta(12, diffThread["line"], 0)
	assert.Equal(false, diffThread["resolved"])
	assert.NotContains(diffThread, "provider_thread_id")
}

func seedApplySuggestionReviewThread(t *testing.T, database *db.DB, mrID int64) db.MRReviewThread {
	t.Helper()
	require := require.New(t)
	line := 11
	require.NoError(database.UpsertMRReviewThreads(t.Context(), mrID, []db.MRReviewThread{{
		ProviderThreadID:  "provider-thread-1",
		ProviderCommentID: "provider-comment-1",
		Body:              "Inline suggestion\n\n```suggestion\nreturn client.publishThreads();\n```",
		AuthorLogin:       "ada",
		Range: db.ReviewLineRange{
			Path:        "src/review.ts",
			Side:        "right",
			Line:        line,
			NewLine:     &line,
			LineType:    "context",
			DiffHeadSHA: "abc123",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}))
	threads, err := database.ListMRReviewThreads(t.Context(), mrID)
	require.NoError(err)
	require.Len(threads, 1)
	return threads[0]
}

func TestAPIApplyReviewSuggestionPassesStoredThreadRangeToProvider(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:            true,
		ReadMergeRequests:           true,
		ReadIssues:                  true,
		ReadComments:                true,
		ReviewSuggestionApplication: true,
		MutationHeadBinding:         true,
		ReadReviewThreads:           true,
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	startLine := 10
	line := 11
	require.NoError(database.UpsertMRReviewThreads(ctx, mr.ID, []db.MRReviewThread{{
		ProviderThreadID:  "provider-thread-1",
		ProviderCommentID: "provider-comment-1",
		Body:              "Inline suggestion\n\n```suggestion\nreturn client.publishThreads();\n```",
		AuthorLogin:       "ada",
		Range: db.ReviewLineRange{
			Path:        "src/review.ts",
			Side:        "right",
			StartSide:   "right",
			StartLine:   &startLine,
			Line:        line,
			NewLine:     &line,
			LineType:    "context",
			DiffHeadSHA: "abc123",
			CommitSHA:   "abc123",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}))
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(threads[0].ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var response pullapi.ApplyReviewSuggestionResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&response))
	assert.Equal("applied", response.Status)
	assert.Equal("suggestion-commit-sha", response.CommitSHA)
	require.Len(provider.AppliedSuggestions, 1)
	applied := provider.AppliedSuggestions[0]
	require.Len(applied.Suggestions, 1)
	assert.Equal("feature/gitlab", applied.HeadBranch)
	assert.Equal("https://gitlab.example.com/fork/project.git", applied.HeadRepoCloneURL)
	assert.Equal("abc123", applied.ExpectedHeadSHA)
	assert.Equal("provider-thread-1", applied.Suggestions[0].ProviderThreadID)
	assert.Equal("provider-comment-1", applied.Suggestions[0].ProviderCommentID)
	assert.Equal("src/review.ts", applied.Suggestions[0].Range.Path)
	assert.Equal("return client.publishThreads();", applied.Suggestions[0].Replacement)
}

func TestAPIApplyReviewSuggestionRejectsNonOpenPullRequest(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:            true,
		ReadMergeRequests:           true,
		ReadIssues:                  true,
		ReadComments:                true,
		ReviewSuggestionApplication: true,
		MutationHeadBinding:         true,
		ReadReviewThreads:           true,
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	now := time.Now().UTC()
	require.NoError(database.UpdateMRState(ctx, repo.ID, 7, string(db.MergeRequestStateClosed), nil, &now))
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	thread := seedApplySuggestionReviewThread(t, database, mr.ID)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(thread.ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	var problem serverfake.RawProblemDetail
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal(string(httpapi.CodeConflict), problem.Code)
	assert.Equal("pull request is not open", problem.Detail)
	require.NotNil(problem.Details)
	assert.Equal("not_open", problem.Details["reason"])
	assert.Empty(provider.AppliedSuggestions)
}

func TestAPIApplyReviewSuggestionReturnsAppliedWithoutCommitMetadata(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:            true,
		ReadMergeRequests:           true,
		ReadIssues:                  true,
		ReadComments:                true,
		ReviewSuggestionApplication: true,
		MutationHeadBinding:         true,
		ReadReviewThreads:           true,
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()
	provider.ApplySuggestionResult = &platform.AppliedReviewSuggestions{}

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	thread := seedApplySuggestionReviewThread(t, database, mr.ID)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(thread.ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var response pullapi.ApplyReviewSuggestionResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&response))
	assert.Equal("applied", response.Status)
	assert.Empty(response.CommitSHA)
	assert.Empty(response.CommitURL)
}

func TestAPIApplyReviewSuggestionReturnsAppliedForNilProviderResult(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:            true,
		ReadMergeRequests:           true,
		ReadIssues:                  true,
		ReadComments:                true,
		ReviewSuggestionApplication: true,
		MutationHeadBinding:         true,
		ReadReviewThreads:           true,
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()
	provider.ApplySuggestionReturnsNil = true

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	thread := seedApplySuggestionReviewThread(t, database, mr.ID)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(thread.ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var response pullapi.ApplyReviewSuggestionResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&response))
	assert.Equal("applied", response.Status)
	assert.Empty(response.CommitSHA)
	assert.Empty(response.CommitURL)
}

func TestAPIApplyReviewSuggestionProviderErrorQueuesDetailSync(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:            true,
		ReadMergeRequests:           true,
		ReadIssues:                  true,
		ReadComments:                true,
		ReviewSuggestionApplication: true,
		MutationHeadBinding:         true,
		ReadReviewThreads:           true,
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()
	provider.ApplySuggestionsErr = errors.New("provider response was lost")

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	thread := seedApplySuggestionReviewThread(t, database, mr.ID)
	ch, _ := srv.Hub().Subscribe(ctx, false)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(thread.ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	changed := readEventMatching(t, ch, func(ev syncevents.Event) bool {
		return ev.Type == "data_changed"
	})
	assert.Equal("data_changed", changed.Type)
}

func TestAPIApplyReviewSuggestionBroadcastsAfterDetailSync(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:            true,
		ReadMergeRequests:           true,
		ReadIssues:                  true,
		ReadComments:                true,
		ReviewSuggestionApplication: true,
		MutationHeadBinding:         true,
		ReadReviewThreads:           true,
	}
	srv, database, _, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	thread := seedApplySuggestionReviewThread(t, database, mr.ID)
	ch, _ := srv.Hub().Subscribe(ctx, false)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(thread.ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	changed := readEventMatching(t, ch, func(ev syncevents.Event) bool {
		return ev.Type == "data_changed"
	})
	require.Equal("data_changed", changed.Type)
}

func TestAPIApplyReviewSuggestionRejectsReplacementOutsideStoredSuggestion(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:            true,
		ReadMergeRequests:           true,
		ReadIssues:                  true,
		ReadComments:                true,
		ReviewSuggestionApplication: true,
		MutationHeadBinding:         true,
		ReadReviewThreads:           true,
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	line := 11
	require.NoError(database.UpsertMRReviewThreads(ctx, mr.ID, []db.MRReviewThread{{
		ProviderThreadID:  "provider-thread-1",
		ProviderCommentID: "provider-comment-1",
		Body:              "Please apply this.\n\n```suggestion\nreturn client.publishThreads();\n```",
		AuthorLogin:       "ada",
		Range: db.ReviewLineRange{
			Path:        "src/review.ts",
			Side:        "right",
			Line:        line,
			NewLine:     &line,
			LineType:    "context",
			DiffHeadSHA: "abc123",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}))
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(threads[0].ID, 10),
				"replacement": "return maliciousRewrite();",
			}},
		})

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Contains(rr.Body.String(), "body.suggestions[0].replacement")
	assert.Empty(provider.AppliedSuggestions)
}

func TestAPIApplyReviewSuggestionRejectsReplacementInsideIndentedExampleFence(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:            true,
		ReadMergeRequests:           true,
		ReadIssues:                  true,
		ReadComments:                true,
		ReviewSuggestionApplication: true,
		MutationHeadBinding:         true,
		ReadReviewThreads:           true,
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	line := 11
	require.NoError(database.UpsertMRReviewThreads(ctx, mr.ID, []db.MRReviewThread{{
		ProviderThreadID:  "provider-thread-1",
		ProviderCommentID: "provider-comment-1",
		Body: strings.Join([]string{
			"Reviewer explained this with an indented markdown example.",
			"",
			"   ````markdown",
			"```suggestion",
			"return client.publishThreads();",
			"```",
			"   ````",
			"",
			"  ```suggestion",
			"return actualSuggestion();",
			"  ```",
		}, "\n"),
		AuthorLogin: "ada",
		Range: db.ReviewLineRange{
			Path:        "src/review.ts",
			Side:        "right",
			Line:        line,
			NewLine:     &line,
			LineType:    "context",
			DiffHeadSHA: "abc123",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}))
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(threads[0].ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Empty(provider.AppliedSuggestions)

	rr = testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(threads[0].ID, 10),
				"replacement": "return actualSuggestion();",
			}},
		})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	require.Len(provider.AppliedSuggestions, 1)
	assert.Equal("return actualSuggestion();", provider.AppliedSuggestions[0].Suggestions[0].Replacement)
}

func TestAPIApplyReviewSuggestionPreProviderValidationFailureDoesNotQueueDetailSync(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:            true,
		ReadMergeRequests:           true,
		ReadIssues:                  true,
		ReadComments:                true,
		ReviewSuggestionApplication: true,
		MutationHeadBinding:         true,
		ReadReviewThreads:           true,
	}
	srv, database, provider, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	thread := seedApplySuggestionReviewThread(t, database, mr.ID)
	provider.BlockNextMRFetch.Store(true)
	provider.MrFetchStarted = make(chan struct{}, 1)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "stale-head",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(thread.ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	require.Empty(provider.AppliedSuggestions)
	select {
	case <-provider.MrFetchStarted:
		require.Fail("pre-provider validation failure queued detail sync")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAPIApplyReviewSuggestionRejectsStaleHead(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:            true,
		ReadMergeRequests:           true,
		ReadIssues:                  true,
		ReadComments:                true,
		ReviewSuggestionApplication: true,
		MutationHeadBinding:         true,
		ReadReviewThreads:           true,
	}
	srv, database, _, _ := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	line := 11
	require.NoError(database.UpsertMRReviewThreads(ctx, mr.ID, []db.MRReviewThread{{
		ProviderThreadID: "provider-thread-1",
		Range: db.ReviewLineRange{
			Path:        "src/review.ts",
			Side:        "right",
			Line:        line,
			NewLine:     &line,
			LineType:    "context",
			DiffHeadSHA: "abc123",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}))
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "stale-head",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(threads[0].ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
}

func draftIDFromResponse(t *testing.T, draft map[string]any) int64 {
	t.Helper()
	draftID, ok := draft["draft_id"].(string)
	require.True(t, ok)
	id, err := strconv.ParseInt(draftID, 10, 64)
	require.NoError(t, err)
	return id
}

func setupGitLabCapabilityServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	return setupGitLabCapabilityServerWithCaps(t, nil)
}

func setupGitLabCapabilityServerWithCaps(
	t *testing.T,
	caps *platform.Capabilities,
) (*Server, *db.DB) {
	t.Helper()
	srv, database, _, _ := setupGitLabCapabilityServerWithProvider(t, caps)
	return srv, database
}

func setupGitLabCapabilityServerWithProvider(
	t *testing.T,
	caps *platform.Capabilities,
) (*Server, *db.DB, *serverfake.ApiTestGitLabProvider, *ghclient.Syncer) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	provider := &serverfake.ApiTestGitLabProvider{
		Ref:               ref,
		CapabilitiesValue: caps,
		MergeRequests: []platform.MergeRequest{{
			Repo:               ref,
			PlatformID:         7001,
			PlatformExternalID: "gid://gitlab/MergeRequest/7001",
			Number:             7,
			URL:                "https://gitlab.example.com/group/project/-/merge_requests/7",
			Title:              "GitLab provider MR",
			Author:             "ada",
			State:              "open",
			IsDraft:            true,
			HeadBranch:         "feature/gitlab",
			HeadRepoCloneURL:   "https://gitlab.example.com/fork/project.git",
			BaseBranch:         "main",
			HeadSHA:            "abc123",
			BaseSHA:            "def456",
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}},
		Issues: []platform.Issue{{
			Repo:               ref,
			PlatformID:         8001,
			PlatformExternalID: "gid://gitlab/Issue/8001",
			Number:             11,
			URL:                "https://gitlab.example.com/group/project/-/issues/11",
			Title:              "GitLab provider issue",
			Author:             "grace",
			State:              "open",
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	repo := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		Owner:              "group",
		Name:               "project",
		PlatformHost:       "gitlab.example.com",
		RepoPath:           "group/project",
		PlatformRepoID:     4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", &config.Config{Tmux: config.Tmux{
		Command: []string{filepath.Join(t.TempDir(), "missing-tmux")},
	}}, ServerOptions{
		WorktreeDir:                        t.TempDir(),
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	return srv, database, provider, syncer
}

func assertUnsupportedCapabilityProblem(
	t *testing.T,
	body io.Reader,
	provider, host, capability string,
) {
	t.Helper()
	require := require.New(t)
	assert := assert.New(t)

	var problem struct {
		Title   string         `json:"title"`
		Status  int            `json:"status"`
		Detail  string         `json:"detail"`
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	require.NoError(json.NewDecoder(body).Decode(&problem))
	assert.Equal(http.StatusText(http.StatusConflict), problem.Title)
	assert.Equal(http.StatusConflict, problem.Status)
	assert.Contains(problem.Detail, "Unsupported provider capability")
	assert.Equal("unsupportedCapability", problem.Code,
		"top-level RFC 9457 code must be the camelCase wire literal")
	require.NotNil(problem.Details, "details must be present on unsupportedCapability problem")
	assert.Equal(capability, problem.Details["capability"])
	assert.Equal(provider, problem.Details["provider"])
	assert.Equal(host, problem.Details["platformHost"])
}

// Should contain an empty array, not null.

func TestAPIGetPullDetailIncludesAssociatedWorkspace(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	client, database, _, _ := setupTestServerWithWorkspaces(t)

	associatedPR := 1
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:                 "associated-workspace",
		Platform:           "github",
		PlatformHost:       "github.com",
		RepoOwner:          "acme",
		RepoName:           "widget",
		ItemType:           db.WorkspaceItemTypeAdHoc,
		ItemKey:            db.AdHocWorkspaceItemKey("feature-link"),
		AssociatedPRNumber: &associatedPR,
		GitHeadRef:         "feature-link",
		WorkspaceBranch:    "feature-link",
		WorktreePath:       filepath.Join(t.TempDir(), "associated-workspace"),
		TmuxSession:        "associated-workspace",
		Status:             "ready",
	}))

	resp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(associatedPR))}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Workspace)
	assert.Equal("associated-workspace", resp.JSON200.Workspace.ID)
	assert.Equal("ready", resp.JSON200.Workspace.Status)
}

func TestAPIActivityFencesRepositoryReconciliationAcrossEventAndWorkspaceReads(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repo.ID, PlatformID: 701, Number: 701,
		URL: "https://github.com/acme/widget/pull/701", Title: "Fenced activity",
		Author: "alice", State: db.MergeRequestStateOpen,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "ws-activity-fence", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypePullRequest,
		ItemNumber: 701, WorktreePath: t.TempDir(), Status: "ready",
	}))

	afterItems := make(chan struct{})
	continueRequest := make(chan struct{})
	srv.activityAfterItemsForTest = func() {
		close(afterItems)
		<-continueRequest
	}
	responseDone := make(chan *generated.ListActivityResp, 1)
	errorDone := make(chan error, 1)
	go func() {
		response, requestErr := client.HTTP.ListActivityWithResponse(context.Background(), &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{}})
		responseDone <- response
		errorDone <- requestErr
	}()
	<-afterItems

	writeAttempted := make(chan struct{})
	restoreHook := database.SetBeforeRepositoryReconciliationWriteLockForTest(func() {
		close(writeAttempted)
	})
	t.Cleanup(restoreHook)
	renameDone := make(chan error, 1)
	go func() {
		renamed := db.GitHubRepoIdentity("github.com", "acme", "gadget")
		renamed.PlatformRepoID = repo.PlatformRepoID
		_, _, renameErr := database.ReconcileRepositoryObservation(
			context.Background(), renamed, now.Add(time.Minute),
		)
		renameDone <- renameErr
	}()
	<-writeAttempted

	var renameErr error
	renameCompletedEarly := false
	select {
	case renameErr = <-renameDone:
		renameCompletedEarly = true
		assert.Fail("repository reconciliation completed during activity snapshot")
	case <-time.After(100 * time.Millisecond):
	}
	close(continueRequest)
	response := <-responseDone
	require.NoError(<-errorDone)
	if !renameCompletedEarly {
		renameErr = <-renameDone
	}
	require.NoError(renameErr)
	require.NotNil(response)
	require.Equal(http.StatusOK, response.StatusCode)
	require.NotNil(response.JSON200)
	require.NotNil(response.JSON200.Items)

	var item generated.ActivityItemResponse
	for i := range response.JSON200.Items {
		candidate := response.JSON200.Items[i]
		if candidate.ItemNumber == 701 {
			item = candidate
			break
		}
	}
	require.NotEmpty(item.ActivityType)
	assert.Equal("widget", item.RepoName)
	require.NotNil(item.Workspace)
	assert.Equal("ws-activity-fence", item.Workspace.ID)
}

// The comment row carries the PR author, not the commenter, so the
// threaded feed can attribute the item to its real author.

func setupTestServerWithClonesAndServer(t *testing.T) (
	client *apiclient.Client,
	database *db.DB,
	mergeBase string,
	headSHA string,
	commitSHAs []string,
	srv *Server,
) {
	t.Helper()
	acquireRootWorkspaceGitSlot(t)

	dir := t.TempDir()
	database = dbtest.Open(t)

	bareDir := filepath.Join(dir, "clones")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	clones := gitclone.New(bareDir, nil)
	bare, err := clones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(t.Context(), "repo-acme-widget"),
		"github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)

	tmpWork := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", bare)
	gitfixture.Run(t, dir, "clone", bare, tmpWork)
	gitfixture.Run(t, tmpWork, "config", "user.email", "test@test.com")
	gitfixture.Run(t, tmpWork, "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(filepath.Join(tmpWork, "base.txt"), []byte("base\n"), 0o644))
	gitfixture.Run(t, tmpWork, "add", ".")
	gitfixture.Run(t, tmpWork, "commit", "-m", "base commit")
	gitfixture.Run(t, tmpWork, "push", "origin", "main")
	mergeBase = gitfixture.SHA(t, tmpWork, "HEAD")

	gitfixture.Run(t, tmpWork, "checkout", "-b", "pr")
	for i := 1; i <= 5; i++ {
		fname := fmt.Sprintf("file%d.txt", i)
		require.NoError(t, os.WriteFile(filepath.Join(tmpWork, fname), fmt.Appendf(nil, "content %d\n", i), 0o644))
		gitfixture.Run(t, tmpWork, "add", ".")
		gitfixture.Run(t, tmpWork, "commit", "-m", fmt.Sprintf("commit %d", i))
	}
	gitfixture.Run(t, tmpWork, "push", "origin", "pr")
	headSHA = gitfixture.SHA(t, tmpWork, "HEAD")

	// Collect SHAs newest-first.
	commitSHAs = make([]string, 5)
	sha := headSHA
	for i := range 5 {
		commitSHAs[i] = sha
		sha = gitfixture.SHA(t, tmpWork, sha+"^1")
	}

	mock := &serverfake.MockGH{}
	repos := []ghclient.RepoRef{{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"}}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, repos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv = New(database, syncer, nil, "/", nil, ServerOptions{Clones: clones})

	serverfake.SeedPR(t, database, "acme", "widget", 1)
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	require.NoError(t, database.UpdateDiffSHAs(ctx, repoID, 1, headSHA, mergeBase, mergeBase))

	client = setupTestClient(t, srv)
	return client, database, mergeBase, headSHA, commitSHAs, srv
}

func TestAPIGetRepoCommitDiffRejectsOptionLikeSHA(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	_, _, _, _, _, srv := setupTestServerWithClonesAndServer(t)
	clonePath, err := srv.clones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(t.Context(), "repo-acme-widget"),
		"github", "github.com", "acme", "widget",
	)
	require.NoError(err)
	configPath := filepath.Join(clonePath, "config")
	before, err := os.ReadFile(configPath)
	require.NoError(err)

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/repo/gh/acme/widget/commits/--output=config/diff",
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusBadRequest, rr.Code)
	after, err := os.ReadFile(configPath)
	require.NoError(err)
	require.Equal(before, after)
}

func TestWorkspaceActivitySearchKeepsSubjectsWithMatchingProviderEvents(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	since := now.Add(-time.Hour)
	repoID := int64(7)
	matchedKey := db.WorkspaceSubjectKey{
		RepoID: repoID, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 41,
	}
	eventlessKey := db.WorkspaceSubjectKey{
		RepoID: repoID, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
	}
	subject := func(key db.WorkspaceSubjectKey, title string) workspaceapi.SubjectActivity {
		return workspaceapi.SubjectActivity{
			Subject: db.WorkspaceSubjectMetadata{
				Key: key, Platform: "github", PlatformHost: "github.com",
				RepoOwner: "acme", RepoName: "widget", RepoPath: "acme/widget",
				Title: title, Author: "alice", State: "open",
			},
			Workspace:  workspaceapi.WorkspaceRef{ID: "ws-" + strconv.Itoa(key.ItemNumber), Status: "ready"},
			ActivityAt: &now,
		}
	}
	snapshot := workspaceapi.WorkspaceSubjectSnapshot{
		OwnReferences: map[db.WorkspaceSubjectKey]workspaceapi.WorkspaceRef{},
		Subjects: map[db.WorkspaceSubjectKey]workspaceapi.SubjectActivity{
			matchedKey:   subject(matchedKey, "Unrelated title"),
			eventlessKey: subject(eventlessKey, "Another unrelated title"),
		},
	}
	srv := wiredServer(&Server{
		repoResolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{}),
		cfg: &config.Config{Activity: config.Activity{
			UseWorkspaceActivityForRecency: true,
		}},
	})

	got := srv.itemapi.WorkspaceActivityResponse(
		&itemapi.ListActivityInput{},
		db.ListActivityOpts{Search: "reviewer", Since: &since},
		snapshot,
		[]db.ActivityItem{{
			RepoID: repoID, ItemType: "pr", ItemNumber: matchedKey.ItemNumber,
			Author: "reviewer", BodyPreview: "matches the search",
		}},
		true,
	)

	require.Len(got, 1)
	assert.Equal(matchedKey.ItemNumber, got[0].ItemNumber)
	require.NotNil(got[0].Workspace)
	assert.Equal("ws-41", got[0].Workspace.ID)
}

func TestWorkspaceActivityAuthorMatchesTheSubjectInsteadOfProviderEventActors(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	since := now.Add(-time.Hour)
	repoID := int64(7)
	matchedKey := db.WorkspaceSubjectKey{
		RepoID: repoID, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 41,
	}
	eventlessKey := db.WorkspaceSubjectKey{
		RepoID: repoID, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42,
	}
	subject := func(key db.WorkspaceSubjectKey) workspaceapi.SubjectActivity {
		return workspaceapi.SubjectActivity{
			Subject: db.WorkspaceSubjectMetadata{
				Key: key, Platform: "github", PlatformHost: "github.com",
				RepoOwner: "acme", RepoName: "widget", RepoPath: "acme/widget",
				Title: "Workspace work", Author: "alice", State: "open",
			},
			Workspace:  workspaceapi.WorkspaceRef{ID: "ws-" + strconv.Itoa(key.ItemNumber), Status: "ready"},
			ActivityAt: &now,
		}
	}
	snapshot := workspaceapi.WorkspaceSubjectSnapshot{
		OwnReferences: map[db.WorkspaceSubjectKey]workspaceapi.WorkspaceRef{},
		Subjects: map[db.WorkspaceSubjectKey]workspaceapi.SubjectActivity{
			matchedKey:   subject(matchedKey),
			eventlessKey: subject(eventlessKey),
		},
	}
	srv := wiredServer(&Server{
		repoResolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{}),
		cfg: &config.Config{Activity: config.Activity{
			UseWorkspaceActivityForRecency: true,
		}},
	})

	byAuthor := srv.itemapi.WorkspaceActivityResponse(
		&itemapi.ListActivityInput{},
		db.ListActivityOpts{Author: "ALICE", Since: &since},
		snapshot,
		[]db.ActivityItem{{
			RepoID: repoID, ItemType: "pr", ItemNumber: matchedKey.ItemNumber,
			Author: "reviewer", ItemAuthor: "alice",
		}},
		true,
	)

	require.Len(byAuthor, 2)
	assert.ElementsMatch([]int{matchedKey.ItemNumber, eventlessKey.ItemNumber}, []int{
		byAuthor[0].ItemNumber,
		byAuthor[1].ItemNumber,
	})

	byCommenter := srv.itemapi.WorkspaceActivityResponse(
		&itemapi.ListActivityInput{},
		db.ListActivityOpts{Author: "reviewer", Since: &since},
		snapshot,
		[]db.ActivityItem{{
			RepoID: repoID, ItemType: "pr", ItemNumber: matchedKey.ItemNumber,
			Author: "reviewer", ItemAuthor: "alice",
		}},
		true,
	)
	require.Empty(byCommenter)
}

func TestWorkspaceActivityProjectionUsesHubPolicy(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	key := db.WorkspaceSubjectKey{
		RepoID: 7, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 41,
	}
	snapshot := workspaceapi.WorkspaceSubjectSnapshot{
		Subjects: map[db.WorkspaceSubjectKey]workspaceapi.SubjectActivity{
			key: {
				Subject: db.WorkspaceSubjectMetadata{
					Key: key, Platform: "github", PlatformHost: "github.com",
					RepoOwner: "acme", RepoName: "widget", RepoPath: "acme/widget",
					Title: "Workspace-only work", Author: "workspace owner", State: "open",
				},
				Workspace:  workspaceapi.WorkspaceRef{ID: "ws-41", Status: "ready"},
				ActivityAt: &now,
			},
		},
	}
	srv := wiredServer(&Server{
		repoResolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{}),
		cfg: &config.Config{Activity: config.Activity{
			UseWorkspaceActivityForRecency: true,
		}},
	})

	require.Empty(srv.itemapi.WorkspaceActivityResponse(
		&itemapi.ListActivityInput{}, db.ListActivityOpts{}, snapshot, nil, false,
	))
	require.Equal([]string{"provider author"}, srv.activityapi.ActivityAuthorsWithWorkspace(
		[]string{"provider author"}, snapshot, db.ListActivityAuthorsOpts{}, false,
	))

	require.Len(srv.itemapi.WorkspaceActivityResponse(
		&itemapi.ListActivityInput{}, db.ListActivityOpts{}, snapshot, nil, true,
	), 1)
	require.ElementsMatch([]string{"provider author", "workspace owner"}, srv.activityapi.ActivityAuthorsWithWorkspace(
		[]string{"provider author"}, snapshot, db.ListActivityAuthorsOpts{}, true,
	))
}

func TestAPIActivityScopesFollowTrackedRepositoryIDAcrossRename(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database, syncer := setupTestServer(t)
	srv.cfg = &config.Config{}
	ctx := t.Context()
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	repo, err := database.GetRepoByIdentity(
		ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	tracked := syncer.TrackedRepos()
	require.Len(tracked, 1)
	tracked[0].RepoID = repo.ID
	syncer.SetRepos(tracked)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repo.ID, PlatformID: 702, Number: 702,
		URL: "https://github.com/acme/gadget/pull/702", Title: "Renamed activity",
		Author: "Rename Actor", State: db.MergeRequestStateOpen,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	renamed := db.GitHubRepoIdentity("github.com", "acme", "gadget")
	renamed.PlatformRepoID = repo.PlatformRepoID
	_, applied, err := database.ReconcileRepositoryObservation(
		ctx, renamed, time.Now().UTC().Add(time.Minute),
	)
	require.NoError(err)
	require.True(applied)

	since := now.Add(-time.Minute).Format(time.RFC3339)
	feed := testutil.DoJSON(
		t, srv, http.MethodGet,
		"/api/v1/activity?since="+url.QueryEscape(since), nil)

	require.Equal(http.StatusOK, feed.Code)
	var feedBody itemapi.ActivityResponse
	require.NoError(json.Unmarshal(feed.Body.Bytes(), &feedBody))
	require.Len(feedBody.Items, 1)
	assert.Equal("gadget", feedBody.Items[0].RepoName)
	assert.Equal(repo.PlatformRepoID, feedBody.Items[0].Repo.PlatformRepoID)
	require.Len(feedBody.ItemActivity, 1)
	assert.Equal(repo.PlatformRepoID, feedBody.ItemActivity[0].Repo.PlatformRepoID)

	candidates := testutil.DoJSON(
		t, srv, http.MethodGet,
		"/api/v1/activity/authors?since="+url.QueryEscape(since), nil)

	require.Equal(http.StatusOK, candidates.Code)
	var candidateBody struct {
		Authors []string `json:"authors"`
	}
	require.NoError(json.Unmarshal(candidates.Body.Bytes(), &candidateBody))
	assert.Equal([]string{"Rename Actor"}, candidateBody.Authors)
}

func TestAPIListActivityIncludesNotificationSyncedBeforeRepo(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupNotificationsEnabledTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	number := 7

	require.NoError(database.UpsertNotifications(ctx, []db.Notification{{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: "thread-before-repo",
		RepoOwner:              "acme",
		RepoName:               "widget",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Review before repo sync",
		WebURL:                 "https://github.com/acme/widget/pull/7",
		ItemNumber:             &number,
		ItemType:               "pr",
		ItemAuthor:             "reviewer",
		Reason:                 "mention",
		Unread:                 true,
		SourceUpdatedAt:        base.Add(10 * time.Minute),
		SyncedAt:               base.Add(10 * time.Minute),
	}}))
	mergedAt := base.Add(20 * time.Minute)
	serverfake.SeedPR(t, database, "acme", "widget", number,
		serverfake.WithSeedPRTitle("Review before repo sync"),
		serverfake.WithSeedPRLifecycle(db.MergeRequestStateMerged, &mergedAt, nil),
		serverfake.WithSeedPRTimes(base, mergedAt, mergedAt))

	types := []string{"notification"}
	since := base.Add(-time.Minute).Format(time.RFC3339)
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Types: types}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.Len(resp.JSON200.Items, 1)
	item := resp.JSON200.Items[0]
	assert.Equal("notification", item.ActivityType)
	assert.Equal("acme", item.RepoOwner)
	assert.Equal("widget", item.RepoName)
	assert.NotNil(item.SubjectState)
	assert.Equal("merged", *item.SubjectState)
}

func TestAPIListActivityScopesNotificationsToTrackedRepos(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupNotificationsEnabledTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	number := 7

	trackedRepoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	removedRepoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "removed"))
	require.NoError(err)
	insertTestActivityPR(t, database, trackedRepoID, "acme", "widget", number, "Tracked notification", base)
	insertTestActivityPR(t, database, removedRepoID, "acme", "removed", number, "Removed notification", base)

	require.NoError(database.UpsertNotifications(ctx, []db.Notification{
		{
			Platform:               "github",
			PlatformHost:           "github.com",
			PlatformNotificationID: "thread-tracked",
			RepoOwner:              "acme",
			RepoName:               "widget",
			SubjectType:            "PullRequest",
			SubjectTitle:           "Tracked notification",
			WebURL:                 "https://github.com/acme/widget/pull/7",
			ItemNumber:             &number,
			ItemType:               "pr",
			ItemAuthor:             "reviewer",
			Reason:                 "mention",
			Unread:                 true,
			SourceUpdatedAt:        base.Add(10 * time.Minute),
			SyncedAt:               base.Add(10 * time.Minute),
		},
		{
			Platform:               "github",
			PlatformHost:           "github.com",
			PlatformNotificationID: "thread-removed",
			RepoOwner:              "acme",
			RepoName:               "removed",
			SubjectType:            "PullRequest",
			SubjectTitle:           "Removed notification",
			WebURL:                 "https://github.com/acme/removed/pull/7",
			ItemNumber:             &number,
			ItemType:               "pr",
			ItemAuthor:             "reviewer",
			Reason:                 "mention",
			Unread:                 true,
			SourceUpdatedAt:        base.Add(11 * time.Minute),
			SyncedAt:               base.Add(11 * time.Minute),
		},
	}))

	types := []string{"notification"}
	since := base.Add(-time.Minute).Format(time.RFC3339)
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Types: types}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.Len(resp.JSON200.Items, 1)
	item := resp.JSON200.Items[0]
	assert.Equal("notification", item.ActivityType)
	assert.Equal("acme", item.RepoOwner)
	assert.Equal("widget", item.RepoName)
	assert.Equal("Tracked notification", item.ItemTitle)
}

// --- Stacks E2E ---

// setupTestServerWithWorkspaces creates a test server wired with
// both a gitclone.Manager and a workspace.Manager backed by a
// bare repo that has a "pr" branch. It seeds a PR in the DB
// and returns the API client and database.
func setupTestServerWithWorkspaces(
	t *testing.T,
) (*apiclient.Client, *db.DB, string, string) {
	t.Helper()
	fixture := setupWorkspaceServerFixture(t, nil)
	return fixture.client, fixture.database, fixture.bare, fixture.remote
}

func setupTestServerWithWorkspacesServer(
	t *testing.T,
	cfg *config.Config,
) (*apiclient.Client, *db.DB, string, string, *Server) {
	t.Helper()
	fixture := setupWorkspaceServerFixture(t, cfg)
	return fixture.client, fixture.database, fixture.bare, fixture.remote, fixture.server
}

func setupTestServerWithWorkspacesServerAndEnrichment(
	t *testing.T,
	cfg *config.Config,
) (*apiclient.Client, *db.DB, string, string, *Server) {
	t.Helper()
	fixture := setupWorkspaceServerFixtureWithOptions(
		t, cfg, ServerOptions{PtyOwnerInProcess: true}, true,
	)
	return fixture.client, fixture.database, fixture.bare, fixture.remote, fixture.server
}

type workspaceServerFixture struct {
	server    *Server
	syncer    *ghclient.Syncer
	client    *apiclient.Client
	database  *db.DB
	clones    *gitclone.Manager
	worktrees string
	bare      string
	remote    string
}

func setupWorkspaceServerFixture(
	t *testing.T,
	cfg *config.Config,
) workspaceServerFixture {
	return setupWorkspaceServerFixtureWithOptions(
		t, cfg, ServerOptions{PtyOwnerInProcess: true},
	)
}

func setupWorkspaceServerFixtureWithOptions(
	t *testing.T,
	cfg *config.Config,
	options ServerOptions,
	enableEnrichment ...bool,
) workspaceServerFixture {
	t.Helper()
	return setupWorkspaceServerFixtureWithHostAndOptions(
		t, cfg, "github.com", options, enableEnrichment...,
	)
}

func setupWorkspaceServerFixtureWithHost(
	t *testing.T,
	cfg *config.Config,
	platformHost string,
) workspaceServerFixture {
	t.Helper()
	return setupWorkspaceServerFixtureWithHostAndOptions(
		t, cfg, platformHost, ServerOptions{PtyOwnerInProcess: true},
	)
}

func setupWorkspaceServerFixtureWithHostAndOptions(
	t *testing.T,
	cfg *config.Config,
	platformHost string,
	options ServerOptions,
	enableEnrichment ...bool,
) workspaceServerFixture {
	return setupWorkspaceServerFixtureWithMockHostAndOptions(
		t, cfg, &serverfake.MockGH{}, platformHost, options, enableEnrichment...,
	)
}

func setupWorkspaceServerFixtureWithMockHostAndOptions(
	t *testing.T,
	cfg *config.Config,
	mock *serverfake.MockGH,
	platformHost string,
	options ServerOptions,
	enableEnrichment ...bool,
) workspaceServerFixture {
	t.Helper()

	if testing.Short() {
		t.Skip("workspace e2e tests skipped in short mode")
	}
	if cfg == nil {
		cfg = &config.Config{}
	} else {
		clone := *cfg
		cfg = &clone
	}
	if len(cfg.Tmux.Command) == 0 {
		cfg.Tmux.Command = isolatedRealTmuxCommandIfAvailable(t)
	}

	dir := t.TempDir()
	database := dbtest.Open(t)

	remoteDir := filepath.Join(dir, "remote")
	require.NoError(t, os.MkdirAll(remoteDir, 0o755))
	remote := filepath.Join(remoteDir, "widget.git")
	gitfixture.Run(
		t, dir, "init", "--bare", "--initial-branch=main", remote,
	)

	tmpWork := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "clone", remote, tmpWork)
	gitfixture.Run(t, tmpWork, "config", "user.email", "test@test.com")
	gitfixture.Run(t, tmpWork, "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(
		filepath.Join(tmpWork, "base.txt"),
		[]byte("base\n"), 0o644,
	))
	gitfixture.Run(t, tmpWork, "add", ".")
	gitfixture.Run(t, tmpWork, "commit", "-m", "base commit")
	gitfixture.Run(t, tmpWork, "push", "origin", "main")

	gitfixture.Run(t, tmpWork, "checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpWork, "new.txt"),
		[]byte("new\n"), 0o644,
	))
	gitfixture.Run(t, tmpWork, "add", ".")
	gitfixture.Run(t, tmpWork, "commit", "-m", "feature commit")
	gitfixture.Run(t, tmpWork, "push", "origin", "feature")
	featureSHA := gitOutput(t, tmpWork, "rev-parse", "HEAD")
	gitfixture.Run(t, remote, "update-ref", "refs/pull/1/head", featureSHA)

	bareDir := filepath.Join(dir, "clones")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	clones := gitclone.New(bareDir, nil)
	bare, err := clones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(t.Context(), "repo-acme-widget"),
		"github", platformHost, "acme", "widget",
	)
	require.NoError(t, err)
	gitfixture.Run(t, dir, "clone", "--bare", remote, bare)
	gitfixture.Run(t, bare, "remote", "set-url", "origin", gitLocalRemoteURL(remote))

	worktreeDir := filepath.Join(dir, "worktrees")
	repos := []ghclient.RepoRef{
		{Owner: "acme", Name: "widget", PlatformHost: platformHost},
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	basePath := "/"
	if cfg != nil && cfg.BasePath != "" {
		basePath = cfg.BasePath
	}
	options.Clones = clones
	options.WorktreeDir = worktreeDir
	options.DisableWorkspaceEnrichment = len(enableEnrichment) == 0 || !enableEnrichment[0]
	options.HostCheckAllowLoopbackAnyPort = true
	if !options.HostCheck.Valid() {
		options.HostCheck = authapi.HostCheckOptions{
			Bind:    config.HostKey{Host: "127.0.0.1", Port: "8091"},
			Allowed: []config.HostKey{{Host: "forge.test", Port: ""}},
		}
	}
	srv := New(database, syncer, nil, basePath, cfg, options)
	t.Cleanup(func() {
		cleanupWorkspaceServerFixtureTmuxSessions(t, cfg.Tmux.Command, dir)
	})
	// Cleanup callbacks run LIFO. Drain the server first so async
	// workspace setup cannot create a tmux session after fixture
	// artifact cleanup has listed workspaces. The DB cleanup was
	// registered earlier, so it remains open for artifact cleanup.
	t.Cleanup(func() { cleanupWorkspaceServerFixtureArtifacts(t, srv, database) })
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	serverfake.SeedPROnHost(
		t, database, platformHost, "acme", "widget", 1,
		withSeedPRHeadRepoCloneURL("https://github.com/acme/widget.git"),
	)

	clientBaseURL := "http://forge.test"
	if basePath != "/" {
		clientBaseURL += strings.TrimSuffix(basePath, "/")
	}
	client := setupTestClientWithBaseURL(t, srv, clientBaseURL)
	return workspaceServerFixture{
		server:    srv,
		syncer:    syncer,
		client:    client,
		database:  database,
		clones:    clones,
		worktrees: worktreeDir,
		bare:      bare,
		remote:    remote,
	}
}

func cleanupWorkspaceServerFixtureArtifacts(
	t *testing.T,
	srv *Server,
	database *db.DB,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(
		t,
		cleanupWorkspaceServerFixtureArtifactsWithContext(ctx, srv, database),
	)
}

func cleanupWorkspaceServerFixtureArtifactsWithContext(
	ctx context.Context,
	srv *Server,
	database *db.DB,
) error {
	if srv.workspaces == nil {
		return nil
	}

	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	var errs []error
	for _, ws := range workspaces {
		_, err := func() ([]string, error) {
			beforeDestructive := func(stopCtx context.Context) error {
				if srv.runtime != nil {
					srv.runtime.StopWorkspace(stopCtx, ws.ID)
				}
				return nil
			}
			if srv.runtime != nil {
				srv.runtime.BeginStopping(ws.ID)
				defer srv.runtime.EndStopping(ws.ID)
			}
			return srv.workspaces.Delete(ctx, ws.ID, true, beforeDestructive)
		}()
		if err != nil {
			errs = append(
				errs,
				fmt.Errorf("delete workspace %s: %w", ws.ID, err),
			)
		}
	}
	if err := srv.workspaces.ReapOrphanTmuxSessions(ctx); err != nil {
		errs = append(errs, fmt.Errorf("reap orphan tmux sessions: %w", err))
	}
	return errors.Join(errs...)
}

func cleanupWorkspaceServerFixtureTmuxSessions(
	t *testing.T,
	tmuxCommand []string,
	root string,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, cleanupWorkspaceServerFixtureTmuxSessionsWithContext(
		ctx, tmuxCommand, root,
	))
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

func waitForWorkspaceReady(
	t *testing.T,
	ctx context.Context,
	client *apiclient.Client,
	wsID string,
) *generated.WorkspaceResponse {
	t.Helper()
	return waitForWorkspaceStatus(t, ctx, client, wsID, "ready")
}

func waitForWorkspaceStatus(
	t *testing.T,
	ctx context.Context,
	client *apiclient.Client,
	wsID string,
	status string,
) *generated.WorkspaceResponse {
	t.Helper()

	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		getResp, err := client.HTTP.GetWorkspaceWithResponse(ctx, &generated.GetWorkspaceRequestOptions{PathParams: &generated.GetWorkspacePath{ID: wsID}})
		require.NoError(t, err)
		if getResp.StatusCode == http.StatusOK &&
			getResp.JSON200 != nil &&
			getResp.JSON200.Status == status {
			return getResp.JSON200
		}

		select {
		case <-waitCtx.Done():
			require.NoError(
				t, waitCtx.Err(),
				"workspace %s never reached %q status",
				wsID, status,
			)
		case <-ticker.C:
		}
	}
}

func TestListWorkspacesIncludesItemLastActivityAt(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	client, database, _, _ := setupTestServerWithWorkspaces(t)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)

	base := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	prActivity := base.Add(2 * time.Hour)
	issueActivity := base.Add(3 * time.Hour)

	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repo.ID,
		PlatformID:     9001,
		Number:         701,
		URL:            "https://github.com/acme/widget/pull/701",
		Title:          "Sort PR workspace",
		Author:         "alice",
		State:          "open",
		CreatedAt:      base,
		UpdatedAt:      base.Add(time.Hour),
		LastActivityAt: prActivity,
	})
	require.NoError(err)

	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repo.ID,
		PlatformID:     9002,
		Number:         702,
		URL:            "https://github.com/acme/widget/issues/702",
		Title:          "Sort issue workspace",
		Author:         "bob",
		State:          "open",
		CreatedAt:      base,
		UpdatedAt:      base.Add(time.Hour),
		LastActivityAt: issueActivity,
	})
	require.NoError(err)

	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:           "ws-pr-activity",
		Platform:     "github",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   701,
		GitHeadRef:   "feature/pr-activity",
		WorktreePath: filepath.Join(t.TempDir(), "ws-pr-activity"),
		TmuxSession:  "kenn-forge-ws-pr-activity",
		Status:       "creating",
		CreatedAt:    base,
	}))
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:           "ws-issue-activity",
		Platform:     "github",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypeIssue,
		ItemNumber:   702,
		GitHeadRef:   "feature/issue-activity",
		WorktreePath: filepath.Join(t.TempDir(), "ws-issue-activity"),
		TmuxSession:  "kenn-forge-ws-issue-activity",
		Status:       "creating",
		CreatedAt:    base.Add(time.Minute),
	}))
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:           "ws-unsynced-activity",
		Platform:     "github",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypeIssue,
		ItemNumber:   799,
		GitHeadRef:   "feature/unsynced-activity",
		WorktreePath: filepath.Join(t.TempDir(), "ws-unsynced-activity"),
		TmuxSession:  "kenn-forge-ws-unsynced-activity",
		Status:       "creating",
		CreatedAt:    base.Add(2 * time.Minute),
	}))

	resp, err := client.HTTP.ListWorkspacesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Workspaces)

	byID := map[string]generated.WorkspaceResponse{}
	for _, ws := range resp.JSON200.Workspaces {
		byID[ws.ID] = ws
	}

	require.Contains(byID, "ws-pr-activity")
	require.Contains(byID, "ws-issue-activity")
	require.Contains(byID, "ws-unsynced-activity")

	assert.NotNil(byID["ws-pr-activity"].ItemLastActivityAt)
	assert.Equal(
		prActivity.Format(time.RFC3339),
		*byID["ws-pr-activity"].ItemLastActivityAt,
	)
	assert.NotNil(byID["ws-issue-activity"].ItemLastActivityAt)
	assert.Equal(
		issueActivity.Format(time.RFC3339),
		*byID["ws-issue-activity"].ItemLastActivityAt,
	)
	assert.Nil(byID["ws-unsynced-activity"].ItemLastActivityAt)
}

func TestWorkspaceServerFixtureCleansUpTmuxSessions(t *testing.T) {
	require := require.New(t)
	if testing.Short() {
		t.Skip("workspace e2e tests skipped in short mode")
	}

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		"TMUX_RECORD=" + shellquote.Join(record) + "\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	t.Run("fixture", func(t *testing.T) {
		cfg := &config.Config{
			Tmux: config.Tmux{Command: []string{script}},
		}
		client, _, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)

		createReadyWorkspace(t, context.Background(), client)
	})

	var killed bool
	for _, argv := range serverfake.ReadTmuxRecord(t, record) {
		if len(argv) >= 3 &&
			argv[0] == "kill-session" &&
			argv[1] == "-t" &&
			strings.HasPrefix(argv[2], "forge-") {
			killed = true
			break
		}
	}
	require.True(killed, "fixture cleanup did not kill workspace tmux session")
}

func TestCleanupWorkspaceServerFixtureArtifactsKeepsDeletingAfterError(
	t *testing.T,
) {
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		"TMUX_RECORD=" + shellquote.Join(record) + "\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`if [ "$1" = "kill-session" ] && [ "$3" = "kenn-forge-fails" ]; then` + "\n" +
		`  echo "permission denied" >&2` + "\n" +
		`  exit 1` + "\n" +
		`fi` + "\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	database := dbtest.Open(t)

	manager := workspace.NewManager(database, filepath.Join(dir, "worktrees"))
	manager.SetTmuxCommand([]string{script})
	srv := wiredServer(&Server{workspaces: manager})
	ctx := context.Background()
	require.NoError(database.InsertWorkspace(ctx, &workspace.Workspace{
		ID:              "ws-succeeds",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      1,
		GitHeadRef:      "feature/succeeds",
		WorkspaceBranch: "kenn-forge/pr-1",
		WorktreePath:    filepath.Join(dir, "succeeds"),
		TmuxSession:     "kenn-forge-succeeds",
		Status:          "ready",
	}))
	require.NoError(database.InsertWorkspace(ctx, &workspace.Workspace{
		ID:              "ws-fails",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      2,
		GitHeadRef:      "feature/fails",
		WorkspaceBranch: "kenn-forge/pr-2",
		WorktreePath:    filepath.Join(dir, "fails"),
		TmuxSession:     "kenn-forge-fails",
		Status:          "ready",
	}))
	_, err := database.WriteDB().ExecContext(ctx, `
		UPDATE forge_workspaces
		SET created_at = CASE id
			WHEN 'ws-succeeds' THEN datetime('now')
			WHEN 'ws-fails' THEN datetime('now', '+1 second')
		END
		WHERE id IN ('ws-succeeds', 'ws-fails')`)
	require.NoError(err)

	err = cleanupWorkspaceServerFixtureArtifactsWithContext(ctx, srv, database)
	require.Error(err)
	require.Contains(err.Error(), "ws-fails")
	require.Contains(err.Error(), "permission denied")

	killedSessions := map[string]bool{}
	for _, argv := range serverfake.ReadTmuxRecord(t, record) {
		if len(argv) >= 3 &&
			argv[0] == "kill-session" {
			killedSessions[argv[2]] = true
		}
	}
	require.True(
		killedSessions["kenn-forge-succeeds"],
		"cleanup stopped before later workspace tmux session",
	)
}

func TestWorkspaceRuntimeTargetsRefreshAfterSettingsUpdateE2E(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	cfg := &config.Config{
		SyncInterval:   "5m",
		GitHubTokenEnv: "KENN_FORGE_GITHUB_TOKEN",
		Host:           "127.0.0.1",
		Port:           8091,
		BasePath:       "/",
		DataDir:        dir,
		Activity: config.Activity{
			ViewMode:  "threaded",
			TimeRange: "7d",
		},
	}
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(cfg.Save(cfgPath))

	agentPath := filepath.Join(dir, "codex-custom")
	require.NoError(os.WriteFile(
		agentPath,
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	))
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	srv.cfgPath = cfgPath
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	agents := []config.Agent{{
		Key:     "codex",
		Label:   "Custom Codex",
		Command: []string{agentPath, "--full-auto"},
	}}
	updateResp := testutil.DoJSON(
		t, srv, http.MethodPut, "/api/v1/settings",
		spokeapi.UpdateSettingsRequest{Agents: &agents})

	require.Equal(http.StatusOK, updateResp.Code, updateResp.Body.String())

	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(reloaded.Agents, 1)
	assert.Equal([]string{agentPath, "--full-auto"}, reloaded.Agents[0].Command)

	runtimeResp, err := client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, &generated.GetWorkspaceRuntimeRequestOptions{PathParams: &generated.GetWorkspaceRuntimePath{ID: ws.ID}})
	require.NoError(err)
	require.Equal(http.StatusOK, runtimeResp.StatusCode)
	require.NotNil(runtimeResp.JSON200)
	require.NotNil(runtimeResp.JSON200.LaunchTargets)

	var codex generated.LaunchTarget
	for _, target := range runtimeResp.JSON200.LaunchTargets {
		if target.Key == "codex" {
			codex = target
			break
		}
	}
	assert.Equal("Custom Codex", codex.Label)
	assert.True(codex.Available)
	require.NotNil(codex.Command)
	assert.Equal([]string{agentPath, "--full-auto"}, codex.Command)
}

func TestWorkspaceCreatesPtyOwnerSessionWhenTmuxUnavailableE2E(t *testing.T) {
	runSerialPTYE2E(t)

	require := require.New(t)
	assert := assert.New(t)

	fixture, dir, ptyOwnerDir := setupPtyOwnerWorkspaceFixture(t)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, ws.TmuxSession)

	require.Equal("ready", ws.Status)
	assert.NotEmpty(ws.TmuxSession)

	stored, err := fixture.database.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal(workspace.TerminalBackendPtyOwner, stored.TerminalBackend)

	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)
	workspaceTerminalWriteRead(
		t, ctx, ts.URL, ws.ID, "printf 'owner-one\n'\n", "owner-one",
	)

	snapshot, err := fixture.server.workspaces.TerminalPaneSnapshot(
		ctx, stored, ws.TmuxSession,
	)
	require.NoError(err)
	assert.Contains(snapshot.Output, "owner-one")

	_, err = fixture.database.WriteDB().ExecContext(
		ctx,
		`UPDATE forge_workspaces SET terminal_backend = '' WHERE id = ?`,
		ws.ID,
	)
	require.NoError(err)
	legacyStored, err := fixture.database.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(legacyStored)
	assert.Empty(legacyStored.TerminalBackend)

	ts.Close()
	serverfake.GracefulShutdown(t, fixture.server)

	availableTmux := filepath.Join(dir, "available-tmux")
	require.NoError(os.WriteFile(
		availableTmux,
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	))
	restartedCfg := &config.Config{Tmux: config.Tmux{
		Command: []string{availableTmux},
	}}
	restarted := New(
		fixture.database, fixture.syncer, nil, "/", restartedCfg,
		ServerOptions{
			Clones:      fixture.clones,
			WorktreeDir: fixture.worktrees,
			PtyOwnerDir: ptyOwnerDir,
		},
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, restarted) })
	restartedClient := setupTestClient(t, restarted)
	restartedTS := httptest.NewServer(restarted)
	t.Cleanup(restartedTS.Close)

	workspaceTerminalWriteRead(
		t, ctx, restartedTS.URL, ws.ID, "printf 'owner-two\n'\n", "owner-two",
	)

	force := true
	delResp, err := restartedClient.HTTP.DeleteWorkspaceWithResponse(ctx, &generated.DeleteWorkspaceRequestOptions{PathParams: &generated.DeleteWorkspacePath{ID: ws.ID}, Query: &generated.DeleteWorkspaceQuery{Force: &force}})
	require.NoError(err)
	require.Equal(http.StatusNoContent, delResp.StatusCode)

	deleted, err := fixture.database.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	assert.Nil(deleted)
	_, err = os.Stat(filepath.Join(ptyOwnerDir, ws.TmuxSession))
	assert.True(os.IsNotExist(err))
}

func TestWorkspaceRuntimePtyOwnerPersistenceFailureRollsBackSessionE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses /bin/sh")
	}
	runSerialPTYE2E(t)

	require := require.New(t)
	assert := assert.New(t)
	fixture, _, ptyOwnerDir := setupPtyOwnerWorkspaceFixture(t)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, ws.TmuxSession)

	transaction := occupyRuntimeWriter(t, fixture.database)
	baseline := fixture.database.WriteDB().Stats().WaitCount
	requestCtx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	responses := serveRuntimeRequestAsync(
		fixture.server,
		requestCtx,
		http.MethodPost,
		"/api/v1/workspaces/"+ws.ID+"/runtime/sessions",
		serverfake.MustMarshal(t, map[string]any{
			"target_key": string(localruntime.LaunchTargetPlainShell),
		}),
	)

	var launched localruntime.SessionInfo
	require.Eventually(func() bool {
		sessions := fixture.server.workspaceAPI.RuntimeSnapshot(ws.ID)
		if len(sessions) != 1 || sessions[0].TmuxSession != "" {
			return false
		}
		launched = sessions[0]
		return true
	}, 2*time.Second, 20*time.Millisecond)
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, launched.Key)
	paths, err := ptyowner.NewSessionPaths(ptyOwnerDir, launched.Key)
	require.NoError(err)
	require.FileExists(paths.StatePath)
	waitForRuntimeWriterWait(t, fixture.database, baseline)

	cancel()
	recorder := awaitRuntimeResponse(t, responses)
	require.NoError(transaction.Rollback())

	assert.Equal(http.StatusInternalServerError, recorder.Code)
	assert.Contains(recorder.Body.String(), "record runtime session")
	assert.Contains(recorder.Body.String(), "context canceled")
	assert.NoFileExists(paths.StatePath)
	assert.Empty(fixture.server.workspaceAPI.RuntimeSnapshot(ws.ID))
	runtimeRows, err := fixture.database.ListWorkspaceRuntimeSessions(ctx, ws.ID)
	require.NoError(err)
	assert.Empty(runtimeRows)
}

func TestWorkspaceRuntimePtyOwnerShellReattachesAfterServerRestartE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses /bin/sh")
	}
	runSerialPTYE2E(t)

	require := require.New(t)
	assert := assert.New(t)

	fixture, dir, ptyOwnerDir := setupPtyOwnerWorkspaceFixture(t)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	originalShell := launchPlainShellRuntimeSession(t, ctx, fixture.client, ws.ID)
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, originalShell.Key)
	staleTmuxKey := ws.ID + "_stale-tmux"
	require.NoError(fixture.database.UpsertWorkspaceRuntimeSession(
		ctx,
		&db.WorkspaceRuntimeSession{
			WorkspaceID: ws.ID,
			SessionKey:  staleTmuxKey,
			TargetKey:   "helper",
			Label:       "Helper",
			Kind:        string(localruntime.LaunchTargetAgent),
			Scope:       "session",
			TmuxSession: "kenn-forge-stale-runtime-tmux",
			CreatedAt:   originalShell.CreatedAt.Add(-time.Minute),
		},
	))
	t.Cleanup(func() {
		_ = fixture.database.DeleteWorkspaceRuntimeSession(
			context.Background(), ws.ID, staleTmuxKey,
		)
	})

	ts := httptest.NewServer(fixture.server)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.ID +
		"/runtime/sessions/" + originalShell.Key + "/terminal?cols=80&rows=24"
	conn := dialWebSocketForTest(t, ctx, wsURL, "pty-owner shell before restart")
	workspaceTerminalConnWriteRead(
		t, ctx, conn,
		"export KENN_FORGE_RESTART_MARK=still-here\r"+
			"printf 'before-restart:%s\\n' \"$KENN_FORGE_RESTART_MARK\"\r",
		"before-restart:still-here",
	)
	require.NoError(conn.Close(websocket.StatusNormalClosure, "restart"))
	ts.Close()

	serverfake.GracefulShutdown(t, fixture.server)

	cfg := &config.Config{Tmux: config.Tmux{
		Command: []string{filepath.Join(dir, "missing-tmux")},
	}}
	options := ptyOwnerServerOptions(ptyOwnerDir)
	options.Clones = fixture.clones
	options.WorktreeDir = fixture.worktrees
	restarted := New(
		fixture.database, fixture.syncer, nil, "/", cfg, options,
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, restarted) })
	restartedClient := setupTestClient(t, restarted)

	runtimeResp, err := restartedClient.HTTP.GetWorkspaceRuntimeWithResponse(ctx, &generated.GetWorkspaceRuntimeRequestOptions{PathParams: &generated.GetWorkspaceRuntimePath{ID: ws.ID}})
	require.NoError(err)
	require.Equal(http.StatusOK, runtimeResp.StatusCode)
	require.NotNil(runtimeResp.JSON200)
	require.NotNil(runtimeResp.JSON200.Sessions)
	require.Len(runtimeResp.JSON200.Sessions, 2)
	var restoredShell *generated.SessionInfo
	var unavailableTmux *generated.SessionInfo
	for i := range runtimeResp.JSON200.Sessions {
		session := &runtimeResp.JSON200.Sessions[i]
		switch session.Key {
		case originalShell.Key:
			restoredShell = session
		case staleTmuxKey:
			unavailableTmux = session
		}
	}
	require.NotNil(restoredShell)
	assert.Equal(string(localruntime.SessionStatusRunning), restoredShell.Status)
	require.NotNil(unavailableTmux)
	assert.Equal(string(localruntime.SessionStatusError), unavailableTmux.Status)

	restartedTS := httptest.NewServer(restarted)
	t.Cleanup(restartedTS.Close)
	restartedURL := "ws" + strings.TrimPrefix(restartedTS.URL, "http") +
		"/ws/v1/workspaces/" + ws.ID +
		"/runtime/sessions/" + originalShell.Key + "/terminal?cols=80&rows=24"
	restartedConn := dialWebSocketForTest(
		t, ctx, restartedURL, "pty-owner shell after restart",
	)
	defer restartedConn.Close(websocket.StatusNormalClosure, "done")

	workspaceTerminalConnWriteRead(
		t, ctx, restartedConn,
		"printf 'after-restart:%s\\n' \"$KENN_FORGE_RESTART_MARK\"\r",
		"after-restart:still-here",
	)
	require.NoError(restartedConn.Close(websocket.StatusNormalClosure, "stop"))

	stopResp, err := restartedClient.HTTP.StopWorkspaceRuntimeSessionWithResponse(ctx, &generated.StopWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.StopWorkspaceRuntimeSessionPath{ID: ws.ID, SessionKey: originalShell.Key}})
	require.NoError(err)
	require.Equal(http.StatusNoContent, stopResp.StatusCode)
	_, err = os.Stat(filepath.Join(ptyOwnerDir, originalShell.Key))
	assert.True(os.IsNotExist(err))
}

func TestWorkspaceRuntimeUnavailablePtyOwnerSessionStaysUntilUserStopE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses /bin/sh")
	}
	runSerialPTYE2E(t)

	require := require.New(t)
	assert := assert.New(t)

	fixture, dir, ptyOwnerDir := setupPtyOwnerWorkspaceFixture(t)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)
	sessionKey := ws.ID + "_stale-shell"
	createdAt := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	require.NoError(fixture.server.workspaces.RecordRuntimeSession(
		ctx,
		db.WorkspaceRuntimeSession{
			WorkspaceID: ws.ID,
			SessionKey:  sessionKey,
			TargetKey:   string(localruntime.LaunchTargetPlainShell),
			Label:       "Shell",
			Kind:        string(localruntime.LaunchTargetPlainShell),
			Scope:       "session",
			CreatedAt:   createdAt,
		},
	))

	serverfake.GracefulShutdown(t, fixture.server)

	cfg := &config.Config{Tmux: config.Tmux{
		Command: []string{filepath.Join(dir, "missing-tmux")},
	}}
	options := ptyOwnerServerOptions(ptyOwnerDir)
	options.Clones = fixture.clones
	options.WorktreeDir = fixture.worktrees
	restarted := New(
		fixture.database, fixture.syncer, nil, "/", cfg, options,
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, restarted) })
	restartedClient := setupTestClient(t, restarted)

	runtimeResp, err := restartedClient.HTTP.GetWorkspaceRuntimeWithResponse(ctx, &generated.GetWorkspaceRuntimeRequestOptions{PathParams: &generated.GetWorkspaceRuntimePath{ID: ws.ID}})
	require.NoError(err)
	require.Equal(http.StatusOK, runtimeResp.StatusCode)
	require.NotNil(runtimeResp.JSON200)
	require.NotNil(runtimeResp.JSON200.Sessions)
	require.Len(runtimeResp.JSON200.Sessions, 1)
	session := runtimeResp.JSON200.Sessions[0]
	assert.Equal(sessionKey, session.Key)
	assert.Equal("Shell", session.Label)
	assert.Equal(string(localruntime.SessionStatusError), session.Status)
	assert.Equal(string(localruntime.LaunchTargetPlainShell), session.TargetKey)

	renameResp, err := restartedClient.HTTP.RenameWorkspaceRuntimeSessionWithResponse(ctx, &generated.RenameWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.RenameWorkspaceRuntimeSessionPath{ID: ws.ID, SessionKey: sessionKey}, Body: &generated.RenameWorkspaceRuntimeSessionInputBody{
		Label: "Recovered shell",
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, renameResp.StatusCode)
	require.NotNil(renameResp.JSON200)
	assert.Equal(sessionKey, renameResp.JSON200.Key)
	assert.Equal("Recovered shell", renameResp.JSON200.Label)
	assert.Equal(string(localruntime.SessionStatusError), renameResp.JSON200.Status)

	runtimeResp, err = restartedClient.HTTP.GetWorkspaceRuntimeWithResponse(ctx, &generated.GetWorkspaceRuntimeRequestOptions{PathParams: &generated.GetWorkspaceRuntimePath{ID: ws.ID}})
	require.NoError(err)
	require.Equal(http.StatusOK, runtimeResp.StatusCode)
	require.NotNil(runtimeResp.JSON200)
	require.NotNil(runtimeResp.JSON200.Sessions)
	require.Len(runtimeResp.JSON200.Sessions, 1)
	assert.Equal("Recovered shell", runtimeResp.JSON200.Sessions[0].Label)

	stored, err := fixture.database.ListWorkspaceRuntimeSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal(sessionKey, stored[0].SessionKey)
	assert.Equal("Recovered shell", stored[0].Label)

	stopResp, err := restartedClient.HTTP.StopWorkspaceRuntimeSessionWithResponse(ctx, &generated.StopWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.StopWorkspaceRuntimeSessionPath{ID: ws.ID, SessionKey: sessionKey}})
	require.NoError(err)
	require.Equal(http.StatusNoContent, stopResp.StatusCode)
	stored, err = fixture.database.ListWorkspaceRuntimeSessions(ctx, ws.ID)
	require.NoError(err)
	assert.Empty(stored)
}

func TestWorkspaceDeleteStopsPtyOwnerAgentAfterServerRestartE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses /bin/sh")
	}
	runSerialPTYE2E(t)

	require := require.New(t)
	assert := assert.New(t)

	dir := t.TempDir()
	ptyOwnerDir := filepath.Join(dir, "pty-owner")
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: serverRuntimeHelperCommand("sleep"),
		}},
		Tmux: config.Tmux{Command: []string{filepath.Join(dir, "missing-tmux")}},
	}
	fixture := setupWorkspaceServerFixtureWithOptions(
		t, cfg, ptyOwnerServerOptions(ptyOwnerDir),
	)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	launchResp, err := fixture.client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(ctx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: ws.ID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{TargetKey: "helper"}})
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode, string(launchResp.Body))
	require.NotNil(launchResp.JSON200)
	sessionKey := launchResp.JSON200.Key
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, sessionKey)

	_, err = os.Stat(filepath.Join(ptyOwnerDir, sessionKey))
	require.NoError(err)
	serverfake.GracefulShutdown(t, fixture.server)

	options := ptyOwnerServerOptions(ptyOwnerDir)
	options.Clones = fixture.clones
	options.WorktreeDir = fixture.worktrees
	restarted := New(
		fixture.database, fixture.syncer, nil, "/", cfg, options,
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, restarted) })
	restartedClient := setupTestClient(t, restarted)

	force := true
	delResp, err := restartedClient.HTTP.DeleteWorkspaceWithResponse(ctx, &generated.DeleteWorkspaceRequestOptions{PathParams: &generated.DeleteWorkspacePath{ID: ws.ID}, Query: &generated.DeleteWorkspaceQuery{Force: &force}})
	require.NoError(err)
	require.Equal(http.StatusNoContent, delResp.StatusCode, string(delResp.Body))

	_, err = os.Stat(filepath.Join(ptyOwnerDir, sessionKey))
	assert.True(os.IsNotExist(err))
}

func setupPtyOwnerWorkspaceFixture(
	t *testing.T,
	enableEnrichment ...bool,
) (workspaceServerFixture, string, string) {
	t.Helper()

	dir := t.TempDir()
	ptyOwnerDir := filepath.Join(dir, "pty-owner")
	cfg := &config.Config{Tmux: config.Tmux{
		Command: []string{filepath.Join(dir, "missing-tmux")},
	}}
	return setupWorkspaceServerFixtureWithOptions(
		t, cfg, ptyOwnerServerOptions(ptyOwnerDir), enableEnrichment...,
	), dir, ptyOwnerDir
}

func ptyOwnerServerOptions(ptyOwnerDir string) ServerOptions {
	return ServerOptions{
		PtyOwnerDir:     ptyOwnerDir,
		PtyOwnerExePath: os.Args[0],
		PtyOwnerExeArgs: ptyOwnerHelperExeArgs(os.Getpid()),
		PtyOwnerCommand: []string{"/bin/sh"},
	}
}

func ptyOwnerHelperExeArgs(parentPID int) []string {
	return []string{
		"-test.run=TestServerPtyOwnerHelperProcess",
		fmt.Sprintf("-server-pty-owner-parent-pid=%d", parentPID),
		"--",
	}
}

func gitLocalRemoteURL(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	slashPath := filepath.ToSlash(path)
	if !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

func cleanupPtyOwnerWorkspace(
	t *testing.T,
	ptyOwnerDir string,
	session string,
) {
	t.Helper()
	t.Cleanup(func() {
		_ = (&ptyowner.Client{Root: ptyOwnerDir}).Stop(
			context.Background(), session,
		)
	})
}

func TestWorkspaceRuntimeExistingSessionsAvailableWhenWorkspaceErroredE2E(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	require := require.New(t)
	assert := assert.New(t)

	client, database, _, _, _ := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(ctx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: ws.ID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{
		TargetKey: "plain_shell",
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode)
	require.NotNil(launchResp.JSON200)
	shell := launchResp.JSON200

	errMsg := "restart failed"
	require.NoError(database.UpdateWorkspaceStatus(ctx, ws.ID, "error", &errMsg))

	getResp, err := client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, &generated.GetWorkspaceRuntimeRequestOptions{PathParams: &generated.GetWorkspaceRuntimePath{ID: ws.ID}})
	require.NoError(err)
	require.Equal(http.StatusOK, getResp.StatusCode)
	require.NotNil(getResp.JSON200)
	require.NotNil(getResp.JSON200.Sessions)
	require.Len(getResp.JSON200.Sessions, 1)
	assert.Equal(shell.Key, getResp.JSON200.Sessions[0].Key)
	assert.Equal(string(localruntime.SessionStatusRunning), getResp.JSON200.Sessions[0].Status)

	stopResp, err := client.HTTP.StopWorkspaceRuntimeSessionWithResponse(ctx, &generated.StopWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.StopWorkspaceRuntimeSessionPath{ID: ws.ID, SessionKey: shell.Key}})
	require.NoError(err)
	require.Equal(http.StatusNoContent, stopResp.StatusCode)

	relaunchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(ctx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: ws.ID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{
		TargetKey: "plain_shell",
	}})
	require.Error(err)
	require.NotNil(relaunchResp)
	require.Equal(http.StatusConflict, relaunchResp.StatusCode)
}

func TestWorkspaceRuntimeIncludesStoredRuntimeSessionsAfterReloadE2E(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	tmuxPath := filepath.Join(dir, "fake-tmux")
	database := dbtest.Open(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	worktreeDir := filepath.Join(dir, "worktrees")
	ownerMarker := workspace.NewManager(database, worktreeDir).TmuxOwnerMarker()
	restoredSession := runtimeTmuxSessionNameForTest("0000000000000001", "helper")
	require.NoError(os.WriteFile(tmuxPath, []byte("#!/bin/sh\n"+
		"TMUX_TEST_OWNER_MARKER="+shellquote.Join(ownerMarker)+"\n"+
		"TMUX_TEST_RESTORED_SESSION="+shellquote.Join(restoredSession)+"\n"+
		`if [ "$1" = "-u" ]; then shift; fi
case "$1" in
  list-sessions)
    printf '%s\n' kenn-forge-0000000000000001
    printf '%s\n' "$TMUX_TEST_RESTORED_SESSION"
    exit 0
    ;;
  attach-session)
    sleep 30
    exit 0
    ;;
  show-options)
    printf '%s\n' "$TMUX_TEST_OWNER_MARKER"
    exit 0
    ;;
  kill-session)
    exit 0
    ;;
esac
exit 0
`), 0o755))

	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "helper",
		Label:   "Helper",
		Command: serverRuntimeHelperCommand("sleep"),
	}}, Tmux: config.Tmux{Command: []string{tmuxPath}}}
	ctx := context.Background()
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
		TmuxSession:     "kenn-forge-0000000000000001",
		Status:          "ready",
	}
	require.NoError(database.InsertWorkspace(ctx, ws))
	tmuxSession := restoredSession
	sessionKey := ws.ID + "_restoredhelper01"
	createdAt := time.Now().UTC().Add(-time.Minute)
	require.NoError(database.UpsertWorkspaceRuntimeSession(
		ctx,
		&db.WorkspaceRuntimeSession{
			WorkspaceID: ws.ID,
			SessionKey:  sessionKey,
			TargetKey:   "helper",
			Label:       "Helper",
			Kind:        string(localruntime.LaunchTargetAgent),
			Scope:       "session",
			TmuxSession: tmuxSession,
			CreatedAt:   createdAt,
		},
	))
	srv := New(database, nil, nil, "/", cfg, ServerOptions{
		WorktreeDir: worktreeDir,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	require.Len(srv.runtime.ListSessions(ws.ID), 1)

	resp, err := client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, &generated.GetWorkspaceRuntimeRequestOptions{PathParams: &generated.GetWorkspaceRuntimePath{ID: ws.ID}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Sessions)
	require.Len(resp.JSON200.Sessions, 1)

	session := resp.JSON200.Sessions[0]
	assert.Equal(sessionKey, session.Key)
	assert.Equal(ws.ID, session.WorkspaceID)
	assert.Equal("helper", session.TargetKey)
	assert.Equal("Helper", session.Label)
	assert.Equal(string(localruntime.LaunchTargetAgent), session.Kind)
	assert.Equal(string(localruntime.SessionStatusRunning), session.Status)
	assert.False(session.CreatedAt.IsZero())
	assert.Equal(time.UTC, session.CreatedAt.Location())
}

func TestWorkspaceRuntimeLaunchAgentCreatesProbeableTmuxSessionE2E(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "fake-tmux")
	agentPath := filepath.Join(dir, "helper-agent")
	require.NoError(os.WriteFile(agentPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(os.WriteFile(tmuxPath, fmt.Appendf(nil, `#!/bin/sh
printf '%%s\0' "$#" "$@" >> %s
session_file=%s.sessions
target=""
mode=""
new_session=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-t" ]; then target="$a"; fi
  if [ "$prev" = "-s" ]; then new_session="$a"; fi
  if [ "$a" = "display-message" ]; then mode="display-message"; fi
  if [ "$a" = "capture-pane" ]; then mode="capture-pane"; fi
  if [ "$a" = "list-sessions" ]; then
    [ -f "$session_file" ] && cat "$session_file"
    exit 0
  fi
  prev="$a"
done
if [ "$mode" = "display-message" ]; then
  case "$target" in
    forge-????????????????-*) printf '⠴ t3code-b5014b03\n' ;;
    *) printf 'idle\n' ;;
  esac
  exit 0
fi
if [ "$mode" = "capture-pane" ]; then
  printf 'stable\n'
  exit 0
fi
if [ "$1" = "-u" ]; then shift; fi
if [ "$1" = "has-session" ]; then
  echo "can't find session: $3" >&2
  exit 1
fi
if [ "$1" = "attach-session" ]; then
  cat >/dev/null
  exit 0
fi
if [ -n "$new_session" ]; then
  printf '%%s\n' "$new_session" >> "$session_file"
fi
exit 0
`, shellquote.Join(record), shellquote.Join(record)), 0o755))
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: []string{agentPath, "--flag"},
		}},
		Tmux: config.Tmux{Command: []string{tmuxPath}},
	}
	client, database, _, _, srv := setupTestServerWithWorkspacesServerAndEnrichment(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	resp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(ctx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: ws.ID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{
		TargetKey: "helper",
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)

	var newSession []string
	require.Eventually(func() bool {
		for _, argv := range serverfake.ReadTmuxRecord(t, record) {
			if len(argv) > 0 &&
				argv[0] == "new-session" &&
				runtimeTmuxNewSessionCommandContains(t, argv, agentPath) {
				newSession = argv
				return true
			}
		}
		return false
	}, 2*time.Second, 20*time.Millisecond)
	scriptText, ok := runtimeTmuxNewSessionScript(t, newSession)
	require.True(ok, "new-session should run generated tmux pane script")

	session, ok := argAfter(newSession, "-s")
	require.True(ok, "new-session should name a tmux session")
	assert.True(isRuntimeTmuxSessionNameForWorkspace(ws.ID, session))
	assert.Contains(newSession, "-d")
	assert.Contains(newSession, "-c")
	assert.Contains(scriptText, agentPath)
	assert.Contains(scriptText, "--flag")
	assert.Contains(newSession, ";")
	assert.Contains(newSession, "set-option")
	assert.Contains(newSession, "-t")
	assert.Contains(newSession, session)
	assert.Contains(newSession, "@forge_owner")
	assert.Contains(newSession, srv.workspaces.TmuxOwnerMarker())

	var listed *generated.WorkspaceResponse
	require.Eventually(func() bool {
		listResp, err := client.HTTP.ListWorkspacesWithResponse(ctx)
		require.NoError(err)
		if listResp.StatusCode != http.StatusOK ||
			listResp.JSON200 == nil || listResp.JSON200.Workspaces == nil {
			return false
		}
		listed = nil
		for i := range listResp.JSON200.Workspaces {
			if listResp.JSON200.Workspaces[i].ID == ws.ID {
				listed = &listResp.JSON200.Workspaces[i]
				break
			}
		}
		return listed != nil && listed.TmuxWorking &&
			listed.TmuxActivitySource == workspaceapi.TmuxActivitySourceTitle &&
			listed.TmuxPaneTitle != nil
	}, 2*time.Second, 10*time.Millisecond)
	require.NotNil(listed)
	assert.True(listed.TmuxWorking)
	assert.Equal(workspaceapi.TmuxActivitySourceTitle, listed.TmuxActivitySource)
	require.NotNil(listed.TmuxPaneTitle)
	assert.Equal("⠴ t3code-b5014b03", *listed.TmuxPaneTitle)
	assert.Contains(serverfake.ReadTmuxRecord(t, record), []string{
		"display-message", "-p", "-t", session, "#{pane_title}",
	})
	stored, err := database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal(session, stored[0].TmuxSession)
	assert.Equal("helper", stored[0].TargetKey)
}

func TestWorkspaceResponseProbesStoredRuntimeTmuxSessionWithoutBaseE2E(
	t *testing.T,
) {
	if runtime.GOOS == "windows" {
		t.Skip("stored runtime tmux probing is Unix-only")
	}

	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "fake-tmux")
	database := dbtest.Open(t)
	worktreeDir := filepath.Join(dir, "worktrees")
	ownerMarker := workspace.NewManager(database, worktreeDir).TmuxOwnerMarker()
	require.NoError(os.WriteFile(tmuxPath, []byte("#!/bin/sh\n"+
		"TMUX_RECORD="+shellquote.Join(record)+"\n"+
		"TMUX_TEST_OWNER_MARKER="+shellquote.Join(ownerMarker)+"\n"+
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"
target=""
mode=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-t" ]; then target="$a"; fi
  if [ "$a" = "display-message" ]; then mode="display-message"; fi
  if [ "$a" = "capture-pane" ]; then mode="capture-pane"; fi
  prev="$a"
done
if [ "$1" = "-u" ]; then shift; fi
case "$1" in
  list-sessions)
    printf '%s\n' 'forge-0000000000000001-e81d3b0e9d82feaa'
    exit 0
    ;;
  show-options)
    printf '%s\n' "$TMUX_TEST_OWNER_MARKER"
    exit 0
    ;;
  attach-session)
    cat >/dev/null
    exit 0
    ;;
esac
if [ "$mode" = "display-message" ]; then
  case "$target" in
    forge-????????????????-*) printf '⠴ t3code-b5014b03\n' ;;
    *) printf 'idle\n' ;;
  esac
  exit 0
fi
if [ "$mode" = "capture-pane" ]; then
  printf 'stable\n'
  exit 0
fi
exit 0
`), 0o755))

	serverfake.SeedPR(t, database, "acme", "widget", 1)

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
		Status:          "ready",
	}
	require.NoError(database.InsertWorkspace(t.Context(), ws))
	sessionName := runtimeTmuxSessionNameForTest("0000000000000001", "helper")
	recordRuntimeTmuxSessionForServerTest(
		t, database, ws.ID, "", "helper", sessionName, time.Time{},
	)

	cfg := &config.Config{Tmux: config.Tmux{Command: []string{tmuxPath}}}
	srv := New(database, nil, nil, "/", cfg, ServerOptions{
		WorktreeDir: worktreeDir,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)
	var got *generated.WorkspaceResponse
	require.Eventually(func() bool {
		resp, err := client.HTTP.GetWorkspaceWithResponse(t.Context(), &generated.GetWorkspaceRequestOptions{PathParams: &generated.GetWorkspacePath{ID: ws.ID}})
		require.NoError(err)
		if resp.StatusCode != http.StatusOK || resp.JSON200 == nil {
			return false
		}
		got = resp.JSON200
		return got.TmuxWorking &&
			got.TmuxActivitySource == workspaceapi.TmuxActivitySourceTitle &&
			got.TmuxPaneTitle != nil
	}, 2*time.Second, 10*time.Millisecond)
	require.NotNil(got)
	assert.True(got.TmuxWorking)
	assert.Equal(workspaceapi.TmuxActivitySourceTitle, got.TmuxActivitySource)
	require.NotNil(got.TmuxPaneTitle)
	assert.Equal("⠴ t3code-b5014b03", *got.TmuxPaneTitle)
	assert.Contains(serverfake.ReadTmuxRecord(t, record), []string{
		"display-message", "-p", "-t",
		sessionName, "#{pane_title}",
	})
}

func TestWorkspaceRuntimeLaunchTmuxOwnerMarkerFailureRejectsSessionE2E(
	t *testing.T,
) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "fake-tmux")
	agentPath := filepath.Join(dir, "helper-agent")
	require.NoError(os.WriteFile(agentPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(os.WriteFile(tmuxPath, fmt.Appendf(nil, `#!/bin/sh
printf '%%s\0' "$#" "$@" >> %s
target=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-t" ]; then target="$a"; fi
  prev="$a"
done
case "$1" in
  has-session)
    echo "can't find session: $3" >&2
    exit 1
    ;;
  new-session)
    for a in "$@"; do
      if [ "$a" = "@forge_owner" ]; then
        echo "owner marker denied" >&2
        exit 42
      fi
    done
    exit 0
    ;;
  set-option)
    case "$target" in
      forge-????????????????-*)
        echo "owner marker denied" >&2
        exit 42
        ;;
    esac
    exit 0
    ;;
  kill-session)
    exit 0
    ;;
esac
exit 0
`, shellquote.Join(record)), 0o755))
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: []string{agentPath},
		}},
		Tmux: config.Tmux{Command: []string{tmuxPath}},
	}
	client, database, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(ctx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: ws.ID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{
		TargetKey: "helper",
	}})
	require.Error(err)
	require.NotNil(launchResp)
	require.Equal(http.StatusInternalServerError, launchResp.StatusCode)

	var sessionName string
	require.Eventually(func() bool {
		name, ok := findRuntimeTmuxNewSessionName(
			t, serverfake.ReadTmuxRecord(t, record), ws.ID, "",
		)
		if ok {
			sessionName = name
		}
		return sessionName != ""
	}, 2*time.Second, 20*time.Millisecond)
	var runtimeNewSession []string
	for _, argv := range serverfake.ReadTmuxRecord(t, record) {
		if len(argv) > 0 &&
			argv[0] == "new-session" &&
			slices.Contains(argv, sessionName) {
			runtimeNewSession = argv
			break
		}
	}
	require.NotNil(runtimeNewSession)
	assert.Contains(runtimeNewSession, "@forge_owner")
	assert.False(slices.ContainsFunc(
		serverfake.ReadTmuxRecord(t, record),
		func(argv []string) bool {
			return len(argv) > 0 &&
				argv[0] == "set-option" &&
				slices.Contains(argv, sessionName)
		},
	))
	assert.NotContains(serverfake.ReadTmuxRecord(t, record), []string{
		"kill-session", "-t", sessionName,
	})

	require.Eventually(func() bool {
		runtimeResp, err := client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, &generated.GetWorkspaceRuntimeRequestOptions{PathParams: &generated.GetWorkspaceRuntimePath{ID: ws.ID}})
		if err != nil ||
			runtimeResp.StatusCode != http.StatusOK ||
			runtimeResp.JSON200 == nil ||
			runtimeResp.JSON200.Sessions == nil {
			return false
		}
		return len(runtimeResp.JSON200.Sessions) == 0
	}, 2*time.Second, 20*time.Millisecond)

	require.Eventually(func() bool {
		stored, err := database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
		return err == nil && len(stored) == 0
	}, 2*time.Second, 20*time.Millisecond)
}

func runtimeTmuxSessionNameForTest(workspaceID string, targetKey string) string {
	sum := sha256.Sum256([]byte(targetKey))
	return "forge-" + workspaceID + "-" + hex.EncodeToString(sum[:8])
}

func isRuntimeTmuxSessionNameForWorkspace(
	workspaceID string,
	sessionName string,
) bool {
	suffix := strings.TrimPrefix(sessionName, "forge-"+workspaceID+"-")
	return suffix != sessionName &&
		len(suffix) == 16 &&
		strings.IndexFunc(suffix, func(r rune) bool {
			return !strings.ContainsRune("0123456789abcdef", r)
		}) == -1
}

func findRuntimeTmuxNewSessionName(
	t *testing.T,
	argvs [][]string,
	workspaceID string,
	commandPath string,
) (string, bool) {
	t.Helper()
	for _, argv := range argvs {
		if len(argv) == 0 ||
			argv[0] != "new-session" {
			continue
		}
		if commandPath != "" &&
			!runtimeTmuxNewSessionCommandContains(t, argv, commandPath) {
			continue
		}
		name, ok := argAfter(argv, "-s")
		if ok && isRuntimeTmuxSessionNameForWorkspace(workspaceID, name) {
			return name, true
		}
	}
	return "", false
}

func runtimeTmuxNewSessionCommandContains(
	t *testing.T,
	argv []string,
	needle string,
) bool {
	t.Helper()
	if strings.Contains(strings.Join(argv, "\n"), needle) {
		return true
	}
	scriptText, ok := runtimeTmuxNewSessionScript(t, argv)
	return ok && strings.Contains(scriptText, needle)
}

func runtimeTmuxNewSessionScript(t *testing.T, argv []string) (string, bool) {
	t.Helper()
	command := tmuxNewSessionPaneCommand(argv)
	if command == "" {
		return "", false
	}
	words, err := shellquote.Split(command)
	if err != nil ||
		len(words) != 2 ||
		words[0] != "/bin/sh" {
		return "", false
	}
	data, err := os.ReadFile(words[1])
	if err != nil {
		return "", false
	}
	return string(data), true
}

func tmuxNewSessionPaneCommand(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	command := argv[len(argv)-1]
	for i, arg := range argv {
		if arg == ";" && i > 0 {
			return argv[i-1]
		}
	}
	return command
}

func TestWorkspaceRuntimeTmuxSessionsHashUnsafeTargetKeysE2E(
	t *testing.T,
) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := writeRuntimeTmuxLifecycleRecorder(t, dir, record)
	agentPath := filepath.Join(dir, "helper-agent")
	require.NoError(os.WriteFile(agentPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	cfg := &config.Config{
		Agents: []config.Agent{
			{Key: "foo/bar", Label: "Foo Slash", Command: []string{agentPath}},
			{Key: "foo:bar", Label: "Foo Colon", Command: []string{agentPath}},
		},
		Tmux: config.Tmux{Command: []string{tmuxPath}},
	}
	client, database, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	var launched []generated.SessionInfo
	for _, targetKey := range []string{"foo/bar", "foo:bar"} {
		resp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(ctx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: ws.ID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: targetKey,
		}})
		require.NoError(err)
		require.Equal(http.StatusOK, resp.StatusCode)
		require.NotNil(resp.JSON200)
		launched = append(launched, *resp.JSON200)
	}

	stored, err := database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(stored, 2)
	sessionsByTarget := map[string]string{}
	for _, session := range stored {
		sessionsByTarget[session.TargetKey] = session.TmuxSession
	}
	slashSession := sessionsByTarget["foo/bar"]
	colonSession := sessionsByTarget["foo:bar"]
	require.NotEmpty(slashSession)
	require.NotEmpty(colonSession)
	assert.NotEqual(slashSession, colonSession)
	for _, sessionName := range []string{slashSession, colonSession} {
		assert.True(isRuntimeTmuxSessionNameForWorkspace(ws.ID, sessionName))
		assert.NotContains(sessionName, "foo")
		assert.NotContains(sessionName, "/")
		assert.NotContains(sessionName, ":")
	}

	for _, session := range launched {
		stopResp, err := client.HTTP.StopWorkspaceRuntimeSessionWithResponse(ctx, &generated.StopWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.StopWorkspaceRuntimeSessionPath{ID: ws.ID, SessionKey: session.Key}})
		require.NoError(err)
		require.Equal(http.StatusNoContent, stopResp.StatusCode)
	}
	stored, err = database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	assert.Empty(stored)
	assert.Contains(serverfake.ReadTmuxRecord(t, record), []string{
		"kill-session", "-t", slashSession,
	})
	assert.Contains(serverfake.ReadTmuxRecord(t, record), []string{
		"kill-session", "-t", colonSession,
	})
}

func TestWorkspaceRuntimeStopClearsStoredWrappedAgentSessionAfterRuntimeForgetE2E(
	t *testing.T,
) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := writeRuntimeTmuxLifecycleRecorder(t, dir, record)
	agentPath := filepath.Join(dir, "helper-agent")
	require.NoError(os.WriteFile(agentPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: []string{agentPath},
		}},
		Tmux: config.Tmux{Command: []string{tmuxPath}},
	}
	client, database, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(ctx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: ws.ID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{
		TargetKey: "helper",
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode)
	require.NotNil(launchResp.JSON200)

	stored, err := database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(stored, 1)
	sessionName := stored[0].TmuxSession
	assert.True(isRuntimeTmuxSessionNameForWorkspace(ws.ID, sessionName))

	require.NoError(srv.runtime.Stop(ctx, ws.ID, launchResp.JSON200.Key))
	stored, err = database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(stored, 1)

	stopResp, err := client.HTTP.StopWorkspaceRuntimeSessionWithResponse(ctx, &generated.StopWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.StopWorkspaceRuntimeSessionPath{ID: ws.ID, SessionKey: launchResp.JSON200.Key}})
	require.NoError(err)
	require.Equal(http.StatusNoContent, stopResp.StatusCode)
	stored, err = database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	assert.Empty(stored)
	assert.Contains(serverfake.ReadTmuxRecord(t, record), []string{
		"kill-session", "-t", sessionName,
	})
}

func TestWorkspaceRuntimeStopTmuxCleanupFailureRetainsStoredSessionE2E(
	t *testing.T,
) {
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	tmuxPath := filepath.Join(dir, "fake-tmux")
	agentPath := filepath.Join(dir, "helper-agent")
	require.NoError(os.WriteFile(agentPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
target=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-t" ]; then target="$a"; fi
  prev="$a"
done
if [ "$1" = "-u" ]; then shift; fi
if [ "$1" = "attach-session" ]; then
  cat >/dev/null
  exit 0
fi
if [ "$1" = "has-session" ]; then
  echo "can't find session: $target" >&2
  exit 1
fi
if [ "$1" = "show-option" ]; then
  exit 0
fi
if [ "$1" = "kill-session" ]; then
  case "$target" in
    forge-????????????????-*)
      echo "permission denied" >&2
      exit 42
      ;;
  esac
fi
if [ "$1" = "new-session" ] || [ "$1" = "set-option" ]; then
  exit 0
fi
exit 0
`), 0o755))
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: []string{agentPath},
		}},
		Tmux: config.Tmux{Command: []string{tmuxPath}},
	}
	client, database, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)
	t.Cleanup(func() {
		_ = database.DeleteWorkspaceRuntimeSessions(context.Background(), ws.ID)
	})

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(ctx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: ws.ID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{
		TargetKey: "helper",
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode)
	require.NotNil(launchResp.JSON200)

	stopResp, err := client.HTTP.StopWorkspaceRuntimeSessionWithResponse(ctx, &generated.StopWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.StopWorkspaceRuntimeSessionPath{ID: ws.ID, SessionKey: launchResp.JSON200.Key}})
	require.Error(err)
	require.NotNil(stopResp)
	require.Equal(http.StatusInternalServerError, stopResp.StatusCode)

	assert.Eventually(func() bool {
		getResp, err := client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, &generated.GetWorkspaceRuntimeRequestOptions{PathParams: &generated.GetWorkspaceRuntimePath{ID: ws.ID}})
		if err != nil ||
			getResp.StatusCode != http.StatusOK ||
			getResp.JSON200 == nil ||
			getResp.JSON200.Sessions == nil {
			return false
		}
		if len(getResp.JSON200.Sessions) != 1 {
			return false
		}
		session := getResp.JSON200.Sessions[0]
		return session.Key == launchResp.JSON200.Key &&
			session.Status == string(localruntime.SessionStatusError)
	}, 2*time.Second, 20*time.Millisecond)

	stored, err := database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(stored, 1)
	assert.True(
		isRuntimeTmuxSessionNameForWorkspace(ws.ID, stored[0].TmuxSession),
	)
}

func TestWorkspaceDeletionRecoversAcrossServerRestartE2E(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	require := require.New(t)
	assert := assert.New(t)
	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	// Persist the same admission transition performed by DELETE immediately
	// before destructive teardown. This is the durable state a process crash
	// can leave behind.
	started, err := fixture.database.BeginWorkspaceDeletion(ctx, ws.ID)
	require.NoError(err)
	require.True(started)
	serverfake.GracefulShutdown(t, fixture.server)
	var databasePath string
	require.NoError(fixture.database.ReadDB().QueryRowContext(
		ctx, "SELECT file FROM pragma_database_list WHERE name = 'main'",
	).Scan(&databasePath))
	restartedDatabase, err := db.OpenPreparedForTest(databasePath)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(restartedDatabase.Close()) })

	restarted := New(
		restartedDatabase, fixture.syncer, nil, "/", nil,
		ServerOptions{
			Clones:                     fixture.clones,
			WorktreeDir:                fixture.worktrees,
			PtyOwnerInProcess:          true,
			DisableWorkspaceEnrichment: true,
		},
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, restarted) })
	restartedClient := setupTestClient(t, restarted)

	getResp, err := restartedClient.HTTP.GetWorkspaceWithResponse(ctx, &generated.GetWorkspaceRequestOptions{PathParams: &generated.GetWorkspacePath{ID: ws.ID}})
	require.NoError(err)
	require.Equal(http.StatusOK, getResp.StatusCode, string(getResp.Body))
	require.NotNil(getResp.JSON200)
	assert.Equal("deletion_failed", getResp.JSON200.Status)
	require.NotNil(getResp.JSON200.ErrorMessage)
	assert.Equal(
		"workspace deletion was interrupted by a server restart; retry deletion to continue",
		*getResp.JSON200.ErrorMessage,
	)

	force := true
	deleteResp, err := restartedClient.HTTP.DeleteWorkspaceWithResponse(ctx, &generated.DeleteWorkspaceRequestOptions{PathParams: &generated.DeleteWorkspacePath{ID: ws.ID}, Query: &generated.DeleteWorkspaceQuery{Force: &force}})
	require.NoError(err)
	require.Equal(
		http.StatusNoContent, deleteResp.StatusCode, string(deleteResp.Body),
	)

	getResp, err = restartedClient.HTTP.GetWorkspaceWithResponse(ctx, &generated.GetWorkspaceRequestOptions{PathParams: &generated.GetWorkspacePath{ID: ws.ID}})
	require.Error(err)
	require.NotNil(getResp)
	assert.Equal(http.StatusNotFound, getResp.StatusCode)
	_, err = os.Stat(ws.WorktreePath)
	assert.True(os.IsNotExist(err))
}

func TestWorkspaceDeleteStopsRuntimeSessionsE2E(t *testing.T) {
	runSerialPTYE2E(t)

	require := require.New(t)
	assert := assert.New(t)
	disableTmuxAgentSessions := false
	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "helper",
		Label:   "Helper",
		Command: serverRuntimeHelperCommand("sleep"),
	}}, Tmux: config.Tmux{AgentSessions: &disableTmuxAgentSessions}}
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(ctx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: ws.ID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{
		TargetKey: "helper",
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode)
	require.NotNil(launchResp.JSON200)

	_ = launchPlainShellRuntimeSession(t, ctx, client, ws.ID)

	require.Len(srv.runtime.ListSessions(ws.ID), 2)

	force := true
	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(ctx, &generated.DeleteWorkspaceRequestOptions{PathParams: &generated.DeleteWorkspacePath{ID: ws.ID}, Query: &generated.DeleteWorkspaceQuery{Force: &force}})
	require.NoError(err)
	require.Equal(http.StatusNoContent, delResp.StatusCode)

	assert.Empty(srv.runtime.ListSessions(ws.ID))
}

func TestWorkspaceDeleteDirtyKeepsRuntimeSessionsE2E(t *testing.T) {
	runSerialPTYE2E(t)

	require := require.New(t)
	assert := assert.New(t)
	disableTmuxAgentSessions := false
	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "helper",
		Label:   "Helper",
		Command: serverRuntimeHelperCommand("sleep"),
	}}, Tmux: config.Tmux{AgentSessions: &disableTmuxAgentSessions}}
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(ctx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: ws.ID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{
		TargetKey: "helper",
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode)
	_ = launchPlainShellRuntimeSession(t, ctx, client, ws.ID)
	require.Len(srv.runtime.ListSessions(ws.ID), 2)

	// Make the worktree dirty so a non-forced delete will be rejected.
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "dirty.txt"),
		[]byte("uncommitted\n"), 0o644,
	))

	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(ctx, &generated.DeleteWorkspaceRequestOptions{PathParams: &generated.DeleteWorkspacePath{ID: ws.ID}, Query: &generated.DeleteWorkspaceQuery{}})
	require.Error(err)
	require.NotNil(delResp)
	require.Equal(http.StatusConflict, delResp.StatusCode)

	// The 409 must not have killed the runtime sessions.
	assert.Len(srv.runtime.ListSessions(ws.ID), 2)

	stopResp, err := client.HTTP.StopWorkspaceRuntimeSessionWithResponse(ctx, &generated.StopWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.StopWorkspaceRuntimeSessionPath{ID: ws.ID, SessionKey: launchResp.JSON200.Key}})
	require.NoError(err)
	require.Equal(http.StatusNoContent, stopResp.StatusCode)

	launchAfterRejectResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(ctx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: ws.ID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{TargetKey: "helper"}})
	require.NoError(err)
	require.Equal(http.StatusOK, launchAfterRejectResp.StatusCode)
	assert.Len(srv.runtime.ListSessions(ws.ID), 2)
}

func pinWorkspaceMergeRequestForReview(
	t *testing.T,
	ctx context.Context,
	database *db.DB,
	ws *generated.WorkspaceResponse,
) string {
	t.Helper()
	headSHA := gitOutput(t, ws.WorktreePath, "rev-parse", "HEAD")
	baseSHA := gitOutput(t, ws.WorktreePath, "rev-parse", "refs/heads/main")
	mr, err := database.GetMergeRequest(
		ctx, "github", "github.com", "acme", "widget", 1,
	)
	require.NoError(t, err)
	require.NotNil(t, mr)
	require.NoError(t, database.UpdatePlatformSHAs(
		ctx, mr.RepoID, mr.Number, headSHA, baseSHA,
	))
	require.NoError(t, database.UpdateDiffSHAs(
		ctx, mr.RepoID, mr.Number, headSHA, baseSHA, baseSHA,
	))
	return headSHA
}

func TestMergeWorkspaceCleanupDeletesWorkspaceAfterConfirmedMerge(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	client, database, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, client)
	headSHA := pinWorkspaceMergeRequestForReview(t, ctx, database, ws)

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/pulls/github/acme/widget/1/merge", map[string]any{
		"commit_title":        "merge title",
		"commit_message":      "merge body",
		"method":              "squash",
		"expected_head_sha":   headSHA,
		"delete_workspace_id": ws.ID,
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var response struct {
		Merged                  bool   `json:"merged"`
		WorkspaceCleanupPending bool   `json:"workspace_cleanup_pending"`
		WorkspaceCleanupWarning string `json:"workspace_cleanup_warning"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&response))
	assert.True(response.Merged)
	assert.True(response.WorkspaceCleanupPending)
	assert.Empty(response.WorkspaceCleanupWarning)
	require.Eventually(func() bool {
		stored, getErr := database.GetWorkspace(ctx, ws.ID)
		return getErr == nil && stored == nil
	}, 5*time.Second, 10*time.Millisecond)
	_, err := os.Stat(ws.WorktreePath)
	assert.ErrorIs(err, os.ErrNotExist)
}

func TestWorkspaceListPrunesMissingTmuxSessionsE2E(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	if testing.Short() {
		t.Skip("workspace e2e tests skipped in short mode")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    printf 'forge-0000000000000001\nforge-0000000000000002-e81d3b0e9d82feaa\n'` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	cfg := &config.Config{
		Tmux: config.Tmux{Command: []string{script}},
	}
	client, database, _, _, _ := setupTestServerWithWorkspacesServerAndEnrichment(t, cfg)
	ctx := context.Background()

	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:           "0000000000000002",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/stale",
		WorktreePath: filepath.Join(dir, "stale"),
		TmuxSession:  "forge-0000000000000002",
		Status:       "ready",
	}))
	runtimeSession := runtimeTmuxSessionNameForTest(
		"0000000000000002", "helper",
	)
	recordRuntimeTmuxSessionForServerTest(
		t, database, "0000000000000002", "", "helper", runtimeSession,
		time.Time{},
	)

	var got generated.WorkspaceResponse
	require.Eventually(func() bool {
		listResp, err := client.HTTP.ListWorkspacesWithResponse(ctx)
		require.NoError(err)
		if listResp.StatusCode != http.StatusOK ||
			listResp.JSON200 == nil || listResp.JSON200.Workspaces == nil ||
			len(listResp.JSON200.Workspaces) != 1 {
			return false
		}
		got = listResp.JSON200.Workspaces[0]
		return got.Status == "error" && got.ErrorMessage != nil
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal("0000000000000002", got.ID)
	assert.Equal("error", got.Status)
	require.NotNil(got.ErrorMessage)
	assert.Contains(*got.ErrorMessage, "tmux session is no longer running")

	stored, err := database.GetWorkspace(ctx, "0000000000000002")
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("error", stored.Status)
	runtimeRows, err := database.ListWorkspaceRuntimeTmuxSessions(
		ctx, "0000000000000002",
	)
	require.NoError(err)
	require.Len(runtimeRows, 1)
	assert.Equal(runtimeSession, runtimeRows[0].TmuxSession)
}

func TestWorkspaceRuntimePlainShellRecordsTmuxSessionE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tmux fixture uses Unix shell semantics")
	}
	require := require.New(t)
	assert := assert.New(t)

	tmuxPath := writeFakeWorkspaceRuntimeTmux(t)
	cfg := &config.Config{
		Tmux: config.Tmux{Command: []string{tmuxPath}},
		Shell: config.Shell{
			Command: serverRuntimeHelperCommand("sleep"),
		},
	}
	client, database, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	shell := launchPlainShellRuntimeSession(t, ctx, client, ws.ID)
	assert.Equal("plain_shell", shell.TargetKey)

	stored, err := database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal(string(localruntime.LaunchTargetPlainShell), stored[0].TargetKey)
	assert.NotEmpty(stored[0].TmuxSession)

	stopResp, err := client.HTTP.StopWorkspaceRuntimeSessionWithResponse(ctx, &generated.StopWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.StopWorkspaceRuntimeSessionPath{ID: ws.ID, SessionKey: shell.Key}})
	require.NoError(err)
	require.Equal(http.StatusNoContent, stopResp.StatusCode)
	stored, err = database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	assert.Empty(stored)
}

func TestWorkspaceRuntimePlainShellRecordFailureCleansCreatedTmuxShellE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tmux fixture uses Unix shell semantics")
	}
	require := require.New(t)

	tmuxPath := writeFakeWorkspaceRuntimeTmux(t)
	attachGate := os.Getenv("KENN_FORGE_FAKE_TMUX_ATTACH_GATE")
	require.NoError(os.WriteFile(attachGate, []byte("wait"), 0o600))
	require.NoError(os.WriteFile(
		os.Getenv("KENN_FORGE_FAKE_TMUX_ATTACH_EXIT"), []byte("1"), 0o600,
	))
	cfg := &config.Config{
		Tmux: config.Tmux{Command: []string{tmuxPath}},
		Shell: config.Shell{
			Command: serverRuntimeHelperCommand("sleep"),
		},
	}
	fixture := setupWorkspaceServerFixture(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)
	stateDir := os.Getenv("KENN_FORGE_FAKE_TMUX_STATE")
	initialStateEntries, err := os.ReadDir(stateDir)
	require.NoError(err)
	initialState := make(map[string]bool, len(initialStateEntries))
	for _, entry := range initialStateEntries {
		initialState[entry.Name()] = true
	}

	tx, err := fixture.database.WriteDB().BeginTx(ctx, &sql.TxOptions{})
	require.NoError(err)
	defer func() { _ = tx.Rollback() }()
	recordCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type launchResult struct {
		response *generated.LaunchWorkspaceRuntimeSessionResp
		err      error
	}
	launchDone := make(chan launchResult, 1)
	go func() {
		response, launchErr := fixture.client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(recordCtx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: ws.ID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: string(localruntime.LaunchTargetPlainShell),
		}})
		launchDone <- launchResult{response: response, err: launchErr}
	}()
	require.Eventually(func() bool {
		entries, readErr := os.ReadDir(stateDir)
		return readErr == nil && len(entries) > len(initialState)
	}, 2*time.Second, 20*time.Millisecond)
	require.Eventually(func() bool {
		return len(fixture.server.runtime.ListSessions(ws.ID)) > 0
	}, 2*time.Second, 20*time.Millisecond)
	require.NoError(os.Remove(attachGate))
	require.Eventually(func() bool {
		return len(fixture.server.runtime.ListSessions(ws.ID)) == 0
	}, 2*time.Second, 20*time.Millisecond)
	cancel()
	var result launchResult
	select {
	case result = <-launchDone:
	case <-time.After(5 * time.Second):
		require.FailNow("runtime launch request did not complete within 5 seconds")
	}
	require.Error(result.err)
	require.NotNil(result.response)
	require.Equal(http.StatusInternalServerError, result.response.StatusCode)
	require.NoError(tx.Rollback())

	require.Eventually(func() bool {
		runtimeResp, runtimeErr := fixture.client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, &generated.GetWorkspaceRuntimeRequestOptions{PathParams: &generated.GetWorkspaceRuntimePath{ID: ws.ID}})
		return runtimeErr == nil &&
			runtimeResp.StatusCode == http.StatusOK &&
			runtimeResp.JSON200 != nil &&
			runtimeResp.JSON200.Sessions != nil &&
			len(runtimeResp.JSON200.Sessions) == 0
	}, 2*time.Second, 20*time.Millisecond)
	require.Eventually(func() bool {
		entries, readErr := os.ReadDir(stateDir)
		if readErr != nil || len(entries) != len(initialState) {
			return false
		}
		for _, entry := range entries {
			if !initialState[entry.Name()] {
				return false
			}
		}
		return true
	}, 2*time.Second, 20*time.Millisecond)
}

func TestWorkspaceRuntimeRestoresTmuxShellAfterRestartE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tmux fixture uses Unix shell semantics")
	}
	require := require.New(t)
	assert := assert.New(t)

	tmuxPath := writeFakeWorkspaceRuntimeTmux(t)
	cfg := &config.Config{
		Tmux: config.Tmux{Command: []string{tmuxPath}},
		Shell: config.Shell{
			Command: serverRuntimeHelperCommand("sleep"),
		},
	}
	fixture := setupWorkspaceServerFixture(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	originalShell := launchPlainShellRuntimeSession(t, ctx, fixture.client, ws.ID)
	stored, err := fixture.database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(stored, 1)
	runtimeRows, err := fixture.database.ListWorkspaceRuntimeSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(runtimeRows, 1)
	assert.Equal("session", runtimeRows[0].Scope)
	require.Eventually(func() bool {
		_, statErr := os.Stat(filepath.Join(
			os.Getenv("KENN_FORGE_FAKE_TMUX_STATE"),
			stored[0].TmuxSession,
		))
		return statErr == nil
	}, 2*time.Second, 20*time.Millisecond)

	serverfake.GracefulShutdown(t, fixture.server)
	stored, err = fixture.database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(stored, 1)
	restarted := New(
		fixture.database, fixture.syncer, nil, "/", cfg,
		ServerOptions{
			Clones:      fixture.clones,
			WorktreeDir: fixture.worktrees,
		},
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, restarted) })
	restartedClient := setupTestClient(t, restarted)
	stored, err = fixture.database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(stored, 1)

	runtimeResp, err := restartedClient.HTTP.GetWorkspaceRuntimeWithResponse(ctx, &generated.GetWorkspaceRuntimeRequestOptions{PathParams: &generated.GetWorkspaceRuntimePath{ID: ws.ID}})
	require.NoError(err)
	require.Equal(http.StatusOK, runtimeResp.StatusCode)
	require.NotNil(runtimeResp.JSON200)
	require.NotNil(runtimeResp.JSON200.Sessions)
	require.Len(runtimeResp.JSON200.Sessions, 1)
	assert.Equal(originalShell.Key, runtimeResp.JSON200.Sessions[0].Key)
	assert.Equal(
		originalShell.CreatedAt,
		runtimeResp.JSON200.Sessions[0].CreatedAt,
	)
	runtimeRows, err = fixture.database.ListWorkspaceRuntimeSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(runtimeRows, 1)
	assert.Equal("session", runtimeRows[0].Scope)

	ts := httptest.NewServer(restarted)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.ID +
		"/runtime/sessions/" + originalShell.Key + "/terminal?cols=80&rows=24"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	workspaceTerminalConnWriteRead(
		t, ctx, conn, "restored-shell\r", "fake-tmux:restored-shell",
	)
}

func TestWorkspaceRuntimeRestoreKeepsStoredTmuxShellWithDifferentOwnerMarkerE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tmux fixture uses Unix shell semantics")
	}
	require := require.New(t)
	assert := assert.New(t)

	tmuxPath := writeFakeWorkspaceRuntimeTmux(t)
	cfg := &config.Config{
		Tmux: config.Tmux{Command: []string{tmuxPath}},
		Shell: config.Shell{
			Command: serverRuntimeHelperCommand("sleep"),
		},
	}
	fixture := setupWorkspaceServerFixture(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	_ = launchPlainShellRuntimeSession(t, ctx, fixture.client, ws.ID)
	stored, err := fixture.database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(stored, 1)
	stateDir := os.Getenv("KENN_FORGE_FAKE_TMUX_STATE")
	sessionPath := filepath.Join(stateDir, stored[0].TmuxSession)
	ownerPath := sessionPath + ".owner"
	require.NoError(os.WriteFile(ownerPath, []byte("attacker-owner\n"), 0o644))

	serverfake.GracefulShutdown(t, fixture.server)
	restarted := New(
		fixture.database, fixture.syncer, nil, "/", cfg,
		ServerOptions{
			Clones:      fixture.clones,
			WorktreeDir: fixture.worktrees,
		},
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, restarted) })
	restartedClient := setupTestClient(t, restarted)

	stored, err = fixture.database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(stored, 1)
	_, err = os.Stat(sessionPath)
	require.NoError(err, "stored tmux session should not be killed")

	runtimeResp, err := restartedClient.HTTP.GetWorkspaceRuntimeWithResponse(ctx, &generated.GetWorkspaceRuntimeRequestOptions{PathParams: &generated.GetWorkspaceRuntimePath{ID: ws.ID}})
	require.NoError(err)
	require.Equal(http.StatusOK, runtimeResp.StatusCode)
	require.NotNil(runtimeResp.JSON200)
	require.NotNil(runtimeResp.JSON200.Sessions)
	require.Len(runtimeResp.JSON200.Sessions, 1)
	assert.Equal(stored[0].SessionKey, runtimeResp.JSON200.Sessions[0].Key)
	assert.Equal(string(localruntime.SessionStatusRunning), runtimeResp.JSON200.Sessions[0].Status)
}

// TestBridgeRuntimeAttachmentOutputClosedEmitsExitFrameBeforeDone pins the
// bridge branch for wrappers where PTY EOF reaches the subscriber before
// cmd.Wait marks the session done. That is the race the real ShellDrawer cares
// about, but it is cleaner to drive it with controlled channels than to depend
// on backend-specific PTY behavior in an e2e helper.

// TestBridgeRuntimeAttachmentSubscriberDropDoesNotEmitExitFrame
// pins the bridge's branch that distinguishes a subscriber drop from
// a real session exit. broadcast closes a subscriber's Output channel
// when its 64-slot buffer fills (slow client); without this branch
// the bridge would emit "exited" on a healthy shell and auto-close
// the drawer in front of a still-running session.
//
// We exercise the bridge directly with an Attachment whose Output is
// pre-closed and whose SessionOutputClosed reports false — exactly
// the post-broadcast-drop state. Constructing that state via real
// PTY traffic would be timing-fragile (it requires saturating the
// TCP send buffer faster than the bridge can drain it), so this is
// a focused unit test on the bridge's branching logic.

// never closed

// Read until close. With the bug present (always-emit on
// outputDone), we'd see a MessageText "exited" frame here.

func workspaceTerminalWriteRead(
	t *testing.T,
	ctx context.Context,
	serverURL string,
	workspaceID string,
	input string,
	needle string,
) {
	t.Helper()

	conn, resp, err := workspaceTerminalDial(ctx, serverURL, workspaceID)
	if err != nil && resp != nil && resp.Body != nil {
		body, readErr := io.ReadAll(resp.Body)
		require.NoError(t, readErr)
		require.NoError(t, err, string(body))
	}
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	workspaceTerminalConnWriteRead(t, ctx, conn, input, needle)
}

func workspaceTerminalConnWriteRead(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
	input string,
	needle string,
) {
	t.Helper()

	require.NoError(t, conn.Write(
		ctx, websocket.MessageBinary, []byte(input),
	))
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var got strings.Builder
	for {
		typ, data, readErr := conn.Read(readCtx)
		if readErr != nil {
			break
		}
		if typ != websocket.MessageBinary {
			continue
		}
		got.WriteString(string(data))
		if strings.Contains(got.String(), needle) {
			return
		}
	}
	require.Contains(t, got.String(), needle)
}

func workspaceTerminalDial(
	ctx context.Context,
	serverURL string,
	workspaceID string,
) (*websocket.Conn, *http.Response, error) {
	return workspaceTerminalDialWithQuery(ctx, serverURL, workspaceID, "")
}

func workspaceTerminalDialWithQuery(
	ctx context.Context,
	serverURL string,
	workspaceID string,
	query string,
) (*websocket.Conn, *http.Response, error) {
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") +
		"/api/v1/workspaces/" + workspaceID + "/terminal"
	if query != "" {
		wsURL += "?" + query
	}
	return websocket.Dial(ctx, wsURL, nil)
}

func TestWorkspaceRuntimeSessionTerminalTmuxBackedWebSocketE2E(
	t *testing.T,
) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available")
	}
	releasePTYSlot := acquirePTYE2ESlot(t)
	t.Cleanup(releasePTYSlot)

	require := require.New(t)
	assert := assert.New(t)
	tmuxCommand := isolatedRealTmuxCommand(t, tmuxPath)
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: serverRuntimeHelperCommand("size-live"),
		}},
		Tmux: config.Tmux{Command: tmuxCommand},
	}
	client, database, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(ctx, &generated.LaunchWorkspaceRuntimeSessionRequestOptions{PathParams: &generated.LaunchWorkspaceRuntimeSessionPath{ID: ws.ID}, Body: &generated.LaunchWorkspaceRuntimeSessionInputBody{
		TargetKey: "helper",
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode)
	require.NotNil(launchResp.JSON200)
	session := launchResp.JSON200
	stored, err := database.ListWorkspaceRuntimeTmuxSessions(ctx, ws.ID)
	require.NoError(err)
	require.Len(stored, 1)
	assert.True(
		isRuntimeTmuxSessionNameForWorkspace(ws.ID, stored[0].TmuxSession),
	)

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.ID +
		"/runtime/sessions/" + session.Key +
		"/terminal?cols=177&rows=41"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	resize, err := json.Marshal(map[string]any{
		"type": "resize",
		"cols": 177,
		"rows": 41,
	})
	require.NoError(err)
	writeResize := func(writeCtx context.Context) error {
		return conn.Write(writeCtx, websocket.MessageText, resize)
	}
	probeSize := func(writeCtx context.Context) error {
		if err := writeResize(writeCtx); err != nil {
			return err
		}
		return conn.Write(writeCtx, websocket.MessageBinary, []byte("probe\n"))
	}
	require.NoError(probeSize(ctx))
	probeCtx, stopProbes := context.WithCancel(ctx)
	probeDone := make(chan struct{})
	probeWriteErr := make(chan error, 1)
	// Tmux applies the attach client's PTY resize asynchronously. Retry on a
	// bounded wall-clock cadence: coupling each retry to a repaint creates a
	// feedback loop that can fill the runtime subscriber buffer and detach an
	// otherwise healthy websocket before the resize settles.
	go func() {
		defer close(probeDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-probeCtx.Done():
				return
			case <-ticker.C:
				if err := probeSize(probeCtx); err != nil {
					probeWriteErr <- err
					return
				}
			}
		}
	}()
	defer func() {
		stopProbes()
		<-probeDone
	}()
	readCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var got strings.Builder
	var terminalReadErr error
	for {
		typ, data, readErr := conn.Read(readCtx)
		if readErr != nil {
			terminalReadErr = readErr
			break
		}
		if typ != websocket.MessageBinary {
			continue
		}
		got.WriteString(string(data))
		// tmux keeps one row for its status line by default, so the
		// pane sees one fewer row than the attached terminal while
		// preserving the requested column count.
		if strings.Contains(got.String(), "size:40:177:probe") {
			return
		}
	}
	if !strings.Contains(got.String(), "size:40:177:probe") {
		var terminalWriteErr error
		select {
		case terminalWriteErr = <-probeWriteErr:
		default:
		}
		terminalOutput := got.String()
		if len(terminalOutput) > 4096 {
			terminalOutput = terminalOutput[len(terminalOutput)-4096:]
		}
		diagnostic := fmt.Sprintf(
			"tmux terminal never observed the resized probe: "+
				"read_err_base64=%s write_err_base64=%s output_base64=%s",
			base64.StdEncoding.EncodeToString(
				fmt.Append(nil, terminalReadErr),
			),
			base64.StdEncoding.EncodeToString(
				fmt.Append(nil, terminalWriteErr),
			),
			base64.StdEncoding.EncodeToString([]byte(terminalOutput)),
		)
		require.NoError(errors.New(diagnostic))
	}
}

func createReadyWorkspace(
	t *testing.T,
	ctx context.Context,
	client *apiclient.Client,
) *generated.WorkspaceResponse {
	t.Helper()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(ctx, &generated.CreateWorkspaceRequestOptions{Body: &generated.CreateWorkspaceInputBody{
		Provider:     "github",
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
		MrNumber:     1,
	}})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, createResp.StatusCode)
	require.NotNil(t, createResp.JSON202)
	return waitForWorkspaceReady(t, ctx, client, createResp.JSON202.ID)
}

func writeFakeWorkspaceRuntimeTmux(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "sessions")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	attachGate := filepath.Join(dir, "attach-gate")
	attachExit := filepath.Join(dir, "attach-exit")
	// The tmux client runs with the non-secret allowlist environment,
	// so control paths are baked into the script; the env vars below
	// only let tests locate the control files.
	t.Setenv("KENN_FORGE_FAKE_TMUX_STATE", stateDir)
	t.Setenv("KENN_FORGE_FAKE_TMUX_ATTACH_GATE", attachGate)
	t.Setenv("KENN_FORGE_FAKE_TMUX_ATTACH_EXIT", attachExit)
	path := filepath.Join(dir, "tmux")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+
		"KENN_FORGE_FAKE_TMUX_STATE="+shellquote.Join(stateDir)+"\n"+
		"KENN_FORGE_FAKE_TMUX_ATTACH_GATE="+shellquote.Join(attachGate)+"\n"+
		"KENN_FORGE_FAKE_TMUX_ATTACH_EXIT="+shellquote.Join(attachExit)+"\n"+
		`set -eu
state_dir="${KENN_FORGE_FAKE_TMUX_STATE:?}"
mkdir -p "$state_dir"
session_arg() {
  session=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -s|-t)
        shift
        session="${1:-}"
        ;;
    esac
    [ "$#" -gt 0 ] && shift || true
  done
  printf '%s' "$session"
}
option_arg() {
  option="$1"
  shift
  value=""
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "$option" ]; then
      shift
      value="${1:-}"
      break
    fi
    [ "$#" -gt 0 ] && shift || true
  done
  printf '%s' "$value"
}
cmd="${1:-}"
[ "$#" -gt 0 ] && shift || true
if [ "$cmd" = "-u" ]; then
  cmd="${1:-}"
  [ "$#" -gt 0 ] && shift || true
fi
case "$cmd" in
  list-sessions)
    for session in "$state_dir"/*; do
      [ -e "$session" ] || continue
      case "$session" in
        *.owner|*.launch) continue ;;
      esac
      basename "$session"
    done
    ;;
  has-session)
    session="$(session_arg "$@")"
    if [ -n "$session" ] && [ -e "$state_dir/$session" ]; then
      exit 0
    fi
    printf "can't find session: %s\n" "$session" >&2
    exit 1
    ;;
  new-session)
    session="$(session_arg "$@")"
    [ -n "$session" ] && : > "$state_dir/$session"
    owner="$(option_arg @forge_owner "$@")"
    if [ -n "$session" ] && [ -n "$owner" ]; then
      printf '%s\n' "$owner" > "$state_dir/$session.owner"
    fi
    launch="$(option_arg @forge_launch "$@")"
    if [ -n "$session" ] && [ -n "$launch" ]; then
      printf '%s\n' "$launch" > "$state_dir/$session.launch"
    fi
    ;;
  set-option)
    session="$(session_arg "$@")"
    owner="$(option_arg @forge_owner "$@")"
    if [ -n "$session" ] && [ -n "$owner" ]; then
      printf '%s\n' "$owner" > "$state_dir/$session.owner"
    fi
    launch="$(option_arg @forge_launch "$@")"
    if [ -n "$session" ] && [ -n "$launch" ]; then
      printf '%s\n' "$launch" > "$state_dir/$session.launch"
    fi
    ;;
  show-option)
    ;;
  show-options)
    session="$(session_arg "$@")"
    requested=""
    for arg in "$@"; do requested="$arg"; done
    if [ "$requested" = "@forge_launch" ]; then
      if [ -n "$session" ] && [ -e "$state_dir/$session.launch" ]; then
        cat "$state_dir/$session.launch"
      fi
    elif [ -n "$session" ] && [ -e "$state_dir/$session.owner" ]; then
      cat "$state_dir/$session.owner"
    else
      printf '%s\n' "${KENN_FORGE_FAKE_TMUX_OWNER:-kenn-forge:test-owner}"
    fi
    ;;
  attach-session)
    session="$(session_arg "$@")"
    if [ -z "$session" ] || [ ! -e "$state_dir/$session" ]; then
      printf "can't find session: %s\n" "$session" >&2
      exit 1
    fi
    printf 'attached:%s\n' "$session"
    while [ -e "$KENN_FORGE_FAKE_TMUX_ATTACH_GATE" ]; do
      sleep 0.01
    done
    if [ -e "$KENN_FORGE_FAKE_TMUX_ATTACH_EXIT" ]; then
      exit 0
    fi
    while [ -e "$state_dir/$session" ]; do
      if IFS= read -r line; then
        printf 'fake-tmux:%s\n' "$line"
      else
        sleep 0.05
      fi
    done
    ;;
  kill-session)
    session="$(session_arg "$@")"
    [ -n "$session" ] && rm -f "$state_dir/$session"
    [ -n "$session" ] && rm -f "$state_dir/$session.owner"
    [ -n "$session" ] && rm -f "$state_dir/$session.launch"
    ;;
  capture-pane|list-clients|refresh-client|wait-for)
    ;;
  *)
    printf 'unsupported fake tmux command: %s\n' "$cmd" >&2
    exit 2
    ;;
esac
`), 0o755))
	return path
}

func isolatedRealTmuxCommandIfAvailable(t *testing.T) []string {
	t.Helper()
	if !testtmux.Supported() {
		return nil
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return nil
	}
	return isolatedRealTmuxCommand(t, tmuxPath)
}

func isolatedRealTmuxCommand(t *testing.T, tmuxPath string) []string {
	t.Helper()
	if !testtmux.Supported() {
		t.Skip("real tmux tests require Unix")
	}
	return serverfake.PrivateTmuxOwner.Command(t, tmuxPath)
}

func serverRuntimeHelperCommand(mode string) []string {
	return []string{
		os.Args[0],
		"-test.run=TestServerRuntimeHelperProcess",
		"--",
		serverfake.ServerRuntimeHelperMarker,
		mode,
	}
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

func TestServerPtyOwnerHelperProcess(t *testing.T) {
	args := os.Args
	sep := slices.Index(args, "--")
	if sep >= 0 {
		args = args[sep+1:]
	}
	if os.Getenv("KENN_FORGE_SERVER_PTY_OWNER_HELPER") != "1" &&
		(len(args) == 0 || args[0] != "pty-owner") {
		return
	}
	if len(args) > 0 && args[0] == "pty-owner" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("test pty-owner", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "", "pty owner state root")
	session := fs.String("session", "", "session name")
	cwd := fs.String("cwd", "", "working directory")
	commandJSON := fs.String("command-json", "", "JSON command argv")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	var command []string
	if *commandJSON != "" {
		if err := json.Unmarshal([]byte(*commandJSON), &command); err != nil {
			os.Exit(2)
		}
	}
	ownerCtx, cancel := testPtyOwnerParentContext(*serverPtyOwnerParentPID)
	defer cancel()
	if ownerCtx.Err() != nil {
		os.Exit(0)
	}
	if err := ptyowner.RunOwner(ownerCtx, ptyowner.Options{
		Root:    *root,
		Session: *session,
		Cwd:     *cwd,
		Command: command,
	}); err != nil && !errors.Is(err, context.Canceled) {
		os.Exit(1)
	}
	os.Exit(0)
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v failed: %s%s", args, out, stderr)
	return strings.TrimSpace(string(out))
}

type rawWorkspaceStatusResponse struct {
	ID                 string                    `json:"id"`
	PlatformHost       string                    `json:"platform_host"`
	RepoOwner          string                    `json:"repo_owner"`
	RepoName           string                    `json:"repo_name"`
	ItemType           string                    `json:"item_type"`
	ItemNumber         int                       `json:"item_number"`
	ItemKey            string                    `json:"item_key"`
	GitHeadRef         string                    `json:"git_head_ref"`
	WorktreePath       string                    `json:"worktree_path"`
	TmuxSession        string                    `json:"tmux_session"`
	Status             string                    `json:"status"`
	ErrorMessage       *string                   `json:"error_message"`
	AssociatedPRNumber *int                      `json:"associated_pr_number"`
	Kata               *db.WorkspaceKataMetadata `json:"kata"`
}

type rawIssueWorkspaceRef struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type rawIssueSummary struct {
	Title string `json:"title"`
	State string `json:"state"`
}

type rawIssueDetailResponse struct {
	Issue        *rawIssueSummary      `json:"issue"`
	PlatformHost string                `json:"platform_host"`
	RepoOwner    string                `json:"repo_owner"`
	RepoName     string                `json:"repo_name"`
	Workspace    *rawIssueWorkspaceRef `json:"workspace"`
}

func TestWorkspaceManualRefreshDiscoversAndSyncsAssociatedPR(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	var headRef string
	var headSHA string
	now := time.Now().UTC().Truncate(time.Second)
	issueID := int64(7001)
	prID := int64(42001)
	issueTitle := "Track workspace association"
	issueState := "open"
	issueBody := "issue body"
	issueURL := "https://github.com/acme/widget/issues/7"
	prTitleFromList := "Indexed PR title"
	prTitleFromDetail := "Fresh PR detail title"
	prState := "open"
	prBody := "fresh body"
	prURL := "https://github.com/acme/widget/pull/42"
	baseRef := "main"
	baseSHA := "base-sha"
	author := "alice"
	cloneURL := "https://github.com/acme/widget.git"
	intPointer := func(value int) *int { return &value }
	mock := &serverfake.MockGH{
		GetIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
			return &gh.Issue{
				ID:        &issueID,
				Number:    intPointer(7),
				Title:     &issueTitle,
				Body:      &issueBody,
				State:     &issueState,
				HTMLURL:   &issueURL,
				User:      &gh.User{Login: &author},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
			}, nil
		},
		ListOpenPullRequestsFn: func(context.Context, string, string) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    intPointer(42),
				Title:     &prTitleFromList,
				State:     &prState,
				HTMLURL:   &prURL,
				User:      &gh.User{Login: &author},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref:  &headRef,
					SHA:  &headSHA,
					Repo: &gh.Repository{CloneURL: &cloneURL},
				},
				Base: &gh.PullRequestBranch{Ref: &baseRef, SHA: &baseSHA},
			}}, nil
		},
		GetPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
			return &gh.PullRequest{
				ID:        &prID,
				Number:    intPointer(42),
				Title:     &prTitleFromDetail,
				Body:      &prBody,
				State:     &prState,
				HTMLURL:   &prURL,
				User:      &gh.User{Login: &author},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref:  &headRef,
					SHA:  &headSHA,
					Repo: &gh.Repository{CloneURL: &cloneURL},
				},
				Base: &gh.PullRequestBranch{Ref: &baseRef, SHA: &baseSHA},
			}, nil
		},
	}
	fixture := setupWorkspaceServerFixtureWithMockHostAndOptions(
		t, nil, mock, "github.com",
		ServerOptions{
			PtyOwnerInProcess:                  true,
			DisableWorkspaceBackgroundMonitors: true,
		},
	)

	serverfake.SeedIssue(t, fixture.database, "acme", "widget", 7, "open")
	createRR := testutil.DoJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/issues/gh/acme/widget/7/workspace",
		map[string]string{})

	require.Equal(http.StatusAccepted, createRR.Code, createRR.Body.String())

	var created rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(createRR.Body).Decode(&created))
	ready := waitForWorkspaceReady(t, ctx, fixture.client, created.ID)
	require.NoError(os.WriteFile(
		filepath.Join(ready.WorktreePath, "feature.txt"),
		[]byte("feature\n"),
		0o644,
	))
	gitfixture.Run(t, ready.WorktreePath, "config", "user.email", "test@test.com")
	gitfixture.Run(t, ready.WorktreePath, "config", "user.name", "Test")
	gitfixture.Run(t, ready.WorktreePath, "add", ".")
	gitfixture.Run(t, ready.WorktreePath, "commit", "-m", "feature commit")
	gitfixture.Run(t, ready.WorktreePath, "push", "-u", "origin", ready.GitHeadRef)
	gitfixture.Run(
		t, ready.WorktreePath,
		"remote", "set-url", "origin", "git@github.com:acme/widget.git",
	)
	headRef = ready.GitHeadRef
	headSHA = gitfixture.SHA(t, ready.WorktreePath, "HEAD")

	refreshRR := testutil.DoJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/workspaces/"+created.ID+"/refresh",
		nil)

	require.Equal(http.StatusOK, refreshRR.Code, refreshRR.Body.String())

	var refreshed rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(refreshRR.Body).Decode(&refreshed))
	require.NotNil(refreshed.AssociatedPRNumber)
	assert.Equal(42, *refreshed.AssociatedPRNumber)

	stored, err := fixture.database.GetWorkspace(ctx, created.ID)
	require.NoError(err)
	require.NotNil(stored)
	require.NotNil(stored.AssociatedPRNumber)
	assert.Equal(42, *stored.AssociatedPRNumber)

	repo, err := fixture.database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	pr, err := fixture.database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 42)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal(prTitleFromDetail, pr.Title)
	assert.Equal(headSHA, pr.PlatformHeadSHA)
}

func TestKataWorkspaceManualRefreshDiscoversAndSyncsAssociatedPR(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	const headRef = "kata-task-1"
	var headSHA string
	now := time.Now().UTC().Truncate(time.Second)
	prID := int64(42001)
	prTitleFromList := "Indexed PR title"
	prTitleFromDetail := "Fresh PR detail title"
	prState := "open"
	prBody := "fresh body"
	prURL := "https://github.com/acme/widget/pull/42"
	baseRef := "main"
	baseSHA := "base-sha"
	author := "alice"
	cloneURL := "https://github.com/acme/widget.git"
	intPointer := func(value int) *int { return &value }
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(context.Context, string, string) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    intPointer(42),
				Title:     &prTitleFromList,
				State:     &prState,
				HTMLURL:   &prURL,
				User:      &gh.User{Login: &author},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref:  new(headRef),
					SHA:  &headSHA,
					Repo: &gh.Repository{CloneURL: &cloneURL},
				},
				Base: &gh.PullRequestBranch{Ref: &baseRef, SHA: &baseSHA},
			}}, nil
		},
		GetPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
			return &gh.PullRequest{
				ID:        &prID,
				Number:    intPointer(42),
				Title:     &prTitleFromDetail,
				Body:      &prBody,
				State:     &prState,
				HTMLURL:   &prURL,
				User:      &gh.User{Login: &author},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref:  new(headRef),
					SHA:  &headSHA,
					Repo: &gh.Repository{CloneURL: &cloneURL},
				},
				Base: &gh.PullRequestBranch{Ref: &baseRef, SHA: &baseSHA},
			}, nil
		},
	}
	fixture := setupWorkspaceServerFixtureWithMockHostAndOptions(
		t, nil, mock, "github.com",
		ServerOptions{
			PtyOwnerInProcess:                  true,
			DisableWorkspaceBackgroundMonitors: true,
		},
	)

	worktreePath := filepath.Join(t.TempDir(), "kata-worktree")
	gitfixture.Run(t, t.TempDir(), "clone", fixture.remote, worktreePath)
	gitfixture.Run(t, worktreePath, "config", "user.email", "test@test.com")
	gitfixture.Run(t, worktreePath, "config", "user.name", "Test")
	gitfixture.Run(t, worktreePath, "checkout", "-b", headRef)
	require.NoError(os.WriteFile(
		filepath.Join(worktreePath, "feature.txt"),
		[]byte("feature\n"),
		0o644,
	))
	gitfixture.Run(t, worktreePath, "add", ".")
	gitfixture.Run(t, worktreePath, "commit", "-m", "feature commit")
	gitfixture.Run(t, worktreePath, "push", "-u", "origin", headRef)
	gitfixture.Run(
		t, worktreePath,
		"remote", "set-url", "origin", "git@github.com:acme/widget.git",
	)
	headSHA = gitfixture.SHA(t, worktreePath, "HEAD")
	kataMetadata := db.WorkspaceKataMetadata{
		DaemonID:   "local",
		ProjectUID: "project-1",
		IssueUID:   "issue-1",
		ShortID:    "task-1",
		Title:      "Track Kata workspace association",
	}
	kataItemKey := db.KataWorkspaceItemKey(kataMetadata)
	require.NoError(fixture.database.InsertWorkspace(ctx, &db.Workspace{
		ID:              "ws-kata-refresh",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypeKataTask,
		ItemKey:         kataItemKey,
		GitHeadRef:      headRef,
		WorkspaceBranch: headRef,
		WorktreePath:    worktreePath,
		TmuxSession:     "kenn-forge-ws-kata-refresh",
		Status:          "ready",
		KataMetadata:    &kataMetadata,
	}))

	refreshRR := testutil.DoJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/workspaces/ws-kata-refresh/refresh",
		nil)

	require.Equal(http.StatusOK, refreshRR.Code, refreshRR.Body.String())

	var refreshed rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(refreshRR.Body).Decode(&refreshed))
	assert.Equal(db.WorkspaceItemTypeKataTask, refreshed.ItemType)
	assert.Equal(kataItemKey, refreshed.ItemKey)
	require.NotNil(refreshed.Kata)
	assert.Equal(kataMetadata, *refreshed.Kata)
	require.NotNil(refreshed.AssociatedPRNumber)
	assert.Equal(42, *refreshed.AssociatedPRNumber)

	stored, err := fixture.database.GetWorkspace(ctx, "ws-kata-refresh")
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal(kataItemKey, stored.ItemKey)
	require.NotNil(stored.KataMetadata)
	assert.Equal(kataMetadata, *stored.KataMetadata)
	require.NotNil(stored.AssociatedPRNumber)
	assert.Equal(42, *stored.AssociatedPRNumber)

	repo, err := fixture.database.GetRepoByIdentity(
		ctx,
		serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	pr, err := fixture.database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 42)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal(prTitleFromDetail, pr.Title)
	assert.Equal(headSHA, pr.PlatformHeadSHA)
}

func TestWorkspaceManualRefreshSkipsRemovedIssueProviderDetail(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	issueNumber := 7
	issueID := int64(7001)
	issueTitle := "Removed issue"
	issueState := "open"
	issueURL := "https://github.com/acme/widget/issues/7"
	prNumber := 1
	prID := int64(1001)
	prTitle := "Seeded pull"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/1"
	author := "alice"
	headRef := "feature"
	baseRef := "main"
	var issueDetailCalls atomic.Int64
	mock := &serverfake.MockGH{
		ListOpenIssuesFn: func(context.Context, string, string) ([]*gh.Issue, error) {
			return []*gh.Issue{{
				ID: &issueID, Number: &issueNumber, Title: &issueTitle,
				State: &issueState, HTMLURL: &issueURL,
				User:      &gh.User{Login: &author},
				CreatedAt: &gh.Timestamp{Time: now}, UpdatedAt: &gh.Timestamp{Time: now},
			}}, nil
		},
		GetIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
			issueDetailCalls.Add(1)
			return nil, errors.New("removed issue must not be fetched")
		},
		ListOpenPullRequestsFn: func(context.Context, string, string) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{{
				ID: &prID, Number: &prNumber, Title: &prTitle,
				State: &prState, HTMLURL: &prURL,
				User:      &gh.User{Login: &author},
				CreatedAt: &gh.Timestamp{Time: now}, UpdatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{Ref: &headRef},
				Base: &gh.PullRequestBranch{Ref: &baseRef},
			}}, nil
		},
	}
	fixture := setupWorkspaceServerFixtureWithMockHostAndOptions(
		t, nil, mock, "github.com",
		ServerOptions{
			PtyOwnerInProcess:                  true,
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	serverfake.SeedIssue(t, fixture.database, "acme", "widget", issueNumber, "open")

	createRR := testutil.DoJSON(
		t, fixture.server, http.MethodPost,
		"/api/v1/issues/gh/acme/widget/7/workspace", map[string]string{})

	require.Equal(http.StatusAccepted, createRR.Code, createRR.Body.String())
	var created rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(createRR.Body).Decode(&created))
	waitForWorkspaceReady(t, ctx, fixture.client, created.ID)

	repo, err := fixture.database.GetRepoByIdentity(
		ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	serverfake.MarkArchiveItemRemovedUpstreamForServerTest(
		t, fixture.database, repo.ID, db.ArchiveItemTypeIssue, issueNumber,
	)
	before := issueDetailCalls.Load()

	refreshRR := testutil.DoJSON(
		t, fixture.server, http.MethodPost,
		"/api/v1/workspaces/"+created.ID+"/refresh", nil)

	require.Equal(http.StatusOK, refreshRR.Code, refreshRR.Body.String())
	require.Equal(before, issueDetailCalls.Load(),
		"manual refresh must not fetch a tombstoned issue")
}

// TestWorkspaceRefreshProceedsThroughIssueScopePartialSyncFailure drives
// POST /workspaces/{id}/refresh through the full router with real SQLite
// while the repo's index sync has an issue-scope partial failure (a seeded
// open issue whose closed-item refresh fails). The refresh must succeed:
// association discovery proceeds, the targeted PR-detail update still runs,
// and the tolerated partial failure stays recorded in repo sync health.
func TestWorkspaceRefreshProceedsThroughIssueScopePartialSyncFailure(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second)
	str := func(v string) *string { return &v }
	prID := int64(1001)
	buildPR := func(number int, title string, updatedAt time.Time) *gh.PullRequest {
		return &gh.PullRequest{
			ID:        &prID,
			Number:    &number,
			Title:     &title,
			State:     str("open"),
			HTMLURL:   str("https://github.com/acme/widget/pull/1"),
			User:      &gh.User{Login: str("alice")},
			CreatedAt: &gh.Timestamp{Time: now.Add(-time.Hour)},
			UpdatedAt: &gh.Timestamp{Time: updatedAt},
			Head:      &gh.PullRequestBranch{Ref: str("feature")},
			Base:      &gh.PullRequestBranch{Ref: str("main")},
		}
	}
	var detailCalls atomic.Int32
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(context.Context, string, string) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{buildPR(1, "list title", now.Add(time.Minute))}, nil
		},
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			detailCalls.Add(1)
			return buildPR(number, "detail title", now.Add(2*time.Minute)), nil
		},
		// The seeded open issue leaves the (empty) open list and its
		// closed-item refresh fails: an issue-scope partial failure on
		// every repo index sync.
		GetIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
			return nil, errors.New("closed issue refresh failed")
		},
	}
	fixture := setupWorkspaceServerFixtureWithMockHostAndOptions(
		t, nil, mock, "github.com",
		ServerOptions{
			PtyOwnerInProcess:                  true,
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	serverfake.SeedIssue(t, fixture.database, "acme", "widget", 8, "open")

	ws := createReadyWorkspace(t, ctx, fixture.client)
	before := detailCalls.Load()

	refreshRR := testutil.DoJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/workspaces/"+ws.ID+"/refresh",
		nil)

	require.Equal(http.StatusOK, refreshRR.Code, refreshRR.Body.String())

	var refreshed rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(refreshRR.Body).Decode(&refreshed))
	assert.Equal(ws.ID, refreshed.ID,
		"the refresh must complete association discovery and return the workspace")

	assert.Greater(detailCalls.Load(), before,
		"the targeted PR-detail refresh must still run")

	repo, err := fixture.database.GetRepoByIdentity(
		ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	assert.NotEmpty(repo.LastSyncError,
		"the tolerated partial failure must stay recorded in sync health")

	pr, err := fixture.database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal("detail title", pr.Title,
		"the targeted PR update must land after the index refresh")
}

// TestWorkspaceRefreshAbortsOnMergeRequestScopePartialSyncFailure is the
// route-level complement of the issue-scope test above: when the repo index
// sync has a merge-request-scope partial failure (a seeded open PR whose
// closed-item refresh fails), POST /workspaces/{id}/refresh must return the
// error envelope instead of success over stale association data — the
// association/PR-detail refresh must not continue — while sync health still
// records the failure.
func TestWorkspaceRefreshAbortsOnMergeRequestScopePartialSyncFailure(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second)
	str := func(v string) *string { return &v }
	prID := int64(1001)
	buildPR := func(number int, title string, updatedAt time.Time) *gh.PullRequest {
		return &gh.PullRequest{
			ID:        &prID,
			Number:    &number,
			Title:     &title,
			State:     str("open"),
			HTMLURL:   str("https://github.com/acme/widget/pull/1"),
			User:      &gh.User{Login: str("alice")},
			CreatedAt: &gh.Timestamp{Time: now.Add(-time.Hour)},
			UpdatedAt: &gh.Timestamp{Time: updatedAt},
			Head:      &gh.PullRequestBranch{Ref: str("feature")},
			Base:      &gh.PullRequestBranch{Ref: str("main")},
		}
	}
	var pr1DetailCalls atomic.Int32
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(context.Context, string, string) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{buildPR(1, "list title", now.Add(3*time.Minute))}, nil
		},
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			// Seeded PR #2 leaves the open list and its closed-item
			// refresh fails: a merge-request-scope partial failure on
			// every repo index sync. PR #1 detail stays healthy.
			if number == 2 {
				return nil, errors.New("closed PR refresh failed")
			}
			pr1DetailCalls.Add(1)
			return buildPR(number, "detail title", now.Add(10*time.Minute)), nil
		},
	}
	fixture := setupWorkspaceServerFixtureWithMockHostAndOptions(
		t, nil, mock, "github.com",
		ServerOptions{
			PtyOwnerInProcess:                  true,
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	serverfake.SeedPR(t, fixture.database, "acme", "widget", 2)

	ws := createReadyWorkspace(t, ctx, fixture.client)

	repo, err := fixture.database.GetRepoByIdentity(
		ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	before, err := fixture.database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 1)
	require.NoError(err)
	require.NotNil(before)
	detailCallsBefore := pr1DetailCalls.Load()

	refreshRR := testutil.DoJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/workspaces/"+ws.ID+"/refresh",
		nil)

	require.Equal(http.StatusBadGateway, refreshRR.Code, refreshRR.Body.String())

	var problem serverfake.RawProblemDetail
	require.NoError(json.NewDecoder(refreshRR.Body).Decode(&problem))
	assert.Equal("upstreamError", problem.Code)
	assert.Contains(problem.Detail, "merge request sync items failed")

	// The association/PR-detail refresh must not have continued.
	assert.Equal(detailCallsBefore, pr1DetailCalls.Load(),
		"the targeted PR-detail refresh must not run after the abort")
	after, err := fixture.database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 1)
	require.NoError(err)
	require.NotNil(after)
	assert.Equal(before.DetailFetchedAt, after.DetailFetchedAt,
		"no detail refresh may land on the workspace PR row")

	refreshedRepo, err := fixture.database.GetRepoByIdentity(
		ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(refreshedRepo)
	assert.NotEmpty(refreshedRepo.LastSyncError,
		"the aborting partial failure must be recorded in sync health")
}

func TestWorkspaceManualRefreshReturnsAssociationInspectionError(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second)
	issueID := int64(7001)
	issueTitle := "Track workspace association"
	issueState := "open"
	issueBody := "issue body"
	issueURL := "https://github.com/acme/widget/issues/7"
	author := "alice"
	intPointer := func(value int) *int { return &value }
	str := func(v string) *string { return &v }
	mock := &serverfake.MockGH{
		GetIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
			return &gh.Issue{
				ID:        &issueID,
				Number:    intPointer(7),
				Title:     &issueTitle,
				Body:      &issueBody,
				State:     &issueState,
				HTMLURL:   &issueURL,
				User:      &gh.User{Login: &author},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
			}, nil
		},
		ListOpenPullRequestsFn: func(context.Context, string, string) ([]*gh.PullRequest, error) {
			return nil, nil
		},
		// The fixture seeds PR #1 as open; with the open list empty,
		// closure detection refreshes it. Serve a valid closed PR so
		// the repo sync cycle stays clean and the refresh reaches the
		// association-inspection failure this test targets.
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(9001)
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				Title:     str("Seeded PR"),
				State:     str("closed"),
				HTMLURL:   str("https://github.com/acme/widget/pull/1"),
				User:      &gh.User{Login: &author},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
				Head:      &gh.PullRequestBranch{Ref: str("feature")},
				Base:      &gh.PullRequestBranch{Ref: str("main")},
			}, nil
		},
	}
	fixture := setupWorkspaceServerFixtureWithMockHostAndOptions(
		t, nil, mock, "github.com",
		ServerOptions{
			PtyOwnerInProcess:                  true,
			DisableWorkspaceBackgroundMonitors: true,
		},
	)

	serverfake.SeedIssue(t, fixture.database, "acme", "widget", 7, "open")
	createRR := testutil.DoJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/issues/gh/acme/widget/7/workspace",
		map[string]string{})

	require.Equal(http.StatusAccepted, createRR.Code, createRR.Body.String())

	var created rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(createRR.Body).Decode(&created))
	ready := waitForWorkspaceReady(t, ctx, fixture.client, created.ID)
	require.NoError(os.RemoveAll(ready.WorktreePath))

	refreshRR := testutil.DoJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/workspaces/"+created.ID+"/refresh",
		nil)

	require.Equal(
		http.StatusInternalServerError, refreshRR.Code, refreshRR.Body.String(),
	)

	var problem serverfake.RawProblemDetail
	require.NoError(json.NewDecoder(refreshRR.Body).Decode(&problem))
	assert.Equal("internalError", problem.Code)
	assert.Contains(problem.Detail, "refresh workspace PR association")
}

func readEventMatching(
	t *testing.T,
	ch <-chan syncevents.RecordedEvent,
	matches func(syncevents.Event) bool,
) syncevents.Event {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case rec := <-ch:
			if matches(rec.Event) {
				return rec.Event
			}
		case <-timeout:
			require.FailNow(t, "timed out waiting for matching event")
		}
	}
}

func TestWorkspaceCreateWithLocalBaseUsesPullRefWhenHeadBranchDeleted(
	t *testing.T,
) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)

	const prNumber = 43
	localRepo, remote, platformHost := setupHTTPWorktreeBaseForServerTest(
		t, "feature",
	)
	wantSHA := gitfixture.SHA(t, localRepo, "refs/remotes/origin/feature")
	gitfixture.Run(
		t, remote, "update-ref",
		fmt.Sprintf("refs/pull/%d/head", prNumber), wantSHA,
	)
	gitfixture.Run(t, remote, "update-ref", "-d", "refs/heads/feature")
	gitfixture.Run(t, remote, "update-server-info")
	gitfixture.Run(t, localRepo, "fetch", "--prune", "origin")

	cfg := &config.Config{Repos: []config.Repo{{
		Platform:         "github",
		PlatformHost:     platformHost,
		Owner:            "acme",
		Name:             "widget",
		WorktreeBasePath: localRepo,
	}}}
	fixture := setupWorkspaceServerFixture(t, cfg)
	ctx := t.Context()
	serverfake.SeedPROnHost(
		t, fixture.database,
		platformHost, "acme", "widget", prNumber,
		withSeedPRHeadRepoCloneURL("https://"+platformHost+"/acme/widget.git"),
	)

	createResp, err := fixture.client.HTTP.CreateWorkspaceWithResponse(ctx, &generated.CreateWorkspaceRequestOptions{Body: &generated.CreateWorkspaceInputBody{
		Provider:     "github",
		PlatformHost: platformHost,
		Owner:        "acme",
		Name:         "widget",
		MrNumber:     prNumber,
	}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode)
	require.NotNil(createResp.JSON202)

	ready := waitForWorkspaceReady(t, ctx, fixture.client, createResp.JSON202.ID)
	stored, err := fixture.database.GetWorkspace(ctx, ready.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("ready", stored.Status)
	assert.Equal(syntheticPRWorktreeBranchForTest(prNumber), stored.WorkspaceBranch)
	assert.Equal(wantSHA, gitfixture.SHA(t, ready.WorktreePath, "HEAD"))
	assert.Equal(
		syntheticPRWorktreeBranchForTest(prNumber),
		gitOutput(t, ready.WorktreePath, "branch", "--show-current"),
	)
}

func TestWorkspaceCreateGitLabUsesSpecificMergeRequestHeadRefE2E(
	t *testing.T,
) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)

	const mrNumber = 57
	const headBranch = "contributor/gitlab-fork"
	localRepo, remote, platformHost := setupHTTPWorktreeBaseForServerTest(
		t, "feature",
	)
	gitfixture.Run(t, localRepo, "checkout", "-b", headBranch, "main")
	require.NoError(os.WriteFile(
		filepath.Join(localRepo, "gitlab-mr.txt"),
		[]byte("gitlab mr head\n"), 0o644,
	))
	gitfixture.Run(t, localRepo, "add", ".")
	gitfixture.Run(t, localRepo, "commit", "-m", "gitlab mr head")
	wantSHA := gitfixture.SHA(t, localRepo, "HEAD")
	gitfixture.Run(
		t, localRepo, "push", remote,
		fmt.Sprintf("HEAD:refs/merge-requests/%d/head", mrNumber),
	)
	gitfixture.Run(t, remote, "update-server-info")

	fixture := setupWorkspaceServerFixtureWithHost(t, nil, platformHost)
	ctx := t.Context()
	repoID, err := fixture.database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       string(platform.KindGitLab),
		PlatformHost:   platformHost,
		PlatformRepoID: "gid://gitlab/Project/57",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	require.NoError(fixture.database.UpdateRepoProviderMetadata(
		ctx, repoID, db.RepoProviderMetadata{
			CloneURL:      "http://" + platformHost + "/acme/widget.git",
			DefaultBranch: "main",
		},
	))
	serverfake.SeedPRForRepo(
		t, fixture.database, repoID, platformHost, "acme", "widget", mrNumber,
		withSeedPRHeadBranch(headBranch),
		withSeedPRHeadRepoCloneURL("http://"+platformHost+"/acme/widget.git"),
	)
	provider := string(platform.KindGitLab)

	createResp, err := fixture.client.HTTP.CreateWorkspaceWithResponse(ctx, &generated.CreateWorkspaceRequestOptions{Body: &generated.CreateWorkspaceInputBody{
		Provider:     provider,
		PlatformHost: platformHost,
		Owner:        "acme",
		Name:         "widget",
		MrNumber:     mrNumber,
	}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode)
	require.NotNil(createResp.JSON202)

	ready := waitForWorkspaceReady(t, ctx, fixture.client, createResp.JSON202.ID)
	require.NotNil(ready.MrHeadRepoKind)
	assert.Equal(generated.WorkspaceResponseMrHeadRepoKindSameRepo, *ready.MrHeadRepoKind)
	stored, err := fixture.database.GetWorkspace(ctx, ready.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("ready", stored.Status)
	assert.Equal(syntheticPRWorktreeBranchForTest(mrNumber), stored.WorkspaceBranch)
	assert.Equal(
		syntheticPRWorktreeBranchForTest(mrNumber),
		gitOutput(t, ready.WorktreePath, "branch", "--show-current"),
	)
	assert.Equal(wantSHA, gitfixture.SHA(t, ready.WorktreePath, "HEAD"))
}

func TestWorkspaceCreateReusesExistingWorktreeThroughAPI(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)

	const prNumber = 44
	localRepo, _, platformHost := setupHTTPWorktreeBaseForServerTest(
		t, "feature",
	)
	cfg := &config.Config{Repos: []config.Repo{{
		Platform:         "github",
		PlatformHost:     platformHost,
		Owner:            "acme",
		Name:             "widget",
		WorktreeBasePath: localRepo,
	}}}
	fixture := setupWorkspaceServerFixture(t, cfg)
	ctx := t.Context()
	serverfake.SeedPROnHost(
		t, fixture.database,
		platformHost, "acme", "widget", prNumber,
		withSeedPRHeadRepoCloneURL("https://"+platformHost+"/acme/widget.git"),
	)
	existingBranch := syntheticPRWorktreeBranchForTest(prNumber)
	worktreePath := filepath.Join(
		fixture.worktrees, "github", platformHost, "acme", "widget",
		fmt.Sprintf("pr-%d", prNumber),
	)
	gitfixture.Run(
		t, localRepo,
		"worktree", "add", worktreePath, "-b", existingBranch, "main",
	)
	wantSHA := gitfixture.SHA(t, worktreePath, "HEAD")
	createResp, err := fixture.client.HTTP.CreateWorkspaceWithResponse(ctx, &generated.CreateWorkspaceRequestOptions{Body: &generated.CreateWorkspaceInputBody{
		Provider:     "github",
		PlatformHost: platformHost,
		Owner:        "acme",
		Name:         "widget",
		MrNumber:     prNumber,
	}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode)
	require.NotNil(createResp.JSON202)

	ready := waitForWorkspaceReady(t, ctx, fixture.client, createResp.JSON202.ID)
	stored, err := fixture.database.GetWorkspace(ctx, ready.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("ready", stored.Status)
	assert.Equal(worktreePath, stored.WorktreePath)
	assert.Equal(existingBranch, stored.WorkspaceBranch)
	assert.Equal(worktreePath, ready.WorktreePath)
	assert.Equal(wantSHA, gitfixture.SHA(t, ready.WorktreePath, "HEAD"))
	assert.Equal(existingBranch, gitOutput(t, ready.WorktreePath, "branch", "--show-current"))
}

func TestWorkspaceRetryReusesExistingLocalHeadBranchThroughAPI(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)

	const prNumber = 45
	localRepo, _, platformHost := setupHTTPWorktreeBaseForServerTest(
		t, "feature",
	)
	cfg := &config.Config{Repos: []config.Repo{{
		Platform:         "github",
		PlatformHost:     platformHost,
		Owner:            "acme",
		Name:             "widget",
		WorktreeBasePath: localRepo,
	}}}
	fixture := setupWorkspaceServerFixture(t, cfg)
	ctx := t.Context()
	serverfake.SeedPROnHost(
		t, fixture.database,
		platformHost, "acme", "widget", prNumber,
		withSeedPRHeadRepoCloneURL("https://"+platformHost+"/acme/widget.git"),
	)
	worktreePath := filepath.Join(
		fixture.worktrees, "github", platformHost, "acme", "widget",
		fmt.Sprintf("pr-%d", prNumber),
	)
	gitfixture.Run(
		t, localRepo,
		"worktree", "add", worktreePath,
		"-b", "feature", "refs/remotes/origin/feature",
	)
	wantSHA := gitfixture.SHA(t, worktreePath, "HEAD")
	createResp, err := fixture.client.HTTP.CreateWorkspaceWithResponse(ctx, &generated.CreateWorkspaceRequestOptions{Body: &generated.CreateWorkspaceInputBody{
		Provider:     "github",
		PlatformHost: platformHost,
		Owner:        "acme",
		Name:         "widget",
		MrNumber:     prNumber,
	}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode)
	require.NotNil(createResp.JSON202)
	ready := waitForWorkspaceReady(t, ctx, fixture.client, createResp.JSON202.ID)
	stored, err := fixture.database.GetWorkspace(ctx, ready.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("ready", stored.Status)
	assert.Empty(stored.WorkspaceBranch)
	assert.Equal("feature", gitOutput(t, ready.WorktreePath, "branch", "--show-current"))
	assert.Equal(wantSHA, gitfixture.SHA(t, ready.WorktreePath, "HEAD"))

	msg := "retry existing local base worktree"
	require.NoError(fixture.database.UpdateWorkspaceStatus(
		ctx, ready.ID, "error", &msg,
	))
	retryResp, err := fixture.client.HTTP.RetryWorkspaceWithResponse(ctx, &generated.RetryWorkspaceRequestOptions{PathParams: &generated.RetryWorkspacePath{ID: ready.ID}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, retryResp.StatusCode)
	require.NotNil(retryResp.JSON202)

	retried := waitForWorkspaceReady(t, ctx, fixture.client, ready.ID)
	stored, err = fixture.database.GetWorkspace(ctx, ready.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("ready", stored.Status)
	assert.Empty(stored.WorkspaceBranch)
	assert.Equal(worktreePath, stored.WorktreePath)
	assert.Equal("feature", gitOutput(t, retried.WorktreePath, "branch", "--show-current"))
	assert.Equal(wantSHA, gitfixture.SHA(t, retried.WorktreePath, "HEAD"))

	force := true
	deleteResp, err := fixture.client.HTTP.DeleteWorkspaceWithResponse(ctx, &generated.DeleteWorkspaceRequestOptions{PathParams: &generated.DeleteWorkspacePath{ID: ready.ID}, Query: &generated.DeleteWorkspaceQuery{Force: &force}})
	require.NoError(err)
	require.Equal(http.StatusNoContent, deleteResp.StatusCode)
	assert.Equal(wantSHA, gitfixture.SHA(t, localRepo, "refs/heads/feature"))
}

func syntheticPRWorktreeBranchForTest(mrNumber int) string {
	return fmt.Sprintf("kenn-forge/pr-%d", mrNumber)
}

func setupHTTPWorktreeBaseForServerTest(
	t *testing.T,
	branch string,
) (repo, remote, platformHost string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "acme", "widget.git")
	repo = filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(filepath.Dir(remote), 0o755))
	gitfixture.Run(t, root, "init", "--bare", "--initial-branch=main", remote)
	server := httptest.NewServer(http.FileServer(http.Dir(root)))
	t.Cleanup(server.Close)
	remoteURL := server.URL + "/acme/widget.git"
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	platformHost = parsed.Host

	gitfixture.Run(t, root, "init", "--initial-branch=main", repo)
	gitfixture.Run(t, repo, "config", "user.email", "test@test.com")
	gitfixture.Run(t, repo, "config", "user.name", "Test")
	gitfixture.Run(t, repo, "remote", "add", "origin", remote)
	require.NoError(t, os.WriteFile(
		filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644,
	))
	gitfixture.Run(t, repo, "add", ".")
	gitfixture.Run(t, repo, "commit", "-m", "base commit")
	gitfixture.Run(t, repo, "push", "origin", "HEAD:refs/heads/main")
	gitfixture.Run(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	gitfixture.Run(t, repo, "push", "origin", "HEAD:refs/heads/"+branch)
	gitfixture.Run(t, remote, "update-server-info")
	gitfixture.Run(t, repo, "remote", "set-url", "origin", remoteURL)
	gitfixture.Run(t, repo, "fetch", "--prune", "origin")
	gitfixture.Run(
		t, repo, "symbolic-ref",
		"refs/remotes/origin/HEAD", "refs/remotes/origin/main",
	)
	return repo, remote, platformHost
}

func TestWorkspaceCreatePortQualifiedHostTracksOriginBranchE2E(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	require := require.New(t)
	assert := assert.New(t)

	fixture := setupWorkspaceServerFixtureWithHost(
		t, nil, "ghe.example.com:8443",
	)
	ctx := t.Context()

	headSHA := gitfixture.SHA(t, fixture.remote, "refs/heads/feature")
	gitfixture.Run(t, fixture.remote, "update-ref", "refs/pull/1/head", headSHA)
	_, err := fixture.database.WriteDB().ExecContext(
		ctx,
		`UPDATE forge_merge_requests
		 SET head_repo_clone_url = ?
		 WHERE number = ?`,
		"https://ghe.example.com:8443/acme/widget.git", 1,
	)
	require.NoError(err)

	createResp, err := fixture.client.HTTP.CreateWorkspaceWithResponse(ctx, &generated.CreateWorkspaceRequestOptions{Body: &generated.CreateWorkspaceInputBody{
		Provider:     "github",
		PlatformHost: "ghe.example.com:8443",
		Owner:        "acme",
		Name:         "widget",
		MrNumber:     1,
	}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode)
	require.NotNil(createResp.JSON202)

	ws := waitForWorkspaceReady(t, ctx, fixture.client, createResp.JSON202.ID)
	stored, err := fixture.database.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Nil(stored.MRHeadRepo)
	assert.Equal("feature", gitOutput(t, ws.WorktreePath, "branch", "--show-current"))
	assert.Equal(
		"origin/feature",
		gitOutput(
			t, ws.WorktreePath,
			"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}",
		),
	)
}

func TestWorkspaceDeleteDoesNotCleanupReplacementCloneFromStaleLocalBaseE2E(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)

	assert := assert.New(t)
	require := require.New(t)

	cfg := &config.Config{}
	client, database, _, remotePath, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := t.Context()
	const branch = "kenn-forge/pr-42"
	localRepo := filepath.Join(t.TempDir(), "local-base")
	gitfixture.Run(t, filepath.Dir(localRepo), "clone", remotePath, localRepo)
	replacementClone := filepath.Join(t.TempDir(), "replacement-clone")
	gitfixture.Run(
		t, localRepo,
		"worktree", "add", replacementClone, "-b", branch, "HEAD",
	)
	require.NoError(os.RemoveAll(replacementClone))
	gitfixture.Run(t, filepath.Dir(replacementClone), "clone", remotePath, replacementClone)
	gitfixture.Run(
		t, replacementClone, "remote", "set-url", "origin",
		"https://github.com/acme/widget.git",
	)
	gitfixture.Run(t, replacementClone, "branch", branch, "HEAD")
	branchSHA := gitfixture.SHA(t, replacementClone, "refs/heads/"+branch)

	srv.cfgMu.Lock()
	srv.cfg.Repos = []config.Repo{{
		Platform:         "github",
		PlatformHost:     "github.com",
		Owner:            "acme",
		Name:             "widget",
		WorktreeBasePath: localRepo,
	}}
	srv.cfgMu.Unlock()

	wsID := "ws-stale-local-base-replacement-clone"
	require.NoError(database.InsertWorkspace(ctx, &workspace.Workspace{
		ID:              wsID,
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature",
		WorkspaceBranch: branch,
		WorktreePath:    replacementClone,
		TerminalBackend: workspace.TerminalBackendTmux,
		Status:          "ready",
	}))

	force := true
	deleteResp, err := client.HTTP.DeleteWorkspaceWithResponse(ctx, &generated.DeleteWorkspaceRequestOptions{PathParams: &generated.DeleteWorkspacePath{ID: wsID}, Query: &generated.DeleteWorkspaceQuery{Force: &force}})

	require.NoError(err)
	require.Equal(http.StatusNoContent, deleteResp.StatusCode)
	assert.DirExists(replacementClone)
	assert.Equal(branchSHA, gitfixture.SHA(t, replacementClone, "refs/heads/"+branch))
	got, err := database.GetWorkspace(ctx, wsID)
	require.NoError(err)
	assert.Nil(got)
}

// --- edit-issue-content (PATCH) tests ---
func TestSyncIssueUntrackedRepoReturnsForbidden(t *testing.T) {
	require := require.New(t)

	srv, database, _ := setupTestServerWithMock(t, &serverfake.MockGH{})
	serverfake.SeedIssue(t, database, "acme", "retired", 3, "open")
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SyncIssueWithResponse(t.Context(), &generated.SyncIssueRequestOptions{
		PathParams: &generated.SyncIssuePath{Provider: "gh", Owner: "acme", Name: "retired", Number: int64(3)},
	})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusForbidden, resp.StatusCode, string(resp.Body))
	require.Contains(string(resp.Body), "not tracked")
}

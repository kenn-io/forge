package e2etest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/server/syncevents"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/forge/platform"
)

type githubAsyncMergeUpstream struct {
	mu             sync.Mutex
	mergeableState string
	mergeResults   []string
	mergeRequests  []recordedGitHubAsyncMergeRequest
	checkRunReads  int
	statusReads    int
	pullReads      int
	merged         bool
	pollStarted    chan struct{}
	pollRelease    chan struct{}
	pollStartOnce  sync.Once
}

type recordedGitHubAsyncMergeRequest struct {
	Method     string
	Path       string
	APIVersion string
	Body       string
}

func (u *githubAsyncMergeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/api/v3/repos/acme/widget/pulls/7/merge-async" && r.Method == http.MethodPut:
		u.writeMergeResult(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v3/repos/acme/widget/pulls/7/merge-async/") && r.Method == http.MethodGet:
		u.writeMergeResult(w, r)
	case r.URL.Path == "/api/v3/repos/acme/widget/commits/reviewed-sha/check-runs" && r.Method == http.MethodGet:
		u.mu.Lock()
		u.checkRunReads++
		u.mu.Unlock()
		_, _ = io.WriteString(w, `{
			"total_count":1,
			"check_runs":[{
				"id":1,"name":"build","status":"completed","conclusion":"success",
				"html_url":"https://github.com/acme/widget/actions/runs/1",
				"app":{"name":"GitHub Actions"}
			}]
		}`)
	case r.URL.Path == "/api/v3/repos/acme/widget/commits/reviewed-sha/status" && r.Method == http.MethodGet:
		u.mu.Lock()
		u.statusReads++
		u.mu.Unlock()
		_, _ = io.WriteString(w, `{"state":"success","total_count":0,"statuses":[]}`)
	case r.URL.Path == "/api/v3/repos/acme/widget/pulls/7" && r.Method == http.MethodGet:
		u.mu.Lock()
		u.pullReads++
		merged := u.merged
		mergeableState := u.mergeableState
		u.mu.Unlock()
		// Reflect a delivered terminal merge result the way the real
		// provider does: the canonical post-merge resync reads this
		// detail to record the transition. updated_at must be current so
		// the monotonic snapshot guard accepts the resync.
		stateFields := `"state":"open","mergeable_state":"` + mergeableState + `",`
		if merged {
			stateFields = `"state":"closed","merged":true,
				"merge_commit_sha":"merge-sha",
				"merged_at":"` + time.Now().UTC().Format(time.RFC3339) + `",
				"merged_by":{"login":"merger"},`
		}
		_, _ = io.WriteString(w, `{
			"id":7001,"number":7,`+stateFields+`"title":"Test PR",
			"html_url":"https://github.com/acme/widget/pull/7",
			"user":{"login":"author"},
			"created_at":"2026-08-01T10:00:00Z",
			"updated_at":"`+time.Now().UTC().Format(time.RFC3339)+`",
			"head":{"ref":"feature","sha":"reviewed-sha","repo":{"id":1,"full_name":"acme/widget"}},
			"base":{"ref":"main","sha":"base-sha","repo":{"id":1,"full_name":"acme/widget"}}
		}`)
	default:
		http.NotFound(w, r)
	}
}

func (u *githubAsyncMergeUpstream) writeMergeResult(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	index := len(u.mergeRequests)
	u.mergeRequests = append(u.mergeRequests, recordedGitHubAsyncMergeRequest{
		Method:     r.Method,
		Path:       r.URL.Path,
		APIVersion: r.Header.Get("X-GitHub-Api-Version"),
		Body:       string(body),
	})
	result := u.mergeResults[len(u.mergeResults)-1]
	if index < len(u.mergeResults) {
		result = u.mergeResults[index]
	}
	if strings.Contains(result, `"status":"merged"`) {
		u.merged = true
	}
	u.mu.Unlock()
	if r.Method == http.MethodGet && u.pollStarted != nil {
		u.pollStartOnce.Do(func() { close(u.pollStarted) })
		<-u.pollRelease
	}
	if conflict, ok := strings.CutPrefix(result, "409:"); ok {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, conflict)
		return
	}
	if strings.Contains(result, `"status":"pending"`) {
		w.WriteHeader(http.StatusAccepted)
	}
	_, _ = io.WriteString(w, result)
}

func (u *githubAsyncMergeUpstream) requests() []recordedGitHubAsyncMergeRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]recordedGitHubAsyncMergeRequest(nil), u.mergeRequests...)
}

func setupGitHubAsyncMergeE2E(
	t *testing.T,
	mergeResults ...string,
) (*server.Server, *db.DB, int64, *githubAsyncMergeUpstream) {
	t.Helper()
	fixture := seedGitHubAsyncMergeE2E(t, mergeResults...)
	srv, _ := fixture.startServer(t, server.ServerOptions{})
	return srv, fixture.database, fixture.repoID, fixture.upstream
}

// githubAsyncMergeE2E is a seeded database and fake GitHub that servers can
// be started against, more than once to model a daemon restart.
type githubAsyncMergeE2E struct {
	database *db.DB
	repoID   int64
	upstream *githubAsyncMergeUpstream
	client   ghclient.Client
}

func seedGitHubAsyncMergeE2E(t *testing.T, mergeResults ...string) githubAsyncMergeE2E {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	upstream := &githubAsyncMergeUpstream{mergeResults: mergeResults}
	githubAPI := httptest.NewServer(upstream)
	t.Cleanup(githubAPI.Close)

	client, err := ghclient.NewClient(
		staticTokenSource("token"), "github.com", nil, nil,
		ghclient.WithBaseURLForTesting(githubAPI.URL),
	)
	require.NoError(err)

	database := dbtest.Open(t)
	repoID, err := reposeed.Seed(ctx, database, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Key:          platform.RepositoryIDKey(1),
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://github.com/acme/widget/pull/7",
		Title:           "Test PR",
		Author:          "author",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "reviewed-sha",
		PlatformBaseSHA: "base-sha",
		CIStatus:        "pending",
		CIChecksJSON:    `[{"name":"build","status":"in_progress","conclusion":"","url":"","app":"GitHub Actions"}]`,
		CIHadPending:    true,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)
	require.NoError(database.UpdateDiffSHAs(
		ctx, repoID, 7, "reviewed-sha", "base-sha", "merge-base",
	))

	return githubAsyncMergeE2E{
		database: database, repoID: repoID, upstream: upstream, client: client,
	}
}

func (f githubAsyncMergeE2E) startServer(
	t *testing.T, options server.ServerOptions,
) (*server.Server, *ghclient.Syncer) {
	t.Helper()
	repo := ghclient.RepoRef{
		Platform:     platform.KindGitHub,
		Owner:        "acme",
		Name:         "widget",
		PlatformHost: "github.com",
		RepoPath:     "acme/widget",
		Key:          platform.RepositoryIDKey(1),
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": f.client},
		f.database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	srv := servertest.New(t, f.database, syncer, nil, "/", nil, options)
	return srv, syncer
}

func TestGitHubAsyncMergePersistsOnlyAfterTerminalSuccess(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, repoID, upstream := setupGitHubAsyncMergeE2E(
		t,
		`{"status":"pending","details":{"message":"Merge request is in progress.","uuid":"operation-id"}}`,
		`{"status":"merged","details":{"message":"Pull request merged.","sha":"merge-sha"}}`,
	)
	upstream.pollStarted = make(chan struct{})
	upstream.pollRelease = make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-upstream.pollRelease:
		default:
			close(upstream.pollRelease)
		}
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge",
		strings.NewReader(`{"method":"squash","commit_title":"Merge title","commit_message":"Merge body","expected_head_sha":"reviewed-sha"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	requestDone := make(chan struct{})
	go func() {
		srv.ServeHTTP(rr, req)
		close(requestDone)
	}()

	select {
	case <-upstream.pollStarted:
	case <-time.After(3 * time.Second):
		require.FailNow("timed out waiting for asynchronous merge poll")
	}
	pending, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(pending)
	assert.Equal(db.MergeRequestStateOpen, pending.State,
		"a pending GitHub operation must not be persisted as merged")
	close(upstream.pollRelease)
	select {
	case <-requestDone:
	case <-time.After(3 * time.Second):
		require.FailNow("timed out waiting for asynchronous merge completion")
	}

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var result struct {
		Merged bool   `json:"merged"`
		SHA    string `json:"sha"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&result))
	assert.True(result.Merged)
	assert.Equal("merge-sha", result.SHA)

	requests := upstream.requests()
	require.Len(requests, 2)
	assert.Equal(http.MethodPut, requests[0].Method)
	assert.Equal(http.MethodGet, requests[1].Method)
	assert.Equal("2026-03-10", requests[0].APIVersion)
	assert.Contains(requests[0].Body, `"merge_action":"direct_merge"`)
	assert.Contains(requests[0].Body, `"sha":"reviewed-sha"`)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(db.MergeRequestStateMerged, mr.State)
}

func TestGitHubAsyncMergeFailureLeavesPullRequestOpen(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, repoID, _ := setupGitHubAsyncMergeE2E(
		t,
		`{"status":"failed","details":{"message":"The stack must be rebased before it can be merged."}}`,
	)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge",
		json.RawMessage(`{"method":"merge","commit_title":"Merge title","commit_message":"Merge body","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	var problem conflictProblemBody
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("conflict", problem.Code)
	assert.Equal("The stack must be rebased before it can be merged.", problem.Detail)
	assert.Equal("conflict", problem.Details["reason"])

	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(db.MergeRequestStateOpen, mr.State)
}

func TestGitHubDeferredMergeUsesAsyncAPIAndPersistsTerminalSuccess(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, repoID, upstream := setupGitHubAsyncMergeE2E(
		t,
		`{"status":"pending","details":{"message":"Merge request is in progress.","uuid":"operation-id"}}`,
		`{"status":"merged","details":{"message":"Pull request merged.","sha":"merge-sha"}}`,
	)
	eventCtx, cancelEvents := context.WithCancel(t.Context())
	t.Cleanup(cancelEvents)
	events, _ := srv.Hub().Subscribe(eventCtx, false)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge/deferred",
		json.RawMessage(`{"method":"squash","commit_title":"Merge title","commit_message":"Merge body","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())

	completed := waitForDeferredMergeCompleted(t, events)
	assert.Equal("merged", completed.Status)
	assert.True(completed.Merged)
	assert.Equal("merge-sha", completed.SHA)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(db.MergeRequestStateMerged, mr.State)

	requests := upstream.requests()
	require.Len(requests, 2)
	assert.Equal(http.MethodPut, requests[0].Method)
	assert.Equal(http.MethodGet, requests[1].Method)
	upstream.mu.Lock()
	assert.Equal(1, upstream.checkRunReads)
	assert.Equal(1, upstream.statusReads)
	assert.Positive(upstream.pullReads)
	upstream.mu.Unlock()
}

// seedMergeQueue records that sync saw pull request 7's base branch with or
// without a merge queue.
func seedMergeQueue(t *testing.T, database *db.DB, repoID int64, enabled bool) {
	t.Helper()
	_, err := database.UpsertMergeQueueState(t.Context(), repoID, db.MergeQueueState{
		Number:      7,
		BaseBranch:  "main",
		Enabled:     enabled,
		MergeMethod: "squash",
		ObservedAt:  time.Now().UTC(),
	})
	require.NoError(t, err)
}

func TestGitHubMergeIntoMergeQueueBranchEnqueues(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, repoID, upstream := setupGitHubAsyncMergeE2E(
		t,
		`{"status":"pending","details":{"message":"Merge request is in progress.","uuid":"operation-id"}}`,
		`{"status":"enqueued","details":{"message":"Pull request was added to the merge queue."}}`,
	)
	seedMergeQueue(t, database, repoID, true)

	detail := doJSONRequest(t, srv, http.MethodGet,
		"/api/v1/pulls/github/acme/widget/7", nil,
	)
	require.Equal(http.StatusOK, detail.Code, detail.Body.String())
	var detailBody struct {
		MergeQueueTarget *struct {
			Branch      string `json:"branch"`
			MergeMethod string `json:"merge_method"`
		} `json:"merge_queue_target"`
	}
	require.NoError(json.NewDecoder(detail.Body).Decode(&detailBody))
	require.NotNil(detailBody.MergeQueueTarget)
	assert.Equal("main", detailBody.MergeQueueTarget.Branch)
	assert.Equal("squash", detailBody.MergeQueueTarget.MergeMethod)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge",
		json.RawMessage(`{"method":"squash","commit_title":"Merge title","commit_message":"Merge body","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var result struct {
		Merged  bool   `json:"merged"`
		Queued  bool   `json:"queued"`
		Message string `json:"message"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&result))
	assert.False(result.Merged)
	assert.True(result.Queued)
	assert.Equal("Pull request was added to the merge queue.", result.Message)

	requests := upstream.requests()
	require.Len(requests, 2)
	var body map[string]any
	require.NoError(json.Unmarshal([]byte(requests[0].Body), &body))
	assert.Equal(map[string]any{
		"merge_action": "merge_queue",
		"sha":          "reviewed-sha",
	}, body, "the queue merges with its own method, so GitHub accepts no direct-merge parameters")

	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(db.MergeRequestStateOpen, mr.State)

	// No queue read ran after the enqueue, so Forge records the pull request
	// as queued at an unknown position until sync reads it.
	state, err := database.GetMergeQueueState(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(state)
	assert.True(state.Queued)
	assert.Equal(0, state.Position)
}

func TestGitHubMergeIntoBranchWithoutQueueStaysDirect(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, repoID, upstream := setupGitHubAsyncMergeE2E(
		t,
		`{"status":"merged","details":{"message":"Pull request merged.","sha":"merge-sha"}}`,
	)
	seedMergeQueue(t, database, repoID, false)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge",
		json.RawMessage(`{"method":"squash","commit_title":"Merge title","commit_message":"Merge body","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	requests := upstream.requests()
	require.Len(requests, 1)
	assert.Contains(requests[0].Body, `"merge_action":"direct_merge"`)
	assert.Contains(requests[0].Body, `"merge_method":"squash"`)
}

func TestGitHubMergeSendsTheBypassChoiceToGitHub(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	for _, consent := range []bool{false, true} {
		t.Run(fmt.Sprintf("bypass ticked %t", consent), func(t *testing.T) {
			require := require.New(t)
			srv, database, repoID, upstream := setupGitHubAsyncMergeE2E(
				t,
				`{"status":"merged","details":{"message":"Pull request merged.","sha":"merge-sha"}}`,
			)
			seedMergeQueue(t, database, repoID, false)

			rr := doJSONRequest(t, srv, http.MethodPost,
				"/api/v1/pulls/github/acme/widget/7/merge",
				json.RawMessage(fmt.Sprintf(
					`{"method":"squash","commit_title":"Merge title","commit_message":"Merge body","expected_head_sha":"reviewed-sha","bypass_rules":%t}`,
					consent)),
			)
			require.Equal(http.StatusOK, rr.Code, rr.Body.String())

			requests := upstream.requests()
			require.Len(requests, 1)
			// GitHub merges over rules the merging credential may bypass only
			// when asked, so the user's choice must reach it unchanged.
			require.Equal(consent, strings.Contains(requests[0].Body, `"bypass_rules":true`), requests[0].Body)
		})
	}
}

func TestGitHubDeferredMergeIntoMergeQueueReportsQueued(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, repoID, upstream := setupGitHubAsyncMergeE2E(
		t,
		`{"status":"enqueued","details":{"message":"Pull request was added to the merge queue."}}`,
	)
	seedMergeQueue(t, database, repoID, true)
	eventCtx, cancelEvents := context.WithCancel(t.Context())
	t.Cleanup(cancelEvents)
	events, _ := srv.Hub().Subscribe(eventCtx, false)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge/deferred",
		json.RawMessage(`{"method":"squash","commit_title":"Merge title","commit_message":"Merge body","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())

	completed := waitForDeferredMergeCompleted(t, events)
	assert.Equal("queued", completed.Status)
	assert.False(completed.Merged)

	// Clients refresh detail as soon as the completion arrives; that read
	// must no longer report a pending deferred merge.
	detail := doJSONRequest(t, srv, http.MethodGet, "/api/v1/pulls/github/acme/widget/7", nil)
	require.Equal(http.StatusOK, detail.Code, detail.Body.String())
	var detailBody struct {
		DeferredMergePending bool `json:"deferred_merge_pending"`
	}
	require.NoError(json.NewDecoder(detail.Body).Decode(&detailBody))
	assert.False(detailBody.DeferredMergePending)

	requests := upstream.requests()
	require.Len(requests, 1)
	assert.Contains(requests[0].Body, `"merge_action":"merge_queue"`)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(db.MergeRequestStateOpen, mr.State)
}

func TestPullResponsesShowMergeQueueStateAndRemoval(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, repoID, _ := setupGitHubAsyncMergeE2E(t, `{"status":"failed","details":{}}`)
	enqueuedAt := time.Date(2026, time.October, 14, 19, 5, 15, 0, time.UTC)
	_, err := database.UpsertMergeQueueState(t.Context(), repoID, db.MergeQueueState{
		Number: 7, BaseBranch: "main", Enabled: true, MergeMethod: "squash",
		Queued: true, Position: 2, State: "AWAITING_CHECKS", EnqueuedAt: &enqueuedAt,
		ObservedAt: enqueuedAt.Add(time.Minute),
	})
	require.NoError(err)

	type queueFields struct {
		MergeQueue *struct {
			Position   int    `json:"position"`
			State      string `json:"state"`
			EnqueuedAt string `json:"enqueued_at"`
		} `json:"merge_queue"`
		MergeQueueRemoval *struct {
			RemovedAt string `json:"removed_at"`
			Reason    string `json:"reason"`
		} `json:"merge_queue_removal"`
	}
	list := doJSONRequest(t, srv, http.MethodGet, "/api/v1/pulls?state=open", nil)
	require.Equal(http.StatusOK, list.Code, list.Body.String())
	var rows []queueFields
	require.NoError(json.NewDecoder(list.Body).Decode(&rows))
	require.Len(rows, 1)
	require.NotNil(rows[0].MergeQueue)
	assert.Equal(2, rows[0].MergeQueue.Position)
	assert.Equal("awaiting_checks", rows[0].MergeQueue.State)
	assert.Equal("2026-10-14T19:05:15Z", rows[0].MergeQueue.EnqueuedAt)
	assert.Nil(rows[0].MergeQueueRemoval)

	removedAt := enqueuedAt.Add(52 * time.Second)
	_, err = database.UpsertMergeQueueState(t.Context(), repoID, db.MergeQueueState{
		Number: 7, BaseBranch: "main", Enabled: true,
		RemovedAt: &removedAt, RemovalReason: "branch_protection_failure",
		ObservedAt: enqueuedAt.Add(2 * time.Minute),
	})
	require.NoError(err)

	detail := doJSONRequest(t, srv, http.MethodGet, "/api/v1/pulls/github/acme/widget/7", nil)
	require.Equal(http.StatusOK, detail.Code, detail.Body.String())
	var detailBody queueFields
	require.NoError(json.NewDecoder(detail.Body).Decode(&detailBody))
	assert.Nil(detailBody.MergeQueue)
	require.NotNil(detailBody.MergeQueueRemoval)
	assert.Equal("branch_protection_failure", detailBody.MergeQueueRemoval.Reason)
	assert.Equal("2026-10-14T19:06:07Z", detailBody.MergeQueueRemoval.RemovedAt)
}

func TestGitHubMergeRefusedByMergeQueueRuleEnqueuesInstead(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	// Forge has not observed the queue (its read is forbidden), so it tries a
	// direct merge; GitHub accepts the request and then refuses it by rule.
	srv, database, repoID, upstream := setupGitHubAsyncMergeE2E(
		t,
		`{"status":"pending","details":{"message":"Merge request is in progress.","uuid":"direct-op"}}`,
		`409:{"message":"Repository rule violations found\n\nChanges must be made through the merge queue"}`,
		`{"status":"pending","details":{"message":"Merge request is in progress.","uuid":"queue-op"}}`,
		`{"status":"enqueued","details":{"message":"Pull request was added to the merge queue."}}`,
	)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge",
		json.RawMessage(`{"method":"squash","commit_title":"Merge title","commit_message":"Merge body","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var result struct {
		Merged bool `json:"merged"`
		Queued bool `json:"queued"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&result))
	assert.False(result.Merged)
	assert.True(result.Queued)

	requests := upstream.requests()
	require.Len(requests, 4)
	assert.Contains(requests[0].Body, `"merge_action":"direct_merge"`)
	var enqueueBody map[string]any
	require.NoError(json.Unmarshal([]byte(requests[2].Body), &enqueueBody))
	assert.Equal(map[string]any{"merge_action": "merge_queue", "sha": "reviewed-sha"}, enqueueBody)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(db.MergeRequestStateOpen, mr.State)
}

func TestGitHubEnqueueAfterRefusalShowsPullRequestQueued(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	// Sync last saw no queue on the base branch, so Forge merges directly;
	// GitHub refuses it because the branch now requires the queue.
	srv, database, repoID, _ := setupGitHubAsyncMergeE2E(
		t,
		`{"status":"pending","details":{"message":"Merge request is in progress.","uuid":"direct-op"}}`,
		`409:{"message":"Repository rule violations found\n\nChanges must be made through the merge queue"}`,
		`{"status":"pending","details":{"message":"Merge request is in progress.","uuid":"queue-op"}}`,
		`{"status":"enqueued","details":{"message":"Pull request was added to the merge queue."}}`,
	)
	seedMergeQueue(t, database, repoID, false)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge",
		json.RawMessage(`{"method":"squash","commit_title":"Merge title","commit_message":"Merge body","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	detail := doJSONRequest(t, srv, http.MethodGet, "/api/v1/pulls/github/acme/widget/7", nil)
	require.Equal(http.StatusOK, detail.Code, detail.Body.String())
	var detailBody struct {
		MergeQueue *struct {
			Position int `json:"position"`
		} `json:"merge_queue"`
	}
	require.NoError(json.NewDecoder(detail.Body).Decode(&detailBody))
	require.NotNil(detailBody.MergeQueue, "the pull request shows as queued right after the enqueue")
	assert.Equal(0, detailBody.MergeQueue.Position)
}

func TestRetargetedPullRequestDoesNotOfferOldBaseQueue(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	srv, database, repoID, _ := setupGitHubAsyncMergeE2E(t, `{"status":"failed","details":{}}`)
	// Sync last saw the queue on another base branch.
	_, err := database.UpsertMergeQueueState(t.Context(), repoID, db.MergeQueueState{
		Number: 7, BaseBranch: "release", Enabled: true, MergeMethod: "squash",
		ObservedAt: time.Now().UTC(),
	})
	require.NoError(err)

	detail := doJSONRequest(t, srv, http.MethodGet, "/api/v1/pulls/github/acme/widget/7", nil)
	require.Equal(http.StatusOK, detail.Code, detail.Body.String())
	var detailBody struct {
		MergeQueueTarget *struct{} `json:"merge_queue_target"`
	}
	require.NoError(json.NewDecoder(detail.Body).Decode(&detailBody))
	require.Nil(detailBody.MergeQueueTarget, "the UI must not offer a queue the merge will not use")
}

func TestPullResponsesCarryTheMergeCredentialsRules(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, repoID, _ := setupGitHubAsyncMergeE2E(t)
	ctx := t.Context()

	resync := func(at time.Time) {
		t.Helper()
		mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
		require.NoError(err)
		require.NotNil(mr)
		mr.MergeableState = "blocked"
		mr.MergeRules = "" // provider snapshots never carry the merge credential's rules
		mr.UpdatedAt = at
		_, err = database.UpsertMergeRequest(ctx, mr)
		require.NoError(err)
	}
	type rulesFields struct {
		MergeRules string `json:"merge_rules"`
	}
	detailRules := func() string {
		t.Helper()
		detail := doJSONRequest(t, srv, http.MethodGet, "/api/v1/pulls/github/acme/widget/7", nil)
		require.Equal(http.StatusOK, detail.Code, detail.Body.String())
		var body struct {
			MergeRequest rulesFields `json:"merge_request"`
		}
		require.NoError(json.NewDecoder(detail.Body).Decode(&body))
		return body.MergeRequest.MergeRules
	}

	now := time.Now().UTC().Truncate(time.Second)
	resync(now.Add(time.Second))
	assert.Empty(detailRules(), "rules nobody read as the merge credential are unknown")

	require.NoError(database.UpdateMergeRequestMergeRules(ctx, repoID, 7, ghclient.MergeRulesClear))
	resync(now.Add(2 * time.Second))
	assert.Equal(ghclient.MergeRulesClear, detailRules(),
		"the sync credential's pull request sync keeps the merge credential's view")

	list := doJSONRequest(t, srv, http.MethodGet, "/api/v1/pulls?state=open", nil)
	require.Equal(http.StatusOK, list.Code, list.Body.String())
	var rows []rulesFields
	require.NoError(json.NewDecoder(list.Body).Decode(&rows))
	require.Len(rows, 1)
	assert.Equal(ghclient.MergeRulesClear, rows[0].MergeRules, "the list menu needs the rules too")
}

// mergeRulesGraphQL answers merge rules reads for pull request 7 with a fixed
// merge state and counts them.
type mergeRulesGraphQL struct {
	mergeStateStatus string
	canBypass        bool
	reads            atomic.Int32
}

func (g *mergeRulesGraphQL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	if !strings.Contains(string(body), "viewerCanMergeAsAdmin") {
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":null}}}`)
		return
	}
	g.reads.Add(1)
	_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"mergeStateStatus":%q,"viewerCanMergeAsAdmin":%t}}}}`,
		g.mergeStateStatus, g.canBypass)
}

// runSplitAuthDeferredMerge queues a deferred merge on a host whose sync App
// sees pull request 7 blocked while the merge credential sees user, with the
// user's bypass choice, and returns the worker's outcome.
func runSplitAuthDeferredMerge(
	t *testing.T, user *mergeRulesGraphQL, consent bool,
) (pullapi.DeferredMergeCompletedPayload, *githubAsyncMergeUpstream, *mergeRulesGraphQL) {
	t.Helper()
	require := require.New(t)
	fixture := seedGitHubAsyncMergeE2E(t,
		`{"status":"pending","details":{"message":"Merge request is in progress.","uuid":"operation-id"}}`,
		`{"status":"merged","details":{"message":"Pull request merged.","sha":"merge-sha"}}`,
	)
	fixture.upstream.mergeableState = "blocked"
	seedMergeQueue(t, fixture.database, fixture.repoID, false)
	srv, syncer := fixture.startServer(t, server.ServerOptions{})
	app := &mergeRulesGraphQL{mergeStateStatus: "BLOCKED"}
	fetcher := func(handler http.Handler) *ghclient.GraphQLFetcher {
		api := httptest.NewServer(handler)
		t.Cleanup(api.Close)
		//nolint:forbidigo // the test points each credential's fetcher at a fake GraphQL server
		return ghclient.NewGraphQLFetcherWithClient(githubv4.NewEnterpriseClient(api.URL, api.Client()), nil)
	}
	router, err := ghclient.NewHostRouter("github.com", &ghclient.Route{
		Key:           ghclient.RouteKey{Host: "github.com", Owner: "acme"},
		Client:        fixture.client,
		Fetcher:       fetcher(app),
		WriteFetcher:  fetcher(user),
		ReadIdentity:  ghclient.IdentityKey{Host: "github.com", Principal: "app:1"},
		WriteIdentity: ghclient.IdentityKey{Host: "github.com", Principal: "user:7"},
	})
	require.NoError(err)
	syncer.SetGitHubRouters(map[string]*ghclient.HostRouter{"github.com": router})
	eventCtx, cancelEvents := context.WithCancel(t.Context())
	t.Cleanup(cancelEvents)
	events, _ := srv.Hub().Subscribe(eventCtx, false)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge/deferred",
		json.RawMessage(fmt.Sprintf(
			`{"method":"squash","commit_title":"Merge title","commit_message":"Merge body","expected_head_sha":"reviewed-sha","bypass_rules":%t}`,
			consent)),
	)
	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())
	return waitForDeferredMergeCompleted(t, events), fixture.upstream, app
}

func TestSplitAuthDeferredMergeFollowsTheMergeCredentialNotTheSyncApp(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	user := &mergeRulesGraphQL{mergeStateStatus: "CLEAN"}
	completed, upstream, app := runSplitAuthDeferredMerge(t, user, false)

	assert.Equal("merged", completed.Status, completed.Error)
	requests := upstream.requests()
	assert.Len(requests, 2, "the worker merges through the async merge API")
	assert.NotContains(requests[0].Body, "bypass_rules", "nothing to bypass, and the user did not choose to")
	assert.Positive(user.reads.Load(), "the worker reads the rules as the merge credential")
	assert.Zero(app.reads.Load(), "the sync App's view of the rules does not decide the merge")
}

func TestSplitAuthDeferredMergeBypassesTheRulesTheUserChoseToBypass(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	user := &mergeRulesGraphQL{mergeStateStatus: "BLOCKED", canBypass: true}
	completed, upstream, _ := runSplitAuthDeferredMerge(t, user, true)

	assert.Equal("merged", completed.Status, completed.Error)
	requests := upstream.requests()
	assert.Len(requests, 2)
	assert.Contains(requests[0].Body, `"bypass_rules":true`, "GitHub bypasses the rules only when asked")
}

func TestSplitAuthDeferredMergeFailsWhenTheMergeCredentialNeedsABypass(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	user := &mergeRulesGraphQL{mergeStateStatus: "BLOCKED", canBypass: true}
	completed, upstream, _ := runSplitAuthDeferredMerge(t, user, false)

	assert.Equal("failed", completed.Status)
	assert.Equal("merge needs a bypass of branch protection or repository rules; it was not performed", completed.Error)
	assert.Empty(upstream.requests(), "no merge runs over the rules without consent")
}

func TestGitHubDeferredMergeReadsQueueBeforeWaitingOnBlockedPullRequest(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	// A branch that requires a merge queue reports direct merges blocked.
	// Forge has stored no queue state for pull request 7, so the worker must
	// read it instead of waiting for the block to clear.
	fixture := seedGitHubAsyncMergeE2E(t,
		`{"status":"pending","details":{"message":"Merge request is in progress.","uuid":"operation-id"}}`,
		`{"status":"enqueued","details":{"message":"Pull request was added to the merge queue."}}`,
	)
	fixture.upstream.mergeableState = "blocked"
	srv, syncer := fixture.startServer(t, server.ServerOptions{})
	graphQL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "configuration"):
			_, _ = io.WriteString(w, `{"data":{"repository":{"mergeQueue":{"configuration":{"mergeMethod":"SQUASH"}}}}}`)
		case strings.Contains(string(body), "isMergeQueueEnabled"):
			_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":{"state":"OPEN","baseRefName":"main",`+
				`"isMergeQueueEnabled":true,"mergeQueueEntry":null,"latestQueueEvent":{"nodes":[]}}}}}`)
		default:
			_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":null}}}`)
		}
	}))
	t.Cleanup(graphQL.Close)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(
			//nolint:forbidigo // the test points the syncer's fetcher at a fake GraphQL server
			githubv4.NewEnterpriseClient(graphQL.URL, graphQL.Client()), nil,
		),
	})
	eventCtx, cancelEvents := context.WithCancel(t.Context())
	t.Cleanup(cancelEvents)
	events, _ := srv.Hub().Subscribe(eventCtx, false)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge/deferred",
		json.RawMessage(`{"method":"squash","commit_title":"Merge title","commit_message":"Merge body","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())

	completed := waitForDeferredMergeCompleted(t, events)
	require.Equal("queued", completed.Status, completed.Error)
}

// waitForDeferredMergeCompleted returns the next deferred_merge_completed
// payload, failing the test if none arrives within three seconds.
func waitForDeferredMergeCompleted(
	t *testing.T, events <-chan syncevents.RecordedEvent,
) pullapi.DeferredMergeCompletedPayload {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := event.Event.Data.(pullapi.DeferredMergeCompletedPayload)
			require.True(t, ok)
			return payload
		case <-deadline:
			require.FailNow(t, "timed out waiting for deferred merge completion")
		}
	}
}

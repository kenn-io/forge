package e2etest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/internal/testutil/servertest"
	"go.kenn.io/forge/platform"
)

type githubAsyncMergeUpstream struct {
	mu            sync.Mutex
	mergeResults  []string
	mergeRequests []recordedGitHubAsyncMergeRequest
	checkRunReads int
	statusReads   int
	pullReads     int
	merged        bool
	pollStarted   chan struct{}
	pollRelease   chan struct{}
	pollStartOnce sync.Once
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
		u.mu.Unlock()
		// Reflect a delivered terminal merge result the way the real
		// provider does: the canonical post-merge resync reads this
		// detail to record the transition. updated_at must be current so
		// the monotonic snapshot guard accepts the resync.
		stateFields := `"state":"open",`
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

	repo := ghclient.RepoRef{
		Platform:     platform.KindGitHub,
		Owner:        "acme",
		Name:         "widget",
		PlatformHost: "github.com",
		RepoPath:     "acme/widget",
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": client},
		database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	srv := servertest.New(t, database, syncer, nil, "/", nil, server.ServerOptions{})
	return srv, database, repoID, upstream
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

	var completed pullapi.DeferredMergeCompletedPayload
	deadline := time.After(3 * time.Second)
	for completed.Status == "" {
		select {
		case event := <-events:
			if event.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := event.Event.Data.(pullapi.DeferredMergeCompletedPayload)
			require.True(ok)
			completed = payload
		case <-deadline:
			require.FailNow("timed out waiting for deferred merge completion")
		}
	}
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

func seedMergeQueue(t *testing.T, database *db.DB, repoID int64, branch string) {
	t.Helper()
	_, err := database.ReplaceMergeQueue(t.Context(), repoID, db.MergeQueueSnapshot{
		Configured:  true,
		Branch:      branch,
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
	seedMergeQueue(t, database, repoID, "main")

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

	// No queue read succeeded after the enqueue, so Forge tracks the entry
	// provisionally until sync observes its position.
	entry, err := database.GetMergeQueueEntry(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(entry)
	assert.Equal(0, entry.Position)
}

func TestGitHubMergeIntoBranchWithoutQueueStaysDirect(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, repoID, upstream := setupGitHubAsyncMergeE2E(
		t,
		`{"status":"merged","details":{"message":"Pull request merged.","sha":"merge-sha"}}`,
	)
	seedMergeQueue(t, database, repoID, "release")

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

func TestGitHubDeferredMergeIntoMergeQueueReportsQueued(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, repoID, upstream := setupGitHubAsyncMergeE2E(
		t,
		`{"status":"enqueued","details":{"message":"Pull request was added to the merge queue."}}`,
	)
	seedMergeQueue(t, database, repoID, "main")
	eventCtx, cancelEvents := context.WithCancel(t.Context())
	t.Cleanup(cancelEvents)
	events, _ := srv.Hub().Subscribe(eventCtx, false)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge/deferred",
		json.RawMessage(`{"method":"squash","commit_title":"Merge title","commit_message":"Merge body","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())

	var completed pullapi.DeferredMergeCompletedPayload
	deadline := time.After(3 * time.Second)
	for completed.Status == "" {
		select {
		case event := <-events:
			if event.Event.Type != "deferred_merge_completed" {
				continue
			}
			payload, ok := event.Event.Data.(pullapi.DeferredMergeCompletedPayload)
			require.True(ok)
			completed = payload
		case <-deadline:
			require.FailNow("timed out waiting for deferred merge completion")
		}
	}
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
	_, err := database.ReplaceMergeQueue(t.Context(), repoID, db.MergeQueueSnapshot{
		Configured: true, Branch: "main", MergeMethod: "squash",
		Entries: []db.MergeQueueEntry{{
			Number: 7, Position: 2, State: "AWAITING_CHECKS", EnqueuedAt: enqueuedAt,
		}},
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

	_, err = database.ReplaceMergeQueue(t.Context(), repoID, db.MergeQueueSnapshot{
		Configured: true, Branch: "main", MergeMethod: "squash",
		ObservedAt: enqueuedAt.Add(2 * time.Minute),
	})
	require.NoError(err)
	require.NoError(database.ResolveMergeQueueDeparture(t.Context(), repoID, db.MergeQueueRemoval{
		Number: 7, RemovedAt: enqueuedAt.Add(52 * time.Second), Reason: "branch_protection_failure",
	}))

	detail := doJSONRequest(t, srv, http.MethodGet, "/api/v1/pulls/github/acme/widget/7", nil)
	require.Equal(http.StatusOK, detail.Code, detail.Body.String())
	var detailBody queueFields
	require.NoError(json.NewDecoder(detail.Body).Decode(&detailBody))
	assert.Nil(detailBody.MergeQueue)
	require.NotNil(detailBody.MergeQueueRemoval)
	assert.Equal("branch_protection_failure", detailBody.MergeQueueRemoval.Reason)
	assert.Equal("2026-10-14T19:06:07Z", detailBody.MergeQueueRemoval.RemovedAt)
}

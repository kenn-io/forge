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
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	platformgitlab "go.kenn.io/middleman/internal/platform/gitlab"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/tokenauth"
)

const gitlabMutationThreadID = "abc123def456789012345678901234567890abcd"

type staticGitLabTokenSource string

func (s staticGitLabTokenSource) Token(context.Context) (string, error) { return string(s), nil }
func (s staticGitLabTokenSource) Invalidate()                           {}
func (s staticGitLabTokenSource) Descriptor() tokenauth.Descriptor {
	return tokenauth.Descriptor{Key: tokenauth.Key{Platform: "gitlab", Host: "gitlab.com"}}
}

type recordedGitLabRequest struct {
	Method string
	Path   string
	Body   string
}

type gitlabAPIRecorder struct {
	mu       sync.Mutex
	requests []recordedGitLabRequest
}

func (rec *gitlabAPIRecorder) record(r *http.Request) recordedGitLabRequest {
	body, _ := io.ReadAll(r.Body)
	entry := recordedGitLabRequest{
		Method: r.Method,
		Path:   r.URL.EscapedPath(),
		Body:   string(body),
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.requests = append(rec.requests, entry)
	return entry
}

func (rec *gitlabAPIRecorder) find(method, path string) (recordedGitLabRequest, bool) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, request := range rec.requests {
		if request.Method == method && request.Path == path {
			return request, true
		}
	}
	return recordedGitLabRequest{}, false
}

// findEventually polls for a request issued from a background goroutine
// (e.g. the resync triggered after a stale mutation).
func (rec *gitlabAPIRecorder) findEventually(method, path string) bool {
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok := rec.find(method, path); ok {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// setupGitLabMutationServer wires the real GitLab provider client against a
// fake GitLab REST API and seeds a tracked repo with MR 7 (including one
// existing comment, platform id 9001) and issue 11.
func setupGitLabMutationServer(
	t *testing.T,
) (*server.Server, *db.DB, *gitlabAPIRecorder, int64) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	recorder := &gitlabAPIRecorder{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := recorder.record(r)
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.EscapedPath()
		switch {
		case path == "/api/v4/projects/acme%2Fwidget" && r.Method == http.MethodGet:
			writeGitLabJSON(w, `{
				"id": 4242,
				"path": "widget",
				"path_with_namespace": "acme/widget",
				"web_url": "https://gitlab.com/acme/widget",
				"http_url_to_repo": "https://gitlab.com/acme/widget.git",
				"default_branch": "main"
			}`)
		case path == "/api/v4/projects/4242/merge_requests/7" && r.Method == http.MethodGet:
			// updated_at must be current: UpsertMergeRequest discards
			// snapshots older than the stored row.
			writeGitLabJSON(w, `{
				"id": 7001, "iid": 7, "title": "Test MR", "state": "opened",
				"sha": "head-sha",
				"author": {"username": "author"},
				"created_at": "2026-05-01T09:00:00Z",
				"updated_at": "`+time.Now().UTC().Add(time.Minute).Format(time.RFC3339)+`"
			}`)
		case path == "/api/v4/projects/4242/merge_requests/7/discussions" && r.Method == http.MethodGet:
			writeGitLabJSON(w, `[{
				"id": "`+gitlabMutationThreadID+`",
				"notes": [{
					"id": 9100,
					"body": "ship it",
					"author": {"username": "ada"},
					"created_at": "2026-06-01T10:00:00Z"
				}]
			}]`)
		case path == "/api/v4/projects/4242/merge_requests/7/commits" && r.Method == http.MethodGet:
			writeGitLabJSON(w, `[]`)
		case path == "/api/v4/projects/4242/pipelines" && r.Method == http.MethodGet:
			writeGitLabJSON(w, `[]`)
		case path == "/api/v4/projects/4242/merge_requests/7/notes" && r.Method == http.MethodPost:
			writeGitLabJSON(w, `{
				"id": 9100,
				"body": "from e2e",
				"author": {"username": "ada"},
				"created_at": "2026-06-01T10:00:00Z"
			}`)
		case path == "/api/v4/projects/4242/merge_requests/7/notes/9001" && r.Method == http.MethodPut:
			writeGitLabJSON(w, `{
				"id": 9001,
				"body": "edited body",
				"author": {"username": "ada"},
				"created_at": "2026-05-01T10:00:00Z"
			}`)
		case path == "/api/v4/projects/4242/merge_requests/7/merge" && r.Method == http.MethodPut:
			if !strings.Contains(request.Body, `"sha":"head-sha"`) {
				w.WriteHeader(http.StatusConflict)
				writeGitLabJSON(w, `{"message": "SHA does not match HEAD of source branch"}`)
				return
			}
			writeGitLabJSON(w, `{
				"id": 7001, "iid": 7, "state": "merged",
				"squash_commit_sha": "squash-sha", "sha": "head-sha"
			}`)
		case path == "/api/v4/projects/4242/merge_requests/7" && r.Method == http.MethodPut:
			if strings.Contains(request.Body, "state_event") {
				writeGitLabJSON(w, `{"id": 7001, "iid": 7, "title": "Test MR", "state": "closed"}`)
				return
			}
			writeGitLabJSON(w, `{
				"id": 7001, "iid": 7, "title": "Test MR",
				"description": "Updated MR body", "state": "opened"
			}`)
		case path == "/api/v4/projects/4242/merge_requests/7/approve" && r.Method == http.MethodPost:
			if !strings.Contains(request.Body, `"sha":"head-sha"`) {
				w.WriteHeader(http.StatusConflict)
				writeGitLabJSON(w, `{"message": "SHA does not match HEAD of source branch"}`)
				return
			}
			writeGitLabJSON(w, `{"approved": true, "updated_at": "2026-06-01T11:00:00Z"}`)
		case path == "/api/v4/user" && r.Method == http.MethodGet:
			writeGitLabJSON(w, `{"id": 1, "username": "ada"}`)
		case path == "/api/v4/projects/4242/merge_requests/7/discussions/"+gitlabMutationThreadID+"/notes" &&
			r.Method == http.MethodPost:
			writeGitLabJSON(w, `{
				"id": 9200,
				"body": "thread reply",
				"author": {"username": "ada"},
				"created_at": "2026-06-01T12:00:00Z",
				"resolvable": true
			}`)
		case path == "/api/v4/projects/4242/merge_requests/7/discussions/"+gitlabMutationThreadID &&
			r.Method == http.MethodPut:
			writeGitLabJSON(w, `{"id": "`+gitlabMutationThreadID+`", "notes": []}`)
		case path == "/api/v4/projects/4242/issues" && r.Method == http.MethodPost:
			writeGitLabJSON(w, `{
				"id": 8002, "iid": 12,
				"title": "Created issue", "description": "Issue body",
				"state": "opened",
				"web_url": "https://gitlab.com/acme/widget/-/issues/12"
			}`)
		case path == "/api/v4/projects/4242/issues/11/notes" && r.Method == http.MethodPost:
			writeGitLabJSON(w, `{
				"id": 9300,
				"body": "issue comment",
				"author": {"username": "ada"},
				"created_at": "2026-06-01T13:00:00Z"
			}`)
		case path == "/api/v4/projects/4242/issues/11" && r.Method == http.MethodPut:
			if strings.Contains(request.Body, "state_event") {
				writeGitLabJSON(w, `{"id": 8001, "iid": 11, "title": "Issue", "state": "closed"}`)
				return
			}
			writeGitLabJSON(w, `{"id": 8001, "iid": 11, "title": "Issue (edited)", "state": "opened"}`)
		case path == "/api/v4/projects/4242/issues/11/notes/9301" && r.Method == http.MethodPut:
			writeGitLabJSON(w, `{
				"id": 9301,
				"body": "edited issue comment",
				"author": {"username": "ada"},
				"created_at": "2026-05-01T13:00:00Z"
			}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)

	client, err := platformgitlab.NewClient(
		"gitlab.com",
		staticGitLabTokenSource("token"),
		platformgitlab.WithBaseURLForTesting(api.URL+"/api/v4"),
	)
	require.NoError(err)
	registry, err := platform.NewRegistry(client)
	require.NoError(err)

	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "4242",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)

	mrID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://gitlab.com/acme/widget/-/merge_requests/7",
		Title:           "Test MR",
		Author:          "author",
		State:           "open",
		PlatformHeadSHA: "head-sha",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	existingNoteID := int64(9001)
	threadID := gitlabMutationThreadID
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &existingNoteID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "original body",
		CreatedAt:      now,
		DedupeKey:      "gitlab:gitlab.com:acme/widget:mr:7:note:9001",
		ThreadID:       &threadID,
		Resolvable:     true,
	}}))

	issueID, err := database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     8001,
		Number:         11,
		URL:            "https://gitlab.com/acme/widget/-/issues/11",
		Title:          "Issue",
		Author:         "author",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	existingIssueNoteID := int64(9301)
	require.NoError(database.UpsertIssueEvents(ctx, []db.IssueEvent{{
		IssueID:    issueID,
		PlatformID: &existingIssueNoteID,
		EventType:  "issue_comment",
		Author:     "reviewer",
		Body:       "original issue comment",
		CreatedAt:  now,
		DedupeKey:  "gitlab:gitlab.com:acme/widget:issue:11:note:9301",
	}}))

	repo := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "gitlab.com",
		RepoPath:           "acme/widget",
		PlatformRepoID:     4242,
		PlatformExternalID: "4242",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	return srv, database, recorder, repoID
}

func writeGitLabJSON(w http.ResponseWriter, body string) {
	_, _ = io.WriteString(w, body)
}

func doGitLabJSON(
	t *testing.T,
	srv *server.Server,
	method, path string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func TestGitLabMutationCommentPostAndEdit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, recorder, repoID := setupGitLabMutationServer(t)
	ctx := t.Context()

	rr := doGitLabJSON(t, srv, http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/comments",
		`{"body":"from e2e"}`,
	)
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())

	created, ok := recorder.find(http.MethodPost, "/api/v4/projects/4242/merge_requests/7/notes")
	require.True(ok, "fake GitLab API did not receive note creation")
	assert.Contains(created.Body, `"body":"from e2e"`)

	var postResult struct {
		Author string `json:"Author"`
		Body   string `json:"Body"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&postResult))
	assert.Equal("ada", postResult.Author)
	assert.Equal("from e2e", postResult.Body)

	rr = doGitLabJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gitlab/acme/widget/7/comments/9001",
		`{"body":"edited body"}`,
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	edited, ok := recorder.find(http.MethodPut, "/api/v4/projects/4242/merge_requests/7/notes/9001")
	require.True(ok, "fake GitLab API did not receive note edit")
	assert.Contains(edited.Body, `"body":"edited body"`)

	var editResult struct {
		Body string `json:"Body"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&editResult))
	assert.Equal("edited body", editResult.Body)

	// GitLab note responses omit the discussion id; the edit must not
	// detach the stored comment from its thread.
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	events, err := database.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	threadPreserved := false
	for _, event := range events {
		if event.PlatformID != nil && *event.PlatformID == 9001 {
			assert.Equal("edited body", event.Body)
			require.NotNil(event.ThreadID, "thread_id must survive a comment edit")
			assert.Equal(gitlabMutationThreadID, *event.ThreadID)
			threadPreserved = true
		}
	}
	assert.True(threadPreserved, "edited comment event not found")
}

func TestGitLabMutationContentEdits(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, recorder, _ := setupGitLabMutationServer(t)

	rr := doGitLabJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gitlab/acme/widget/7",
		`{"body":"Updated MR body"}`,
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	mrEdit, ok := recorder.find(http.MethodPut, "/api/v4/projects/4242/merge_requests/7")
	require.True(ok, "fake GitLab API did not receive MR content edit")
	assert.Contains(mrEdit.Body, `"description":"Updated MR body"`)
	var prDetail struct {
		MergeRequest struct {
			Body string `json:"Body"`
		} `json:"merge_request"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&prDetail))
	assert.Equal("Updated MR body", prDetail.MergeRequest.Body)

	rr = doGitLabJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gitlab/acme/widget/11",
		`{"title":"Issue (edited)"}`,
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	issueEdit, ok := recorder.find(http.MethodPut, "/api/v4/projects/4242/issues/11")
	require.True(ok, "fake GitLab API did not receive issue content edit")
	assert.Contains(issueEdit.Body, `"title":"Issue (edited)"`)
}

func TestGitLabMutationIssueCommentEdit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, recorder, _ := setupGitLabMutationServer(t)

	rr := doGitLabJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gitlab/acme/widget/11/comments/9301",
		`{"body":"edited issue comment"}`,
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	edited, ok := recorder.find(http.MethodPut, "/api/v4/projects/4242/issues/11/notes/9301")
	require.True(ok, "fake GitLab API did not receive issue note edit")
	assert.Contains(edited.Body, `"body":"edited issue comment"`)
}

func TestGitLabMutationMergeSquash(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, recorder, repoID := setupGitLabMutationServer(t)
	ctx := t.Context()

	rr := doGitLabJSON(t, srv, http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/merge",
		`{"method":"squash","commit_title":"Squash title","commit_message":"Squash body"}`,
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var result struct {
		Merged bool   `json:"merged"`
		SHA    string `json:"sha"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&result))
	assert.True(result.Merged)
	assert.Equal("squash-sha", result.SHA)

	merge, ok := recorder.find(http.MethodPut, "/api/v4/projects/4242/merge_requests/7/merge")
	require.True(ok, "fake GitLab API did not receive merge")
	assert.Contains(merge.Body, `"squash":true`)
	assert.Contains(merge.Body, `"squash_commit_message":"Squash title\n\nSquash body"`)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("merged", string(mr.State))
}

func TestGitLabMutationMergeRebaseReturnsTypedCapabilityError(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, recorder, _ := setupGitLabMutationServer(t)

	rr := doGitLabJSON(t, srv, http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/merge",
		`{"method":"rebase","commit_title":"t","commit_message":"m"}`,
	)
	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())

	var problem struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("unsupportedCapability", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal("merge_method_rebase", problem.Details["capability"])
	assert.Equal("gitlab", problem.Details["provider"])

	_, merged := recorder.find(http.MethodPut, "/api/v4/projects/4242/merge_requests/7/merge")
	assert.False(merged, "rebase must not reach the GitLab merge API")
}

func TestGitLabMutationStateChange(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, recorder, repoID := setupGitLabMutationServer(t)
	ctx := t.Context()

	rr := doGitLabJSON(t, srv, http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/github-state",
		`{"state":"closed"}`,
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	update, ok := recorder.find(http.MethodPut, "/api/v4/projects/4242/merge_requests/7")
	require.True(ok, "fake GitLab API did not receive MR update")
	assert.Contains(update.Body, `"state_event":"close"`)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("closed", string(mr.State))
}

func TestGitLabMutationIssueStateChange(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, recorder, _ := setupGitLabMutationServer(t)

	rr := doGitLabJSON(t, srv, http.MethodPost,
		"/api/v1/issues/gitlab/acme/widget/11/github-state",
		`{"state":"closed"}`,
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	update, ok := recorder.find(http.MethodPut, "/api/v4/projects/4242/issues/11")
	require.True(ok, "fake GitLab API did not receive issue update")
	assert.Contains(update.Body, `"state_event":"close"`)
}

func TestGitLabMutationApprove(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, recorder, repoID := setupGitLabMutationServer(t)
	ctx := t.Context()

	rr := doGitLabJSON(t, srv, http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/approve",
		`{"body":"ship it"}`,
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var result struct {
		Status string `json:"status"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&result))
	assert.Equal("approved", result.Status)

	_, approved := recorder.find(http.MethodPost, "/api/v4/projects/4242/merge_requests/7/approve")
	assert.True(approved, "fake GitLab API did not receive approval")
	note, ok := recorder.find(http.MethodPost, "/api/v4/projects/4242/merge_requests/7/notes")
	require.True(ok, "approval body was not posted as a note")
	assert.Contains(note.Body, `"body":"ship it"`)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	events, err := database.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	reviewSeen := false
	commentSeen := false
	for _, event := range events {
		if event.EventType == "review" {
			reviewSeen = true
			assert.Equal("ada", event.Author)
			assert.Equal("approved", event.Summary)
		}
		if event.EventType == "issue_comment" && event.Body == "ship it" {
			commentSeen = true
		}
	}
	assert.True(reviewSeen, "approval event was not persisted")
	// The approval body lives in an upstream note; the inline sync after
	// approve must make it visible locally right away.
	assert.True(commentSeen, "approval comment was not synced into local events")
}

func TestGitLabMutationApproveStaleHeadReturnsConflict(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, recorder, repoID := setupGitLabMutationServer(t)
	ctx := t.Context()

	// The local head is behind the provider head served by the fake API,
	// mimicking a source-branch push after the user reviewed.
	_, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://gitlab.com/acme/widget/-/merge_requests/7",
		Title:           "Test MR",
		Author:          "author",
		State:           "open",
		PlatformHeadSHA: "stale-sha",
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		LastActivityAt:  time.Now().UTC(),
	})
	require.NoError(err)

	rr := doGitLabJSON(t, srv, http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/approve",
		`{"body":"ship it"}`,
	)
	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	var problem struct {
		Code string `json:"code"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("conflict", problem.Code)

	_, noted := recorder.find(http.MethodPost, "/api/v4/projects/4242/merge_requests/7/notes")
	assert.False(noted, "stale approval must not post the comment")
	_, approved := recorder.find(http.MethodPost, "/api/v4/projects/4242/merge_requests/7/approve")
	assert.False(approved, "stale approval must not reach the approvals API")
}

func TestGitLabMutationMergeStaleHeadReturnsConflict(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, recorder, repoID := setupGitLabMutationServer(t)
	ctx := t.Context()

	_, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://gitlab.com/acme/widget/-/merge_requests/7",
		Title:           "Test MR",
		Author:          "author",
		State:           "open",
		PlatformHeadSHA: "stale-sha",
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		LastActivityAt:  time.Now().UTC(),
	})
	require.NoError(err)

	rr := doGitLabJSON(t, srv, http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/merge",
		`{"method":"squash","commit_title":"t","commit_message":"m"}`,
	)
	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	var problem struct {
		Code string `json:"code"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("conflict", problem.Code)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("open", string(mr.State), "stale merge must not mark the MR merged locally")

	assert.True(
		recorder.findEventually(http.MethodGet, "/api/v4/projects/4242/merge_requests/7"),
		"stale merge must trigger an MR resync",
	)
}

// A legacy or partially synced row with no head SHA must not produce an
// unbound merge: the server refreshes the MR once and merges bound to the
// refreshed head.
func TestGitLabMutationMergeRefreshesMissingHeadSHA(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, recorder, repoID := setupGitLabMutationServer(t)
	ctx := t.Context()

	_, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     7001,
		Number:         7,
		URL:            "https://gitlab.com/acme/widget/-/merge_requests/7",
		Title:          "Test MR",
		Author:         "author",
		State:          "open",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
		LastActivityAt: time.Now().UTC(),
	})
	require.NoError(err)

	rr := doGitLabJSON(t, srv, http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/merge",
		`{"method":"squash","commit_title":"t","commit_message":"m"}`,
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	_, refreshed := recorder.find(http.MethodGet, "/api/v4/projects/4242/merge_requests/7")
	assert.True(refreshed, "missing head SHA must refresh the MR before merging")
	merge, ok := recorder.find(http.MethodPut, "/api/v4/projects/4242/merge_requests/7/merge")
	require.True(ok)
	assert.Contains(merge.Body, `"sha":"head-sha"`, "merge must be bound to the refreshed head")
}

func TestGitLabMutationDiscussionReplyThroughRealClient(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, recorder, _ := setupGitLabMutationServer(t)

	rr := doGitLabJSON(t, srv, http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/discussions/"+gitlabMutationThreadID+"/reply",
		`{"body":"thread reply"}`,
	)
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())

	reply, ok := recorder.find(
		http.MethodPost,
		"/api/v4/projects/4242/merge_requests/7/discussions/"+gitlabMutationThreadID+"/notes",
	)
	require.True(ok, "fake GitLab API did not receive discussion reply")
	assert.Contains(reply.Body, `"body":"thread reply"`)

	var result struct {
		Body     string  `json:"Body"`
		ThreadID *string `json:"ThreadID"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&result))
	assert.Equal("thread reply", result.Body)
	require.NotNil(result.ThreadID)
	assert.Equal(gitlabMutationThreadID, *result.ThreadID)
}

func TestGitLabMutationDiscussionResolveAndUnresolve(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, recorder, repoID := setupGitLabMutationServer(t)
	ctx := t.Context()

	resolvePath := "/api/v1/pulls/gitlab/acme/widget/7/discussions/" +
		gitlabMutationThreadID + "/resolve"

	rr := doGitLabJSON(t, srv, http.MethodPost, resolvePath, `{"resolved":true}`)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	update, ok := recorder.find(
		http.MethodPut,
		"/api/v4/projects/4242/merge_requests/7/discussions/"+gitlabMutationThreadID,
	)
	require.True(ok, "fake GitLab API did not receive discussion resolve")
	assert.Contains(update.Body, `"resolved":true`)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	events, err := database.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.NotEmpty(events)
	resolvedSeen := false
	for _, event := range events {
		if event.ThreadID != nil && *event.ThreadID == gitlabMutationThreadID {
			resolvedSeen = true
			assert.True(event.Resolved)
		}
	}
	assert.True(resolvedSeen, "local discussion events were not marked resolved")

	rr = doGitLabJSON(t, srv, http.MethodPost, resolvePath, `{"resolved":false}`)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	events, err = database.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	for _, event := range events {
		if event.ThreadID != nil && *event.ThreadID == gitlabMutationThreadID {
			assert.False(event.Resolved)
		}
	}
}

func TestGitLabMutationRequestChangesUnsupported(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, _, _, _ := setupGitLabMutationServer(t)

	rr := doGitLabJSON(t, srv, http.MethodPost,
		"/api/v1/pulls/gitlab/acme/widget/7/review-draft/publish",
		`{"action":"request_changes","body":"needs work"}`,
	)
	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())

	var problem struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("unsupportedCapability", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal("review_action_request_changes", problem.Details["capability"])
	assert.Equal("gitlab", problem.Details["provider"])
	assert.Equal("gitlab.com", problem.Details["platformHost"])
}

func TestGitLabMutationCreateIssueAndComment(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	srv, database, recorder, repoID := setupGitLabMutationServer(t)
	ctx := t.Context()

	rr := doGitLabJSON(t, srv, http.MethodPost,
		"/api/v1/issues/gitlab/acme/widget",
		`{"title":"Created issue","body":"Issue body"}`,
	)
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())

	created, ok := recorder.find(http.MethodPost, "/api/v4/projects/4242/issues")
	require.True(ok, "fake GitLab API did not receive issue creation")
	assert.Contains(created.Body, `"title":"Created issue"`)
	assert.Contains(created.Body, `"description":"Issue body"`)

	issue, err := database.GetIssueByRepoIDAndNumber(ctx, repoID, 12)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("Created issue", issue.Title)

	rr = doGitLabJSON(t, srv, http.MethodPost,
		"/api/v1/issues/gitlab/acme/widget/11/comments",
		`{"body":"issue comment"}`,
	)
	require.Equal(http.StatusCreated, rr.Code, rr.Body.String())

	comment, ok := recorder.find(http.MethodPost, "/api/v4/projects/4242/issues/11/notes")
	require.True(ok, "fake GitLab API did not receive issue comment")
	assert.Contains(comment.Body, `"body":"issue comment"`)
}

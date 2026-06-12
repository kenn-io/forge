package e2etest

// Full-stack coverage for provider-enforced head pins on GitHub: the
// HTTP API must hand the reviewed head to the provider client (merge
// `sha`, review `commit_id`), map the provider's moved-head rejection
// to a 409 conflict with reason stale_state, and back out an approval
// that landed after the head moved.

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

// setupGitHubHeadPinServer boots the HTTP API with real SQLite against
// the recording GitHub mock and seeds repo acme/widget with PR 7 whose
// locally synced (reviewed) head is "reviewed-sha".
func setupGitHubHeadPinServer(
	t *testing.T, mock *mockGH,
) (*server.Server, *db.DB, int64) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "1",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
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
		PlatformHeadSHA: "reviewed-sha",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	repo := ghclient.RepoRef{
		Platform:     platform.KindGitHub,
		Owner:        "acme",
		Name:         "widget",
		PlatformHost: "github.com",
		RepoPath:     "acme/widget",
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	return srv, database, repoID
}

// githubHeadSequenceFn returns a GetPullRequest stub whose head SHA
// advances through heads on successive calls (later calls repeat the
// final head), simulating a push racing an in-flight mutation.
func githubHeadSequenceFn(
	heads ...string,
) func(context.Context, string, string, int) (*gh.PullRequest, error) {
	var calls atomic.Int32
	return func(context.Context, string, string, int) (*gh.PullRequest, error) {
		i := int(calls.Add(1)) - 1
		head := heads[len(heads)-1]
		if i < len(heads) {
			head = heads[i]
		}
		number := 7
		return &gh.PullRequest{
			Number: &number,
			Head:   &gh.PullRequestBranch{SHA: &head},
		}, nil
	}
}

type conflictProblemBody struct {
	Code    string         `json:"code"`
	Detail  string         `json:"detail"`
	Details map[string]any `json:"details"`
}

func decodeConflictProblem(t *testing.T, body *json.Decoder) (string, map[string]any) {
	t.Helper()
	problem := decodeConflictProblemBody(t, body)
	return problem.Code, problem.Details
}

func decodeConflictProblemBody(t *testing.T, body *json.Decoder) conflictProblemBody {
	t.Helper()
	var problem conflictProblemBody
	require.NoError(t, body.Decode(&problem))
	return problem
}

func TestGitHubMergePassesReviewedHeadPinToProvider(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var recordedPin atomic.Value
	mock := &mockGH{
		mergePullRequestFn: func(
			_ context.Context, _, _ string, _ int, _, _, _, expectedHeadSHA string,
		) (*gh.PullRequestMergeResult, error) {
			recordedPin.Store(expectedHeadSHA)
			merged := true
			sha := "merge-sha"
			message := "merged"
			return &gh.PullRequestMergeResult{Merged: &merged, SHA: &sha, Message: &message}, nil
		},
	}
	srv, database, repoID := setupGitHubHeadPinServer(t, mock)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge",
		json.RawMessage(`{"method":"merge","commit_title":"t","commit_message":"m","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("reviewed-sha", recordedPin.Load(),
		"the reviewed head must reach the provider merge call as the sha pin")

	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("merged", string(mr.State))
}

func TestGitHubMergeRejectsMissingReviewedHeadPin(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var providerCalled atomic.Bool
	mock := &mockGH{
		mergePullRequestFn: func(
			_ context.Context, _, _ string, _ int, _, _, _, _ string,
		) (*gh.PullRequestMergeResult, error) {
			providerCalled.Store(true)
			return &gh.PullRequestMergeResult{}, nil
		},
	}
	srv, _, _ := setupGitHubHeadPinServer(t, mock)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge",
		json.RawMessage(`{"method":"merge","commit_title":"t","commit_message":"m"}`),
	)
	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	var problem conflictProblemBody
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("validationError", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal("body.expected_head_sha", problem.Details["field"])
	assert.False(providerCalled.Load())
}

func TestGitHubMergeMovedHeadRejectionMapsToStaleState(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mock := &mockGH{
		mergePullRequestFn: func(
			_ context.Context, _, _ string, _ int, _, _, _, _ string,
		) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusMethodNotAllowed},
				Message:  "Head branch was modified. Review and try the merge again.",
			}
		},
	}
	srv, _, _ := setupGitHubHeadPinServer(t, mock)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge",
		json.RawMessage(`{"method":"merge","commit_title":"t","commit_message":"m","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	code, details := decodeConflictProblem(t, json.NewDecoder(rr.Body))
	assert.Equal("conflict", code)
	require.NotNil(details)
	assert.Equal("stale_state", details["reason"],
		"the provider's moved-head rejection must surface as stale_state")
}

func TestGitHubMergeGenericConflictKeepsConflictReason(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mock := &mockGH{
		mergePullRequestFn: func(
			_ context.Context, _, _ string, _ int, _, _, _, _ string,
		) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusMethodNotAllowed},
				Message:  "Pull Request is not mergeable",
			}
		},
	}
	srv, _, _ := setupGitHubHeadPinServer(t, mock)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/merge",
		json.RawMessage(`{"method":"merge","commit_title":"t","commit_message":"m","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	code, details := decodeConflictProblem(t, json.NewDecoder(rr.Body))
	assert.Equal("conflict", code)
	require.NotNil(details)
	assert.Equal("conflict", details["reason"],
		"an unrelated provider conflict must not present as staleness")
}

func TestGitHubApproveBindsReviewToReviewedHead(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var recordedCommitID atomic.Value
	mock := &mockGH{
		getPullRequestFn: githubHeadSequenceFn("reviewed-sha"),
		createReviewWithCommentsFn: func(
			_ context.Context, _, _ string, _ int, event, body, commitID string,
			_ []*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			recordedCommitID.Store(commitID)
			id := int64(31)
			submitted := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequestReview{
				ID: &id, State: &event, Body: &body, SubmittedAt: &submitted,
			}, nil
		},
	}
	srv, _, _ := setupGitHubHeadPinServer(t, mock)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/approve",
		json.RawMessage(`{"body":"lgtm","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal("reviewed-sha", recordedCommitID.Load(),
		"the reviewed head must reach the provider review call as commit_id")
}

func TestGitHubApproveRejectsMissingReviewedHeadPin(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var providerCalled atomic.Bool
	mock := &mockGH{
		createReviewWithCommentsFn: func(
			_ context.Context, _, _ string, _ int, _, _, _ string,
			_ []*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			providerCalled.Store(true)
			return &gh.PullRequestReview{}, nil
		},
	}
	srv, _, _ := setupGitHubHeadPinServer(t, mock)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/approve",
		json.RawMessage(`{"body":"lgtm"}`),
	)
	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	var problem conflictProblemBody
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("validationError", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal("body.expected_head_sha", problem.Details["field"])
	assert.False(providerCalled.Load())
}

// When the approval lands on a moved head and the dismissal itself
// fails, the 409 must tell the client the approval may still stand —
// hiding that behind the generic re-review prompt would leave an
// unreviewed approval in place silently.
func TestGitHubApproveFailedDismissalSurfacesContext(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	mock := &mockGH{
		getPullRequestFn: githubHeadSequenceFn("reviewed-sha", "new-sha"),
		createReviewWithCommentsFn: func(
			_ context.Context, _, _ string, _ int, event, body, _ string,
			_ []*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			id := int64(31)
			submitted := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequestReview{
				ID: &id, State: &event, Body: &body, SubmittedAt: &submitted,
			}, nil
		},
		dismissReviewFn: func(
			_ context.Context, _, _ string, _ int, _ int64, _ string,
		) (*gh.PullRequestReview, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusForbidden},
				Message:  "Must have admin rights to dismiss a review",
			}
		},
	}
	srv, _, _ := setupGitHubHeadPinServer(t, mock)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/approve",
		json.RawMessage(`{"body":"lgtm","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	problem := decodeConflictProblemBody(t, json.NewDecoder(rr.Body))
	assert.Equal("conflict", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal("stale_state", problem.Details["reason"])
	sideEffect, _ := problem.Details["context"].(string)
	assert.Contains(sideEffect, "dismissal failed",
		"a standing approval on a moved head must be reported to the client")
	assert.Contains(problem.Detail, "dismissal failed")
	assert.Equal("failed", problem.Details["revocation"],
		"clients must be able to branch on the revocation outcome without parsing prose")
	assert.Equal("31", problem.Details["review_id"])
}

func TestGitHubApproveDismissedWhenHeadMovesDuringSubmit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var dismissedID atomic.Int64
	mock := &mockGH{
		// Pre-check sees the reviewed head; the post-submit check sees
		// a push that landed while the review submitted.
		getPullRequestFn: githubHeadSequenceFn("reviewed-sha", "new-sha"),
		createReviewWithCommentsFn: func(
			_ context.Context, _, _ string, _ int, event, body, _ string,
			_ []*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			id := int64(31)
			submitted := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequestReview{
				ID: &id, State: &event, Body: &body, SubmittedAt: &submitted,
			}, nil
		},
		dismissReviewFn: func(
			_ context.Context, _, _ string, _ int, reviewID int64, _ string,
		) (*gh.PullRequestReview, error) {
			dismissedID.Store(reviewID)
			return &gh.PullRequestReview{ID: &reviewID}, nil
		},
	}
	srv, _, _ := setupGitHubHeadPinServer(t, mock)

	rr := doJSONRequest(t, srv, http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/approve",
		json.RawMessage(`{"body":"lgtm","expected_head_sha":"reviewed-sha"}`),
	)
	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	code, details := decodeConflictProblem(t, json.NewDecoder(rr.Body))
	assert.Equal("conflict", code)
	require.NotNil(details)
	assert.Equal("stale_state", details["reason"])
	assert.Equal("succeeded", details["revocation"],
		"a clean dismissal must be distinguishable from one that left the approval standing")
	assert.Equal(int64(31), dismissedID.Load(),
		"the approval that landed on a moved head must be dismissed upstream")
}

package pullreviewtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/server/syncevents"
	"go.kenn.io/forge/internal/stacks"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"

	forgejoplatform "go.kenn.io/forge/platform/forgejo"

	giteaplatform "go.kenn.io/forge/platform/gitea"
	"go.kenn.io/forge/platform/gitealike"

	platformgithub "go.kenn.io/forge/platform/github"

	platformgitlab "go.kenn.io/forge/platform/gitlab"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m))
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

type staleReadyForReviewError struct{ err error }

func (e *staleReadyForReviewError) Error() string { return e.err.Error() }

func (e *staleReadyForReviewError) Unwrap() error { return e.err }

func (e *staleReadyForReviewError) StatusCode() int { return http.StatusNotFound }

func (e *staleReadyForReviewError) IsStaleState() bool { return true }

func TestAPIReplyToGitHubReviewThreadUsesProviderCommentID(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	providerUpdatedAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	var gotCommentID int64
	var gotBody string
	mock := &serverfake.MockGH{
		CreateReviewCommentReplyFn: func(
			_ context.Context, owner, repo string, number int, body string, commentID int64,
		) (*gh.PullRequestComment, error) {
			assert.Equal("acme", owner)
			assert.Equal("widget", repo)
			assert.Equal(7, number)
			gotCommentID = commentID
			gotBody = body
			id := int64(222)
			login := "fixture-bot"
			now := gh.Timestamp{Time: providerUpdatedAt.Add(-time.Second)}
			return &gh.PullRequestComment{
				ID:        &id,
				Body:      &body,
				User:      &gh.User{Login: &login},
				CreatedAt: &now,
			}, nil
		},
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			return serverfake.ProviderStatePR(number, "open", providerUpdatedAt, nil, nil, "head-sha"), nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	seededAt := providerUpdatedAt.Add(-2 * time.Minute)
	mrID := serverfake.SeedPR(t, database, "acme", "widget", 7,
		serverfake.WithSeedPRTimes(seededAt, seededAt, seededAt),
	)

	now := time.Now().UTC().Truncate(time.Second)
	newLine := 11
	require.NoError(database.UpsertMRReviewThreads(ctx, mrID, []db.MRReviewThread{{
		ProviderThreadID:  "PRRT_1",
		ProviderCommentID: "101",
		Body:              "Please keep this explicit.",
		AuthorLogin:       "reviewer",
		Range: db.ReviewLineRange{
			Path:        "src/review.ts",
			Side:        "right",
			Line:        11,
			NewLine:     &newLine,
			LineType:    "add",
			DiffHeadSHA: "head-sha",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}}))
	threads, err := database.ListMRReviewThreads(ctx, mrID)
	require.NoError(err)
	require.Len(threads, 1)

	localThreadID := strconv.FormatInt(threads[0].ID, 10)
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/discussions/"+localThreadID+"/reply",
		strings.NewReader(`{"body":"Reply from kenn-forge"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusCreated, rr.Code, "response: %s", rr.Body.String())
	assert.Equal(int64(101), gotCommentID)
	assert.Equal("Reply from kenn-forge", gotBody)

	var result struct {
		PlatformExternalID string  `json:"PlatformExternalID"`
		EventType          string  `json:"EventType"`
		Author             string  `json:"Author"`
		Body               string  `json:"Body"`
		ThreadID           *string `json:"ThreadID"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&result))
	assert.Equal("222", result.PlatformExternalID)
	assert.Equal("review_comment", result.EventType)
	assert.Equal("fixture-bot", result.Author)
	assert.Equal("Reply from kenn-forge", result.Body)
	require.NotNil(result.ThreadID)
	assert.Equal("PRRT_1", *result.ThreadID)

	events, err := database.ListMREvents(ctx, mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("review_comment", events[0].EventType)
	assert.Equal("222", events[0].PlatformExternalID)
	assert.Equal("review_comment:222", events[0].DedupeKey)
	require.NotNil(events[0].ThreadID)
	assert.Equal("PRRT_1", *events[0].ThreadID)
	storedMR, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 7)
	require.NoError(err)
	require.NotNil(storedMR)
	assert.Equal(providerUpdatedAt, storedMR.UpdatedAt)
	assert.Equal(providerUpdatedAt, storedMR.LastActivityAt)
}

func TestAPIMergePR405ReturnsGitHubMessage(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	mock := &serverfake.MockGH{
		MergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: 405},
				Message:  "Pull Request is not mergeable",
			}
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRHeadSHA(expectedHeadSHA))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusConflict, resp.StatusCode)
	require.Contains(string(resp.Body), "Pull Request is not mergeable")
}

func TestAPIMergePR409ReturnsGitHubMessage(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	mock := &serverfake.MockGH{
		MergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: 409},
				Message:  "Head branch was modified",
			}
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRHeadSHA(expectedHeadSHA))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusConflict, resp.StatusCode)
	require.Contains(string(resp.Body), "Head branch was modified")
}

func TestAPIMergePRNetworkErrorReturns502(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	mock := &serverfake.MockGH{
		MergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRHeadSHA(expectedHeadSHA))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)
	require.Contains(string(resp.Body), "connection refused")
}

func TestAPIMergePR422ForwardsGitHubMessage(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	mock := &serverfake.MockGH{
		MergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusUnprocessableEntity},
				Message:  "Required status check is failing",
			}
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRHeadSHA(expectedHeadSHA))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(string(resp.Body), "Required status check is failing")
}

func TestAPIMergePR403ForwardsGitHubMessage(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	mock := &serverfake.MockGH{
		MergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusForbidden},
				Message:  "Resource not accessible by integration",
			}
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRHeadSHA(expectedHeadSHA))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusForbidden, resp.StatusCode)
	require.Contains(string(resp.Body), "Resource not accessible by integration")
}

func TestAPIMergePR5xxReturns502WithGitHubMessage(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	mock := &serverfake.MockGH{
		MergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusServiceUnavailable},
				Message:  "Service unavailable",
			}
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRHeadSHA(expectedHeadSHA))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)
	require.Contains(string(resp.Body), "Service unavailable")
}

func TestAPIMergePRForwardsGitHubErrorDetailsAndLogsError(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	logBuf := &lockedBuffer{}
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	mock := &serverfake.MockGH{
		MergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusInternalServerError},
				Message:  "GitHub Server Error",
				Errors: []gh.Error{{
					Resource: "PullRequest",
					Field:    "merge",
					Code:     "custom",
					Message:  "Required status check \"build\" is failing",
				}},
			}
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRHeadSHA(expectedHeadSHA))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	var body generated.ProblemError
	require.NoError(json.Unmarshal(resp.Body, &body))
	require.NotNil(body.Detail)
	assert.Contains(*body.Detail, "Required status check \"build\" is failing")
	assert.NotEqual("GitHub Server Error", *body.Detail)

	logText := logBuf.String()
	assert.Contains(logText, "level=ERROR")
	assert.Contains(logText, "provider merge failed")
	assert.Contains(logText, "Required status check")
}

func TestAPIMergePRStoresUTCTimestamps(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	// The provider reports the merge in a non-UTC zone; the canonical
	// post-merge resync must store it as UTC.
	providerNow := testEDTTime(8, 30)
	expectedHeadSHA := "reviewed-sha"
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(
			context.Context, string, string, int,
		) (*gh.PullRequest, error) {
			return serverfake.ProviderStatePR(
				1, "closed", time.Now().UTC().Add(time.Hour),
				&providerNow, &providerNow, expectedHeadSHA,
			), nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRHeadSHA(expectedHeadSHA))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.Equal(db.MergeRequestStateMerged, pr.State)
	assertTimePtrEqualsUTC(t, pr.MergedAt, providerNow)
	assertTimePtrEqualsUTC(t, pr.ClosedAt, providerNow)
}

func TestAPISyncPRPersistsMergeableState(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	now := time.Now().UTC().Truncate(time.Second)
	mergeableState := "dirty"
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _ string, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			sha := "abc123"
			state := "open"
			title := "Conflicted PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: now}
			createdAt := gh.Timestamp{Time: now}
			return &gh.PullRequest{
				ID:             &id,
				Number:         &number,
				State:          &state,
				Title:          &title,
				HTMLURL:        &url,
				UpdatedAt:      &updatedAt,
				CreatedAt:      &createdAt,
				MergeableState: &mergeableState,
				Head:           &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base:           &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1, func(pr *db.MergeRequest) {
		pr.UpdatedAt = now.Add(-time.Second)
		pr.LastActivityAt = now.Add(-time.Second)
	})
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.JSON200)
	assert.Equal("dirty", resp.JSON200.MergeRequest.MergeableState)

	stored, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("dirty", stored.MergeableState)
}

func TestAPISyncPRPersistsMergedActorInDetail(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now.Add(time.Minute)
	mergedBy := "merge-admin"
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _ string, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			sha := "abc123"
			baseSHA := "def456"
			state := "closed"
			title := "Merged PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: mergedAt}
			createdAt := gh.Timestamp{Time: now}
			mergedAtTimestamp := gh.Timestamp{Time: mergedAt}
			merged := true
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Merged:    &merged,
				MergedAt:  &mergedAtTimestamp,
				ClosedAt:  &mergedAtTimestamp,
				MergedBy:  &gh.User{Login: &mergedBy},
				Head:      &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: new("main")},
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	syncResp, err := client.HTTP.SyncPullWithResponse(ctx, &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode, string(syncResp.Body))

	detailResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
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

func TestAPIIndexSyncPersistsMergedActorForImmediateDetail(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now.Add(time.Minute)
	mergedBy := "merge-admin"
	var getPullCalls atomic.Int32
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			number := 1
			id := int64(1001)
			sha := "abc123"
			baseSHA := "def456"
			headRef := "feature"
			baseRef := "main"
			state := "closed"
			title := "Merged PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: mergedAt}
			createdAt := gh.Timestamp{Time: now}
			mergedAtTimestamp := gh.Timestamp{Time: mergedAt}
			merged := true
			return []*gh.PullRequest{{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Merged:    &merged,
				MergedAt:  &mergedAtTimestamp,
				ClosedAt:  &mergedAtTimestamp,
				MergedBy:  &gh.User{Login: &mergedBy},
				Head:      &gh.PullRequestBranch{SHA: &sha, Ref: &headRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &baseRef},
			}}, nil
		},
		GetPullRequestFn: func(_ context.Context, _ string, _ string, _ int) (*gh.PullRequest, error) {
			getPullCalls.Add(1)
			return nil, errors.New("detail lookup should not run")
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	client := servertest.SetupTestClient(t, srv)

	syncResp, err := client.HTTP.TriggerSyncWithResponse(ctx, &generated.TriggerSyncRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusAccepted, syncResp.StatusCode, string(syncResp.Body))

	require.Eventually(func() bool {
		mr, mrErr := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 1)
		if mrErr != nil || mr == nil {
			return false
		}
		events, eventsErr := database.ListMREvents(ctx, mr.ID)
		if eventsErr != nil {
			return false
		}
		for _, event := range events {
			if event.EventType == "merged" && event.Author == "merge-admin" {
				return true
			}
		}
		return false
	}, 3*time.Second, 25*time.Millisecond)

	detailResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
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
	assert.Equal(int32(0), getPullCalls.Load())
}

func TestAPIGetPullDoesNotFetchProviderForMissingMergedActor(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now.Add(time.Minute)
	var getPullCalls atomic.Int32
	var timelineCalls atomic.Int32
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _ string, _ string, _ int) (*gh.PullRequest, error) {
			getPullCalls.Add(1)
			return nil, errors.New("detail reads must not fetch pull request data")
		},
		ListPRTimelineEventsFn: func(_ context.Context, _ string, _ string, _ int) ([]platformgithub.PullRequestTimelineEvent, error) {
			timelineCalls.Add(1)
			return nil, errors.New("detail reads must not fetch timeline data")
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1,
		serverfake.WithSeedPRLifecycle(db.MergeRequestStateMerged, &mergedAt, &mergedAt),
	)
	client := servertest.SetupTestClient(t, srv)

	detailResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.Events)
	require.Len(detailResp.JSON200.Events, 1)
	event := detailResp.JSON200.Events[0]
	assert.Equal("merged", event.EventType)
	assert.Empty(event.Author)
	assert.Equal("merged this", event.Summary)
	assert.True(event.CreatedAt.Equal(mergedAt))
	assert.Equal(int32(0), getPullCalls.Load())
	assert.Equal(int32(0), timelineCalls.Load())
}

func TestAPISyncPRPreservesMergeableStateWhenRefreshHasNoAnswer(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	tests := []struct {
		name  string
		state *string
	}{
		{name: "omitted", state: nil},
		{name: "unknown", state: new("unknown")},
	}
	now := time.Now().UTC().Truncate(time.Second)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			mock := &serverfake.MockGH{
				GetPullRequestFn: func(_ context.Context, _ string, _ string, number int) (*gh.PullRequest, error) {
					id := int64(1001)
					sha := "abc123"
					baseSHA := "def456"
					state := "open"
					title := "Conflicted PR"
					url := "https://github.com/acme/widget/pull/1"
					updatedAt := gh.Timestamp{Time: now}
					createdAt := gh.Timestamp{Time: now}
					return &gh.PullRequest{
						ID:             &id,
						Number:         &number,
						State:          &state,
						Title:          &title,
						HTMLURL:        &url,
						UpdatedAt:      &updatedAt,
						CreatedAt:      &createdAt,
						MergeableState: tt.state,
						Head:           &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
						Base:           &gh.PullRequestBranch{Ref: new("main"), SHA: &baseSHA},
					}, nil
				},
			}

			srv, database := servertest.SetupTestServerWithMock(t, mock)
			serverfake.SeedPR(t, database, "acme", "widget", 1, func(pr *db.MergeRequest) {
				pr.MergeableState = "dirty"
			}, serverfake.WithSeedPRHeadSHA("abc123"), serverfake.WithSeedPRBaseSHA("def456"))
			client := servertest.SetupTestClient(t, srv)

			resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
			require.NoError(err)
			require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
			require.NotNil(resp.JSON200)
			assert.Equal("dirty", resp.JSON200.MergeRequest.MergeableState)

			stored, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
			require.NoError(err)
			require.NotNil(stored)
			assert.Equal("dirty", stored.MergeableState)
		})
	}
}

func TestAPIApproveWorkflows(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	approvedRunIDs := []int64{}
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			sha := "abc123"
			state := "open"
			title := "Workflow PR"
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
					ID:           new(int64(81)),
					HeadSHA:      new("abc123"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
				{
					ID:           new(int64(82)),
					HeadSHA:      new("abc123"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
				{
					ID:           new(int64(99)),
					HeadSHA:      new("zzz999"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
			}, nil
		},
		ApproveWorkflowRunFn: func(_ context.Context, owner, repo string, runID int64) error {
			require.Equal("acme", owner)
			require.Equal("widget", repo)
			approvedRunIDs = append(approvedRunIDs, runID)
			return nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	repo, err := database.GetRepoByIdentity(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateMRWorkflowApproval(
		t.Context(), repo.ID, 1, time.Now().UTC(), "abc123", true, 2,
	))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(t.Context(), &generated.ApprovePullWorkflowsRequestOptions{PathParams: &generated.ApprovePullWorkflowsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.ApprovedCount)
	assert.Equal("approved_workflows", resp.JSON200.Status)
	assert.EqualValues(2, *resp.JSON200.ApprovedCount)
	assert.Equal([]int64{81, 82}, approvedRunIDs)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal("abc123", pr.PlatformHeadSHA)
	require.NotNil(pr.WorkflowApprovalCheckedAt)
	assert.Equal("abc123", pr.WorkflowApprovalHeadSHA)
	assert.False(pr.WorkflowApprovalRequired)
	assert.Equal(0, pr.WorkflowApprovalCount)
}

func TestAPIApproveWorkflowsZeroMatchesStillSyncsPR(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1002)
			sha := "abc123"
			state := "open"
			title := "Workflow PR"
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
			return nil, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(t.Context(), &generated.ApprovePullWorkflowsRequestOptions{PathParams: &generated.ApprovePullWorkflowsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	assert.Equal("approved_workflows", resp.JSON200.Status)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal("abc123", pr.PlatformHeadSHA)
}

func TestAPIApproveWorkflowsReturnsUnderlyingApprovalErrorAfterPartialFailure(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	approvedRunIDs := []int64{}
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1003)
			sha := "abc123"
			state := "open"
			title := "Workflow PR"
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
					ID:           new(int64(91)),
					HeadSHA:      new("abc123"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
				{
					ID:           new(int64(92)),
					HeadSHA:      new("abc123"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
			}, nil
		},
		ApproveWorkflowRunFn: func(_ context.Context, _, _ string, runID int64) error {
			approvedRunIDs = append(approvedRunIDs, runID)
			if runID == 92 {
				return fmt.Errorf("permission denied")
			}
			return nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(t.Context(), &generated.ApprovePullWorkflowsRequestOptions{PathParams: &generated.ApprovePullWorkflowsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)
	require.NotNil(resp.Error)
	require.NotNil(resp.Error.Detail)
	assert.Contains(*resp.Error.Detail, "permission denied")
	assert.Equal([]int64{91, 92}, approvedRunIDs)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal("abc123", pr.PlatformHeadSHA)
}

// TestAPIApproveWorkflowsForForkPR verifies the approve endpoint reaches
// ApproveWorkflowRun for a fork-triggered run when the run's head repo and
// branch match the PR.
func TestAPIApproveWorkflowsForForkPR(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	approvedRunIDs := []int64{}
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(2002)
			sha := "forkhead"
			state := "open"
			title := "Fork PR"
			url := "https://github.com/acme/widget/pull/1"
			cloneURL := "https://github.com/fork/widget.git"
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
				Head: &gh.PullRequestBranch{
					SHA:  &sha,
					Ref:  new("feature"),
					Repo: &gh.Repository{CloneURL: &cloneURL, FullName: new("fork/widget")},
				},
				Base: &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		ListWorkflowRunsForHeadFn: func(_ context.Context, _, _, headSHA string) ([]*gh.WorkflowRun, error) {
			require.Equal("forkhead", headSHA)
			return []*gh.WorkflowRun{
				{
					ID:             new(int64(71)),
					HeadSHA:        new("forkhead"),
					Event:          new("pull_request"),
					HeadBranch:     new("feature"),
					HeadRepository: &gh.Repository{FullName: new("fork/widget")},
				},
			}, nil
		},
		ApproveWorkflowRunFn: func(_ context.Context, _, _ string, runID int64) error {
			approvedRunIDs = append(approvedRunIDs, runID)
			return nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(t.Context(), &generated.ApprovePullWorkflowsRequestOptions{PathParams: &generated.ApprovePullWorkflowsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.ApprovedCount)
	assert.Equal("approved_workflows", resp.JSON200.Status)
	assert.EqualValues(1, *resp.JSON200.ApprovedCount)
	assert.Equal([]int64{71}, approvedRunIDs)
}

// TestAPIApproveWorkflowsIgnoresRunsForOtherPRAtSameSHA verifies the approve
// endpoint does not call ApproveWorkflowRun for runs whose pull_requests
// association points at a different PR sharing the same head SHA.
func TestAPIApproveWorkflowsIgnoresRunsForOtherPRAtSameSHA(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	approvedRunIDs := []int64{}
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(3002)
			sha := "sharedsha"
			state := "open"
			title := "Shared SHA PR"
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
			require.Equal("sharedsha", headSHA)
			return []*gh.WorkflowRun{
				{
					ID:           new(int64(88)),
					HeadSHA:      new("sharedsha"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(99)}},
				},
				{
					ID:           new(int64(89)),
					HeadSHA:      new("sharedsha"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
			}, nil
		},
		ApproveWorkflowRunFn: func(_ context.Context, _, _ string, runID int64) error {
			approvedRunIDs = append(approvedRunIDs, runID)
			return nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(t.Context(), &generated.ApprovePullWorkflowsRequestOptions{PathParams: &generated.ApprovePullWorkflowsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.ApprovedCount)
	assert.EqualValues(1, *resp.JSON200.ApprovedCount)
	assert.Equal([]int64{89}, approvedRunIDs)
}

// TestAPIApproveWorkflowsRejectsRunFromDifferentForkAtSameSHA exercises the
// safety guarantee that two distinct forks sharing a head SHA do not
// cross-approve. The PR's head repo is alice/widget; the run's head repo is
// bob/widget. ApproveWorkflowRun must not be called.
func TestAPIApproveWorkflowsRejectsRunFromDifferentForkAtSameSHA(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	approvedRunIDs := []int64{}
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(4001)
			sha := "sharedsha"
			state := "open"
			title := "Alice Fork PR"
			url := "https://github.com/acme/widget/pull/1"
			cloneURL := "https://github.com/alice/widget.git"
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
				Head: &gh.PullRequestBranch{
					SHA:  &sha,
					Ref:  new("feature"),
					Repo: &gh.Repository{CloneURL: &cloneURL, FullName: new("alice/widget")},
				},
				Base: &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		ListWorkflowRunsForHeadFn: func(_ context.Context, _, _, headSHA string) ([]*gh.WorkflowRun, error) {
			require.Equal("sharedsha", headSHA)
			return []*gh.WorkflowRun{
				{
					ID:             new(int64(123)),
					HeadSHA:        new("sharedsha"),
					Event:          new("pull_request"),
					HeadBranch:     new("feature"),
					HeadRepository: &gh.Repository{FullName: new("bob/widget")},
				},
			}, nil
		},
		ApproveWorkflowRunFn: func(_ context.Context, _, _ string, runID int64) error {
			approvedRunIDs = append(approvedRunIDs, runID)
			return nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(t.Context(), &generated.ApprovePullWorkflowsRequestOptions{PathParams: &generated.ApprovePullWorkflowsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	assert.Empty(approvedRunIDs)
}

// TestAPIGetPullEmitsStaleDiffWarningOnMergedPR pins the staleness
// branch for merged PRs. getDiff treats merged PRs as stale when the
// recorded DiffHeadSHA no longer matches PlatformHeadSHA, so the
// warning must fire in the same case. Without this coverage a merged
// PR with a stale recorded diff would render outdated content with no
// indication.
func TestAPIGetPullEmitsStaleDiffWarningOnMergedPR(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, clones, serverfake.DefaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	serverfake.SeedPR(t, database, "acme", "widget", 5)

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now
	require.NoError(database.UpdateClosedMRState(
		ctx, repoID, 5, "merged", now, &mergedAt, &mergedAt,
		"deadbeef00000000000000000000000000000099",
		"deadbeef00000000000000000000000000000010",
	))
	// Recorded diff was computed against an earlier head; the merge
	// commit advanced the platform head past it.
	require.NoError(database.UpdateDiffSHAs(
		ctx, repoID, 5,
		"deadbeef00000000000000000000000000000001",
		"deadbeef00000000000000000000000000000010",
		"deadbeef00000000000000000000000000000003",
	))

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when merged diff is stale")
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "out of date")
}

// TestAPIGetPullEmitsDiffWarningWhenSHAsMissingClosed covers a closed
// (not merged) PR whose fetchAndUpdateClosed path failed to populate
// diff SHAs - for example because the clone fetch errored out. The
// previous diffWarnings implementation suppressed warnings for any
// non-open/non-merged state and the user would silently see no diff.
func TestAPIGetPullEmitsDiffWarningWhenSHAsMissingClosed(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, clones, serverfake.DefaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	serverfake.SeedPR(t, database, "acme", "widget", 6)

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	closedAt := now
	require.NoError(database.UpdateClosedMRState(
		ctx, repoID, 6, "closed", now, nil, &closedAt,
		"deadbeef00000000000000000000000000000001",
		"deadbeef00000000000000000000000000000010",
	))
	// Diff SHAs intentionally left empty to simulate a closed PR whose
	// diff sync errored out.

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(6)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when closed PR diff is missing")
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "unavailable")
}

// TestAPIGetPullEmitsStaleDiffWarningOnClosedPR covers a closed (not
// merged) PR whose head or base advanced after the diff sync recorded
// SHAs. getDiff treats this as stale; diffWarnings must agree so the
// detail page shows a warning instead of silently rendering an old
// diff.
func TestAPIGetPullEmitsStaleDiffWarningOnClosedPR(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, clones, serverfake.DefaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	serverfake.SeedPR(t, database, "acme", "widget", 7)

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	closedAt := now
	require.NoError(database.UpdateClosedMRState(
		ctx, repoID, 7, "closed", now, nil, &closedAt,
		"deadbeef00000000000000000000000000000099",
		"deadbeef00000000000000000000000000000010",
	))
	require.NoError(database.UpdateDiffSHAs(
		ctx, repoID, 7,
		"deadbeef00000000000000000000000000000001",
		"deadbeef00000000000000000000000000000010",
		"deadbeef00000000000000000000000000000003",
	))

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when closed PR diff is stale")
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "out of date")
}

// TestAPIGetPullNoDiffWarningOnMergedPRWithBaseDrift pins the
// asymmetry between merged and open/closed staleness: merged PRs only
// care about head SHA drift because the base never advances after
// merge. A merged PR whose head matches but base differs must NOT
// emit a warning.
func TestAPIGetPullNoDiffWarningOnMergedPRWithBaseDrift(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database, clones, serverfake.DefaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	serverfake.SeedPR(t, database, "acme", "widget", 8)

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now
	headSHA := "deadbeef00000000000000000000000000000001"
	require.NoError(database.UpdateClosedMRState(
		ctx, repoID, 8, "merged", now, &mergedAt, &mergedAt,
		headSHA,
		"deadbeef00000000000000000000000000000099",
	))
	require.NoError(database.UpdateDiffSHAs(
		ctx, repoID, 8,
		headSHA,
		"deadbeef00000000000000000000000000000010",
		"deadbeef00000000000000000000000000000003",
	))

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(8)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	if resp.JSON200.Warnings != nil {
		assert.Empty(resp.JSON200.Warnings)
	}
}

func TestAPIGitLabDirectSyncPersistsMergedActorForImmediateDetail(t *testing.T) {
	serverfake.RunParallelServerTest(t)
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
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	syncResp, err := client.HTTP.SyncPullOnHostWithResponse(ctx, &generated.SyncPullOnHostRequestOptions{PathParams: &generated.SyncPullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode, string(syncResp.Body))
	require.NotNil(syncResp.JSON200)
	require.NotNil(syncResp.JSON200.Events)
	require.Len(syncResp.JSON200.Events, 1)
	event := syncResp.JSON200.Events[0]
	assert.Equal("merged", event.EventType)
	assert.Equal("merge-admin", event.Author)
	assert.Equal("merged this", event.Summary)
	assert.True(event.CreatedAt.Equal(mergedAt))

	detailResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.Events)
	require.Len(detailResp.JSON200.Events, 1)
	assert.Equal("merge-admin", detailResp.JSON200.Events[0].Author)
}

func TestAPIGitLabDirectSyncDoesNotDuplicateMergedActorAfterClosedFallback(t *testing.T) {
	serverfake.RunParallelServerTest(t)
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
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)
	provider.MergeRequests = nil
	syncer.RunOnce(ctx)

	stored, err := database.GetMergeRequest(
		ctx, string(ref.Platform), ref.Host, ref.Owner, ref.Name, 7,
	)
	require.NoError(err)
	require.NotNil(stored)
	events, err := database.ListMREvents(ctx, stored.ID)
	require.NoError(err)
	require.Len(events, 1)
	require.Equal("merged", events[0].EventType)
	require.Equal("merge-admin", events[0].Author)

	provider.MergeRequestEvents = map[int][]platform.MergeRequestEvent{7: {{
		Repo:               ref,
		PlatformID:         9001,
		PlatformExternalID: "gid://gitlab/Note/9001",
		MergeRequestNumber: 7,
		EventType:          "merged",
		Author:             "merge-admin",
		Summary:            "merged this",
		CreatedAt:          mergedAt,
		DedupeKey:          "gitlab:merged-note:9001",
	}}}
	syncResp, err := client.HTTP.SyncPullOnHostWithResponse(ctx, &generated.SyncPullOnHostRequestOptions{PathParams: &generated.SyncPullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode, string(syncResp.Body))
	require.NotNil(syncResp.JSON200)
	require.NotNil(syncResp.JSON200.Events)
	require.Len(syncResp.JSON200.Events, 1)
	assert.Equal("merged", syncResp.JSON200.Events[0].EventType)
	assert.Equal("merge-admin", syncResp.JSON200.Events[0].Author)

	events, err = database.ListMREvents(ctx, stored.ID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("merged", events[0].EventType)
	assert.Equal("merge-admin", events[0].Author)
	assert.Equal("merged this", events[0].Summary)
}

func TestAPIPostPRCommentRejectsNilProviderPayload(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	mock := &serverfake.MockGH{
		CreateIssueCommentFn: func(context.Context, string, string, int, string) (*gh.IssueComment, error) {
			return nil, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	mrID := serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.PostPrCommentWithResponse(t.Context(), &generated.PostPrCommentRequestOptions{PathParams: &generated.PostPrCommentPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.PostPrCommentBody{Body: "Looks good"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Empty(events)
}

func TestAPIEditPRCommentRejectsNilProviderPayload(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	mock := &serverfake.MockGH{
		EditIssueCommentFn: func(context.Context, string, string, int64, string) (*gh.IssueComment, error) {
			return nil, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	mrID := serverfake.SeedPR(t, database, "acme", "widget", 1)
	commentID := int64(42)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &commentID,
		EventType:      "issue_comment",
		Body:           "original body",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "comment-42",
	}}))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.EditPrCommentWithResponse(t.Context(), &generated.EditPrCommentRequestOptions{PathParams: &generated.EditPrCommentPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1), CommentID: int64(commentID)}, Body: &generated.EditPrCommentBody{Body: "edited body"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("original body", events[0].Body)
}

func TestAPIApprovePRSubmitsGitHubReview(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	var providerCalled atomic.Bool
	var reviewCommitID string
	mock := &serverfake.MockGH{
		CreateReviewWithCommentsFn: func(
			_ context.Context,
			_, _ string,
			_ int,
			event, body, commitID string,
			_ []*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			providerCalled.Store(true)
			reviewCommitID = commitID
			id := int64(101)
			state := event
			now := gh.Timestamp{Time: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
			return &gh.PullRequestReview{
				ID:          &id,
				State:       &state,
				Body:        &body,
				SubmittedAt: &now,
				User:        &gh.User{Login: new("reviewer")},
			}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	mrID := serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRHeadSHA(expectedHeadSHA))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWithResponse(t.Context(), &generated.ApprovePullRequestOptions{PathParams: &generated.ApprovePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.ApprovePullBody{ExpectedHeadSha: &expectedHeadSHA}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	assert.True(providerCalled.Load())
	assert.Equal(expectedHeadSHA, reviewCommitID)

	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("review", events[0].EventType)
	assert.Equal("APPROVE", events[0].Summary)
}

func TestAPIMergePRRejectsNilProviderPayload(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	mock := &serverfake.MockGH{
		MergePullRequestFn: func(
			context.Context,
			string,
			string,
			int,
			string,
			string,
			string,
		) (*gh.PullRequestMergeResult, error) {
			return nil, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	serverfake.SeedPR(t, database, "acme", "widget", 1, serverfake.WithSeedPRHeadSHA(expectedHeadSHA))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePullBody{
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	mr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(db.MergeRequestStateOpen, mr.State)
	assert.Nil(mr.MergedAt)
}

func TestAPIPostPrCommentAllowsMixedCaseTrackedRepo(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServerWithRepos(
		t,
		&serverfake.MockGH{},
		[]ghclient.RepoRef{{
			Owner:        "Acme",
			Name:         "widget",
			PlatformHost: "github.com",
		}},
	)
	client := servertest.SetupTestClient(t, srv)

	serverfake.SeedPR(t, database, "acme", "widget", 7)

	resp, err := client.HTTP.PostPrCommentWithResponse(t.Context(), &generated.PostPrCommentRequestOptions{PathParams: &generated.PostPrCommentPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(7)}, Body: &generated.PostPrCommentBody{Body: "looks good"}})
	require.NoError(err)
	require.Equal(http.StatusCreated, resp.StatusCode)
	require.NotNil(resp.JSON201)
}

func TestAPIEditPrCommentUpdatesGitHubAndLocalTimeline(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(9876)
	createdAt := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	mock := &serverfake.MockGH{
		EditIssueCommentFn: func(_ context.Context, owner, repo string, gotCommentID int64, body string) (*gh.IssueComment, error) {
			assert.Equal("acme", owner)
			assert.Equal("widget", repo)
			assert.Equal(commentID, gotCommentID)
			assert.Equal("edited body", body)
			login := "maintainer"
			return &gh.IssueComment{
				ID:        &gotCommentID,
				Body:      &body,
				User:      &gh.User{Login: &login},
				CreatedAt: &gh.Timestamp{Time: createdAt},
				UpdatedAt: &gh.Timestamp{Time: createdAt.Add(time.Minute)},
			}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	mrID := serverfake.SeedPR(t, database, "acme", "widget", 7)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &commentID,
		EventType:      "issue_comment",
		Author:         "maintainer",
		Body:           "original body",
		MetadataJSON:   `{"provider_hidden":true,"provider_hidden_reason":"OFF_TOPIC"}`,
		CreatedAt:      createdAt,
		DedupeKey:      "comment-9876",
	}}))

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/7/comments/9876",
		strings.NewReader(`{"body":"edited body"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(http.StatusOK, rec.Code)
	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("edited body", events[0].Body)
	assert.Equal("maintainer", events[0].Author)
	assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"OFF_TOPIC"}`, events[0].MetadataJSON)
	require.NotNil(events[0].PlatformID)
	assert.Equal(commentID, *events[0].PlatformID)
}

func TestAPIEditPrCommentRejectsCommentFromDifferentPR(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	commentID := int64(5555)
	var editCalls atomic.Int32
	mock := &serverfake.MockGH{
		EditIssueCommentFn: func(_ context.Context, _, _ string, _ int64, _ string) (*gh.IssueComment, error) {
			editCalls.Add(1)
			return nil, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	routeMRID := serverfake.SeedPR(t, database, "acme", "widget", 7)
	otherMRID := serverfake.SeedPR(t, database, "acme", "widget", 8)
	require.NotEqual(routeMRID, otherMRID)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: otherMRID,
		PlatformID:     &commentID,
		EventType:      "issue_comment",
		Author:         "maintainer",
		Body:           "other PR body",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "comment-5555",
	}}))

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/7/comments/5555",
		strings.NewReader(`{"body":"wrong target"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(http.StatusNotFound, rec.Code)
	require.Equal(int32(0), editCalls.Load())
}

func TestAPIDeletePrCommentLeavesLocalStateForSync(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(9876)
	var deleteCalls atomic.Int32
	mock := &serverfake.MockGH{
		DeleteIssueCommentFn: func(_ context.Context, owner, repo string, gotCommentID int64) error {
			deleteCalls.Add(1)
			assert.Equal("acme", owner)
			assert.Equal("widget", repo)
			assert.Equal(commentID, gotCommentID)
			return nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	mrID := serverfake.SeedPR(t, database, "acme", "widget", 7)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &commentID,
		EventType:      "issue_comment",
		Author:         "maintainer",
		Body:           "remove me",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "comment-9876",
	}}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/pulls/gh/acme/widget/7/comments/9876", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(http.StatusNoContent, rec.Code)
	assert.Equal(int32(1), deleteCalls.Load())
	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("remove me", events[0].Body)
}

func TestAPIDeletePrCommentKeepsLocalCommentWhenProviderReportsNotFound(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(4321)
	mock := &serverfake.MockGH{
		DeleteIssueCommentFn: func(context.Context, string, string, int64) error {
			return platform.ErrNotFound
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	mrID := serverfake.SeedPR(t, database, "acme", "widget", 7)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &commentID,
		EventType:      "issue_comment",
		Author:         "maintainer",
		Body:           "already absent upstream",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "comment-4321",
	}}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/pulls/gh/acme/widget/7/comments/4321", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(http.StatusNotFound, rec.Code)
	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("already absent upstream", events[0].Body)
}

func TestAPIDeletePrCommentRejectsAnotherParentBeforeProviderCall(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(6543)
	var deleteCalls atomic.Int32
	mock := &serverfake.MockGH{
		DeleteIssueCommentFn: func(context.Context, string, string, int64) error {
			deleteCalls.Add(1)
			return nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 7)
	otherMRID := serverfake.SeedPR(t, database, "acme", "widget", 8)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: otherMRID,
		PlatformID:     &commentID,
		EventType:      "issue_comment",
		Author:         "maintainer",
		Body:           "belongs to another pull request",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "comment-6543",
	}}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/pulls/gh/acme/widget/7/comments/6543", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(http.StatusNotFound, rec.Code)
	assert.Equal(int32(0), deleteCalls.Load())
}

func TestAPIReadyForReview(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	database := dbtest.Open(t)

	mock := &serverfake.MockGH{
		MarkReadyForReviewFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			title := "Ready PR"
			state := "open"
			url := "https://github.com/acme/widget/pull/1"
			author := "octocat"
			draft := false
			now := gh.Timestamp{Time: time.Now().UTC()}
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
				Head:      &gh.PullRequestBranch{Ref: new("feature")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
	}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, serverfake.DefaultTestRepos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := server.New(
		database, syncer, nil, "/",
		nil, server.ServerOptions{},
	)
	client := servertest.SetupTestClient(t, srv)

	repoID, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	prID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1001,
		Number:         1,
		URL:            "https://github.com/acme/widget/pull/1",
		Title:          "Ready PR",
		Author:         "octocat",
		State:          "open",
		IsDraft:        true,
		Body:           "",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		Additions:      0,
		Deletions:      0,
		CommentCount:   0,
		ReviewDecision: "",
		CIStatus:       "",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(database.EnsureKanbanState(t.Context(), prID))

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	require.False(pr.IsDraft)
}

func TestAPIClosePR(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	// The provider reports the close in a non-UTC zone; the handler commits
	// the provider's own edit response, so closed_at comes from there.
	providerClosedAt := testEDTTime(9, 15)
	mock := &serverfake.MockGH{
		EditPullRequestFn: func(
			_ context.Context, _, _ string, number int, opts platformgithub.EditPullRequestOpts,
		) (*gh.PullRequest, error) {
			require.NotNil(opts.State)
			return serverfake.ProviderStatePR(
				number, *opts.State, time.Now().UTC().Add(time.Hour),
				&providerClosedAt, nil, "head-sha",
			), nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1,
		serverfake.WithSeedPRHeadSHA("head-sha"),
		serverfake.WithSeedPRCI("success", `[{"name":"build","status":"completed","conclusion":"success","url":"","app":"GitHub Actions"}]`),
	)
	repo, err := database.GetRepoByIdentity(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	updatedAt := providerClosedAt.Add(-time.Hour)
	number := 1
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: "thread-pr-close",
		RepoID:                 &repo.ID,
		RepoOwner:              "acme",
		RepoName:               "widget",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Test PR #1",
		WebURL:                 "https://github.com/acme/widget/pull/1",
		ItemNumber:             &number,
		ItemType:               "pr",
		Reason:                 "mention",
		Unread:                 true,
		SourceUpdatedAt:        updatedAt,
		SyncedAt:               updatedAt,
	}}))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	assert.Equal(db.MergeRequestStateClosed, pr.State)
	assertTimePtrEqualsUTC(t, pr.ClosedAt, providerClosedAt)
	// The edit response cannot carry CI state; closing must not erase the
	// cached columns of a row no later sync will refetch.
	assert.Equal("success", pr.CIStatus)
	assert.Contains(pr.CIChecksJSON, `"name":"build"`)
	doneNotifications, err := database.ListNotifications(t.Context(), db.ListNotificationsOpts{State: "done"})
	require.NoError(err)
	require.Len(doneNotifications, 1)
	assert.Equal("thread-pr-close", doneNotifications[0].PlatformNotificationID)
	assert.Equal("closed", doneNotifications[0].DoneReason)
}

func TestAPIReopenPR(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	mock := &serverfake.MockGH{
		EditPullRequestFn: func(
			_ context.Context, _, _ string, number int, opts platformgithub.EditPullRequestOpts,
		) (*gh.PullRequest, error) {
			require.NotNil(opts.State)
			return serverfake.ProviderStatePR(
				number, *opts.State, time.Now().UTC().Add(time.Hour),
				nil, nil, "head-sha",
			), nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	ctx := t.Context()

	// Close it first.
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now()
	require.NoError(database.UpdateMRState(ctx, repo.ID, 1, "closed", nil, &now))

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.SetPrGithubStateWithResponse(ctx, &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "open"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	pr, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.Equal(db.MergeRequestStateOpen, pr.State)
	require.Nil(pr.ClosedAt, "closed_at should be cleared on reopen")
}

func TestAPIReadyForReviewDoesNotGetRevertedByStaleSync(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	staleUpdatedAt := time.Date(2026, 4, 12, 1, 0, 0, 0, time.UTC)
	readyUpdatedAt := staleUpdatedAt.Add(30 * time.Minute)
	syncStarted := make(chan struct{}, 1)
	releaseSync := make(chan struct{})
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			syncStarted <- struct{}{}
			<-releaseSync

			id := int64(101)
			state := "open"
			title := "stale sync"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			draft := true
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			createdAt := gh.Timestamp{Time: staleUpdatedAt.Add(-time.Hour)}
			updatedAt := gh.Timestamp{Time: staleUpdatedAt}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				Draft:     &draft,
				User:      &gh.User{Login: &author},
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
		MarkReadyForReviewFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			id := int64(101)
			state := "open"
			title := "ready for review"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			draft := false
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			createdAt := gh.Timestamp{Time: staleUpdatedAt.Add(-time.Hour)}
			updatedAt := gh.Timestamp{Time: readyUpdatedAt}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				Draft:     &draft,
				User:      &gh.User{Login: &author},
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	client := servertest.SetupTestClient(t, srv)

	repoID, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	prID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      101,
		Number:          1,
		URL:             "https://github.com/acme/widget/pull/1",
		Title:           "draft PR",
		Author:          "alice",
		State:           "open",
		IsDraft:         true,
		Body:            "",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "abc123",
		PlatformBaseSHA: "def456",
		Additions:       0,
		Deletions:       0,
		CommentCount:    0,
		ReviewDecision:  "",
		CIStatus:        "",
		CreatedAt:       staleUpdatedAt.Add(-time.Hour),
		UpdatedAt:       staleUpdatedAt.Add(-time.Minute),
		LastActivityAt:  staleUpdatedAt.Add(-time.Minute),
	})
	require.NoError(err)
	require.NoError(database.EnsureKanbanState(t.Context(), prID))

	syncDone := make(chan *generated.SyncPullResp, 1)
	syncErr := make(chan error, 1)
	go func() {
		resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
		if err != nil {
			syncErr <- err
			return
		}
		syncDone <- resp
	}()

	<-syncStarted

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	readyPR, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.False(readyPR.IsDraft)
	assert.True(readyPR.UpdatedAt.Equal(readyUpdatedAt))

	close(releaseSync)

	completed := false
	select {
	case err := <-syncErr:
		require.NoError(err)
		completed = true
	case resp := <-syncDone:
		require.Equal(http.StatusOK, resp.StatusCode)
		completed = true
	case <-time.After(5 * time.Second):
	}
	require.True(completed, "timed out waiting for stale draft sync")

	finalPR, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	assert.False(finalPR.IsDraft)
	assert.Equal("ready for review", finalPR.Title)
	assert.True(finalPR.UpdatedAt.Equal(readyUpdatedAt))
}

func TestAPIListPullsReportsHistoricalMergedPRFromMergedAt(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	now := time.Date(2024, 6, 7, 12, 0, 0, 0, time.UTC)
	mergedAt := now.Add(time.Hour)
	number := 42
	title := "Historical merged PR"
	headSHA := "abc123def456"
	baseSHA := "def456abc123"
	database := dbtest.Open(t)
	serverfake.SeedPR(
		t, database, "acme", "widget", number,
		serverfake.WithSeedPRTitle(title),
		serverfake.WithSeedPRAuthor("alice"),
		serverfake.WithSeedPRLifecycle(db.MergeRequestStateMerged, &mergedAt, &mergedAt),
		serverfake.WithSeedPRTimes(now, now, mergedAt),
		serverfake.WithSeedPRHeadSHA(headSHA),
		serverfake.WithSeedPRBaseSHA(baseSHA),
	)

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
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	client := servertest.SetupTestClient(t, srv)
	filterState := "closed"
	resp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{Query: &generated.ListPullsQuery{State: &filterState}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	apiPR := (*resp.JSON200)[0]
	assert.Equal(int64(number), apiPR.Number)
	assert.Equal(title, apiPR.Title)
	assert.Equal(generated.MergeRequestResponseStateMerged, apiPR.State)
	require.NotNil(apiPR.MergedAt)
	assert.True(apiPR.MergedAt.Equal(mergedAt))
}

func TestAPIClosePR422NilFallbackPayloadDoesNotCorruptDB(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &serverfake.MockGH{
		EditPullRequestFn: func(_ context.Context, _, _ string, _ int, _ platformgithub.EditPullRequestOpts) (*gh.PullRequest, error) {
			return nil, serverfake.Make422Error()
		},
		GetPullRequestFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	before, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(before)

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "closed"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	after, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(after)
	assert.Equal(before.State, after.State)
	assert.Equal(before.UpdatedAt, after.UpdatedAt)
	assert.Nil(after.ClosedAt)
}

func TestAPIClosePR422AlreadyClosed(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	// EditPullRequest returns 422, but re-fetch shows PR is already closed.
	// Should succeed since the requested state matches.
	state := "closed"
	mock := &serverfake.MockGH{
		EditPullRequestFn: func(_ context.Context, _, _ string, _ int, _ platformgithub.EditPullRequestOpts) (*gh.PullRequest, error) {
			return nil, serverfake.Make422Error()
		},
		GetPullRequestFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			id := int64(1000)
			now := gh.Timestamp{Time: time.Now().UTC()}
			closedAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID: &id, Number: new(1), State: &state,
				Title: new("PR"), HTMLURL: new("https://example.com"),
				User:      &gh.User{Login: new("u")},
				Head:      &gh.PullRequestBranch{Ref: new("f")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
				CreatedAt: &now, UpdatedAt: &now, ClosedAt: &closedAt,
			}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	pr, _ := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.Equal(db.MergeRequestStateClosed, pr.State)
}

// When MarkPullRequestReadyForReview returns (nil, nil) the handler
// must return 502 rather than claiming success with no PR payload.
func TestAPIReadyForReview502OnNilPR(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	mock := &serverfake.MockGH{
		MarkReadyForReviewFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)
}

func TestAPIReadyForReviewReturnsUnderlyingErrorDetail(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	mock := &serverfake.MockGH{
		MarkReadyForReviewFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, errors.New("marking acme/widget#1 ready for review: draft review threads still pending")
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)
	require.NotNil(resp.Error)
	require.NotNil(resp.Error.Detail)
	require.Equal(
		"marking acme/widget#1 ready for review: draft review threads still pending",
		*resp.Error.Detail,
	)
}

func TestAPIReadyForReviewStaleStateRefreshesAndReturnsSuccess(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	staleErr := &staleReadyForReviewError{
		err: errors.New(
			"marking acme/widget#1 ready for review: graphql errors: Could not resolve to a PullRequest with the global id of 'PR_kwDOAAABc84'.",
		),
	}
	mock := &serverfake.MockGH{
		MarkReadyForReviewFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, staleErr
		},
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			title := "Already ready"
			state := "open"
			url := "https://github.com/acme/widget/pull/1"
			author := "octocat"
			draft := false
			now := gh.Timestamp{Time: time.Now().UTC()}
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
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
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	pr.IsDraft = true
	_, err = database.UpsertMergeRequest(t.Context(), pr)
	require.NoError(err)

	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	pr, err = database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	require.False(pr.IsDraft)
}

func TestAPIReadyForReview404RefreshesStaleDraftState(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	notFound := &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found"},
		Message:  "Not Found",
	}
	mock := &serverfake.MockGH{
		MarkReadyForReviewFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, fmt.Errorf("marking acme/widget#1 ready for review: %w", notFound)
		},
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			title := "Already ready"
			state := "open"
			url := "https://github.com/acme/widget/pull/1"
			author := "octocat"
			draft := false
			now := gh.Timestamp{Time: time.Now().UTC()}
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
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
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	require.False(pr.IsDraft)
}

func TestAPIClosePR422Merged(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	// EditPullRequest returns 422, re-fetch shows PR is merged.
	// Should return 409.
	merged := "closed"
	mock := &serverfake.MockGH{
		EditPullRequestFn: func(_ context.Context, _, _ string, _ int, _ platformgithub.EditPullRequestOpts) (*gh.PullRequest, error) {
			return nil, serverfake.Make422Error()
		},
		GetPullRequestFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			id := int64(1000)
			now := gh.Timestamp{Time: time.Now().UTC()}
			mergedBool := true
			return &gh.PullRequest{
				ID: &id, Number: new(1), State: &merged, Merged: &mergedBool,
				Title: new("PR"), HTMLURL: new("https://example.com"),
				User:      &gh.User{Login: new("u")},
				Head:      &gh.PullRequestBranch{Ref: new("f")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
				CreatedAt: &now, UpdatedAt: &now,
			}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "closed"}})
	require.Error(t, err)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestAPIGitHubPublishReviewDraftSendsCommentsThroughServer(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	type capturedReview struct {
		owner    string
		repo     string
		number   int
		event    string
		body     string
		commitID string
		comments []*gh.DraftReviewComment
	}
	var captured capturedReview
	submittedAt := gh.Timestamp{Time: time.Now().UTC().Truncate(time.Second)}
	mock := &serverfake.MockGH{
		CreateReviewWithCommentsFn: func(
			_ context.Context,
			owner, repo string,
			number int,
			event string,
			body string,
			commitID string,
			comments []*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			captured = capturedReview{
				owner:    owner,
				repo:     repo,
				number:   number,
				event:    event,
				body:     body,
				commitID: commitID,
				comments: comments,
			}
			id := int64(501)
			state := "COMMENTED"
			return &gh.PullRequestReview{ID: &id, State: &state, SubmittedAt: &submittedAt}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 42)
	mr, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 42)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpdateDiffSHAs(ctx, mr.RepoID, 42, "github-head", "base", "merge-base"))

	basePath := "/api/v1/pulls/gh/acme/widget/42/review-draft"
	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "Please tighten this line.",
		"range": map[string]any{
			"path":          "src/main.go",
			"side":          "right",
			"start_side":    "right",
			"start_line":    40,
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "github-head",
			"commit_sha":    "github-commit",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	publishRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/publish", map[string]string{
		"action": "request_changes",
		"body":   " Needs changes. ",
	})

	require.Equal(http.StatusOK, publishRR.Code, publishRR.Body.String())
	var publishStatus pullapi.ActionStatusBody
	require.NoError(json.NewDecoder(publishRR.Body).Decode(&publishStatus))
	assert.Equal("published", publishStatus.Status)

	assert.Equal("acme", captured.owner)
	assert.Equal("widget", captured.repo)
	assert.Equal(42, captured.number)
	assert.Equal("REQUEST_CHANGES", captured.event)
	assert.Equal("Needs changes.", captured.body)
	assert.Equal("github-head", captured.commitID)
	require.Len(captured.comments, 1)
	comment := captured.comments[0]
	assert.Equal("src/main.go", comment.GetPath())
	assert.Equal("Please tighten this line.", comment.GetBody())
	assert.Equal("RIGHT", comment.GetSide())
	assert.Equal(40, comment.GetStartLine())
	assert.Equal("RIGHT", comment.GetStartSide())
	assert.Equal(42, comment.GetLine())

	storedDraft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	assert.Nil(storedDraft)
}

func TestAPIGitHubRequestChangesDoesNotPublishSavedDraftComments(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	var captured platform.PublishDiffReviewDraftInput
	// No getPullRequestFn: the default mock returns no PR, so this test
	// also proves direct request-changes performs no provider head reads
	// beyond the review submission — the same call shape as /approve.
	mock := &serverfake.MockGH{
		CreateReviewWithCommentsFn: func(
			_ context.Context,
			_, _ string,
			_ int,
			event string,
			body string,
			commitID string,
			comments []*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			captured = platform.PublishDiffReviewDraftInput{
				Body:    body,
				Action:  platform.ReviewActionRequestChanges,
				HeadSHA: commitID,
			}
			assert.Equal("REQUEST_CHANGES", event)
			assert.Empty(comments)
			id := int64(502)
			state := "CHANGES_REQUESTED"
			return &gh.PullRequestReview{ID: &id, State: &state}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 42)
	mr, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 42)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpdateDiffSHAs(ctx, mr.RepoID, 42, "reviewed-head", "base", "merge-base"))

	basePath := "/api/v1/pulls/gh/acme/widget/42/review-draft"
	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "Saved for the later inline review.",
		"range": map[string]any{
			"path": "src/main.go", "side": "right", "line": 42,
			"new_line": 42, "line_type": "add", "diff_head_sha": "reviewed-head",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	requestRR := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/pulls/gh/acme/widget/42/request-changes", map[string]string{
		"body":              " Please cover the empty state. ",
		"expected_head_sha": "provider-head",
	})

	require.Equal(http.StatusOK, requestRR.Code, requestRR.Body.String())
	assert.Equal("Please cover the empty state.", captured.Body)
	assert.Equal(platform.ReviewActionRequestChanges, captured.Action)
	assert.Equal("provider-head", captured.HeadSHA)

	storedDraft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	require.NotNil(storedDraft)
	comments, err := database.ListMRReviewDraftComments(ctx, storedDraft.ID)
	require.NoError(err)
	require.Len(comments, 1)
	assert.Equal("Saved for the later inline review.", comments[0].Body)
}

func TestAPIGitHubReviewDraftHidesApproveForSelfAuthoredPR(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &serverfake.MockGH{
		AuthenticatedViewerLoginFn: func(context.Context) (string, error) {
			return "marius", nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 42, serverfake.WithSeedPRAuthor("marius"))
	serverfake.SeedPR(t, database, "acme", "widget", 43, serverfake.WithSeedPRAuthor("someone-else"))

	selfRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/gh/acme/widget/42/review-draft", nil)
	require.Equal(http.StatusOK, selfRR.Code, selfRR.Body.String())
	var selfDraft map[string]any
	require.NoError(json.NewDecoder(selfRR.Body).Decode(&selfDraft))
	assert.Equal(
		[]any{"comment", "request_changes"},
		selfDraft["supported_actions"],
		"self-authored PR draft must not advertise approve",
	)

	otherRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/gh/acme/widget/43/review-draft", nil)
	require.Equal(http.StatusOK, otherRR.Code, otherRR.Body.String())
	var otherDraft map[string]any
	require.NoError(json.NewDecoder(otherRR.Body).Decode(&otherDraft))
	assert.Equal(
		[]any{"comment", "approve", "request_changes"},
		otherDraft["supported_actions"],
		"other-authored PR draft must still advertise approve",
	)
}

func TestAPIGitLabPublishReviewDraftSurfacesCleanupFailureAsPartial(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	providerUpdatedAt := now.Add(time.Minute)
	var createAttempts atomic.Int32
	var publishAttempts atomic.Int32
	var deleteAttempts atomic.Int32
	writeRawJSON := func(w http.ResponseWriter, body string) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/4242/merge_requests/7/draft_notes":
			assert.Equal(http.MethodPost, r.Method)
			next := createAttempts.Add(1)
			var body struct {
				Note     string `json:"note"`
				CommitID string `json:"commit_id"`
				Position struct {
					NewPath string `json:"new_path"`
					NewLine int64  `json:"new_line"`
				} `json:"position"`
			}
			if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			assert.Equal("gitlab-head", body.CommitID)
			assert.Equal("src/main.go", body.Position.NewPath)
			assert.Equal(int64(40+next), body.Position.NewLine)
			writeRawJSON(w, fmt.Sprintf(`{"id": %d, "note": %q}`, 54+next, body.Note))
		case "/api/v4/projects/4242/merge_requests/7/draft_notes/55/publish":
			assert.Equal(http.MethodPut, r.Method)
			publishAttempts.Add(1)
			writeRawJSON(w, `{}`)
		case "/api/v4/projects/4242/merge_requests/7/draft_notes/56/publish":
			assert.Equal(http.MethodPut, r.Method)
			publishAttempts.Add(1)
			http.Error(w, "publish failed", http.StatusBadRequest)
		case "/api/v4/projects/4242/merge_requests/7/draft_notes/56":
			assert.Equal(http.MethodDelete, r.Method)
			deleteAttempts.Add(1)
			http.Error(w, "delete failed", http.StatusBadRequest)
		case "/api/v4/projects/4242/merge_requests/7/discussions":
			assert.Equal(http.MethodGet, r.Method)
			writeRawJSON(w, `[
				{
					"id": "discussion-55",
					"individual_note": false,
					"notes": [{
						"id": 55,
						"type": "DiscussionNote",
						"body": "first line",
						"author": {"username": "reviewer"},
						"system": false,
						"resolvable": true,
						"resolved": false,
						"created_at": "`+now.Format(time.RFC3339)+`",
						"updated_at": "`+now.Format(time.RFC3339)+`",
						"position": {
							"base_sha": "base",
							"start_sha": "merge-base",
							"head_sha": "gitlab-head",
							"position_type": "text",
							"new_path": "src/main.go",
							"new_line": 41
						}
					}]
				}
			]`)
		case "/api/v4/projects/4242/merge_requests/7":
			assert.Equal(http.MethodGet, r.Method)
			writeRawJSON(w, `{"id":7001,"iid":7,"updated_at":"`+
				providerUpdatedAt.Format(time.RFC3339)+`"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gitlabServer.Close()

	database := dbtest.Open(t)
	client, err := platformgitlab.NewClient(
		"gitlab.example.com",
		serverfake.TestTokenSource("token"),
		platformgitlab.WithBaseURLForTesting(gitlabServer.URL+"/api/v4"), platformgitlab.
			WithTransport(http.DefaultTransport),
	)
	require.NoError(err)
	registry, err := platform.NewRegistry(client)
	require.NoError(err)
	repoRef := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		PlatformHost:       "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformRepoID:     4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repoRef}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "4242",
		Owner:          "group",
		Name:           "project",
		RepoPath:       "group/project",
	})
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderMetadata(ctx, repoID, db.RepoProviderMetadata{
		PlatformRepoID: "4242",
		WebURL:         "https://gitlab.example.com/group/project",
		CloneURL:       "https://gitlab.example.com/group/project.git",
		DefaultBranch:  "main",
	}))
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:             repoID,
		PlatformID:         7001,
		PlatformExternalID: "gid://gitlab/MergeRequest/7001",
		Number:             7,
		URL:                "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:              "GitLab provider MR",
		Author:             "ada",
		State:              "open",
		HeadBranch:         "feature/gitlab",
		BaseBranch:         "main",
		PlatformHeadSHA:    "gitlab-head",
		PlatformBaseSHA:    "base",
		CreatedAt:          now,
		UpdatedAt:          now,
		LastActivityAt:     now,
	})
	require.NoError(err)
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 7, "gitlab-head", "base", "merge-base"))

	basePath := "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft"
	for _, line := range []int{41, 42} {
		createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
			"body": fmt.Sprintf("line %d", line),
			"range": map[string]any{
				"path":          "src/main.go",
				"side":          "right",
				"line":          line,
				"new_line":      line,
				"line_type":     "add",
				"diff_head_sha": "gitlab-head",
			},
		})

		require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())
	}

	publishRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/publish", map[string]string{
		"action": "comment",
	})

	require.Equal(http.StatusOK, publishRR.Code, publishRR.Body.String())
	var publishStatus pullapi.ActionStatusBody
	require.NoError(json.NewDecoder(publishRR.Body).Decode(&publishStatus))
	assert.Equal("partially_published", publishStatus.Status)
	assert.Equal(int32(2), createAttempts.Load())
	assert.Equal(int32(2), publishAttempts.Load())
	assert.Equal(int32(1), deleteAttempts.Load())

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	draft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	require.NotNil(draft)
	comments, err := database.ListMRReviewDraftComments(ctx, draft.ID)
	require.NoError(err)
	require.Len(comments, 1)
	assert.Equal("line 42", comments[0].Body)
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)
	assert.Equal("discussion-55", threads[0].ProviderThreadID)
	assert.Equal(now, threads[0].CreatedAt)
	assert.Equal(now, threads[0].UpdatedAt)
	freshMR, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(freshMR)
	assert.Equal(providerUpdatedAt, freshMR.UpdatedAt)
	assert.Equal(providerUpdatedAt, freshMR.LastActivityAt)
}

func TestAPIForgejoPublishReviewDraftIngestsTimelineThread(t *testing.T) {
	serverfake.RunParallelServerTest(t)
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
	srv, database, provider := setupForgejoCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "forgejo",
		PlatformHost: "codeberg.org",
		RepoPath:     "acme/widgets",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 42)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 42, "forgejo-head", "base", "merge"))
	now := time.Now().UTC().Truncate(time.Second)
	line := 7
	provider.ReviewThreads = []platform.MergeRequestReviewThread{{
		ProviderThreadID:  "comment-42",
		ProviderReviewID:  "review-42",
		ProviderCommentID: "comment-42",
		Body:              "Forgejo inline note",
		AuthorLogin:       "ada",
		Range: platform.DiffReviewLineRange{
			Path:        "src/main.go",
			Side:        "right",
			Line:        7,
			NewLine:     &line,
			LineType:    "add",
			DiffHeadSHA: "forgejo-head",
			CommitSHA:   "forgejo-head",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}}

	basePath := "/api/v1/pulls/forgejo/acme/widgets/42/review-draft"
	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "Forgejo inline note",
		"range": map[string]any{
			"path":          "src/main.go",
			"side":          "right",
			"line":          7,
			"new_line":      7,
			"line_type":     "add",
			"diff_head_sha": "forgejo-head",
			"commit_sha":    "forgejo-head",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	publishRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/publish", map[string]string{
		"action": "comment",
	})

	require.Equal(http.StatusOK, publishRR.Code, publishRR.Body.String())
	require.Len(provider.PublishedReviews, 1)

	detailRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/forgejo/acme/widgets/42", nil)
	require.Equal(http.StatusOK, detailRR.Code, detailRR.Body.String())
	var detail pullapi.MergeRequestDetailResponse
	require.NoError(json.NewDecoder(detailRR.Body).Decode(&detail))
	require.Len(detail.Events, 1)
	require.NotNil(detail.Events[0].DiffThread)
	assert.Equal("Forgejo inline note", detail.Events[0].DiffThread.Body)
	assert.Equal("src/main.go", detail.Events[0].DiffThread.Path)
	assert.Equal(7, detail.Events[0].DiffThread.Line)
}

func TestAPIForgejoSyncPersistsCompleteLargeReviewDatasetBeforeSuccess(t *testing.T) {
	serverfake.RunParallelServerTest(t)
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
	srv, database, provider := setupForgejoCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "forgejo",
		PlatformHost: "codeberg.org",
		RepoPath:     "acme/widgets",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 42)
	require.NoError(err)
	require.NotNil(mr)
	events, err := database.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.Empty(events)

	now := time.Now().UTC().Truncate(time.Second)
	provider.ReviewThreads = make([]platform.MergeRequestReviewThread, 101)
	for i := range provider.ReviewThreads {
		line := i + 1
		id := fmt.Sprintf("%d", line)
		provider.ReviewThreads[i] = platform.MergeRequestReviewThread{
			ProviderThreadID: "thread-" + id, ProviderReviewID: "review-" + id,
			ProviderCommentID: "comment-" + id, Body: "inline note " + id,
			AuthorLogin: "ada",
			Range: platform.DiffReviewLineRange{
				Path: "src/recovered.go", Side: "right", Line: line,
				NewLine: &line, LineType: "add",
				DiffHeadSHA: "abc123", CommitSHA: "abc123",
			},
			CreatedAt: now, UpdatedAt: now,
		}
	}

	syncRR := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/pulls/forgejo/acme/widgets/42/sync", nil)
	require.Equal(http.StatusOK, syncRR.Code, syncRR.Body.String())
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 101)

	detailRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/forgejo/acme/widgets/42", nil)
	require.Equal(http.StatusOK, detailRR.Code, detailRR.Body.String())
	var detail pullapi.MergeRequestDetailResponse
	require.NoError(json.NewDecoder(detailRR.Body).Decode(&detail))
	require.Len(detail.Events, 101)
	for _, event := range detail.Events {
		assert.Equal("review_comment", event.EventType)
		require.NotNil(event.DiffThread)
		assert.Equal("src/recovered.go", event.DiffThread.Path)
	}
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

func TestAPIApplyReviewSuggestionRejectsUnknownHeadRepoOnGitHub(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	tests := []struct {
		name             string
		headRepoCloneURL string
	}{
		{name: "missing clone URL"},
		{name: "unparseable clone URL", headRepoCloneURL: "not-a-url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			srv, _, provider := setupGitHubCapabilityServerWithProvider(t, &caps, tt.headRepoCloneURL)

			rr := testutil.DoJSON(
				t,
				srv,
				http.MethodPost,
				"/api/v1/host/github.example.com/pulls/gh/acme/widget/7/review-suggestions/apply",
				map[string]any{
					"expected_head_sha": "abc123",
					"suggestions": []map[string]any{{
						"thread_id":   "1",
						"replacement": "return client.publishThreads();",
					}},
				})

			require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
			var problem serverfake.RawProblemDetail
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal(string(httpapi.CodeConflict), problem.Code)
			assert.Equal("pull request head repository is unknown", problem.Detail)
			require.NotNil(problem.Details)
			assert.Equal("head_repo_unknown", problem.Details["reason"])
			assert.Empty(provider.AppliedSuggestions)
		})
	}
}

func TestAPIApplyReviewSuggestionPassesHeadRepoToGitHubProvider(t *testing.T) {
	serverfake.RunParallelServerTest(t)
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
	srv, database, provider := setupGitHubCapabilityServerWithProvider(
		t, &caps, "https://github.example.com/fork/widget.git",
	)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.example.com",
		RepoPath:     "acme/widget",
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
		"/api/v1/host/github.example.com/pulls/gh/acme/widget/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(thread.ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	require.Len(provider.AppliedSuggestions, 1)
	assert.Equal(
		"https://github.example.com/fork/widget.git",
		provider.AppliedSuggestions[0].HeadRepoCloneURL,
	)
}

func TestAPIApplyReviewSuggestionMapsProviderConflictReason(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	tests := []struct {
		name   string
		reason string
	}{
		{name: "closed upstream after sync", reason: "not_open"},
		{name: "head repo lost after sync", reason: "head_repo_unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			srv, database, provider := setupGitHubCapabilityServerWithProvider(
				t, &caps, "https://github.example.com/fork/widget.git",
			)
			ctx := t.Context()
			provider.ApplySuggestionsErr = &platform.Error{
				Code:         platform.ErrCodeConflict,
				Provider:     platform.KindGitHub,
				PlatformHost: "github.example.com",
				Details:      map[string]string{"reason": tt.reason},
				Err:          errors.New("live pull request state rejected the apply"),
			}

			repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
				Platform:     "github",
				PlatformHost: "github.example.com",
				RepoPath:     "acme/widget",
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
				"/api/v1/host/github.example.com/pulls/gh/acme/widget/7/review-suggestions/apply",
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
			require.NotNil(problem.Details)
			assert.Equal(tt.reason, problem.Details["reason"])
		})
	}
}

func TestAPIApplyReviewSuggestionMapsProviderStaleStateAndRefreshesDetail(t *testing.T) {
	serverfake.RunParallelServerTest(t)
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
	srv, database, provider := setupGitHubCapabilityServerWithProvider(
		t, &caps, "https://github.example.com/fork/widget.git",
	)
	ctx := t.Context()
	provider.ApplySuggestionsErr = &platform.Error{
		Code:         platform.ErrCodeStaleState,
		Provider:     platform.KindGitHub,
		PlatformHost: "github.example.com",
		Err:          errors.New("pull request head branch changed since it was reviewed"),
	}

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.example.com",
		RepoPath:     "acme/widget",
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
		"/api/v1/host/github.example.com/pulls/gh/acme/widget/7/review-suggestions/apply",
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
	assert.Equal("target changed since it was reviewed; refresh and retry", problem.Detail)
	require.NotNil(problem.Details)
	assert.Equal("stale_state", problem.Details["reason"])
	assert.Empty(provider.AppliedSuggestions)
	changed := readEventMatching(t, ch, func(ev syncevents.Event) bool {
		return ev.Type == "data_changed"
	})
	assert.Equal("data_changed", changed.Type)
}

func TestAPIGitealikeHTTPMergeabilityPersistsThroughServer(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	tests := []struct {
		name      string
		kind      platform.Kind
		host      string
		token     string
		newClient func(host, token, baseURL string) (platform.Provider, error)
	}{
		{
			name:  "gitea",
			kind:  platform.KindGitea,
			host:  "gitea.test",
			token: "gitea-token",
			newClient: func(host, token, baseURL string) (platform.Provider, error) {
				return giteaplatform.NewClient(
					host,
					serverfake.TestTokenSource(token),
					giteaplatform.WithBaseURL(baseURL, true),
					giteaplatform.WithServerVersion("1.26.0"),
					giteaplatform.WithTransport(http.DefaultTransport),
				)
			},
		},
		{
			name:  "forgejo",
			kind:  platform.KindForgejo,
			host:  "codeberg.test",
			token: "forgejo-token",
			newClient: func(host, token, baseURL string) (platform.Provider, error) {
				return forgejoplatform.NewClient(
					host,
					serverfake.TestTokenSource(token),
					forgejoplatform.WithBaseURLForTesting(baseURL),
					forgejoplatform.WithTransport(http.DefaultTransport),
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			ctx := t.Context()
			base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
			pull := func(number int, title, headSHA string) map[string]any {
				return map[string]any{
					"id":         number + 1000,
					"number":     number,
					"url":        "https://" + tt.host + "/tea/kettle/pulls/" + strconv.Itoa(number),
					"html_url":   "https://" + tt.host + "/tea/kettle/pulls/" + strconv.Itoa(number),
					"title":      title,
					"state":      "open",
					"user":       map[string]any{"login": "alice"},
					"head":       map[string]any{"ref": "feature", "sha": headSHA},
					"base":       map[string]any{"ref": "main", "sha": "base-sha"},
					"created_at": base,
					"updated_at": base.Add(time.Minute),
				}
			}
			dirtyPull := pull(7, "Conflicted kettle", "head-dirty")
			dirtyPull["mergeable"] = false
			nullPull := pull(8, "Unknown kettle", "head-null")
			nullPull["mergeable"] = nil
			omittedPull := pull(9, "Omitted kettle", "head-omitted")

			providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal("token "+tt.token, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/repos/tea/kettle":
					assert.NoError(json.NewEncoder(w).Encode(map[string]any{
						"id":             101,
						"name":           "kettle",
						"full_name":      "tea/kettle",
						"html_url":       "https://" + tt.host + "/tea/kettle",
						"clone_url":      "https://" + tt.host + "/tea/kettle.git",
						"default_branch": "main",
						"owner":          map[string]any{"login": "tea"},
						"created_at":     base,
						"updated_at":     base,
					}))
				case "/api/v1/repos/tea/kettle/pulls":
					assert.Equal("open", r.URL.Query().Get("state"))
					assert.NoError(json.NewEncoder(w).Encode([]map[string]any{
						dirtyPull, nullPull, omittedPull,
					}))
				case "/api/v1/repos/tea/kettle/issues",
					"/api/v1/repos/tea/kettle/releases",
					"/api/v1/repos/tea/kettle/tags":
					assert.NoError(json.NewEncoder(w).Encode([]map[string]any{}))
				default:
					http.NotFound(w, r)
				}
			}))
			defer providerServer.Close()

			provider, err := tt.newClient(tt.host, tt.token, providerServer.URL)
			require.NoError(err)
			registry, err := platform.NewRegistry(provider)
			require.NoError(err)

			database := dbtest.Open(t)
			syncer := ghclient.NewSyncerWithRegistry(
				registry,
				database,
				nil,
				[]ghclient.RepoRef{{
					Platform:     tt.kind,
					PlatformHost: tt.host,
					Owner:        "tea",
					Name:         "kettle",
					RepoPath:     "tea/kettle",
				}},
				time.Minute,
				nil,
				nil,
			)
			t.Cleanup(syncer.Stop)
			srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
			t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
			client := servertest.SetupTestClient(t, srv)

			syncer.RunOnce(ctx)
			repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
				Platform:     string(tt.kind),
				PlatformHost: tt.host,
				RepoPath:     "tea/kettle",
			})
			require.NoError(err)
			require.NotNil(repo)

			assert.Equal("dirty", serverfake.RequireMR(t, database, repo.ID, 7).MergeableState)
			assert.Empty(serverfake.RequireMR(t, database, repo.ID, 8).MergeableState)
			assert.Empty(serverfake.RequireMR(t, database, repo.ID, 9).MergeableState)

			detailResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: tt.host, Provider: string(tt.kind), Owner: "tea", Name: "kettle", Number: int64(7)}})
			require.NoError(err)
			require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
			require.NotNil(detailResp.JSON200)
			assert.Equal("dirty", detailResp.JSON200.MergeRequest.MergeableState)
		})
	}
}

// setupGitealikeHeadPinServer boots the HTTP API with real SQLite over a
// Gitea provider whose PR 7 syncs with head "abc123", for head-pin
// safety coverage through the real route path.
func setupGitealikeHeadPinServer(
	t *testing.T,
	transport *serverfake.ApiTestGitealikeTransport,
) *apiclient.Client {
	t.Helper()
	require := require.New(t)
	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	transport.Repo = gitealike.RepositoryDTO{
		ID:            101,
		Owner:         gitealike.UserDTO{UserName: "tea"},
		Name:          "kettle",
		FullName:      "tea/kettle",
		HTMLURL:       "https://gitea.test/tea/kettle",
		CloneURL:      "https://gitea.test/tea/kettle.git",
		DefaultBranch: "main",
		Created:       base,
		Updated:       base,
	}
	transport.Pulls = []gitealike.PullRequestDTO{{
		ID:      201,
		Index:   7,
		HTMLURL: "https://gitea.test/tea/kettle/pulls/7",
		Title:   "Add kettle",
		User:    gitealike.UserDTO{UserName: "alice"},
		State:   "open",
		Head:    gitealike.BranchDTO{Ref: "feature", SHA: "abc123"},
		Base:    gitealike.BranchDTO{Ref: "main", SHA: "def456"},
		Created: base,
		Updated: base,
	}}

	provider := gitealike.NewProvider(
		platform.KindGitea, "gitea.test", transport, gitealike.WithMutations(),
	)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitea,
			PlatformHost: "gitea.test",
			Owner:        "tea",
			Name:         "kettle",
			RepoPath:     "tea/kettle",
		}},
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	syncer.RunOnce(t.Context())
	repo, err := database.GetRepoByIdentity(t.Context(), db.RepoIdentity{
		Platform:     "gitea",
		PlatformHost: "gitea.test",
		RepoPath:     "tea/kettle",
	})
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateDiffSHAs(t.Context(), repo.ID, 7, "abc123", "def456", "merge-base"))
	return servertest.SetupTestClient(t, srv)
}

func decodeGitealikeConflict(t *testing.T, body []byte) (string, map[string]any) {
	t.Helper()
	var problem struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	require.NoError(t, json.Unmarshal(body, &problem))
	return problem.Code, problem.Details
}

func TestAPIGitealikePinnedMergeHeadMismatchIsStale(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	transport := &serverfake.ApiTestGitealikeTransport{
		MergeErr: &gitealike.HTTPError{
			StatusCode: 409,
			Message:    "head out of date",
		},
	}
	client := setupGitealikeHeadPinServer(t, transport)

	pin := "abc123"
	resp, err := client.HTTP.MergePullOnHostWithResponse(t.Context(), &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "t",
		CommitMessage:   "m",
		ExpectedHeadSha: &pin,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusConflict, resp.StatusCode)
	code, details := decodeGitealikeConflict(t, resp.Body)
	assert.Equal("conflict", code)
	require.NotNil(details)
	assert.Equal("stale_state", details["reason"],
		"the provider's head-mismatch rejection must surface as stale_state")
	assert.Equal("abc123", transport.LastMergeOpts.ExpectedHeadSHA,
		"the reviewed head must reach the provider as head_commit_id")
}

func TestAPIGitealikePinnedMergeGenericConflictStaysConflict(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	transport := &serverfake.ApiTestGitealikeTransport{
		MergeErr: &gitealike.HTTPError{
			StatusCode: 409,
			Message:    "merge conflict detected",
		},
	}
	client := setupGitealikeHeadPinServer(t, transport)

	pin := "abc123"
	resp, err := client.HTTP.MergePullOnHostWithResponse(t.Context(), &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "t",
		CommitMessage:   "m",
		ExpectedHeadSha: &pin,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusConflict, resp.StatusCode)
	code, details := decodeGitealikeConflict(t, resp.Body)
	assert.Equal("conflict", code)
	require.NotNil(details)
	assert.Equal("conflict", details["reason"],
		"an unrelated 409 must not present as a stale-head re-review flow")
}

func TestAPIGitealikeApproveSubmitsReview(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	transport := &serverfake.ApiTestGitealikeTransport{}
	client := setupGitealikeHeadPinServer(t, transport)

	pin := "abc123"
	resp, err := client.HTTP.ApprovePullOnHostWithResponse(t.Context(), &generated.ApprovePullOnHostRequestOptions{PathParams: &generated.ApprovePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.ApprovePullOnHostBody{
		Body:            "lgtm",
		ExpectedHeadSha: &pin,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	assert.Contains(transport.MutationCalls, "review:7:lgtm:abc123")
}

func TestAPIGitealikeApproveRefreshesAfterMutation(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	transport := &serverfake.ApiTestGitealikeTransport{}
	client := setupGitealikeHeadPinServer(t, transport)
	transport.HeadCalls = 0

	pin := "abc123"
	resp, err := client.HTTP.ApprovePullOnHostWithResponse(t.Context(), &generated.ApprovePullOnHostRequestOptions{PathParams: &generated.ApprovePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.ApprovePullOnHostBody{
		Body:            "lgtm",
		ExpectedHeadSha: &pin,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	assert.Equal(1, transport.HeadCalls)
	assert.Contains(transport.MutationCalls, "review:7:lgtm:abc123")
}

func TestAPIGitealikeMergeConflictReturnsConflict(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	unmergeable := false
	transport := &serverfake.ApiTestGitealikeTransport{
		MergeErr: &gitealike.HTTPError{StatusCode: http.StatusConflict, Message: "pull request is out of date"},
		Repo: gitealike.RepositoryDTO{
			ID:            101,
			Owner:         gitealike.UserDTO{UserName: "tea"},
			Name:          "kettle",
			FullName:      "tea/kettle",
			HTMLURL:       "https://gitea.test/tea/kettle",
			CloneURL:      "https://gitea.test/tea/kettle.git",
			DefaultBranch: "main",
			Created:       base,
			Updated:       base,
		},
		Pulls: []gitealike.PullRequestDTO{{
			ID:        201,
			Index:     7,
			HTMLURL:   "https://gitea.test/tea/kettle/pulls/7",
			Title:     "Add kettle",
			User:      gitealike.UserDTO{UserName: "alice"},
			State:     "open",
			Head:      gitealike.BranchDTO{Ref: "feature", SHA: "abc123"},
			Base:      gitealike.BranchDTO{Ref: "main", SHA: "def456"},
			Mergeable: &unmergeable,
			Created:   base,
			Updated:   base,
		}},
	}
	provider := gitealike.NewProvider(
		platform.KindGitea,
		"gitea.test",
		transport,
		gitealike.WithMutations(),
	)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitea,
			PlatformHost: "gitea.test",
			Owner:        "tea",
			Name:         "kettle",
			RepoPath:     "tea/kettle",
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)
	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitea",
		PlatformHost: "gitea.test",
		RepoPath:     "tea/kettle",
	})
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 7, "abc123", "def456", "merge-base"))
	expectedHeadSHA := serverfake.RequireMR(t, database, repo.ID, 7).PlatformHeadSHA
	assert.Equal("dirty", serverfake.RequireMR(t, database, repo.ID, 7).MergeableState)

	resp, err := client.HTTP.MergePullOnHostWithResponse(ctx, &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "Merge kettle",
		CommitMessage:   "Merge Gitea MR",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusConflict, resp.StatusCode, string(resp.Body))
	assert.Contains(string(resp.Body), "pull request is out of date")
	assert.Contains(transport.MutationCalls, "merge:7:squash")
	assert.Equal("Add kettle", serverfake.RequireMR(t, database, repo.ID, 7).Title)
}

func setupAPIGitealikeHeadPinServer(
	t *testing.T,
	transport *serverfake.ApiTestGitealikeTransport,
) (*apiclient.Client, *db.DB) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	provider := gitealike.NewProvider(
		platform.KindGitea,
		"gitea.test",
		transport,
		gitealike.WithMutations(),
	)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	database := dbtest.Open(t)
	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitea,
			PlatformHost: "gitea.test",
			Owner:        "tea",
			Name:         "kettle",
			RepoPath:     "tea/kettle",
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)
	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitea",
		PlatformHost: "gitea.test",
		RepoPath:     "tea/kettle",
	})
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 7, "abc123", "def456", "merge-base"))
	return client, database
}

func newAPIGitealikeHeadPinTransport(t *testing.T) *serverfake.ApiTestGitealikeTransport {
	t.Helper()
	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	mergeable := true
	return &serverfake.ApiTestGitealikeTransport{
		Repo: gitealike.RepositoryDTO{
			ID:            101,
			Owner:         gitealike.UserDTO{UserName: "tea"},
			Name:          "kettle",
			FullName:      "tea/kettle",
			HTMLURL:       "https://gitea.test/tea/kettle",
			CloneURL:      "https://gitea.test/tea/kettle.git",
			DefaultBranch: "main",
			Created:       base,
			Updated:       base,
		},
		Pulls: []gitealike.PullRequestDTO{{
			ID:        201,
			Index:     7,
			HTMLURL:   "https://gitea.test/tea/kettle/pulls/7",
			Title:     "Add kettle",
			User:      gitealike.UserDTO{UserName: "alice"},
			State:     "open",
			Head:      gitealike.BranchDTO{Ref: "feature", SHA: "abc123"},
			Base:      gitealike.BranchDTO{Ref: "main", SHA: "def456"},
			Mergeable: &mergeable,
			Created:   base,
			Updated:   base,
		}},
	}
}

func TestAPIGitealikeMergePassesReviewedHeadPinToProvider(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	transport := newAPIGitealikeHeadPinTransport(t)
	client, database := setupAPIGitealikeHeadPinServer(t, transport)
	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitea",
		PlatformHost: "gitea.test",
		RepoPath:     "tea/kettle",
	})
	require.NoError(err)
	require.NotNil(repo)
	require.Equal("abc123", serverfake.RequireMR(t, database, repo.ID, 7).PlatformHeadSHA)
	expectedHeadSHA := "abc123"

	resp, err := client.HTTP.MergePullOnHostWithResponse(ctx, &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "Merge kettle",
		CommitMessage:   "Merge Gitea PR",
		ExpectedHeadSha: &expectedHeadSHA,
	}})

	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	assert.Equal([]string{"abc123"}, transport.MergeHeadPins)
}

func TestAPIGitealikeMergeHeadMismatchMapsToStaleState(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	transport := newAPIGitealikeHeadPinTransport(t)
	transport.MergeErr = &gitealike.HTTPError{
		StatusCode: http.StatusConflict,
		Message:    "head target does not match",
	}
	client, _ := setupAPIGitealikeHeadPinServer(t, transport)
	expectedHeadSHA := "abc123"

	resp, err := client.HTTP.MergePullOnHostWithResponse(ctx, &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "Merge kettle",
		CommitMessage:   "Merge Gitea PR",
		ExpectedHeadSha: &expectedHeadSHA,
	}})

	require.Error(err)

	require.NotNil(resp)
	require.Equal(http.StatusConflict, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.Error)
	assert.Equal("conflict", string(resp.Error.Code))
	require.NotNil(resp.Error.Details)
	assert.Equal("stale_state", (resp.Error.Details)["reason"])
	assert.Equal([]string{"abc123"}, transport.MergeHeadPins)
}

func TestAPIGitealikeApproveBeforeHeadRace(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	transport := newAPIGitealikeHeadPinTransport(t)
	client, _ := setupAPIGitealikeHeadPinServer(t, transport)
	expectedHeadSHA := "abc123"

	resp, err := client.HTTP.ApprovePullOnHostWithResponse(ctx, &generated.ApprovePullOnHostRequestOptions{PathParams: &generated.ApprovePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.ApprovePullOnHostBody{
		Body:            "approved",
		ExpectedHeadSha: &expectedHeadSHA,
	}})

	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	assert.Contains(transport.MutationCalls, "review:7:approved:abc123")
}

func TestAPIGitealikeRequestChangesPassesReviewedHeadPinToProvider(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	transport := newAPIGitealikeHeadPinTransport(t)
	client, _ := setupAPIGitealikeHeadPinServer(t, transport)
	expectedHeadSHA := "abc123"

	resp, err := client.HTTP.RequestPullChangesOnHostWithResponse(ctx, &generated.RequestPullChangesOnHostRequestOptions{PathParams: &generated.RequestPullChangesOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.RequestPullChangesOnHostBody{
		Body:            "needs work",
		ExpectedHeadSha: &expectedHeadSHA,
	}})

	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	assert.Equal("REQUEST_CHANGES", transport.LastReviewOpts.State)
	assert.Equal("needs work", transport.LastReviewOpts.Body)
	assert.Equal("abc123", transport.LastReviewOpts.CommitID)
}

func setupGitHubCapabilityServerWithProvider(
	t *testing.T,
	caps *platform.Capabilities,
	headRepoCloneURL string,
) (*server.Server, *db.DB, *serverfake.ApiTestGitLabProvider) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitHub,
		Host:               "github.example.com",
		Owner:              "acme",
		Name:               "widget",
		RepoPath:           "acme/widget",
		PlatformID:         6262,
		PlatformExternalID: "6262",
		WebURL:             "https://github.example.com/acme/widget",
		CloneURL:           "https://github.example.com/acme/widget.git",
		DefaultBranch:      "main",
	}
	provider := &serverfake.ApiTestGitLabProvider{
		Ref:               ref,
		CapabilitiesValue: caps,
		MergeRequests: []platform.MergeRequest{{
			Repo:               ref,
			PlatformID:         7101,
			PlatformExternalID: "7101",
			Number:             7,
			URL:                "https://github.example.com/acme/widget/pull/7",
			Title:              "GitHub provider PR",
			Author:             "ada",
			State:              "open",
			HeadBranch:         "feature/github",
			HeadRepoCloneURL:   headRepoCloneURL,
			BaseBranch:         "main",
			HeadSHA:            "abc123",
			BaseSHA:            "def456",
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	repo := ghclient.RepoRef{
		Platform:           platform.KindGitHub,
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.example.com",
		RepoPath:           "acme/widget",
		PlatformRepoID:     6262,
		PlatformExternalID: "6262",
		WebURL:             "https://github.example.com/acme/widget",
		CloneURL:           "https://github.example.com/acme/widget.git",
		DefaultBranch:      "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	return srv, database, provider
}

func setupForgejoCapabilityServerWithProvider(
	t *testing.T,
	caps *platform.Capabilities,
) (*server.Server, *db.DB, *serverfake.ApiTestGitLabProvider) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindForgejo,
		Host:               "codeberg.org",
		Owner:              "acme",
		Name:               "widgets",
		RepoPath:           "acme/widgets",
		PlatformID:         5252,
		PlatformExternalID: "5252",
		WebURL:             "https://codeberg.org/acme/widgets",
		CloneURL:           "https://codeberg.org/acme/widgets.git",
		DefaultBranch:      "main",
	}
	provider := &serverfake.ApiTestGitLabProvider{
		Ref:               ref,
		CapabilitiesValue: caps,
		MergeRequests: []platform.MergeRequest{{
			Repo:               ref,
			PlatformID:         9001,
			PlatformExternalID: "9001",
			Number:             42,
			URL:                "https://codeberg.org/acme/widgets/pulls/42",
			Title:              "Forgejo provider PR",
			Author:             "ada",
			State:              "open",
			HeadBranch:         "feature/forgejo",
			BaseBranch:         "main",
			HeadSHA:            "abc123",
			BaseSHA:            "def456",
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	repo := ghclient.RepoRef{
		Platform:           platform.KindForgejo,
		Owner:              "acme",
		Name:               "widgets",
		PlatformHost:       "codeberg.org",
		RepoPath:           "acme/widgets",
		PlatformRepoID:     5252,
		PlatformExternalID: "5252",
		WebURL:             "https://codeberg.org/acme/widgets",
		CloneURL:           "https://codeberg.org/acme/widgets.git",
		DefaultBranch:      "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	return srv, database, provider
}

func TestAPIGetStackForPR_DraftNotBaseReady(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)

	// Draft base with green CI + approval; non-draft tip pending.
	serverfake.SeedStackedPRDraft(t, database, "acme", "widget", 10, "feat/x", "main", db.MergeRequestStateOpen, "success", "APPROVED", true)
	serverfake.SeedStackedPR(t, database, "acme", "widget", 11, "feat/y", "feat/x", db.MergeRequestStateOpen, "pending", "")
	serverfake.RunStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullStackWithResponse(t.Context(), &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotNil(t, resp.JSON200)
	assert.NotEqual("base_ready", resp.JSON200.Health, "draft base must not be base_ready")
	assert.NotEqual("all_green", resp.JSON200.Health, "draft stack must not be all_green")
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

// TestMergeBlocksPredecessorPreservedByNativeStackOverlapFallback closes the
// seam between stack detection and the merge safeguard. Detection tests prove
// the projection and pullapi tests prove the guard blocks on hand-seeded rows;
// neither proves the rows detection writes for an overlap actually preserve the
// preceding blocker the guard reads.
func TestMergeBlocksPredecessorPreservedByNativeStackOverlapFallback(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	merged := false
	mock := &serverfake.MockGH{
		MergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			merged = true
			return &gh.PullRequestMergeResult{}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	ctx := t.Context()
	serverfake.SeedStackedPR(t, database, "acme", "widget", 100, "feature/a", "main", db.MergeRequestStateOpen, "", "")
	serverfake.SeedStackedPR(t, database, "acme", "widget", 101, "feature/b", "feature/a", db.MergeRequestStateMerged, "", "")
	tipHeadSHA := "sha102"
	serverfake.SeedStackedPR(t, database, "acme", "widget", 102, "feature/c", "feature/b", db.MergeRequestStateOpen, "", "")
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	now := time.Now().UTC()
	// Two confirmed stacks share merged PR 101, and stack 42's leading member
	// has no row yet, so the overlap is only visible from declared membership.
	require.NoError(database.ReplaceGitHubNativeStack(ctx, db.GitHubNativeStack{
		RepoID: repo.ID, GitHubID: 9043, Number: 43, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native-43", LastObservedAt: now,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 101, State: "merged", HeadRef: "feature/b", HeadSHA: "sha101"},
			{Position: 2, PullRequestNumber: 102, State: "open", HeadRef: "feature/c", HeadSHA: tipHeadSHA},
		},
	}))
	require.NoError(database.ReplaceGitHubNativeStack(ctx, db.GitHubNativeStack{
		RepoID: repo.ID, GitHubID: 9042, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native-42", LastObservedAt: now,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 900, State: "open", HeadRef: "feature/z", HeadSHA: "sha900"},
			{Position: 2, PullRequestNumber: 101, State: "merged", HeadRef: "feature/b", HeadSHA: "sha101"},
		},
	}))
	require.NoError(stacks.RunDetectionWithNativeStacks(ctx, database, repo.ID, []int{42, 43}))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(ctx, &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(102)}, Body: &generated.MergePRInputBody{
		Method:          "squash",
		ExpectedHeadSha: &tipHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)

	assert.Equal(http.StatusConflict, resp.StatusCode)
	assert.Contains(string(resp.Body), `"reason":"mid_stack_merge_disallowed"`)
	assert.Contains(string(resp.Body), `"blocking_number":100`)
	assert.False(merged, "the provider must not be asked to merge past an open predecessor")
}

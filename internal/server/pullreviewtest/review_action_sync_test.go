package pullreviewtest

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/server/workspaceapi"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"
)

type reviewActionCase struct {
	name   string
	submit func(context.Context, *apiclient.Client) (int, error)
}

func reviewActionCases() []reviewActionCase {
	path := func() (provider, owner, name string, number int64) { return "gh", "acme", "widget", 1 }
	return []reviewActionCase{
		{
			name: "approve",
			submit: func(ctx context.Context, client *apiclient.Client) (int, error) {
				provider, owner, name, number := path()
				resp, err := client.HTTP.ApprovePullWithResponse(ctx, &generated.ApprovePullRequestOptions{
					PathParams: &generated.ApprovePullPath{Provider: provider, Owner: owner, Name: name, Number: number},
					Body:       &generated.ApprovePullBody{Body: "looks good"},
				})
				if resp == nil {
					return 0, err
				}
				return resp.StatusCode, nil
			},
		},
		{
			name: "request changes",
			submit: func(ctx context.Context, client *apiclient.Client) (int, error) {
				provider, owner, name, number := path()
				resp, err := client.HTTP.RequestPullChangesWithResponse(ctx, &generated.RequestPullChangesRequestOptions{
					PathParams: &generated.RequestPullChangesPath{Provider: provider, Owner: owner, Name: name, Number: number},
					Body:       &generated.RequestPullChangesBody{Body: "please split this"},
				})
				if resp == nil {
					return 0, err
				}
				return resp.StatusCode, nil
			},
		},
	}
}

// The provider accepting a review is the request's outcome; the full pull
// request re-sync after it must not hold the response, and must still land.
func TestAPIReviewActionReturnsBeforePostMutationSync(t *testing.T) {
	for _, tc := range reviewActionCases() {
		t.Run(tc.name, func(t *testing.T) {
			serverfake.RunParallelServerTest(t)
			require := require.New(t)
			assert := assert.New(t)
			ctx := t.Context()

			readStarted := make(chan struct{}, 1)
			releaseRead := make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-releaseRead:
				default:
					close(releaseRead)
				}
			})
			mock := &serverfake.MockGH{
				CreateReviewWithCommentsFn: func(
					_ context.Context, _, _ string, _ int,
					event, body, _ string, _ []*gh.DraftReviewComment,
				) (*gh.PullRequestReview, error) {
					submitted := gh.Timestamp{Time: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
					return &gh.PullRequestReview{
						ID: new(int64(301)), State: &event, Body: &body,
						SubmittedAt: &submitted, User: &gh.User{Login: new("reviewer")},
					}, nil
				},
				GetPullRequestFn: func(readCtx context.Context, _, _ string, number int) (*gh.PullRequest, error) {
					select {
					case readStarted <- struct{}{}:
					default:
					}
					select {
					case <-releaseRead:
					case <-readCtx.Done():
						return nil, readCtx.Err()
					}
					updated := gh.Timestamp{Time: time.Now().UTC()}
					return &gh.PullRequest{
						ID: new(int64(1000)), Number: &number, State: new("open"),
						Title:     new("Title from provider sync"),
						HTMLURL:   new("https://github.com/acme/widget/pull/1"),
						CreatedAt: &updated, UpdatedAt: &updated,
						Head: &gh.PullRequestBranch{SHA: new("synced-head"), Ref: new("feature")},
						Base: &gh.PullRequestBranch{Ref: new("main")},
					}, nil
				},
			}
			srv, database := servertest.SetupTestServerWithMock(t, mock)
			serverfake.SeedPR(t, database, "acme", "widget", 1)
			client := servertest.SetupTestClient(t, srv)
			events, _ := srv.Hub().Subscribe(ctx, false)

			type result struct {
				status int
				err    error
			}
			done := make(chan result, 1)
			go func() {
				status, err := tc.submit(ctx, client)
				done <- result{status, err}
			}()

			// The provider read stays blocked, so a handler that waited on the
			// re-sync could not answer here.
			var got result
			select {
			case got = <-done:
			case <-time.After(10 * time.Second):
				require.FailNow("review action waited for the post-mutation sync")
			}
			require.NoError(got.err)
			require.Equal(http.StatusOK, got.status)

			select {
			case <-readStarted:
			case <-time.After(10 * time.Second):
				require.FailNow("post-mutation sync never read the pull request")
			}
			close(releaseRead)

			assert.Eventually(func() bool {
				mr, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 1)
				return err == nil && mr != nil && mr.Title == "Title from provider sync" &&
					mr.PlatformHeadSHA == "synced-head"
			}, 10*time.Second, 20*time.Millisecond)

			deadline := time.After(10 * time.Second)
			for {
				select {
				case event := <-events:
					payload, ok := event.Event.Data.(workspaceapi.PRDetailRefreshedPayload)
					if event.Event.Type != "pr_detail_refreshed" || !ok || payload.Number != 1 {
						continue
					}
					assert.Equal("acme", payload.Owner)
					assert.Equal("widget", payload.Name)
					assert.Equal("synced-head", payload.HeadSHA)
					return
				case <-deadline:
					require.FailNow("background sync did not tell clients to reload the detail")
				}
			}
		})
	}
}

func TestAPIReviewActionReturnsProviderRejection(t *testing.T) {
	for _, tc := range reviewActionCases() {
		t.Run(tc.name, func(t *testing.T) {
			serverfake.RunParallelServerTest(t)
			require := require.New(t)

			mock := &serverfake.MockGH{
				CreateReviewWithCommentsFn: func(
					context.Context, string, string, int,
					string, string, string, []*gh.DraftReviewComment,
				) (*gh.PullRequestReview, error) {
					return nil, &gh.ErrorResponse{
						Response: &http.Response{StatusCode: http.StatusForbidden},
						Message:  "Resource not accessible by integration",
					}
				},
			}
			srv, database := servertest.SetupTestServerWithMock(t, mock)
			serverfake.SeedPR(t, database, "acme", "widget", 1)
			client := servertest.SetupTestClient(t, srv)

			// A provider rejection is still the request's answer, not a
			// success with a background sync behind it.
			status, _ := tc.submit(t.Context(), client)
			require.Equal(http.StatusBadGateway, status)
		})
	}
}

// A push that lands while the post-approval sync runs gives the pull request
// a head whose runs this approval did not cover; that head's pending state
// must survive the approval's cleanup.
func TestAPIApproveWorkflowsKeepsPendingStateOfNewerHead(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	var prReads atomic.Int64
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			head := "approved-head"
			if prReads.Add(1) > 1 {
				head = "pushed-head"
			}
			updated := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID: new(int64(1000)), Number: &number, State: new("open"),
				Title:     new("Workflow PR"),
				HTMLURL:   new("https://github.com/acme/widget/pull/1"),
				CreatedAt: &updated, UpdatedAt: &updated,
				Head: &gh.PullRequestBranch{SHA: &head, Ref: new("feature")},
				Base: &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		ListWorkflowRunsForHeadFn: func(_ context.Context, _, _, headSHA string) ([]*gh.WorkflowRun, error) {
			return []*gh.WorkflowRun{{
				ID: new(int64(70)), HeadSHA: &headSHA, Event: new("pull_request"),
				PullRequests: []*gh.PullRequest{{Number: new(1)}},
			}}, nil
		},
		ApproveWorkflowRunFn: func(context.Context, string, string, int64) error { return nil },
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	client := servertest.SetupTestClient(t, srv)
	events, _ := srv.Hub().Subscribe(t.Context(), false)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(t.Context(), &generated.ApprovePullWorkflowsRequestOptions{PathParams: &generated.ApprovePullWorkflowsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))

	deadline := time.After(10 * time.Second)
	for refreshed := false; !refreshed; {
		select {
		case event := <-events:
			refreshed = event.Event.Type == "pr_detail_refreshed"
		case <-deadline:
			require.FailNow("workflow approval sync did not finish")
		}
	}
	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal("pushed-head", pr.PlatformHeadSHA)
	assert.Equal("pushed-head", pr.WorkflowApprovalHeadSHA)
	assert.True(pr.WorkflowApprovalRequired)
	assert.Equal(1, pr.WorkflowApprovalCount)
}

package server

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/syncevents"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestEnqueueDetailSyncOrRerunRunsPendingAfterInFlight(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, _, _ := setupTestServer(t)

	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		secondDone := make(chan struct{})
		var calls atomic.Int64

		started := srv.syncevents.EnqueueDetailSyncOrRerun("pr:github:github.com:acme/widget#7", nil, func(context.Context) error {
			calls.Add(1)
			close(firstStarted)
			<-releaseFirst
			return nil
		})
		require.True(started)
		synctest.Wait()
		<-firstStarted

		started = srv.syncevents.EnqueueDetailSyncOrRerun("pr:github:github.com:acme/widget#7", nil, func(context.Context) error {
			calls.Add(1)
			close(secondDone)
			return nil
		})
		assert.False(started, "duplicate detail sync should report deduped while queuing a rerun")

		close(releaseFirst)
		synctest.Wait()
		<-secondDone
		assert.Equal(int64(2), calls.Load())
		srv.bg.Wait()
	})
}

func TestEnqueueDetailSyncOrRerunKeepsReplacedCompletion(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, _, _ := setupTestServer(t)

	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		const key = "pr:github:github.com:acme/widget#7"
		releaseFirst := make(chan struct{})
		var order []string
		record := func(step string) func(context.Context) error {
			return func(context.Context) error {
				order = append(order, step)
				return nil
			}
		}

		require.True(srv.syncevents.EnqueueDetailSyncOrRerun(key, nil, func(context.Context) error {
			<-releaseFirst
			return nil
		}))
		synctest.Wait()

		// A mutation queues a rerun with follow-up work, then a plain refresh
		// replaces that rerun before the in-flight sync finishes.
		require.False(srv.syncevents.EnqueueDetailSyncOrRerunWithCompletion(
			key, nil, record("mutation sync"),
			func(context.Context) { order = append(order, "mutation completion") },
		))
		require.False(srv.syncevents.EnqueueDetailSyncOrRerun(key, nil, record("refresh sync")))

		close(releaseFirst)
		synctest.Wait()
		srv.bg.Wait()
		assert.Equal([]string{"refresh sync", "mutation completion"}, order)
	})
}

func TestEnqueuePRSyncBroadcastsChangesToChecksAndComments(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	when := gh.Timestamp{Time: time.Now().UTC().Add(time.Minute).Truncate(time.Second)}
	comment := "original"
	commentWhen := gh.Timestamp{Time: when.Add(time.Minute)}
	check := "success"
	mock := &serverfake.MockGH{
		GetPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			return &gh.PullRequest{ID: new(int64(1000)), Number: &number, State: new("open"), Title: new("Example pull"), Body: new("body"), CreatedAt: &when, UpdatedAt: &when, Head: &gh.PullRequestBranch{SHA: new("abc123"), Ref: new("feature")}, Base: &gh.PullRequestBranch{Ref: new("main")}}, nil
		},
		ListIssueCommentsFn: func(context.Context, string, string, int) ([]*gh.IssueComment, error) {
			return []*gh.IssueComment{{ID: new(int64(99)), Body: &comment, CreatedAt: &commentWhen, UpdatedAt: &commentWhen}}, nil
		},
		ListCheckRunsForRefFn: func(context.Context, string, string, string) ([]*gh.CheckRun, error) {
			return []*gh.CheckRun{{ID: new(int64(1)), Name: new("build"), Status: new("completed"), Conclusion: &check}}, nil
		},
	}
	srv, database, _ := setupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	input := &itemapi.RepoNumberInput{Provider: "github", Owner: "acme", Name: "widget", Number: 1}
	_, err := srv.activityapi.EnqueuePRSync(t.Context(), input)
	require.NoError(t, err)
	srv.bg.Wait()
	oldHub := srv.hub
	srv.hub = syncevents.NewEventHub()
	oldHub.Close()
	t.Cleanup(srv.hub.Close)
	events, _ := srv.hub.Subscribe(t.Context(), false)
	_, err = srv.activityapi.EnqueuePRSync(t.Context(), input)
	require.NoError(t, err)
	srv.bg.Wait()
	select {
	case event := <-events:
		require.FailNow(t, "unchanged pull refresh sent event", "%+v", event)
	default:
	}
	comment = "edited"
	check = "failure"
	_, err = srv.activityapi.EnqueuePRSync(t.Context(), input)
	require.NoError(t, err)
	srv.bg.Wait()
	select {
	case event := <-events:
		require.Equal(t, "data_changed", event.Event.Type)
		payload, err := json.Marshal(event.Event.Data)
		require.NoError(t, err)
		var data struct {
			Repositories []struct {
				Provider     string `json:"provider"`
				PlatformHost string `json:"platform_host"`
				RepoPath     string `json:"repo_path"`
			} `json:"repositories"`
		}
		require.NoError(t, json.Unmarshal(payload, &data))
		require.Len(t, data.Repositories, 1)
		require.Equal(t, "github", data.Repositories[0].Provider)
		require.Equal(t, "github.com", data.Repositories[0].PlatformHost)
		require.Equal(t, "acme/widget", data.Repositories[0].RepoPath)
		var legacy struct{}
		require.NoError(t, json.Unmarshal(payload, &legacy))
	default:
		require.FailNow(t, "changed pull refresh did not broadcast")
	}
	select {
	case event := <-events:
		require.FailNow(t, "extra event", "%+v", event)
	default:
	}
	stored, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(t, err)
	require.Equal(t, "failure", stored.CIStatus)
	eventRows, err := database.ListMREvents(t.Context(), stored.ID)
	require.NoError(t, err)
	require.Len(t, eventRows, 1)
	require.Equal(t, "edited", eventRows[0].Body)
}

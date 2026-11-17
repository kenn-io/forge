package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
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

func TestEnqueueIssueSyncBroadcastsOnlyPersistedChanges(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	body := "original"
	when := gh.Timestamp{Time: time.Now().UTC().Add(time.Minute).Truncate(time.Second)}
	commentWhen := gh.Timestamp{Time: when.Time.Add(time.Minute)}
	mock := &serverfake.MockGH{GetIssueFn: func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
		return &gh.Issue{ID: new(int64(202)), Number: &number, State: new("open"), Title: new("Example issue"), Body: &body, CreatedAt: &when, UpdatedAt: &when}, nil
	}, ListIssueCommentsFn: func(context.Context, string, string, int) ([]*gh.IssueComment, error) {
		return []*gh.IssueComment{{ID: new(int64(99)), Body: new("comment"), CreatedAt: &commentWhen, UpdatedAt: &commentWhen}}, nil
	}}
	srv, database, _ := setupTestServerWithMock(t, mock)
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	input := &itemapi.IssueRepoNumberInput{Provider: "github", Owner: "acme", Name: "widget", Number: 5}
	_, err := srv.activityapi.EnqueueIssueSync(t.Context(), input)
	require.NoError(t, err)
	srv.bg.Wait()

	oldHub := srv.hub
	srv.hub = syncevents.NewEventHub()
	oldHub.Close()
	t.Cleanup(srv.hub.Close)
	events, _ := srv.hub.Subscribe(t.Context(), false)
	_, err = srv.activityapi.EnqueueIssueSync(t.Context(), input)
	require.NoError(t, err)
	srv.bg.Wait()
	select {
	case event := <-events:
		require.FailNow(t, "unchanged refresh sent event", "%+v", event)
	default:
	}

	body = "edited"
	_, err = srv.activityapi.EnqueueIssueSync(t.Context(), input)
	require.NoError(t, err)
	srv.bg.Wait()
	select {
	case event := <-events:
		require.Equal(t, "data_changed", event.Event.Type)
		payload, err := json.Marshal(event.Event.Data)
		require.NoError(t, err)
		var decoded struct {
			Repositories []struct {
				Provider string `json:"provider"`
				Host     string `json:"platform_host"`
				Path     string `json:"repo_path"`
				ID       int64  `json:"platform_repo_id"`
			} `json:"repositories"`
		}
		require.NoError(t, json.Unmarshal(payload, &decoded))
		require.Len(t, decoded.Repositories, 1)
		require.Equal(t, "github", decoded.Repositories[0].Provider)
		require.Equal(t, "github.com", decoded.Repositories[0].Host)
		require.Equal(t, "acme/widget", decoded.Repositories[0].Path)
		require.Positive(t, decoded.Repositories[0].ID)
		var legacy struct{}
		require.NoError(t, json.Unmarshal(payload, &legacy))
	default:
		require.FailNow(t, "changed refresh did not broadcast")
	}
	select {
	case event := <-events:
		require.FailNow(t, "changed refresh sent an extra event", "%+v", event)
	default:
	}
	stored, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(t, err)
	require.Equal(t, "edited", stored.Body)
}

func TestEnqueuePRSyncBroadcastsChangesToChecksAndComments(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	when := gh.Timestamp{Time: time.Now().UTC().Add(time.Minute).Truncate(time.Second)}
	comment := "original"
	commentWhen := gh.Timestamp{Time: when.Time.Add(time.Minute)}
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
		require.Contains(t, string(payload), `"repo_path":"acme/widget"`)
	default:
		require.FailNow(t, "changed pull refresh did not broadcast")
	}
	stored, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(t, err)
	require.Equal(t, "failure", stored.CIStatus)
	eventRows, err := database.ListMREvents(t.Context(), stored.ID)
	require.NoError(t, err)
	require.Len(t, eventRows, 1)
	require.Equal(t, "edited", eventRows[0].Body)
}

func TestRepositoryDetailSyncPublishesPersistedChangesAfterFailure(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, database, _ := setupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	mr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(t, err)
	require.NotNil(t, mr)
	repo, err := database.GetRepoByID(t.Context(), mr.RepoID)
	require.NoError(t, err)
	require.NotNil(t, repo)
	eventRepo := syncevents.RepositoryEvent{Provider: repo.Platform, PlatformHost: repo.PlatformHost, RepoPath: repo.RepoPath, Key: repo.Key}
	events, _ := srv.hub.Subscribe(t.Context(), false)
	sync := func(body string, syncErr error) {
		require.True(t, srv.syncevents.EnqueueRepositoryDetailSync("acceptance", nil, eventRepo, func(ctx context.Context) (bool, error) {
			ctx, changed := db.TrackDetailChanges(ctx, repo.ID, 1, false)
			current, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 1)
			if err != nil {
				return false, err
			}
			current.Body = body
			_, _, _, err = database.UpsertMergeRequestSnapshotWithLabels(ctx, current)
			if err != nil {
				return changed(), err
			}
			return changed(), syncErr
		}))
		srv.bg.Wait()
	}
	sync(mr.Body, nil)
	select {
	case event := <-events:
		require.FailNow(t, "unchanged refresh broadcast", "%+v", event)
	default:
	}
	sync("edited", errors.New("later child fetch failed"))
	stored, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repo.ID, 1)
	require.NoError(t, err)
	require.Equal(t, "edited", stored.Body)
	select {
	case event := <-events:
		require.Equal(t, "data_changed", event.Event.Type)
		payload, err := json.Marshal(event.Event.Data)
		require.NoError(t, err)
		require.Contains(t, string(payload), `"repo_path":"acme/widget"`)
		var legacy struct{}
		require.NoError(t, json.Unmarshal(payload, &legacy))
	default:
		require.FailNow(t, "persisted change was not broadcast")
	}
	select {
	case event := <-events:
		require.FailNow(t, "extra event", "%+v", event)
	default:
	}
}

func TestRepositoryDetailSyncIncludesCompletionWrites(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, database, _ := setupTestServer(t)
	serverfake.SeedPR(t, database, "acme", "widget", 1)
	mr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(t, err)
	repo, err := database.GetRepoByID(t.Context(), mr.RepoID)
	require.NoError(t, err)
	events, _ := srv.hub.Subscribe(t.Context(), false)
	result := make(chan error, 1)
	require.True(t, srv.syncevents.EnqueueRepositoryDetailSyncWithCompletion("completion", nil, *repo, 1, func(context.Context) error { return nil }, func(ctx context.Context) {
		result <- database.UpdateMRCIStatusForHead(ctx, repo.ID, 1, mr.PlatformHeadSHA, "failure", "[]", false, nil)
	}))
	srv.bg.Wait()
	require.NoError(t, <-result)
	stored, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repo.ID, 1)
	require.NoError(t, err)
	require.Equal(t, "failure", stored.CIStatus)
	select {
	case event := <-events:
		require.Equal(t, "data_changed", event.Event.Type)
		payload, err := json.Marshal(event.Event.Data)
		require.NoError(t, err)
		require.Contains(t, string(payload), `"repo_path":"acme/widget"`)
	default:
		require.FailNow(t, "completion write was not broadcast")
	}
}

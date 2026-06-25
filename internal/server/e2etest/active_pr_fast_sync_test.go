package e2etest

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestActivePRFastSyncRefreshesActivityAndBroadcastsDataChangedE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	oldActivity := now.Add(-30 * time.Minute)
	newActivity := now.Add(-1 * time.Minute)

	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "101",
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
		Title:           "Keep Activity fresh",
		Author:          "author",
		State:           db.MergeRequestStateOpen,
		PlatformHeadSHA: "head-sha",
		PlatformBaseSHA: "base-sha",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		CreatedAt:       now.Add(-2 * time.Hour),
		UpdatedAt:       oldActivity,
		LastActivityAt:  oldActivity,
	})
	require.NoError(err)

	mock := &mockGH{
		getPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
			return githubPullRequest(7, "Keep Activity fresh", "author", newActivity), nil
		},
	}
	mock.listIssueCommentsFn = func(context.Context, string, string, int) ([]*gh.IssueComment, error) {
		return []*gh.IssueComment{githubIssueComment(9001, "reviewer", "new activity", newActivity)}, nil
	}

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitHub,
			Owner:        "acme",
			Name:         "widget",
			PlatformHost: "github.com",
			RepoPath:     "acme/widget",
		}},
		time.Hour, nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(50)},
	)
	t.Cleanup(syncer.Stop)

	cfg := &config.Config{
		SyncInterval:            "1h",
		ActivePRRefreshInterval: "10ms",
		ActivePRWindow:          "4h",
	}
	syncer.SetWatchInterval(cfg.ActivePRRefreshDuration())
	syncer.SetActiveMRWindow(cfg.ActivePRWindowDuration())

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	syncer.SetOnWatchedMRSyncCompleted(func() {
		srv.Hub().Broadcast(server.Event{Type: "data_changed", Data: struct{}{}})
	})

	ts := httptest.NewServer(srv)
	defer ts.Close()
	api, err := apiclient.NewWithHTTPClient(ts.URL, ts.Client())
	require.NoError(err)

	eventDone := waitForSSEEvent(t, ts, "data_changed")
	syncer.Start(ctx)

	select {
	case err := <-eventDone:
		require.NoError(err)
	case <-time.After(2 * time.Second):
		require.FailNow("timed out waiting for data_changed")
	}

	since := newActivity.Add(-time.Hour).Format(time.RFC3339)
	activity, err := api.HTTP.ListActivityWithResponse(
		ctx,
		&generated.ListActivityParams{Since: &since},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, activity.StatusCode(), string(activity.Body))
	require.NotNil(activity.JSON200)
	require.NotNil(activity.JSON200.Items)
	require.NotEmpty(*activity.JSON200.Items)

	first := (*activity.JSON200.Items)[0]
	assert.Equal("comment", first.ActivityType)
	assert.Equal(int64(7), first.ItemNumber)
	assert.Equal("new activity", first.BodyPreview)
}

func waitForSSEEvent(t *testing.T, ts *httptest.Server, eventType string) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/events", http.NoBody)
		if err != nil {
			done <- err
			return
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			done <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			done <- fmt.Errorf("events status %d", resp.StatusCode)
			return
		}
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "event: "+eventType {
				done <- nil
				return
			}
		}
		if err := scanner.Err(); err != nil {
			done <- err
			return
		}
		done <- fmt.Errorf("events stream closed before %s", eventType)
	}()
	return done
}

func githubPullRequest(number int, title, author string, updatedAt time.Time) *gh.PullRequest {
	state := "open"
	body := ""
	url := "https://github.com/acme/widget/pull/7"
	headRef := "feature"
	baseRef := "main"
	headSHA := "head-sha"
	baseSHA := "base-sha"
	additions := 1
	deletions := 1
	return &gh.PullRequest{
		Number:    &number,
		Title:     &title,
		State:     &state,
		Body:      &body,
		HTMLURL:   &url,
		User:      &gh.User{Login: &author},
		Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
		Base:      &gh.PullRequestBranch{Ref: &baseRef, SHA: &baseSHA},
		Additions: &additions,
		Deletions: &deletions,
		CreatedAt: &gh.Timestamp{Time: updatedAt.Add(-2 * time.Hour)},
		UpdatedAt: &gh.Timestamp{Time: updatedAt},
	}
}

func githubIssueComment(id int64, author, body string, updatedAt time.Time) *gh.IssueComment {
	url := "https://github.com/acme/widget/pull/7#issuecomment-9001"
	return &gh.IssueComment{
		ID:        &id,
		User:      &gh.User{Login: &author},
		Body:      &body,
		HTMLURL:   &url,
		CreatedAt: &gh.Timestamp{Time: updatedAt},
		UpdatedAt: &gh.Timestamp{Time: updatedAt},
	}
}

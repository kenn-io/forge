package e2etest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func TestIssueSyncCannotReplaceEqualTimestampArchiveSnapshotE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.com",
		Owner:              "acme",
		Name:               "widget",
		RepoPath:           "acme/widget",
		PlatformID:         1234,
		PlatformExternalID: "gid://gitlab/Project/1234",
		DefaultBranch:      "main",
		WebURL:             "https://gitlab.com/acme/widget",
		CloneURL:           "https://gitlab.com/acme/widget.git",
	}
	provider := &blockingIssueEventsProvider{
		gitLabDiscussionProvider: gitLabDiscussionProvider{
			ref: ref,
			issues: map[int]platform.Issue{11: {
				Repo:               ref,
				PlatformID:         3001,
				PlatformExternalID: "gid://gitlab/Issue/3001",
				Number:             11,
				URL:                "https://gitlab.com/acme/widget/-/issues/11",
				Title:              "Provider parent",
				Author:             "author",
				State:              "open",
				CreatedAt:          now.Add(-time.Hour),
				UpdatedAt:          now,
				LastActivityAt:     now,
			}},
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
		staleEvents: []platform.IssueEvent{{
			Repo:               ref,
			PlatformID:         4001,
			PlatformExternalID: "gid://gitlab/Note/4001",
			IssueNumber:        11,
			EventType:          "issue_comment",
			Author:             "provider",
			Body:               "stale normal-sync comment",
			CreatedAt:          now,
			DedupeKey:          "gitlab-issue-note-4001",
		}},
	}
	defer provider.unblock()

	database := dbtest.Open(t)
	_, err := database.UpsertRepo(ctx, platform.DBRepoIdentity(ref))
	require.NoError(err)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
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
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	httpServer := httptest.NewServer(srv)
	defer httpServer.Close()
	client, err := apiclient.NewWithHTTPClient(httpServer.URL, httpServer.Client())
	require.NoError(err)

	type syncResult struct {
		response *generated.SyncIssueResponse
		err      error
	}
	result := make(chan syncResult, 1)
	go func() {
		response, syncErr := client.HTTP.SyncIssueWithResponse(
			ctx, "gitlab", ref.Owner, ref.Name, 11,
			func(_ context.Context, request *http.Request) error {
				request.Header.Set("Content-Type", "application/json")
				return nil
			},
		)
		result <- syncResult{response: response, err: syncErr}
	}()

	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		require.Fail("provider issue-event fetch did not start")
	}

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: ref.Host,
		Owner:        ref.Owner,
		Name:         ref.Name,
	})
	require.NoError(err)
	require.NotNil(repo)
	parent, err := database.GetIssueByRepoIDAndNumber(ctx, repo.ID, 11)
	require.NoError(err)
	require.NotNil(parent)
	archiveParent := *parent
	archiveParent.Title = "Archive parent"
	issueID, archiveRevision, accepted, err := database.UpsertArchiveIssueSnapshot(ctx, &archiveParent)
	require.NoError(err)
	require.True(accepted)
	applied, err := database.CommitIssueChildSnapshot(ctx, db.IssueChildSnapshot{
		IssueID:          issueID,
		ExpectedRevision: archiveRevision,
		Comments: []db.IssueEvent{{
			IssueID:            issueID,
			PlatformID:         int64Pointer(5001),
			PlatformExternalID: "gid://gitlab/Note/5001",
			EventType:          "issue_comment",
			Author:             "archive",
			Body:               "archive comment",
			CreatedAt:          now,
			DedupeKey:          "gitlab-issue-note-5001",
		}},
	})
	require.NoError(err)
	require.True(applied)
	provider.unblock()

	var syncResponse syncResult
	select {
	case syncResponse = <-result:
	case <-time.After(5 * time.Second):
		require.Fail("issue sync did not finish")
	}
	require.NoError(syncResponse.err)
	require.NotNil(syncResponse.response)
	require.Equal(http.StatusOK, syncResponse.response.StatusCode(), string(syncResponse.response.Body))

	detail, err := client.HTTP.GetIssueWithResponse(
		ctx, "gitlab", ref.Owner, ref.Name, 11,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, detail.StatusCode(), string(detail.Body))
	require.NotNil(detail.JSON200)
	assert.Equal("Archive parent", detail.JSON200.Issue.Title)
	require.NotNil(detail.JSON200.Events)
	require.Len(*detail.JSON200.Events, 1)
	assert.Equal("archive comment", (*detail.JSON200.Events)[0].Body)
	assert.Equal("gitlab-issue-note-5001", (*detail.JSON200.Events)[0].DedupeKey)
}

func TestGitHubIssueSyncTreatsEqualTimestampArchiveWinnerAsSuccessE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 14, 13, 0, 0, 0, time.UTC)
	commentsEntered := make(chan struct{})
	commentsRelease := make(chan struct{})
	var enteredOnce sync.Once

	providerMux := http.NewServeMux()
	providerMux.HandleFunc("/api/v3/repos/acme/widget/issues/11", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 3001, "node_id": "I_3001", "number": 11,
			"html_url": "https://github.com/acme/widget/issues/11",
			"title":    "Provider parent", "state": "open",
			"user":       map[string]any{"login": "author"},
			"created_at": now.Add(-time.Hour).Format(time.RFC3339),
			"updated_at": now.Format(time.RFC3339),
		})
	})
	providerMux.HandleFunc("/api/v3/repos/acme/widget/issues/11/comments", func(w http.ResponseWriter, _ *http.Request) {
		enteredOnce.Do(func() { close(commentsEntered) })
		<-commentsRelease
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": 4001, "node_id": "IC_4001", "body": "stale normal-sync comment",
			"user":       map[string]any{"login": "provider"},
			"created_at": now.Format(time.RFC3339),
			"updated_at": now.Format(time.RFC3339),
		}})
	})
	providerMux.HandleFunc("/api/v3/repos/acme/widget/issues/11/timeline", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	providerServer := httptest.NewServer(providerMux)
	defer providerServer.Close()
	defer func() {
		select {
		case <-commentsRelease:
		default:
			close(commentsRelease)
		}
	}()

	database := dbtest.Open(t)
	providerClient, err := ghclient.NewClient(
		staticTokenSource("token"), "github.com", nil, nil,
		ghclient.WithBaseURLForTesting(providerServer.URL),
	)
	require.NoError(err)
	repo := ghclient.RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		PlatformRepoID: 1234, PlatformExternalID: "R_1234",
	}
	repoID, err := database.UpsertRepo(ctx, platform.DBRepoIdentity(platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.com", Owner: "acme", Name: "widget",
		RepoPath: "acme/widget", PlatformID: 1234, PlatformExternalID: "R_1234",
	}))
	require.NoError(err)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": providerClient}, database, nil,
		[]ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	httpServer := httptest.NewServer(srv)
	defer httpServer.Close()
	client, err := apiclient.NewWithHTTPClient(httpServer.URL, httpServer.Client())
	require.NoError(err)

	type syncResult struct {
		response *generated.SyncIssueResponse
		err      error
	}
	result := make(chan syncResult, 1)
	go func() {
		response, syncErr := client.HTTP.SyncIssueWithResponse(
			ctx, "github", "acme", "widget", 11,
			func(_ context.Context, request *http.Request) error {
				request.Header.Set("Content-Type", "application/json")
				return nil
			},
		)
		result <- syncResult{response: response, err: syncErr}
	}()

	select {
	case <-commentsEntered:
	case <-time.After(5 * time.Second):
		require.Fail("GitHub issue comment fetch did not start")
	}
	parent, err := database.GetIssueByRepoIDAndNumber(ctx, repoID, 11)
	require.NoError(err)
	require.NotNil(parent)
	archiveParent := *parent
	archiveParent.Title = "Archive parent"
	issueID, archiveRevision, accepted, err := database.UpsertArchiveIssueSnapshot(ctx, &archiveParent)
	require.NoError(err)
	require.True(accepted)
	applied, err := database.CommitIssueChildSnapshot(ctx, db.IssueChildSnapshot{
		IssueID: issueID, ExpectedRevision: archiveRevision,
		Comments: []db.IssueEvent{{
			IssueID: issueID, PlatformID: int64Pointer(5001), PlatformExternalID: "IC_5001",
			EventType: "issue_comment", Author: "archive", Body: "archive comment",
			CreatedAt: now, DedupeKey: "comment-5001",
		}},
	})
	require.NoError(err)
	require.True(applied)
	close(commentsRelease)

	var syncResponse syncResult
	select {
	case syncResponse = <-result:
	case <-time.After(5 * time.Second):
		require.Fail("GitHub issue sync did not finish")
	}
	require.NoError(syncResponse.err)
	require.NotNil(syncResponse.response)
	require.Equal(http.StatusOK, syncResponse.response.StatusCode(), string(syncResponse.response.Body))
	detail, err := client.HTTP.GetIssueWithResponse(ctx, "github", "acme", "widget", 11)
	require.NoError(err)
	require.Equal(http.StatusOK, detail.StatusCode(), string(detail.Body))
	require.NotNil(detail.JSON200)
	assert.Equal("Archive parent", detail.JSON200.Issue.Title)
	require.NotNil(detail.JSON200.Events)
	require.Len(*detail.JSON200.Events, 1)
	assert.Equal("archive comment", (*detail.JSON200.Events)[0].Body)
}

type blockingIssueEventsProvider struct {
	gitLabDiscussionProvider
	entered     chan struct{}
	enterOnce   sync.Once
	release     chan struct{}
	releaseOnce sync.Once
	staleEvents []platform.IssueEvent
}

func (p *blockingIssueEventsProvider) unblock() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func (p *blockingIssueEventsProvider) ListIssueEvents(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
) ([]platform.IssueEvent, error) {
	p.enterOnce.Do(func() { close(p.entered) })
	<-p.release
	return p.staleEvents, nil
}

//go:fix inline
func int64Pointer(value int64) *int64 {
	return new(value)
}

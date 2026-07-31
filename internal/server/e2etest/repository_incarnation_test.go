package e2etest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gh "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

type repositoryIncarnationMockGH struct {
	*mockGH
	openIssues       []*gh.Issue
	listOpenIssuesFn func(context.Context, string, string) ([]*gh.Issue, error)
}

func (m *repositoryIncarnationMockGH) ListOpenIssues(
	ctx context.Context, owner, name string,
) ([]*gh.Issue, error) {
	if m.listOpenIssuesFn != nil {
		return m.listOpenIssuesFn(ctx, owner, name)
	}
	return m.openIssues, nil
}

func TestRepositoryReplacementRetiresOldIncarnationAndHidesItsItems(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	providerID := "repo-old"
	mock := &repositoryIncarnationMockGH{
		mockGH: &mockGH{
			getRepositoryFn: func(
				context.Context, string, string,
			) (*gh.Repository, error) {
				id := int64(1)
				owner := "acme"
				name := "widget"
				return &gh.Repository{
					ID:       &id,
					NodeID:   &providerID,
					Owner:    &gh.User{Login: &owner},
					Name:     &name,
					Archived: new(bool),
				}, nil
			},
		},
	}
	database := dbtest.Open(t)
	repos := []ghclient.RepoRef{{
		Owner: "acme", Name: "widget", PlatformHost: "github.com",
		PlatformExternalID: "repo-old",
	}}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, repos, time.Minute, nil,
		map[string]*ghclient.SyncBudget{
			"github.com": ghclient.NewSyncBudget(1000),
		},
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client, err := apiclient.NewWithHTTPClient(ts.URL, ts.Client())
	require.NoError(err)

	syncer.RunOnce(ctx)
	stored, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	})
	require.NoError(err)
	require.NotNil(stored)
	oldRepoID := stored.ID

	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	mrID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: oldRepoID, PlatformID: 6006, Number: 6,
		URL:   "https://github.com/acme/widget/pull/6",
		Title: "old stack member", Author: "octocat",
		State:     db.MergeRequestStateOpen,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	stackID, err := database.UpsertStack(ctx, oldRepoID, 6, "old stack")
	require.NoError(err)
	require.NoError(database.ReplaceStackMembers(ctx, stackID, []db.StackMember{{
		StackID: stackID, MergeRequestID: mrID, Position: 1,
	}}))

	issueID := int64(7007)
	issueNumber := 7
	oldTitle := "issue from original repository"
	newTitle := "issue from replacement repository"
	state := "open"
	body := ""
	url := "https://github.com/acme/widget/issues/7"
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID: stored.ID, PlatformID: issueID, Number: issueNumber,
		URL: url, Title: oldTitle, State: state,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	replacementIssue := &gh.Issue{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &newTitle,
		State:     &state,
		Body:      &body,
		HTMLURL:   &url,
		CreatedAt: &gh.Timestamp{Time: now},
		UpdatedAt: &gh.Timestamp{Time: now.Add(time.Hour)},
	}
	mock.openIssues = []*gh.Issue{replacementIssue}
	mock.getIssueFn = func(
		context.Context, string, string, int,
	) (*gh.Issue, error) {
		return replacementIssue, nil
	}

	providerID = "repo-new"
	manualSync, err := client.HTTP.SyncIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
		generated.RequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Content-Type", "application/json")
			return nil
		}),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, manualSync.StatusCode(), string(manualSync.Body))
	require.NotNil(manualSync.JSON200)
	assert.Equal(newTitle, manualSync.JSON200.Issue.Title)

	active, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	})
	require.NoError(err)
	require.NotNil(active)
	assert.NotEqual(oldRepoID, active.ID)
	assert.Equal("repo-new", active.PlatformRepoID)

	retired, err := database.GetRepoByID(ctx, oldRepoID)
	require.NoError(err)
	require.NotNil(retired)
	require.NotNil(retired.RetiredAt)
	require.NotNil(retired.RetiredReplacementID)
	assert.Equal(active.ID, *retired.RetiredReplacementID)

	historicalIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx, oldRepoID, issueNumber,
	)
	require.NoError(err)
	require.NotNil(historicalIssue)
	assert.Equal(oldTitle, historicalIssue.Title)

	activeIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx, active.ID, issueNumber,
	)
	require.NoError(err)
	require.NotNil(activeIssue)
	assert.Equal(newTitle, activeIssue.Title)

	response, err := client.HTTP.ListReposWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode(), string(response.Body))
	require.NotNil(response.JSON200)
	require.Len(*response.JSON200, 1)
	repo := (*response.JSON200)[0]
	assert.Equal("acme", repo.Owner)
	assert.Equal("widget", repo.Name)
	assert.Empty(repo.LastSyncError)

	issueResponse, err := client.HTTP.GetIssueWithResponse(
		ctx,
		"gh",
		"acme",
		"widget",
		int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(
		http.StatusOK,
		issueResponse.StatusCode(),
		string(issueResponse.Body),
	)
	require.NotNil(issueResponse.JSON200)
	assert.Equal(newTitle, issueResponse.JSON200.Issue.Title)

	stackResponse, err := client.HTTP.ListStacksWithResponse(ctx, nil)
	require.NoError(err)
	require.Equal(
		http.StatusOK,
		stackResponse.StatusCode(),
		string(stackResponse.Body),
	)
	require.NotNil(stackResponse.JSON200)
	assert.Empty(*stackResponse.JSON200)

	prNumber := 8
	prID := int64(8008)
	prTitle := "pull request from newest repository incarnation"
	prURL := "https://github.com/acme/widget/pull/8"
	headRef, baseRef := "feature", "main"
	headSHA, baseSHA := "head-sha", "base-sha"
	mock.getPullRequestFn = func(
		context.Context, string, string, int,
	) (*gh.PullRequest, error) {
		return &gh.PullRequest{
			ID: &prID, Number: &prNumber, Title: &prTitle, HTMLURL: &prURL,
			State: &state, Body: &body, User: &gh.User{Login: new("author")},
			CreatedAt: &gh.Timestamp{Time: now}, UpdatedAt: &gh.Timestamp{Time: now},
			Head: &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
			Base: &gh.PullRequestBranch{Ref: &baseRef, SHA: &baseSHA},
		}, nil
	}
	providerID = "repo-newest"
	manualPRSync, err := client.HTTP.SyncPullWithResponse(
		ctx, "gh", "acme", "widget", int64(prNumber),
		generated.RequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Content-Type", "application/json")
			return nil
		}),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, manualPRSync.StatusCode(), string(manualPRSync.Body))
	require.NotNil(manualPRSync.JSON200)
	assert.Equal(prTitle, manualPRSync.JSON200.MergeRequest.Title)
	newest, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: "widget",
	})
	require.NoError(err)
	require.NotNil(newest)
	assert.NotEqual(active.ID, newest.ID)
	assert.Equal("repo-newest", newest.PlatformRepoID)
}

func TestRepositoryRenamePreservesIncarnationHistoryAndConfiguredLookup(
	t *testing.T,
) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	const providerID = "repo-stable"
	currentOwner := "acme"
	currentName := "widget"
	var repositoryLookups []string
	var issueLookups []string
	var issueMutations []string
	issueID := int64(7007)
	issueNumber := 7
	issueTitle := "preserved issue"
	issueState := "open"
	issueBody := ""
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	issueForRoute := func(owner string, name string) *gh.Issue {
		url := "https://github.com/" + owner + "/" + name + "/issues/7"
		return &gh.Issue{
			ID:        &issueID,
			Number:    &issueNumber,
			Title:     &issueTitle,
			State:     &issueState,
			Body:      &issueBody,
			HTMLURL:   &url,
			CreatedAt: &gh.Timestamp{Time: now},
			UpdatedAt: &gh.Timestamp{Time: now},
		}
	}

	mock := &repositoryIncarnationMockGH{}
	mock.mockGH = &mockGH{
		getRepositoryFn: func(
			_ context.Context,
			owner string,
			name string,
		) (*gh.Repository, error) {
			repositoryLookups = append(
				repositoryLookups,
				owner+"/"+name,
			)
			resolvedOwner := currentOwner
			resolvedName := currentName
			return &gh.Repository{
				ID:       new(int64(1)),
				NodeID:   new(providerID),
				Owner:    &gh.User{Login: &resolvedOwner},
				Name:     &resolvedName,
				Archived: new(bool),
			}, nil
		},
		getIssueFn: func(
			_ context.Context,
			owner string,
			name string,
			_ int,
		) (*gh.Issue, error) {
			return issueForRoute(owner, name), nil
		},
		editIssueFn: func(
			_ context.Context,
			owner string,
			name string,
			_ int,
			state string,
		) (*gh.Issue, error) {
			issueMutations = append(
				issueMutations,
				owner+"/"+name+":"+state,
			)
			return issueForRoute(owner, name), nil
		},
	}
	mock.listOpenIssuesFn = func(
		_ context.Context,
		owner string,
		name string,
	) ([]*gh.Issue, error) {
		issueLookups = append(issueLookups, owner+"/"+name)
		return []*gh.Issue{issueForRoute(owner, name)}, nil
	}

	database := dbtest.Open(t)
	router, err := ghclient.NewHostRouter("github.com", &ghclient.Route{
		Key: ghclient.RouteKey{
			Host: "github.com", Owner: "acme", Name: "widget",
		},
		Client: mock,
		ReadIdentity: ghclient.IdentityKey{
			Host: "github.com", Principal: "user:1",
		},
		WriteIdentity: ghclient.IdentityKey{
			Host: "github.com", Principal: "user:1",
		},
	})
	require.NoError(err)
	routed, err := ghclient.NewRoutedClient(router)
	require.NoError(err)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": routed},
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:           "github",
			PlatformHost:       "github.com",
			Owner:              "acme",
			Name:               "widget",
			RepoPath:           "acme/widget",
			PlatformExternalID: providerID,
		}},
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{
			"github.com": ghclient.NewSyncBudget(1000),
		},
	)
	syncer.SetGitHubRouters(map[string]*ghclient.HostRouter{
		"github.com": router,
	})
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		HostCheckAllowLoopbackAnyPort: true,
	})
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cancel()
		require.NoError(srv.Shutdown(shutdownCtx))
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client, err := apiclient.NewWithHTTPClient(ts.URL, ts.Client())
	require.NoError(err)

	triggerSync := func() {
		t.Helper()
		done := make(chan struct{}, 1)
		syncer.SetOnStatusChange(func(status *ghclient.SyncStatus) {
			if !status.Running {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		})
		response, syncErr := client.HTTP.TriggerSyncWithResponse(
			ctx,
			nil,
			func(
				_ context.Context,
				request *http.Request,
			) error {
				request.Header.Set("Content-Type", "application/json")
				return nil
			},
		)
		require.NoError(syncErr)
		require.Equal(
			http.StatusAccepted,
			response.StatusCode(),
			string(response.Body),
		)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			require.Fail("repository sync did not complete")
		}
	}

	triggerSync()
	original, err := database.GetRepoByIdentity(
		ctx,
		db.RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			Owner: "acme", Name: "widget",
		},
	)
	require.NoError(err)
	require.NotNil(original)
	originalIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx,
		original.ID,
		issueNumber,
	)
	require.NoError(err)
	require.NotNil(originalIssue)

	currentOwner = "acme-tools"
	currentName = "renamed-widget"
	triggerSync()

	renamed, err := database.GetRepoByIdentity(
		ctx,
		db.RepoIdentity{
			Platform: "github", PlatformHost: "github.com",
			Owner: "acme-tools", Name: "renamed-widget",
		},
	)
	require.NoError(err)
	require.NotNil(renamed)
	assert.Equal(original.ID, renamed.ID)
	assert.Equal(providerID, renamed.PlatformRepoID)
	renamedIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx,
		renamed.ID,
		issueNumber,
	)
	require.NoError(err)
	require.NotNil(renamedIssue)
	assert.Equal(originalIssue.ID, renamedIssue.ID)

	tracked := syncer.TrackedRepos()
	require.Len(tracked, 1)
	assert.Equal("acme-tools", tracked[0].Owner)
	assert.Equal("renamed-widget", tracked[0].Name)
	assert.Equal("acme", tracked[0].CredentialOwner)
	assert.Equal("widget", tracked[0].CredentialName)

	triggerSync()
	require.Len(repositoryLookups, 3)
	assert.Equal(
		[]string{"acme/widget", "acme/widget", "acme/widget"},
		repositoryLookups,
	)
	require.Len(issueLookups, 3)
	assert.Equal("acme/widget", issueLookups[0])
	assert.Equal("acme-tools/renamed-widget", issueLookups[1])
	assert.Equal("acme-tools/renamed-widget", issueLookups[2])

	reposResponse, err := client.HTTP.ListReposWithResponse(ctx)
	require.NoError(err)
	require.Equal(
		http.StatusOK,
		reposResponse.StatusCode(),
		string(reposResponse.Body),
	)
	require.NotNil(reposResponse.JSON200)
	require.Len(*reposResponse.JSON200, 1)
	assert.Equal("acme-tools", (*reposResponse.JSON200)[0].Owner)
	assert.Equal("renamed-widget", (*reposResponse.JSON200)[0].Name)

	issueResponse, err := client.HTTP.GetIssueWithResponse(
		ctx,
		"gh",
		"acme-tools",
		"renamed-widget",
		int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(
		http.StatusOK,
		issueResponse.StatusCode(),
		string(issueResponse.Body),
	)
	require.NotNil(issueResponse.JSON200)
	assert.Equal(issueTitle, issueResponse.JSON200.Issue.Title)

	mutationResponse, err := client.HTTP.SetIssueGithubStateWithResponse(
		ctx,
		"gh",
		"acme-tools",
		"renamed-widget",
		int64(issueNumber),
		generated.SetIssueGithubStateJSONRequestBody{State: "closed"},
	)
	require.NoError(err)
	require.Equal(
		http.StatusOK,
		mutationResponse.StatusCode(),
		string(mutationResponse.Body),
	)
	assert.Equal(
		[]string{"acme-tools/renamed-widget:closed"},
		issueMutations,
	)
}

func TestRepositorySyncDiscardsPublicationSupersededDuringHTTPRun(
	t *testing.T,
) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	firstLookupStarted := make(chan struct{})
	releaseFirstLookup := make(chan struct{})
	provider := &repositoryIncarnationMockGH{
		mockGH: &mockGH{
			getRepositoryFn: func(
				_ context.Context,
				owner string,
				name string,
			) (*gh.Repository, error) {
				if owner == "acme" && name == "first" {
					close(firstLookupStarted)
					<-releaseFirstLookup
				}
				nodeID := "repo-" + name
				return &gh.Repository{
					NodeID:   &nodeID,
					Owner:    &gh.User{Login: &owner},
					Name:     &name,
					Archived: new(bool),
				}, nil
			},
		},
	}
	database := dbtest.Open(t)
	first := ghclient.RepoRef{
		Platform: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "first", RepoPath: "acme/first",
	}
	second := ghclient.RepoRef{
		Platform: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "second", RepoPath: "acme/second",
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": provider},
		database,
		nil,
		[]ghclient.RepoRef{first},
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{
			"github.com": ghclient.NewSyncBudget(1000),
		},
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(
		database,
		syncer,
		nil,
		"/",
		nil,
		server.ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cancel()
		require.NoError(srv.Shutdown(shutdownCtx))
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client, err := apiclient.NewWithHTTPClient(ts.URL, ts.Client())
	require.NoError(err)

	triggerSync := func() <-chan struct{} {
		t.Helper()
		done := make(chan struct{}, 1)
		syncer.SetOnStatusChange(func(status *ghclient.SyncStatus) {
			if !status.Running {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		})
		response, syncErr := client.HTTP.TriggerSyncWithResponse(
			ctx,
			nil,
			func(
				_ context.Context,
				request *http.Request,
			) error {
				request.Header.Set("Content-Type", "application/json")
				return nil
			},
		)
		require.NoError(syncErr)
		require.Equal(
			http.StatusAccepted,
			response.StatusCode(),
			string(response.Body),
		)
		return done
	}

	firstDone := triggerSync()
	select {
	case <-firstLookupStarted:
	case <-time.After(2 * time.Second):
		close(releaseFirstLookup)
		require.Fail("first repository lookup did not start")
	}
	require.NoError(syncer.SetReposWithContext(
		ctx,
		[]ghclient.RepoRef{second},
		false,
	))
	close(releaseFirstLookup)
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		require.Fail("superseded sync did not complete")
	}
	assert.Equal([]ghclient.RepoRef{second}, syncer.TrackedRepos())

	secondDone := triggerSync()
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		require.Fail("replacement configured sync did not complete")
	}
	repos, err := database.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 1)
	assert.Equal("acme", repos[0].Owner)
	assert.Equal("second", repos[0].Name)
	assert.Equal("repo-second", repos[0].PlatformRepoID)
}

package issuetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"

	giteaplatform "go.kenn.io/forge/platform/gitea"

	platformgitlab "go.kenn.io/forge/platform/gitlab"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m))
}

func TestAPIListIssuesFiltersPullRequestReferences(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	referencedID := serverfake.SeedIssue(t, database, "acme", "widget", 1, "open")
	serverfake.SeedIssue(t, database, "acme", "widget", 2, "open")
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:   referencedID,
		EventType: "cross_referenced",
		MetadataJSON: `{
			"source_type":"PullRequest",
			"source_owner":"acme",
			"source_repo":"client",
			"source_number":42,
			"source_url":"https://github.com/acme/client/pull/42"
		}`,
		CreatedAt: time.Now().UTC(),
		DedupeKey: "cross-reference-42",
	}}))

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/issues?state=all&referenced_by_pr=true", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var issues []issueapi.IssueResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &issues))
	require.Len(issues, 1)
	require.Equal(1, issues[0].Number)
}

func TestAPICreateIssue(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	createdAt := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	mock := &serverfake.MockGH{
		CreateIssueFn: func(_ context.Context, owner, repo, title, body string) (*gh.Issue, error) {
			id := int64(9876)
			number := 27
			state := "open"
			url := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, number)
			login := "issue-bot"
			ts := gh.Timestamp{Time: createdAt}
			comments := 0
			labelID := int64(42)
			labelName := "enhancement"
			labelColor := "a2eeef"
			return &gh.Issue{
				ID:       &id,
				Number:   &number,
				Title:    &title,
				Body:     &body,
				State:    &state,
				HTMLURL:  &url,
				User:     &gh.User{Login: &login},
				Comments: &comments,
				Labels: []*gh.Label{{
					ID:    labelID,
					Name:  labelName,
					Color: labelColor,
				}},
				CreatedAt: &ts,
				UpdatedAt: &ts,
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithRepos(
		t,
		mock,
		[]ghclient.RepoRef{
			{Platform: "github", Owner: "acme", Name: "widgets", PlatformHost: "github.com"},
		},
	)
	client := servertest.SetupTestClient(t, srv)

	_, err := database.UpsertRepo(context.Background(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)

	resp, err := client.HTTP.CreateIssueWithResponse(context.Background(), &generated.CreateIssueRequestOptions{PathParams: &generated.CreateIssuePath{Provider: "gh", Owner: "acme", Name: "widgets"}, Body: &generated.CreateIssueBody{
		Title: "Ship repo summaries",
		Body:  "Add a top-level repository overview page.",
	}})
	require.NoError(err)
	require.Equal(http.StatusCreated, resp.StatusCode)
	require.NotNil(resp.JSON201)

	assert.Equal(int64(27), resp.JSON201.Number)
	assert.Equal("acme", resp.JSON201.RepoOwner)
	assert.Equal("widgets", resp.JSON201.RepoName)
	assert.Equal("Ship repo summaries", resp.JSON201.Title)
	require.NotNil(resp.JSON201.Labels)
	assert.Equal([]generated.Label{{
		Name:      "enhancement",
		Color:     "a2eeef",
		IsDefault: false,
	}}, resp.JSON201.Labels)

	issue, err := database.GetIssue(context.Background(), "github", "github.com", "acme", "widgets", 27)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("Ship repo summaries", issue.Title)
	assert.Equal("Add a top-level repository overview page.", issue.Body)
	assert.Equal("open", issue.State)
	assert.Equal(createdAt, issue.CreatedAt.UTC())
	require.Len(issue.Labels, 1)
	assert.Equal("enhancement", issue.Labels[0].Name)
	assert.Equal("a2eeef", issue.Labels[0].Color)
}

func TestAPICreateIssueRejectsNilProviderPayload(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	mock := &serverfake.MockGH{
		CreateIssueFn: func(context.Context, string, string, string, string) (*gh.Issue, error) {
			return nil, nil
		},
	}
	srv, database := servertest.SetupTestServerWithRepos(
		t,
		mock,
		[]ghclient.RepoRef{
			{Platform: "github", Owner: "acme", Name: "widgets", PlatformHost: "github.com"},
		},
	)
	client := servertest.SetupTestClient(t, srv)

	repoID, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)

	resp, err := client.HTTP.CreateIssueWithResponse(t.Context(), &generated.CreateIssueRequestOptions{PathParams: &generated.CreateIssuePath{Provider: "gh", Owner: "acme", Name: "widgets"}, Body: &generated.CreateIssueBody{Title: "Empty payload"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 0)
	require.NoError(err)
	require.Nil(issue)
}

func TestAPICreateIssueReportsUnknownOutcomeForUnverifiedProviderFailure(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv := servertest.SetupGitLabIssueMutatorServer(t, errors.New("provider response unavailable"))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.CreateIssueOnHostWithResponse(t.Context(), &generated.CreateIssueOnHostRequestOptions{PathParams: &generated.CreateIssueOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gl", Owner: "group", Name: "project"}, Body: &generated.CreateIssueOnHostBody{Title: "Unverified issue"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.Error)

	assert.Equal(generated.ProblemErrorCodeMutationOutcomeUnknown, resp.Error.Code)
	require.NotNil(resp.Error.Details)
	assert.Equal("gitlab", resp.Error.Details["provider"])
	assert.Equal("gitlab.example.com", resp.Error.Details["platformHost"])
}

func TestAPICreateIssueReportsUnknownOutcomeWhenPersistenceFailsAfterProviderSuccess(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	var database *db.DB
	var persistenceSetupErr error
	createdAt := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	mock := &serverfake.MockGH{
		CreateIssueFn: func(ctx context.Context, owner, repo, title, body string) (*gh.Issue, error) {
			_, persistenceSetupErr = database.WriteDB().ExecContext(ctx, "DROP TABLE forge_issue_labels")
			return &gh.Issue{
				ID:        new(int64(9876)),
				Number:    new(27),
				Title:     &title,
				Body:      &body,
				State:     new("open"),
				HTMLURL:   new(fmt.Sprintf("https://github.com/%s/%s/issues/27", owner, repo)),
				User:      &gh.User{Login: new("issue-bot")},
				CreatedAt: &gh.Timestamp{Time: createdAt},
				UpdatedAt: &gh.Timestamp{Time: createdAt},
			}, nil
		},
	}
	srv, openedDatabase := servertest.SetupTestServerWithRepos(
		t,
		mock,
		[]ghclient.RepoRef{{
			Platform: "github", Owner: "acme", Name: "widgets", PlatformHost: "github.com",
		}},
	)
	database = openedDatabase
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.CreateIssueWithResponse(t.Context(), &generated.CreateIssueRequestOptions{PathParams: &generated.CreateIssuePath{Provider: "gh", Owner: "acme", Name: "widgets"}, Body: &generated.CreateIssueBody{Title: "Persist this issue"}})
	require.Error(err)
	require.NotNil(resp)
	require.NoError(persistenceSetupErr)
	require.Equal(http.StatusBadGateway, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.Error)

	assert.Equal(generated.ProblemErrorCodeMutationOutcomeUnknown, resp.Error.Code)
	require.NotNil(resp.Error.Details)
	assert.Equal("github", resp.Error.Details["provider"])
	assert.Equal("github.com", resp.Error.Details["platformHost"])
}

func TestAPIPostIssueCommentRejectsNilProviderPayload(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	mock := &serverfake.MockGH{
		CreateIssueCommentFn: func(context.Context, string, string, int, string) (*gh.IssueComment, error) {
			return nil, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	issueID := serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.PostIssueCommentWithResponse(t.Context(), &generated.PostIssueCommentRequestOptions{PathParams: &generated.PostIssueCommentPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.PostIssueCommentBody{Body: "Looks good"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	events, err := database.ListIssueEvents(t.Context(), issueID)
	require.NoError(err)
	require.Empty(events)
}

func TestAPIEditIssueCommentRejectsNilProviderPayload(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	mock := &serverfake.MockGH{
		EditIssueCommentFn: func(context.Context, string, string, int64, string) (*gh.IssueComment, error) {
			return nil, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	issueID := serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	commentID := int64(42)
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:    issueID,
		PlatformID: &commentID,
		EventType:  "issue_comment",
		Body:       "original body",
		CreatedAt:  time.Now().UTC(),
		DedupeKey:  "issue-comment-42",
	}}))
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.EditIssueCommentWithResponse(t.Context(), &generated.EditIssueCommentRequestOptions{PathParams: &generated.EditIssueCommentPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5), CommentID: int64(commentID)}, Body: &generated.EditIssueCommentBody{Body: "edited body"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	events, err := database.ListIssueEvents(t.Context(), issueID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("original body", events[0].Body)
}

func TestAPICreateIssueUsesPlatformHost(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	githubCalled := false
	enterpriseCalled := false
	githubClient := &serverfake.MockGH{
		CreateIssueFn: func(_ context.Context, _, _, _, _ string) (*gh.Issue, error) {
			githubCalled = true
			return nil, errors.New("wrong host")
		},
	}
	enterpriseClient := &serverfake.MockGH{
		CreateIssueFn: func(_ context.Context, owner, repo, title, body string) (*gh.Issue, error) {
			enterpriseCalled = true
			number := 44
			state := "open"
			url := fmt.Sprintf("https://ghe.example.com/%s/%s/issues/%d", owner, repo, number)
			login := "issue-bot"
			ts := gh.Timestamp{Time: time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)}
			return &gh.Issue{
				Number:    &number,
				Title:     &title,
				Body:      &body,
				State:     &state,
				HTMLURL:   &url,
				User:      &gh.User{Login: &login},
				CreatedAt: &ts,
				UpdatedAt: &ts,
			}, nil
		},
	}
	repos := []ghclient.RepoRef{
		{Platform: "github", Owner: "acme", Name: "widgets", PlatformHost: "github.com"},
		{Owner: "acme", Name: "widgets", PlatformHost: "ghe.example.com"},
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com":      githubClient,
			"ghe.example.com": enterpriseClient,
		},
		database,
		nil,
		repos,
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	_, err := database.UpsertRepo(context.Background(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	enterpriseRepoID, err := database.UpsertRepo(context.Background(), serverfake.VerifiedGitHubRepoIdentity("ghe.example.com", "acme", "widgets"))
	require.NoError(err)

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.CreateIssueOnHostWithResponse(t.Context(), &generated.CreateIssueOnHostRequestOptions{PathParams: &generated.CreateIssueOnHostPath{PlatformHost: "ghe.example.com", Provider: "gh", Owner: "acme", Name: "widgets"}, Body: &generated.CreateIssueOnHostBody{
		Title: "Ship enterprise issue",
		Body:  "Route to the selected host.",
	}})
	require.NoError(err)
	require.Equal(http.StatusCreated, resp.StatusCode)
	assert.False(githubCalled)
	assert.True(enterpriseCalled)
	issue, err := database.GetIssueByRepoIDAndNumber(
		context.Background(),
		enterpriseRepoID,
		44,
	)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("Ship enterprise issue", issue.Title)
}

func TestAPIEditIssueCommentUpdatesGitHubAndLocalTimeline(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(1234)
	createdAt := time.Date(2026, 4, 29, 13, 0, 0, 0, time.UTC)
	mock := &serverfake.MockGH{
		EditIssueCommentFn: func(_ context.Context, owner, repo string, gotCommentID int64, body string) (*gh.IssueComment, error) {
			assert.Equal("acme", owner)
			assert.Equal("widget", repo)
			assert.Equal(commentID, gotCommentID)
			assert.Equal("edited issue body", body)
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
	issueID := serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:      issueID,
		PlatformID:   &commentID,
		EventType:    "issue_comment",
		Author:       "maintainer",
		Body:         "original issue body",
		MetadataJSON: `{"provider_hidden":true,"provider_hidden_reason":"OFF_TOPIC"}`,
		CreatedAt:    createdAt,
		DedupeKey:    "issue-comment-1234",
	}}))

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5/comments/1234",
		strings.NewReader(`{"body":"edited issue body"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(http.StatusOK, rec.Code)
	events, err := database.ListIssueEvents(t.Context(), issueID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("edited issue body", events[0].Body)
	assert.Equal("maintainer", events[0].Author)
	assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"OFF_TOPIC"}`, events[0].MetadataJSON)
	require.NotNil(events[0].PlatformID)
	assert.Equal(commentID, *events[0].PlatformID)
}

func TestAPIEditIssueCommentRejectsCommentFromDifferentIssue(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	commentID := int64(6666)
	var editCalls atomic.Int32
	mock := &serverfake.MockGH{
		EditIssueCommentFn: func(_ context.Context, _, _ string, _ int64, _ string) (*gh.IssueComment, error) {
			editCalls.Add(1)
			return nil, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	routeIssueID := serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	otherIssueID := serverfake.SeedIssue(t, database, "acme", "widget", 6, "open")
	require.NotEqual(routeIssueID, otherIssueID)
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:    otherIssueID,
		PlatformID: &commentID,
		EventType:  "issue_comment",
		Author:     "maintainer",
		Body:       "other issue body",
		CreatedAt:  time.Now().UTC(),
		DedupeKey:  "issue-comment-6666",
	}}))

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5/comments/6666",
		strings.NewReader(`{"body":"wrong target"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(http.StatusNotFound, rec.Code)
	require.Equal(int32(0), editCalls.Load())
}

func TestAPIDeleteIssueCommentKeepsLocalCommentWhenProviderRejects(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(1234)
	mock := &serverfake.MockGH{
		DeleteIssueCommentFn: func(context.Context, string, string, int64) error {
			return errors.New("provider denied deletion")
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	issueID := serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:    issueID,
		PlatformID: &commentID,
		EventType:  "issue_comment",
		Author:     "maintainer",
		Body:       "keep me",
		CreatedAt:  time.Now().UTC(),
		DedupeKey:  "issue-comment-1234",
	}}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/issues/gh/acme/widget/5/comments/1234", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(http.StatusBadGateway, rec.Code)
	events, err := database.ListIssueEvents(t.Context(), issueID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("keep me", events[0].Body)
}

func TestAPIDeleteIssueCommentKeepsLocalCommentWhenProviderReportsNotFound(t *testing.T) {
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
	issueID := serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:    issueID,
		PlatformID: &commentID,
		EventType:  "issue_comment",
		Author:     "maintainer",
		Body:       "keep until deletion is confirmed",
		CreatedAt:  time.Now().UTC(),
		DedupeKey:  "issue-comment-4321",
	}}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/issues/gh/acme/widget/5/comments/4321", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(http.StatusNotFound, rec.Code)
	events, err := database.ListIssueEvents(t.Context(), issueID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("keep until deletion is confirmed", events[0].Body)
}

func TestAPIDeleteIssueCommentLeavesLocalStateForSync(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(5432)
	var deleteCalls atomic.Int32
	mock := &serverfake.MockGH{
		DeleteIssueCommentFn: func(context.Context, string, string, int64) error {
			deleteCalls.Add(1)
			return nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	issueID := serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:    issueID,
		PlatformID: &commentID,
		EventType:  "issue_comment",
		Author:     "maintainer",
		Body:       "remove from issue detail",
		CreatedAt:  time.Now().UTC(),
		DedupeKey:  "issue-comment-5432",
	}}))

	deleteReq := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/issues/gh/acme/widget/5/comments/5432", nil)
	deleteReq.Header.Set("Content-Type", "application/json")
	deleteRec := httptest.NewRecorder()
	srv.ServeHTTP(deleteRec, deleteReq)
	require.Equal(http.StatusNoContent, deleteRec.Code, deleteRec.Body.String())
	assert.Equal(int32(1), deleteCalls.Load())

	detailReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/issues/gh/acme/widget/5", nil)
	detailRec := httptest.NewRecorder()
	srv.ServeHTTP(detailRec, detailReq)
	require.Equal(http.StatusOK, detailRec.Code, detailRec.Body.String())
	var detail issueapi.IssueDetailResponse
	require.NoError(json.NewDecoder(detailRec.Body).Decode(&detail))
	require.Len(detail.Events, 1)
	assert.Equal("remove from issue detail", detail.Events[0].Body)
}

func TestAPIGitLabDisabledIssueCooldownPersistsThroughHTTPAndSQLite(t *testing.T) {
	serverfake.RunParallelServerTest(t)

	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	var metadataCalls atomic.Int32
	var issueCalls atomic.Int32
	var mergeRequestCalls atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42":
			metadataCalls.Add(1)
			_, _ = io.WriteString(w, `{
				"id":42,"path":"project","path_with_namespace":"group/project",
				"web_url":"https://gitlab.test/group/project",
				"http_url_to_repo":"https://gitlab.test/group/project.git",
				"default_branch":"main","issues_access_level":"disabled",
				"merge_requests_access_level":"enabled"
			}`)
		case "/api/v4/projects/42/issues":
			issueCalls.Add(1)
			http.Error(w, "issues are disabled", http.StatusNotFound)
		case "/api/v4/projects/42/merge_requests":
			mergeRequestCalls.Add(1)
			_, _ = io.WriteString(w, `[{
				"id":7001,"iid":7,"project_id":42,"source_project_id":42,
				"title":"unaffected merge request","state":"opened",
				"web_url":"https://gitlab.test/group/project/-/merge_requests/7",
				"author":{"username":"ada"},"source_branch":"feature",
				"target_branch":"main","sha":"head-sha",
				"created_at":"2026-07-22T12:00:00Z",
				"updated_at":"2026-07-22T12:01:00Z"
			}]`)
		case "/api/v4/projects/42/releases",
			"/api/v4/projects/42/repository/tags",
			"/api/v4/projects/42/labels":
			_, _ = io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(providerServer.Close)

	provider, err := platformgitlab.NewClient(
		"gitlab.test",
		serverfake.TestTokenSource("token"),
		platformgitlab.WithBaseURLForTesting(providerServer.URL+"/api/v4"),
		platformgitlab.WithoutRetriesForTesting(), platformgitlab.
			WithTransport(http.DefaultTransport),
	)
	require.NoError(err)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	database := dbtest.Open(t)
	ref := ghclient.RepoRef{
		Platform: platform.KindGitLab, PlatformHost: "gitlab.test",
		Owner: "group", Name: "project", RepoPath: "group/project",
		PlatformRepoID: 42, PlatformExternalID: "42",
		WebURL:   "https://gitlab.test/group/project",
		CloneURL: "https://gitlab.test/group/project.git", DefaultBranch: "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{ref}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	syncer.RunOnce(ctx)

	assert.Equal(int32(1), issueCalls.Load())
	assert.Equal(int32(2), mergeRequestCalls.Load())
	assert.Equal(int32(3), metadataCalls.Load())
	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform: "gitlab", PlatformHost: "gitlab.test", RepoPath: "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	assert.Empty(repo.LastSyncError)
	stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("unaffected merge request", stored.Title)
}

func TestAPIGiteaDisabledIssueCooldownPersistsThroughHTTPAndSQLite(t *testing.T) {
	serverfake.RunParallelServerTest(t)

	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	var metadataCalls atomic.Int32
	var issueCalls atomic.Int32
	var mergeRequestCalls atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v1/repos/tea/kettle":
			metadataCalls.Add(1)
			_, _ = io.WriteString(w, `{
				"id":101,"name":"kettle","full_name":"tea/kettle",
				"html_url":"https://gitea.test/tea/kettle",
				"clone_url":"http://gitea.test/tea/kettle.git",
				"default_branch":"main","owner":{"login":"tea"},
				"has_issues":false,"has_pull_requests":true,
				"created_at":"2026-07-22T12:00:00Z",
				"updated_at":"2026-07-22T12:01:00Z"
			}`)
		case "/api/v1/repos/tea/kettle/issues":
			issueCalls.Add(1)
			http.Error(w, "issues are disabled", http.StatusNotFound)
		case "/api/v1/repos/tea/kettle/pulls":
			mergeRequestCalls.Add(1)
			_, _ = io.WriteString(w, `[{
				"id":201,"number":7,
				"html_url":"https://gitea.test/tea/kettle/pulls/7",
				"title":"unaffected pull request","state":"open",
				"user":{"login":"ada"},
				"head":{"ref":"feature","sha":"head-sha"},
				"base":{"ref":"main","sha":"base-sha"},
				"created_at":"2026-07-22T12:00:00Z",
				"updated_at":"2026-07-22T12:01:00Z"
			}]`)
		case "/api/v1/repos/tea/kettle/releases",
			"/api/v1/repos/tea/kettle/tags",
			"/api/v1/repos/tea/kettle/labels":
			_, _ = io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(providerServer.Close)

	provider, err := giteaplatform.NewClient(
		"gitea.test",
		serverfake.TestTokenSource("token"),
		giteaplatform.WithBaseURL(providerServer.URL, true),
		giteaplatform.WithServerVersion("1.26.0"), giteaplatform.WithTransport(http.DefaultTransport))
	require.NoError(err)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	database := dbtest.Open(t)
	ref := ghclient.RepoRef{
		Platform: platform.KindGitea, PlatformHost: "gitea.test",
		Owner: "tea", Name: "kettle", RepoPath: "tea/kettle",
		PlatformRepoID: 101, PlatformExternalID: "101",
		WebURL:   "https://gitea.test/tea/kettle",
		CloneURL: "https://gitea.test/tea/kettle.git", DefaultBranch: "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{ref}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	syncer.RunOnce(ctx)

	assert.Equal(int32(1), issueCalls.Load())
	assert.Equal(int32(2), mergeRequestCalls.Load())
	assert.Equal(int32(3), metadataCalls.Load())
	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform: "gitea", PlatformHost: "gitea.test", RepoPath: "tea/kettle",
	})
	require.NoError(err)
	require.NotNil(repo)
	assert.Empty(repo.LastSyncError)
	assert.Equal("http://gitea.test/tea/kettle.git", repo.CloneURL)
	stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("unaffected pull request", stored.Title)
}

func TestAPIReopenIssue(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "closed")
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.SetIssueGithubStateWithResponse(t.Context(), &generated.SetIssueGithubStateRequestOptions{PathParams: &generated.SetIssueGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.SetIssueGithubStateBody{State: "open"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	issue, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	require.Equal("open", issue.State)
	require.Nil(issue.ClosedAt, "closed_at should be cleared on reopen")
}

func TestAPIEnqueueIssueSyncReturnsBeforeGitHubFetchCompletes(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)

	syncStarted := make(chan struct{}, 1)
	releaseSync := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseSync) })
	})

	mock := &serverfake.MockGH{
		GetIssueFn: func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
			syncStarted <- struct{}{}
			<-releaseSync

			id := int64(202)
			state := "open"
			title := "fresh async issue"
			url := "https://github.com/acme/widget/issues/5"
			author := "alice"
			now := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.Issue{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &now,
				UpdatedAt: &now,
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	client := servertest.SetupTestClient(t, srv)

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	resp, err := client.HTTP.EnqueueIssueSyncWithResponse(ctx, &generated.EnqueueIssueSyncRequestOptions{PathParams: &generated.EnqueueIssueSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode)

	select {
	case <-syncStarted:
	case <-time.After(2 * time.Second):
		require.Fail("background issue sync did not start")
	}
	releaseOnce.Do(func() { close(releaseSync) })
}

func TestAPISyncIssueDoesNotOverwriteNewerStateChange(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	staleUpdatedAt := time.Date(2026, 4, 12, 1, 0, 0, 0, time.UTC)
	syncStarted := make(chan struct{}, 1)
	releaseSync := make(chan struct{})
	mock := &serverfake.MockGH{
		GetIssueFn: func(_ context.Context, owner, repo string, number int) (*gh.Issue, error) {
			syncStarted <- struct{}{}
			<-releaseSync

			id := int64(202)
			state := "open"
			title := "stale issue sync"
			url := "https://github.com/acme/widget/issues/5"
			author := "alice"
			createdAt := gh.Timestamp{Time: staleUpdatedAt.Add(-time.Hour)}
			updatedAt := gh.Timestamp{Time: staleUpdatedAt}
			return &gh.Issue{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	client := servertest.SetupTestClient(t, srv)

	syncDone := make(chan *generated.SyncIssueResp, 1)
	syncErr := make(chan error, 1)
	go func() {
		resp, err := client.HTTP.SyncIssueWithResponse(t.Context(), &generated.SyncIssueRequestOptions{PathParams: &generated.SyncIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}})
		if err != nil {
			syncErr <- err
			return
		}
		syncDone <- resp
	}()

	<-syncStarted

	resp, err := client.HTTP.SetIssueGithubStateWithResponse(t.Context(), &generated.SetIssueGithubStateRequestOptions{PathParams: &generated.SetIssueGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.SetIssueGithubStateBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	closedIssue, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	require.Equal("closed", closedIssue.State)
	require.NotNil(closedIssue.ClosedAt)

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
	require.True(completed, "timed out waiting for stale issue sync")

	finalIssue, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	assert.Equal("closed", finalIssue.State)
	assert.NotNil(finalIssue.ClosedAt)
	assert.Equal("Test Issue", finalIssue.Title)
	assert.True(finalIssue.UpdatedAt.After(staleUpdatedAt))
}

// TestAPISyncIssueNilUpdatedAtFallsBackToCreatedAt drives the full
// HTTP handler -> syncer -> SQLite path with a GitHub response that
// has updated_at: null, and verifies last_activity_at falls back to
// created_at via the nil guard in refreshIssueTimeline. The sync_test
// unit tests cover the same logic at the syncer layer; this test
// covers the request path users actually hit in production.
func TestAPISyncIssueNilUpdatedAtFallsBackToCreatedAt(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	createdAt := time.Date(2025, 3, 14, 9, 0, 0, 0, time.UTC)
	mock := &serverfake.MockGH{
		GetIssueFn: func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
			id := int64(9999)
			state := "open"
			title := "nil updated_at"
			url := "https://github.com/acme/widget/issues/9"
			author := "alice"
			createdTs := gh.Timestamp{Time: createdAt}
			return &gh.Issue{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &createdTs,
				UpdatedAt: nil,
			}, nil
		},
	}

	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedIssue(t, database, "acme", "widget", 9, "open")
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(ctx, `
		UPDATE forge_issues
		SET created_at = ?, updated_at = ?, last_activity_at = ?
		WHERE repo_id = ? AND number = 9`,
		createdAt.Add(-time.Hour), createdAt.Add(-time.Hour), createdAt.Add(-time.Hour), repo.ID)
	require.NoError(err)
	client := servertest.SetupTestClient(t, srv)

	// Before the nil guard, refreshIssueTimeline panicked on
	// ghIssue.UpdatedAt.Time and the handler returned 502.
	syncResp, err := client.HTTP.SyncIssueWithResponse(ctx, &generated.SyncIssueRequestOptions{PathParams: &generated.SyncIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(9)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode)
	require.NotNil(syncResp.JSON200)
	// LastActivityAt must equal CreatedAt, not Go's zero time.
	// Without the fallback, activity-ordered views would sort
	// this issue at 0001-01-01 instead of its creation date.
	assert.False(syncResp.JSON200.Issue.LastActivityAt.IsZero())
	assert.Equal(createdAt, syncResp.JSON200.Issue.LastActivityAt.UTC())

	// Verify the persisted value round-trips through the read
	// endpoint so the storage -> serializer path is covered.
	getResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(9)}})
	require.NoError(err)
	require.Equal(http.StatusOK, getResp.StatusCode)
	require.NotNil(getResp.JSON200)
	assert.Equal(createdAt, getResp.JSON200.Issue.LastActivityAt.UTC())
}

func TestAPIListIssuesSearchByNumber(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := servertest.SetupTestServer(t)
	ctx := t.Context()

	serverfake.SeedIssueOnHost(t, database, "github.com", "acme", "widget", 12, "open", "report a bug")
	issueID := serverfake.SeedIssueOnHost(t, database, "github.com", "acme", "widget", 278, "open", "filter broken")
	serverfake.SeedIssueOnHost(t, database, "github.com", "acme", "widget", 290, "open", "another change")
	serverfake.SeedIssueOnHost(t, database, "github.com", "tools", "worker", 301, "open", "triage bug")
	serverfake.SeedIssueOnHost(t, database, "github.com", "docs", "reader", 302, "open", "O'Reilly reference")
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NoError(database.ReplaceIssueLabels(ctx, repo.ID, issueID, []db.Label{{
		PlatformID: 300,
		Name:       "needs-triage",
		Color:      "d73a4a",
		UpdatedAt:  time.Now().UTC(),
	}}))

	client := servertest.SetupTestClient(t, srv)

	issueNumbers := func(params *generated.ListIssuesQuery) []int {
		t.Helper()
		resp, err := client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesRequestOptions{Query: params})
		require.NoError(err)
		require.Equal(http.StatusOK, resp.StatusCode)
		require.NotNil(resp.JSON200)
		nums := make([]int, 0, len(*resp.JSON200))
		for _, issue := range *resp.JSON200 {
			nums = append(nums, int(issue.Number))
		}
		return nums
	}

	q := "278"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	q = "#278"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	// Title still matches.
	q = "broken"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	q = "filter widget"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	q = "work bug"
	assert.ElementsMatch([]int{301}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	q = "needs-triage filter"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	q = "O'Reilly"
	assert.ElementsMatch([]int{302}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	q = "needs-triage"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	// Substring of number matches multiple.
	q = "2"
	assert.ElementsMatch([]int{12, 278, 290, 302}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))
}

func TestAPIListIssuesAcceptsProviderAndHostQualifiedRepoFilter(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedIssueOnHost(t, database, "github.com", "acme", "widget", 5, "open", "GitHub issue")
	serverfake.SeedIssueOnHost(t, database, "ghe.example.com", "acme", "widget", 7, "open", "Enterprise issue")
	client := servertest.SetupTestClient(t, srv)

	repo := "github|ghe.example.com/acme/widget"
	resp, err := client.HTTP.ListIssuesWithResponse(t.Context(), &generated.ListIssuesRequestOptions{Query: &generated.ListIssuesQuery{Repo: &repo}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("ghe.example.com", (*resp.JSON200)[0].PlatformHost)
	assert.Equal("acme", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("widget", (*resp.JSON200)[0].RepoName)
	assert.EqualValues(7, (*resp.JSON200)[0].Number)
}

func TestAPIListIssuesFiltersProviderQualifiedHostedNestedRepoPath(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)

	srv, database := servertest.SetupTestServerWithRepos(t, &serverfake.MockGH{}, []ghclient.RepoRef{
		{Owner: "Group/SubGroup", Name: "Project.Special", PlatformHost: "ghe.example.com"},
		{Owner: "other", Name: "repo", PlatformHost: "ghe.example.com"},
	})
	serverfake.SeedIssueOnHost(
		t, database,
		"ghe.example.com", "Group/SubGroup", "Project.Special", 1,
		"open", "Nested issue",
	)
	serverfake.SeedIssueOnHost(
		t, database,
		"ghe.example.com", "other", "repo", 2,
		"open", "Other issue",
	)
	client := servertest.SetupTestClient(t, srv)

	repo := "github|ghe.example.com/Group/SubGroup/Project.Special"
	resp, err := client.HTTP.ListIssuesWithResponse(t.Context(), &generated.ListIssuesRequestOptions{Query: &generated.ListIssuesQuery{Repo: &repo}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("ghe.example.com", (*resp.JSON200)[0].PlatformHost)
	assert.Equal("group/subgroup", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("project.special", (*resp.JSON200)[0].RepoName)
	assert.EqualValues(1, (*resp.JSON200)[0].Number)
}

func TestAPIListIssuesAcceptsProviderQualifiedRepoFilter(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	client := servertest.SetupTestClient(t, srv)
	ctx := t.Context()

	githubRepo, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "github-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	giteaRepo, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitea",
		PlatformHost:   "github.com",
		PlatformRepoID: "gitea-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	serverfake.SeedIssueForRepo(t, database, githubRepo, "github.com", "acme", "widget", 1, "open", "GitHub issue")
	serverfake.SeedIssueForRepo(t, database, giteaRepo, "github.com", "acme", "widget", 2, "open", "Gitea issue")

	repo := "gitea|github.com/acme/widget"
	resp, err := client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesRequestOptions{Query: &generated.ListIssuesQuery{Repo: &repo}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("gitea", (*resp.JSON200)[0].Repo.Provider)
	assert.Equal("github.com", (*resp.JSON200)[0].PlatformHost)
	assert.Equal("acme", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("widget", (*resp.JSON200)[0].RepoName)
	assert.EqualValues(2, (*resp.JSON200)[0].Number)
}

func TestAPIGetIssueUsesPlatformHostQuery(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	serverfake.SeedIssueOnHost(
		t, database,
		"github.com", "acme", "widget", 7,
		"open", "GitHub issue",
	)
	serverfake.SeedIssueOnHost(
		t, database,
		"ghe.example.com", "acme", "widget", 7,
		"open", "GHES issue",
	)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com":      &serverfake.MockGH{},
			"ghe.example.com": &serverfake.MockGH{},
		},
		database,
		nil,
		[]ghclient.RepoRef{
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "ghe.example.com"},
		},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(
		database, syncer, nil, "/", nil, server.ServerOptions{},
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/host/ghe.example.com/issues/gh/acme/widget/7",
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	var body rawIssueDetailResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal("ghe.example.com", body.PlatformHost)
	if assert.NotNil(body.Issue) {
		assert.Equal("GHES issue", body.Issue.Title)
	}
}

func TestAPISyncIssueUsesPlatformHostQuery(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()

	database := dbtest.Open(t)

	serverfake.SeedIssueOnHost(
		t, database,
		"github.com", "acme", "widget", 7,
		"open", "GitHub stale issue",
	)
	serverfake.SeedIssueOnHost(
		t, database,
		"ghe.example.com", "acme", "widget", 7,
		"open", "GHES stale issue",
	)

	githubClient := &serverfake.MockGH{
		GetIssueFn: func(_ context.Context, owner, repo string, number int) (*gh.Issue, error) {
			title := "GitHub synced issue"
			state := "open"
			url := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, number)
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.Issue{
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				User:      &gh.User{Login: new("github-user")},
			}, nil
		},
	}
	ghesClient := &serverfake.MockGH{
		GetIssueFn: func(_ context.Context, owner, repo string, number int) (*gh.Issue, error) {
			title := "GHES synced issue"
			state := "open"
			url := fmt.Sprintf("https://ghe.example.com/%s/%s/issues/%d", owner, repo, number)
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.Issue{
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				User:      &gh.User{Login: new("ghes-user")},
			}, nil
		},
	}

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com":      githubClient,
			"ghe.example.com": ghesClient,
		},
		database,
		nil,
		[]ghclient.RepoRef{
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "ghe.example.com"},
		},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(
		database, syncer, nil, "/", nil, server.ServerOptions{},
	)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/host/ghe.example.com/issues/gh/acme/widget/7/sync",
		http.NoBody,
	).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	var body rawIssueDetailResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal("ghe.example.com", body.PlatformHost)
	if assert.NotNil(body.Issue) {
		assert.Equal("GHES synced issue", body.Issue.Title)
	}
	// The frontend replaces the issue detail payload with this
	// response: it must carry operations or every mutation gate would
	// clear after a sync.
	var withOps struct {
		Repo struct {
			Operations *httpapi.RepoOperations `json:"operations"`
		} `json:"repo"`
	}
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &withOps))
	require.NotNil(withOps.Repo.Operations,
		"issue sync response must include repo.operations")

	githubRepo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(githubRepo)
	githubIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx, githubRepo.ID, 7,
	)
	require.NoError(err)
	require.NotNil(githubIssue)
	assert.Equal("GitHub stale issue", githubIssue.Title)

	ghesRepo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("ghe.example.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(ghesRepo)
	ghesIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx, ghesRepo.ID, 7,
	)
	require.NoError(err)
	require.NotNil(ghesIssue)
	assert.Equal("GHES synced issue", ghesIssue.Title)
}

func TestAPISetIssueStateUsesPlatformHostBody(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()

	database := dbtest.Open(t)

	serverfake.SeedIssueOnHost(
		t, database,
		"github.com", "acme", "widget", 7,
		"open", "GitHub issue",
	)
	serverfake.SeedIssueOnHost(
		t, database,
		"ghe.example.com", "acme", "widget", 7,
		"open", "GHES issue",
	)

	githubClient := &serverfake.MockGH{
		EditIssueFn: func(_ context.Context, _, _ string, number int, state string) (*gh.Issue, error) {
			url := fmt.Sprintf("https://github.com/acme/widget/issues/%d", number)
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			title := "GitHub issue"
			return &gh.Issue{
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				User:      &gh.User{Login: new("github-user")},
			}, nil
		},
	}
	ghesClient := &serverfake.MockGH{
		EditIssueFn: func(_ context.Context, _, _ string, number int, state string) (*gh.Issue, error) {
			url := fmt.Sprintf("https://ghe.example.com/acme/widget/issues/%d", number)
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			title := "GHES issue"
			return &gh.Issue{
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				User:      &gh.User{Login: new("ghes-user")},
			}, nil
		},
	}

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com":      githubClient,
			"ghe.example.com": ghesClient,
		},
		database,
		nil,
		[]ghclient.RepoRef{
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "ghe.example.com"},
		},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(
		database, syncer, nil, "/", nil, server.ServerOptions{},
	)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.SetIssueGithubStateOnHostWithResponse(ctx, &generated.SetIssueGithubStateOnHostRequestOptions{PathParams: &generated.SetIssueGithubStateOnHostPath{PlatformHost: "ghe.example.com", Provider: "gh", Owner: "acme", Name: "widget", Number: int64(7)}, Body: &generated.SetIssueGithubStateOnHostBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	githubRepo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(githubRepo)
	githubIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx, githubRepo.ID, 7,
	)
	require.NoError(err)
	require.NotNil(githubIssue)
	assert.Equal("open", githubIssue.State)

	ghesRepo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("ghe.example.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(ghesRepo)
	ghesIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx, ghesRepo.ID, 7,
	)
	require.NoError(err)
	require.NotNil(ghesIssue)
	assert.Equal("closed", ghesIssue.State)
}

// TestAPIIssueDataFromGraphQLSync verifies the API correctly serves
// issue data that was persisted by the GraphQL sync path. The sync
// path itself (GraphQL fetch → normalize → DB upsert) is tested in
// internal/github/sync_test.go; this test covers the DB → API layer.
func TestAPIIssueDataFromGraphQLSync(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	mock := &serverfake.MockGH{}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	client := servertest.SetupTestClient(t, srv)

	// Seed DB directly — same shape as GraphQL sync output.
	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	issueID, err := database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     60000,
		Number:         60,
		URL:            "https://github.com/acme/widget/issues/60",
		Title:          "GraphQL synced issue",
		Author:         "testuser",
		State:          "open",
		Body:           "Synced via GraphQL",
		CommentCount:   1,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	// Add a label
	require.NoError(database.ReplaceIssueLabels(ctx, repoID, issueID, []db.Label{
		{PlatformID: 1, Name: "bug", Color: "d73a4a", UpdatedAt: now},
	}))

	// Add a comment event
	require.NoError(database.UpsertIssueEvents(ctx, []db.IssueEvent{
		{
			IssueID:   issueID,
			EventType: "issue_comment",
			Author:    "commenter",
			Body:      "I can reproduce",
			CreatedAt: now,
			DedupeKey: "issue-comment-601",
		},
	}))

	// Verify via ListIssues API
	resp, err := client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesRequestOptions{})
	require.NoError(err)
	require.Equal(200, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	apiIssue := (*resp.JSON200)[0]
	assert.Equal(int64(60), apiIssue.Number)
	assert.Equal("GraphQL synced issue", apiIssue.Title)
	assert.Equal("testuser", apiIssue.Author)
	assert.Equal("open", apiIssue.State)
	require.NotNil(apiIssue.Labels)
	require.Len(apiIssue.Labels, 1)
	assert.Equal("bug", apiIssue.Labels[0].Name)

	// Verify via GetIssue API
	detailResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(60)}})
	require.NoError(err)
	require.Equal(200, detailResp.StatusCode)
	require.NotNil(detailResp.JSON200)
	assert.Equal("Synced via GraphQL", detailResp.JSON200.Issue.Body)
	assert.Equal(int64(1), detailResp.JSON200.Issue.CommentCount)
}

func TestAPISetIssueGitHubStateReturns404WhenNoClientConfigured(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "ghe.corp.com"}}
	srv, database := servertest.SetupTestServerWithRepos(t, &serverfake.MockGH{}, repos)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("ghe.corp.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     5000,
		Number:         5,
		URL:            "https://ghe.corp.com/acme/widget/issues/5",
		Title:          "Issue",
		Author:         "u",
		State:          "open",
		CreatedAt:      time.Now().UTC().Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Truncate(time.Second),
	})
	require.NoError(err)

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.SetIssueGithubStateWithResponse(ctx, &generated.SetIssueGithubStateRequestOptions{PathParams: &generated.SetIssueGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.SetIssueGithubStateBody{State: "closed"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestAPICloseIssue422NilFallbackPayloadDoesNotCorruptDB(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &serverfake.MockGH{
		EditIssueFn: func(_ context.Context, _, _ string, _ int, _ string) (*gh.Issue, error) {
			return nil, serverfake.Make422Error()
		},
		GetIssueFn: func(_ context.Context, _, _ string, _ int) (*gh.Issue, error) {
			return nil, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	before, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	require.NotNil(before)

	client := servertest.SetupTestClient(t, srv)
	resp, err := client.HTTP.SetIssueGithubStateWithResponse(t.Context(), &generated.SetIssueGithubStateRequestOptions{PathParams: &generated.SetIssueGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.SetIssueGithubStateBody{State: "closed"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	after, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	require.NotNil(after)
	assert.Equal(before.State, after.State)
	assert.Equal(before.UpdatedAt, after.UpdatedAt)
	assert.Nil(after.ClosedAt)
}

func TestResolveItem_Issue(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget"}}
	srv, database := servertest.SetupTestServerWithRepos(t, &serverfake.MockGH{}, repos)
	serverfake.SeedIssue(t, database, "acme", "widget", 7, "open")
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.ResolveRepoItemWithResponse(t.Context(), &generated.ResolveRepoItemRequestOptions{PathParams: &generated.ResolveRepoItemPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Equal("issue", resp.JSON200.ItemType)
	require.EqualValues(7, resp.JSON200.Number)
	require.True(resp.JSON200.RepoTracked)
}

func TestAPICloseIssue422AlreadyClosed(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	state := "closed"
	mock := &serverfake.MockGH{
		EditIssueFn: func(_ context.Context, _, _ string, _ int, _ string) (*gh.Issue, error) {
			return nil, serverfake.Make422Error()
		},
		GetIssueFn: func(_ context.Context, _, _ string, _ int) (*gh.Issue, error) {
			id := int64(5000)
			now := gh.Timestamp{Time: time.Now().UTC()}
			closedAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.Issue{
				ID: &id, Number: new(5), State: &state,
				Title: new("Issue"), HTMLURL: new("https://example.com"),
				User:      &gh.User{Login: new("u")},
				CreatedAt: &now, UpdatedAt: &now, ClosedAt: &closedAt,
			}, nil
		},
	}
	srv, database := servertest.SetupTestServerWithMock(t, mock)
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	client := servertest.SetupTestClient(t, srv)

	resp, err := client.HTTP.SetIssueGithubStateWithResponse(t.Context(), &generated.SetIssueGithubStateRequestOptions{PathParams: &generated.SetIssueGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.SetIssueGithubStateBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	issue, _ := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.Equal("closed", issue.State)
}

type rawIssueWorkspaceRef struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type rawIssueSummary struct {
	Title string `json:"title"`
	State string `json:"state"`
}

type rawIssueDetailResponse struct {
	Issue        *rawIssueSummary      `json:"issue"`
	PlatformHost string                `json:"platform_host"`
	RepoOwner    string                `json:"repo_owner"`
	RepoName     string                `json:"repo_name"`
	Workspace    *rawIssueWorkspaceRef `json:"workspace"`
}

func TestAPIEditIssueBodyOnly(t *testing.T) {
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5",
		map[string]string{"body": "- [x] task done"})

	require.Equal(http.StatusOK, rr.Code)

	issue, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	require.NotNil(issue)
	require.Equal("Test Issue", issue.Title)
	require.Equal("- [x] task done", issue.Body)
}

func TestAPIEditIssueTitleAndBody(t *testing.T) {
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5",
		map[string]string{"title": "new title", "body": "new body"})

	require.Equal(http.StatusOK, rr.Code)

	issue, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	require.NotNil(issue)
	require.Equal("new title", issue.Title)
	require.Equal("new body", issue.Body)
}

func TestAPIEditIssueNoFields400(t *testing.T) {
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5",
		map[string]any{})

	require.Equal(http.StatusBadRequest, rr.Code)
}

func TestAPIEditIssueBlankTitle400(t *testing.T) {
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5",
		map[string]string{"title": "   "})

	require.Equal(http.StatusBadRequest, rr.Code)
}

func TestAPIEditIssueMissing404(t *testing.T) {
	require := require.New(t)
	srv, database := servertest.SetupTestServer(t)
	// Register the repo without the issue.
	_, err := database.UpsertRepo(
		t.Context(),
		serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/999",
		map[string]string{"body": "anything"})

	require.Equal(http.StatusNotFound, rr.Code)
}

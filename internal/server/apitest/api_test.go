package apitest

import (
	"net/http"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/db"
)

func TestAPIListPullsIncludesLabels(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	description := "Needs a fix"
	seedPRWithLabels(t, database, "acme", "widget", 1, []db.Label{{
		Name:        "bug",
		Description: description,
		Color:       "d73a4a",
		IsDefault:   true,
	}})
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListPullsWithResponse(t.Context(), nil)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	require.NotNil((*resp.JSON200)[0].Labels)
	require.Equal([]generated.Label{{
		Name:        "bug",
		Description: &description,
		Color:       "d73a4a",
		IsDefault:   true,
	}}, *(*resp.JSON200)[0].Labels)
}

func TestAPIGetPull(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPRWithHeadSHA(t, database, "acme", "widget", 1, "abc123def456")
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.MergeRequest)
	require.EqualValues(1, resp.JSON200.MergeRequest.Number)
	require.Equal("acme", resp.JSON200.RepoOwner)
	require.Equal("widget", resp.JSON200.RepoName)
	require.Equal("abc123def456", resp.JSON200.PlatformHeadSha)
}

func TestAPIGetPullAcceptsMixedCaseRepoPath(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "Acme", "Widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Equal("acme", resp.JSON200.RepoOwner)
	require.Equal("widget", resp.JSON200.RepoName)
}

func TestAPIListPullsAcceptsMixedCaseRepoFilter(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	repo := "Acme/Widget"
	resp, err := client.HTTP.ListPullsWithResponse(
		t.Context(), &generated.ListPullsParams{Repo: &repo},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	require.Equal("acme", (*resp.JSON200)[0].RepoOwner)
	require.Equal("widget", (*resp.JSON200)[0].RepoName)
}

func TestAPIListPullsAcceptsHostQualifiedRepoFilter(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, database := setupTestServer(t)
	seedPROnHost(t, database, "github.com", "acme", "widget", 1)
	seedPROnHost(t, database, "ghe.example.com", "acme", "widget", 2)
	client := setupTestClient(t, srv)

	repo := "ghe.example.com/acme/widget"
	resp, err := client.HTTP.ListPullsWithResponse(
		t.Context(), &generated.ListPullsParams{Repo: &repo},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("ghe.example.com", (*resp.JSON200)[0].PlatformHost)
	assert.Equal("acme", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("widget", (*resp.JSON200)[0].RepoName)
	assert.EqualValues(2, (*resp.JSON200)[0].Number)
}

func TestAPIGetPullIncludesBranches(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	mr := resp.JSON200.MergeRequest
	require.NotNil(mr)
	require.Equal("feature", mr.HeadBranch)
	require.Equal("main", mr.BaseBranch)
}

func TestAPIGetPullIncludesLabels(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPRWithLabels(t, database, "acme", "widget", 1, []db.Label{{
		Name:      "enhancement",
		Color:     "a2eeef",
		IsDefault: false,
	}})
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.MergeRequest.Labels)
	require.Equal([]generated.Label{{
		Name:      "enhancement",
		Color:     "a2eeef",
		IsDefault: false,
	}}, *resp.JSON200.MergeRequest.Labels)
}

func TestAPIListPullsStateFilter(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	seedPR(t, database, "acme", "widget", 1)
	seedPR(t, database, "acme", "widget", 2)
	seedPR(t, database, "acme", "widget", 3)

	repo, _ := database.GetRepoByOwnerName(ctx, "acme", "widget")
	now := time.Now()
	require.NoError(database.UpdateMRState(ctx, repo.ID, 2, "closed", nil, &now))
	require.NoError(database.UpdateMRState(ctx, repo.ID, 3, "merged", &now, &now))

	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListPullsWithResponse(ctx, nil)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.Len(*resp.JSON200, 1)

	state := "closed"
	resp, err = client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsParams{State: &state})
	require.NoError(err)
	require.Len(*resp.JSON200, 2)

	state = "all"
	resp, err = client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsParams{State: &state})
	require.NoError(err)
	require.Len(*resp.JSON200, 3)

	state = "bogus"
	resp, err = client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsParams{State: &state})
	require.NoError(err)
	require.Equal(http.StatusBadRequest, resp.StatusCode())
}

func TestAPIListIssuesStateFilter(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	seedIssue(t, database, "acme", "widget", 1, "open")
	seedIssue(t, database, "acme", "widget", 2, "closed")

	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListIssuesWithResponse(ctx, nil)
	require.NoError(err)
	require.Len(*resp.JSON200, 1)

	state := "closed"
	resp, err = client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesParams{State: &state})
	require.NoError(err)
	require.Len(*resp.JSON200, 1)

	state = "all"
	resp, err = client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesParams{State: &state})
	require.NoError(err)
	require.Len(*resp.JSON200, 2)
}

func TestAPIListIssuesFilterByAssignee(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	seedIssueWithAssignees(t, database, "acme", "widget", 1, "open", `["alice"]`)
	seedIssueWithAssignees(t, database, "acme", "widget", 2, "open", `["bob"]`)

	client := setupTestClient(t, srv)

	assignee := "alice"
	state := "all"
	resp, err := client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesParams{
		Assignee: &assignee,
		State:    &state,
	})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	require.EqualValues(1, (*resp.JSON200)[0].Number)
}

func TestAPIListIssuesResponseIncludesAssignees(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	seedIssueWithAssignees(t, database, "acme", "widget", 1, "open", `["alice","bob"]`)

	client := setupTestClient(t, srv)

	state := "all"
	resp, err := client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesParams{
		State: &state,
	})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	require.NotNil((*resp.JSON200)[0].Assignees)
	require.Equal([]string{"alice", "bob"}, *(*resp.JSON200)[0].Assignees)
}

func TestAPIGetIssueIncludesAssignees(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssueWithAssignees(t, database, "acme", "widget", 7, "open", `["alice","bob"]`)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.GetIssueWithResponse(
		t.Context(), "gh", "acme", "widget", 7,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Issue.Assignees)
	require.Equal([]string{"alice", "bob"}, *resp.JSON200.Issue.Assignees)
}

func TestAPISyncIssuePersistsAssigneesFromProvider(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	srv, database, providerClient, syncer := setupTestServerWithFixtureClient(t)
	issueID := int64(7000)
	issueNumber := 7
	title := "Synced issue with assignees"
	state := "open"
	url := "https://github.com/acme/widget/issues/7"
	author := "octocat"
	createdAt := gh.Timestamp{Time: now}
	updatedAt := gh.Timestamp{Time: now}
	providerClient.Issues["acme/widget"] = []*gh.Issue{{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &title,
		State:     &state,
		HTMLURL:   &url,
		User:      &gh.User{Login: &author},
		CreatedAt: &createdAt,
		UpdatedAt: &updatedAt,
		Assignees: []*gh.User{{Login: gh.String("alice")}, {Login: gh.String("bob")}},
	}}

	require.NoError(syncer.SyncIssue(ctx, "acme", "widget", issueNumber))

	repo, err := database.GetRepoByOwnerName(ctx, "acme", "widget")
	require.NoError(err)
	require.NotNil(repo)
	persisted, err := database.GetIssueByRepoIDAndNumber(ctx, repo.ID, issueNumber)
	require.NoError(err)
	require.NotNil(persisted)
	assert.JSONEq(`["alice","bob"]`, persisted.AssigneesJSON)
	assert.Equal([]string{"alice", "bob"}, persisted.Assignees)

	client := setupTestClient(t, srv)
	stateParam := "all"
	listResp, err := client.HTTP.ListIssuesWithResponse(
		ctx, &generated.ListIssuesParams{State: &stateParam},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode())
	require.NotNil(listResp.JSON200)
	require.Len(*listResp.JSON200, 1)
	require.NotNil((*listResp.JSON200)[0].Assignees)
	assert.Equal([]string{"alice", "bob"}, *(*listResp.JSON200)[0].Assignees)

	assignee := "bob"
	filterResp, err := client.HTTP.ListIssuesWithResponse(
		ctx, &generated.ListIssuesParams{Assignee: &assignee, State: &stateParam},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, filterResp.StatusCode())
	require.NotNil(filterResp.JSON200)
	require.Len(*filterResp.JSON200, 1)
	assert.EqualValues(issueNumber, (*filterResp.JSON200)[0].Number)

	detailResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode())
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.Issue.Assignees)
	assert.Equal([]string{"alice", "bob"}, *detailResp.JSON200.Issue.Assignees)
}

func TestAPIGetIssueIncludesLabels(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	description := "Customer reported"
	seedIssueWithLabels(t, database, "acme", "widget", 5, "open", []db.Label{{
		Name:        "bug",
		Description: description,
		Color:       "d73a4a",
		IsDefault:   true,
	}})
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.GetIssueWithResponse(
		t.Context(), "gh", "acme", "widget", 5,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Issue.Labels)
	require.Equal([]generated.Label{{
		Name:        "bug",
		Description: &description,
		Color:       "d73a4a",
		IsDefault:   true,
	}}, *resp.JSON200.Issue.Labels)
}

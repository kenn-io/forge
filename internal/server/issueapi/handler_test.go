package issueapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestHandlerRegistersOnlyIssueRoutes(t *testing.T) {
	t.Parallel()

	api := humago.New(http.NewServeMux(), huma.DefaultConfig("test", "0"))
	New(Deps{}).Register(api)
	assert := assert.New(t)

	type routeContract struct {
		method string
		path   string
		status int
	}
	issueRepo := "/issues/{provider}/{owner}/{name}"
	hostIssueRepo := "/host/{platform_host}" + issueRepo
	issue := issueRepo + "/{number}"
	hostIssue := hostIssueRepo + "/{number}"
	want := map[string]routeContract{
		"list-issues": {http.MethodGet, "/issues", http.StatusOK},
	}
	addPair := func(id, method, suffix string, status int) {
		want[id] = routeContract{method, issue + suffix, status}
		want[id+"-on-host"] = routeContract{method, hostIssue + suffix, status}
	}
	want["create-issue"] = routeContract{http.MethodPost, issueRepo, http.StatusCreated}
	want["create-issue-on-host"] = routeContract{http.MethodPost, hostIssueRepo, http.StatusCreated}
	addPair("get-issue", http.MethodGet, "", http.StatusOK)
	addPair("post-issue-comment", http.MethodPost, "/comments", http.StatusCreated)
	addPair("edit-issue-content", http.MethodPatch, "", http.StatusOK)
	addPair("edit-issue-comment", http.MethodPatch, "/comments/{comment_id}", http.StatusOK)
	addPair("delete-issue-comment", http.MethodDelete, "/comments/{comment_id}", http.StatusNoContent)
	addPair("set-issue-labels", http.MethodPut, "/labels", http.StatusOK)
	addPair("set-issue-assignees", http.MethodPut, "/assignees", http.StatusOK)
	addPair("set-issue-github-state", http.MethodPost, "/github-state", http.StatusOK)

	gotByID := make(map[string]*huma.Operation)
	gotPathByID := make(map[string]string)
	for path, item := range api.OpenAPI().Paths {
		for _, operation := range []*huma.Operation{
			item.Get, item.Put, item.Post, item.Delete, item.Patch,
		} {
			if operation != nil {
				gotByID[operation.OperationID] = operation
				gotPathByID[operation.OperationID] = path
			}
		}
	}
	assert.Len(gotByID, len(want))
	for operationID, expected := range want {
		gotOperation := gotByID[operationID]
		if assert.NotNil(gotOperation, operationID) {
			assert.Equal(expected.method, gotOperation.Method, operationID)
			assert.Equal(expected.status, gotOperation.DefaultStatus, operationID)
		}
		assert.Equal(expected.path, gotPathByID[operationID], operationID)
	}
	for _, operationID := range []string{
		"sync-issue", "sync-issue-on-host",
		"enqueue-issue-sync", "enqueue-issue-sync-on-host",
		"create-issue-workspace", "create-issue-workspace-on-host",
		"list-pulls", "get-pull", "get-repo", "list-repo-labels",
	} {
		assert.NotContains(gotByID, operationID)
	}
}

func TestIssueDetailTreatsWorkspaceSnapshotFailureAsBestEffort(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	identity.PlatformRepoID = "repo-acme-widget"
	repoID, err := database.UpsertRepo(t.Context(), identity)
	require.NoError(err)
	_, err = database.UpsertIssue(t.Context(), &db.Issue{
		RepoID: repoID, PlatformID: 43, Number: 43, Title: "Available issue",
		Author: "bob", State: "open",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)
	repo, err := database.GetRepoByID(t.Context(), repoID)
	require.NoError(err)
	require.NotNil(repo)
	issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 43)
	require.NoError(err)
	require.NotNil(issue)
	handler := New(Deps{
		DB:       database,
		Resolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: database}),
		WorkspaceSubjects: func(context.Context) (workspaceapi.WorkspaceSubjectSnapshot, error) {
			return workspaceapi.WorkspaceSubjectSnapshot{}, errors.New("snapshot unavailable")
		},
	})

	response, err := handler.BuildDetail(t.Context(), repo, issue)
	require.NoError(err)
	require.NotNil(response.Issue)
	assert.Equal(t, 43, response.Issue.Number)
	assert.Nil(t, response.Workspace)
}

type stubIssueProviderSource struct {
	rows   []IssueResponse
	detail IssueDetailResponse
}

func (s stubIssueProviderSource) ListIssues(
	context.Context, ListQuery,
) ([]IssueResponse, error) {
	return s.rows, nil
}

func (s stubIssueProviderSource) GetIssue(
	context.Context, ItemIdentity,
) (IssueDetailResponse, error) {
	return s.detail, nil
}

func TestProviderFetchPreservesEmptyIssueList(t *testing.T) {
	t.Parallel()
	handler := New(Deps{
		ProviderSource: stubIssueProviderSource{rows: []IssueResponse{}},
	})

	rows, err := handler.ListService(t.Context(), ListQuery{})
	require.NoError(t, err)
	require.NotNil(t, rows)
	require.Empty(t, rows)
}

func TestProviderFetchOverlaysLocalIssueWorkspaceWithoutReordering(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Parallel()

	key := db.WorkspaceSubjectKey{
		RepoID: 7, ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 1,
	}
	ref := workspaceapi.WorkspaceRef{ID: "ws-local", Status: "ready"}
	handler := New(Deps{
		ProviderSource: stubIssueProviderSource{rows: []IssueResponse{
			{RepoID: 91, Number: 2, Repo: httpapi.RepoRefResponse{
				Provider: "github", PlatformHost: "github.com", PlatformRepoID: "repo-widget",
			}},
			{RepoID: 91, Number: 1, Repo: httpapi.RepoRefResponse{
				Provider: "github", PlatformHost: "github.com", PlatformRepoID: "repo-widget",
			}},
		}},
		WorkspaceSubjects: func(context.Context) (workspaceapi.WorkspaceSubjectSnapshot, error) {
			return workspaceapi.WorkspaceSubjectSnapshot{
				OwnReferences: map[db.WorkspaceSubjectKey]workspaceapi.WorkspaceRef{key: ref},
				Subjects: map[db.WorkspaceSubjectKey]workspaceapi.SubjectActivity{
					key: {
						Subject: db.WorkspaceSubjectMetadata{
							Key: key, Platform: "github", PlatformHost: "github.com",
							PlatformRepoID: "repo-widget",
						},
						Workspace: ref,
					},
				},
			}, nil
		},
	})

	rows, err := handler.ListService(t.Context(), ListQuery{})
	require.NoError(err)
	require.Len(rows, 2)
	assert.Equal([]int{2, 1}, []int{rows[0].Number, rows[1].Number})
	assert.Nil(rows[0].Workspace)
	require.NotNil(rows[1].Workspace)
	assert.Equal("ws-local", rows[1].Workspace.ID)
}

func TestProviderFetchReplacesHubIssueDetailWorkspaceWithLocalWorkspace(t *testing.T) {
	t.Parallel()

	key := db.WorkspaceSubjectKey{
		RepoID: 7, ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 42,
	}
	local := workspaceapi.WorkspaceRef{ID: "ws-local", Status: "ready"}
	handler := New(Deps{
		ProviderSource: stubIssueProviderSource{detail: IssueDetailResponse{
			Issue: &db.Issue{RepoID: 91, Number: 42},
			Repo: httpapi.RepoRefResponse{
				Provider: "github", PlatformHost: "github.com", PlatformRepoID: "repo-widget",
			},
			Workspace: &workspaceapi.WorkspaceRef{ID: "ws-hub", Status: "ready"},
		}},
		WorkspaceSubjects: func(context.Context) (workspaceapi.WorkspaceSubjectSnapshot, error) {
			return workspaceapi.WorkspaceSubjectSnapshot{
				OwnReferences: map[db.WorkspaceSubjectKey]workspaceapi.WorkspaceRef{key: local},
				Subjects: map[db.WorkspaceSubjectKey]workspaceapi.SubjectActivity{
					key: {
						Subject: db.WorkspaceSubjectMetadata{
							Key: key, Platform: "github", PlatformHost: "github.com",
							PlatformRepoID: "repo-widget",
						},
						Workspace: local,
					},
				},
			}, nil
		},
	})

	detail, err := handler.GetService(t.Context(), ItemIdentity{
		Provider: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "widget", Number: 42,
	})

	require.NoError(t, err)
	require.NotNil(t, detail.Workspace)
	assert.Equal(t, "ws-local", detail.Workspace.ID)
}

func TestListIssuesWorkspaceActivityRecencyIsOptIn(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	base := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	identity := db.GitHubRepoIdentity("github.com", "acme", "widget")
	identity.PlatformRepoID = "repo-acme-widget"
	repoID, err := database.UpsertRepo(t.Context(), identity)
	require.NoError(err)
	for number, activityAt := range map[int]time.Time{1: base, 2: base.Add(time.Hour)} {
		_, err = database.UpsertIssue(t.Context(), &db.Issue{
			RepoID: repoID, PlatformID: int64(number), Number: number, Title: "Work",
			Author: "alice", State: "open",
			CreatedAt: activityAt, UpdatedAt: activityAt, LastActivityAt: activityAt,
		})
		require.NoError(err)
	}
	key := db.WorkspaceSubjectKey{RepoID: repoID, ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 1}
	workspaceAt := base.Add(2 * time.Hour)
	ref := workspaceapi.WorkspaceRef{ID: "ws-1", Status: "ready"}
	handler := New(Deps{
		DB: database, Resolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: database}),
		WorkspaceSubjects: func(context.Context) (workspaceapi.WorkspaceSubjectSnapshot, error) {
			return workspaceapi.WorkspaceSubjectSnapshot{
				OwnReferences: map[db.WorkspaceSubjectKey]workspaceapi.WorkspaceRef{key: ref},
				Subjects: map[db.WorkspaceSubjectKey]workspaceapi.SubjectActivity{
					key: {
						Subject: db.WorkspaceSubjectMetadata{
							Key: key, Platform: "github", PlatformHost: "github.com",
							PlatformRepoID: identity.PlatformRepoID,
						},
						Workspace: ref, ActivityAt: &workspaceAt,
					},
				},
			}, nil
		},
	})

	disabled, err := handler.listIssues(t.Context(), &listIssuesInput{State: "open"})
	require.NoError(err)
	require.Len(disabled.Body, 2)
	assert.Equal(t, 2, disabled.Body[0].Number)
	assert.Equal(t, workspaceAt.Format(time.RFC3339), disabled.Body[1].LastWorkspaceActivityAt)
	require.NotNil(disabled.Body[1].Workspace)

	handler.ApplyConfig(ConfigSnapshot{UseWorkspaceActivityForRecency: true})
	enabled, err := handler.listIssues(t.Context(), &listIssuesInput{State: "open"})
	require.NoError(err)
	require.Len(enabled.Body, 2)
	assert.Equal(t, 1, enabled.Body[0].Number)
}

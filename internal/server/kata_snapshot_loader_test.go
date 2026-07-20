package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/doordash-oss/oapi-codegen-dd/v3/pkg/runtime"
	"github.com/stretchr/testify/require"
	katagenerated "go.kenn.io/kata/pkg/client/generated"
)

func TestKataSnapshotLoaderLoadsGlobalReadyAuthority(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	createdAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	projectUID := "project-a"
	owner := "marius"
	priority := int64(1)
	loader := kataSnapshotLoader{
		client: &fakeKataSnapshotAPIClient{
			listProjects: func(_ context.Context, options *katagenerated.ListProjectsRequestOptions) (*katagenerated.ListProjectsResp, error) {
				require.NotNil(options)
				require.NotNil(options.Query)
				require.Equal("stats", *options.Query.Include)
				return &katagenerated.ListProjectsResp{
					StatusCode: http.StatusOK,
					JSON200: &katagenerated.ListProjectsResponse{Projects: []katagenerated.ProjectOut{
						{ID: 7, UID: projectUID, Name: "Project A", Metadata: map[string]any{"role": "inbox"}, Revision: 3, CreatedAt: createdAt, Stats: &katagenerated.ProjectStatsOut{Open: 1}},
						{ID: 8, UID: "empty-project", Name: "Empty", Metadata: map[string]any{"area": "later"}, Revision: 1, CreatedAt: createdAt, Stats: &katagenerated.ProjectStatsOut{Open: 0}},
					}},
				}, nil
			},
			readyGlobal: func(_ context.Context, options *katagenerated.ReadyIssuesGlobalRequestOptions) (*katagenerated.ReadyIssuesGlobalResp, error) {
				require.NotNil(options)
				return &katagenerated.ReadyIssuesGlobalResp{
					StatusCode: http.StatusOK,
					JSON200: &katagenerated.ReadyIssuesGlobalResponse{Issues: []katagenerated.ReadyGlobalIssueOut{{
						ID: 11, UID: "issue-a", ProjectID: 7, ProjectUID: &projectUID, ProjectName: "Project A",
						ShortID: "abc1", QualifiedID: "Project A#abc1", Title: "Ship it", Body: "body", Status: "open",
						Metadata: map[string]any{"scheduled_on": "2026-07-20"}, Revision: 4, Owner: &owner, Author: "marius",
						Priority: &priority, Labels: []string{"backend"}, Parent: newKataTestLinkPeer("parent-a", "par1"),
						Blocks:      []katagenerated.LinkPeer{*newKataTestLinkPeer("blocked-a", "blk1")},
						BlockedBy:   []katagenerated.LinkPeer{*newKataTestLinkPeer("blocker-a", "blo1")},
						Related:     []katagenerated.LinkPeer{*newKataTestLinkPeer("related-a", "rel1")},
						ChildCounts: &katagenerated.ChildCounts{Open: 1, Total: 2}, CreatedAt: createdAt, UpdatedAt: createdAt,
					}}},
				}, nil
			},
		},
		now: func() time.Time { return createdAt.Add(time.Minute) },
	}

	snapshot, err := loader.Load(t.Context(), kataAuthorityRequest{Scope: "global", Authority: "ready"})
	require.NoError(err)
	require.Equal(createdAt.Add(time.Minute), snapshot.FetchedAt)
	require.Equal([]string{"issue-a"}, snapshot.MemberIssueUIDs)
	require.Len(snapshot.Projects, 2, "empty projects remain in the atomic catalog")
	require.Equal(int64(1), snapshot.Projects[0].OpenCount)
	require.Equal(map[string]any{"role": "inbox"}, snapshot.Projects[0].Metadata)
	require.Equal(int64(0), snapshot.Projects[1].OpenCount)
	require.Len(snapshot.Issues, 1)
	issue := snapshot.Issues[0]
	require.Equal("issue-a", issue.UID)
	require.Equal("Project A", issue.ProjectName)
	require.Equal(&kataLinkPeer{UID: "parent-a", ShortID: "par1"}, issue.Parent)
	require.Equal([]kataLinkPeer{{UID: "blocked-a", ShortID: "blk1"}}, issue.Blocks)
	require.Equal([]kataLinkPeer{{UID: "blocker-a", ShortID: "blo1"}}, issue.BlockedBy)
	require.Equal([]kataLinkPeer{{UID: "related-a", ShortID: "rel1"}}, issue.Related)
	require.Equal(&kataChildCounts{Open: 1, Total: 2}, issue.ChildCounts)
}

func TestKataSnapshotLoaderResolvesProjectReadyByUID(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	projectUID := "project-a"
	createdAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	loader := kataSnapshotLoader{client: &fakeKataSnapshotAPIClient{
		listProjects: func(context.Context, *katagenerated.ListProjectsRequestOptions) (*katagenerated.ListProjectsResp, error) {
			return &katagenerated.ListProjectsResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ListProjectsResponse{Projects: []katagenerated.ProjectOut{{
				ID: 7, UID: projectUID, Name: "Project A", Metadata: map[string]any{}, CreatedAt: createdAt, Stats: &katagenerated.ProjectStatsOut{},
			}}}}, nil
		},
		readyProject: func(_ context.Context, options *katagenerated.ReadyIssuesRequestOptions) (*katagenerated.ReadyIssuesResp, error) {
			require.NotNil(options)
			require.Equal(int64(7), options.PathParams.ProjectID)
			return &katagenerated.ReadyIssuesResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ReadyIssuesResponse{Issues: []katagenerated.IssueOut{{
				ID: 11, UID: "issue-a", ProjectID: 7, ShortID: "abc1", QualifiedID: "Project A#abc1", Title: "Ship it", Body: "", Status: "open", Metadata: map[string]any{}, Author: "marius", CreatedAt: createdAt, UpdatedAt: createdAt,
			}}}}, nil
		},
	}}

	snapshot, err := loader.Load(t.Context(), kataAuthorityRequest{Scope: "project", ProjectUID: projectUID, Authority: "ready"})
	require.NoError(err)
	require.Equal([]string{"issue-a"}, snapshot.MemberIssueUIDs)
	require.Equal(projectUID, snapshot.Issues[0].ProjectUID)
	require.Equal("Project A", snapshot.Issues[0].ProjectName)
}

func TestKataSnapshotLoaderLoadsListAuthorities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		authority  string
		wantStatus *katagenerated.ListAllIssuesQueryStatus
	}{
		{name: "open", authority: "open", wantStatus: new(katagenerated.ListAllIssuesQueryStatusOpen)},
		{name: "closed", authority: "closed", wantStatus: new(katagenerated.ListAllIssuesQueryStatusClosed)},
		{name: "all", authority: "all"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			require := require.New(t)
			projectUID := "project-a"
			createdAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
			issueStatus := test.authority
			if issueStatus == "all" {
				issueStatus = "open"
			}
			loader := kataSnapshotLoader{client: &fakeKataSnapshotAPIClient{
				listProjects: func(context.Context, *katagenerated.ListProjectsRequestOptions) (*katagenerated.ListProjectsResp, error) {
					return &katagenerated.ListProjectsResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ListProjectsResponse{Projects: []katagenerated.ProjectOut{{ID: 7, UID: projectUID, Name: "Project A", Metadata: map[string]any{}, CreatedAt: createdAt, Stats: &katagenerated.ProjectStatsOut{}}}}}, nil
				},
				listIssues: func(_ context.Context, options *katagenerated.ListAllIssuesRequestOptions) (*katagenerated.ListAllIssuesResp, error) {
					require.NotNil(options)
					require.Equal(new(int64(7)), options.Query.ProjectID)
					require.Equal(test.wantStatus, options.Query.Status)
					return &katagenerated.ListAllIssuesResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ListAllIssuesResponse{Issues: []katagenerated.IssueOut{{
						ID: 11, UID: "issue-a", ProjectID: 7, ProjectUID: &projectUID, ShortID: "abc1", QualifiedID: "Project A#abc1", Title: "Ship it", Body: "", Status: issueStatus, Metadata: map[string]any{}, Author: "marius", CreatedAt: createdAt, UpdatedAt: createdAt,
					}}}}, nil
				},
			}}

			snapshot, err := loader.Load(t.Context(), kataAuthorityRequest{Scope: "project", ProjectUID: projectUID, Authority: test.authority})
			require.NoError(err)
			require.Equal([]string{"issue-a"}, snapshot.MemberIssueUIDs)
		})
	}
}

func TestKataSnapshotLoaderMapsGeneratedFailuresToUpstreamProblem(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	loader := kataSnapshotLoader{client: &fakeKataSnapshotAPIClient{
		listProjects: func(context.Context, *katagenerated.ListProjectsRequestOptions) (*katagenerated.ListProjectsResp, error) {
			return nil, errors.New("daemon unavailable")
		},
	}}

	_, err := loader.Load(t.Context(), kataAuthorityRequest{Scope: "global", Authority: "open"})
	require.Error(err)
	problem, ok := err.(*ProblemError)
	require.True(ok, "want *ProblemError, got %T", err)
	require.Equal(http.StatusBadGateway, problem.Status)
	require.Equal(CodeUpstreamError, problem.Code)
}

func TestKataSnapshotLoaderRejectsNonOKGeneratedResponse(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	loader := kataSnapshotLoader{client: &fakeKataSnapshotAPIClient{
		listProjects: func(context.Context, *katagenerated.ListProjectsRequestOptions) (*katagenerated.ListProjectsResp, error) {
			return &katagenerated.ListProjectsResp{
				StatusCode: http.StatusInternalServerError,
				JSON200:    &katagenerated.ListProjectsResponse{},
			}, nil
		},
		listIssues: func(context.Context, *katagenerated.ListAllIssuesRequestOptions) (*katagenerated.ListAllIssuesResp, error) {
			return &katagenerated.ListAllIssuesResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ListAllIssuesResponse{}}, nil
		},
	}}

	_, err := loader.Load(t.Context(), kataAuthorityRequest{Scope: "global", Authority: "open"})
	require.Error(err)
	problem, ok := err.(*ProblemError)
	require.True(ok, "want *ProblemError, got %T", err)
	require.Equal(http.StatusBadGateway, problem.Status)
}

func TestKataSnapshotLoaderRejectsIssueOutsideProjectCatalog(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	createdAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	loader := kataSnapshotLoader{client: &fakeKataSnapshotAPIClient{
		listProjects: func(context.Context, *katagenerated.ListProjectsRequestOptions) (*katagenerated.ListProjectsResp, error) {
			return &katagenerated.ListProjectsResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ListProjectsResponse{}}, nil
		},
		listIssues: func(context.Context, *katagenerated.ListAllIssuesRequestOptions) (*katagenerated.ListAllIssuesResp, error) {
			return &katagenerated.ListAllIssuesResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ListAllIssuesResponse{Issues: []katagenerated.IssueOut{{
				ID: 11, UID: "issue-a", ProjectID: 99, ShortID: "abc1", QualifiedID: "Missing#abc1", Title: "Ship it", Body: "", Status: "open", Metadata: map[string]any{}, Author: "marius", CreatedAt: createdAt, UpdatedAt: createdAt,
			}}}}, nil
		},
	}}

	_, err := loader.Load(t.Context(), kataAuthorityRequest{Scope: "global", Authority: "open"})
	require.Error(err)
	problem, ok := err.(*ProblemError)
	require.True(ok, "want *ProblemError, got %T", err)
	require.Equal(http.StatusBadGateway, problem.Status)
}

func TestKataSnapshotLoaderValidatesGeneratedBodies(t *testing.T) {
	t.Parallel()

	loader := kataSnapshotLoader{client: &fakeKataSnapshotAPIClient{
		listProjects: func(context.Context, *katagenerated.ListProjectsRequestOptions) (*katagenerated.ListProjectsResp, error) {
			return &katagenerated.ListProjectsResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ListProjectsResponse{Projects: []katagenerated.ProjectOut{{
				ID: 7, UID: "", Name: "", Metadata: map[string]any{}, Stats: &katagenerated.ProjectStatsOut{},
			}}}}, nil
		},
		listIssues: func(context.Context, *katagenerated.ListAllIssuesRequestOptions) (*katagenerated.ListAllIssuesResp, error) {
			return &katagenerated.ListAllIssuesResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ListAllIssuesResponse{}}, nil
		},
	}}

	_, err := loader.Load(t.Context(), kataAuthorityRequest{Scope: "global", Authority: "open"})
	requireKataSnapshotUpstreamProblem(t, err)
}

func TestKataSnapshotLoaderRejectsInconsistentProjectCatalog(t *testing.T) {
	t.Parallel()
	createdAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	deletedAt := createdAt.Add(time.Hour)

	tests := []struct {
		name     string
		projects []katagenerated.ProjectOut
	}{
		{name: "missing stats", projects: []katagenerated.ProjectOut{{ID: 7, UID: "project-a", Name: "A", Metadata: map[string]any{}, CreatedAt: createdAt}}},
		{name: "duplicate ID", projects: []katagenerated.ProjectOut{
			{ID: 7, UID: "project-a", Name: "A", Metadata: map[string]any{}, CreatedAt: createdAt, Stats: &katagenerated.ProjectStatsOut{}},
			{ID: 7, UID: "project-b", Name: "B", Metadata: map[string]any{}, CreatedAt: createdAt, Stats: &katagenerated.ProjectStatsOut{}},
		}},
		{name: "duplicate UID", projects: []katagenerated.ProjectOut{
			{ID: 7, UID: "project-a", Name: "A", Metadata: map[string]any{}, CreatedAt: createdAt, Stats: &katagenerated.ProjectStatsOut{}},
			{ID: 8, UID: "project-a", Name: "B", Metadata: map[string]any{}, CreatedAt: createdAt, Stats: &katagenerated.ProjectStatsOut{}},
		}},
		{name: "deleted active project", projects: []katagenerated.ProjectOut{{
			ID: 7, UID: "project-a", Name: "A", Metadata: map[string]any{}, CreatedAt: createdAt, DeletedAt: &deletedAt, Stats: &katagenerated.ProjectStatsOut{},
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			loader := kataSnapshotLoader{client: &fakeKataSnapshotAPIClient{
				listProjects: func(context.Context, *katagenerated.ListProjectsRequestOptions) (*katagenerated.ListProjectsResp, error) {
					return &katagenerated.ListProjectsResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ListProjectsResponse{Projects: test.projects}}, nil
				},
				listIssues: func(context.Context, *katagenerated.ListAllIssuesRequestOptions) (*katagenerated.ListAllIssuesResp, error) {
					return &katagenerated.ListAllIssuesResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ListAllIssuesResponse{}}, nil
				},
			}}

			_, err := loader.Load(t.Context(), kataAuthorityRequest{Scope: "global", Authority: "open"})
			requireKataSnapshotUpstreamProblem(t, err)
		})
	}
}

func TestKataSnapshotLoaderRejectsInconsistentIssueProjectIdentity(t *testing.T) {
	t.Parallel()
	createdAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	projectUID := "project-a"
	otherProjectUID := "project-b"
	projects := []katagenerated.ProjectOut{
		{ID: 7, UID: projectUID, Name: "A", Metadata: map[string]any{}, CreatedAt: createdAt, Stats: &katagenerated.ProjectStatsOut{}},
		{ID: 8, UID: otherProjectUID, Name: "B", Metadata: map[string]any{}, CreatedAt: createdAt, Stats: &katagenerated.ProjectStatsOut{}},
	}
	validIssue := katagenerated.IssueOut{
		ID: 11, UID: "issue-a", ProjectID: 7, ProjectUID: &projectUID, ShortID: "abc1", QualifiedID: "A#abc1", Title: "Ship it", Body: "body", Status: "open", Metadata: map[string]any{}, Author: "marius", CreatedAt: createdAt, UpdatedAt: createdAt,
	}

	t.Run("catalog UID mismatch", func(t *testing.T) {
		t.Parallel()
		issue := validIssue
		issue.ProjectUID = &otherProjectUID
		loader := kataSnapshotLoader{client: fakeKataListLoader(projects, []katagenerated.IssueOut{issue})}
		_, err := loader.Load(t.Context(), kataAuthorityRequest{Scope: "global", Authority: "open"})
		requireKataSnapshotUpstreamProblem(t, err)
	})

	t.Run("project response contains another project", func(t *testing.T) {
		t.Parallel()
		issue := validIssue
		issue.ProjectID = 8
		issue.ProjectUID = &otherProjectUID
		loader := kataSnapshotLoader{client: fakeKataListLoader(projects, []katagenerated.IssueOut{issue})}
		_, err := loader.Load(t.Context(), kataAuthorityRequest{Scope: "project", ProjectUID: projectUID, Authority: "open"})
		requireKataSnapshotUpstreamProblem(t, err)
	})

	t.Run("global ready project name mismatch", func(t *testing.T) {
		t.Parallel()
		loader := kataSnapshotLoader{client: &fakeKataSnapshotAPIClient{
			listProjects: func(context.Context, *katagenerated.ListProjectsRequestOptions) (*katagenerated.ListProjectsResp, error) {
				return &katagenerated.ListProjectsResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ListProjectsResponse{Projects: projects}}, nil
			},
			readyGlobal: func(context.Context, *katagenerated.ReadyIssuesGlobalRequestOptions) (*katagenerated.ReadyIssuesGlobalResp, error) {
				return &katagenerated.ReadyIssuesGlobalResp{
					StatusCode: http.StatusOK,
					JSON200: &katagenerated.ReadyIssuesGlobalResponse{Issues: []katagenerated.ReadyGlobalIssueOut{{
						ID: 11, UID: "issue-a", ProjectID: 7, ProjectUID: &projectUID, ProjectName: "Wrong", ShortID: "abc1", QualifiedID: "A#abc1", Title: "Ship it", Body: "body", Status: "open", Metadata: map[string]any{}, Author: "marius", CreatedAt: createdAt, UpdatedAt: createdAt,
					}}},
				}, nil
			},
		}}
		_, err := loader.Load(t.Context(), kataAuthorityRequest{Scope: "global", Authority: "ready"})
		requireKataSnapshotUpstreamProblem(t, err)
	})

	t.Run("duplicate numeric issue ID", func(t *testing.T) {
		t.Parallel()
		second := validIssue
		second.UID = "issue-b"
		second.ShortID = "abc2"
		second.QualifiedID = "A#abc2"
		loader := kataSnapshotLoader{client: fakeKataListLoader(projects, []katagenerated.IssueOut{validIssue, second})}
		_, err := loader.Load(t.Context(), kataAuthorityRequest{Scope: "global", Authority: "open"})
		requireKataSnapshotUpstreamProblem(t, err)
	})
}

func TestKataSnapshotLoaderRejectsAuthorityStatusMismatch(t *testing.T) {
	t.Parallel()
	createdAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	projectUID := "project-a"
	projects := []katagenerated.ProjectOut{{
		ID: 7, UID: projectUID, Name: "A", Metadata: map[string]any{}, CreatedAt: createdAt, Stats: &katagenerated.ProjectStatsOut{},
	}}
	issue := katagenerated.IssueOut{
		ID: 11, UID: "issue-a", ProjectID: 7, ProjectUID: &projectUID, ShortID: "abc1", QualifiedID: "A#abc1", Title: "Ship it", Body: "body", Metadata: map[string]any{}, Author: "marius", CreatedAt: createdAt, UpdatedAt: createdAt,
	}

	tests := []struct {
		name      string
		authority string
		status    string
		ready     bool
		deleted   bool
	}{
		{name: "open contains closed", authority: "open", status: "closed"},
		{name: "closed contains open", authority: "closed", status: "open"},
		{name: "ready contains closed", authority: "ready", status: "closed", ready: true},
		{name: "open contains deleted", authority: "open", status: "open", deleted: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			row := issue
			row.Status = test.status
			if test.deleted {
				deletedAt := createdAt.Add(time.Hour)
				row.DeletedAt = &deletedAt
			}
			client := fakeKataListLoader(projects, []katagenerated.IssueOut{row})
			if test.ready {
				client.listIssues = nil
				client.readyGlobal = func(context.Context, *katagenerated.ReadyIssuesGlobalRequestOptions) (*katagenerated.ReadyIssuesGlobalResp, error) {
					readyRow := katagenerated.ReadyGlobalIssueOut{
						ID: row.ID, UID: row.UID, ProjectID: row.ProjectID, ProjectUID: row.ProjectUID, ProjectName: "A",
						ShortID: row.ShortID, QualifiedID: row.QualifiedID, Title: row.Title, Body: row.Body, Status: row.Status,
						Metadata: row.Metadata, Author: row.Author, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
					}
					return &katagenerated.ReadyIssuesGlobalResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ReadyIssuesGlobalResponse{Issues: []katagenerated.ReadyGlobalIssueOut{readyRow}}}, nil
				}
			}
			loader := kataSnapshotLoader{client: client}
			_, err := loader.Load(t.Context(), kataAuthorityRequest{Scope: "global", Authority: test.authority})
			requireKataSnapshotUpstreamProblem(t, err)
		})
	}
}

func fakeKataListLoader(projects []katagenerated.ProjectOut, issues []katagenerated.IssueOut) *fakeKataSnapshotAPIClient {
	return &fakeKataSnapshotAPIClient{
		listProjects: func(context.Context, *katagenerated.ListProjectsRequestOptions) (*katagenerated.ListProjectsResp, error) {
			return &katagenerated.ListProjectsResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ListProjectsResponse{Projects: projects}}, nil
		},
		listIssues: func(context.Context, *katagenerated.ListAllIssuesRequestOptions) (*katagenerated.ListAllIssuesResp, error) {
			return &katagenerated.ListAllIssuesResp{StatusCode: http.StatusOK, JSON200: &katagenerated.ListAllIssuesResponse{Issues: issues}}, nil
		},
	}
}

func requireKataSnapshotUpstreamProblem(t *testing.T, err error) {
	t.Helper()
	require := require.New(t)
	require.Error(err)
	problem, ok := err.(*ProblemError)
	require.True(ok, "want *ProblemError, got %T", err)
	require.Equal(http.StatusBadGateway, problem.Status)
}

func newKataTestLinkPeer(uid, shortID string) *katagenerated.LinkPeer {
	return &katagenerated.LinkPeer{
		UID:         uid,
		ShortID:     shortID,
		Project:     "Project A",
		QualifiedID: "Project A#" + shortID,
		Status:      "open",
	}
}

type fakeKataSnapshotAPIClient struct {
	listProjects func(context.Context, *katagenerated.ListProjectsRequestOptions) (*katagenerated.ListProjectsResp, error)
	listIssues   func(context.Context, *katagenerated.ListAllIssuesRequestOptions) (*katagenerated.ListAllIssuesResp, error)
	readyProject func(context.Context, *katagenerated.ReadyIssuesRequestOptions) (*katagenerated.ReadyIssuesResp, error)
	readyGlobal  func(context.Context, *katagenerated.ReadyIssuesGlobalRequestOptions) (*katagenerated.ReadyIssuesGlobalResp, error)
}

func (f *fakeKataSnapshotAPIClient) InstanceWithResponse(context.Context, ...runtime.RequestEditorFn) (*katagenerated.InstanceResp, error) {
	panic("unexpected InstanceWithResponse call")
}

func (f *fakeKataSnapshotAPIClient) ListAllIssuesWithResponse(ctx context.Context, options *katagenerated.ListAllIssuesRequestOptions, _ ...runtime.RequestEditorFn) (*katagenerated.ListAllIssuesResp, error) {
	return f.listIssues(ctx, options)
}

func (f *fakeKataSnapshotAPIClient) ListProjectsWithResponse(ctx context.Context, options *katagenerated.ListProjectsRequestOptions, _ ...runtime.RequestEditorFn) (*katagenerated.ListProjectsResp, error) {
	return f.listProjects(ctx, options)
}

func (f *fakeKataSnapshotAPIClient) ReadyIssuesWithResponse(ctx context.Context, options *katagenerated.ReadyIssuesRequestOptions, _ ...runtime.RequestEditorFn) (*katagenerated.ReadyIssuesResp, error) {
	return f.readyProject(ctx, options)
}

func (f *fakeKataSnapshotAPIClient) ReadyIssuesGlobalWithResponse(ctx context.Context, options *katagenerated.ReadyIssuesGlobalRequestOptions, _ ...runtime.RequestEditorFn) (*katagenerated.ReadyIssuesGlobalResp, error) {
	return f.readyGlobal(ctx, options)
}

func (f *fakeKataSnapshotAPIClient) StreamEventsRaw(context.Context, *katagenerated.StreamEventsRequestOptions, ...runtime.RequestEditorFn) (*http.Response, error) {
	panic("unexpected StreamEventsRaw call")
}

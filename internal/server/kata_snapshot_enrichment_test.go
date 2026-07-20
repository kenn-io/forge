package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	katagenerated "go.kenn.io/kata/pkg/client/generated"

	"go.kenn.io/middleman/internal/db"
)

func TestKataSnapshotEnricherSkipsDirectNonmemberSelection(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	workspaceCalls := 0
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{
		client: &fakeKataSnapshotAPIClient{},
		resolveWorkspaceTarget: func(context.Context, db.WorkspaceKataMetadata) (kataWorkspaceTargetResponse, error) {
			workspaceCalls++
			return kataWorkspaceTargetResponse{}, nil
		},
	})
	authority := testKataCoordinatedAuthority()
	authority.Snapshot.Issues = append(authority.Snapshot.Issues, kataTaskSummary{
		UID: "issue-outside", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A",
	})

	result, err := enricher.Enrich(t.Context(), authority, kataSnapshotEnrichmentRequest{
		SelectedIssueUID: "issue-outside",
	})

	require.NoError(err)
	assert.Empty(result.SelectedIssueUID)
	assert.Nil(result.SelectedDetail)
	assert.Nil(result.Graph)
	assert.Empty(result.Errors)
	assert.Zero(workspaceCalls)
}

func TestKataSnapshotEnricherGraphNodeAuthorizesSelectionWithoutRerooting(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	client := &fakeKataSnapshotAPIClient{}
	client.reachableGraph = func(_ context.Context, options *katagenerated.ReachableIssueGraphRequestOptions) (*katagenerated.ReachableIssueGraphResp, error) {
		require.NotNil(options.PathParams)
		assert.Equal(int64(7), options.PathParams.ProjectID)
		assert.Equal("issue-source", options.PathParams.Ref)
		require.NotNil(options.Query)
		require.NotNil(options.Query.Depth)
		assert.Equal("full", *options.Query.Depth)
		require.NotNil(options.Query.HideDone)
		assert.False(*options.Query.HideDone)
		return testKataGraphResponse("issue-source", "issue-linked"), nil
	}
	client.showIssue = func(_ context.Context, options *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
		require.NotNil(options.PathParams)
		assert.Equal("issue-linked", options.PathParams.UID)
		require.NotNil(options.Query)
		require.NotNil(options.Query.IncludeDeleted)
		assert.False(*options.Query.IncludeDeleted)
		return testKataShowIssueResponse("issue-linked"), nil
	}

	var workspaceMetadata db.WorkspaceKataMetadata
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{
		client: client,
		resolveWorkspaceTarget: func(_ context.Context, metadata db.WorkspaceKataMetadata) (kataWorkspaceTargetResponse, error) {
			workspaceMetadata = metadata
			return kataWorkspaceTargetResponse{Available: true, ItemKey: "kata-item"}, nil
		},
		now: func() time.Time { return time.Date(2026, 7, 20, 16, 0, 0, 0, time.UTC) },
	})

	result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{
		SelectedIssueUID: "issue-linked",
		GraphSourceUID:   "issue-source",
	})

	require.NoError(err)
	assert.Equal("issue-linked", result.SelectedIssueUID)
	require.NotNil(result.Graph)
	assert.Equal("issue-source", result.Graph.SourceUID)
	require.NotNil(result.GraphFetchedAt)
	assert.Equal(time.Date(2026, 7, 20, 16, 0, 0, 0, time.UTC), *result.GraphFetchedAt)
	require.NotNil(result.SelectedDetail)
	assert.Equal("kata-item", result.SelectedDetail.WorkspaceTarget.ItemKey)
	assert.Equal("detail-etag", result.SelectedDetail.ETag)
	assert.Equal(db.WorkspaceKataMetadata{
		DaemonID:    "daemon-a",
		ProjectUID:  "project-a",
		ProjectName: "Project A",
		IssueUID:    "issue-linked",
		ShortID:     "linked",
		QualifiedID: "Project A#linked",
		Title:       "Linked task",
	}, workspaceMetadata)
	assert.Empty(result.Errors)
}

func TestKataSnapshotEnricherSkipsNonmemberGraphSource(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	client := &fakeKataSnapshotAPIClient{}
	client.showIssue = func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
		return testKataShowIssueResponse("issue-member"), nil
	}
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client})
	authority := testKataCoordinatedAuthority()
	authority.Snapshot.Issues = append(authority.Snapshot.Issues, kataTaskSummary{
		UID: "issue-outside", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A",
	})

	result, err := enricher.Enrich(t.Context(), authority, kataSnapshotEnrichmentRequest{
		SelectedIssueUID: "issue-member",
		GraphSourceUID:   "issue-outside",
	})

	require.NoError(err)
	require.NotNil(result.SelectedDetail)
	assert.Nil(result.Graph)
	assert.Empty(result.Errors)
}

func TestKataSnapshotEnricherKeepsGraphSourceIndependentFromSelectedIssue(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	client := &fakeKataSnapshotAPIClient{}
	client.reachableGraph = func(_ context.Context, options *katagenerated.ReachableIssueGraphRequestOptions) (*katagenerated.ReachableIssueGraphResp, error) {
		assert.Equal("issue-source", options.PathParams.Ref)
		return testKataGraphResponse("issue-source", "issue-linked"), nil
	}
	client.showIssue = func(_ context.Context, options *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
		assert.Equal("issue-member", options.PathParams.UID)
		return testKataShowIssueResponse("issue-member"), nil
	}
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{
		client: client,
		resolveWorkspaceTarget: func(context.Context, db.WorkspaceKataMetadata) (kataWorkspaceTargetResponse, error) {
			return kataWorkspaceTargetResponse{Available: false}, nil
		},
	})

	result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{
		SelectedIssueUID: "issue-member",
		GraphSourceUID:   "issue-source",
	})

	require.NoError(err)
	assert.Equal("issue-member", result.SelectedIssueUID)
	require.NotNil(result.Graph)
	assert.Equal("issue-source", result.Graph.SourceUID)
}

func TestKataSnapshotEnricherKeepsFailuresLocalToTheirStage(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	client := &fakeKataSnapshotAPIClient{}
	client.reachableGraph = func(context.Context, *katagenerated.ReachableIssueGraphRequestOptions) (*katagenerated.ReachableIssueGraphResp, error) {
		return nil, errors.New("graph unavailable")
	}
	client.showIssue = func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
		return testKataShowIssueResponse("issue-member"), nil
	}
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{
		client: client,
		resolveWorkspaceTarget: func(context.Context, db.WorkspaceKataMetadata) (kataWorkspaceTargetResponse, error) {
			return kataWorkspaceTargetResponse{}, errors.New("workspace unavailable")
		},
	})

	result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{
		SelectedIssueUID: "issue-member",
		GraphSourceUID:   "issue-source",
	})

	require.NoError(err)
	assert.Equal("issue-member", result.SelectedIssueUID)
	assert.Nil(result.Graph)
	require.NotNil(result.SelectedDetail)
	assert.False(result.SelectedDetail.WorkspaceTarget.Available)
	assert.Equal(kataSnapshotEnrichmentError{Code: CodeUpstreamError, Message: "Could not load reachable graph."}, result.Errors[kataSnapshotEnrichmentStageGraph])
	assert.Equal(kataSnapshotEnrichmentError{Code: CodeInternalError, Message: "Could not resolve workspace target."}, result.Errors[kataSnapshotEnrichmentStageWorkspaceTarget])
}

func TestKataSnapshotEnricherPropagatesContextCancellation(t *testing.T) {
	t.Parallel()

	client := &fakeKataSnapshotAPIClient{}
	client.showIssue = func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
		return nil, context.Canceled
	}
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client})

	_, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{
		SelectedIssueUID: "issue-member",
	})

	require.ErrorIs(t, err, context.Canceled)
}

func TestKataSnapshotEnricherRejectsWrongReturnedIdentitiesLocally(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	client := &fakeKataSnapshotAPIClient{}
	client.reachableGraph = func(context.Context, *katagenerated.ReachableIssueGraphRequestOptions) (*katagenerated.ReachableIssueGraphResp, error) {
		return testKataGraphResponse("wrong-source", "issue-linked"), nil
	}
	client.showIssue = func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
		return testKataShowIssueResponse("wrong-issue"), nil
	}
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client})

	result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{
		SelectedIssueUID: "issue-member",
		GraphSourceUID:   "issue-source",
	})

	require.NoError(t, err)
	assert.Nil(result.Graph)
	assert.Nil(result.SelectedDetail)
	assert.Equal(kataSnapshotEnrichmentError{Code: CodeUpstreamError, Message: "Could not load reachable graph."}, result.Errors[kataSnapshotEnrichmentStageGraph])
	assert.Equal(kataSnapshotEnrichmentError{Code: CodeUpstreamError, Message: "Could not load selected task detail."}, result.Errors[kataSnapshotEnrichmentStageDetail])
}

func testKataCoordinatedAuthority() kataCoordinatedAuthority {
	return kataCoordinatedAuthority{
		DaemonID: "daemon-a",
		Snapshot: kataAuthoritySnapshot{
			Projects:        []kataProjectSummary{{ID: 7, UID: "project-a", Name: "Project A"}},
			MemberIssueUIDs: []string{"issue-source", "issue-member"},
			Issues: []kataTaskSummary{
				{ID: 1, UID: "issue-source", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A", ShortID: "source", QualifiedID: "Project A#source", Title: "Source task"},
				{ID: 2, UID: "issue-member", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A", ShortID: "member", QualifiedID: "Project A#member", Title: "Member task"},
			},
		},
	}
}

func testKataGraphResponse(sourceUID, linkedUID string) *katagenerated.ReachableIssueGraphResp {
	projectUID := "project-a"
	return &katagenerated.ReachableIssueGraphResp{
		StatusCode: http.StatusOK,
		JSON200: &katagenerated.ReachableGraphResponseBody{
			SourceUID: sourceUID,
			Depth:     "full",
			HideDone:  false,
			Nodes: []katagenerated.ReachableGraphNode{
				{ID: 1, UID: sourceUID, ProjectID: 7, ProjectUID: &projectUID, ShortID: "source", QualifiedID: "Project A#source", Title: "Source task"},
				{ID: 3, UID: linkedUID, ProjectID: 7, ProjectUID: &projectUID, ShortID: "linked", QualifiedID: "Project A#linked", Title: "Linked task"},
			},
		},
	}
}

func testKataShowIssueResponse(uid string) *katagenerated.ShowIssueByUIDResp {
	projectUID := "project-a"
	issueID := int64(3)
	if uid == "issue-member" {
		issueID = 2
	}
	header := make(http.Header)
	header.Set("ETag", "detail-etag")
	return &katagenerated.ShowIssueByUIDResp{
		HTTPResponse: &http.Response{Header: header},
		StatusCode:   http.StatusOK,
		JSON200: &katagenerated.ShowIssueResponseBody{Issue: katagenerated.Issue{
			ID:         issueID,
			UID:        uid,
			ProjectID:  7,
			ProjectUID: &projectUID,
			ShortID:    "linked",
			Title:      "Linked task",
		}},
	}
}

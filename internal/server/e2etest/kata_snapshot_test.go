package e2etest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	katagenerated "go.kenn.io/kata/pkg/client/generated"
	"go.kenn.io/middleman/internal/apiclient"
	apigenerated "go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/db"
)

const kataSnapshotE2EDaemonID = "primary"

var kataSnapshotE2ETime = time.Date(2026, 7, 20, 16, 0, 0, 0, time.UTC)

func TestKataTaskSnapshotProjectReadyAuthorityE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Ready task")

	scope := apigenerated.Project
	authority := apigenerated.Ready
	projectUID := "project-a"
	selectedIssueUID := "issue-member"
	response, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
		Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
		SelectedIssueUid: &selectedIssueUID, XMiddlemanKataDaemon: new(kataSnapshotE2EDaemonID),
	})

	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode(), string(response.Body))
	require.NotNil(response.JSON200)
	snapshot := response.JSON200
	assert.Equal(kataSnapshotE2EDaemonID, snapshot.DaemonId)
	assert.Equal(apigenerated.KataAuthorityRequest{
		Scope: "project", ProjectUid: &projectUID, Authority: "ready",
	}, snapshot.Intent)
	require.NotNil(snapshot.Projects)
	require.Len(*snapshot.Projects, 1)
	assert.Equal("Project A", (*snapshot.Projects)[0].Name)
	require.NotNil(snapshot.MemberIssueUids)
	assert.Equal([]string{selectedIssueUID}, *snapshot.MemberIssueUids)
	require.NotNil(snapshot.Issues)
	require.Len(*snapshot.Issues, 1)
	assert.Equal("Ready task", (*snapshot.Issues)[0].Title)
	require.NotNil(snapshot.Enrichment.SelectedIssueUid)
	assert.Equal(selectedIssueUID, *snapshot.Enrichment.SelectedIssueUid)
	require.NotNil(snapshot.Enrichment.SelectedDetail)
	workspaceTarget := snapshot.Enrichment.SelectedDetail.WorkspaceTarget
	assert.True(workspaceTarget.Available)
	require.NotNil(workspaceTarget.Repo)
	assert.Equal("github.com", workspaceTarget.Repo.PlatformHost)
	assert.Equal("acme", workspaceTarget.Repo.Owner)
	assert.Equal("widget", workspaceTarget.Repo.Name)
	require.NotNil(workspaceTarget.ExistingWorkspace)
	assert.Equal("ws-kata-e2e", workspaceTarget.ExistingWorkspace.Id)
	assert.Equal("ready", workspaceTarget.ExistingWorkspace.Status)
	assert.Positive(snapshot.Generation)
	assert.Positive(snapshot.EventCursor)
}

func TestKataTaskSnapshotLoadsCompleteRetainedProjectHistoryE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Ready task")
	selectedIssueUID := "issue-member"
	events := make([]katagenerated.EventEnvelope, 0, 128)
	for eventID := int64(1); eventID <= 126; eventID++ {
		events = append(events, katagenerated.EventEnvelope{
			Actor:             "acceptance",
			ContentHash:       fmt.Sprintf("content-%d", eventID),
			CreatedAt:         kataSnapshotE2ETime.Add(time.Duration(eventID) * time.Second),
			EventID:           eventID,
			EventUID:          fmt.Sprintf("event-%d", eventID),
			IssueUID:          &selectedIssueUID,
			OriginInstanceUID: "kata-e2e",
			ProjectID:         7,
			ProjectName:       "Project A",
			ProjectUID:        "project-a",
			Type:              "issue.updated",
		})
	}
	events = append(events,
		katagenerated.EventEnvelope{
			Actor: "acceptance", ContentHash: "content-127", CreatedAt: kataSnapshotE2ETime.Add(127 * time.Second),
			EventID: 127, EventUID: "event-127", OriginInstanceUID: "kata-e2e",
			ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "project.updated",
		},
		katagenerated.EventEnvelope{
			Actor: "acceptance", ContentHash: "content-128", CreatedAt: kataSnapshotE2ETime.Add(128 * time.Second),
			EventID: 128, EventUID: "event-128", IssueUID: &selectedIssueUID, OriginInstanceUID: "kata-e2e",
			ProjectID: 8, ProjectName: "Project B", ProjectUID: "project-b", Type: "issue.updated",
		},
	)
	fixture.daemon.mu.Lock()
	fixture.daemon.projectEvents = events
	fixture.daemon.projectEventPageSize = 100
	fixture.daemon.mu.Unlock()

	scope := apigenerated.Project
	authority := apigenerated.Ready
	projectUID := "project-a"
	response, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
		Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
		SelectedIssueUid: &selectedIssueUID, XMiddlemanKataDaemon: new(kataSnapshotE2EDaemonID),
	})

	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode(), string(response.Body))
	require.NotNil(response.JSON200)
	require.NotNil(response.JSON200.Enrichment.SelectedHistory)
	require.Len(*response.JSON200.Enrichment.SelectedHistory, 126)
	assert.Equal(int64(1), (*response.JSON200.Enrichment.SelectedHistory)[0].EventId)
	assert.Equal(int64(126), (*response.JSON200.Enrichment.SelectedHistory)[125].EventId)
	assert.Nil(response.JSON200.Enrichment.Errors)
	fixture.daemon.mu.RLock()
	assert.Equal([]int64{0, 100, 127}, fixture.daemon.projectEventCursors)
	fixture.daemon.mu.RUnlock()
}

func TestKataTaskSnapshotReusesSelectedEnrichmentUntilMutationInvalidatesEpochE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Before mutation")
	selectedIssueUID := "issue-member"
	otherIssueUID := "issue-other"
	fixture.daemon.mu.Lock()
	fixture.daemon.graphFails = false
	fixture.daemon.projectEvents = []katagenerated.EventEnvelope{
		{
			Actor: "acceptance", ContentHash: "before-selected", CreatedAt: kataSnapshotE2ETime.Add(time.Second),
			EventID: 1, EventUID: "event-1", IssueUID: &selectedIssueUID, OriginInstanceUID: "kata-e2e",
			ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
		},
		{
			Actor: "acceptance", ContentHash: "before-other", CreatedAt: kataSnapshotE2ETime.Add(2 * time.Second),
			EventID: 2, EventUID: "event-2", IssueUID: &otherIssueUID, OriginInstanceUID: "kata-e2e",
			ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
		},
	}
	fixture.daemon.projectEventPageSize = 1
	fixture.daemon.mu.Unlock()

	scope := apigenerated.Project
	authority := apigenerated.Ready
	projectUID := "project-a"
	firstResponse, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
		Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
		SelectedIssueUid: &selectedIssueUID, GraphSourceUid: &selectedIssueUID,
		XMiddlemanKataDaemon: new(kataSnapshotE2EDaemonID),
	})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResponse.StatusCode(), string(firstResponse.Body))
	require.NotNil(firstResponse.JSON200)
	first := firstResponse.JSON200
	require.NotNil(first.Enrichment.SelectedDetail)
	require.NotNil(first.Enrichment.SelectedHistory)
	require.Len(*first.Enrichment.SelectedHistory, 1)
	assert.Equal("before-selected", (*first.Enrichment.SelectedHistory)[0].ContentHash)
	require.NotNil(first.Enrichment.Graph)
	require.NotNil(first.Enrichment.Graph.Nodes)
	require.Len(*first.Enrichment.Graph.Nodes, 2)
	assert.Equal("Before mutation", (*first.Enrichment.Graph.Nodes)[0].Title)
	assert.Nil(first.Enrichment.Errors)
	assert.Equal(int64(1), fixture.daemon.detailCalls.Load())
	assert.Equal(int64(3), fixture.daemon.projectEventCalls.Load())
	assert.Equal(int64(1), fixture.daemon.graphCalls.Load())

	warmStarted := time.Now()
	secondResponse, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
		Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
		SelectedIssueUid: &selectedIssueUID, GraphSourceUid: &selectedIssueUID,
		XMiddlemanKataDaemon: new(kataSnapshotE2EDaemonID),
	})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResponse.StatusCode(), string(secondResponse.Body))
	require.NotNil(secondResponse.JSON200)
	require.Less(time.Since(warmStarted), 5*time.Second)
	assert.Equal(first.Generation, secondResponse.JSON200.Generation)
	assert.Equal(first.InvalidationEpoch, secondResponse.JSON200.InvalidationEpoch)
	assert.Equal(int64(1), fixture.daemon.detailCalls.Load(), "warm detail must reuse the upstream result")
	assert.Equal(int64(3), fixture.daemon.projectEventCalls.Load(), "warm history must reuse the complete paginated project stream")
	assert.Equal(int64(1), fixture.daemon.graphCalls.Load(), "warm graph must reuse the upstream result")

	mutationRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		fixture.middleman.URL+"/api/v1/kata/proxy/api/v1/issues/issue-member",
		strings.NewReader(`{"title":"After mutation"}`),
	)
	require.NoError(err)
	mutationRequest.Header.Set("Content-Type", "application/json")
	mutationRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	mutationResponse, err := fixture.middleman.Client().Do(mutationRequest)
	require.NoError(err)
	require.NoError(mutationResponse.Body.Close())
	require.Equal(http.StatusNoContent, mutationResponse.StatusCode)

	thirdResponse, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
		Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
		SelectedIssueUid: &selectedIssueUID, GraphSourceUid: &selectedIssueUID,
		XMiddlemanKataDaemon: new(kataSnapshotE2EDaemonID),
	})
	require.NoError(err)
	require.Equal(http.StatusOK, thirdResponse.StatusCode(), string(thirdResponse.Body))
	require.NotNil(thirdResponse.JSON200)
	third := thirdResponse.JSON200
	assert.Greater(third.InvalidationEpoch, first.InvalidationEpoch)
	assert.Greater(third.Generation, first.Generation)
	require.NotNil(third.Issues)
	require.Len(*third.Issues, 1)
	assert.Equal("After mutation", (*third.Issues)[0].Title)
	require.NotNil(third.Enrichment.SelectedDetail)
	detailBody, ok := third.Enrichment.SelectedDetail.Detail.(map[string]any)
	require.True(ok)
	detailIssue, ok := detailBody["issue"].(map[string]any)
	require.True(ok)
	assert.Equal("After mutation", detailIssue["title"])
	require.NotNil(third.Enrichment.SelectedHistory)
	require.Len(*third.Enrichment.SelectedHistory, 2)
	assert.Equal("mutation-After mutation", (*third.Enrichment.SelectedHistory)[1].ContentHash)
	require.NotNil(third.Enrichment.Graph)
	require.NotNil(third.Enrichment.Graph.Nodes)
	require.Len(*third.Enrichment.Graph.Nodes, 2)
	assert.Equal("After mutation", (*third.Enrichment.Graph.Nodes)[0].Title)
	assert.Nil(third.Enrichment.Errors)
	assert.Equal(int64(2), fixture.daemon.detailCalls.Load())
	assert.Equal(int64(7), fixture.daemon.projectEventCalls.Load())
	assert.Equal(int64(2), fixture.daemon.graphCalls.Load())
}

func TestKataTaskSnapshotDiscardsPreResetHistoryAtRetainedBaselineE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Ready task")
	selectedIssueUID := "issue-member"
	otherIssueUID := "issue-other"
	resetAfterID := int64(41)
	fixture.daemon.mu.Lock()
	fixture.daemon.projectEventPages = []katagenerated.PollEventsBody{
		{
			Events: []katagenerated.EventEnvelope{
				{
					Actor: "acceptance", ContentHash: "content-1", CreatedAt: kataSnapshotE2ETime.Add(time.Second),
					EventID: 1, EventUID: "event-1", IssueUID: &selectedIssueUID, OriginInstanceUID: "kata-e2e",
					ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
				},
				{
					Actor: "acceptance", ContentHash: "content-2", CreatedAt: kataSnapshotE2ETime.Add(2 * time.Second),
					EventID: 2, EventUID: "event-2", IssueUID: &otherIssueUID, OriginInstanceUID: "kata-e2e",
					ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
				},
			},
			NextAfterID: 2,
		},
		{Events: []katagenerated.EventEnvelope{}, NextAfterID: resetAfterID, ResetAfterID: &resetAfterID, ResetRequired: true},
		{
			Events: []katagenerated.EventEnvelope{
				{
					Actor: "acceptance", ContentHash: "content-42", CreatedAt: kataSnapshotE2ETime.Add(42 * time.Second),
					EventID: 42, EventUID: "event-42", IssueUID: &selectedIssueUID, OriginInstanceUID: "kata-e2e",
					ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
				},
				{
					Actor: "acceptance", ContentHash: "content-43", CreatedAt: kataSnapshotE2ETime.Add(43 * time.Second),
					EventID: 43, EventUID: "event-43", IssueUID: &selectedIssueUID, OriginInstanceUID: "kata-e2e",
					ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
				},
			},
			NextAfterID: 43,
		},
		{Events: []katagenerated.EventEnvelope{}, NextAfterID: 43},
	}
	fixture.daemon.mu.Unlock()

	scope := apigenerated.Project
	authority := apigenerated.Ready
	projectUID := "project-a"
	response, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
		Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
		SelectedIssueUid: &selectedIssueUID, XMiddlemanKataDaemon: new(kataSnapshotE2EDaemonID),
	})

	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode(), string(response.Body))
	require.NotNil(response.JSON200)
	require.NotNil(response.JSON200.Enrichment.SelectedHistory)
	require.Len(*response.JSON200.Enrichment.SelectedHistory, 2)
	assert.Equal([]int64{42, 43}, []int64{
		(*response.JSON200.Enrichment.SelectedHistory)[0].EventId,
		(*response.JSON200.Enrichment.SelectedHistory)[1].EventId,
	})
	assert.Nil(response.JSON200.Enrichment.Errors)
	fixture.daemon.mu.RLock()
	assert.Equal([]int64{0, 2, 41, 43}, fixture.daemon.projectEventCursors)
	fixture.daemon.mu.RUnlock()
}

func TestKataTaskSnapshotPreservesGraphSourceWhenEnrichmentFailsE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Ready task")

	scope := apigenerated.Project
	authority := apigenerated.Ready
	projectUID := "project-a"
	graphSourceUID := "issue-member"
	response, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
		Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
		GraphSourceUid: &graphSourceUID, XMiddlemanKataDaemon: new(kataSnapshotE2EDaemonID),
	})

	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode(), string(response.Body))
	require.NotNil(response.JSON200)
	snapshot := response.JSON200
	require.NotNil(snapshot.GraphSourceUid)
	assert.Equal(graphSourceUID, *snapshot.GraphSourceUid)
	require.NotNil(snapshot.Issues)
	require.Len(*snapshot.Issues, 1)
	assert.Equal("Ready task", (*snapshot.Issues)[0].Title)
	assert.Nil(snapshot.Enrichment.Graph)
	require.NotNil(snapshot.Enrichment.Errors)
	assert.Equal(apigenerated.KataSnapshotEnrichmentError{
		Code: string(apigenerated.UpstreamError), Message: "Could not load reachable graph.",
	}, (*snapshot.Enrichment.Errors)["graph"])
}

func TestKataTaskSnapshotEventInvalidationRefreshesAuthorityE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Before event")

	first := fixture.snapshot(t, apigenerated.Project, apigenerated.Ready)
	require.Positive(first.EventCursor)
	require.Zero(first.InvalidationEpoch)
	fixture.daemon.waitForStream(t)
	fixture.daemon.setTitle("After event")
	fixture.daemon.publishEvent(t, 1)

	var refreshed *apigenerated.KataTaskSnapshotResponse
	require.Eventually(func() bool {
		candidate := fixture.snapshot(t, apigenerated.Project, apigenerated.Ready)
		if candidate.InvalidationEpoch <= first.InvalidationEpoch || candidate.Generation <= first.Generation {
			return false
		}
		if candidate.Issues == nil || len(*candidate.Issues) != 1 || (*candidate.Issues)[0].Title != "After event" {
			return false
		}
		refreshed = candidate
		return true
	}, 3*time.Second, 10*time.Millisecond)

	require.NotNil(refreshed)
	assert.Greater(refreshed.EventCursor, first.EventCursor)
	assert.GreaterOrEqual(fixture.daemon.readyCalls.Load(), int64(2))
}

func TestKataTaskReferencesFirstRequestStartsEventsAndInvalidatesCacheE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Before reference event")

	first := fixture.references(t)
	require.NotNil(first.References)
	require.Len(*first.References, 1)
	assert.Equal("Before reference event", (*first.References)[0].Title)
	assert.Zero(fixture.daemon.readyCalls.Load(), "references must not rely on a prior snapshot request")
	fixture.daemon.waitForStream(t)
	fixture.daemon.setTitle("After reference event")
	fixture.daemon.publishEvent(t, 1)

	var refreshed *apigenerated.KataTaskReferenceResponse
	require.Eventually(func() bool {
		candidate := fixture.references(t)
		if candidate.InvalidationEpoch <= first.InvalidationEpoch || candidate.Generation <= first.Generation {
			return false
		}
		if candidate.References == nil || len(*candidate.References) != 1 || (*candidate.References)[0].Title != "After reference event" {
			return false
		}
		refreshed = candidate
		return true
	}, 3*time.Second, 10*time.Millisecond)

	require.NotNil(refreshed)
	assert.GreaterOrEqual(fixture.daemon.issueListCalls.Load(), int64(2))
}

func TestKataTaskSnapshotDaemonRotationRejectsInflightAuthorityE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	releaseOld := make(chan struct{})
	oldDaemon := newKataSnapshotDaemonStub(t, "Stale target")
	oldDaemon.blockFirstProjects = releaseOld
	newDaemon := newKataSnapshotDaemonStub(t, "Rotated target")
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataSnapshotE2ECatalog(t, home, oldDaemon.server.URL)
	_, client := startKataSnapshotE2EMiddleman(t)

	type result struct {
		response *apigenerated.GetKataTaskSnapshotResponse
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		scope := apigenerated.Project
		authority := apigenerated.Ready
		projectUID := "project-a"
		response, err := client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
			Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
			XMiddlemanKataDaemon: new(kataSnapshotE2EDaemonID),
		})
		resultCh <- result{response: response, err: err}
	}()

	oldDaemon.waitForFirstProjects(t)
	writeKataSnapshotE2ECatalog(t, home, newDaemon.server.URL)
	close(releaseOld)
	var got result
	select {
	case got = <-resultCh:
	case <-time.After(3 * time.Second):
		require.FailNow("snapshot request did not complete after daemon rotation")
	}

	require.NoError(got.err)
	require.NotNil(got.response)
	require.Equal(http.StatusOK, got.response.StatusCode(), string(got.response.Body))
	require.NotNil(got.response.JSON200)
	require.NotNil(got.response.JSON200.Issues)
	require.Len(*got.response.JSON200.Issues, 1)
	assert.Equal("Rotated target", (*got.response.JSON200.Issues)[0].Title)
	assert.Positive(oldDaemon.readyCalls.Load())
	assert.Positive(newDaemon.readyCalls.Load())
	assert.Positive(got.response.JSON200.InvalidationEpoch)
}

func TestKataProxySuccessfulMutationInvalidatesSnapshotBeforeResponseE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Before mutation")

	first := fixture.snapshot(t, apigenerated.Project, apigenerated.Ready)
	require.Zero(first.InvalidationEpoch)
	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		fixture.middleman.URL+"/api/v1/kata/proxy/api/v1/issues/issue-member",
		strings.NewReader(`{"title":"After mutation"}`),
	)
	require.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err := fixture.middleman.Client().Do(req)
	require.NoError(err)
	defer response.Body.Close()
	require.Equal(http.StatusNoContent, response.StatusCode)

	refreshed := fixture.snapshot(t, apigenerated.Project, apigenerated.Ready)
	require.NotNil(refreshed.Issues)
	require.Len(*refreshed.Issues, 1)
	assert.Equal("After mutation", (*refreshed.Issues)[0].Title)
	assert.Greater(refreshed.InvalidationEpoch, first.InvalidationEpoch)
	assert.Greater(refreshed.Generation, first.Generation)
}

type kataSnapshotE2EFixture struct {
	daemon    *kataSnapshotDaemonStub
	middleman *httptest.Server
	client    *apiclient.Client
}

func newKataSnapshotE2EFixture(t *testing.T, title string) *kataSnapshotE2EFixture {
	t.Helper()
	daemon := newKataSnapshotDaemonStub(t, title)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataSnapshotE2ECatalog(t, home, daemon.server.URL)
	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[[kata_projects]]
daemon_id = "primary"
project_uid = "project-a"
provider = "github"
platform_host = "github.com"
repo_path = "acme/widget"
`, &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	metadata := db.WorkspaceKataMetadata{
		DaemonID: "primary", ProjectUID: "project-a", ProjectName: "Project A",
		IssueUID: "issue-member", ShortID: "task-1", QualifiedID: "Project A#task-1", Title: title,
	}
	require.NoError(t, database.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "ws-kata-e2e", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypeKataTask,
		ItemKey: db.KataWorkspaceItemKey(metadata), GitHeadRef: "middleman/kata/task-1",
		WorkspaceBranch: "middleman/kata/task-1", WorktreePath: "/tmp/ws-kata-e2e",
		TmuxSession: "middleman-ws-kata-e2e", Status: "ready", KataMetadata: &metadata,
	}))
	middleman := httptest.NewServer(srv)
	t.Cleanup(middleman.Close)
	client, err := apiclient.NewWithHTTPClient(middleman.URL, middleman.Client())
	require.NoError(t, err)
	return &kataSnapshotE2EFixture{daemon: daemon, middleman: middleman, client: client}
}

func (f *kataSnapshotE2EFixture) snapshot(
	t *testing.T,
	scope apigenerated.GetKataTaskSnapshotParamsScope,
	authority apigenerated.GetKataTaskSnapshotParamsAuthority,
) *apigenerated.KataTaskSnapshotResponse {
	t.Helper()
	projectUID := "project-a"
	response, err := f.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
		Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
		XMiddlemanKataDaemon: new(kataSnapshotE2EDaemonID),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode(), string(response.Body))
	require.NotNil(t, response.JSON200)
	return response.JSON200
}

func (f *kataSnapshotE2EFixture) references(t *testing.T) *apigenerated.KataTaskReferenceResponse {
	t.Helper()
	response, err := f.client.HTTP.SearchKataTaskReferencesWithResponse(t.Context(), &apigenerated.SearchKataTaskReferencesParams{
		XMiddlemanKataDaemon: new(kataSnapshotE2EDaemonID),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode(), string(response.Body))
	require.NotNil(t, response.JSON200)
	return response.JSON200
}

func startKataSnapshotE2EMiddleman(t *testing.T) (*httptest.Server, *apiclient.Client) {
	t.Helper()
	srv, _ := setupTestServer(t)
	middleman := httptest.NewServer(srv)
	t.Cleanup(middleman.Close)
	client, err := apiclient.NewWithHTTPClient(middleman.URL, middleman.Client())
	require.NoError(t, err)
	return middleman, client
}

func writeKataSnapshotE2ECatalog(t *testing.T, home, daemonURL string) {
	t.Helper()
	body := "active_daemon = \"" + kataSnapshotE2EDaemonID + "\"\n\n" +
		"[[daemon]]\n" +
		"name = \"" + kataSnapshotE2EDaemonID + "\"\n" +
		"url = \"" + daemonURL + "\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600))
}

type kataSnapshotDaemonStub struct {
	t                    *testing.T
	server               *httptest.Server
	mu                   sync.RWMutex
	title                string
	streamEvents         chan int64
	streamStarted        chan struct{}
	streamStartedOnce    sync.Once
	firstProjects        chan struct{}
	firstProjectsOnce    sync.Once
	blockFirstProjects   <-chan struct{}
	projectCalls         atomic.Int64
	readyCalls           atomic.Int64
	issueListCalls       atomic.Int64
	detailCalls          atomic.Int64
	graphCalls           atomic.Int64
	projectEventCalls    atomic.Int64
	graphFails           bool
	projectEvents        []katagenerated.EventEnvelope
	projectEventPages    []katagenerated.PollEventsBody
	projectEventPageSize int
	projectEventCursors  []int64
}

func newKataSnapshotDaemonStub(t *testing.T, title string) *kataSnapshotDaemonStub {
	t.Helper()
	stub := &kataSnapshotDaemonStub{
		t: t, title: title, graphFails: true, streamEvents: make(chan int64, 1),
		streamStarted: make(chan struct{}), firstProjects: make(chan struct{}),
	}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.serveHTTP))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *kataSnapshotDaemonStub) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v1/projects":
		call := s.projectCalls.Add(1)
		if call == 1 {
			s.firstProjectsOnce.Do(func() { close(s.firstProjects) })
			if s.blockFirstProjects != nil {
				select {
				case <-s.blockFirstProjects:
				case <-r.Context().Done():
					return
				}
			}
		}
		s.writeJSON(w, katagenerated.ListProjectsResponseBody{Projects: []katagenerated.ProjectOut{s.project()}})
	case "/api/v1/projects/7/ready":
		s.readyCalls.Add(1)
		s.writeJSON(w, katagenerated.ReadyResponseBody{Issues: []katagenerated.IssueOut{s.issueOut()}})
	case "/api/v1/issues/issue-member":
		if r.Method == http.MethodPost {
			var input struct {
				Title string `json:"title"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			s.mu.Lock()
			s.title = input.Title
			nextEventID := int64(1)
			if eventCount := len(s.projectEvents); eventCount > 0 {
				nextEventID = s.projectEvents[eventCount-1].EventID + 1
			}
			issueUID := "issue-member"
			s.projectEvents = append(s.projectEvents, katagenerated.EventEnvelope{
				Actor: "acceptance", ContentHash: "mutation-" + input.Title,
				CreatedAt: kataSnapshotE2ETime.Add(time.Duration(nextEventID) * time.Second),
				EventID:   nextEventID, EventUID: fmt.Sprintf("event-%d", nextEventID), IssueUID: &issueUID,
				OriginInstanceUID: "kata-e2e", ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a",
				Type: "issue.updated",
			})
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.detailCalls.Add(1)
		w.Header().Set("ETag", `"issue-revision"`)
		s.writeJSON(w, katagenerated.ShowIssueResponseBody{Issue: s.issue()})
	case "/api/v1/projects/7/issues/issue-member/graph":
		s.graphCalls.Add(1)
		s.mu.RLock()
		graphFails := s.graphFails
		title := s.title
		s.mu.RUnlock()
		if graphFails {
			http.Error(w, "forced graph failure", http.StatusBadGateway)
			return
		}
		projectUID := "project-a"
		s.writeJSON(w, katagenerated.ReachableGraphResponseBody{
			SourceUID: "issue-member", Depth: "full", HideDone: false,
			Edges: []katagenerated.ReachableGraphEdge{
				{FromUID: "issue-linked", ToUID: "issue-member", Kind: katagenerated.ReachableGraphEdgeKindRelated},
			},
			Nodes: []katagenerated.ReachableGraphNode{
				{
					ID: 11, UID: "issue-member", ProjectID: 7, ProjectUID: &projectUID,
					ShortID: "task-1", QualifiedID: "Project A#task-1", Title: title,
					Author: "acceptance", Body: "Acceptance body", Status: "open", Metadata: map[string]any{"source": "e2e"},
					Revision: 1, CreatedAt: kataSnapshotE2ETime, UpdatedAt: kataSnapshotE2ETime,
				},
				{
					ID: 12, UID: "issue-linked", ProjectID: 7, ProjectUID: &projectUID,
					ShortID: "task-2", QualifiedID: "Project A#task-2", Title: "Linked task",
					Author: "acceptance", Body: "Linked body", Status: "open", Metadata: map[string]any{"source": "e2e"},
					Revision: 1, CreatedAt: kataSnapshotE2ETime, UpdatedAt: kataSnapshotE2ETime,
				},
			},
		})
	case "/api/v1/issues":
		s.issueListCalls.Add(1)
		s.writeJSON(w, katagenerated.ListIssuesResponseBody{Issues: []katagenerated.IssueOut{s.issueOut()}})
	case "/api/v1/events":
		afterID, _ := strconv.ParseInt(r.URL.Query().Get("after_id"), 10, 64)
		s.writeJSON(w, katagenerated.PollEventsBody{Events: []katagenerated.EventEnvelope{}, NextAfterID: afterID})
	case "/api/v1/projects/7/events":
		s.projectEventCalls.Add(1)
		afterID, _ := strconv.ParseInt(r.URL.Query().Get("after_id"), 10, 64)
		requestedLimit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		s.mu.Lock()
		s.projectEventCursors = append(s.projectEventCursors, afterID)
		if len(s.projectEventPages) > 0 {
			page := s.projectEventPages[0]
			s.projectEventPages = s.projectEventPages[1:]
			s.mu.Unlock()
			s.writeJSON(w, page)
			return
		}
		configuredPageSize := s.projectEventPageSize
		projectEvents := append([]katagenerated.EventEnvelope(nil), s.projectEvents...)
		s.mu.Unlock()
		page := make([]katagenerated.EventEnvelope, 0, len(projectEvents))
		for _, event := range projectEvents {
			if event.ProjectID == 7 && event.EventID > afterID {
				page = append(page, event)
			}
		}
		pageSize := len(page)
		if requestedLimit > 0 && requestedLimit < pageSize {
			pageSize = requestedLimit
		}
		if configuredPageSize > 0 && configuredPageSize < pageSize {
			pageSize = configuredPageSize
		}
		page = page[:pageSize]
		nextAfterID := afterID
		if len(page) > 0 {
			nextAfterID = page[len(page)-1].EventID
		}
		s.writeJSON(w, katagenerated.PollEventsBody{Events: page, NextAfterID: nextAfterID})
	case "/api/v1/events/stream":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		s.streamStartedOnce.Do(func() { close(s.streamStarted) })
		for {
			select {
			case eventID := <-s.streamEvents:
				_, _ = w.Write([]byte("id: " + strconv.FormatInt(eventID, 10) + "\n" +
					"event: issue.updated\n" +
					"data: {}\n\n"))
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			case <-r.Context().Done():
				return
			}
		}
	default:
		assert.Fail(s.t, "unexpected Kata request", "%s", r.URL.String())
		http.NotFound(w, r)
	}
}

func (s *kataSnapshotDaemonStub) project() katagenerated.ProjectOut {
	return katagenerated.ProjectOut{
		ID: 7, UID: "project-a", Name: "Project A", Metadata: map[string]any{"kind": "test"},
		Revision: 1, CreatedAt: kataSnapshotE2ETime,
		Stats: &katagenerated.ProjectStatsOut{Open: 1, Closed: 0, LastEventAt: new(kataSnapshotE2ETime)},
	}
}

func (s *kataSnapshotDaemonStub) issueOut() katagenerated.IssueOut {
	s.mu.RLock()
	title := s.title
	s.mu.RUnlock()
	projectUID := "project-a"
	return katagenerated.IssueOut{
		ID: 11, UID: "issue-member", ProjectID: 7, ProjectUID: &projectUID,
		ShortID: "task-1", QualifiedID: "Project A#task-1", Title: title,
		Body: "Acceptance body", Status: "open", Metadata: map[string]any{"source": "e2e"},
		Revision: 1, Author: "acceptance", CreatedAt: kataSnapshotE2ETime, UpdatedAt: kataSnapshotE2ETime,
		Labels: []string{}, Blocks: []katagenerated.LinkPeer{}, BlockedBy: []katagenerated.LinkPeer{}, Related: []katagenerated.LinkPeer{},
	}
}

func (s *kataSnapshotDaemonStub) issue() katagenerated.Issue {
	issue := s.issueOut()
	return katagenerated.Issue{
		ID: issue.ID, UID: issue.UID, ProjectID: issue.ProjectID, ProjectUID: issue.ProjectUID,
		ShortID: issue.ShortID, Title: issue.Title, Body: issue.Body, Status: issue.Status,
		Metadata: issue.Metadata, Revision: issue.Revision, Author: issue.Author,
		CreatedAt: issue.CreatedAt, UpdatedAt: issue.UpdatedAt,
	}
}

func (s *kataSnapshotDaemonStub) writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		assert.NoError(s.t, err)
	}
}

func (s *kataSnapshotDaemonStub) setTitle(title string) {
	s.mu.Lock()
	s.title = title
	s.mu.Unlock()
}

func (s *kataSnapshotDaemonStub) waitForStream(t *testing.T) {
	t.Helper()
	select {
	case <-s.streamStarted:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Kata event stream did not start")
	}
}

func (s *kataSnapshotDaemonStub) waitForFirstProjects(t *testing.T) {
	t.Helper()
	select {
	case <-s.firstProjects:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "Kata project request did not start")
	}
}

func (s *kataSnapshotDaemonStub) publishEvent(t *testing.T, eventID int64) {
	t.Helper()
	select {
	case s.streamEvents <- eventID:
	case <-time.After(time.Second):
		require.FailNow(t, "Kata event stream did not accept event")
	}
}

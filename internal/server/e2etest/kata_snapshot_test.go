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
	"go.kenn.io/forge/internal/apiclient"
	apigenerated "go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	katagenerated "go.kenn.io/kata/pkg/client/generated"
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
		SelectedIssueUid: &selectedIssueUID, XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
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
		SelectedIssueUid: &selectedIssueUID, XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
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
		XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
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
		XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
	})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResponse.StatusCode(), string(secondResponse.Body))
	require.NotNil(secondResponse.JSON200)
	require.Less(time.Since(warmStarted), 5*time.Second)
	assert.Equal(first.Generation, secondResponse.JSON200.Generation)
	assert.Equal(first.InvalidationEpoch, secondResponse.JSON200.InvalidationEpoch)
	assert.Equal(first.Enrichment.SelectedDetail, secondResponse.JSON200.Enrichment.SelectedDetail)
	assert.Equal(first.Enrichment.SelectedHistory, secondResponse.JSON200.Enrichment.SelectedHistory)
	assert.Equal(first.Enrichment.Graph, secondResponse.JSON200.Enrichment.Graph)
	require.NotNil(first.Enrichment.GraphFetchedAt)
	require.NotNil(secondResponse.JSON200.Enrichment.GraphFetchedAt)
	assert.Equal(*first.Enrichment.GraphFetchedAt, *secondResponse.JSON200.Enrichment.GraphFetchedAt)
	assert.Equal(first.Enrichment.Errors, secondResponse.JSON200.Enrichment.Errors)
	assert.Equal(int64(1), fixture.daemon.detailCalls.Load(), "warm detail must reuse the upstream result")
	assert.Equal(int64(3), fixture.daemon.projectEventCalls.Load(), "warm history must reuse the complete paginated project stream")
	assert.Equal(int64(1), fixture.daemon.graphCalls.Load(), "warm graph must reuse the upstream result")

	mutationRequest, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		fixture.forge.URL+"/api/v1/kata/proxy/api/v1/issues/issue-member",
		strings.NewReader(`{"title":"After mutation"}`),
	)
	require.NoError(err)
	mutationRequest.Header.Set("Content-Type", "application/json")
	mutationRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	mutationResponse, err := fixture.forge.Client().Do(mutationRequest)
	require.NoError(err)
	require.NoError(mutationResponse.Body.Close())
	require.Equal(http.StatusNoContent, mutationResponse.StatusCode)

	thirdResponse, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
		Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
		SelectedIssueUid: &selectedIssueUID, GraphSourceUid: &selectedIssueUID,
		XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
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

func TestKataTaskSnapshotRetriesWhenSelectedRevisionChangesBetweenReadsE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Racing task")
	selectedIssueUID := "issue-member"
	fixture.daemon.mu.Lock()
	fixture.daemon.bumpRevisionOnFirstDetail = true
	fixture.daemon.mu.Unlock()

	scope := apigenerated.Project
	authority := apigenerated.Ready
	projectUID := "project-a"
	response, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
		Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
		SelectedIssueUid:     &selectedIssueUID,
		XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
	})

	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode(), string(response.Body))
	require.NotNil(response.JSON200)
	snapshot := response.JSON200
	require.NotNil(snapshot.Issues)
	require.Len(*snapshot.Issues, 1)
	assert.Equal(int64(2), (*snapshot.Issues)[0].Revision, "authority must be reloaded at the post-change revision")
	require.NotNil(snapshot.Enrichment.SelectedDetail)
	detailBody, ok := snapshot.Enrichment.SelectedDetail.Detail.(map[string]any)
	require.True(ok)
	detailIssue, ok := detailBody["issue"].(map[string]any)
	require.True(ok)
	assert.InDelta(float64(2), detailIssue["revision"], 0, "detail must match the reloaded authority revision")
	assert.Equal(int64(2), fixture.daemon.detailCalls.Load(), "stale detail must trigger exactly one retry")
	assert.Positive(snapshot.InvalidationEpoch, "revision mismatch must invalidate the authority epoch")
	assert.Nil(snapshot.Enrichment.Errors)
}

func TestKataTaskSnapshotRetriesWhenGraphRevisionChangesBetweenReadsE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Racing graph task")
	fixture.daemon.mu.Lock()
	fixture.daemon.graphFails = false
	fixture.daemon.bumpRevisionOnFirstGraph = true
	fixture.daemon.mu.Unlock()

	scope := apigenerated.Project
	authority := apigenerated.Ready
	projectUID := "project-a"
	graphSourceUID := "issue-member"
	response, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
		Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
		GraphSourceUid:       &graphSourceUID,
		XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
	})

	require.NoError(err)
	require.Equal(http.StatusOK, response.StatusCode(), string(response.Body))
	require.NotNil(response.JSON200)
	snapshot := response.JSON200
	require.NotNil(snapshot.Issues)
	require.Len(*snapshot.Issues, 1)
	assert.Equal(int64(2), (*snapshot.Issues)[0].Revision, "authority must be reloaded at the post-change revision")
	require.NotNil(snapshot.Enrichment.Graph)
	require.NotNil(snapshot.Enrichment.Graph.Nodes)
	require.Len(*snapshot.Enrichment.Graph.Nodes, 2)
	assert.Equal(int64(2), (*snapshot.Enrichment.Graph.Nodes)[0].Revision, "graph must match the reloaded authority revision")
	assert.Equal(int64(2), fixture.daemon.graphCalls.Load(), "stale graph must trigger exactly one retry")
	assert.Positive(snapshot.InvalidationEpoch, "revision mismatch must invalidate the authority epoch")
	assert.Nil(snapshot.Enrichment.Errors)
}

func TestKataTaskSnapshotConcurrentSelectedHistoriesShareProjectPaginationE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Ready task")
	memberIssueUID := "issue-member"
	otherIssueUID := "issue-other"
	releaseEvents := make(chan struct{})
	fixture.daemon.mu.Lock()
	fixture.daemon.includeOtherIssue = true
	fixture.daemon.blockFirstProjectEvents = releaseEvents
	fixture.daemon.projectEvents = []katagenerated.EventEnvelope{
		{
			Actor: "acceptance", ContentHash: "member-1", CreatedAt: kataSnapshotE2ETime.Add(time.Second),
			EventID: 1, EventUID: "event-1", IssueUID: &memberIssueUID, OriginInstanceUID: "kata-e2e",
			ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
		},
		{
			Actor: "acceptance", ContentHash: "other-2", CreatedAt: kataSnapshotE2ETime.Add(2 * time.Second),
			EventID: 2, EventUID: "event-2", IssueUID: &otherIssueUID, OriginInstanceUID: "kata-e2e",
			ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
		},
		{
			Actor: "acceptance", ContentHash: "member-3", CreatedAt: kataSnapshotE2ETime.Add(3 * time.Second),
			EventID: 3, EventUID: "event-3", IssueUID: &memberIssueUID, OriginInstanceUID: "kata-e2e",
			ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
		},
		{
			Actor: "acceptance", ContentHash: "other-4", CreatedAt: kataSnapshotE2ETime.Add(4 * time.Second),
			EventID: 4, EventUID: "event-4", IssueUID: &otherIssueUID, OriginInstanceUID: "kata-e2e",
			ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
		},
	}
	fixture.daemon.projectEventPageSize = 1
	fixture.daemon.mu.Unlock()

	type result struct {
		selectedUID string
		response    *apigenerated.GetKataTaskSnapshotResponse
		err         error
	}
	start := make(chan struct{})
	started := make(chan struct{}, 2)
	results := make(chan result, 2)
	go func() {
		started <- struct{}{}
		<-start
		scope := apigenerated.Project
		authority := apigenerated.Ready
		projectUID := "project-a"
		response, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
			Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
			SelectedIssueUid: &memberIssueUID, XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
		})
		results <- result{selectedUID: memberIssueUID, response: response, err: err}
	}()
	go func() {
		started <- struct{}{}
		<-start
		scope := apigenerated.Project
		authority := apigenerated.Ready
		projectUID := "project-a"
		response, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
			Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
			SelectedIssueUid: &otherIssueUID, XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
		})
		results <- result{selectedUID: otherIssueUID, response: response, err: err}
	}()
	<-started
	<-started
	close(start)
	select {
	case <-fixture.daemon.firstProjectEvents:
	case <-time.After(2 * time.Second):
		require.FailNow("shared project history request did not start")
	}
	close(releaseEvents)

	got := make(map[string]*apigenerated.KataTaskSnapshotResponse, 2)
	for range 2 {
		completed := <-results
		require.NoError(completed.err)
		require.NotNil(completed.response)
		require.Equal(http.StatusOK, completed.response.StatusCode(), string(completed.response.Body))
		require.NotNil(completed.response.JSON200)
		got[completed.selectedUID] = completed.response.JSON200
	}
	member := got[memberIssueUID]
	require.NotNil(member)
	require.NotNil(member.Enrichment.SelectedIssueUid)
	assert.Equal(memberIssueUID, *member.Enrichment.SelectedIssueUid)
	require.NotNil(member.Enrichment.SelectedHistory)
	require.Len(*member.Enrichment.SelectedHistory, 2)
	assert.Equal([]string{"member-1", "member-3"}, []string{
		(*member.Enrichment.SelectedHistory)[0].ContentHash,
		(*member.Enrichment.SelectedHistory)[1].ContentHash,
	})
	assert.Nil(member.Enrichment.Errors)
	other := got[otherIssueUID]
	require.NotNil(other)
	require.NotNil(other.Enrichment.SelectedIssueUid)
	assert.Equal(otherIssueUID, *other.Enrichment.SelectedIssueUid)
	require.NotNil(other.Enrichment.SelectedHistory)
	require.Len(*other.Enrichment.SelectedHistory, 2)
	assert.Equal([]string{"other-2", "other-4"}, []string{
		(*other.Enrichment.SelectedHistory)[0].ContentHash,
		(*other.Enrichment.SelectedHistory)[1].ContentHash,
	})
	assert.Nil(other.Enrichment.Errors)
	assert.Equal(int64(5), fixture.daemon.projectEventCalls.Load(), "concurrent selections must share one complete project pagination sequence")
	fixture.daemon.mu.RLock()
	assert.Equal([]int64{0, 1, 2, 3, 4}, fixture.daemon.projectEventCursors)
	fixture.daemon.mu.RUnlock()
}

func TestKataTaskSnapshotConcurrentOversizedHistoriesStaySelectedAndUncachedE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Ready task")
	memberIssueUID := "issue-member"
	otherIssueUID := "issue-other"
	unrelatedIssueUID := "issue-unrelated"
	releaseEvents := make(chan struct{})
	fixture.daemon.mu.Lock()
	fixture.daemon.includeOtherIssue = true
	fixture.daemon.blockFirstProjectEvents = releaseEvents
	fixture.daemon.projectEvents = []katagenerated.EventEnvelope{
		{
			Actor: "acceptance", ContentHash: "member-before-oversized", CreatedAt: kataSnapshotE2ETime.Add(time.Second),
			EventID: 1, EventUID: "event-1", IssueUID: &memberIssueUID, OriginInstanceUID: "kata-e2e",
			ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
		},
		{
			Actor: "acceptance", ContentHash: "other-before-oversized", CreatedAt: kataSnapshotE2ETime.Add(2 * time.Second),
			EventID: 2, EventUID: "event-2", IssueUID: &otherIssueUID, OriginInstanceUID: "kata-e2e",
			ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
		},
		{
			Actor: "acceptance", ContentHash: strings.Repeat("x", 16<<20), CreatedAt: kataSnapshotE2ETime.Add(3 * time.Second),
			EventID: 3, EventUID: "event-3", IssueUID: &unrelatedIssueUID, OriginInstanceUID: "kata-e2e",
			ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
		},
		{
			Actor: "acceptance", ContentHash: "member-after-oversized", CreatedAt: kataSnapshotE2ETime.Add(4 * time.Second),
			EventID: 4, EventUID: "event-4", IssueUID: &memberIssueUID, OriginInstanceUID: "kata-e2e",
			ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
		},
		{
			Actor: "acceptance", ContentHash: "other-after-oversized", CreatedAt: kataSnapshotE2ETime.Add(5 * time.Second),
			EventID: 5, EventUID: "event-5", IssueUID: &otherIssueUID, OriginInstanceUID: "kata-e2e",
			ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
		},
	}
	fixture.daemon.projectEventPageSize = 1
	fixture.daemon.mu.Unlock()

	type result struct {
		selectedUID string
		response    *apigenerated.GetKataTaskSnapshotResponse
		err         error
	}
	start := make(chan struct{})
	started := make(chan struct{}, 2)
	results := make(chan result, 2)
	go func() {
		started <- struct{}{}
		<-start
		scope := apigenerated.Project
		authority := apigenerated.Ready
		projectUID := "project-a"
		response, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
			Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
			SelectedIssueUid: &memberIssueUID, XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
		})
		results <- result{selectedUID: memberIssueUID, response: response, err: err}
	}()
	go func() {
		started <- struct{}{}
		<-start
		scope := apigenerated.Project
		authority := apigenerated.Ready
		projectUID := "project-a"
		response, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
			Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
			SelectedIssueUid: &otherIssueUID, XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
		})
		results <- result{selectedUID: otherIssueUID, response: response, err: err}
	}()
	<-started
	<-started
	close(start)
	select {
	case <-fixture.daemon.firstProjectEvents:
	case <-time.After(2 * time.Second):
		require.FailNow("oversized shared project history request did not start")
	}
	close(releaseEvents)

	got := make(map[string]*apigenerated.KataTaskSnapshotResponse, 2)
	for range 2 {
		completed := <-results
		require.NoError(completed.err)
		require.NotNil(completed.response)
		require.Equal(http.StatusOK, completed.response.StatusCode(), string(completed.response.Body))
		require.NotNil(completed.response.JSON200)
		got[completed.selectedUID] = completed.response.JSON200
	}
	member := got[memberIssueUID]
	require.NotNil(member)
	require.NotNil(member.Enrichment.SelectedIssueUid)
	assert.Equal(memberIssueUID, *member.Enrichment.SelectedIssueUid)
	require.NotNil(member.Enrichment.SelectedHistory)
	require.Len(*member.Enrichment.SelectedHistory, 2)
	assert.Equal([]string{"member-before-oversized", "member-after-oversized"}, []string{
		(*member.Enrichment.SelectedHistory)[0].ContentHash,
		(*member.Enrichment.SelectedHistory)[1].ContentHash,
	})
	assert.Nil(member.Enrichment.Errors)
	other := got[otherIssueUID]
	require.NotNil(other)
	require.NotNil(other.Enrichment.SelectedIssueUid)
	assert.Equal(otherIssueUID, *other.Enrichment.SelectedIssueUid)
	require.NotNil(other.Enrichment.SelectedHistory)
	require.Len(*other.Enrichment.SelectedHistory, 2)
	assert.Equal([]string{"other-before-oversized", "other-after-oversized"}, []string{
		(*other.Enrichment.SelectedHistory)[0].ContentHash,
		(*other.Enrichment.SelectedHistory)[1].ContentHash,
	})
	assert.Nil(other.Enrichment.Errors)
	assert.Equal(int64(12), fixture.daemon.projectEventCalls.Load(), "the second selection must retry after the oversized shared flight")
	fixture.daemon.mu.RLock()
	assert.Equal([]int64{0, 1, 2, 3, 4, 5, 0, 1, 2, 3, 4, 5}, fixture.daemon.projectEventCursors)
	fixture.daemon.mu.RUnlock()

	scope := apigenerated.Project
	authority := apigenerated.Ready
	projectUID := "project-a"
	reloaded, err := fixture.client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
		Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
		SelectedIssueUid: &memberIssueUID, XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
	})
	require.NoError(err)
	require.Equal(http.StatusOK, reloaded.StatusCode(), string(reloaded.Body))
	require.NotNil(reloaded.JSON200)
	require.NotNil(reloaded.JSON200.Enrichment.SelectedHistory)
	require.Len(*reloaded.JSON200.Enrichment.SelectedHistory, 2)
	assert.Equal(member.Enrichment.SelectedHistory, reloaded.JSON200.Enrichment.SelectedHistory)
	assert.Nil(reloaded.JSON200.Enrichment.Errors)
	assert.Equal(int64(18), fixture.daemon.projectEventCalls.Load(), "oversized project history must not be retained")
	fixture.daemon.mu.RLock()
	assert.Equal([]int64{
		0, 1, 2, 3, 4, 5,
		0, 1, 2, 3, 4, 5,
		0, 1, 2, 3, 4, 5,
	}, fixture.daemon.projectEventCursors)
	fixture.daemon.mu.RUnlock()
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
		SelectedIssueUid: &selectedIssueUID, XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
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
		GraphSourceUid: &graphSourceUID, XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
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
	_, client := startKataSnapshotE2EForge(t)

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
			XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
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

func TestKataTaskSnapshotDaemonRotationRejectsInflightSelectedEnrichmentE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	releaseOld := make(chan struct{})
	oldDaemon := newKataSnapshotDaemonStub(t, "Stale target")
	oldDaemon.blockFirstDetail = releaseOld
	oldSelectedIssueUID := "issue-member"
	oldDaemon.mu.Lock()
	oldDaemon.graphFails = false
	oldDaemon.projectEvents = []katagenerated.EventEnvelope{{
		Actor: "acceptance", ContentHash: "stale-history", CreatedAt: kataSnapshotE2ETime.Add(time.Second),
		EventID: 1, EventUID: "event-1", IssueUID: &oldSelectedIssueUID, OriginInstanceUID: "kata-e2e",
		ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
	}}
	oldDaemon.mu.Unlock()

	newDaemon := newKataSnapshotDaemonStub(t, "Rotated target")
	newSelectedIssueUID := "issue-member"
	newDaemon.mu.Lock()
	newDaemon.graphFails = false
	newDaemon.projectEvents = []katagenerated.EventEnvelope{{
		Actor: "acceptance", ContentHash: "rotated-history", CreatedAt: kataSnapshotE2ETime.Add(2 * time.Second),
		EventID: 2, EventUID: "event-2", IssueUID: &newSelectedIssueUID, OriginInstanceUID: "kata-e2e",
		ProjectID: 7, ProjectName: "Project A", ProjectUID: "project-a", Type: "issue.updated",
	}}
	newDaemon.mu.Unlock()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataSnapshotE2ECatalog(t, home, oldDaemon.server.URL)
	_, client := startKataSnapshotE2EForge(t)

	type result struct {
		response *apigenerated.GetKataTaskSnapshotResponse
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		scope := apigenerated.Project
		authority := apigenerated.Ready
		projectUID := "project-a"
		selectedIssueUID := "issue-member"
		response, err := client.HTTP.GetKataTaskSnapshotWithResponse(t.Context(), &apigenerated.GetKataTaskSnapshotParams{
			Scope: &scope, ProjectUid: &projectUID, Authority: &authority,
			SelectedIssueUid: &selectedIssueUID, GraphSourceUid: &selectedIssueUID,
			XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
		})
		resultCh <- result{response: response, err: err}
	}()

	select {
	case <-oldDaemon.firstDetail:
	case <-time.After(2 * time.Second):
		require.FailNow("old target selected detail request did not start")
	}
	writeKataSnapshotE2ECatalog(t, home, newDaemon.server.URL)
	close(releaseOld)

	var got result
	select {
	case got = <-resultCh:
	case <-time.After(3 * time.Second):
		require.FailNow("snapshot request did not complete after selected enrichment target rotation")
	}

	require.NoError(got.err)
	require.NotNil(got.response)
	require.Equal(http.StatusOK, got.response.StatusCode(), string(got.response.Body))
	require.NotNil(got.response.JSON200)
	snapshot := got.response.JSON200
	assert.Positive(snapshot.InvalidationEpoch)
	require.NotNil(snapshot.Issues)
	require.Len(*snapshot.Issues, 1)
	assert.Equal("Rotated target", (*snapshot.Issues)[0].Title)
	require.NotNil(snapshot.Enrichment.SelectedDetail)
	detailBody, ok := snapshot.Enrichment.SelectedDetail.Detail.(map[string]any)
	require.True(ok)
	detailIssue, ok := detailBody["issue"].(map[string]any)
	require.True(ok)
	assert.Equal("Rotated target", detailIssue["title"])
	require.NotNil(snapshot.Enrichment.SelectedHistory)
	require.Len(*snapshot.Enrichment.SelectedHistory, 1)
	assert.Equal("rotated-history", (*snapshot.Enrichment.SelectedHistory)[0].ContentHash)
	require.NotNil(snapshot.Enrichment.Graph)
	require.NotNil(snapshot.Enrichment.Graph.Nodes)
	require.Len(*snapshot.Enrichment.Graph.Nodes, 2)
	assert.Equal("Rotated target", (*snapshot.Enrichment.Graph.Nodes)[0].Title)
	assert.Nil(snapshot.Enrichment.Errors)
	assert.Positive(oldDaemon.detailCalls.Load())
	assert.Positive(newDaemon.detailCalls.Load())
	assert.Positive(newDaemon.projectEventCalls.Load())
	assert.Positive(newDaemon.graphCalls.Load())
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
		fixture.forge.URL+"/api/v1/kata/proxy/api/v1/issues/issue-member",
		strings.NewReader(`{"title":"After mutation"}`),
	)
	require.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err := fixture.forge.Client().Do(req)
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

func TestKataTaskSnapshotFreshReadAdvancesInvalidationEpochE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Cached title")

	first := fixture.snapshot(t, apigenerated.Project, apigenerated.Ready)
	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		fixture.forge.URL+"/api/v1/kata/tasks/snapshot?scope=project&project_uid=project-a&authority=ready&fresh=true",
		http.NoBody,
	)
	require.NoError(err)
	req.Header.Set("X-Kenn-Forge-Kata-Daemon", kataSnapshotE2EDaemonID)
	response, err := fixture.forge.Client().Do(req)
	require.NoError(err)
	defer response.Body.Close()
	require.Equal(http.StatusOK, response.StatusCode)
	var fresh apigenerated.KataTaskSnapshotResponse
	require.NoError(json.NewDecoder(response.Body).Decode(&fresh))

	assert.Greater(fresh.InvalidationEpoch, first.InvalidationEpoch)
	assert.Greater(fresh.Generation, first.Generation)
}

func TestKataProxyCommittedMutationWithLostResponseIsReportedOnceE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newKataSnapshotE2EFixture(t, "Before lost response")
	first := fixture.snapshot(t, apigenerated.Project, apigenerated.Ready)
	fixture.daemon.mu.Lock()
	fixture.daemon.dropNextMutationResponse = true
	fixture.daemon.mu.Unlock()

	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		fixture.forge.URL+"/api/v1/kata/proxy/api/v1/issues/issue-member",
		strings.NewReader(`{"title":"Committed once"}`),
	)
	require.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	response, err := fixture.forge.Client().Do(req)
	require.NoError(err)
	defer response.Body.Close()
	require.Equal(http.StatusBadGateway, response.StatusCode)
	var problem apigenerated.ProblemError
	require.NoError(json.NewDecoder(response.Body).Decode(&problem))

	assert.Equal(apigenerated.MutationOutcomeUnknown, problem.Code)
	assert.Equal(int64(1), fixture.daemon.mutationCalls.Load())
	refreshed := fixture.snapshot(t, apigenerated.Project, apigenerated.Ready)
	require.NotNil(refreshed.Issues)
	require.Len(*refreshed.Issues, 1)
	assert.Equal("Committed once", (*refreshed.Issues)[0].Title)
	assert.Greater(refreshed.InvalidationEpoch, first.InvalidationEpoch)
	assert.Equal(int64(1), fixture.daemon.mutationCalls.Load())
}

type kataSnapshotE2EFixture struct {
	daemon *kataSnapshotDaemonStub
	forge  *httptest.Server
	client *apiclient.Client
}

func newKataSnapshotE2EFixture(t *testing.T, title string) *kataSnapshotE2EFixture {
	t.Helper()
	daemon := newKataSnapshotDaemonStub(t, title)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataSnapshotE2ECatalog(t, home, daemon.server.URL)
	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
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
		ItemKey: db.KataWorkspaceItemKey(metadata), GitHeadRef: "kenn-forge/kata/task-1",
		WorkspaceBranch: "kenn-forge/kata/task-1", WorktreePath: "/tmp/ws-kata-e2e",
		TmuxSession: "kenn-forge-ws-kata-e2e", Status: "ready", KataMetadata: &metadata,
	}))
	forge := httptest.NewServer(srv)
	t.Cleanup(forge.Close)
	client, err := apiclient.NewWithHTTPClient(forge.URL, forge.Client())
	require.NoError(t, err)
	return &kataSnapshotE2EFixture{daemon: daemon, forge: forge, client: client}
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
		XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode(), string(response.Body))
	require.NotNil(t, response.JSON200)
	return response.JSON200
}

func (f *kataSnapshotE2EFixture) references(t *testing.T) *apigenerated.KataTaskReferenceResponse {
	t.Helper()
	response, err := f.client.HTTP.SearchKataTaskReferencesWithResponse(t.Context(), &apigenerated.SearchKataTaskReferencesParams{
		XKennForgeKataDaemon: new(kataSnapshotE2EDaemonID),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode(), string(response.Body))
	require.NotNil(t, response.JSON200)
	return response.JSON200
}

func startKataSnapshotE2EForge(t *testing.T) (*httptest.Server, *apiclient.Client) {
	t.Helper()
	srv, _ := setupTestServer(t)
	forge := httptest.NewServer(srv)
	t.Cleanup(forge.Close)
	client, err := apiclient.NewWithHTTPClient(forge.URL, forge.Client())
	require.NoError(t, err)
	return forge, client
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
	t                         *testing.T
	server                    *httptest.Server
	mu                        sync.RWMutex
	title                     string
	streamEvents              chan int64
	streamStarted             chan struct{}
	streamStartedOnce         sync.Once
	firstProjects             chan struct{}
	firstProjectsOnce         sync.Once
	blockFirstProjects        <-chan struct{}
	firstDetail               chan struct{}
	firstDetailOnce           sync.Once
	blockFirstDetail          <-chan struct{}
	firstProjectEvents        chan struct{}
	firstProjectEventsOnce    sync.Once
	blockFirstProjectEvents   <-chan struct{}
	projectCalls              atomic.Int64
	readyCalls                atomic.Int64
	issueListCalls            atomic.Int64
	detailCalls               atomic.Int64
	graphCalls                atomic.Int64
	projectEventCalls         atomic.Int64
	graphFails                bool
	includeOtherIssue         bool
	revision                  int64
	bumpRevisionOnFirstDetail bool
	bumpRevisionOnFirstGraph  bool
	projectEvents             []katagenerated.EventEnvelope
	projectEventPages         []katagenerated.PollEventsBody
	projectEventPageSize      int
	projectEventCursors       []int64
	dropNextMutationResponse  bool
	mutationCalls             atomic.Int64
}

func newKataSnapshotDaemonStub(t *testing.T, title string) *kataSnapshotDaemonStub {
	t.Helper()
	stub := &kataSnapshotDaemonStub{
		t: t, title: title, graphFails: true, revision: 1, streamEvents: make(chan int64, 1),
		streamStarted: make(chan struct{}), firstProjects: make(chan struct{}), firstDetail: make(chan struct{}),
		firstProjectEvents: make(chan struct{}),
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
		issues := []katagenerated.IssueOut{s.issueOut("issue-member")}
		s.mu.RLock()
		includeOtherIssue := s.includeOtherIssue
		s.mu.RUnlock()
		if includeOtherIssue {
			issues = append(issues, s.issueOut("issue-other"))
		}
		s.writeJSON(w, katagenerated.ReadyResponseBody{Issues: issues})
	case "/api/v1/issues/issue-member", "/api/v1/issues/issue-other":
		issueUID := strings.TrimPrefix(r.URL.Path, "/api/v1/issues/")
		if r.Method == http.MethodPost {
			s.mutationCalls.Add(1)
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
			dropResponse := s.dropNextMutationResponse
			s.dropNextMutationResponse = false
			s.mu.Unlock()
			if dropResponse {
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					http.Error(w, "response hijacking unavailable", http.StatusInternalServerError)
					return
				}
				connection, _, hijackErr := hijacker.Hijack()
				if hijackErr == nil {
					_ = connection.Close()
				}
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		call := s.detailCalls.Add(1)
		if call == 1 {
			s.firstDetailOnce.Do(func() { close(s.firstDetail) })
			if s.blockFirstDetail != nil {
				select {
				case <-s.blockFirstDetail:
				case <-r.Context().Done():
					return
				}
			}
			s.mu.Lock()
			if s.bumpRevisionOnFirstDetail {
				// The authority for this request was read at the previous
				// revision, so this detail response is deliberately torn.
				s.revision++
			}
			s.mu.Unlock()
		}
		w.Header().Set("ETag", `"issue-revision"`)
		s.writeJSON(w, katagenerated.ShowIssueResponseBody{Issue: s.issue(issueUID)})
	case "/api/v1/projects/7/issues/issue-member/graph":
		call := s.graphCalls.Add(1)
		s.mu.Lock()
		if call == 1 && s.bumpRevisionOnFirstGraph {
			// The authority for this request was read at the previous
			// revision, so this graph response is deliberately torn.
			s.revision++
		}
		graphFails := s.graphFails
		title := s.title
		revision := s.revision
		s.mu.Unlock()
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
					Revision: revision, CreatedAt: kataSnapshotE2ETime, UpdatedAt: kataSnapshotE2ETime,
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
		issues := []katagenerated.IssueOut{s.issueOut("issue-member")}
		s.mu.RLock()
		includeOtherIssue := s.includeOtherIssue
		s.mu.RUnlock()
		if includeOtherIssue {
			issues = append(issues, s.issueOut("issue-other"))
		}
		s.writeJSON(w, katagenerated.ListIssuesResponseBody{Issues: issues})
	case "/api/v1/events":
		afterID, _ := strconv.ParseInt(r.URL.Query().Get("after_id"), 10, 64)
		s.writeJSON(w, katagenerated.PollEventsBody{Events: []katagenerated.EventEnvelope{}, NextAfterID: afterID})
	case "/api/v1/projects/7/events":
		call := s.projectEventCalls.Add(1)
		if call == 1 {
			s.firstProjectEventsOnce.Do(func() { close(s.firstProjectEvents) })
			s.mu.RLock()
			blockFirstProjectEvents := s.blockFirstProjectEvents
			s.mu.RUnlock()
			if blockFirstProjectEvents != nil {
				select {
				case <-blockFirstProjectEvents:
				case <-r.Context().Done():
					return
				}
			}
		}
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

func (s *kataSnapshotDaemonStub) issueOut(uid string) katagenerated.IssueOut {
	s.mu.RLock()
	title := s.title
	revision := s.revision
	s.mu.RUnlock()
	id := int64(11)
	shortID := "task-1"
	qualifiedID := "Project A#task-1"
	if uid == "issue-other" {
		id = 12
		shortID = "task-2"
		qualifiedID = "Project A#task-2"
		title = "Other ready task"
	}
	projectUID := "project-a"
	return katagenerated.IssueOut{
		ID: id, UID: uid, ProjectID: 7, ProjectUID: &projectUID,
		ShortID: shortID, QualifiedID: qualifiedID, Title: title,
		Body: "Acceptance body", Status: "open", Metadata: map[string]any{"source": "e2e"},
		Revision: revision, Author: "acceptance", CreatedAt: kataSnapshotE2ETime, UpdatedAt: kataSnapshotE2ETime,
		Labels: []string{}, Blocks: []katagenerated.LinkPeer{}, BlockedBy: []katagenerated.LinkPeer{}, Related: []katagenerated.LinkPeer{},
	}
}

func (s *kataSnapshotDaemonStub) issue(uid string) katagenerated.Issue {
	issue := s.issueOut(uid)
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

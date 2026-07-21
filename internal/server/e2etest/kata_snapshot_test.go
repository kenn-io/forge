package e2etest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	katagenerated "go.kenn.io/kata/pkg/client/generated"
	"go.kenn.io/middleman/internal/apiclient"
	apigenerated "go.kenn.io/middleman/internal/apiclient/generated"
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
	assert.False(snapshot.Enrichment.SelectedDetail.WorkspaceTarget.Available,
		"the empty temporary SQLite database has no workspace mapping")
	assert.Positive(snapshot.Generation)
	assert.Positive(snapshot.EventCursor)
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
	client := startKataSnapshotE2EMiddleman(t)

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
	got := <-resultCh

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

type kataSnapshotE2EFixture struct {
	daemon *kataSnapshotDaemonStub
	client *apiclient.Client
}

func newKataSnapshotE2EFixture(t *testing.T, title string) *kataSnapshotE2EFixture {
	t.Helper()
	daemon := newKataSnapshotDaemonStub(t, title)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataSnapshotE2ECatalog(t, home, daemon.server.URL)
	client := startKataSnapshotE2EMiddleman(t)
	return &kataSnapshotE2EFixture{daemon: daemon, client: client}
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

func startKataSnapshotE2EMiddleman(t *testing.T) *apiclient.Client {
	t.Helper()
	srv, _ := setupTestServer(t)
	middleman := httptest.NewServer(srv)
	t.Cleanup(middleman.Close)
	client, err := apiclient.NewWithHTTPClient(middleman.URL, middleman.Client())
	require.NoError(t, err)
	return client
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
	t                  *testing.T
	server             *httptest.Server
	mu                 sync.RWMutex
	title              string
	streamEvents       chan int64
	streamStarted      chan struct{}
	streamStartedOnce  sync.Once
	firstProjects      chan struct{}
	firstProjectsOnce  sync.Once
	blockFirstProjects <-chan struct{}
	projectCalls       atomic.Int64
	readyCalls         atomic.Int64
	issueListCalls     atomic.Int64
}

func newKataSnapshotDaemonStub(t *testing.T, title string) *kataSnapshotDaemonStub {
	t.Helper()
	stub := &kataSnapshotDaemonStub{
		t: t, title: title, streamEvents: make(chan int64, 1),
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
		w.Header().Set("ETag", `"issue-revision"`)
		s.writeJSON(w, katagenerated.ShowIssueResponseBody{Issue: s.issue()})
	case "/api/v1/issues":
		s.issueListCalls.Add(1)
		s.writeJSON(w, katagenerated.ListIssuesResponseBody{Issues: []katagenerated.IssueOut{s.issueOut()}})
	case "/api/v1/events":
		afterID, _ := strconv.ParseInt(r.URL.Query().Get("after_id"), 10, 64)
		s.writeJSON(w, katagenerated.PollEventsBody{Events: []katagenerated.EventEnvelope{}, NextAfterID: afterID})
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

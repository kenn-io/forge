package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/kata"
)

func TestKataTaskSnapshotReturnsServiceUnavailableWhenEventRegistryIsClosed(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		assert.Fail("closed event registry contacted Kata")
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
active_daemon = "primary"

[[daemon]]
name = "primary"
url = "`+upstream.URL+`"
`)
	srv, _ := setupTestServer(t)
	srv.kataEvents.Close()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/tasks/snapshot?scope=global&authority=open", nil)
	req.Header.Set(kataDaemonHeaderName, "primary")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	assert.Contains(rr.Header().Values("Vary"), kataDaemonHeaderName)
	problem := decodeMsgvaultProblem(t, rr)
	assert.Equal(CodeServiceUnavailable, problem.Code)
}

func TestKataTaskSnapshotSerializesAuthorityAndReplayableCursor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	snapshot := testKataCoordinatedAuthority().Snapshot
	snapshot.FetchedAt = time.Date(2026, 7, 20, 20, 30, 0, 0, time.UTC)
	srv := setupKataSnapshotRouteServer(t, daemon, snapshot, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/tasks/snapshot?scope=project&project_uid=project-a&authority=ready&selected_issue_uid=not-a-member&graph_source_uid=not-a-member", nil)
	req.Header.Set(kataDaemonHeaderName, "primary")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(rr.Header().Values("Vary"), kataDaemonHeaderName)
	raw := rr.Body.String()
	var response kataTaskSnapshotResponse
	require.NoError(json.Unmarshal([]byte(raw), &response))
	assert.Equal("server-a", response.ServerInstanceID)
	assert.Equal("primary", response.DaemonID)
	assert.Equal(kataAuthorityRequest{Scope: "project", ProjectUID: "project-a", Authority: "ready"}, response.Intent)
	assert.Equal(uint64(1), response.Generation)
	assert.Zero(response.InvalidationEpoch)
	assert.Equal(uint64(101), response.EventCursor)
	assert.Equal(snapshot.FetchedAt, response.FetchedAt)
	assert.Len(response.Projects, 1)
	assert.Equal(snapshot.MemberIssueUIDs, response.MemberIssueUIDs)
	assert.Len(response.Issues, 2)
	assert.Empty(response.Enrichment.SelectedIssueUID)
	assert.Contains(raw, `"member_issue_uids"`)
	assert.Contains(raw, `"project_uid"`)
	assert.Contains(raw, `"qualified_id"`)

	binding := srv.kataEvents.bindings[daemon.ID]
	require.NotNil(binding)
	binding.target.invalidateAndBroadcast()
	records, _, stale := binding.hub.ReplaySnapshotSince(response.EventCursor)
	assert.False(stale)
	require.Len(records, 1)
	transformed, ok := binding.transform(records[0])
	require.True(ok)
	assert.Equal("kata.tasks.invalidated", transformed.Event.Type)
}

func TestKataTaskSnapshotSerializesLocalEnrichmentErrorsAndIndependentGraphSource(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	daemon := kata.Daemon{ID: "primary", URL: upstream.URL}
	snapshot := testKataCoordinatedAuthority().Snapshot
	snapshot.FetchedAt = time.Date(2026, 7, 20, 20, 45, 0, 0, time.UTC)
	srv := setupKataSnapshotRouteServer(t, daemon, snapshot, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/tasks/snapshot?selected_issue_uid=issue-member&graph_source_uid=issue-source", nil)
	req.Header.Set(kataDaemonHeaderName, "primary")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var response kataTaskSnapshotResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &response))
	assert.Equal("issue-member", response.Enrichment.SelectedIssueUID)
	assert.Nil(response.Enrichment.SelectedDetail)
	assert.Nil(response.Enrichment.Graph)
	assert.Equal(kataSnapshotEnrichmentError{Code: CodeUpstreamError, Message: "Could not load selected task detail."}, response.Enrichment.Errors[kataSnapshotEnrichmentStageDetail])
	assert.Equal(kataSnapshotEnrichmentError{Code: CodeUpstreamError, Message: "Could not load selected task history."}, response.Enrichment.Errors[kataSnapshotEnrichmentStageHistory])
	assert.Equal(kataSnapshotEnrichmentError{Code: CodeUpstreamError, Message: "Could not load reachable graph."}, response.Enrichment.Errors[kataSnapshotEnrichmentStageGraph])
	assert.True(slices.Contains(paths, "/api/v1/issues/issue-member"))
	assert.True(slices.Contains(paths, "/api/v1/events"))
	assert.True(slices.Contains(paths, "/api/v1/projects/7/issues/issue-source/graph"))
}

func TestKataTaskReferencesReuseCachedGlobalOpenAuthority(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	snapshot := testKataCoordinatedAuthority().Snapshot
	snapshot.MemberIssueUIDs = []string{"issue-a", "issue-b", "issue-unique"}
	snapshot.Issues = []kataTaskSummary{
		{ID: 1, UID: "issue-a", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A", ShortID: "dup", QualifiedID: "Project A#dup", Title: "Matching task"},
		{ID: 2, UID: "issue-b", ProjectID: 8, ProjectUID: "project-b", ProjectName: "Project B", ShortID: "dup", QualifiedID: "Project B#dup", Title: "Outside query"},
		{ID: 3, UID: "issue-unique", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A", ShortID: "solo", QualifiedID: "Project A#solo", Title: "Another matching task"},
	}
	var loads atomic.Int64
	srv := setupKataSnapshotRouteServer(t, daemon, snapshot, &loads)

	request := func(query string) kataTaskReferenceResponse {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/tasks/references?q="+query+"&limit=1", nil)
		req.Header.Set(kataDaemonHeaderName, "primary")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		require.Equal(http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(rr.Header().Values("Vary"), kataDaemonHeaderName)
		var response kataTaskReferenceResponse
		require.NoError(json.Unmarshal(rr.Body.Bytes(), &response))
		return response
	}
	first := request("matching")
	second := request("another")

	assert.Equal(int64(1), loads.Load())
	require.Len(first.References, 1)
	assert.Equal("Project A#dup", first.References[0].Reference)
	require.Len(second.References, 1)
	assert.Equal("solo", second.References[0].Reference)
	assert.Equal(first.Generation, second.Generation)
}

func TestKataTaskSnapshotKeepsHistoryCursorAndBoundFailuresLocalOverHTTP(t *testing.T) {
	tests := []struct {
		name        string
		eventsBody  func() any
		wantMessage string
	}{
		{
			name: "cursor mismatch",
			eventsBody: func() any {
				selectedUID := "issue-member"
				return testKataPollEventsResponse(2, testKataEvent(1, &selectedUID, time.Now().UTC())).JSON200
			},
			wantMessage: "Could not load selected task history.",
		},
		{
			name: "bounded scan exhaustion",
			eventsBody: func() any {
				events := testKataEventPage(1, kataSnapshotHistoryScanEventLimit+1, nil)
				return testKataPollEventsResponse(events[len(events)-1].EventID, events...).JSON200
			},
			wantMessage: "Selected task history exceeded the bounded scan limit.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/issues/issue-member":
					assert.NoError(json.NewEncoder(w).Encode(testKataShowIssueResponse("issue-member").JSON200))
				case "/api/v1/events":
					assert.NoError(json.NewEncoder(w).Encode(test.eventsBody()))
				default:
					assert.Fail("unexpected Kata request", r.URL.String())
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			daemon := kata.Daemon{ID: "primary", URL: upstream.URL}
			snapshot := testKataCoordinatedAuthority().Snapshot
			snapshot.FetchedAt = time.Date(2026, 7, 20, 21, 0, 0, 0, time.UTC)
			srv := setupKataSnapshotRouteServer(t, daemon, snapshot, nil)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/tasks/snapshot?selected_issue_uid=issue-member", nil)
			req.Header.Set(kataDaemonHeaderName, "primary")
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)

			require.Equal(http.StatusOK, rr.Code, rr.Body.String())
			var response kataTaskSnapshotResponse
			require.NoError(json.Unmarshal(rr.Body.Bytes(), &response))
			assert.NotNil(response.Enrichment.SelectedDetail)
			assert.Empty(response.Enrichment.SelectedHistory)
			assert.Equal(kataSnapshotEnrichmentError{Code: CodeUpstreamError, Message: test.wantMessage}, response.Enrichment.Errors[kataSnapshotEnrichmentStageHistory])
		})
	}
}

func TestKataTaskSnapshotDoesNotAuthorizeDisconnectedGraphNodeOverHTTP(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/projects/7/issues/issue-source/graph" {
			assert.Fail("disconnected selection made unexpected request", r.URL.String())
			http.NotFound(w, r)
			return
		}
		response := testKataGraphResponse("issue-source", "issue-linked")
		response.JSON200.Edges = nil
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(json.NewEncoder(w).Encode(response.JSON200))
	}))
	defer upstream.Close()
	daemon := kata.Daemon{ID: "primary", URL: upstream.URL}
	snapshot := testKataCoordinatedAuthority().Snapshot
	snapshot.FetchedAt = time.Date(2026, 7, 20, 21, 15, 0, 0, time.UTC)
	srv := setupKataSnapshotRouteServer(t, daemon, snapshot, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/tasks/snapshot?selected_issue_uid=issue-linked&graph_source_uid=issue-source", nil)
	req.Header.Set(kataDaemonHeaderName, "primary")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var response kataTaskSnapshotResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &response))
	assert.NotNil(response.Enrichment.Graph)
	assert.Empty(response.Enrichment.SelectedIssueUID)
	assert.Nil(response.Enrichment.SelectedDetail)
	assert.Empty(response.Enrichment.Errors)
}

func setupKataSnapshotRouteServer(
	t *testing.T,
	daemon kata.Daemon,
	snapshot kataAuthoritySnapshot,
	loads *atomic.Int64,
) *Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
active_daemon = "`+daemon.ID+`"

[[daemon]]
name = "`+daemon.ID+`"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)
	srv.kataSnapshots = newKataSnapshotCoordinator(t.Context(), kataSnapshotCoordinatorDeps{
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) { return daemon, nil },
		newLoader: func(context.Context, kata.Daemon) (kataAuthoritySnapshotLoader, error) {
			return kataAuthoritySnapshotLoaderFunc(func(context.Context, kataAuthorityRequest) (kataAuthoritySnapshot, error) {
				if loads != nil {
					loads.Add(1)
				}
				return snapshot, nil
			}), nil
		},
		newServerInstanceID: func() string { return "server-a" },
	})
	eventRoot, cancelEvents := context.WithCancel(t.Context())
	cancelEvents()
	srv.kataEvents = newKataFrontendEventRegistry(eventRoot, kataFrontendEventRegistryDeps{
		invalidate:       srv.kataSnapshots.invalidateDaemon,
		daemonEpoch:      srv.kataSnapshots.daemonEpoch,
		serverInstanceID: "server-a",
		generationSeed:   func(string) uint64 { return 100 },
	})
	return srv
}

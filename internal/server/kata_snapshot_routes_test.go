package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	katagenerated "go.kenn.io/kata/pkg/client/generated"

	"go.kenn.io/middleman/internal/kata"
	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/server/kataapi"
)

func decodeProblem(t *testing.T, rr *httptest.ResponseRecorder) httpapi.ProblemError {
	t.Helper()
	var body httpapi.ProblemError
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	return body
}

func writeKataServerCatalog(t *testing.T, home, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(
		filepath.Join(home, "config.toml"), []byte(body), 0o600,
	))
}

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
	req.Header.Set(kataapi.DaemonHeaderName, "primary")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	assert.Contains(rr.Header().Values("Vary"), kataapi.DaemonHeaderName)
	problem := decodeProblem(t, rr)
	assert.Equal(httpapi.CodeServiceUnavailable, problem.Code)
}

func TestKataTaskReferencesReturnsServiceUnavailableWhenEventRegistryIsClosed(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/tasks/references", nil)
	req.Header.Set(kataapi.DaemonHeaderName, "primary")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	assert.Contains(rr.Header().Values("Vary"), kataapi.DaemonHeaderName)
	problem := decodeProblem(t, rr)
	assert.Equal(httpapi.CodeServiceUnavailable, problem.Code)
}

func TestKataTaskSnapshotSerializesAuthorityAndReplayableCursor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	snapshot := testKataCoordinatedAuthority().Snapshot
	snapshot.FetchedAt = time.Date(2026, 7, 20, 20, 30, 0, 0, time.UTC)
	srv := setupKataSnapshotRouteServer(t, daemon, snapshot, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/tasks/snapshot?scope=project&project_uid=project-a&authority=ready&selected_issue_uid=not-a-member&graph_source_uid=not-a-member", nil)
	req.Header.Set(kataapi.DaemonHeaderName, "primary")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(rr.Header().Values("Vary"), kataapi.DaemonHeaderName)
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
	req.Header.Set(kataapi.DaemonHeaderName, "primary")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var response kataTaskSnapshotResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &response))
	assert.Equal("issue-source", response.GraphSourceUID)
	assert.Equal("issue-member", response.Enrichment.SelectedIssueUID)
	assert.Nil(response.Enrichment.SelectedDetail)
	assert.Nil(response.Enrichment.Graph)
	assert.Equal(kataSnapshotEnrichmentError{Code: httpapi.CodeUpstreamError, Message: "Could not load selected task detail."}, response.Enrichment.Errors[kataSnapshotEnrichmentStageDetail])
	assert.Equal(kataSnapshotEnrichmentError{Code: httpapi.CodeUpstreamError, Message: "Could not load selected task history."}, response.Enrichment.Errors[kataSnapshotEnrichmentStageHistory])
	assert.Equal(kataSnapshotEnrichmentError{Code: httpapi.CodeUpstreamError, Message: "Could not load reachable graph."}, response.Enrichment.Errors[kataSnapshotEnrichmentStageGraph])
	assert.True(slices.Contains(paths, "/api/v1/issues/issue-member"))
	assert.True(slices.Contains(paths, "/api/v1/projects/7/events"))
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
		req.Header.Set(kataapi.DaemonHeaderName, "primary")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		require.Equal(http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(rr.Header().Values("Vary"), kataapi.DaemonHeaderName)
		var response kataTaskReferenceResponse
		require.NoError(json.Unmarshal(rr.Body.Bytes(), &response))
		var wire map[string]json.RawMessage
		require.NoError(json.Unmarshal(rr.Body.Bytes(), &wire))
		assert.NotContains(wire, "event_cursor")
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

func TestKataTaskReferencesFirstRequestStartsSupervisorAndInvalidatesCache(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
active_daemon = "primary"

[[daemon]]
name = "primary"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)
	snapshot := testKataCoordinatedAuthority().Snapshot
	var loads atomic.Int64
	srv.kataSnapshots = newKataSnapshotCoordinator(t.Context(), kataSnapshotCoordinatorDeps{
		resolveDaemon: func(string) (kata.Daemon, *httpapi.ProblemError) { return daemon, nil },
		newLoader: func(context.Context, kata.Daemon) (kataAuthoritySnapshotLoader, error) {
			return kataAuthoritySnapshotLoaderFunc(func(context.Context, kataAuthorityRequest) (kataAuthoritySnapshot, error) {
				loads.Add(1)
				return snapshot, nil
			}), nil
		},
		newServerInstanceID: func() string { return "server-a" },
	})
	pollStarted := make(chan struct{})
	releaseEvent := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseEvent) }) })
	var pollCalls atomic.Int64
	srv.kataEvents = newKataFrontendEventRegistry(t.Context(), kataFrontendEventRegistryDeps{
		newClient: func(context.Context, kata.Daemon) (kataAPIClient, error) {
			return &fakeKataFrontendEventClient{
				poll: func(ctx context.Context, options *katagenerated.PollEventsRequestOptions) (*katagenerated.PollEventsResp, error) {
					call := pollCalls.Add(1)
					afterID := *options.Query.AfterID
					if call == 1 {
						close(pollStarted)
						select {
						case <-releaseEvent:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
						event := testKataEventEnvelope(1)
						return &katagenerated.PollEventsResp{StatusCode: http.StatusOK, JSON200: &katagenerated.PollEventsBody{
							Events: []katagenerated.EventEnvelope{event}, NextAfterID: 1,
						}}, nil
					}
					return &katagenerated.PollEventsResp{StatusCode: http.StatusOK, JSON200: &katagenerated.PollEventsBody{
						NextAfterID: afterID,
					}}, nil
				},
				stream: func(ctx context.Context, _ *katagenerated.StreamEventsRequestOptions) (*http.Response, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}, nil
		},
		invalidate:       srv.kataSnapshots.invalidateDaemon,
		daemonEpoch:      srv.kataSnapshots.daemonEpoch,
		serverInstanceID: "server-a",
		coalesceWindow:   time.Millisecond,
		retryDelay:       time.Hour,
	})

	request := func() kataTaskReferenceResponse {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/tasks/references", nil)
		req.Header.Set(kataapi.DaemonHeaderName, "primary")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		require.Equal(http.StatusOK, rr.Code, rr.Body.String())
		var response kataTaskReferenceResponse
		require.NoError(json.Unmarshal(rr.Body.Bytes(), &response))
		return response
	}

	first := request()
	select {
	case <-pollStarted:
	case <-time.After(time.Second):
		require.FailNow("first reference request did not start the Kata event supervisor")
	}
	assert.Zero(first.InvalidationEpoch)
	releaseOnce.Do(func() { close(releaseEvent) })
	require.Eventually(func() bool {
		return srv.kataSnapshots.daemonEpoch("primary") == 1
	}, time.Second, time.Millisecond)
	second := request()

	assert.Equal(int64(2), loads.Load(), "the upstream event must invalidate the first cached authority")
	assert.Equal(uint64(1), second.InvalidationEpoch)
	assert.GreaterOrEqual(pollCalls.Load(), int64(2))
}

func TestKataTaskSnapshotKeepsHistoryCursorFailureLocalOverHTTP(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	selectedUID := "issue-member"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/issues/issue-member":
			assert.NoError(json.NewEncoder(w).Encode(testKataShowIssueResponse(selectedUID).JSON200))
		case "/api/v1/projects/7/events":
			assert.Equal("0", r.URL.Query().Get("after_id"))
			assert.NoError(json.NewEncoder(w).Encode(testKataPollProjectEventsResponse(2, testKataEvent(1, &selectedUID, time.Now().UTC())).JSON200))
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
	req.Header.Set(kataapi.DaemonHeaderName, "primary")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var response kataTaskSnapshotResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &response))
	assert.NotNil(response.Enrichment.SelectedDetail)
	assert.Empty(response.Enrichment.SelectedHistory)
	assert.Equal(kataSnapshotEnrichmentError{Code: httpapi.CodeUpstreamError, Message: "Could not load selected task history."}, response.Enrichment.Errors[kataSnapshotEnrichmentStageHistory])
}

func TestKataTaskSnapshotLoadsCompleteRetainedProjectHistoryOverHTTP(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	selectedUID := "issue-member"
	otherUID := "issue-other"
	cursors := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/issues/issue-member":
			assert.NoError(json.NewEncoder(w).Encode(testKataShowIssueResponse(selectedUID).JSON200))
		case "/api/v1/projects/7/events":
			assert.Equal("1000", r.URL.Query().Get("limit"))
			cursors = append(cursors, r.URL.Query().Get("after_id"))
			var body *katagenerated.PollEventsBody
			switch len(cursors) {
			case 1:
				body = testKataPollProjectEventsResponse(2,
					testKataEvent(1, &otherUID, time.Unix(1, 0)),
					testKataEvent(2, &selectedUID, time.Unix(2, 0))).JSON200
			case 2:
				events := testKataEventPage(3, 125, &selectedUID)
				body = testKataPollProjectEventsResponse(127, events...).JSON200
			default:
				body = testKataPollProjectEventsResponse(127).JSON200
			}
			assert.NoError(json.NewEncoder(w).Encode(body))
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
	req.Header.Set(kataapi.DaemonHeaderName, "primary")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var response kataTaskSnapshotResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &response))
	assert.Len(response.Enrichment.SelectedHistory, 126)
	assert.Equal([]string{"0", "2", "127"}, cursors)
	assert.NotContains(response.Enrichment.Errors, kataSnapshotEnrichmentStageHistory)
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
	req.Header.Set(kataapi.DaemonHeaderName, "primary")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var response kataTaskSnapshotResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &response))
	assert.Equal("issue-source", response.GraphSourceUID)
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
		resolveDaemon: func(string) (kata.Daemon, *httpapi.ProblemError) { return daemon, nil },
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

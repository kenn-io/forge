package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	katagenerated "go.kenn.io/kata/pkg/client/generated"
	"go.kenn.io/middleman/internal/kata"
)

type fakeKataFrontendEventHandle struct {
	fingerprint string
	cursor      func() uint64
}

func (h fakeKataFrontendEventHandle) DaemonFingerprint() string {
	return h.fingerprint
}

func (h fakeKataFrontendEventHandle) Cursor() uint64 {
	if h.cursor == nil {
		return 0
	}
	return h.cursor()
}

func TestKataSnapshotFrontendEnsuresEventsBeforeBlockedAuthorityLoad(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	fingerprint := kataDaemonTargetFingerprint(daemon)
	var ensured atomic.Bool
	var enriched atomic.Bool
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	frontend := newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) { return daemon, nil },
		ensureEvents: func(got kata.Daemon) (kataFrontendEventHandle, error) {
			assert.Equal(daemon, got)
			ensured.Store(true)
			return fakeKataFrontendEventHandle{
				fingerprint: fingerprint,
				cursor: func() uint64 {
					assert.True(enriched.Load(), "cursor must be captured after enrichment")
					return 41
				},
			}, nil
		},
		loadAuthority: func(_ context.Context, daemonID string, intent kataAuthorityRequest) (kataCoordinatedAuthority, error) {
			assert.True(ensured.Load(), "event binding must exist before authority loading starts")
			assert.Equal("primary", daemonID)
			assert.Equal(kataAuthorityRequest{Scope: "global", Authority: "open"}, intent)
			close(loadStarted)
			<-releaseLoad
			return testKataSnapshotFrontendAuthority(daemon, 3, 7), nil
		},
		daemonEpoch: func(string) uint64 { return 3 },
		newClient: func(context.Context, kata.Daemon) (kataAPIClient, error) {
			return &fakeKataSnapshotAPIClient{}, nil
		},
		enrich: func(_ context.Context, _ kataAPIClient, _ kataCoordinatedAuthority, request kataSnapshotEnrichmentRequest) (kataSnapshotEnrichment, error) {
			assert.Equal(kataSnapshotEnrichmentRequest{SelectedIssueUID: "issue-member", GraphSourceUID: "issue-source"}, request)
			enriched.Store(true)
			return kataSnapshotEnrichment{SelectedIssueUID: "issue-member"}, nil
		},
	})

	type result struct {
		response kataTaskSnapshotResponse
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		response, err := frontend.Snapshot(t.Context(), &kataTaskSnapshotInput{
			DaemonID: "primary", SelectedIssueUID: " issue-member ", GraphSourceUID: " issue-source ",
		})
		resultCh <- result{response: response, err: err}
	}()
	select {
	case early := <-resultCh:
		require.FailNow("snapshot returned before authority load", "error: %v", early.err)
	case <-loadStarted:
	}
	assert.True(ensured.Load())
	close(releaseLoad)
	got := <-resultCh

	require.NoError(got.err)
	assert.Equal("server-a", got.response.ServerInstanceID)
	assert.Equal("primary", got.response.DaemonID)
	assert.Equal(kataAuthorityRequest{Scope: "global", Authority: "open"}, got.response.Intent)
	assert.Equal(uint64(7), got.response.Generation)
	assert.Equal(uint64(3), got.response.InvalidationEpoch)
	assert.Equal(uint64(41), got.response.EventCursor)
	assert.Equal(testKataSnapshotFrontendFetchedAt, got.response.FetchedAt)
	assert.Len(got.response.Projects, 1)
	assert.Equal([]string{"issue-source", "issue-member"}, got.response.MemberIssueUIDs)
	assert.Len(got.response.Issues, 2)
	assert.Equal("issue-member", got.response.Enrichment.SelectedIssueUID)
}

func TestKataSnapshotFrontendReusesSelectedEnrichmentWithinDaemonEpoch(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	authority := testKataSnapshotFrontendAuthority(daemon, 3, 7)
	cache := newKataSnapshotEnrichmentCacheWithConfig(time.Minute, 8)
	t.Cleanup(cache.close)
	var detailCalls atomic.Int64
	var eventCalls atomic.Int64
	client := &fakeKataSnapshotAPIClient{
		showIssue: func(_ context.Context, options *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
			detailCalls.Add(1)
			uid := options.PathParams.UID
			response := testKataShowIssueResponse(uid)
			if uid == "issue-source" {
				response.JSON200.Issue.ID = 1
				response.JSON200.Issue.ShortID = "source"
				response.JSON200.Issue.Title = "Source task"
			}
			return response, nil
		},
		pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
			eventCalls.Add(1)
			afterID := *options.Query.AfterID
			if afterID == 0 {
				sourceUID := "issue-source"
				memberUID := "issue-member"
				return testKataPollProjectEventsResponse(2,
					testKataEvent(1, &sourceUID, time.Unix(1, 0)),
					testKataEvent(2, &memberUID, time.Unix(2, 0)),
				), nil
			}
			return testKataPollProjectEventsResponse(afterID), nil
		},
	}
	frontend := newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) { return daemon, nil },
		ensureEvents: func(kata.Daemon) (kataFrontendEventHandle, error) {
			return fakeKataFrontendEventHandle{fingerprint: kataDaemonTargetFingerprint(daemon)}, nil
		},
		loadAuthority: func(context.Context, string, kataAuthorityRequest) (kataCoordinatedAuthority, error) {
			return authority, nil
		},
		daemonEpoch: func(string) uint64 { return authority.InvalidationEpoch },
		newClient:   func(context.Context, kata.Daemon) (kataAPIClient, error) { return client, nil },
		enrich: func(ctx context.Context, client kataAPIClient, authority kataCoordinatedAuthority, request kataSnapshotEnrichmentRequest) (kataSnapshotEnrichment, error) {
			return newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client, cache: cache}).Enrich(ctx, authority, request)
		},
	})

	first, err := frontend.Snapshot(t.Context(), &kataTaskSnapshotInput{DaemonID: daemon.ID, SelectedIssueUID: "issue-member"})
	require.NoError(err)
	second, err := frontend.Snapshot(t.Context(), &kataTaskSnapshotInput{DaemonID: daemon.ID, SelectedIssueUID: "issue-member"})
	require.NoError(err)
	third, err := frontend.Snapshot(t.Context(), &kataTaskSnapshotInput{DaemonID: daemon.ID, SelectedIssueUID: "issue-source"})
	require.NoError(err)

	require.Len(first.Enrichment.SelectedHistory, 1)
	require.Len(second.Enrichment.SelectedHistory, 1)
	require.Len(third.Enrichment.SelectedHistory, 1)
	assert.Equal(int64(2), detailCalls.Load(), "each selected issue has its own detail cache key")
	assert.Equal(int64(2), eventCalls.Load(), "both issues share one complete project-history pagination sequence")
	assert.Equal(int64(2), first.Enrichment.SelectedHistory[0].EventID)
	assert.Equal(int64(1), third.Enrichment.SelectedHistory[0].EventID)
}

func TestKataTaskReferencesPrioritizeExactIdentifiersBeforeCappedSubstringMatches(t *testing.T) {
	t.Parallel()
	authority := testKataCoordinatedAuthority()
	authority.Snapshot.MemberIssueUIDs = []string{"issue-partial", "issue-exact"}
	authority.Snapshot.Issues = []kataTaskSummary{
		{UID: "issue-partial", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A", ShortID: "other", QualifiedID: "Project A#other", Title: "Needle appears in this title"},
		{UID: "issue-exact", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A", ShortID: "needle", QualifiedID: "Project A#needle", Title: "Exact task"},
	}

	response := kataTaskReferencesFromAuthority(authority, "needle", 1)

	require.Len(t, response.References, 1)
	assert.Equal(t, "issue-exact", response.References[0].UID)
}

func TestKataSnapshotFrontendRetriesInvalidationBeforePublish(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	fingerprint := kataDaemonTargetFingerprint(daemon)
	var epoch atomic.Uint64
	var loads atomic.Int64
	var enrichments atomic.Int64
	frontend := newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) { return daemon, nil },
		ensureEvents: func(kata.Daemon) (kataFrontendEventHandle, error) {
			return fakeKataFrontendEventHandle{fingerprint: fingerprint, cursor: func() uint64 { return 90 + epoch.Load() }}, nil
		},
		loadAuthority: func(context.Context, string, kataAuthorityRequest) (kataCoordinatedAuthority, error) {
			return testKataSnapshotFrontendAuthority(daemon, epoch.Load(), uint64(loads.Add(1))), nil
		},
		daemonEpoch: func(string) uint64 { return epoch.Load() },
		newClient: func(context.Context, kata.Daemon) (kataAPIClient, error) {
			return &fakeKataSnapshotAPIClient{}, nil
		},
		enrich: func(context.Context, kataAPIClient, kataCoordinatedAuthority, kataSnapshotEnrichmentRequest) (kataSnapshotEnrichment, error) {
			if enrichments.Add(1) == 1 {
				epoch.Add(1)
			}
			return kataSnapshotEnrichment{}, nil
		},
	})

	response, err := frontend.Snapshot(t.Context(), &kataTaskSnapshotInput{DaemonID: "primary", Scope: "global", Authority: "open"})

	require.NoError(t, err)
	assert.Equal(int64(2), loads.Load())
	assert.Equal(int64(2), enrichments.Load())
	assert.Equal(uint64(1), response.InvalidationEpoch)
	assert.Equal(uint64(91), response.EventCursor)
}

func TestKataSnapshotFrontendRetriesTargetRotationDuringEnrichment(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	first := kata.Daemon{ID: "primary", URL: "https://first.example.test"}
	second := kata.Daemon{ID: "primary", URL: "https://second.example.test"}
	current := first
	var loads int
	var enrichments int
	var clientURLs []string
	frontend := newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) { return current, nil },
		ensureEvents: func(daemon kata.Daemon) (kataFrontendEventHandle, error) {
			cursor := uint64(11)
			if daemon.URL == second.URL {
				cursor = 22
			}
			return fakeKataFrontendEventHandle{fingerprint: kataDaemonTargetFingerprint(daemon), cursor: func() uint64 { return cursor }}, nil
		},
		loadAuthority: func(context.Context, string, kataAuthorityRequest) (kataCoordinatedAuthority, error) {
			loads++
			return testKataSnapshotFrontendAuthority(current, 0, uint64(loads)), nil
		},
		daemonEpoch: func(string) uint64 { return 0 },
		newClient: func(_ context.Context, daemon kata.Daemon) (kataAPIClient, error) {
			clientURLs = append(clientURLs, daemon.URL)
			return &fakeKataSnapshotAPIClient{}, nil
		},
		enrich: func(context.Context, kataAPIClient, kataCoordinatedAuthority, kataSnapshotEnrichmentRequest) (kataSnapshotEnrichment, error) {
			enrichments++
			if enrichments == 1 {
				current = second
			}
			return kataSnapshotEnrichment{}, nil
		},
	})

	response, err := frontend.Snapshot(t.Context(), &kataTaskSnapshotInput{DaemonID: "primary", Scope: "global", Authority: "open"})

	require.NoError(t, err)
	assert.Equal(2, loads)
	assert.Equal(2, enrichments)
	assert.Equal([]string{first.URL, second.URL}, clientURLs)
	assert.Equal(uint64(22), response.EventCursor)
}

func TestKataSnapshotFrontendStopsAfterTwoDeliveryAttempts(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	fingerprint := kataDaemonTargetFingerprint(daemon)
	var loads atomic.Int64
	frontend := newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) { return daemon, nil },
		ensureEvents: func(kata.Daemon) (kataFrontendEventHandle, error) {
			return fakeKataFrontendEventHandle{fingerprint: fingerprint, cursor: func() uint64 { return 1 }}, nil
		},
		loadAuthority: func(context.Context, string, kataAuthorityRequest) (kataCoordinatedAuthority, error) {
			loads.Add(1)
			return testKataSnapshotFrontendAuthority(daemon, 0, 1), nil
		},
		daemonEpoch: func(string) uint64 { return 1 },
		newClient: func(context.Context, kata.Daemon) (kataAPIClient, error) {
			return &fakeKataSnapshotAPIClient{}, nil
		},
		enrich: func(context.Context, kataAPIClient, kataCoordinatedAuthority, kataSnapshotEnrichmentRequest) (kataSnapshotEnrichment, error) {
			return kataSnapshotEnrichment{}, nil
		},
	})

	_, err := frontend.Snapshot(t.Context(), &kataTaskSnapshotInput{DaemonID: "primary", Scope: "global", Authority: "open"})

	require.Error(t, err)
	assert.Equal(int64(kataSnapshotDeliveryAttempts), loads.Load())
}

func TestKataSnapshotFrontendDoesNotPublishAfterCancellationDuringEnrichment(t *testing.T) {
	t.Parallel()

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	fingerprint := kataDaemonTargetFingerprint(daemon)
	ctx, cancel := context.WithCancel(t.Context())
	frontend := newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) { return daemon, nil },
		ensureEvents: func(kata.Daemon) (kataFrontendEventHandle, error) {
			return fakeKataFrontendEventHandle{fingerprint: fingerprint, cursor: func() uint64 { return 1 }}, nil
		},
		loadAuthority: func(context.Context, string, kataAuthorityRequest) (kataCoordinatedAuthority, error) {
			return testKataSnapshotFrontendAuthority(daemon, 0, 1), nil
		},
		daemonEpoch: func(string) uint64 { return 0 },
		newClient: func(context.Context, kata.Daemon) (kataAPIClient, error) {
			return &fakeKataSnapshotAPIClient{}, nil
		},
		enrich: func(context.Context, kataAPIClient, kataCoordinatedAuthority, kataSnapshotEnrichmentRequest) (kataSnapshotEnrichment, error) {
			cancel()
			return kataSnapshotEnrichment{}, nil
		},
	})

	_, err := frontend.Snapshot(ctx, &kataTaskSnapshotInput{DaemonID: "primary", Scope: "global", Authority: "open"})

	require.ErrorIs(t, err, context.Canceled)
}

func TestKataSnapshotFrontendReferencesUseGlobalOpenAuthorityAndFullSetUniqueness(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	authority := testKataSnapshotFrontendAuthority(daemon, 4, 8)
	authority.Snapshot.MemberIssueUIDs = []string{"issue-a", "issue-b", "issue-unique"}
	authority.Snapshot.Issues = []kataTaskSummary{
		{ID: 1, UID: "issue-a", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A", ShortID: "dup", QualifiedID: "Project A#dup", Title: "Matching task"},
		{ID: 2, UID: "issue-b", ProjectID: 8, ProjectUID: "project-b", ProjectName: "Project B", ShortID: "dup", QualifiedID: "Project B#dup", Title: "Outside query"},
		{ID: 3, UID: "issue-unique", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A", ShortID: "solo", QualifiedID: "Project A#solo", Title: "Another matching task"},
		{ID: 4, UID: "issue-nonmember", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A", ShortID: "hidden", QualifiedID: "Project A#hidden", Title: "Matching hidden task"},
	}
	var loads atomic.Int64
	frontend := newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) { return daemon, nil },
		ensureEvents: func(kata.Daemon) (kataFrontendEventHandle, error) {
			return fakeKataFrontendEventHandle{fingerprint: kataDaemonTargetFingerprint(daemon)}, nil
		},
		loadAuthority: func(_ context.Context, daemonID string, intent kataAuthorityRequest) (kataCoordinatedAuthority, error) {
			loads.Add(1)
			assert.Equal("primary", daemonID)
			assert.Equal(kataAuthorityRequest{Scope: "global", Authority: "open"}, intent)
			return authority, nil
		},
		daemonEpoch: func(string) uint64 { return 4 },
	})

	response, err := frontend.References(t.Context(), &kataTaskReferenceInput{DaemonID: "primary", Query: " matching ", Limit: 1})

	require.NoError(err)
	assert.Equal(int64(1), loads.Load())
	assert.Equal("server-a", response.ServerInstanceID)
	assert.Equal("primary", response.DaemonID)
	assert.Equal(uint64(8), response.Generation)
	assert.Equal(uint64(4), response.InvalidationEpoch)
	assert.Equal(testKataSnapshotFrontendFetchedAt, response.FetchedAt)
	require.Len(response.References, 1)
	assert.Equal(kataTaskReference{
		UID: "issue-a", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A",
		ShortID: "dup", QualifiedID: "Project A#dup", Title: "Matching task", Reference: "Project A#dup",
	}, response.References[0])
}

func TestKataSnapshotFrontendReferencesEnsuresEventsBeforeBlockedAuthorityLoad(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	var ensured atomic.Bool
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	frontend := newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) { return daemon, nil },
		ensureEvents: func(got kata.Daemon) (kataFrontendEventHandle, error) {
			assert.Equal(daemon, got)
			ensured.Store(true)
			return fakeKataFrontendEventHandle{fingerprint: kataDaemonTargetFingerprint(daemon)}, nil
		},
		loadAuthority: func(_ context.Context, daemonID string, intent kataAuthorityRequest) (kataCoordinatedAuthority, error) {
			assert.True(ensured.Load(), "event binding must exist before authority loading starts")
			assert.Equal("primary", daemonID)
			assert.Equal(kataAuthorityRequest{Scope: "global", Authority: "open"}, intent)
			close(loadStarted)
			<-releaseLoad
			return testKataSnapshotFrontendAuthority(daemon, 3, 7), nil
		},
		daemonEpoch: func(string) uint64 { return 3 },
	})

	type result struct {
		response kataTaskReferenceResponse
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		response, err := frontend.References(t.Context(), &kataTaskReferenceInput{DaemonID: "primary"})
		resultCh <- result{response: response, err: err}
	}()
	select {
	case early := <-resultCh:
		require.FailNow("references returned before authority load", "error: %v", early.err)
	case <-loadStarted:
	}
	assert.True(ensured.Load())
	close(releaseLoad)
	got := <-resultCh

	require.NoError(got.err)
	assert.Equal("primary", got.response.DaemonID)
	assert.Equal(uint64(3), got.response.InvalidationEpoch)
}

func TestKataSnapshotFrontendReferencesRetriesInvalidationBeforeReturn(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	fingerprint := kataDaemonTargetFingerprint(daemon)
	var loads atomic.Int64
	frontend := newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) { return daemon, nil },
		ensureEvents: func(kata.Daemon) (kataFrontendEventHandle, error) {
			return fakeKataFrontendEventHandle{fingerprint: fingerprint}, nil
		},
		loadAuthority: func(context.Context, string, kataAuthorityRequest) (kataCoordinatedAuthority, error) {
			load := loads.Add(1)
			return testKataSnapshotFrontendAuthority(daemon, uint64(load-1), uint64(load)), nil
		},
		daemonEpoch: func(string) uint64 { return 1 },
	})

	response, err := frontend.References(t.Context(), &kataTaskReferenceInput{DaemonID: "primary"})

	require.NoError(t, err)
	assert.Equal(int64(2), loads.Load())
	assert.Equal(uint64(1), response.InvalidationEpoch)
}

func TestKataSnapshotFrontendReferencesRetriesTargetRotationBeforeReturn(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	first := kata.Daemon{ID: "primary", URL: "https://first.example.test"}
	second := kata.Daemon{ID: "primary", URL: "https://second.example.test"}
	current := first
	var loads int
	var ensuredURLs []string
	frontend := newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) { return current, nil },
		ensureEvents: func(daemon kata.Daemon) (kataFrontendEventHandle, error) {
			ensuredURLs = append(ensuredURLs, daemon.URL)
			return fakeKataFrontendEventHandle{fingerprint: kataDaemonTargetFingerprint(daemon)}, nil
		},
		loadAuthority: func(context.Context, string, kataAuthorityRequest) (kataCoordinatedAuthority, error) {
			loads++
			authority := testKataSnapshotFrontendAuthority(current, 0, uint64(loads))
			if loads == 1 {
				current = second
			}
			return authority, nil
		},
		daemonEpoch: func(string) uint64 { return 0 },
	})

	response, err := frontend.References(t.Context(), &kataTaskReferenceInput{DaemonID: "primary"})

	require.NoError(t, err)
	assert.Equal(2, loads)
	assert.Equal([]string{first.URL, second.URL}, ensuredURLs)
	assert.Equal(uint64(2), response.Generation)
}

func TestKataSnapshotFrontendReferencesStopsAfterTwoDeliveryAttempts(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	fingerprint := kataDaemonTargetFingerprint(daemon)
	var loads atomic.Int64
	frontend := newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) { return daemon, nil },
		ensureEvents: func(kata.Daemon) (kataFrontendEventHandle, error) {
			return fakeKataFrontendEventHandle{fingerprint: fingerprint}, nil
		},
		loadAuthority: func(context.Context, string, kataAuthorityRequest) (kataCoordinatedAuthority, error) {
			loads.Add(1)
			return testKataSnapshotFrontendAuthority(daemon, 0, 1), nil
		},
		daemonEpoch: func(string) uint64 { return 1 },
	})

	_, err := frontend.References(t.Context(), &kataTaskReferenceInput{DaemonID: "primary"})

	require.Error(err)
	problem, ok := err.(*ProblemError)
	require.True(ok, "want *ProblemError, got %T", err)
	assert.Equal(CodeUpstreamError, problem.Code)
	assert.Contains(problem.Detail, "deliver consistent snapshot")
	assert.Equal(int64(kataSnapshotDeliveryAttempts), loads.Load())
}

func TestKataSnapshotFrontendReferencesPreservesCancellationAfterAuthorityLoad(t *testing.T) {
	t.Parallel()

	daemon := kata.Daemon{ID: "primary", URL: "https://kata.example.test"}
	ctx, cancel := context.WithCancel(t.Context())
	frontend := newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		resolveDaemon: func(string) (kata.Daemon, *ProblemError) { return daemon, nil },
		ensureEvents: func(kata.Daemon) (kataFrontendEventHandle, error) {
			return fakeKataFrontendEventHandle{fingerprint: kataDaemonTargetFingerprint(daemon)}, nil
		},
		loadAuthority: func(context.Context, string, kataAuthorityRequest) (kataCoordinatedAuthority, error) {
			cancel()
			return testKataSnapshotFrontendAuthority(daemon, 0, 1), nil
		},
		daemonEpoch: func(string) uint64 { return 0 },
	})

	_, err := frontend.References(ctx, &kataTaskReferenceInput{DaemonID: "primary"})

	require.ErrorIs(t, err, context.Canceled)
}

var testKataSnapshotFrontendFetchedAt = time.Date(2026, 7, 20, 20, 0, 0, 0, time.UTC)

func testKataSnapshotFrontendAuthority(daemon kata.Daemon, epoch, generation uint64) kataCoordinatedAuthority {
	authority := testKataCoordinatedAuthority()
	authority.ServerInstanceID = "server-a"
	authority.DaemonID = daemon.ID
	authority.Key = kataSnapshotKey{
		DaemonID: daemon.ID, DaemonFingerprint: kataDaemonTargetFingerprint(daemon), Scope: "global", Authority: "open",
	}
	authority.Generation = generation
	authority.InvalidationEpoch = epoch
	authority.Snapshot.Generation = generation
	authority.Snapshot.FetchedAt = testKataSnapshotFrontendFetchedAt
	return authority
}

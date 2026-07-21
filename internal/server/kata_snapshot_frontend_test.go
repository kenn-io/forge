package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

	authority := testKataSnapshotFrontendAuthority(kata.Daemon{ID: "primary", URL: "https://kata.example.test"}, 4, 8)
	authority.Snapshot.MemberIssueUIDs = []string{"issue-a", "issue-b", "issue-unique"}
	authority.Snapshot.Issues = []kataTaskSummary{
		{ID: 1, UID: "issue-a", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A", ShortID: "dup", QualifiedID: "Project A#dup", Title: "Matching task"},
		{ID: 2, UID: "issue-b", ProjectID: 8, ProjectUID: "project-b", ProjectName: "Project B", ShortID: "dup", QualifiedID: "Project B#dup", Title: "Outside query"},
		{ID: 3, UID: "issue-unique", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A", ShortID: "solo", QualifiedID: "Project A#solo", Title: "Another matching task"},
		{ID: 4, UID: "issue-nonmember", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A", ShortID: "hidden", QualifiedID: "Project A#hidden", Title: "Matching hidden task"},
	}
	var loads atomic.Int64
	frontend := newKataSnapshotFrontend(kataSnapshotFrontendDeps{
		loadAuthority: func(_ context.Context, daemonID string, intent kataAuthorityRequest) (kataCoordinatedAuthority, error) {
			loads.Add(1)
			assert.Equal("primary", daemonID)
			assert.Equal(kataAuthorityRequest{Scope: "global", Authority: "open"}, intent)
			return authority, nil
		},
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

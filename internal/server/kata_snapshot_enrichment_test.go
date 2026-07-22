package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
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

func TestKataSnapshotEnricherSkipsHistoryWithoutAuthorizedSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		selection string
	}{
		{name: "no selection"},
		{name: "direct nonmember", selection: "issue-outside"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			pollCalls := 0
			authority := testKataCoordinatedAuthority()
			authority.Snapshot.Issues = append(authority.Snapshot.Issues, kataTaskSummary{
				UID: "issue-outside", ProjectID: 7, ProjectUID: "project-a", ProjectName: "Project A",
			})
			enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: &fakeKataSnapshotAPIClient{
				pollProjectEvents: func(context.Context, *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
					pollCalls++
					return testKataPollProjectEventsResponse(0), nil
				},
			}})

			result, err := enricher.Enrich(t.Context(), authority, kataSnapshotEnrichmentRequest{SelectedIssueUID: test.selection})

			require.NoError(t, err)
			assert.Empty(result.SelectedHistory)
			assert.NotContains(result.Errors, kataSnapshotEnrichmentStageHistory)
			assert.Zero(pollCalls)
		})
	}
}

func TestKataSnapshotEnricherLoadsExactSelectedHistoryAcrossPages(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	createdAt1, err := time.Parse(time.RFC3339, "2026-07-20T09:00:00-05:00")
	require.NoError(err)
	createdAt2, err := time.Parse(time.RFC3339, "2026-07-20T10:00:00-05:00")
	require.NoError(err)
	createdAt3, err := time.Parse(time.RFC3339, "2026-07-20T11:00:00-05:00")
	require.NoError(err)
	createdAt4, err := time.Parse(time.RFC3339, "2026-07-20T12:00:00-05:00")
	require.NoError(err)
	selectedUID := "issue-member"
	otherUID := "issue-other"
	calls := 0
	client := &fakeKataSnapshotAPIClient{
		showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
			return testKataShowIssueResponse(selectedUID), nil
		},
		pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
			requireKataProjectEventsRequest(t, options)
			assert.Equal(int64(1000), *options.Query.Limit)
			calls++
			switch calls {
			case 1:
				assert.Equal(int64(0), *options.Query.AfterID)
				return testKataPollProjectEventsResponse(2,
					testKataEvent(1, &otherUID, createdAt1),
					testKataEvent(2, &selectedUID, createdAt2),
				), nil
			case 2:
				assert.Equal(int64(2), *options.Query.AfterID)
				return testKataPollProjectEventsResponse(4,
					testKataEvent(3, nil, createdAt3),
					testKataEvent(4, &selectedUID, createdAt4),
				), nil
			default:
				assert.Equal(int64(4), *options.Query.AfterID)
				return testKataPollProjectEventsResponse(4), nil
			}
		},
	}
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client})

	result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{SelectedIssueUID: selectedUID})

	require.NoError(err)
	require.Len(result.SelectedHistory, 2)
	assert.Equal([]int64{2, 4}, []int64{result.SelectedHistory[0].EventID, result.SelectedHistory[1].EventID})
	assert.Equal(time.UTC, result.SelectedHistory[0].CreatedAt.Location())
	assert.Equal(time.Date(2026, 7, 20, 15, 0, 0, 0, time.UTC), result.SelectedHistory[0].CreatedAt)
	assert.Equal(3, calls)
	assert.NotContains(result.Errors, kataSnapshotEnrichmentStageHistory)
}

func TestKataSnapshotEnricherLoadsCompleteRetainedSelectedHistoryFromProjectEvents(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	selectedUID := "issue-member"
	otherUID := "issue-other"
	cursors := []int64{}
	client := &fakeKataSnapshotAPIClient{
		showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
			return testKataShowIssueResponse(selectedUID), nil
		},
		pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
			require.Equal(t, int64(7), options.PathParams.ProjectID)
			cursors = append(cursors, *options.Query.AfterID)
			switch len(cursors) {
			case 1:
				return testKataPollProjectEventsResponse(2,
					testKataEvent(1, &otherUID, time.Unix(1, 0)),
					testKataEvent(2, &selectedUID, time.Unix(2, 0))), nil
			case 2:
				events := testKataEventPage(3, 125, &selectedUID)
				return testKataPollProjectEventsResponse(127, events...), nil
			default:
				return testKataPollProjectEventsResponse(127), nil
			}
		},
	}

	result, err := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client}).Enrich(
		t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{SelectedIssueUID: selectedUID},
	)

	require.NoError(t, err)
	assert.Len(result.SelectedHistory, 126)
	assert.Equal([]int64{0, 2, 127}, cursors)
	assert.NotContains(result.Errors, kataSnapshotEnrichmentStageHistory)
}

func TestKataSnapshotEnricherDiscardsOversizedProjectAccumulatorAndReloadsSelectedHistory(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	const maxBytes = uint64(512)
	selectedUID := "issue-member"
	otherUID := "issue-other"
	var calls atomic.Int64
	client := &fakeKataSnapshotAPIClient{
		pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
			requireKataProjectEventsRequest(t, options)
			switch (calls.Add(1) - 1) % 3 {
			case 0:
				unrelated := testKataEvent(1, &otherUID, time.Unix(1, 0))
				unrelated.ContentHash = strings.Repeat("x", int(maxBytes))
				return testKataPollProjectEventsResponse(2,
					unrelated,
					testKataEvent(2, &selectedUID, time.Unix(2, 0)),
				), nil
			case 1:
				return testKataPollProjectEventsResponse(3,
					testKataEvent(3, &selectedUID, time.Unix(3, 0)),
				), nil
			default:
				return testKataPollProjectEventsResponse(3), nil
			}
		},
	}
	cache := newKataSnapshotEnrichmentCacheWithLimits(
		t.Context(), time.Minute, 8, maxBytes, func(string) uint64 { return 0 },
	)
	t.Cleanup(cache.close)
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client, cache: cache})

	loaded, err := enricher.loadProjectEvents(t.Context(), 7, selectedUID, maxBytes)
	require.NoError(err)
	assert.False(loaded.Cacheable)
	assert.Nil(loaded.ProjectEvents)
	require.Len(loaded.SelectedHistory, 2)
	assert.Equal([]int64{2, 3}, []int64{loaded.SelectedHistory[0].EventID, loaded.SelectedHistory[1].EventID})

	authority := testKataCoordinatedAuthority()
	first, err := enricher.loadHistory(t.Context(), authority, 7, selectedUID)
	require.NoError(err)
	second, err := enricher.loadHistory(t.Context(), authority, 7, selectedUID)
	require.NoError(err)
	require.Len(first, 2)
	require.Len(second, 2)
	assert.Equal([]int64{2, 3}, []int64{first[0].EventID, first[1].EventID})
	assert.Equal([]int64{2, 3}, []int64{second[0].EventID, second[1].EventID})
	assert.Equal(int64(9), calls.Load(), "an oversized project stream must reload on the next selection")
}

func TestKataSnapshotEnricherResumesRetainedProjectHistoryAfterCursorReset(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	selectedUID := "issue-member"
	cursors := []int64{}
	client := &fakeKataSnapshotAPIClient{
		showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
			return testKataShowIssueResponse(selectedUID), nil
		},
		pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
			requireKataProjectEventsRequest(t, options)
			cursors = append(cursors, *options.Query.AfterID)
			switch len(cursors) {
			case 1:
				return testKataPollProjectEventsResetResponse(41), nil
			case 2:
				return testKataPollProjectEventsResponse(43,
					testKataEvent(42, &selectedUID, time.Unix(42, 0)),
					testKataEvent(43, &selectedUID, time.Unix(43, 0))), nil
			default:
				return testKataPollProjectEventsResponse(43), nil
			}
		},
	}

	result, err := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client}).Enrich(
		t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{SelectedIssueUID: selectedUID},
	)

	require.NoError(t, err)
	assert.Len(result.SelectedHistory, 2)
	assert.Equal([]int64{0, 41, 43}, cursors)
	assert.NotContains(result.Errors, kataSnapshotEnrichmentStageHistory)
}

func TestKataSnapshotEnricherDiscardsPreResetHistoryAtRetainedBaseline(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	selectedUID := "issue-member"
	otherUID := "issue-other"
	cursors := []int64{}
	client := &fakeKataSnapshotAPIClient{
		showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
			return testKataShowIssueResponse(selectedUID), nil
		},
		pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
			requireKataProjectEventsRequest(t, options)
			cursors = append(cursors, *options.Query.AfterID)
			switch len(cursors) {
			case 1:
				return testKataPollProjectEventsResponse(2,
					testKataEvent(1, &selectedUID, time.Unix(1, 0)),
					testKataEvent(2, &otherUID, time.Unix(2, 0))), nil
			case 2:
				return testKataPollProjectEventsResetResponse(41), nil
			case 3:
				return testKataPollProjectEventsResponse(43,
					testKataEvent(42, &selectedUID, time.Unix(42, 0)),
					testKataEvent(43, &selectedUID, time.Unix(43, 0))), nil
			default:
				return testKataPollProjectEventsResponse(43), nil
			}
		},
	}

	result, err := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client}).Enrich(
		t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{SelectedIssueUID: selectedUID},
	)

	require.NoError(t, err)
	require.Len(t, result.SelectedHistory, 2)
	assert.Equal([]int64{42, 43}, []int64{result.SelectedHistory[0].EventID, result.SelectedHistory[1].EventID})
	assert.Equal([]int64{0, 2, 41, 43}, cursors)
	assert.NotContains(result.Errors, kataSnapshotEnrichmentStageHistory)
}

func TestKataSnapshotEnricherRejectsRepeatedHistoryCursorReset(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	resetCalls := 0
	client := &fakeKataSnapshotAPIClient{
		showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
			return testKataShowIssueResponse("issue-member"), nil
		},
		pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
			requireKataProjectEventsRequest(t, options)
			resetCalls++
			return testKataPollProjectEventsResetResponse(int64(resetCalls * 41)), nil
		},
	}

	result, err := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client}).Enrich(
		t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{SelectedIssueUID: "issue-member"},
	)

	require.NoError(t, err)
	assert.Equal(2, resetCalls)
	assert.Empty(result.SelectedHistory)
	assert.Equal(kataSnapshotEnrichmentError{Code: CodeUpstreamError, Message: "Could not load selected task history."}, result.Errors[kataSnapshotEnrichmentStageHistory])
}

func TestKataSnapshotEnricherRejectsInvalidHistoryPagination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response *katagenerated.PollProjectEventsResp
	}{
		{
			name: "missing response body",
			response: &katagenerated.PollProjectEventsResp{
				StatusCode: http.StatusOK,
			},
		},
		{
			name: "generated event validation failure",
			response: func() *katagenerated.PollProjectEventsResp {
				event := testKataEvent(1, nil, time.Now().UTC())
				event.Actor = ""
				return testKataPollProjectEventsResponse(1, event)
			}(),
		},
		{
			name:     "non-positive event id",
			response: testKataPollProjectEventsResponse(1, testKataEvent(0, nil, time.Now().UTC())),
		},
		{
			name: "non-monotonic event ids",
			response: testKataPollProjectEventsResponse(2,
				testKataEvent(2, nil, time.Now().UTC()),
				testKataEvent(1, nil, time.Now().UTC()),
			),
		},
		{name: "reset missing cursor", response: func() *katagenerated.PollProjectEventsResp {
			response := testKataPollProjectEventsResponse(0)
			response.JSON200.ResetRequired = true
			return response
		}()},
		{name: "reset cursor does not advance", response: testKataPollProjectEventsResetResponse(0)},
		{name: "reset cursor mismatches next cursor", response: func() *katagenerated.PollProjectEventsResp {
			response := testKataPollProjectEventsResetResponse(9)
			response.JSON200.NextAfterID = 10
			return response
		}()},
		{name: "reset contains events", response: func() *katagenerated.PollProjectEventsResp {
			response := testKataPollProjectEventsResetResponse(9)
			response.JSON200.Events = []katagenerated.EventEnvelope{testKataEvent(9, nil, time.Now().UTC())}
			return response
		}()},
		{name: "reset cursor without reset", response: func() *katagenerated.PollProjectEventsResp {
			resetAfterID := int64(9)
			response := testKataPollProjectEventsResponse(0)
			response.JSON200.ResetAfterID = &resetAfterID
			return response
		}()},
		{
			name:     "nonempty page makes no cursor progress",
			response: testKataPollProjectEventsResponse(0, testKataEvent(1, nil, time.Now().UTC())),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeKataSnapshotAPIClient{
				showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
					return testKataShowIssueResponse("issue-member"), nil
				},
				pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
					requireKataProjectEventsRequest(t, options)
					return test.response, nil
				},
			}
			enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client})

			result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{SelectedIssueUID: "issue-member"})

			require.NoError(t, err)
			assert.Empty(t, result.SelectedHistory)
			assert.Equal(t, kataSnapshotEnrichmentError{Code: CodeUpstreamError, Message: "Could not load selected task history."}, result.Errors[kataSnapshotEnrichmentStageHistory])
		})
	}
}

func TestKataSnapshotEnricherRejectsHistoryCursorMismatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		pollProjectEvents func(*katagenerated.PollProjectEventsRequestOptions) *katagenerated.PollProjectEventsResp
	}{
		{
			name: "nonempty page cursor skips past last event",
			pollProjectEvents: func(options *katagenerated.PollProjectEventsRequestOptions) *katagenerated.PollProjectEventsResp {
				if *options.Query.AfterID == 0 {
					return testKataPollProjectEventsResponse(2, testKataEvent(1, nil, time.Now().UTC()))
				}
				return testKataPollProjectEventsResponse(*options.Query.AfterID)
			},
		},
		{
			name: "empty page cursor advances",
			pollProjectEvents: func(*katagenerated.PollProjectEventsRequestOptions) *katagenerated.PollProjectEventsResp {
				return testKataPollProjectEventsResponse(1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			client := &fakeKataSnapshotAPIClient{
				showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
					return testKataShowIssueResponse("issue-member"), nil
				},
				pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
					requireKataProjectEventsRequest(t, options)
					return test.pollProjectEvents(options), nil
				},
			}
			enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client})

			result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{SelectedIssueUID: "issue-member"})

			require.NoError(t, err)
			assert.NotNil(result.SelectedDetail)
			assert.Empty(result.SelectedHistory)
			assert.Equal(kataSnapshotEnrichmentError{Code: CodeUpstreamError, Message: "Could not load selected task history."}, result.Errors[kataSnapshotEnrichmentStageHistory])
		})
	}
}

func TestKataSnapshotEnricherKeepsDetailAndHistoryFailuresIndependent(t *testing.T) {
	t.Parallel()
	selectedUID := "issue-member"

	tests := []struct {
		name           string
		detailResponse *katagenerated.ShowIssueByUIDResp
		detailErr      error
		historyResp    *katagenerated.PollProjectEventsResp
		historyErr     error
		wantDetail     bool
		wantHistory    bool
		wantErrorStage string
	}{
		{
			name:           "detail failure preserves history",
			detailErr:      errors.New("detail unavailable"),
			historyResp:    testKataPollProjectEventsResponse(1, testKataEvent(1, &selectedUID, time.Now().UTC())),
			wantHistory:    true,
			wantErrorStage: kataSnapshotEnrichmentStageDetail,
		},
		{
			name:           "history failure preserves detail",
			detailResponse: testKataShowIssueResponse("issue-member"),
			historyErr:     errors.New("history unavailable"),
			wantDetail:     true,
			wantErrorStage: kataSnapshotEnrichmentStageHistory,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			historyCalls := 0
			client := &fakeKataSnapshotAPIClient{
				showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
					return test.detailResponse, test.detailErr
				},
				pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
					requireKataProjectEventsRequest(t, options)
					historyCalls++
					if test.historyErr == nil && historyCalls > 1 {
						return testKataPollProjectEventsResponse(*options.Query.AfterID), nil
					}
					return test.historyResp, test.historyErr
				},
			}
			enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client})

			result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{SelectedIssueUID: "issue-member"})

			require.NoError(t, err)
			if test.wantDetail {
				assert.NotNil(result.SelectedDetail)
			} else {
				assert.Nil(result.SelectedDetail)
			}
			if test.wantHistory {
				assert.Len(result.SelectedHistory, 1)
			} else {
				assert.Empty(result.SelectedHistory)
			}
			assert.Contains(result.Errors, test.wantErrorStage)
		})
	}
}

func TestKataSnapshotEnricherPropagatesHistoryCancellation(t *testing.T) {
	t.Parallel()

	client := &fakeKataSnapshotAPIClient{
		showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
			return testKataShowIssueResponse("issue-member"), nil
		},
		pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
			requireKataProjectEventsRequest(t, options)
			return nil, context.DeadlineExceeded
		},
	}
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client})

	_, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{SelectedIssueUID: "issue-member"})

	require.ErrorIs(t, err, context.DeadlineExceeded)
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
	client.pollProjectEvents = func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
		requireKataProjectEventsRequest(t, options)
		return testKataPollProjectEventsResponse(0), nil
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

func TestKataSnapshotEnricherPreservesGraphFetchTimestampOnCacheHit(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	fetchedAt := time.Date(2026, 7, 21, 15, 0, 0, 0, time.UTC)
	later := fetchedAt.Add(time.Minute)
	var graphCalls atomic.Int64
	var nowCalls atomic.Int64
	client := &fakeKataSnapshotAPIClient{
		reachableGraph: func(context.Context, *katagenerated.ReachableIssueGraphRequestOptions) (*katagenerated.ReachableIssueGraphResp, error) {
			graphCalls.Add(1)
			return testKataGraphResponse("issue-source", "issue-linked"), nil
		},
	}
	cache := newKataSnapshotEnrichmentCacheWithConfig(time.Minute, 8, func(string) uint64 { return 0 })
	t.Cleanup(cache.close)
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{
		client: client,
		cache:  cache,
		now: func() time.Time {
			if nowCalls.Add(1) == 1 {
				return fetchedAt
			}
			return later
		},
	})
	authority := testKataCoordinatedAuthority()
	request := kataSnapshotEnrichmentRequest{GraphSourceUID: "issue-source"}

	first, err := enricher.Enrich(t.Context(), authority, request)
	require.NoError(err)
	second, err := enricher.Enrich(t.Context(), authority, request)
	require.NoError(err)

	require.NotNil(first.GraphFetchedAt)
	require.NotNil(second.GraphFetchedAt)
	assert.Equal(fetchedAt, *first.GraphFetchedAt)
	assert.Equal(fetchedAt, *second.GraphFetchedAt)
	assert.Equal(int64(1), graphCalls.Load())
}

func TestKataSnapshotEnricherDoesNotAuthorizeDisconnectedGraphNode(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	detailCalls := 0
	historyCalls := 0
	workspaceCalls := 0
	client := &fakeKataSnapshotAPIClient{
		reachableGraph: func(context.Context, *katagenerated.ReachableIssueGraphRequestOptions) (*katagenerated.ReachableIssueGraphResp, error) {
			response := testKataGraphResponse("issue-source", "issue-linked")
			response.JSON200.Edges = nil
			return response, nil
		},
		showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
			detailCalls++
			return testKataShowIssueResponse("issue-linked"), nil
		},
		pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
			requireKataProjectEventsRequest(t, options)
			historyCalls++
			return testKataPollProjectEventsResponse(0), nil
		},
	}
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{
		client: client,
		resolveWorkspaceTarget: func(context.Context, db.WorkspaceKataMetadata) (kataWorkspaceTargetResponse, error) {
			workspaceCalls++
			return kataWorkspaceTargetResponse{}, nil
		},
	})

	result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{
		SelectedIssueUID: "issue-linked",
		GraphSourceUID:   "issue-source",
	})

	require.NoError(t, err)
	assert.NotNil(result.Graph)
	assert.Empty(result.SelectedIssueUID)
	assert.Nil(result.SelectedDetail)
	assert.Zero(detailCalls)
	assert.Zero(historyCalls)
	assert.Zero(workspaceCalls)
	assert.Empty(result.Errors)
}

func TestKataSnapshotEnricherRejectsGraphEdgesOutsideNodeSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*katagenerated.ReachableGraphEdge)
	}{
		{
			name: "unknown from endpoint",
			mutate: func(edge *katagenerated.ReachableGraphEdge) {
				edge.FromUID = "issue-missing"
			},
		},
		{
			name: "unknown to endpoint",
			mutate: func(edge *katagenerated.ReachableGraphEdge) {
				edge.ToUID = "issue-missing"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			detailCalls := 0
			client := &fakeKataSnapshotAPIClient{
				reachableGraph: func(context.Context, *katagenerated.ReachableIssueGraphRequestOptions) (*katagenerated.ReachableIssueGraphResp, error) {
					response := testKataGraphResponse("issue-source", "issue-linked")
					test.mutate(&response.JSON200.Edges[0])
					return response, nil
				},
				showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
					detailCalls++
					return testKataShowIssueResponse("issue-linked"), nil
				},
			}
			enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client})

			result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{
				SelectedIssueUID: "issue-linked",
				GraphSourceUID:   "issue-source",
			})

			require.NoError(t, err)
			assert.Nil(result.Graph)
			assert.Empty(result.SelectedIssueUID)
			assert.Zero(detailCalls)
			assert.Contains(result.Errors, kataSnapshotEnrichmentStageGraph)
		})
	}
}

func TestKataSnapshotEnricherAllowsCatalogedCrossProjectGraphSelection(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	projectBUID := "project-b"
	client := &fakeKataSnapshotAPIClient{}
	client.reachableGraph = func(context.Context, *katagenerated.ReachableIssueGraphRequestOptions) (*katagenerated.ReachableIssueGraphResp, error) {
		response := testKataGraphResponse("issue-source", "issue-linked")
		response.JSON200.Nodes[1].ProjectID = 8
		response.JSON200.Nodes[1].ProjectUID = &projectBUID
		response.JSON200.Nodes[1].QualifiedID = "Project B#linked"
		return response, nil
	}
	client.showIssue = func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
		response := testKataShowIssueResponse("issue-linked")
		response.JSON200.Issue.ProjectID = 8
		response.JSON200.Issue.ProjectUID = &projectBUID
		return response, nil
	}
	client.pollProjectEvents = func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
		require.NotNil(options.PathParams)
		assert.Equal(int64(8), options.PathParams.ProjectID)
		return testKataPollProjectEventsResponse(0), nil
	}
	var workspaceMetadata db.WorkspaceKataMetadata
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{
		client: client,
		resolveWorkspaceTarget: func(_ context.Context, metadata db.WorkspaceKataMetadata) (kataWorkspaceTargetResponse, error) {
			workspaceMetadata = metadata
			return kataWorkspaceTargetResponse{Available: false}, nil
		},
	})
	authority := testKataCoordinatedAuthority()
	authority.Snapshot.Projects = append(authority.Snapshot.Projects, kataProjectSummary{ID: 8, UID: projectBUID, Name: "Project B"})

	result, err := enricher.Enrich(t.Context(), authority, kataSnapshotEnrichmentRequest{
		SelectedIssueUID: "issue-linked",
		GraphSourceUID:   "issue-source",
	})

	require.NoError(err)
	require.NotNil(result.Graph)
	require.NotNil(result.SelectedDetail)
	assert.Equal("issue-source", result.Graph.SourceUID)
	assert.Equal("project-b", workspaceMetadata.ProjectUID)
	assert.Equal("Project B", workspaceMetadata.ProjectName)
	assert.Equal("Project B#linked", workspaceMetadata.QualifiedID)
}

func TestKataSnapshotEnricherDoesNotAuthorizeMalformedGraphNodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*katagenerated.ReachableGraphResponseBody)
	}{
		{
			name: "missing required field",
			mutate: func(graph *katagenerated.ReachableGraphResponseBody) {
				graph.Nodes[1].Author = ""
			},
		},
		{
			name: "non-positive node id",
			mutate: func(graph *katagenerated.ReachableGraphResponseBody) {
				graph.Nodes[1].ID = 0
			},
		},
		{
			name: "wrong authoritative source id",
			mutate: func(graph *katagenerated.ReachableGraphResponseBody) {
				graph.Nodes[0].ID = 99
			},
		},
		{
			name: "uncataloged project identity",
			mutate: func(graph *katagenerated.ReachableGraphResponseBody) {
				unknownProjectUID := "project-unknown"
				graph.Nodes[1].ProjectID = 99
				graph.Nodes[1].ProjectUID = &unknownProjectUID
			},
		},
		{
			name: "qualified id does not match accepted project and short id",
			mutate: func(graph *katagenerated.ReachableGraphResponseBody) {
				graph.Nodes[1].QualifiedID = "Project A#different"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			require := require.New(t)

			detailCalls := 0
			client := &fakeKataSnapshotAPIClient{}
			client.reachableGraph = func(context.Context, *katagenerated.ReachableIssueGraphRequestOptions) (*katagenerated.ReachableIssueGraphResp, error) {
				response := testKataGraphResponse("issue-source", "issue-linked")
				test.mutate(response.JSON200)
				return response, nil
			}
			client.showIssue = func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
				detailCalls++
				return testKataShowIssueResponse("issue-linked"), nil
			}
			enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client})

			result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{
				SelectedIssueUID: "issue-linked",
				GraphSourceUID:   "issue-source",
			})

			require.NoError(err)
			assert.Nil(result.Graph)
			assert.Empty(result.SelectedIssueUID)
			assert.Nil(result.SelectedDetail)
			assert.Zero(detailCalls)
			assert.Contains(result.Errors, kataSnapshotEnrichmentStageGraph)
		})
	}
}

func TestKataSnapshotEnricherRejectsMalformedGeneratedDetail(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	workspaceCalls := 0
	client := &fakeKataSnapshotAPIClient{}
	client.showIssue = func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
		response := testKataShowIssueResponse("issue-member")
		response.JSON200.Issue.Author = ""
		return response, nil
	}
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{
		client: client,
		resolveWorkspaceTarget: func(context.Context, db.WorkspaceKataMetadata) (kataWorkspaceTargetResponse, error) {
			workspaceCalls++
			return kataWorkspaceTargetResponse{}, nil
		},
	})

	result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{
		SelectedIssueUID: "issue-member",
	})

	require.NoError(err)
	assert.Nil(result.SelectedDetail)
	assert.Zero(workspaceCalls)
	assert.Contains(result.Errors, kataSnapshotEnrichmentStageDetail)
}

func TestKataSnapshotEnricherRejectsDetailShortIDMismatch(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	workspaceCalls := 0
	client := &fakeKataSnapshotAPIClient{
		showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
			response := testKataShowIssueResponse("issue-member")
			response.JSON200.Issue.ShortID = "renamed"
			return response, nil
		},
	}
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{
		client: client,
		resolveWorkspaceTarget: func(context.Context, db.WorkspaceKataMetadata) (kataWorkspaceTargetResponse, error) {
			workspaceCalls++
			return kataWorkspaceTargetResponse{}, nil
		},
	})

	result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{
		SelectedIssueUID: "issue-member",
	})

	require.NoError(t, err)
	assert.Nil(result.SelectedDetail)
	assert.Zero(workspaceCalls)
	assert.Contains(result.Errors, kataSnapshotEnrichmentStageDetail)
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

func TestKataSnapshotEnricherPreservesMemberEnrichmentWhenOptionalGraphTimesOut(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	selectedUID := "issue-member"
	historyCalls := 0
	client := &fakeKataSnapshotAPIClient{
		reachableGraph: func(context.Context, *katagenerated.ReachableIssueGraphRequestOptions) (*katagenerated.ReachableIssueGraphResp, error) {
			return nil, context.DeadlineExceeded
		},
		showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
			return testKataShowIssueResponse(selectedUID), nil
		},
		pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
			requireKataProjectEventsRequest(t, options)
			historyCalls++
			if historyCalls == 1 {
				return testKataPollProjectEventsResponse(1, testKataEvent(1, &selectedUID, time.Now().UTC())), nil
			}
			return testKataPollProjectEventsResponse(*options.Query.AfterID), nil
		},
	}
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client})

	result, err := enricher.Enrich(t.Context(), testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{
		SelectedIssueUID: selectedUID,
		GraphSourceUID:   "issue-source",
	})

	require.NoError(t, err)
	assert.NotNil(result.SelectedDetail)
	assert.Len(result.SelectedHistory, 1)
	assert.Nil(result.Graph)
	assert.Equal(kataSnapshotEnrichmentError{Code: CodeUpstreamError, Message: "Could not load reachable graph."}, result.Errors[kataSnapshotEnrichmentStageGraph])
}

func TestKataSnapshotEnricherPropagatesRequestCancellationFromOptionalGraph(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	client := &fakeKataSnapshotAPIClient{
		reachableGraph: func(context.Context, *katagenerated.ReachableIssueGraphRequestOptions) (*katagenerated.ReachableIssueGraphResp, error) {
			cancel()
			return nil, context.Canceled
		},
		showIssue: func(context.Context, *katagenerated.ShowIssueByUIDRequestOptions) (*katagenerated.ShowIssueByUIDResp, error) {
			return testKataShowIssueResponse("issue-member"), nil
		},
		pollProjectEvents: func(_ context.Context, options *katagenerated.PollProjectEventsRequestOptions) (*katagenerated.PollProjectEventsResp, error) {
			requireKataProjectEventsRequest(t, options)
			return testKataPollProjectEventsResponse(*options.Query.AfterID), nil
		},
	}
	enricher := newKataSnapshotEnricher(kataSnapshotEnricherDeps{client: client})

	_, err := enricher.Enrich(ctx, testKataCoordinatedAuthority(), kataSnapshotEnrichmentRequest{
		SelectedIssueUID: "issue-member",
		GraphSourceUID:   "issue-source",
	})

	require.ErrorIs(t, err, context.Canceled)
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
	now := time.Date(2026, 7, 20, 16, 0, 0, 0, time.UTC)
	return &katagenerated.ReachableIssueGraphResp{
		StatusCode: http.StatusOK,
		JSON200: &katagenerated.ReachableGraphResponseBody{
			SourceUID: sourceUID,
			Depth:     "full",
			HideDone:  false,
			Edges: []katagenerated.ReachableGraphEdge{
				{FromUID: linkedUID, ToUID: sourceUID, Kind: katagenerated.ReachableGraphEdgeKindRelated},
			},
			Nodes: []katagenerated.ReachableGraphNode{
				{ID: 1, UID: sourceUID, ProjectID: 7, ProjectUID: &projectUID, ShortID: "source", QualifiedID: "Project A#source", Title: "Source task", Author: "actor", Body: "body", Status: "open", CreatedAt: now, UpdatedAt: now},
				{ID: 3, UID: linkedUID, ProjectID: 7, ProjectUID: &projectUID, ShortID: "linked", QualifiedID: "Project A#linked", Title: "Linked task", Author: "actor", Body: "body", Status: "open", CreatedAt: now, UpdatedAt: now},
			},
		},
	}
}

func testKataShowIssueResponse(uid string) *katagenerated.ShowIssueByUIDResp {
	projectUID := "project-a"
	now := time.Date(2026, 7, 20, 16, 0, 0, 0, time.UTC)
	issueID := int64(3)
	shortID := "linked"
	title := "Linked task"
	if uid == "issue-member" {
		issueID = 2
		shortID = "member"
		title = "Member task"
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
			ShortID:    shortID,
			Title:      title,
			Author:     "actor",
			Body:       "body",
			Status:     "open",
			CreatedAt:  now,
			UpdatedAt:  now,
		}},
	}
}

func testKataEventPage(firstEventID int64, count int, issueUID *string) []katagenerated.EventEnvelope {
	events := make([]katagenerated.EventEnvelope, count)
	for i := range events {
		eventID := firstEventID + int64(i)
		events[i] = testKataEvent(eventID, issueUID, time.Unix(eventID, 0).UTC())
	}
	return events
}

func requireKataProjectEventsRequest(t *testing.T, options *katagenerated.PollProjectEventsRequestOptions) {
	t.Helper()
	require.NotNil(t, options)
	require.NotNil(t, options.PathParams)
	require.Equal(t, int64(7), options.PathParams.ProjectID)
	require.NotNil(t, options.Query)
	require.NotNil(t, options.Query.AfterID)
	require.NotNil(t, options.Query.Limit)
}

func testKataPollProjectEventsResponse(nextAfterID int64, events ...katagenerated.EventEnvelope) *katagenerated.PollProjectEventsResp {
	return &katagenerated.PollProjectEventsResp{
		StatusCode: http.StatusOK,
		JSON200: &katagenerated.PollEventsBody{
			Events:        events,
			NextAfterID:   nextAfterID,
			ResetRequired: false,
		},
	}
}

func testKataPollProjectEventsResetResponse(resetAfterID int64) *katagenerated.PollProjectEventsResp {
	response := testKataPollProjectEventsResponse(resetAfterID)
	response.JSON200.ResetRequired = true
	response.JSON200.ResetAfterID = &resetAfterID
	return response
}

func testKataEvent(eventID int64, issueUID *string, createdAt time.Time) katagenerated.EventEnvelope {
	return katagenerated.EventEnvelope{
		Actor:             "actor",
		ContentHash:       "content-hash",
		CreatedAt:         createdAt,
		EventID:           eventID,
		EventUID:          "event-uid-" + time.Unix(eventID, 0).UTC().Format(time.RFC3339),
		IssueUID:          issueUID,
		OriginInstanceUID: "instance-a",
		ProjectID:         7,
		ProjectName:       "Project A",
		ProjectUID:        "project-a",
		Type:              "issue.updated",
	}
}

package platform

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectFakeReader records the queries the live collectors issue and serves
// configurable canonical results.
type collectFakeReader struct {
	issueQueries        []ItemPageQuery
	mergeRequestQueries []ItemPageQuery
	issuePage           Page[Issue]
	mergeRequestPage    Page[MergeRequest]
	issueLookup         ItemLookup[Issue]
	mergeRequestLookup  ItemLookup[MergeRequest]
	lookupNumbers       []int
	err                 error
}

func (r *collectFakeReader) ListIssuesPage(
	_ context.Context, _ RepoRef, query ItemPageQuery,
) (Page[Issue], error) {
	r.issueQueries = append(r.issueQueries, query)
	return r.issuePage, r.err
}

func (r *collectFakeReader) LookupIssue(
	_ context.Context, _ RepoRef, number int,
) (ItemLookup[Issue], error) {
	r.lookupNumbers = append(r.lookupNumbers, number)
	return r.issueLookup, r.err
}

func (r *collectFakeReader) ListIssueCommentsPage(
	context.Context, RepoRef, int, string,
) (Page[IssueEvent], error) {
	return Page[IssueEvent]{Exhausted: true}, nil
}

func (r *collectFakeReader) ListMergeRequestsPage(
	_ context.Context, _ RepoRef, query ItemPageQuery,
) (Page[MergeRequest], error) {
	r.mergeRequestQueries = append(r.mergeRequestQueries, query)
	return r.mergeRequestPage, r.err
}

func (r *collectFakeReader) LookupMergeRequest(
	_ context.Context, _ RepoRef, number int,
) (ItemLookup[MergeRequest], error) {
	r.lookupNumbers = append(r.lookupNumbers, number)
	return r.mergeRequestLookup, r.err
}

func (r *collectFakeReader) ListMergeRequestCommentsPage(
	context.Context, RepoRef, int, string,
) (Page[MergeRequestEvent], error) {
	return Page[MergeRequestEvent]{Exhausted: true}, nil
}

func (r *collectFakeReader) ListSubmittedReviewsPage(
	context.Context, RepoRef, int, string,
) (Page[MergeRequestEvent], error) {
	return Page[MergeRequestEvent]{Exhausted: true}, nil
}

func (r *collectFakeReader) ListReviewThreadsPage(
	context.Context, RepoRef, int, string,
) (Page[MergeRequestReviewThread], error) {
	return Page[MergeRequestReviewThread]{Exhausted: true}, nil
}

func collectTestRef() RepoRef {
	return RepoRef{
		Platform: KindGitea,
		Host:     "gitea.example.com",
		Owner:    "octo-org",
		Name:     "widgets",
		RepoPath: "octo-org/widgets",
	}
}

func TestListOpenCollectorsDrainOpenScans(t *testing.T) {
	ref := collectTestRef()
	reader := &collectFakeReader{
		issuePage: Page[Issue]{
			Items: []Issue{{Repo: ref, Number: 1}, {Repo: ref, Number: 2}}, Exhausted: true,
		},
		mergeRequestPage: Page[MergeRequest]{
			Items: []MergeRequest{{Repo: ref, Number: 5}}, Exhausted: true,
		},
	}

	assert := assert.New(t)
	require := require.New(t)
	issues, err := ListOpenIssues(context.Background(), reader, ref)
	require.NoError(err)
	require.Len(issues, 2)
	require.Len(reader.issueQueries, 1)
	assert.Equal(ItemPageQuery{State: ItemStateOpen, Order: ItemOrderUpdated}, reader.issueQueries[0])

	mergeRequests, err := ListOpenMergeRequests(context.Background(), reader, ref)
	require.NoError(err)
	require.Len(mergeRequests, 1)
	require.Len(reader.mergeRequestQueries, 1)
	assert.Equal(ItemPageQuery{State: ItemStateOpen, Order: ItemOrderUpdated}, reader.mergeRequestQueries[0])
}

func TestListOpenCollectorsSurfaceReaderErrors(t *testing.T) {
	ref := collectTestRef()
	readerErr := errors.New("upstream unavailable")
	reader := &collectFakeReader{err: readerErr}

	_, err := ListOpenIssues(context.Background(), reader, ref)
	require.ErrorIs(t, err, readerErr)
	_, err = ListOpenMergeRequests(context.Background(), reader, ref)
	require.ErrorIs(t, err, readerErr)
}

func TestRequireIssueMapsLookupOutcomes(t *testing.T) {
	ref := collectTestRef()
	destination := ref
	destination.Owner = "moved-org"
	destination.RepoPath = "moved-org/widgets"

	cases := []struct {
		name            string
		lookup          ItemLookup[Issue]
		wantErr         error
		wantDestination bool
	}{
		{
			name: "present returns the item",
			lookup: ItemLookup[Issue]{
				Outcome: LookupPresent, Item: Issue{Repo: ref, Number: 7},
			},
		},
		{
			name:    "removed is not_found",
			lookup:  ItemLookup[Issue]{Outcome: LookupRemoved},
			wantErr: ErrNotFound,
		},
		{
			name:    "inaccessible is permission_denied",
			lookup:  ItemLookup[Issue]{Outcome: LookupInaccessible},
			wantErr: ErrPermissionDenied,
		},
		{
			name: "moved is not_found carrying the destination",
			lookup: ItemLookup[Issue]{
				Outcome: LookupMoved, Destination: &destination,
			},
			wantErr:         ErrNotFound,
			wantDestination: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			reader := &collectFakeReader{issueLookup: tc.lookup}
			issue, err := RequireIssue(context.Background(), reader, ref, 7)
			require.Equal([]int{7}, reader.lookupNumbers)
			if tc.wantErr == nil {
				require.NoError(err)
				assert.Equal(7, issue.Number)
				return
			}
			require.ErrorIs(err, tc.wantErr)
			var platformErr *Error
			require.ErrorAs(err, &platformErr)
			assert.Equal(ref.Platform, platformErr.Provider)
			assert.Equal(ref.Host, platformErr.PlatformHost)
			if tc.wantDestination {
				require.NotNil(platformErr.Destination)
				assert.Equal(destination, *platformErr.Destination)
			} else {
				assert.Nil(platformErr.Destination)
			}
		})
	}
}

func TestRequireMergeRequestMapsLookupOutcomes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ref := collectTestRef()

	reader := &collectFakeReader{
		mergeRequestLookup: ItemLookup[MergeRequest]{
			Outcome: LookupPresent, Item: MergeRequest{Repo: ref, Number: 12},
		},
	}
	mr, err := RequireMergeRequest(context.Background(), reader, ref, 12)
	require.NoError(err)
	assert.Equal(12, mr.Number)

	reader = &collectFakeReader{
		mergeRequestLookup: ItemLookup[MergeRequest]{Outcome: LookupInaccessible},
	}
	_, err = RequireMergeRequest(context.Background(), reader, ref, 12)
	require.ErrorIs(err, ErrPermissionDenied)

	readerErr := errors.New("upstream unavailable")
	reader = &collectFakeReader{err: readerErr}
	_, err = RequireMergeRequest(context.Background(), reader, ref, 12)
	require.ErrorIs(err, readerErr)
}

package platform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testPageReadProvider struct {
	testProvider

	issuePage            Page[Issue]
	issueLookup          ItemLookup[Issue]
	mergeRequestComments Page[MergeRequestEvent]
}

func (p testPageReadProvider) ListIssuesPage(context.Context, RepoRef, ItemPageQuery) (Page[Issue], error) {
	return p.issuePage, nil
}
func (p testPageReadProvider) LookupIssue(context.Context, RepoRef, int) (ItemLookup[Issue], error) {
	return p.issueLookup, nil
}
func (p testPageReadProvider) ListIssueCommentsPage(context.Context, RepoRef, int, string) (Page[IssueEvent], error) {
	return Page[IssueEvent]{}, nil
}
func (p testPageReadProvider) ListMergeRequestsPage(context.Context, RepoRef, ItemPageQuery) (Page[MergeRequest], error) {
	return Page[MergeRequest]{}, nil
}
func (p testPageReadProvider) LookupMergeRequest(context.Context, RepoRef, int) (ItemLookup[MergeRequest], error) {
	return ItemLookup[MergeRequest]{}, nil
}
func (p testPageReadProvider) ListMergeRequestCommentsPage(context.Context, RepoRef, int, string) (Page[MergeRequestEvent], error) {
	return p.mergeRequestComments, nil
}
func (p testPageReadProvider) ListSubmittedReviewsPage(context.Context, RepoRef, int, string) (Page[MergeRequestEvent], error) {
	return Page[MergeRequestEvent]{}, nil
}
func (p testPageReadProvider) ListReviewThreadsPage(context.Context, RepoRef, int, string) (Page[MergeRequestReviewThread], error) {
	return Page[MergeRequestReviewThread]{}, nil
}

func pageReaderTestRef() RepoRef {
	return RepoRef{
		Platform: KindGitHub, Host: "github.com", Owner: "octo-org",
		Name: "widgets", RepoPath: "octo-org/widgets",
	}
}

func fullPageReadCapabilities() Capabilities {
	return Capabilities{
		ReadIssues: true, ReadMergeRequests: true,
		Archive: ArchiveCapabilities{
			HistoricalIssues: true, HistoricalMergeRequests: true,
			OrdinaryComments: true, SubmittedReviews: true, InlineReviewComments: true,
		},
	}
}

func wrappedPageReaders(t *testing.T, provider testPageReadProvider) (IssuePageReader, MergeRequestPageReader) {
	t.Helper()
	registry, err := NewRegistry(provider)
	require.NoError(t, err)
	issues, err := registry.IssuePageReader(provider.kind, provider.host)
	require.NoError(t, err)
	mergeRequests, err := registry.MergeRequestPageReader(provider.kind, provider.host)
	require.NoError(t, err)
	return issues, mergeRequests
}

func TestValidatingPageReadersRejectInvalidProviderResults(t *testing.T) {
	ref := pageReaderTestRef()
	foreign := ref
	foreign.Owner, foreign.RepoPath = "other", "other/widgets"
	provider := testPageReadProvider{
		testProvider: testProvider{
			kind: KindGitHub, host: "github.com", caps: fullPageReadCapabilities(),
		},
		issuePage: Page[Issue]{
			Items: []Issue{{Repo: foreign, Number: 7}}, Exhausted: true,
		},
		issueLookup: ItemLookup[Issue]{
			Outcome: LookupPresent, Item: Issue{Repo: ref, Number: 8},
		},
		mergeRequestComments: Page[MergeRequestEvent]{
			Items: []MergeRequestEvent{{Repo: ref, MergeRequestNumber: 9}}, Exhausted: true,
		},
	}
	issues, mergeRequests := wrappedPageReaders(t, provider)
	require := require.New(t)
	_, err := issues.ListIssuesPage(context.Background(), ref, ItemPageQuery{State: ItemStateOpen, Order: ItemOrderUpdated})
	require.ErrorIs(err, ErrProviderContract)
	_, err = issues.LookupIssue(context.Background(), ref, 7)
	require.ErrorIs(err, ErrProviderContract)
	_, err = mergeRequests.ListMergeRequestCommentsPage(context.Background(), ref, 7, "")
	require.ErrorIs(err, ErrProviderContract)
}

func TestValidatingPageReadersValidateMovedDestinations(t *testing.T) {
	ref := pageReaderTestRef()
	destination := ref
	destination.Owner, destination.RepoPath = "moved", "moved/widgets"
	for _, tt := range []struct {
		name        string
		destination *RepoRef
		wantErr     bool
	}{
		{"same repository", &ref, true},
		{"different repository", &destination, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := testPageReadProvider{
				testProvider: testProvider{
					kind: KindGitHub, host: "github.com", caps: fullPageReadCapabilities(),
				},
				issueLookup: ItemLookup[Issue]{Outcome: LookupMoved, Destination: tt.destination},
			}
			issues, _ := wrappedPageReaders(t, provider)
			result, err := issues.LookupIssue(context.Background(), ref, 7)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrProviderContract)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, LookupMoved, result.Outcome)
		})
	}
}

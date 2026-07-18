package platform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testPageReadProvider struct {
	testProvider
	calls map[string]int

	issuePage            Page[Issue]
	mergeRequestPage     Page[MergeRequest]
	issueLookup          ItemLookup[Issue]
	mergeRequestLookup   ItemLookup[MergeRequest]
	issueComments        Page[IssueEvent]
	mergeRequestComments Page[MergeRequestEvent]
	submittedReviews     Page[MergeRequestEvent]
	reviewThreads        Page[MergeRequestReviewThread]
}

func (p testPageReadProvider) record(name string) { p.calls[name]++ }
func (p testPageReadProvider) ListIssuesPage(context.Context, RepoRef, ItemPageQuery) (Page[Issue], error) {
	p.record("issues")
	return p.issuePage, nil
}
func (p testPageReadProvider) LookupIssue(context.Context, RepoRef, int) (ItemLookup[Issue], error) {
	p.record("issue_lookup")
	return p.issueLookup, nil
}
func (p testPageReadProvider) ListIssueCommentsPage(context.Context, RepoRef, int, string) (Page[IssueEvent], error) {
	p.record("issue_comments")
	return p.issueComments, nil
}
func (p testPageReadProvider) ListMergeRequestsPage(context.Context, RepoRef, ItemPageQuery) (Page[MergeRequest], error) {
	p.record("mrs")
	return p.mergeRequestPage, nil
}
func (p testPageReadProvider) LookupMergeRequest(context.Context, RepoRef, int) (ItemLookup[MergeRequest], error) {
	p.record("mr_lookup")
	return p.mergeRequestLookup, nil
}
func (p testPageReadProvider) ListMergeRequestCommentsPage(context.Context, RepoRef, int, string) (Page[MergeRequestEvent], error) {
	p.record("mr_comments")
	return p.mergeRequestComments, nil
}
func (p testPageReadProvider) ListSubmittedReviewsPage(context.Context, RepoRef, int, string) (Page[MergeRequestEvent], error) {
	p.record("reviews")
	return p.submittedReviews, nil
}
func (p testPageReadProvider) ListReviewThreadsPage(context.Context, RepoRef, int, string) (Page[MergeRequestReviewThread], error) {
	p.record("threads")
	return p.reviewThreads, nil
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

type readerCall struct {
	name       string
	itemScoped bool
	call       func(IssuePageReader, MergeRequestPageReader, RepoRef, int) error
}

var readerCalls = []readerCall{
	{"open issues", false, func(i IssuePageReader, _ MergeRequestPageReader, ref RepoRef, _ int) error {
		_, err := i.ListIssuesPage(context.Background(), ref, ItemPageQuery{State: ItemStateOpen, Order: ItemOrderUpdated})
		return err
	}},
	{"historical issues", false, func(i IssuePageReader, _ MergeRequestPageReader, ref RepoRef, _ int) error {
		_, err := i.ListIssuesPage(context.Background(), ref, ItemPageQuery{State: ItemStateAll, Order: ItemOrderCreated})
		return err
	}},
	{"issue lookup", true, func(i IssuePageReader, _ MergeRequestPageReader, ref RepoRef, number int) error {
		_, err := i.LookupIssue(context.Background(), ref, number)
		return err
	}},
	{"issue comments", true, func(i IssuePageReader, _ MergeRequestPageReader, ref RepoRef, number int) error {
		_, err := i.ListIssueCommentsPage(context.Background(), ref, number, "")
		return err
	}},
	{"open merge requests", false, func(_ IssuePageReader, m MergeRequestPageReader, ref RepoRef, _ int) error {
		_, err := m.ListMergeRequestsPage(context.Background(), ref, ItemPageQuery{State: ItemStateOpen, Order: ItemOrderUpdated})
		return err
	}},
	{"historical merge requests", false, func(_ IssuePageReader, m MergeRequestPageReader, ref RepoRef, _ int) error {
		_, err := m.ListMergeRequestsPage(context.Background(), ref, ItemPageQuery{State: ItemStateAll, Order: ItemOrderCreated})
		return err
	}},
	{"merge request lookup", true, func(_ IssuePageReader, m MergeRequestPageReader, ref RepoRef, number int) error {
		_, err := m.LookupMergeRequest(context.Background(), ref, number)
		return err
	}},
	{"merge request comments", true, func(_ IssuePageReader, m MergeRequestPageReader, ref RepoRef, number int) error {
		_, err := m.ListMergeRequestCommentsPage(context.Background(), ref, number, "")
		return err
	}},
	{"submitted reviews", true, func(_ IssuePageReader, m MergeRequestPageReader, ref RepoRef, number int) error {
		_, err := m.ListSubmittedReviewsPage(context.Background(), ref, number, "")
		return err
	}},
	{"review threads", true, func(_ IssuePageReader, m MergeRequestPageReader, ref RepoRef, number int) error {
		_, err := m.ListReviewThreadsPage(context.Background(), ref, number, "")
		return err
	}},
}

func TestValidatingPageReadersRejectInputsAndMissingCapabilities(t *testing.T) {
	ref := pageReaderTestRef()
	for _, tt := range []struct {
		name     string
		provider testPageReadProvider
		ref      RepoRef
		number   int
		want     error
		items    bool
	}{
		{
			name: "foreign repository", ref: RepoRef{
				Platform: KindGitLab, Host: "gitlab.example", Owner: "octo-org",
				Name: "widgets", RepoPath: "octo-org/widgets",
			}, number: 7, want: ErrInvalidRepoRef,
			provider: testPageReadProvider{testProvider: testProvider{
				kind: KindGitHub, host: "github.com", caps: fullPageReadCapabilities(),
			}, calls: map[string]int{}},
		},
		{
			name: "missing capabilities", ref: ref, number: 7, want: ErrUnsupportedCapability,
			provider: testPageReadProvider{
				testProvider: testProvider{kind: KindGitHub, host: "github.com"}, calls: map[string]int{},
			},
		},
		{
			name: "nonpositive item number", ref: ref, number: 0, want: ErrInvalidArgument, items: true,
			provider: testPageReadProvider{testProvider: testProvider{
				kind: KindGitHub, host: "github.com", caps: fullPageReadCapabilities(),
			}, calls: map[string]int{}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			issues, mergeRequests := wrappedPageReaders(t, tt.provider)
			for _, call := range readerCalls {
				if tt.items && !call.itemScoped {
					continue
				}
				require.ErrorIs(t, call.call(issues, mergeRequests, tt.ref, tt.number), tt.want, call.name)
			}
			assert.Empty(t, tt.provider.calls)
		})
	}
}

func TestValidatingPageReadersAllowLiveReadsWithoutArchiveCapabilities(t *testing.T) {
	ref := pageReaderTestRef()
	provider := testPageReadProvider{
		testProvider: testProvider{
			kind: KindGitHub, host: "github.com",
			caps: Capabilities{ReadIssues: true, ReadMergeRequests: true},
		},
		calls:            map[string]int{},
		issuePage:        Page[Issue]{Items: []Issue{{Repo: ref, Number: 7}}, Exhausted: true},
		mergeRequestPage: Page[MergeRequest]{Items: []MergeRequest{{Repo: ref, Number: 7}}, Exhausted: true},
		issueLookup: ItemLookup[Issue]{
			Outcome: LookupPresent, Item: Issue{Repo: ref, Number: 7},
		},
		mergeRequestLookup: ItemLookup[MergeRequest]{
			Outcome: LookupPresent, Item: MergeRequest{Repo: ref, Number: 7},
		},
	}
	issues, mergeRequests := wrappedPageReaders(t, provider)
	require := require.New(t)
	require.NoError(readerCalls[0].call(issues, mergeRequests, ref, 7))
	require.NoError(readerCalls[2].call(issues, mergeRequests, ref, 7))
	require.NoError(readerCalls[4].call(issues, mergeRequests, ref, 7))
	require.NoError(readerCalls[6].call(issues, mergeRequests, ref, 7))
	require.ErrorIs(readerCalls[1].call(issues, mergeRequests, ref, 7), ErrUnsupportedCapability)
	require.ErrorIs(readerCalls[9].call(issues, mergeRequests, ref, 7), ErrUnsupportedCapability)
}

func TestValidatingPageReadersRejectInvalidProviderResults(t *testing.T) {
	ref := pageReaderTestRef()
	foreign := ref
	foreign.Owner, foreign.RepoPath = "other", "other/widgets"
	cursor := "same"
	provider := testPageReadProvider{
		testProvider: testProvider{
			kind: KindGitHub, host: "github.com", caps: fullPageReadCapabilities(),
		},
		calls: map[string]int{},
		issuePage: Page[Issue]{
			Items: []Issue{{Repo: foreign, Number: 7}}, Exhausted: true,
		},
		issueLookup: ItemLookup[Issue]{
			Outcome: LookupPresent, Item: Issue{Repo: ref, Number: 8},
		},
		mergeRequestComments: Page[MergeRequestEvent]{
			Items: []MergeRequestEvent{{Repo: ref, MergeRequestNumber: 9}}, Exhausted: true,
		},
		submittedReviews: Page[MergeRequestEvent]{NextCursor: cursor},
	}
	issues, mergeRequests := wrappedPageReaders(t, provider)
	require := require.New(t)
	_, err := issues.ListIssuesPage(context.Background(), ref, ItemPageQuery{State: ItemStateOpen, Order: ItemOrderUpdated})
	require.ErrorIs(err, ErrProviderContract)
	_, err = issues.LookupIssue(context.Background(), ref, 7)
	require.ErrorIs(err, ErrProviderContract)
	_, err = mergeRequests.ListMergeRequestCommentsPage(context.Background(), ref, 7, "")
	require.ErrorIs(err, ErrProviderContract)
	_, err = mergeRequests.ListSubmittedReviewsPage(context.Background(), ref, 7, cursor)
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
		{"missing", nil, true},
		{"same repository", &ref, true},
		{"different repository", &destination, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := testPageReadProvider{
				testProvider: testProvider{
					kind: KindGitHub, host: "github.com", caps: fullPageReadCapabilities(),
				},
				calls:       map[string]int{},
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

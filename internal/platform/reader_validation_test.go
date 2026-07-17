package platform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPageReadProvider is a configurable fake canonical reader used to drive
// the validating wrapper across every provider kind.
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

func (p testPageReadProvider) ListIssuesPage(
	context.Context, RepoRef, ItemPageQuery,
) (Page[Issue], error) {
	p.record("issues_page")
	return p.issuePage, nil
}

func (p testPageReadProvider) LookupIssue(
	context.Context, RepoRef, int,
) (ItemLookup[Issue], error) {
	p.record("lookup_issue")
	return p.issueLookup, nil
}

func (p testPageReadProvider) ListIssueCommentsPage(
	context.Context, RepoRef, int, string,
) (Page[IssueEvent], error) {
	p.record("issue_comments")
	return p.issueComments, nil
}

func (p testPageReadProvider) ListMergeRequestsPage(
	context.Context, RepoRef, ItemPageQuery,
) (Page[MergeRequest], error) {
	p.record("merge_requests_page")
	return p.mergeRequestPage, nil
}

func (p testPageReadProvider) LookupMergeRequest(
	context.Context, RepoRef, int,
) (ItemLookup[MergeRequest], error) {
	p.record("lookup_merge_request")
	return p.mergeRequestLookup, nil
}

func (p testPageReadProvider) ListMergeRequestCommentsPage(
	context.Context, RepoRef, int, string,
) (Page[MergeRequestEvent], error) {
	p.record("merge_request_comments")
	return p.mergeRequestComments, nil
}

func (p testPageReadProvider) ListSubmittedReviewsPage(
	context.Context, RepoRef, int, string,
) (Page[MergeRequestEvent], error) {
	p.record("submitted_reviews")
	return p.submittedReviews, nil
}

func (p testPageReadProvider) ListReviewThreadsPage(
	context.Context, RepoRef, int, string,
) (Page[MergeRequestReviewThread], error) {
	p.record("review_threads")
	return p.reviewThreads, nil
}

func (p testPageReadProvider) record(method string) {
	if p.calls != nil {
		p.calls[method]++
	}
}

// pageReaderTestKinds are the provider identities the contract tests run
// against; every check must hold for every supported provider.
var pageReaderTestKinds = []struct {
	kind Kind
	host string
}{
	{KindGitHub, "github.com"},
	{KindGitLab, "gitlab.example.com"},
	{KindForgejo, "forge.example.com"},
	{KindGitea, "gitea.example.com"},
}

func fullPageReadCapabilities() Capabilities {
	return Capabilities{
		ReadIssues:        true,
		ReadMergeRequests: true,
		Archive: ArchiveCapabilities{
			HistoricalIssues:        true,
			HistoricalMergeRequests: true,
			OrdinaryComments:        true,
			SubmittedReviews:        true,
			InlineReviewComments:    true,
		},
	}
}

func pageReaderTestRef(kind Kind, host string) RepoRef {
	return RepoRef{
		Platform: kind,
		Host:     host,
		Owner:    "octo-org",
		Name:     "widgets",
		RepoPath: "octo-org/widgets",
	}
}

func newPageReaderRegistry(t *testing.T, provider Provider) *Registry {
	t.Helper()
	registry, err := NewRegistry(provider)
	require.NoError(t, err)
	return registry
}

func wrappedPageReaders(
	t *testing.T,
	provider testPageReadProvider,
) (IssuePageReader, MergeRequestPageReader) {
	t.Helper()
	registry := newPageReaderRegistry(t, provider)
	issues, err := registry.IssuePageReader(provider.kind, provider.host)
	require.NoError(t, err)
	mergeRequests, err := registry.MergeRequestPageReader(provider.kind, provider.host)
	require.NoError(t, err)
	return issues, mergeRequests
}

// issuePageReaderCalls and mergeRequestPageReaderCalls address every canonical
// reader method by name with the fixed item number 7 so contract tests can
// exercise the full method set.
var issuePageReaderCalls = map[string]func(IssuePageReader, RepoRef) error{
	"open_issues": func(r IssuePageReader, ref RepoRef) error {
		_, err := r.ListIssuesPage(context.Background(), ref, ItemPageQuery{
			State: ItemStateOpen, Order: ItemOrderUpdated,
		})
		return err
	},
	"historical_issues": func(r IssuePageReader, ref RepoRef) error {
		_, err := r.ListIssuesPage(context.Background(), ref, ItemPageQuery{
			State: ItemStateAll, Order: ItemOrderCreated,
		})
		return err
	},
	"lookup_issue": func(r IssuePageReader, ref RepoRef) error {
		_, err := r.LookupIssue(context.Background(), ref, 7)
		return err
	},
	"issue_comments": func(r IssuePageReader, ref RepoRef) error {
		_, err := r.ListIssueCommentsPage(context.Background(), ref, 7, "")
		return err
	},
}

var mergeRequestPageReaderCalls = map[string]func(MergeRequestPageReader, RepoRef) error{
	"open_merge_requests": func(r MergeRequestPageReader, ref RepoRef) error {
		_, err := r.ListMergeRequestsPage(context.Background(), ref, ItemPageQuery{
			State: ItemStateOpen, Order: ItemOrderUpdated,
		})
		return err
	},
	"historical_merge_requests": func(r MergeRequestPageReader, ref RepoRef) error {
		_, err := r.ListMergeRequestsPage(context.Background(), ref, ItemPageQuery{
			State: ItemStateAll, Order: ItemOrderCreated,
		})
		return err
	},
	"lookup_merge_request": func(r MergeRequestPageReader, ref RepoRef) error {
		_, err := r.LookupMergeRequest(context.Background(), ref, 7)
		return err
	},
	"merge_request_comments": func(r MergeRequestPageReader, ref RepoRef) error {
		_, err := r.ListMergeRequestCommentsPage(context.Background(), ref, 7, "")
		return err
	},
	"submitted_reviews": func(r MergeRequestPageReader, ref RepoRef) error {
		_, err := r.ListSubmittedReviewsPage(context.Background(), ref, 7, "")
		return err
	},
	"review_threads": func(r MergeRequestPageReader, ref RepoRef) error {
		_, err := r.ListReviewThreadsPage(context.Background(), ref, 7, "")
		return err
	},
}

func TestValidatingPageReadersRejectForeignRepositoryIdentity(t *testing.T) {
	for _, tc := range pageReaderTestKinds {
		t.Run(string(tc.kind), func(t *testing.T) {
			calls := map[string]int{}
			provider := testPageReadProvider{
				testProvider: testProvider{
					kind: tc.kind, host: tc.host, caps: fullPageReadCapabilities(),
				},
				calls: calls,
			}
			issues, mergeRequests := wrappedPageReaders(t, provider)
			require := require.New(t)

			foreign := pageReaderTestRef(KindGitea, "somewhere.example.net")
			if tc.kind == KindGitea {
				foreign = pageReaderTestRef(KindForgejo, "somewhere.example.net")
			}
			noncanonical := pageReaderTestRef(tc.kind, tc.host)
			noncanonical.Owner = "Octo Org"

			for name, call := range issuePageReaderCalls {
				require.ErrorIs(call(issues, foreign), ErrInvalidRepoRef, "issue method %s foreign ref", name)
				require.ErrorIs(call(issues, noncanonical), ErrInvalidRepoRef, "issue method %s noncanonical ref", name)
			}
			for name, call := range mergeRequestPageReaderCalls {
				require.ErrorIs(call(mergeRequests, foreign), ErrInvalidRepoRef, "MR method %s foreign ref", name)
				require.ErrorIs(call(mergeRequests, noncanonical), ErrInvalidRepoRef, "MR method %s noncanonical ref", name)
			}
			assert.Empty(t, calls, "provider must not be consulted for rejected refs")
		})
	}
}

func TestValidatingPageReadersRejectNonPositiveItemNumbers(t *testing.T) {
	for _, tc := range pageReaderTestKinds {
		t.Run(string(tc.kind), func(t *testing.T) {
			calls := map[string]int{}
			provider := testPageReadProvider{
				testProvider: testProvider{
					kind: tc.kind, host: tc.host, caps: fullPageReadCapabilities(),
				},
				calls: calls,
			}
			issues, mergeRequests := wrappedPageReaders(t, provider)
			require := require.New(t)
			ref := pageReaderTestRef(tc.kind, tc.host)

			for _, number := range []int{0, -4} {
				_, err := issues.LookupIssue(context.Background(), ref, number)
				require.ErrorIs(err, ErrInvalidArgument, "lookup issue %d", number)
				_, err = issues.ListIssueCommentsPage(context.Background(), ref, number, "")
				require.ErrorIs(err, ErrInvalidArgument, "issue comments %d", number)
				_, err = mergeRequests.LookupMergeRequest(context.Background(), ref, number)
				require.ErrorIs(err, ErrInvalidArgument, "lookup MR %d", number)
				_, err = mergeRequests.ListMergeRequestCommentsPage(context.Background(), ref, number, "")
				require.ErrorIs(err, ErrInvalidArgument, "MR comments %d", number)
				_, err = mergeRequests.ListSubmittedReviewsPage(context.Background(), ref, number, "")
				require.ErrorIs(err, ErrInvalidArgument, "submitted reviews %d", number)
				_, err = mergeRequests.ListReviewThreadsPage(context.Background(), ref, number, "")
				require.ErrorIs(err, ErrInvalidArgument, "review threads %d", number)
			}
			assert.Empty(t, calls, "provider must not be consulted for invalid item numbers")
		})
	}
}

func TestValidatingPageReadersRequireCapabilities(t *testing.T) {
	for _, tc := range pageReaderTestKinds {
		t.Run(string(tc.kind), func(t *testing.T) {
			calls := map[string]int{}
			// No live read capabilities and no archive capabilities at all:
			// every canonical read must be refused before the provider runs.
			provider := testPageReadProvider{
				testProvider: testProvider{kind: tc.kind, host: tc.host},
				calls:        calls,
			}
			issues, mergeRequests := wrappedPageReaders(t, provider)
			require := require.New(t)
			ref := pageReaderTestRef(tc.kind, tc.host)

			for name, call := range issuePageReaderCalls {
				require.ErrorIs(call(issues, ref), ErrUnsupportedCapability, "issue method %s", name)
			}
			for name, call := range mergeRequestPageReaderCalls {
				require.ErrorIs(call(mergeRequests, ref), ErrUnsupportedCapability, "MR method %s", name)
			}
			assert.Empty(t, calls, "provider must not be consulted without the capability")
		})
	}
}

func TestValidatingPageReadersLiveReadsNeedNoArchiveCapabilities(t *testing.T) {
	for _, tc := range pageReaderTestKinds {
		t.Run(string(tc.kind), func(t *testing.T) {
			ref := pageReaderTestRef(tc.kind, tc.host)
			provider := testPageReadProvider{
				testProvider: testProvider{
					kind: tc.kind, host: tc.host,
					caps: Capabilities{ReadIssues: true, ReadMergeRequests: true},
				},
				issuePage: Page[Issue]{
					Items: []Issue{{Repo: ref, Number: 7}}, Exhausted: true,
				},
				mergeRequestPage: Page[MergeRequest]{
					Items: []MergeRequest{{Repo: ref, Number: 7}}, Exhausted: true,
				},
				issueLookup: ItemLookup[Issue]{
					Outcome: LookupPresent, Item: Issue{Repo: ref, Number: 7},
				},
				mergeRequestLookup: ItemLookup[MergeRequest]{
					Outcome: LookupPresent, Item: MergeRequest{Repo: ref, Number: 7},
				},
			}
			issues, mergeRequests := wrappedPageReaders(t, provider)
			require := require.New(t)

			require.NoError(issuePageReaderCalls["open_issues"](issues, ref))
			require.NoError(issuePageReaderCalls["lookup_issue"](issues, ref))
			require.NoError(mergeRequestPageReaderCalls["open_merge_requests"](mergeRequests, ref))
			require.NoError(mergeRequestPageReaderCalls["lookup_merge_request"](mergeRequests, ref))

			// Historical traversal and detail datasets stay gated on the
			// archive capability declarations.
			require.ErrorIs(issuePageReaderCalls["historical_issues"](issues, ref), ErrUnsupportedCapability)
			require.ErrorIs(issuePageReaderCalls["issue_comments"](issues, ref), ErrUnsupportedCapability)
			require.ErrorIs(mergeRequestPageReaderCalls["historical_merge_requests"](mergeRequests, ref), ErrUnsupportedCapability)
			require.ErrorIs(mergeRequestPageReaderCalls["review_threads"](mergeRequests, ref), ErrUnsupportedCapability)
		})
	}
}

func TestValidatingPageReadersRejectEchoedCursor(t *testing.T) {
	for _, tc := range pageReaderTestKinds {
		t.Run(string(tc.kind), func(t *testing.T) {
			ref := pageReaderTestRef(tc.kind, tc.host)
			cursor := "cursor-1"
			provider := testPageReadProvider{
				testProvider: testProvider{
					kind: tc.kind, host: tc.host, caps: fullPageReadCapabilities(),
				},
				issuePage: Page[Issue]{
					Items: []Issue{{Repo: ref, Number: 7}}, NextCursor: cursor,
				},
				issueComments: Page[IssueEvent]{
					Items:      []IssueEvent{{Repo: ref, IssueNumber: 7}},
					NextCursor: cursor,
				},
				mergeRequestPage: Page[MergeRequest]{
					Items: []MergeRequest{{Repo: ref, Number: 7}}, NextCursor: cursor,
				},
			}
			issues, mergeRequests := wrappedPageReaders(t, provider)

			_, err := issues.ListIssuesPage(context.Background(), ref, ItemPageQuery{
				State: ItemStateAll, Order: ItemOrderCreated, Cursor: cursor,
			})
			require.ErrorIs(t, err, ErrProviderContract, "issue scan echoed cursor")

			_, err = issues.ListIssueCommentsPage(context.Background(), ref, 7, cursor)
			require.ErrorIs(t, err, ErrProviderContract, "issue comments echoed cursor")

			_, err = mergeRequests.ListMergeRequestsPage(context.Background(), ref, ItemPageQuery{
				State: ItemStateAll, Order: ItemOrderCreated, Cursor: cursor,
			})
			require.ErrorIs(t, err, ErrProviderContract, "MR scan echoed cursor")
		})
	}
}

func TestValidatingPageReadersValidateMovedDestinations(t *testing.T) {
	for _, tc := range pageReaderTestKinds {
		t.Run(string(tc.kind), func(t *testing.T) {
			ref := pageReaderTestRef(tc.kind, tc.host)
			destination := ref
			destination.Owner = "moved-org"
			destination.RepoPath = "moved-org/widgets"

			cases := []struct {
				name        string
				destination *RepoRef
				wantErr     error
			}{
				{name: "missing destination", destination: nil, wantErr: ErrProviderContract},
				{name: "destination equals source", destination: &ref, wantErr: ErrProviderContract},
				{name: "valid destination", destination: &destination},
			}
			for _, lookupCase := range cases {
				t.Run(lookupCase.name, func(t *testing.T) {
					provider := testPageReadProvider{
						testProvider: testProvider{
							kind: tc.kind, host: tc.host, caps: fullPageReadCapabilities(),
						},
						issueLookup: ItemLookup[Issue]{
							Outcome: LookupMoved, Destination: lookupCase.destination,
						},
						mergeRequestLookup: ItemLookup[MergeRequest]{
							Outcome: LookupMoved, Destination: lookupCase.destination,
						},
					}
					issues, mergeRequests := wrappedPageReaders(t, provider)

					issueLookup, err := issues.LookupIssue(context.Background(), ref, 7)
					if lookupCase.wantErr != nil {
						require.ErrorIs(t, err, lookupCase.wantErr)
					} else {
						require.NoError(t, err)
						assert.Equal(t, LookupMoved, issueLookup.Outcome)
					}

					mrLookup, err := mergeRequests.LookupMergeRequest(context.Background(), ref, 7)
					if lookupCase.wantErr != nil {
						require.ErrorIs(t, err, lookupCase.wantErr)
					} else {
						require.NoError(t, err)
						assert.Equal(t, LookupMoved, mrLookup.Outcome)
					}
				})
			}
		})
	}
}

func TestValidatingPageReadersValidateReturnedIdentities(t *testing.T) {
	for _, tc := range pageReaderTestKinds {
		t.Run(string(tc.kind), func(t *testing.T) {
			ref := pageReaderTestRef(tc.kind, tc.host)
			foreign := ref
			foreign.Owner = "someone-else"
			foreign.RepoPath = "someone-else/widgets"

			provider := testPageReadProvider{
				testProvider: testProvider{
					kind: tc.kind, host: tc.host, caps: fullPageReadCapabilities(),
				},
				issuePage: Page[Issue]{
					Items: []Issue{{Repo: foreign, Number: 7}}, Exhausted: true,
				},
				issueLookup: ItemLookup[Issue]{
					Outcome: LookupPresent, Item: Issue{Repo: ref, Number: 8},
				},
				mergeRequestComments: Page[MergeRequestEvent]{
					Items:     []MergeRequestEvent{{Repo: ref, MergeRequestNumber: 9}},
					Exhausted: true,
				},
			}
			issues, mergeRequests := wrappedPageReaders(t, provider)

			_, err := issues.ListIssuesPage(context.Background(), ref, ItemPageQuery{
				State: ItemStateOpen, Order: ItemOrderUpdated,
			})
			require.ErrorIs(t, err, ErrProviderContract, "foreign repo identity on page item")

			_, err = issues.LookupIssue(context.Background(), ref, 7)
			require.ErrorIs(t, err, ErrProviderContract, "wrong item number on present lookup")

			_, err = mergeRequests.ListMergeRequestCommentsPage(context.Background(), ref, 7, "")
			require.ErrorIs(t, err, ErrProviderContract, "event bound to another merge request")
		})
	}
}

func TestValidatingPageReadersPassThroughValidResults(t *testing.T) {
	for _, tc := range pageReaderTestKinds {
		t.Run(string(tc.kind), func(t *testing.T) {
			ref := pageReaderTestRef(tc.kind, tc.host)
			provider := testPageReadProvider{
				testProvider: testProvider{
					kind: tc.kind, host: tc.host, caps: fullPageReadCapabilities(),
				},
				issuePage: Page[Issue]{
					Items:     []Issue{{Repo: ref, Number: 3}, {Repo: ref, Number: 9}},
					Exhausted: true,
				},
				issueLookup: ItemLookup[Issue]{
					Outcome: LookupPresent, Item: Issue{Repo: ref, Number: 7},
				},
				issueComments: Page[IssueEvent]{
					Items:      []IssueEvent{{Repo: ref, IssueNumber: 7}},
					NextCursor: "next-cursor",
				},
			}
			issues, _ := wrappedPageReaders(t, provider)

			assert := assert.New(t)
			require := require.New(t)

			page, err := issues.ListIssuesPage(context.Background(), ref, ItemPageQuery{
				State: ItemStateOpen, Order: ItemOrderUpdated,
			})
			require.NoError(err)
			assert.Len(page.Items, 2)

			lookup, err := issues.LookupIssue(context.Background(), ref, 7)
			require.NoError(err)
			assert.Equal(7, lookup.Item.Number)

			comments, err := issues.ListIssueCommentsPage(context.Background(), ref, 7, "")
			require.NoError(err)
			assert.Equal("next-cursor", comments.NextCursor)
		})
	}
}

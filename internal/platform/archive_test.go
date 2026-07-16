package platform

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testArchiveProvider struct {
	testProvider
	calls                   map[string]int
	historicalIssues        ArchivePage[Issue]
	historicalMergeRequests ArchivePage[MergeRequest]
	updatedIssues           ArchivePage[Issue]
	updatedMergeRequests    ArchivePage[MergeRequest]
	issueResult             ArchiveItemResult[Issue]
	mergeRequestResult      ArchiveItemResult[MergeRequest]
	issueComments           ArchivePage[IssueEvent]
	mergeRequestComments    ArchivePage[MergeRequestEvent]
	submittedReviews        ArchivePage[MergeRequestEvent]
	reviewThreads           ArchivePage[MergeRequestReviewThread]
}

func (p testArchiveProvider) ListHistoricalIssues(
	context.Context, RepoRef, string,
) (ArchivePage[Issue], error) {
	p.record("historical_issues")
	return p.historicalIssues, nil
}

func (p testArchiveProvider) ListHistoricalMergeRequests(
	context.Context, RepoRef, string,
) (ArchivePage[MergeRequest], error) {
	p.record("historical_merge_requests")
	return p.historicalMergeRequests, nil
}

func (p testArchiveProvider) ListUpdatedIssues(
	context.Context, RepoRef, time.Time, string,
) (ArchivePage[Issue], error) {
	p.record("updated_issues")
	return p.updatedIssues, nil
}

func (p testArchiveProvider) ListUpdatedMergeRequests(
	context.Context, RepoRef, time.Time, string,
) (ArchivePage[MergeRequest], error) {
	p.record("updated_merge_requests")
	return p.updatedMergeRequests, nil
}

func (p testArchiveProvider) GetArchiveIssue(
	context.Context, RepoRef, int,
) (ArchiveItemResult[Issue], error) {
	p.record("get_issue")
	return p.issueResult, nil
}

func (p testArchiveProvider) GetArchiveMergeRequest(
	context.Context, RepoRef, int,
) (ArchiveItemResult[MergeRequest], error) {
	p.record("get_merge_request")
	return p.mergeRequestResult, nil
}

func (p testArchiveProvider) ListArchiveIssueComments(
	context.Context, RepoRef, int, string,
) (ArchivePage[IssueEvent], error) {
	p.record("issue_comments")
	return p.issueComments, nil
}

func (p testArchiveProvider) ListArchiveMergeRequestComments(
	context.Context, RepoRef, int, string,
) (ArchivePage[MergeRequestEvent], error) {
	p.record("merge_request_comments")
	return p.mergeRequestComments, nil
}

func (p testArchiveProvider) ListArchiveSubmittedReviews(
	context.Context, RepoRef, int, string,
) (ArchivePage[MergeRequestEvent], error) {
	p.record("submitted_reviews")
	return p.submittedReviews, nil
}

func (p testArchiveProvider) ListArchiveReviewThreads(
	context.Context, RepoRef, int, string,
) (ArchivePage[MergeRequestReviewThread], error) {
	p.record("review_threads")
	return p.reviewThreads, nil
}

func (p testArchiveProvider) record(method string) {
	if p.calls != nil {
		p.calls[method]++
	}
}

// archiveReaderCalls invokes each reader method with the fixed item number 7
// so validation tests can address any method by name.
var archiveReaderCalls = map[string]func(ArchiveReader, RepoRef) error{
	"historical_issues": func(r ArchiveReader, ref RepoRef) error {
		_, err := r.ListHistoricalIssues(context.Background(), ref, "")
		return err
	},
	"historical_merge_requests": func(r ArchiveReader, ref RepoRef) error {
		_, err := r.ListHistoricalMergeRequests(context.Background(), ref, "")
		return err
	},
	"updated_issues": func(r ArchiveReader, ref RepoRef) error {
		_, err := r.ListUpdatedIssues(context.Background(), ref, time.Now().UTC(), "")
		return err
	},
	"updated_merge_requests": func(r ArchiveReader, ref RepoRef) error {
		_, err := r.ListUpdatedMergeRequests(context.Background(), ref, time.Now().UTC(), "")
		return err
	},
	"get_issue": func(r ArchiveReader, ref RepoRef) error {
		_, err := r.GetArchiveIssue(context.Background(), ref, 7)
		return err
	},
	"get_merge_request": func(r ArchiveReader, ref RepoRef) error {
		_, err := r.GetArchiveMergeRequest(context.Background(), ref, 7)
		return err
	},
	"issue_comments": func(r ArchiveReader, ref RepoRef) error {
		_, err := r.ListArchiveIssueComments(context.Background(), ref, 7, "")
		return err
	},
	"merge_request_comments": func(r ArchiveReader, ref RepoRef) error {
		_, err := r.ListArchiveMergeRequestComments(context.Background(), ref, 7, "")
		return err
	},
	"submitted_reviews": func(r ArchiveReader, ref RepoRef) error {
		_, err := r.ListArchiveSubmittedReviews(context.Background(), ref, 7, "")
		return err
	},
	"review_threads": func(r ArchiveReader, ref RepoRef) error {
		_, err := r.ListArchiveReviewThreads(context.Background(), ref, 7, "")
		return err
	},
}

func TestArchiveCapabilitiesReportPerDatasetSupport(t *testing.T) {
	caps := ArchiveCapabilities{
		HistoricalIssues:        true,
		OrdinaryComments:        true,
		InlineReviewComments:    true,
		HistoricalMergeRequests: false,
		SubmittedReviews:        false,
	}

	tests := []struct {
		name       string
		capability ArchiveCapability
		want       ArchiveCapabilitySupport
	}{
		{name: "historical issues", capability: ArchiveCapabilityHistoricalIssues, want: ArchiveCapabilitySupported},
		{name: "historical merge requests", capability: ArchiveCapabilityHistoricalMergeRequests, want: ArchiveCapabilityUnsupported},
		{name: "ordinary comments", capability: ArchiveCapabilityOrdinaryComments, want: ArchiveCapabilitySupported},
		{name: "submitted reviews", capability: ArchiveCapabilitySubmittedReviews, want: ArchiveCapabilityUnsupported},
		{name: "inline review comments", capability: ArchiveCapabilityInlineReviewComments, want: ArchiveCapabilitySupported},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := caps.Support(tt.capability)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestArchiveCapabilitiesRejectUnknownCapability(t *testing.T) {
	caps := ArchiveCapabilities{}

	_, err := caps.Support(ArchiveCapability("unknown"))
	require.ErrorIs(t, err, ErrInvalidArgument)

	err = caps.Require(KindGitLab, "gitlab.example.com", ArchiveCapability("unknown"))
	var platformErr *Error
	require.ErrorAs(t, err, &platformErr)
	assert := assert.New(t)
	assert.Equal(ErrCodeInvalidArgument, platformErr.Code)
	assert.Equal("archive_capability", platformErr.Field)
}

func TestArchiveCapabilitiesReturnProviderScopedUnsupportedErrors(t *testing.T) {
	caps := ArchiveCapabilities{}

	err := caps.Require(KindGitLab, "gitlab.example.com", ArchiveCapabilitySubmittedReviews)

	var platformErr *Error
	require.ErrorAs(t, err, &platformErr)
	require.ErrorIs(t, err, ErrUnsupportedCapability)
	assert := assert.New(t)
	assert.Equal(ErrCodeUnsupportedCapability, platformErr.Code)
	assert.Equal(KindGitLab, platformErr.Provider)
	assert.Equal("gitlab.example.com", platformErr.PlatformHost)
	assert.Equal(string(ArchiveCapabilitySubmittedReviews), platformErr.Capability)
}

func TestRegistryArchiveReaderRequiresInterfaceAndHistoricalDeclaration(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
	}{
		{
			name: "interface without declaration",
			provider: testArchiveProvider{testProvider: testProvider{
				kind: KindGitLab,
				host: "gitlab.example.com",
			}},
		},
		{
			name: "declaration without interface",
			provider: testProvider{
				kind: KindGitLab,
				host: "gitlab.example.com",
				caps: Capabilities{Archive: ArchiveCapabilities{HistoricalIssues: true}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, err := NewRegistry(tt.provider)
			require.NoError(t, err)

			_, err = registry.ArchiveReader(KindGitLab, "gitlab.example.com")

			var platformErr *Error
			require.ErrorAs(t, err, &platformErr)
			require.ErrorIs(t, err, ErrUnsupportedCapability)
			assert := assert.New(t)
			assert.Equal(KindGitLab, platformErr.Provider)
			assert.Equal("gitlab.example.com", platformErr.PlatformHost)
			assert.Equal("archive_reader", platformErr.Capability)
		})
	}
}

func TestRegistryArchiveReaderAcceptsPartialHistoricalDeclarations(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	provider := &testArchiveProvider{testProvider: testProvider{
		kind: KindForgejo,
		host: "forge.example.com",
		caps: Capabilities{Archive: ArchiveCapabilities{
			HistoricalMergeRequests: true,
			OrdinaryComments:        true,
		}},
	}}
	registry, err := NewRegistry(provider)
	require.NoError(err)

	reader, err := registry.ArchiveReader(KindForgejo, "forge.example.com")
	require.NoError(err)

	assert.NotSame(provider, reader)
	support, err := provider.Capabilities().Archive.Support(ArchiveCapabilitySubmittedReviews)
	require.NoError(err)
	assert.Equal(ArchiveCapabilityUnsupported, support)
}

func TestRegistryArchiveReaderIsolatesProviderAndHost(t *testing.T) {
	require := require.New(t)
	archiveProvider := &testArchiveProvider{testProvider: testProvider{
		kind: KindGitLab,
		host: "code.example.com",
		caps: Capabilities{Archive: ArchiveCapabilities{HistoricalIssues: true}},
	}}
	registry, err := NewRegistry(
		archiveProvider,
		testProvider{kind: KindGitLab, host: "other.example.com"},
		testProvider{kind: KindForgejo, host: "code.example.com"},
	)
	require.NoError(err)

	reader, err := registry.ArchiveReader(KindGitLab, "code.example.com")
	require.NoError(err)
	assert.NotSame(t, archiveProvider, reader)

	for _, ref := range []struct {
		kind Kind
		host string
	}{
		{kind: KindGitLab, host: "other.example.com"},
		{kind: KindForgejo, host: "code.example.com"},
	} {
		_, err = registry.ArchiveReader(ref.kind, ref.host)
		require.ErrorIs(err, ErrUnsupportedCapability)
	}
}

func TestRegistryArchiveReaderValidatesRequestedProviderIdentityBeforeCalling(t *testing.T) {
	tests := []struct {
		name string
		edit func(*RepoRef)
	}{
		{name: "different provider", edit: func(ref *RepoRef) { ref.Platform = KindGitHub }},
		{name: "different host", edit: func(ref *RepoRef) { ref.Host = "other.example.com" }},
		{name: "noncanonical host", edit: func(ref *RepoRef) { ref.Host = "GitLab.example.com" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, reader := archiveTestReader(t, allArchiveCapabilities())
			ref := archiveTestRepoRef()
			tt.edit(&ref)

			_, err := reader.ListHistoricalIssues(context.Background(), ref, "")

			require.ErrorIs(t, err, ErrInvalidRepoRef)
			assert.Empty(t, provider.calls)
		})
	}
}

func TestRegistryArchiveReaderEnforcesEveryMethodCapabilityBeforeCalling(t *testing.T) {
	issuesOnly := ArchiveCapabilities{HistoricalIssues: true}
	mergeRequestsOnly := ArchiveCapabilities{HistoricalMergeRequests: true}
	tests := []struct {
		call           string
		caps           ArchiveCapabilities
		wantCapability ArchiveCapability
	}{
		{call: "historical_issues", caps: mergeRequestsOnly, wantCapability: ArchiveCapabilityHistoricalIssues},
		{call: "updated_issues", caps: mergeRequestsOnly, wantCapability: ArchiveCapabilityHistoricalIssues},
		{call: "get_issue", caps: mergeRequestsOnly, wantCapability: ArchiveCapabilityHistoricalIssues},
		{call: "historical_merge_requests", caps: issuesOnly, wantCapability: ArchiveCapabilityHistoricalMergeRequests},
		{call: "updated_merge_requests", caps: issuesOnly, wantCapability: ArchiveCapabilityHistoricalMergeRequests},
		{call: "get_merge_request", caps: issuesOnly, wantCapability: ArchiveCapabilityHistoricalMergeRequests},
		{
			call:           "issue_comments",
			caps:           ArchiveCapabilities{HistoricalMergeRequests: true, OrdinaryComments: true},
			wantCapability: ArchiveCapabilityHistoricalIssues,
		},
		{call: "issue_comments", caps: issuesOnly, wantCapability: ArchiveCapabilityOrdinaryComments},
		{
			call:           "merge_request_comments",
			caps:           ArchiveCapabilities{HistoricalIssues: true, OrdinaryComments: true},
			wantCapability: ArchiveCapabilityHistoricalMergeRequests,
		},
		{call: "merge_request_comments", caps: mergeRequestsOnly, wantCapability: ArchiveCapabilityOrdinaryComments},
		{
			call:           "submitted_reviews",
			caps:           ArchiveCapabilities{HistoricalIssues: true, SubmittedReviews: true},
			wantCapability: ArchiveCapabilityHistoricalMergeRequests,
		},
		{call: "submitted_reviews", caps: mergeRequestsOnly, wantCapability: ArchiveCapabilitySubmittedReviews},
		{
			call:           "review_threads",
			caps:           ArchiveCapabilities{HistoricalIssues: true, InlineReviewComments: true},
			wantCapability: ArchiveCapabilityHistoricalMergeRequests,
		},
		{call: "review_threads", caps: mergeRequestsOnly, wantCapability: ArchiveCapabilityInlineReviewComments},
	}

	for _, tt := range tests {
		t.Run(tt.call+"/"+string(tt.wantCapability), func(t *testing.T) {
			provider, reader := archiveTestReader(t, tt.caps)

			err := archiveReaderCalls[tt.call](reader, archiveTestRepoRef())

			var platformErr *Error
			require.ErrorAs(t, err, &platformErr)
			require.ErrorIs(t, err, ErrUnsupportedCapability)
			assert := assert.New(t)
			assert.Equal(string(tt.wantCapability), platformErr.Capability)
			assert.Empty(provider.calls)
		})
	}
}

func TestRegistryArchiveReaderRejectsNonpositiveRequestedNumbersBeforeCalling(t *testing.T) {
	for _, number := range []int{0, -1} {
		provider, reader := archiveTestReader(t, allArchiveCapabilities())

		_, err := reader.GetArchiveIssue(context.Background(), archiveTestRepoRef(), number)

		var platformErr *Error
		require.ErrorAs(t, err, &platformErr)
		require.ErrorIs(t, err, ErrInvalidArgument)
		assert := assert.New(t)
		assert.Equal("item_number", platformErr.Field)
		assert.Empty(provider.calls)
	}
}

// Each case seeds the one provider response shape its reader method validates.
// Methods sharing a validator (historical/updated enumerations, merge request
// comments/reviews) are represented once; TestRegistryArchiveReaderValidatesAllTenSuccessfulMethods
// proves every method routes through the validating wrapper.
func TestRegistryArchiveReaderValidatesReturnedItems(t *testing.T) {
	tests := []struct {
		name      string
		call      string
		seed      func(*testArchiveProvider, RepoRef)
		wantField string
	}{
		{
			name: "issue page nonpositive number",
			call: "historical_issues",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.historicalIssues = ArchivePage[Issue]{Items: []Issue{{Repo: ref}}, Exhausted: true}
			},
			wantField: "archive_item_number",
		},
		{
			name: "issue page wrong repo",
			call: "historical_issues",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.historicalIssues = ArchivePage[Issue]{Items: []Issue{{Repo: archiveOtherRepoRef(), Number: 7}}, Exhausted: true}
			},
			wantField: "archive_item_repo",
		},
		{
			name: "merge request page nonpositive number",
			call: "historical_merge_requests",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.historicalMergeRequests = ArchivePage[MergeRequest]{Items: []MergeRequest{{Repo: ref}}, Exhausted: true}
			},
			wantField: "archive_item_number",
		},
		{
			name: "merge request page wrong repo",
			call: "historical_merge_requests",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.historicalMergeRequests = ArchivePage[MergeRequest]{Items: []MergeRequest{{Repo: archiveOtherRepoRef(), Number: 7}}, Exhausted: true}
			},
			wantField: "archive_item_repo",
		},
		{
			name: "present issue wrong repo",
			call: "get_issue",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.issueResult = ArchiveItemResult[Issue]{Outcome: ArchiveLookupPresent, Item: Issue{Repo: archiveOtherRepoRef(), Number: 7}}
			},
			wantField: "archive_item_repo",
		},
		{
			name: "present issue wrong number",
			call: "get_issue",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.issueResult = ArchiveItemResult[Issue]{Outcome: ArchiveLookupPresent, Item: Issue{Repo: ref, Number: 8}}
			},
			wantField: "archive_item_number",
		},
		{
			name: "present merge request wrong repo",
			call: "get_merge_request",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.mergeRequestResult = ArchiveItemResult[MergeRequest]{Outcome: ArchiveLookupPresent, Item: MergeRequest{Repo: archiveOtherRepoRef(), Number: 7}}
			},
			wantField: "archive_item_repo",
		},
		{
			name: "present merge request wrong number",
			call: "get_merge_request",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.mergeRequestResult = ArchiveItemResult[MergeRequest]{Outcome: ArchiveLookupPresent, Item: MergeRequest{Repo: ref, Number: 8}}
			},
			wantField: "archive_item_number",
		},
		{
			name: "issue comment wrong repo",
			call: "issue_comments",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.issueComments = ArchivePage[IssueEvent]{Items: []IssueEvent{{Repo: archiveOtherRepoRef(), IssueNumber: 7}}, Exhausted: true}
			},
			wantField: "archive_event_repo",
		},
		{
			name: "issue comment wrong number",
			call: "issue_comments",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.issueComments = ArchivePage[IssueEvent]{Items: []IssueEvent{{Repo: ref, IssueNumber: 8}}, Exhausted: true}
			},
			wantField: "archive_event_number",
		},
		{
			name: "merge request event wrong repo",
			call: "merge_request_comments",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.mergeRequestComments = ArchivePage[MergeRequestEvent]{Items: []MergeRequestEvent{{Repo: archiveOtherRepoRef(), MergeRequestNumber: 7}}, Exhausted: true}
			},
			wantField: "archive_event_repo",
		},
		{
			name: "merge request event wrong number",
			call: "merge_request_comments",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.mergeRequestComments = ArchivePage[MergeRequestEvent]{Items: []MergeRequestEvent{{Repo: ref, MergeRequestNumber: 8}}, Exhausted: true}
			},
			wantField: "archive_event_number",
		},
		{
			name: "review thread wrong repo",
			call: "review_threads",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.reviewThreads = ArchivePage[MergeRequestReviewThread]{
					Items:     []MergeRequestReviewThread{{Repo: archiveOtherRepoRef(), MergeRequestNumber: 7}},
					Exhausted: true,
				}
			},
			wantField: "archive_thread_repo",
		},
		{
			name: "review thread wrong number",
			call: "review_threads",
			seed: func(p *testArchiveProvider, ref RepoRef) {
				p.reviewThreads = ArchivePage[MergeRequestReviewThread]{
					Items:     []MergeRequestReviewThread{{Repo: ref, MergeRequestNumber: 8}},
					Exhausted: true,
				}
			},
			wantField: "archive_thread_number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, reader := archiveTestReader(t, allArchiveCapabilities())
			tt.seed(provider, archiveTestRepoRef())

			err := archiveReaderCalls[tt.call](reader, archiveTestRepoRef())

			assertArchiveContractError(t, err, tt.wantField)
			assert.Equal(t, 1, archiveProviderCallCount(provider))
		})
	}
}

func TestRegistryArchiveReaderReportsMalformedReturnedSourceAsProviderContract(t *testing.T) {
	provider, reader := archiveTestReader(t, allArchiveCapabilities())
	returned := archiveTestRepoRef()
	returned.Host = "gitlab.example.com:bad"
	provider.historicalIssues = ArchivePage[Issue]{
		Items: []Issue{{Repo: returned, Number: 7}}, Exhausted: true,
	}

	_, err := reader.ListHistoricalIssues(context.Background(), archiveTestRepoRef(), "")

	require.ErrorIs(t, err, ErrProviderContract)
	require.NotErrorIs(t, err, ErrInvalidRepoRef)
}

func TestRegistryArchiveReaderValidatesMovedDestination(t *testing.T) {
	provider, reader := archiveTestReader(t, allArchiveCapabilities())
	source := archiveTestRepoRef()
	provider.issueResult = ArchiveItemResult[Issue]{Outcome: ArchiveLookupMoved, Destination: &source}

	_, err := reader.GetArchiveIssue(context.Background(), archiveTestRepoRef(), 7)
	assertArchiveContractError(t, err, "archive_lookup_destination")

	destination := archiveOtherRepoRef()
	provider.issueResult = ArchiveItemResult[Issue]{Outcome: ArchiveLookupMoved, Destination: &destination}

	_, err = reader.GetArchiveIssue(context.Background(), archiveTestRepoRef(), 7)
	assert.NoError(t, err)
}

func TestRegistryArchiveReaderPassesCurrentCursorToValidation(t *testing.T) {
	provider, reader := archiveTestReader(t, allArchiveCapabilities())
	ref := archiveTestRepoRef()
	provider.historicalIssues = ArchivePage[Issue]{
		Items:      []Issue{{Repo: ref, Number: 1}},
		NextCursor: "opaque cursor",
	}

	_, err := reader.ListHistoricalIssues(context.Background(), ref, "opaque cursor")

	assertArchiveContractError(t, err, "archive_page_cursor")
}

func TestRegistryArchiveReaderDoesNotValidateGenericItemForNonPresentLookup(t *testing.T) {
	for _, outcome := range []ArchiveLookupOutcome{
		ArchiveLookupRemoved,
		ArchiveLookupInaccessible,
	} {
		provider, reader := archiveTestReader(t, allArchiveCapabilities())
		provider.issueResult = ArchiveItemResult[Issue]{Outcome: outcome}

		_, err := reader.GetArchiveIssue(context.Background(), archiveTestRepoRef(), 7)

		assert.NoError(t, err)
	}
}

func TestRegistryArchiveReaderValidatesAllTenSuccessfulMethods(t *testing.T) {
	provider, reader := archiveTestReader(t, allArchiveCapabilities())
	ref := archiveTestRepoRef()
	provider.historicalIssues = ArchivePage[Issue]{Items: []Issue{{Repo: ref, Number: 7}}, Exhausted: true}
	provider.historicalMergeRequests = ArchivePage[MergeRequest]{Items: []MergeRequest{{Repo: ref, Number: 7}}, Exhausted: true}
	provider.updatedIssues = provider.historicalIssues
	provider.updatedMergeRequests = provider.historicalMergeRequests
	provider.issueResult = ArchiveItemResult[Issue]{Outcome: ArchiveLookupPresent, Item: Issue{Repo: ref, Number: 7}}
	provider.mergeRequestResult = ArchiveItemResult[MergeRequest]{Outcome: ArchiveLookupPresent, Item: MergeRequest{Repo: ref, Number: 7}}
	provider.issueComments = ArchivePage[IssueEvent]{Items: []IssueEvent{{Repo: ref, IssueNumber: 7}}, Exhausted: true}
	provider.mergeRequestComments = ArchivePage[MergeRequestEvent]{Items: []MergeRequestEvent{{Repo: ref, MergeRequestNumber: 7}}, Exhausted: true}
	provider.submittedReviews = provider.mergeRequestComments
	provider.reviewThreads = ArchivePage[MergeRequestReviewThread]{
		Items:     []MergeRequestReviewThread{{Repo: ref, MergeRequestNumber: 7}},
		Exhausted: true,
	}

	assert := assert.New(t)
	for name, call := range archiveReaderCalls {
		require.NoError(t, call(reader, ref), name)
	}
	assert.Len(provider.calls, len(archiveReaderCalls))
	for method, count := range provider.calls {
		assert.Equal(1, count, method)
	}
}

func TestValidateArchivePageAcceptsOnlyBoundedTerminationShapes(t *testing.T) {
	tests := []struct {
		name string
		page ArchivePage[int]
	}{
		{
			name: "next cursor with items",
			page: ArchivePage[int]{Items: []int{0}, NextCursor: "opaque:cursor:value"},
		},
		{
			name: "whitespace cursor remains opaque",
			page: ArchivePage[int]{Items: []int{1}, NextCursor: " \t"},
		},
		{
			name: "exhausted empty page",
			page: ArchivePage[int]{Exhausted: true},
		},
		{
			name: "exhausted page with items",
			page: ArchivePage[int]{Items: []int{1}, Exhausted: true},
		},
		{
			name: "filtered progress with advancing cursor",
			page: ArchivePage[int]{NextCursor: "next", ProgressOnly: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, ValidateArchivePage(KindGitLab, "gitlab.example.com", "", tt.page))
		})
	}
}

func TestValidateArchivePageReturnsTypedContractErrors(t *testing.T) {
	tests := []struct {
		name string
		page ArchivePage[int]
	}{
		{
			name: "cursor and exhaustion",
			page: ArchivePage[int]{Items: []int{1}, NextCursor: "next", Exhausted: true},
		},
		{
			name: "neither cursor nor exhaustion",
			page: ArchivePage[int]{Items: []int{1}},
		},
		{
			name: "non-exhausted empty page",
			page: ArchivePage[int]{NextCursor: "next"},
		},
		{
			name: "progress page with items",
			page: ArchivePage[int]{Items: []int{1}, NextCursor: "next", ProgressOnly: true},
		},
		{
			name: "exhausted progress page",
			page: ArchivePage[int]{Exhausted: true, ProgressOnly: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateArchivePage(KindGitLab, "gitlab.example.com", "", tt.page)
			assertArchiveContractError(t, err, "archive_page")
		})
	}
}

func TestValidateArchivePageRejectsRepeatedOpaqueCursor(t *testing.T) {
	for _, cursor := range []string{"opaque:cursor", " \t"} {
		err := ValidateArchivePage(KindGitLab, "gitlab.example.com", cursor, ArchivePage[int]{
			Items:      []int{1},
			NextCursor: cursor,
		})
		assertArchiveContractError(t, err, "archive_page_cursor")
	}
}

func TestValidateArchiveItemResultAcceptsLookupOutcomes(t *testing.T) {
	destination := &RepoRef{
		Platform: KindForgejo,
		Host:     "forge.example.com",
		Owner:    "group",
		Name:     "project",
	}
	tests := []struct {
		name   string
		result ArchiveItemResult[Issue]
	}{
		{name: "present zero-value item", result: ArchiveItemResult[Issue]{Outcome: ArchiveLookupPresent}},
		{name: "removed", result: ArchiveItemResult[Issue]{Outcome: ArchiveLookupRemoved}},
		{name: "inaccessible", result: ArchiveItemResult[Issue]{Outcome: ArchiveLookupInaccessible}},
		{name: "moved", result: ArchiveItemResult[Issue]{Outcome: ArchiveLookupMoved, Destination: destination}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, ValidateArchiveItemResult(KindGitLab, "gitlab.example.com", tt.result))
		})
	}
}

func TestValidateArchiveItemResultRequiresMovedDestinationIdentity(t *testing.T) {
	complete := RepoRef{
		Platform: KindForgejo,
		Host:     "forge.example.com",
		Owner:    "group",
		Name:     "project",
	}
	tests := []struct {
		name        string
		destination *RepoRef
	}{
		{name: "missing destination"},
		{name: "missing provider", destination: archiveRepoRefWithout(complete, "provider")},
		{name: "missing host", destination: archiveRepoRefWithout(complete, "host")},
		{name: "missing owner", destination: archiveRepoRefWithout(complete, "owner")},
		{name: "missing name", destination: archiveRepoRefWithout(complete, "name")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateArchiveItemResult(KindGitLab, "gitlab.example.com", ArchiveItemResult[Issue]{
				Outcome:     ArchiveLookupMoved,
				Destination: tt.destination,
			})
			assertArchiveContractError(t, err, "archive_lookup_destination")
		})
	}
}

func TestValidateArchiveItemResultRejectsUnexpectedDestinationsAndOutcomes(t *testing.T) {
	destination := &RepoRef{
		Platform: KindForgejo,
		Host:     "forge.example.com",
		Owner:    "group",
		Name:     "project",
	}
	tests := []struct {
		name      string
		result    ArchiveItemResult[Issue]
		wantField string
	}{
		{
			name:      "unknown outcome",
			result:    ArchiveItemResult[Issue]{Outcome: ArchiveLookupOutcome("unknown")},
			wantField: "archive_lookup_outcome",
		},
		{
			name:      "present destination",
			result:    ArchiveItemResult[Issue]{Outcome: ArchiveLookupPresent, Destination: destination},
			wantField: "archive_lookup_destination",
		},
		{
			name:      "removed destination",
			result:    ArchiveItemResult[Issue]{Outcome: ArchiveLookupRemoved, Destination: destination},
			wantField: "archive_lookup_destination",
		},
		{
			name:      "inaccessible destination",
			result:    ArchiveItemResult[Issue]{Outcome: ArchiveLookupInaccessible, Destination: destination},
			wantField: "archive_lookup_destination",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateArchiveItemResult(KindGitLab, "gitlab.example.com", tt.result)
			assertArchiveContractError(t, err, tt.wantField)
		})
	}
}

func assertArchiveContractError(t *testing.T, err error, field string) {
	t.Helper()
	var platformErr *Error
	require.ErrorAs(t, err, &platformErr)
	require.ErrorIs(t, err, ErrProviderContract)
	require.NotErrorIs(t, err, ErrInvalidRepoRef)
	assert := assert.New(t)
	assert.Equal(ErrCodeProviderContract, platformErr.Code)
	assert.Equal(KindGitLab, platformErr.Provider)
	assert.Equal("gitlab.example.com", platformErr.PlatformHost)
	assert.Equal(field, platformErr.Field)
}

func archiveRepoRefWithout(ref RepoRef, field string) *RepoRef {
	switch field {
	case "provider":
		ref.Platform = ""
	case "host":
		ref.Host = ""
	case "owner":
		ref.Owner = ""
	case "name":
		ref.Name = ""
	}
	return &ref
}

func archiveTestReader(
	t *testing.T,
	caps ArchiveCapabilities,
) (*testArchiveProvider, ArchiveReader) {
	t.Helper()
	provider := &testArchiveProvider{
		testProvider: testProvider{
			kind: KindGitLab,
			host: "gitlab.example.com",
			caps: Capabilities{Archive: caps},
		},
		calls: make(map[string]int),
	}
	registry, err := NewRegistry(provider)
	require.NoError(t, err)
	reader, err := registry.ArchiveReader(KindGitLab, "gitlab.example.com")
	require.NoError(t, err)
	return provider, reader
}

func allArchiveCapabilities() ArchiveCapabilities {
	return ArchiveCapabilities{
		HistoricalIssues:        true,
		HistoricalMergeRequests: true,
		OrdinaryComments:        true,
		SubmittedReviews:        true,
		InlineReviewComments:    true,
	}
}

func archiveTestRepoRef() RepoRef {
	return RepoRef{
		Platform: KindGitLab,
		Host:     "gitlab.example.com",
		Owner:    "group/subgroup",
		Name:     "project",
		RepoPath: "group/subgroup/project",
	}
}

func archiveOtherRepoRef() RepoRef {
	ref := archiveTestRepoRef()
	ref.Name = "other"
	ref.RepoPath = "group/subgroup/other"
	return ref
}

func archiveProviderCallCount(provider *testArchiveProvider) int {
	total := 0
	for _, count := range provider.calls {
		total += count
	}
	return total
}

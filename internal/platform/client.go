package platform

import "context"

type Provider interface {
	Platform() Kind
	Host() string
	Capabilities() Capabilities
}

type RepositoryReader interface {
	GetRepository(ctx context.Context, ref RepoRef) (Repository, error)
	ListRepositories(
		ctx context.Context,
		owner string,
		opts RepositoryListOptions,
	) ([]Repository, error)
}

type MergeRequestReader interface {
	ListOpenMergeRequests(ctx context.Context, ref RepoRef) ([]MergeRequest, error)
	GetMergeRequest(ctx context.Context, ref RepoRef, number int) (MergeRequest, error)
	ListMergeRequestEvents(
		ctx context.Context,
		ref RepoRef,
		number int,
	) ([]MergeRequestEvent, error)
}

type IssueReader interface {
	ListOpenIssues(ctx context.Context, ref RepoRef) ([]Issue, error)
	GetIssue(ctx context.Context, ref RepoRef, number int) (Issue, error)
	ListIssueEvents(ctx context.Context, ref RepoRef, number int) ([]IssueEvent, error)
}

type LabelCatalog struct {
	Labels      []Label
	NotModified bool
}

type LabelReader interface {
	ListLabels(ctx context.Context, ref RepoRef) (LabelCatalog, error)
}

type ReleaseReader interface {
	ListReleases(ctx context.Context, ref RepoRef) ([]Release, error)
}

type TagReader interface {
	ListTags(ctx context.Context, ref RepoRef) ([]Tag, error)
}

type CIReader interface {
	ListCIChecks(ctx context.Context, ref RepoRef, sha string) ([]CICheck, error)
}

type CommentMutator interface {
	CreateMergeRequestComment(
		ctx context.Context,
		ref RepoRef,
		number int,
		body string,
	) (MergeRequestEvent, error)
	EditMergeRequestComment(
		ctx context.Context,
		ref RepoRef,
		number int,
		commentID int64,
		body string,
	) (MergeRequestEvent, error)
	CreateIssueComment(ctx context.Context, ref RepoRef, number int, body string) (IssueEvent, error)
	EditIssueComment(ctx context.Context, ref RepoRef, number int, commentID int64, body string) (IssueEvent, error)
}

type StateMutator interface {
	SetMergeRequestState(ctx context.Context, ref RepoRef, number int, state string) (MergeRequest, error)
	SetIssueState(ctx context.Context, ref RepoRef, number int, state string) (Issue, error)
}

type MergeMutator interface {
	// MergeMergeRequest merges the MR. expectedHeadSHA is the head commit
	// the caller reviewed; providers that support head binding must reject
	// the merge when the MR head has moved past it. An empty value skips
	// the check. Providers whose API cannot bind the merge to a head
	// commit treat it as advisory.
	MergeMergeRequest(
		ctx context.Context,
		ref RepoRef,
		number int,
		commitTitle string,
		commitMessage string,
		method string,
		expectedHeadSHA string,
	) (MergeResult, error)
}

type WorkflowApprovalMutator interface {
	ApproveWorkflow(ctx context.Context, ref RepoRef, runID string) error
}

type ReadyForReviewMutator interface {
	MarkReadyForReview(ctx context.Context, ref RepoRef, number int) (MergeRequest, error)
}

type IssueMutator interface {
	CreateIssue(ctx context.Context, ref RepoRef, title string, body string) (Issue, error)
}

type LabelMutator interface {
	SetMergeRequestLabels(ctx context.Context, ref RepoRef, number int, names []string) ([]Label, error)
	SetIssueLabels(ctx context.Context, ref RepoRef, number int, names []string) ([]Label, error)
}

// AssigneeMutator replaces the full assignee username set on a merge
// request or issue and returns the provider-normalized assignee list.
type AssigneeMutator interface {
	SetMergeRequestAssignees(ctx context.Context, ref RepoRef, number int, usernames []string) ([]string, error)
	SetIssueAssignees(ctx context.Context, ref RepoRef, number int, usernames []string) ([]string, error)
}

// ReviewerMutator requests reviews from users on a merge request and
// removes pending review requests. Both calls return the full updated
// requested-reviewer username list after the mutation. Requesting an
// empty username list is a read: it mutates nothing and returns the
// provider's current requested-reviewer set, so callers can diff a
// desired set against live provider state instead of cached state.
// All usernames, in both directions, are individual user logins;
// implementations must exclude team or group reviewer identities so a
// caller-computed removal diff never targets an unsupported identity.
type ReviewerMutator interface {
	RequestMergeRequestReviewers(ctx context.Context, ref RepoRef, number int, usernames []string) ([]string, error)
	RemoveMergeRequestReviewers(ctx context.Context, ref RepoRef, number int, usernames []string) ([]string, error)
}

type ReviewMutator interface {
	// ApproveMergeRequest approves the MR. expectedHeadSHA is the caller's
	// target provider head for the approval; providers that support head
	// binding must reject the approval when the MR head has moved past it.
	// An empty value skips the check. Providers whose API cannot bind the
	// approval to a head commit treat it as advisory.
	ApproveMergeRequest(
		ctx context.Context,
		ref RepoRef,
		number int,
		body string,
		expectedHeadSHA string,
	) (MergeRequestEvent, error)
}

type ThreadReplier interface {
	ReplyToThread(
		ctx context.Context,
		ref RepoRef,
		number int,
		threadID string,
		body string,
	) (MergeRequestEvent, error)
}

type ThreadResolver interface {
	ResolveThread(
		ctx context.Context,
		ref RepoRef,
		number int,
		threadID string,
		resolved bool,
	) error
}

type DiffReviewDraftMutator interface {
	PublishDiffReviewDraft(
		ctx context.Context,
		ref RepoRef,
		number int,
		input PublishDiffReviewDraftInput,
	) (*PublishedDiffReview, error)
}

type DiffReviewThreadResolver interface {
	ResolveDiffReviewThread(ctx context.Context, ref RepoRef, number int, providerThreadID string) error
	UnresolveDiffReviewThread(ctx context.Context, ref RepoRef, number int, providerThreadID string) error
}

type MergeRequestReviewThreadReader interface {
	ListMergeRequestReviewThreads(
		ctx context.Context,
		ref RepoRef,
		number int,
	) ([]MergeRequestReviewThread, error)
}

type MergeRequestContentMutator interface {
	EditMergeRequestContent(
		ctx context.Context,
		ref RepoRef,
		number int,
		title *string,
		body *string,
	) (MergeRequest, error)
}

type IssueContentMutator interface {
	EditIssueContent(
		ctx context.Context,
		ref RepoRef,
		number int,
		title *string,
		body *string,
	) (Issue, error)
}

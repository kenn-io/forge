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
	MergeMergeRequest(
		ctx context.Context,
		ref RepoRef,
		number int,
		commitTitle string,
		commitMessage string,
		method string,
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

type ReviewMutator interface {
	ApproveMergeRequest(ctx context.Context, ref RepoRef, number int, body string) (MergeRequestEvent, error)
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

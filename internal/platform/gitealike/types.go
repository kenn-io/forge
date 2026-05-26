package gitealike

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/middleman/internal/platform"
)

const defaultPageSize = 100

type Transport interface {
	GetRepository(ctx context.Context, owner, repo string) (RepositoryDTO, error)
	ListUserRepositories(ctx context.Context, owner string, opts PageOptions) ([]RepositoryDTO, Page, error)
	ListOrgRepositories(ctx context.Context, owner string, opts PageOptions) ([]RepositoryDTO, Page, error)
	ListOpenPullRequests(ctx context.Context, ref platform.RepoRef, opts PageOptions) ([]PullRequestDTO, Page, error)
	GetPullRequest(ctx context.Context, ref platform.RepoRef, number int) (PullRequestDTO, error)
	ListPullRequestComments(ctx context.Context, ref platform.RepoRef, number int, opts PageOptions) ([]CommentDTO, Page, error)
	ListPullRequestReviews(ctx context.Context, ref platform.RepoRef, number int, opts PageOptions) ([]ReviewDTO, Page, error)
	ListPullRequestCommits(ctx context.Context, ref platform.RepoRef, number int, opts PageOptions) ([]CommitDTO, Page, error)
	ListOpenIssues(ctx context.Context, ref platform.RepoRef, opts PageOptions) ([]IssueDTO, Page, error)
	GetIssue(ctx context.Context, ref platform.RepoRef, number int) (IssueDTO, error)
	ListIssueComments(ctx context.Context, ref platform.RepoRef, number int, opts PageOptions) ([]CommentDTO, Page, error)
	ListReleases(ctx context.Context, ref platform.RepoRef, opts PageOptions) ([]ReleaseDTO, Page, error)
	ListTags(ctx context.Context, ref platform.RepoRef, opts PageOptions) ([]TagDTO, Page, error)
	ListStatuses(ctx context.Context, ref platform.RepoRef, sha string, opts PageOptions) ([]StatusDTO, Page, error)
}

type TimelineTransport interface {
	ListIssueTimeline(ctx context.Context, ref platform.RepoRef, number int, opts PageOptions) ([]TimelineEventDTO, Page, error)
}

type MutationTransport interface {
	CreateIssueComment(ctx context.Context, ref platform.RepoRef, number int, body string) (CommentDTO, error)
	EditIssueComment(ctx context.Context, ref platform.RepoRef, commentID int64, body string) (CommentDTO, error)
	CreateIssue(ctx context.Context, ref platform.RepoRef, title string, body string) (IssueDTO, error)
	EditIssue(ctx context.Context, ref platform.RepoRef, number int, opts IssueMutationOptions) (IssueDTO, error)
	EditPullRequest(ctx context.Context, ref platform.RepoRef, number int, opts PullRequestMutationOptions) (PullRequestDTO, error)
	MergePullRequest(ctx context.Context, ref platform.RepoRef, number int, opts MergeOptions) (MergeResultDTO, error)
	CreatePullReview(ctx context.Context, ref platform.RepoRef, number int, body string) (ReviewDTO, error)
}

type ActionsTransport interface {
	ListActionRuns(ctx context.Context, ref platform.RepoRef, sha string, opts PageOptions) ([]ActionRunDTO, Page, error)
}

type PageOptions struct {
	Page     int
	PageSize int
}

type Page struct {
	Next int
}

type UserDTO struct {
	ID       int64
	UserName string
	FullName string
}

type RepositoryDTO struct {
	ID            int64
	Owner         UserDTO
	Name          string
	FullName      string
	HTMLURL       string
	CloneURL      string
	DefaultBranch string
	Private       bool
	Archived      bool
	Description   string
	AllowSquash   bool
	AllowMerge    bool
	AllowRebase   bool
	CanPush       *bool
	CanAdmin      *bool
	Created       time.Time
	Updated       time.Time
}

type BranchDTO struct {
	Ref          string
	SHA          string
	RepoCloneURL string
}

type LabelDTO struct {
	ID          int64
	Name        string
	Description string
	Color       string
	IsDefault   bool
}

type PullRequestDTO struct {
	ID        int64
	Index     int
	HTMLURL   string
	Title     string
	User      UserDTO
	State     string
	Draft     bool
	IsLocked  bool
	Body      string
	Head      BranchDTO
	Base      BranchDTO
	Labels    []LabelDTO
	Comments  int
	Mergeable *bool
	Created   time.Time
	Updated   time.Time
	Merged    bool
	MergedAt  *time.Time
	Closed    *time.Time
}

type IssueDTO struct {
	ID            int64
	Index         int
	HTMLURL       string
	Title         string
	User          UserDTO
	State         string
	Body          string
	Comments      int
	Labels        []LabelDTO
	Created       time.Time
	Updated       time.Time
	Closed        *time.Time
	IsPullRequest bool
}

type CommentDTO struct {
	ID      int64
	User    UserDTO
	Body    string
	Created time.Time
	Updated time.Time
}

type TimelineEventDTO struct {
	ID            int64
	User          UserDTO
	Type          string
	Body          string
	Assignee      UserDTO
	PreviousTitle string
	CurrentTitle  string
	Created       time.Time
	Updated       time.Time
}

type ReviewDTO struct {
	ID        int64
	User      UserDTO
	State     string
	Body      string
	Submitted time.Time
}

type CommitDTO struct {
	SHA        string
	AuthorName string
	Message    string
	URL        string
	Created    time.Time
}

type ReleaseDTO struct {
	ID          int64
	TagName     string
	Title       string
	HTMLURL     string
	Target      string
	Prerelease  bool
	PublishedAt *time.Time
	CreatedAt   time.Time
}

type TagDTO struct {
	Name   string
	Commit CommitDTO
	URL    string
}

type StatusDTO struct {
	ID          int64
	Context     string
	State       string
	TargetURL   string
	Description string
	Created     time.Time
	Updated     time.Time
}

type IssueMutationOptions struct {
	Title *string
	Body  *string
	State *string
}

type PullRequestMutationOptions struct {
	Title *string
	Body  *string
	State *string
}

type MergeOptions struct {
	CommitTitle   string
	CommitMessage string
	Method        string
}

type MergeResultDTO struct {
	Merged  bool
	SHA     string
	Message string
}

type ActionRunDTO struct {
	ID           int64
	RunNumber    int64
	WorkflowID   string
	Title        string
	Status       string
	Conclusion   string
	CommitSHA    string
	HTMLURL      string
	Created      time.Time
	Updated      time.Time
	Started      *time.Time
	Stopped      *time.Time
	NeedApproval bool
}

type HTTPError struct {
	StatusCode int
	Message    string
	Err        error
}

func (e *HTTPError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := e.Message
	if message == "" {
		message = httpStatusMessage(e.StatusCode)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", message, e.Err)
	}
	return message
}

func (e *HTTPError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func mapTransportError(kind platform.Kind, host string, err error) error {
	if err == nil {
		return nil
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return err
	}
	var code platform.PlatformErrorCode
	switch httpErr.StatusCode {
	case 401, 403:
		code = platform.ErrCodePermissionDenied
	case 404:
		code = platform.ErrCodeNotFound
	default:
		return err
	}
	return &platform.Error{
		Code:         code,
		Provider:     kind,
		PlatformHost: host,
		Err:          err,
	}
}

func httpStatusMessage(statusCode int) string {
	switch statusCode {
	case 401:
		return "unauthorized"
	case 403:
		return "forbidden"
	case 404:
		return "not found"
	default:
		return fmt.Sprintf("http status %d", statusCode)
	}
}

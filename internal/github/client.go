package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	gh "github.com/google/go-github/v84/github"
	"golang.org/x/oauth2"
)

type ForcePushEvent struct {
	Actor     string
	BeforeSHA string
	AfterSHA  string
	Ref       string
	CreatedAt time.Time
}

type PullRequestTimelineEvent struct {
	NodeID               string
	EventType            string
	Actor                string
	Assignee             string
	CreatedAt            time.Time
	DeletedCommentAuthor string
	BeforeSHA            string
	AfterSHA             string
	Ref                  string
	PreviousTitle        string
	CurrentTitle         string
	PreviousRefName      string
	CurrentRefName       string
	SourceType           string
	SourceOwner          string
	SourceRepo           string
	SourceNumber         int
	SourceTitle          string
	SourceURL            string
	IsCrossRepository    bool
	WillCloseTarget      bool
}

type PullRequestReviewThread struct {
	NodeID            string
	IsResolved        bool
	IsOutdated        bool
	Path              string
	Side              string
	StartLine         *int
	OriginalStartLine *int
	Line              int
	OriginalLine      int
	Comments          []PullRequestReviewThreadComment
}

type PullRequestReviewThreadComment struct {
	NodeID           string
	DatabaseID       int64
	ReviewDatabaseID int64
	SubjectType      string
	Body             string
	AuthorLogin      string
	Path             string
	Line             int
	OriginalLine     int
	DiffHunk         string
	URL              string
	CommitID         string
	OriginalCommitID string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// EditPullRequestOpts holds optional fields for editing a pull request.
// Nil pointer fields are omitted from the GitHub API call.
type EditPullRequestOpts struct {
	State *string
	Title *string
	Body  *string
}

// Client is the interface for interacting with the GitHub API.
type Client interface {
	ListOpenPullRequests(ctx context.Context, owner, repo string) ([]*gh.PullRequest, error)
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*gh.PullRequest, error)
	GetUser(ctx context.Context, login string) (*gh.User, error)
	ListRepositoriesByOwner(ctx context.Context, owner string) ([]*gh.Repository, error)
	ListReleases(ctx context.Context, owner, repo string, perPage int) ([]*gh.RepositoryRelease, error)
	ListTags(ctx context.Context, owner, repo string, perPage int) ([]*gh.RepositoryTag, error)
	ListOpenIssues(ctx context.Context, owner, repo string) ([]*gh.Issue, error)
	GetIssue(ctx context.Context, owner, repo string, number int) (*gh.Issue, error)
	CreateIssue(ctx context.Context, owner, repo, title, body string) (*gh.Issue, error)
	ListIssueComments(ctx context.Context, owner, repo string, number int) ([]*gh.IssueComment, error)
	ListIssueCommentsIfChanged(ctx context.Context, owner, repo string, number int) ([]*gh.IssueComment, error)
	ListReviews(ctx context.Context, owner, repo string, number int) ([]*gh.PullRequestReview, error)
	ListPullRequestReviewThreads(ctx context.Context, owner, repo string, number int) ([]PullRequestReviewThread, error)
	ListCommits(ctx context.Context, owner, repo string, number int) ([]*gh.RepositoryCommit, error)
	ListPullRequestTimelineEvents(ctx context.Context, owner, repo string, number int) ([]PullRequestTimelineEvent, error)
	ListForcePushEvents(ctx context.Context, owner, repo string, number int) ([]ForcePushEvent, error)
	GetCombinedStatus(ctx context.Context, owner, repo, ref string) (*gh.CombinedStatus, error)
	ListCheckRunsForRef(ctx context.Context, owner, repo, ref string) ([]*gh.CheckRun, error)
	ListWorkflowRunsForHeadSHA(ctx context.Context, owner, repo, headSHA string) ([]*gh.WorkflowRun, error)
	ApproveWorkflowRun(ctx context.Context, owner, repo string, runID int64) error
	CreateIssueComment(ctx context.Context, owner, repo string, number int, body string) (*gh.IssueComment, error)
	EditIssueComment(ctx context.Context, owner, repo string, commentID int64, body string) (*gh.IssueComment, error)
	CreatePullRequestReviewCommentReply(ctx context.Context, owner, repo string, number int, body string, commentID int64) (*gh.PullRequestComment, error)
	GetRepository(ctx context.Context, owner, repo string) (*gh.Repository, error)
	CreateReview(ctx context.Context, owner, repo string, number int, event string, body string) (*gh.PullRequestReview, error)
	CreateReviewWithComments(
		ctx context.Context,
		owner, repo string,
		number int,
		event string,
		body string,
		commitID string,
		comments []*gh.DraftReviewComment,
	) (*gh.PullRequestReview, error)
	MarkPullRequestReadyForReview(ctx context.Context, owner, repo string, number int) (*gh.PullRequest, error)
	MergePullRequest(ctx context.Context, owner, repo string, number int, commitTitle, commitMessage, method string) (*gh.PullRequestMergeResult, error)
	EditPullRequest(ctx context.Context, owner, repo string, number int, opts EditPullRequestOpts) (*gh.PullRequest, error)
	EditIssue(ctx context.Context, owner, repo string, number int, state string) (*gh.Issue, error)
	EditIssueContent(ctx context.Context, owner, repo string, number int, title *string, body *string) (*gh.Issue, error)
	ListPullRequestsPage(ctx context.Context, owner, repo, state string, page int) ([]*gh.PullRequest, bool, error)
	ListIssuesPage(ctx context.Context, owner, repo, state string, page int) ([]*gh.Issue, bool, error)
	// InvalidateListETagsForRepo drops cached conditional-GET
	// validators for the given repo's list endpoints so the next
	// list call issues an unconditional fetch. The endpoints
	// parameter selects which caches to clear ("pulls", "issues",
	// "comments"); passing no endpoints clears every supported
	// repo-scoped list path. Used to recover from a partial-failure
	// sync.
	InvalidateListETagsForRepo(owner, repo string, endpoints ...string)
}

type conditionalPullRequestGetter interface {
	GetPullRequestIfChanged(
		ctx context.Context,
		owner, repo string,
		number int,
		etag string,
	) (*gh.PullRequest, string, bool, error)
}

type conditionalIssueGetter interface {
	GetIssueIfChanged(
		ctx context.Context,
		owner, repo string,
		number int,
		etag string,
	) (*gh.Issue, string, bool, error)
}

type issueTimelineLister interface {
	ListIssueTimelineEvents(
		ctx context.Context,
		owner, repo string,
		number int,
	) ([]PullRequestTimelineEvent, error)
}

func graphQLEndpointForHost(platformHost string) string {
	if platformHost == "" || platformHost == "github.com" {
		return "https://api.github.com/graphql"
	}
	return "https://" + platformHost + "/api/graphql"

}

// NewClient creates a GitHub Client authenticated with the given
// token. platformHost selects the API endpoint: "" or "github.com"
// uses the public API; any other value creates an Enterprise
// client. rateTracker and budget may be nil.
func NewClient(
	token string,
	platformHost string,
	rateTracker *RateTracker,
	budget *SyncBudget,
) (Client, error) {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(context.Background(), ts)
	et := &etagTransport{base: tc.Transport}
	if budget != nil {
		tc.Transport = &budgetTransport{base: et, budget: budget}
	} else {
		tc.Transport = et
	}

	var ghClient *gh.Client
	if platformHost == "" || platformHost == "github.com" {
		ghClient = gh.NewClient(tc)
	} else {
		baseURL := "https://" + platformHost + "/api/v3/"
		uploadURL := "https://" + platformHost +
			"/api/uploads/"
		var err error
		ghClient, err = gh.NewClient(tc).
			WithEnterpriseURLs(baseURL, uploadURL)
		if err != nil {
			return nil, fmt.Errorf(
				"create enterprise client: %w", err,
			)
		}
	}
	return &liveClient{
		gh:              ghClient,
		httpClient:      tc,
		rateTracker:     rateTracker,
		platformHost:    platformHost,
		graphQLEndpoint: graphQLEndpointForHost(platformHost),
		etag:            et,
	}, nil
}

type liveClient struct {
	gh              *gh.Client
	httpClient      *http.Client
	rateTracker     *RateTracker
	platformHost    string
	graphQLEndpoint string
	etag            *etagTransport
	viewerMu        sync.Mutex
	viewerLogin     string
}

// InvalidateListETagsForRepo evicts cached ETag entries for the repo's
// list endpoints. Pass any combination of "pulls", "issues", and
// "comments" to scope the invalidation; omitting endpoints clears
// every supported repo-scoped list path. Safe to call when the
// transport is nil (tests).
func (c *liveClient) InvalidateListETagsForRepo(owner, repo string, endpoints ...string) {
	if c.etag == nil {
		return
	}
	c.etag.invalidateRepo(owner, repo, endpoints...)
}

func (c *liveClient) ListReleases(
	ctx context.Context, owner, repo string, perPage int,
) ([]*gh.RepositoryRelease, error) {
	if perPage < 1 {
		perPage = 1
	}
	releases, resp, err := c.gh.Repositories.ListReleases(ctx, owner, repo, &gh.ListOptions{
		PerPage: perPage,
	})
	c.trackRate(resp)
	if err != nil {
		return nil, err
	}
	return releases, nil
}

func (c *liveClient) ListTags(
	ctx context.Context, owner, repo string, perPage int,
) ([]*gh.RepositoryTag, error) {
	if perPage < 1 {
		perPage = 1
	}
	tags, resp, err := c.gh.Repositories.ListTags(ctx, owner, repo, &gh.ListOptions{
		PerPage: perPage,
	})
	c.trackRate(resp)
	if err != nil {
		return nil, err
	}
	return tags, nil
}

func (c *liveClient) ListRepoLabels(
	ctx context.Context, owner, repo string,
) ([]*gh.Label, error) {
	var all []*gh.Label
	opts := &gh.ListOptions{PerPage: 100}
	for {
		labels, resp, err := c.gh.Issues.ListLabels(ctx, owner, repo, opts)
		c.trackRate(resp)
		if err != nil {
			return nil, err
		}
		all = append(all, labels...)
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

func (c *liveClient) ReplaceIssueLabels(
	ctx context.Context, owner, repo string, number int, names []string,
) ([]*gh.Label, error) {
	labels, resp, err := c.gh.Issues.ReplaceLabelsForIssue(ctx, owner, repo, number, names)
	c.trackRate(resp)
	if err != nil {
		return nil, err
	}
	return labels, nil
}

func (c *liveClient) CreateIssue(
	ctx context.Context, owner, repo, title, body string,
) (*gh.Issue, error) {
	req := &gh.IssueRequest{
		Title: &title,
	}
	if body != "" {
		req.Body = &body
	}
	issue, _, err := c.gh.Issues.Create(ctx, owner, repo, req)
	if err != nil {
		return nil, err
	}
	return issue, nil
}

const pullRequestTimelineEventsQuery = `
query($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      timelineItems(itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT, COMMENT_DELETED_EVENT, CROSS_REFERENCED_EVENT, RENAMED_TITLE_EVENT, BASE_REF_CHANGED_EVENT, ASSIGNED_EVENT, UNASSIGNED_EVENT], first: 100, after: $cursor) {
        nodes {
          __typename
          ... on Node {
            id
          }
          ... on HeadRefForcePushedEvent {
            actor { login }
            beforeCommit { oid }
            afterCommit { oid }
            createdAt
            ref { name }
          }
          ... on CommentDeletedEvent {
            actor { login }
            createdAt
            deletedCommentAuthor { login }
          }
          ... on CrossReferencedEvent {
            actor { login }
            createdAt
            isCrossRepository
            willCloseTarget
            source {
              __typename
              ... on Issue {
                number
                title
                url
                repository {
                  owner { login }
                  name
                }
              }
              ... on PullRequest {
                number
                title
                url
                repository {
                  owner { login }
                  name
                }
              }
            }
          }
          ... on RenamedTitleEvent {
            actor { login }
            createdAt
            previousTitle
            currentTitle
          }
          ... on BaseRefChangedEvent {
            actor { login }
            createdAt
            previousRefName
            currentRefName
          }
          ... on AssignedEvent {
            actor { login }
            assignee {
              __typename
              ... on Bot { login }
              ... on Mannequin { login }
              ... on Organization { login }
              ... on User { login }
            }
            createdAt
          }
          ... on UnassignedEvent {
            actor { login }
            assignee {
              __typename
              ... on Bot { login }
              ... on Mannequin { login }
              ... on Organization { login }
              ... on User { login }
            }
            createdAt
          }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`

const issueTimelineEventsQuery = `
query($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    issue(number: $number) {
      timelineItems(itemTypes: [ASSIGNED_EVENT, UNASSIGNED_EVENT], first: 100, after: $cursor) {
        nodes {
          __typename
          ... on Node {
            id
          }
          ... on AssignedEvent {
            actor { login }
            assignee {
              __typename
              ... on Bot { login }
              ... on Mannequin { login }
              ... on Organization { login }
              ... on User { login }
            }
            createdAt
          }
          ... on UnassignedEvent {
            actor { login }
            assignee {
              __typename
              ... on Bot { login }
              ... on Mannequin { login }
              ... on Organization { login }
              ... on User { login }
            }
            createdAt
          }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`

const readyForReviewIDQuery = `
query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      id
    }
  }
}`

const readyForReviewMutation = `
mutation($pullRequestId: ID!) {
  markPullRequestReadyForReview(input: {pullRequestId: $pullRequestId}) {
    pullRequest {
      databaseId
      number
      title
      state
      isDraft
      locked
      body
      url
      author {
        login
      }
      createdAt
      updatedAt
      mergedAt
      closedAt
      additions
      deletions
      mergeable
      reviewDecision
      headRefName
      baseRefName
      headRefOid
      baseRefOid
      headRepository {
        url
      }
      labels(first: 100) {
        nodes {
          name
          color
          description
          isDefault
        }
      }
    }
  }
}`

const pullRequestReviewThreadsQuery = `
query($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $cursor) {
        nodes {
          id
          isResolved
          isOutdated
          path
          line
          originalLine
          startLine
          originalStartLine
          diffSide
          comments(first: 100) {
            nodes {
              id
              databaseId
              fullDatabaseId
              body
              path
              line
              originalLine
              subjectType
              diffHunk
              url
              author { login }
              commit { oid }
              originalCommit { oid }
              pullRequestReview { databaseId }
              createdAt
              updatedAt
            }
            pageInfo {
              hasNextPage
              endCursor
            }
          }
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`

const pullRequestReviewThreadCommentsQuery = `
query($threadID: ID!, $cursor: String) {
  node(id: $threadID) {
    ... on PullRequestReviewThread {
      comments(first: 100, after: $cursor) {
        nodes {
          id
          databaseId
          fullDatabaseId
          body
          path
          line
          originalLine
          subjectType
          diffHunk
          url
          author { login }
          commit { oid }
          originalCommit { oid }
          pullRequestReview { databaseId }
          createdAt
          updatedAt
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
}`

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type graphQLError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type graphQLReviewThreadComment struct {
	NodeID         string       `json:"id"`
	DatabaseID     graphQLInt64 `json:"databaseId"`
	FullDatabaseID graphQLInt64 `json:"fullDatabaseId"`
	Body           string       `json:"body"`
	Path           string       `json:"path"`
	Line           int          `json:"line"`
	OriginalLine   int          `json:"originalLine"`
	SubjectType    string       `json:"subjectType"`
	DiffHunk       string       `json:"diffHunk"`
	URL            string       `json:"url"`
	Author         *struct {
		Login string `json:"login"`
	} `json:"author"`
	Commit *struct {
		OID string `json:"oid"`
	} `json:"commit"`
	OriginalCommit *struct {
		OID string `json:"oid"`
	} `json:"originalCommit"`
	PullRequestReview *struct {
		DatabaseID int64 `json:"databaseId"`
	} `json:"pullRequestReview"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type graphQLInt64 int64

func (value *graphQLInt64) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "" || text == "null" {
		*value = 0
		return nil
	}
	if strings.HasPrefix(text, `"`) {
		unquoted, err := strconv.Unquote(text)
		if err != nil {
			return fmt.Errorf("decode GraphQL int64: %w", err)
		}
		text = unquoted
		if text == "" {
			*value = 0
			return nil
		}
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("decode GraphQL int64 %q: %w", text, err)
	}
	*value = graphQLInt64(parsed)
	return nil
}

type graphQLReviewThreadCommentConnection struct {
	Nodes    []graphQLReviewThreadComment `json:"nodes"`
	PageInfo struct {
		HasNextPage bool    `json:"hasNextPage"`
		EndCursor   *string `json:"endCursor"`
	} `json:"pageInfo"`
}

type readyForReviewError struct {
	err        error
	statusCode int
	staleState bool
}

func (e *readyForReviewError) Error() string      { return e.err.Error() }
func (e *readyForReviewError) Unwrap() error      { return e.err }
func (e *readyForReviewError) StatusCode() int    { return e.statusCode }
func (e *readyForReviewError) IsStaleState() bool { return e.staleState }

func newReadyForReviewError(err error, statusCode int, staleState bool) error {
	return &readyForReviewError{
		err:        err,
		statusCode: statusCode,
		staleState: staleState,
	}
}

func readyForReviewGraphQLErrorMeta(graphQLErrors []graphQLError) (int, bool) {
	for _, graphQLError := range graphQLErrors {
		if strings.EqualFold(graphQLError.Type, "NOT_FOUND") {
			return http.StatusNotFound, true
		}
		if strings.Contains(graphQLError.Message, "Could not resolve to a PullRequest") ||
			strings.Contains(graphQLError.Message, "Could not resolve to a node with the global id") {
			return http.StatusNotFound, true
		}
	}
	return 0, false
}

func joinGraphQLErrorMessages(graphQLErrors []graphQLError) string {
	messages := make([]string, 0, len(graphQLErrors))
	for _, graphQLError := range graphQLErrors {
		if graphQLError.Message != "" {
			messages = append(messages, graphQLError.Message)
		}
	}
	if len(messages) == 0 {
		return "unknown GraphQL error"
	}
	return strings.Join(messages, "; ")
}

// trackRate records the request and updates rate limit state
// from the response. Safe to call with nil response or nil
// tracker.
func (c *liveClient) trackRate(resp *gh.Response) {
	if resp == nil || c.rateTracker == nil {
		return
	}
	c.rateTracker.RecordRequest()
	c.rateTracker.UpdateFromRate(rateFromGitHub(resp.Rate))
}

func (c *liveClient) trackRateHeaders(resp *http.Response) {
	if resp == nil || c.rateTracker == nil {
		return
	}
	c.rateTracker.RecordRequest()
	remaining, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	if err != nil {
		return
	}
	resetUnix, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		return
	}
	c.rateTracker.UpdateFromRate(rateFromGitHubHeaders(
		0, remaining, time.Unix(resetUnix, 0).UTC(),
	))
}

func (c *liveClient) ListOpenPullRequests(ctx context.Context, owner, repo string) ([]*gh.PullRequest, error) {
	opts := &gh.PullRequestListOptions{
		State:       "open",
		ListOptions: gh.ListOptions{PerPage: 100},
	}
	progress := newMergeRequestListFetchProgressLogger(RepoRef{
		Owner:        owner,
		Name:         repo,
		PlatformHost: c.platformHost,
	}, "rest")
	all, err := collectPagesWithProgress(ctx, func(pageOpts *gh.ListOptions) ([]*gh.PullRequest, *gh.Response, error) {
		opts.ListOptions = *pageOpts
		page, resp, err := c.gh.PullRequests.List(ctx, owner, repo, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("listing open pull requests for %s/%s: %w", owner, repo, err)
		}
		return page, resp, nil
	}, c.trackRate, progress.recordPage)
	if err != nil {
		return nil, err
	}
	progress.done()
	return all, nil
}

func (c *liveClient) ListOpenIssues(
	ctx context.Context, owner, repo string,
) ([]*gh.Issue, error) {
	opts := &gh.IssueListByRepoOptions{
		State:       "open",
		Sort:        "updated",
		Direction:   "desc",
		ListOptions: gh.ListOptions{PerPage: 100},
	}
	progress := newIssueListFetchProgressLogger(RepoRef{
		Owner:        owner,
		Name:         repo,
		PlatformHost: c.platformHost,
	}, "rest")
	issues, err := collectPagesWithProgress(ctx, func(pageOpts *gh.ListOptions) ([]*gh.Issue, *gh.Response, error) {
		opts.ListOptions = *pageOpts
		issues, resp, err := c.gh.Issues.ListByRepo(
			ctx, owner, repo, opts,
		)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"listing issues for %s/%s: %w", owner, repo, err,
			)
		}
		return issues, resp, nil
	}, c.trackRate, progress.recordPage)
	if err != nil {
		return nil, err
	}
	progress.done()

	var all []*gh.Issue
	// GitHub's Issues API returns PRs too — filter them out.
	for _, issue := range issues {
		if issue.PullRequestLinks == nil {
			all = append(all, issue)
		}
	}
	return all, nil
}

func (c *liveClient) ListRepositoriesByOwner(
	ctx context.Context, owner string,
) ([]*gh.Repository, error) {
	viewerLogin, viewerErr := c.authenticatedLogin(ctx)
	if viewerErr == nil && strings.EqualFold(owner, viewerLogin) {
		repos, err := collectPages(
			ctx,
			func(opts *gh.ListOptions) ([]*gh.Repository, *gh.Response, error) {
				page, resp, err := c.gh.Repositories.ListByAuthenticatedUser(
					ctx, &gh.RepositoryListByAuthenticatedUserOptions{
						Affiliation: "owner",
						ListOptions: *opts,
					},
				)
				if err != nil {
					return nil, resp, err
				}
				return page, resp, nil
			},
			c.trackRate,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"listing repositories for authenticated owner %s: %w",
				owner, err,
			)
		}
		return repos, nil
	}

	orgRepos, err := collectPages(
		ctx,
		func(opts *gh.ListOptions) ([]*gh.Repository, *gh.Response, error) {
			page, resp, err := c.gh.Repositories.ListByOrg(
				ctx, owner, &gh.RepositoryListByOrgOptions{
					Type:        "all",
					ListOptions: *opts,
				},
			)
			if err != nil {
				return nil, resp, err
			}
			return page, resp, nil
		},
		c.trackRate,
	)
	if err == nil {
		return orgRepos, nil
	}

	userRepos, userErr := collectPages(
		ctx,
		func(opts *gh.ListOptions) ([]*gh.Repository, *gh.Response, error) {
			page, resp, err := c.gh.Repositories.ListByUser(
				ctx, owner, &gh.RepositoryListByUserOptions{
					Type:        "owner",
					ListOptions: *opts,
				},
			)
			if err != nil {
				return nil, resp, err
			}
			return page, resp, nil
		},
		c.trackRate,
	)
	if userErr != nil {
		return nil, fmt.Errorf(
			"listing repositories for %s: org=%v user=%w",
			owner, err, userErr,
		)
	}
	return userRepos, nil
}

func (c *liveClient) authenticatedLogin(ctx context.Context) (string, error) {
	c.viewerMu.Lock()
	defer c.viewerMu.Unlock()
	if c.viewerLogin != "" {
		return c.viewerLogin, nil
	}
	user, resp, err := c.gh.Users.Get(ctx, "")
	c.trackRate(resp)
	if err != nil {
		return "", fmt.Errorf("getting authenticated user: %w", err)
	}
	login := user.GetLogin()
	if login == "" {
		return "", fmt.Errorf("authenticated user login is empty")
	}
	c.viewerLogin = login
	return login, nil
}

func (c *liveClient) GetIssue(
	ctx context.Context, owner, repo string, number int,
) (*gh.Issue, error) {
	issue, resp, err := c.gh.Issues.Get(ctx, owner, repo, number)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"getting issue %s/%s#%d: %w", owner, repo, number, err,
		)
	}
	return issue, nil
}

func (c *liveClient) GetIssueIfChanged(
	ctx context.Context,
	owner, repo string,
	number int,
	etag string,
) (*gh.Issue, string, bool, error) {
	u := fmt.Sprintf("repos/%v/%v/issues/%v", owner, repo, number)
	req, err := c.gh.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, "", false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	issue := new(gh.Issue)
	resp, err := c.gh.Do(ctx, req, issue)
	c.trackRate(resp)
	if err != nil {
		if IsNotModified(err) {
			return nil, etag, true, nil
		}
		return nil, "", false, fmt.Errorf(
			"getting issue %s/%s#%d: %w", owner, repo, number, err,
		)
	}
	if resp != nil && resp.Response != nil {
		etag = resp.Header.Get("ETag")
	}
	return issue, etag, false, nil
}

func (c *liveClient) GetPullRequest(ctx context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
	pr, resp, err := c.gh.PullRequests.Get(ctx, owner, repo, number)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf("getting pull request %s/%s#%d: %w", owner, repo, number, err)
	}
	return pr, nil
}

func (c *liveClient) GetPullRequestIfChanged(
	ctx context.Context,
	owner, repo string,
	number int,
	etag string,
) (*gh.PullRequest, string, bool, error) {
	u := fmt.Sprintf("repos/%v/%v/pulls/%v", owner, repo, number)
	req, err := c.gh.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, "", false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	pr := new(gh.PullRequest)
	resp, err := c.gh.Do(ctx, req, pr)
	c.trackRate(resp)
	if err != nil {
		if IsNotModified(err) {
			return nil, etag, true, nil
		}
		return nil, "", false, fmt.Errorf("getting pull request %s/%s#%d: %w", owner, repo, number, err)
	}
	if resp != nil && resp.Response != nil {
		etag = resp.Header.Get("ETag")
	}
	return pr, etag, false, nil
}

func (c *liveClient) GetUser(ctx context.Context, login string) (*gh.User, error) {
	user, resp, err := c.gh.Users.Get(ctx, login)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf("getting user %s: %w", login, err)
	}
	return user, nil
}

func (c *liveClient) ListIssueComments(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.IssueComment, error) {
	return c.listIssueComments(withBypassETag(ctx), owner, repo, number)
}

func (c *liveClient) ListIssueCommentsIfChanged(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.IssueComment, error) {
	return c.listIssueComments(ctx, owner, repo, number)
}

func (c *liveClient) listIssueComments(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.IssueComment, error) {
	opts := &gh.IssueListCommentsOptions{
		ListOptions: gh.ListOptions{PerPage: 100},
	}
	all, err := collectPages(ctx, func(pageOpts *gh.ListOptions) ([]*gh.IssueComment, *gh.Response, error) {
		opts.ListOptions = *pageOpts
		page, resp, err := c.gh.Issues.ListComments(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("listing comments for %s/%s#%d: %w", owner, repo, number, err)
		}
		return page, resp, nil
	}, c.trackRate)
	if err != nil {
		return nil, err
	}
	return all, nil
}

func (c *liveClient) ListReviews(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.PullRequestReview, error) {
	all, err := collectPages(ctx, func(opts *gh.ListOptions) ([]*gh.PullRequestReview, *gh.Response, error) {
		page, resp, err := c.gh.PullRequests.ListReviews(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("listing reviews for %s/%s#%d: %w", owner, repo, number, err)
		}
		return page, resp, nil
	}, c.trackRate)
	if err != nil {
		return nil, err
	}
	return all, nil
}

func (c *liveClient) ListPullRequestReviewThreads(
	ctx context.Context,
	owner string,
	repo string,
	number int,
) ([]PullRequestReviewThread, error) {
	type graphQLResponse struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Repository *struct {
				PullRequest *struct {
					ReviewThreads struct {
						Nodes []struct {
							NodeID            string                               `json:"id"`
							IsResolved        bool                                 `json:"isResolved"`
							IsOutdated        bool                                 `json:"isOutdated"`
							Path              string                               `json:"path"`
							Line              int                                  `json:"line"`
							OriginalLine      int                                  `json:"originalLine"`
							StartLine         *int                                 `json:"startLine"`
							OriginalStartLine *int                                 `json:"originalStartLine"`
							Side              string                               `json:"diffSide"`
							Comments          graphQLReviewThreadCommentConnection `json:"comments"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool    `json:"hasNextPage"`
							EndCursor   *string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}

	var threads []PullRequestReviewThread
	var cursor *string
	for {
		payload, err := json.Marshal(graphQLRequest{
			Query: pullRequestReviewThreadsQuery,
			Variables: map[string]any{
				"owner":  owner,
				"repo":   repo,
				"number": number,
				"cursor": cursor,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("marshal pull request review threads query: %w", err)
		}

		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			c.graphQLEndpoint,
			bytes.NewReader(payload),
		)
		if err != nil {
			return nil, fmt.Errorf("create pull request review threads request: %w", err)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf(
				"list pull request review threads for %s/%s#%d: %w",
				owner, repo, number, err,
			)
		}
		c.trackRateHeaders(resp)
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf(
				"list pull request review threads for %s/%s#%d: graphql status %s",
				owner, repo, number, resp.Status,
			)
		}

		var decoded graphQLResponse
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf(
				"decode pull request review threads for %s/%s#%d: %w",
				owner, repo, number, err,
			)
		}
		_ = resp.Body.Close()

		if len(decoded.Errors) > 0 {
			return nil, fmt.Errorf(
				"list pull request review threads for %s/%s#%d: graphql errors: %s",
				owner, repo, number, joinGraphQLErrorMessages(decoded.Errors),
			)
		}
		if decoded.Data.Repository == nil {
			return nil, fmt.Errorf(
				"list pull request review threads for %s/%s#%d: missing repository in graphql response",
				owner, repo, number,
			)
		}
		if decoded.Data.Repository.PullRequest == nil {
			return nil, fmt.Errorf(
				"list pull request review threads for %s/%s#%d: missing pull request in graphql response",
				owner, repo, number,
			)
		}

		for _, node := range decoded.Data.Repository.PullRequest.ReviewThreads.Nodes {
			thread := PullRequestReviewThread{
				NodeID:            node.NodeID,
				IsResolved:        node.IsResolved,
				IsOutdated:        node.IsOutdated,
				Path:              node.Path,
				Side:              node.Side,
				StartLine:         node.StartLine,
				OriginalStartLine: node.OriginalStartLine,
				Line:              node.Line,
				OriginalLine:      node.OriginalLine,
				Comments:          make([]PullRequestReviewThreadComment, 0, len(node.Comments.Nodes)),
			}
			for _, comment := range node.Comments.Nodes {
				thread.Comments = append(thread.Comments, githubReviewThreadCommentFromGraphQL(comment))
			}
			if node.Comments.PageInfo.HasNextPage && node.Comments.PageInfo.EndCursor != nil {
				comments, err := c.listPullRequestReviewThreadComments(
					ctx, owner, repo, number, node.NodeID, node.Comments.PageInfo.EndCursor,
				)
				if err != nil {
					return nil, err
				}
				thread.Comments = append(thread.Comments, comments...)
			}
			threads = append(threads, thread)
		}

		pageInfo := decoded.Data.Repository.PullRequest.ReviewThreads.PageInfo
		if !pageInfo.HasNextPage || pageInfo.EndCursor == nil {
			break
		}
		cursor = pageInfo.EndCursor
	}
	return threads, nil
}

func (c *liveClient) listPullRequestReviewThreadComments(
	ctx context.Context,
	owner string,
	repo string,
	number int,
	threadID string,
	cursor *string,
) ([]PullRequestReviewThreadComment, error) {
	type graphQLResponse struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Node *struct {
				Comments graphQLReviewThreadCommentConnection `json:"comments"`
			} `json:"node"`
		} `json:"data"`
	}

	var comments []PullRequestReviewThreadComment
	for {
		payload, err := json.Marshal(graphQLRequest{
			Query: pullRequestReviewThreadCommentsQuery,
			Variables: map[string]any{
				"threadID": threadID,
				"cursor":   cursor,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("marshal pull request review thread comments query: %w", err)
		}

		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			c.graphQLEndpoint,
			bytes.NewReader(payload),
		)
		if err != nil {
			return nil, fmt.Errorf("create pull request review thread comments request: %w", err)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf(
				"list pull request review thread comments for %s/%s#%d thread %s: %w",
				owner, repo, number, threadID, err,
			)
		}
		c.trackRateHeaders(resp)
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf(
				"list pull request review thread comments for %s/%s#%d thread %s: graphql status %s",
				owner, repo, number, threadID, resp.Status,
			)
		}

		var decoded graphQLResponse
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf(
				"decode pull request review thread comments for %s/%s#%d thread %s: %w",
				owner, repo, number, threadID, err,
			)
		}
		_ = resp.Body.Close()

		if len(decoded.Errors) > 0 {
			return nil, fmt.Errorf(
				"list pull request review thread comments for %s/%s#%d thread %s: graphql errors: %s",
				owner, repo, number, threadID, joinGraphQLErrorMessages(decoded.Errors),
			)
		}
		if decoded.Data.Node == nil {
			return nil, fmt.Errorf(
				"list pull request review thread comments for %s/%s#%d thread %s: missing node in graphql response",
				owner, repo, number, threadID,
			)
		}

		for _, comment := range decoded.Data.Node.Comments.Nodes {
			comments = append(comments, githubReviewThreadCommentFromGraphQL(comment))
		}
		pageInfo := decoded.Data.Node.Comments.PageInfo
		if !pageInfo.HasNextPage || pageInfo.EndCursor == nil {
			return comments, nil
		}
		cursor = pageInfo.EndCursor
	}
}

func githubReviewThreadCommentFromGraphQL(
	comment graphQLReviewThreadComment,
) PullRequestReviewThreadComment {
	next := PullRequestReviewThreadComment{
		NodeID:       comment.NodeID,
		DatabaseID:   firstPositiveInt64(int64(comment.FullDatabaseID), int64(comment.DatabaseID)),
		SubjectType:  comment.SubjectType,
		Body:         comment.Body,
		Path:         comment.Path,
		Line:         comment.Line,
		OriginalLine: comment.OriginalLine,
		DiffHunk:     comment.DiffHunk,
		URL:          comment.URL,
		CreatedAt:    comment.CreatedAt,
		UpdatedAt:    comment.UpdatedAt,
	}
	if comment.Author != nil {
		next.AuthorLogin = comment.Author.Login
	}
	if comment.Commit != nil {
		next.CommitID = comment.Commit.OID
	}
	if comment.OriginalCommit != nil {
		next.OriginalCommitID = comment.OriginalCommit.OID
	}
	if comment.PullRequestReview != nil {
		next.ReviewDatabaseID = comment.PullRequestReview.DatabaseID
	}
	return next
}

func firstPositiveInt64(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func (c *liveClient) ListCommits(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.RepositoryCommit, error) {
	all, err := collectPages(ctx, func(opts *gh.ListOptions) ([]*gh.RepositoryCommit, *gh.Response, error) {
		page, resp, err := c.gh.PullRequests.ListCommits(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("listing commits for %s/%s#%d: %w", owner, repo, number, err)
		}
		return page, resp, nil
	}, c.trackRate)
	if err != nil {
		return nil, err
	}
	return all, nil
}

func (c *liveClient) ListPullRequestTimelineEvents(
	ctx context.Context, owner, repo string, number int,
) ([]PullRequestTimelineEvent, error) {
	type graphQLResponse struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Repository *struct {
				PullRequest *struct {
					TimelineItems struct {
						Nodes []struct {
							TypeName string `json:"__typename"`
							ID       string `json:"id"`
							Actor    *struct {
								Login string `json:"login"`
							} `json:"actor"`
							Assignee *struct {
								TypeName string `json:"__typename"`
								Login    string `json:"login"`
							} `json:"assignee"`
							BeforeCommit *struct {
								OID string `json:"oid"`
							} `json:"beforeCommit"`
							AfterCommit *struct {
								OID string `json:"oid"`
							} `json:"afterCommit"`
							CreatedAt            time.Time              `json:"createdAt"`
							Ref                  *struct{ Name string } `json:"ref"`
							DeletedCommentAuthor *struct {
								Login string `json:"login"`
							} `json:"deletedCommentAuthor"`
							PreviousTitle   string `json:"previousTitle"`
							CurrentTitle    string `json:"currentTitle"`
							PreviousRefName string `json:"previousRefName"`
							CurrentRefName  string `json:"currentRefName"`
							Source          *struct {
								TypeName   string `json:"__typename"`
								Number     int    `json:"number"`
								Title      string `json:"title"`
								URL        string `json:"url"`
								Repository *struct {
									Owner *struct {
										Login string `json:"login"`
									} `json:"owner"`
									Name string `json:"name"`
								} `json:"repository"`
							} `json:"source"`
							IsCrossRepository bool `json:"isCrossRepository"`
							WillCloseTarget   bool `json:"willCloseTarget"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool    `json:"hasNextPage"`
							EndCursor   *string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"timelineItems"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}

	var events []PullRequestTimelineEvent
	var cursor *string
	for {
		payload, err := json.Marshal(graphQLRequest{
			Query: pullRequestTimelineEventsQuery,
			Variables: map[string]any{
				"owner":  owner,
				"repo":   repo,
				"number": number,
				"cursor": cursor,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("marshal pull request timeline query: %w", err)
		}

		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			c.graphQLEndpoint,
			bytes.NewReader(payload),
		)
		if err != nil {
			return nil, fmt.Errorf("create pull request timeline request: %w", err)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf(
				"list pull request timeline events for %s/%s#%d: %w",
				owner, repo, number, err,
			)
		}
		c.trackRateHeaders(resp)
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf(
				"list pull request timeline events for %s/%s#%d: graphql status %s",
				owner, repo, number, resp.Status,
			)
		}

		var decoded graphQLResponse
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf(
				"decode pull request timeline events for %s/%s#%d: %w",
				owner, repo, number, err,
			)
		}
		_ = resp.Body.Close()

		if len(decoded.Errors) > 0 {
			return nil, fmt.Errorf(
				"list pull request timeline events for %s/%s#%d: graphql errors: %s",
				owner, repo, number, joinGraphQLErrorMessages(decoded.Errors),
			)
		}

		if decoded.Data.Repository == nil {
			return nil, fmt.Errorf(
				"list pull request timeline events for %s/%s#%d: missing repository in graphql response",
				owner, repo, number,
			)
		}
		if decoded.Data.Repository.PullRequest == nil {
			return nil, fmt.Errorf(
				"list pull request timeline events for %s/%s#%d: missing pull request in graphql response",
				owner, repo, number,
			)
		}

		for _, node := range decoded.Data.Repository.PullRequest.TimelineItems.Nodes {
			event := PullRequestTimelineEvent{
				NodeID:            node.ID,
				CreatedAt:         node.CreatedAt,
				PreviousTitle:     node.PreviousTitle,
				CurrentTitle:      node.CurrentTitle,
				PreviousRefName:   node.PreviousRefName,
				CurrentRefName:    node.CurrentRefName,
				IsCrossRepository: node.IsCrossRepository,
				WillCloseTarget:   node.WillCloseTarget,
			}
			switch node.TypeName {
			case "HeadRefForcePushedEvent":
				event.EventType = "force_push"
			case "CommentDeletedEvent":
				event.EventType = "comment_deleted"
			case "CrossReferencedEvent":
				event.EventType = "cross_referenced"
			case "RenamedTitleEvent":
				event.EventType = "renamed_title"
			case "BaseRefChangedEvent":
				event.EventType = "base_ref_changed"
			case "AssignedEvent":
				event.EventType = "assigned"
			case "UnassignedEvent":
				event.EventType = "unassigned"
			default:
				continue
			}
			if node.Actor != nil {
				event.Actor = node.Actor.Login
			}
			if node.Assignee != nil {
				event.Assignee = node.Assignee.Login
			}
			if node.BeforeCommit != nil {
				event.BeforeSHA = node.BeforeCommit.OID
			}
			if node.AfterCommit != nil {
				event.AfterSHA = node.AfterCommit.OID
			}
			if node.Ref != nil {
				event.Ref = node.Ref.Name
			}
			if node.DeletedCommentAuthor != nil {
				event.DeletedCommentAuthor = node.DeletedCommentAuthor.Login
			}
			if node.Source != nil {
				event.SourceType = node.Source.TypeName
				event.SourceNumber = node.Source.Number
				event.SourceTitle = node.Source.Title
				event.SourceURL = node.Source.URL
				if node.Source.Repository != nil {
					event.SourceRepo = node.Source.Repository.Name
					if node.Source.Repository.Owner != nil {
						event.SourceOwner = node.Source.Repository.Owner.Login
					}
				}
			}
			events = append(events, event)
		}

		pageInfo := decoded.Data.Repository.PullRequest.TimelineItems.PageInfo
		if !pageInfo.HasNextPage {
			break
		}
		cursor = pageInfo.EndCursor
	}

	return events, nil
}

func (c *liveClient) ListIssueTimelineEvents(
	ctx context.Context, owner, repo string, number int,
) ([]PullRequestTimelineEvent, error) {
	type graphQLResponse struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Repository *struct {
				Issue *struct {
					TimelineItems struct {
						Nodes []struct {
							TypeName string `json:"__typename"`
							ID       string `json:"id"`
							Actor    *struct {
								Login string `json:"login"`
							} `json:"actor"`
							Assignee *struct {
								TypeName string `json:"__typename"`
								Login    string `json:"login"`
							} `json:"assignee"`
							CreatedAt time.Time `json:"createdAt"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool    `json:"hasNextPage"`
							EndCursor   *string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"timelineItems"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}

	var events []PullRequestTimelineEvent
	var cursor *string
	for {
		payload, err := json.Marshal(graphQLRequest{
			Query: issueTimelineEventsQuery,
			Variables: map[string]any{
				"owner":  owner,
				"repo":   repo,
				"number": number,
				"cursor": cursor,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("marshal issue timeline query: %w", err)
		}

		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			c.graphQLEndpoint,
			bytes.NewReader(payload),
		)
		if err != nil {
			return nil, fmt.Errorf("create issue timeline request: %w", err)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf(
				"list issue timeline events for %s/%s#%d: %w",
				owner, repo, number, err,
			)
		}
		c.trackRateHeaders(resp)
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf(
				"list issue timeline events for %s/%s#%d: graphql status %s",
				owner, repo, number, resp.Status,
			)
		}

		var decoded graphQLResponse
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf(
				"decode issue timeline events for %s/%s#%d: %w",
				owner, repo, number, err,
			)
		}
		_ = resp.Body.Close()

		if len(decoded.Errors) > 0 {
			return nil, fmt.Errorf(
				"list issue timeline events for %s/%s#%d: graphql errors: %s",
				owner, repo, number, joinGraphQLErrorMessages(decoded.Errors),
			)
		}
		if decoded.Data.Repository == nil {
			return nil, fmt.Errorf(
				"list issue timeline events for %s/%s#%d: missing repository in graphql response",
				owner, repo, number,
			)
		}
		if decoded.Data.Repository.Issue == nil {
			return nil, fmt.Errorf(
				"list issue timeline events for %s/%s#%d: missing issue in graphql response",
				owner, repo, number,
			)
		}

		for _, node := range decoded.Data.Repository.Issue.TimelineItems.Nodes {
			event := PullRequestTimelineEvent{
				NodeID:    node.ID,
				CreatedAt: node.CreatedAt,
			}
			switch node.TypeName {
			case "AssignedEvent":
				event.EventType = "assigned"
			case "UnassignedEvent":
				event.EventType = "unassigned"
			default:
				continue
			}
			if node.Actor != nil {
				event.Actor = node.Actor.Login
			}
			if node.Assignee != nil {
				event.Assignee = node.Assignee.Login
			}
			events = append(events, event)
		}

		pageInfo := decoded.Data.Repository.Issue.TimelineItems.PageInfo
		if !pageInfo.HasNextPage {
			break
		}
		cursor = pageInfo.EndCursor
	}

	return events, nil
}

func (c *liveClient) ListForcePushEvents(
	ctx context.Context, owner, repo string, number int,
) ([]ForcePushEvent, error) {
	timelineEvents, err := c.ListPullRequestTimelineEvents(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}

	events := make([]ForcePushEvent, 0, len(timelineEvents))
	for _, timelineEvent := range timelineEvents {
		if timelineEvent.EventType != "force_push" {
			continue
		}
		events = append(events, ForcePushEvent{
			Actor:     timelineEvent.Actor,
			BeforeSHA: timelineEvent.BeforeSHA,
			AfterSHA:  timelineEvent.AfterSHA,
			Ref:       timelineEvent.Ref,
			CreatedAt: timelineEvent.CreatedAt,
		})
	}
	return events, nil
}

func (c *liveClient) GetCombinedStatus(
	ctx context.Context, owner, repo, ref string,
) (*gh.CombinedStatus, error) {
	status, resp, err := c.gh.Repositories.GetCombinedStatus(ctx, owner, repo, ref, nil)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf("getting combined status for %s/%s@%s: %w", owner, repo, ref, err)
	}
	return status, nil
}

func (c *liveClient) ListCheckRunsForRef(
	ctx context.Context, owner, repo, ref string,
) ([]*gh.CheckRun, error) {
	opts := &gh.ListCheckRunsOptions{
		ListOptions: gh.ListOptions{PerPage: 100},
	}
	all, err := collectPages(ctx, func(pageOpts *gh.ListOptions) ([]*gh.CheckRun, *gh.Response, error) {
		opts.ListOptions = *pageOpts
		result, resp, err := c.gh.Checks.ListCheckRunsForRef(
			ctx, owner, repo, ref, opts,
		)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"listing check runs for %s/%s@%s: %w",
				owner, repo, ref, err,
			)
		}
		return result.CheckRuns, resp, nil
	}, c.trackRate)
	if err != nil {
		return nil, err
	}
	return all, nil
}

func (c *liveClient) ListWorkflowRunsForHeadSHA(
	ctx context.Context, owner, repo, headSHA string,
) ([]*gh.WorkflowRun, error) {
	opts := &gh.ListWorkflowRunsOptions{
		HeadSHA:     headSHA,
		Status:      "action_required",
		ListOptions: gh.ListOptions{PerPage: 100},
	}
	all, err := collectPages(ctx, func(pageOpts *gh.ListOptions) ([]*gh.WorkflowRun, *gh.Response, error) {
		opts.ListOptions = *pageOpts
		result, resp, err := c.gh.Actions.ListRepositoryWorkflowRuns(
			ctx, owner, repo, opts,
		)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"listing workflow runs for %s/%s@%s: %w",
				owner, repo, headSHA, err,
			)
		}
		return result.WorkflowRuns, resp, nil
	}, c.trackRate)
	if err != nil {
		return nil, err
	}
	return all, nil
}

func (c *liveClient) ApproveWorkflowRun(
	ctx context.Context, owner, repo string, runID int64,
) error {
	req, err := c.gh.NewRequest(
		"POST",
		fmt.Sprintf("repos/%s/%s/actions/runs/%d/approve", owner, repo, runID),
		nil,
	)
	if err != nil {
		return fmt.Errorf(
			"building workflow approval request for %s/%s run %d: %w",
			owner, repo, runID, err,
		)
	}

	resp, err := c.gh.Do(ctx, req, nil)
	c.trackRate(resp)
	if err != nil {
		return fmt.Errorf(
			"approving workflow run %s/%s#%d: %w",
			owner, repo, runID, err,
		)
	}
	return nil
}

func (c *liveClient) CreateIssueComment(
	ctx context.Context, owner, repo string, number int, body string,
) (*gh.IssueComment, error) {
	comment, resp, err := c.gh.Issues.CreateComment(ctx, owner, repo, number, &gh.IssueComment{
		Body: new(body),
	})
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf("creating comment on %s/%s#%d: %w", owner, repo, number, err)
	}
	return comment, nil
}

func (c *liveClient) EditIssueComment(
	ctx context.Context, owner, repo string, commentID int64, body string,
) (*gh.IssueComment, error) {
	comment, resp, err := c.gh.Issues.EditComment(
		ctx, owner, repo, commentID, &gh.IssueComment{Body: new(body)},
	)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"editing comment %d on %s/%s: %w", commentID, owner, repo, err,
		)
	}
	return comment, nil
}

func (c *liveClient) CreatePullRequestReviewCommentReply(
	ctx context.Context, owner, repo string, number int, body string, commentID int64,
) (*gh.PullRequestComment, error) {
	comment, resp, err := c.gh.PullRequests.CreateCommentInReplyTo(
		ctx, owner, repo, number, body, commentID,
	)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"replying to review comment %d on %s/%s#%d: %w",
			commentID, owner, repo, number, err,
		)
	}
	return comment, nil
}

func (c *liveClient) GetRepository(
	ctx context.Context, owner, repo string,
) (*gh.Repository, error) {
	r, resp, err := c.gh.Repositories.Get(ctx, owner, repo)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf("getting repository %s/%s: %w", owner, repo, err)
	}
	return r, nil
}

func (c *liveClient) CreateReview(
	ctx context.Context, owner, repo string, number int,
	event string, body string,
) (*gh.PullRequestReview, error) {
	return c.CreateReviewWithComments(ctx, owner, repo, number, event, body, "", nil)
}

func (c *liveClient) CreateReviewWithComments(
	ctx context.Context,
	owner, repo string,
	number int,
	event string,
	body string,
	commitID string,
	comments []*gh.DraftReviewComment,
) (*gh.PullRequestReview, error) {
	request := &gh.PullRequestReviewRequest{
		Event:    new(event),
		Body:     new(body),
		Comments: comments,
	}
	if commitID != "" {
		request.CommitID = &commitID
	}
	review, resp, err := c.gh.PullRequests.CreateReview(
		ctx, owner, repo, number, request,
	)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"creating review on %s/%s#%d: %w", owner, repo, number, err,
		)
	}
	return review, nil
}

func (c *liveClient) MarkPullRequestReadyForReview(
	ctx context.Context, owner, repo string, number int,
) (*gh.PullRequest, error) {
	type readyForReviewIDResponse struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Repository *struct {
				PullRequest *struct {
					ID string `json:"id"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	type readyForReviewMutationResponse struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			MarkPullRequestReadyForReview *struct {
				PullRequest *gqlPR `json:"pullRequest"`
			} `json:"markPullRequestReadyForReview"`
		} `json:"data"`
	}

	postGraphQL := func(payload any, dest any) (*http.Response, error) {
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			c.graphQLEndpoint,
			bytes.NewReader(body),
		)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		c.trackRateHeaders(resp)
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return resp, newReadyForReviewError(
				fmt.Errorf("graphql status %s", resp.Status),
				resp.StatusCode,
				resp.StatusCode == http.StatusNotFound,
			)
		}
		if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
			_ = resp.Body.Close()
			return resp, err
		}
		_ = resp.Body.Close()
		return resp, nil
	}

	idPayload := graphQLRequest{
		Query: readyForReviewIDQuery,
		Variables: map[string]any{
			"owner":  owner,
			"repo":   repo,
			"number": number,
		},
	}
	var idResult readyForReviewIDResponse
	if _, err := postGraphQL(idPayload, &idResult); err != nil {
		return nil, fmt.Errorf(
			"marking %s/%s#%d ready for review: resolve pull request id: %w",
			owner, repo, number, err,
		)
	}
	if len(idResult.Errors) > 0 {
		statusCode, staleState := readyForReviewGraphQLErrorMeta(idResult.Errors)
		return nil, newReadyForReviewError(fmt.Errorf(
			"marking %s/%s#%d ready for review: resolve pull request id: graphql errors: %s",
			owner, repo, number, joinGraphQLErrorMessages(idResult.Errors),
		), statusCode, staleState)
	}
	if idResult.Data.Repository == nil || idResult.Data.Repository.PullRequest == nil || idResult.Data.Repository.PullRequest.ID == "" {
		return nil, newReadyForReviewError(
			fmt.Errorf(
				"marking %s/%s#%d ready for review: resolve pull request id: missing pull request in graphql response",
				owner, repo, number,
			),
			http.StatusNotFound,
			true,
		)
	}

	mutationPayload := graphQLRequest{
		Query: readyForReviewMutation,
		Variables: map[string]any{
			"pullRequestId": idResult.Data.Repository.PullRequest.ID,
		},
	}
	var mutationResult readyForReviewMutationResponse
	if _, err := postGraphQL(mutationPayload, &mutationResult); err != nil {
		return nil, fmt.Errorf(
			"marking %s/%s#%d ready for review: %w",
			owner, repo, number, err,
		)
	}
	if len(mutationResult.Errors) > 0 {
		statusCode, staleState := readyForReviewGraphQLErrorMeta(mutationResult.Errors)
		return nil, newReadyForReviewError(fmt.Errorf(
			"marking %s/%s#%d ready for review: graphql errors: %s",
			owner, repo, number, joinGraphQLErrorMessages(mutationResult.Errors),
		), statusCode, staleState)
	}
	if mutationResult.Data.MarkPullRequestReadyForReview == nil || mutationResult.Data.MarkPullRequestReadyForReview.PullRequest == nil {
		return nil, newReadyForReviewError(
			fmt.Errorf(
				"marking %s/%s#%d ready for review: missing pull request in graphql response",
				owner, repo, number,
			),
			0,
			false,
		)
	}

	return adaptPR(mutationResult.Data.MarkPullRequestReadyForReview.PullRequest), nil
}

func (c *liveClient) MergePullRequest(
	ctx context.Context, owner, repo string, number int,
	commitTitle, commitMessage, method string,
) (*gh.PullRequestMergeResult, error) {
	opts := &gh.PullRequestOptions{
		CommitTitle: commitTitle,
		MergeMethod: method,
	}
	result, resp, err := c.gh.PullRequests.Merge(
		ctx, owner, repo, number, commitMessage, opts,
	)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"merging %s/%s#%d: %w", owner, repo, number, err,
		)
	}
	return result, nil
}

func (c *liveClient) EditPullRequest(
	ctx context.Context, owner, repo string, number int, opts EditPullRequestOpts,
) (*gh.PullRequest, error) {
	edit := &gh.PullRequest{}
	if opts.State != nil {
		edit.State = opts.State
	}
	if opts.Title != nil {
		edit.Title = opts.Title
	}
	if opts.Body != nil {
		edit.Body = opts.Body
	}
	pr, resp, err := c.gh.PullRequests.Edit(
		ctx, owner, repo, number, edit,
	)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"editing pull request %s/%s#%d: %w",
			owner, repo, number, err,
		)
	}
	return pr, nil
}

func (c *liveClient) EditIssue(
	ctx context.Context, owner, repo string, number int, state string,
) (*gh.Issue, error) {
	issue, resp, err := c.gh.Issues.Edit(
		ctx, owner, repo, number, &gh.IssueRequest{State: &state},
	)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"editing issue %s/%s#%d: %w",
			owner, repo, number, err,
		)
	}
	return issue, nil
}

func (c *liveClient) EditIssueContent(
	ctx context.Context, owner, repo string, number int, title *string, body *string,
) (*gh.Issue, error) {
	req := &gh.IssueRequest{}
	if title != nil {
		req.Title = title
	}
	if body != nil {
		req.Body = body
	}
	issue, resp, err := c.gh.Issues.Edit(ctx, owner, repo, number, req)
	c.trackRate(resp)
	if err != nil {
		return nil, fmt.Errorf(
			"editing issue %s/%s#%d: %w",
			owner, repo, number, err,
		)
	}
	return issue, nil
}

func (c *liveClient) ListPullRequestsPage(
	ctx context.Context, owner, repo, state string, page int,
) ([]*gh.PullRequest, bool, error) {
	opts := &gh.PullRequestListOptions{
		State:     state,
		Sort:      "updated",
		Direction: "desc",
		ListOptions: gh.ListOptions{
			Page:    page,
			PerPage: 100,
		},
	}
	prs, resp, err := c.gh.PullRequests.List(
		ctx, owner, repo, opts,
	)
	c.trackRate(resp)
	if err != nil {
		return nil, false, fmt.Errorf(
			"list %s PRs page %d for %s/%s: %w",
			state, page, owner, repo, err,
		)
	}
	hasMore := resp != nil && resp.NextPage > 0
	return prs, hasMore, nil
}

func (c *liveClient) ListIssuesPage(
	ctx context.Context, owner, repo, state string, page int,
) ([]*gh.Issue, bool, error) {
	opts := &gh.IssueListByRepoOptions{
		State:     state,
		Sort:      "updated",
		Direction: "desc",
		ListOptions: gh.ListOptions{
			Page:    page,
			PerPage: 100,
		},
	}
	issues, resp, err := c.gh.Issues.ListByRepo(
		ctx, owner, repo, opts,
	)
	c.trackRate(resp)
	if err != nil {
		return nil, false, fmt.Errorf(
			"list %s issues page %d for %s/%s: %w",
			state, page, owner, repo, err,
		)
	}
	// Filter out PRs (GitHub Issues API returns them).
	var filtered []*gh.Issue
	for _, issue := range issues {
		if issue.PullRequestLinks == nil {
			filtered = append(filtered, issue)
		}
	}
	hasMore := resp != nil && resp.NextPage > 0
	return filtered, hasMore, nil
}

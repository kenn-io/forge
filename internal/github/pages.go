package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v89/github"
	"go.kenn.io/forge/internal/platform"
	platformgithub "go.kenn.io/forge/internal/platform/github"
	"go.kenn.io/forge/internal/tokenauth"
)

// ListIssuesPage owns GitHub issue inventory requests and normalization.
func (p *gitHubClientProvider) ListIssuesPage(
	ctx context.Context,
	ref platform.RepoRef,
	query platform.ItemPageQuery,
) (platform.Page[platform.Issue], error) {
	if err := platform.ValidateItemPageQuery(query); err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	if query.Order == platform.ItemOrderUpdated {
		since := time.Time{}
		if query.UpdatedSince != nil {
			since = query.UpdatedSince.UTC()
		}
		return p.listInventoryIssuesPage(ctx, ref, query.Cursor, "updated_issues", "updated", since)
	}
	return p.listInventoryIssuesPage(ctx, ref, query.Cursor, "historical_issues", "created", time.Time{})
}

// ListMergeRequestsPage owns GitHub merge-request inventory requests and normalization.
func (p *gitHubClientProvider) ListMergeRequestsPage(
	ctx context.Context,
	ref platform.RepoRef,
	query platform.ItemPageQuery,
) (platform.Page[platform.MergeRequest], error) {
	if err := platform.ValidateItemPageQuery(query); err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	if query.Order == platform.ItemOrderUpdated {
		since := time.Time{}
		if query.UpdatedSince != nil {
			since = query.UpdatedSince.UTC()
		}
		return p.listInventoryMergeRequestsPage(
			ctx, ref, query.Cursor, "updated_merge_requests", "updated", since,
		)
	}
	return p.listInventoryMergeRequestsPage(
		ctx, ref, query.Cursor, "historical_merge_requests", "created", time.Time{},
	)
}

type lookupOutcome string

const (
	lookupRemoved      lookupOutcome = "removed"
	lookupMoved        lookupOutcome = "moved"
	lookupInaccessible lookupOutcome = "inaccessible"
)

func githubRepositoryFeatureDisabled(host, capability string, err error) error {
	var responseErr *gh.ErrorResponse
	if !errors.As(err, &responseErr) || responseErr.Response == nil ||
		responseErr.Response.StatusCode != http.StatusGone {
		return nil
	}

	message := strings.ToLower(responseErr.Message)
	var phrase string
	switch capability {
	case platform.RepositoryFeatureIssues:
		phrase = "issues are disabled"
	case platform.RepositoryFeatureMergeRequests:
		phrase = "pull requests are disabled"
	default:
		return nil
	}
	if !strings.Contains(message, phrase) {
		return nil
	}
	return platform.RepositoryFeatureDisabled(platform.KindGitHub, host, capability, err)
}

func (p *gitHubClientProvider) classifyIssueLookup(
	ctx context.Context,
	ref platform.RepoRef,
	err error,
) (lookupOutcome, *platform.RepoRef, error) {
	if disabledErr := githubRepositoryFeatureDisabled(
		p.host, platform.RepositoryFeatureIssues, err,
	); disabledErr != nil {
		return "", nil, disabledErr
	}
	// GitHub documents 410 from the single-issue endpoint as a deleted
	// issue. Repository-wide issue disablement uses the same status but is
	// classified above, so every remaining 410 here is a definitive parent
	// removal rather than a transient transport failure. Keep this mapping at
	// the issue lookup boundary; 410 has different meanings on other endpoints.
	if githubStatusCode(err) == http.StatusGone {
		return lookupRemoved, nil, nil
	}
	mapped := p.archiveTransportError(platform.ArchiveCapabilityHistoricalIssues, err)
	if errors.Is(mapped, platform.ErrRateLimited) {
		return "", nil, mapped
	}
	status := githubStatusCode(err)
	if status != http.StatusNotFound {
		if status == http.StatusForbidden || status == http.StatusUnauthorized {
			if _, repoErr := p.client.GetRepository(ctx, ref.Owner, ref.Name); repoErr != nil {
				return "", nil, p.archiveRepositoryProbeError(repoErr)
			}
			return lookupInaccessible, nil, nil
		}
		return "", nil, mapped
	}
	if _, repoErr := p.client.GetRepository(ctx, ref.Owner, ref.Name); repoErr != nil {
		return "", nil, p.archiveRepositoryProbeError(repoErr)
	}
	return lookupRemoved, nil, nil
}

func (p *gitHubClientProvider) classifyMergeRequestLookup(
	ctx context.Context,
	ref platform.RepoRef,
	err error,
) (lookupOutcome, *platform.RepoRef, error) {
	if disabledErr := githubRepositoryFeatureDisabled(
		p.host, platform.RepositoryFeatureMergeRequests, err,
	); disabledErr != nil {
		return "", nil, disabledErr
	}
	mapped := p.archiveTransportError(platform.ArchiveCapabilityHistoricalMergeRequests, err)
	if errors.Is(mapped, platform.ErrRateLimited) {
		return "", nil, mapped
	}
	status := githubStatusCode(err)
	if status != http.StatusNotFound {
		if status == http.StatusForbidden || status == http.StatusUnauthorized {
			if _, repoErr := p.client.GetRepository(ctx, ref.Owner, ref.Name); repoErr != nil {
				return "", nil, p.archiveRepositoryProbeError(repoErr)
			}
			return lookupInaccessible, nil, nil
		}
		return "", nil, mapped
	}
	if _, repoErr := p.client.GetRepository(ctx, ref.Owner, ref.Name); repoErr != nil {
		return "", nil, p.archiveRepositoryProbeError(repoErr)
	}
	return lookupRemoved, nil, nil
}

// issueLookupOutcomeError maps a raw single-issue fetch result onto the
// canonical lookup classification so the optimized GitHub detail path
// surfaces the same typed outcomes as LookupIssue: removed is not_found,
// inaccessible is permission_denied, and a repository transfer is not_found
// carrying the destination. A nil return means the result needs no outcome
// mapping. Classification may spend one repository probe on the live client;
// this runs in live sync, not archive admission, so no admitted budget
// applies.
func (p *gitHubClientProvider) issueLookupOutcomeError(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	issue *gh.Issue,
	err error,
) error {
	if err != nil {
		outcome, destination, classifyErr := p.classifyIssueLookup(ctx, ref, err)
		if classifyErr != nil {
			return classifyErr
		}
		return p.lookupNotPresentError(ref, number, outcome, destination)
	}
	if issue == nil {
		return nil
	}
	if destination := githubArchiveDestination(ref, issue.GetRepositoryURL()); destination != nil {
		return p.lookupNotPresentError(ref, number, lookupMoved, destination)
	}
	return nil
}

// issuePullRequestOutcomeError classifies an issue fetch whose number
// resolved to a pull request. REST serves pull requests from the issues
// endpoint, but an issue number that resolves to a pull request is not an
// issue: surfacing it as present hands downstream issue reads (GraphQL
// timeline, comments) a number they can never resolve, so hydration retries
// forever. Callers that dispatch on the fetched item's kind (issue vs pull
// request) must not apply this; it is for reads that require an issue.
func (p *gitHubClientProvider) issuePullRequestOutcomeError(
	ref platform.RepoRef,
	number int,
	issue *gh.Issue,
) error {
	if issue == nil || !issue.IsPullRequest() {
		return nil
	}
	return p.lookupNotPresentError(ref, number, lookupRemoved, nil)
}

// mergeRequestLookupOutcomeError is the merge-request counterpart to
// issueLookupOutcomeError.
func (p *gitHubClientProvider) mergeRequestLookupOutcomeError(
	ctx context.Context,
	ref platform.RepoRef,
	number int,
	pr *gh.PullRequest,
	err error,
) error {
	if err != nil {
		outcome, destination, classifyErr := p.classifyMergeRequestLookup(ctx, ref, err)
		if classifyErr != nil {
			return classifyErr
		}
		return p.lookupNotPresentError(ref, number, outcome, destination)
	}
	if pr == nil {
		return nil
	}
	if destination := githubArchiveDestination(ref, pr.GetBase().GetRepo().GetURL()); destination != nil {
		return p.lookupNotPresentError(ref, number, lookupMoved, destination)
	}
	return nil
}
func (p *gitHubClientProvider) listInventoryIssuesPage(
	ctx context.Context,
	ref platform.RepoRef,
	cursor string,
	mode string,
	sortBy string,
	since time.Time,
) (platform.Page[platform.Issue], error) {
	client, err := p.pageClient()
	if err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	state, err := decodeGitHubArchiveCursor(cursor, ref, mode, since)
	if err != nil {
		return platform.Page[platform.Issue]{}, platform.ProviderContract(
			platform.KindGitHub, p.host, "archive_cursor", err,
		)
	}
	querySince, err := githubArchiveIssueSince(mode, state.Since)
	if err != nil {
		return platform.Page[platform.Issue]{}, platform.ProviderContract(
			platform.KindGitHub, p.host, "archive_cursor", err,
		)
	}
	items, next, exhausted, err := client.ListInventoryIssuesPage(
		ctx, ref.Owner, ref.Name, sortBy, state.After, querySince,
	)
	if err != nil {
		return platform.Page[platform.Issue]{}, p.archiveTransportError(platform.ArchiveCapabilityHistoricalIssues, err)
	}
	out := make([]platform.Issue, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		normalized, err := platformgithub.NormalizeIssue(ref, item)
		if err != nil {
			return platform.Page[platform.Issue]{}, err
		}
		out = append(out, normalized)
	}
	if exhausted {
		return platform.Page[platform.Issue]{Items: out, Exhausted: true}, nil
	}
	state.After = next
	encoded, err := encodeGitHubArchiveCursor(state)
	if err != nil {
		return platform.Page[platform.Issue]{}, err
	}
	return platform.Page[platform.Issue]{Items: out, NextCursor: encoded}, nil
}

// githubArchiveIssueSince overlaps the maintenance issue scan by one second so
// the inclusive watermark contract is honored against GitHub's exclusive
// GraphQL since filter.
func githubArchiveIssueSince(mode, since string) (string, error) {
	if mode != "updated_issues" || since == "" {
		return since, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, since)
	if err != nil {
		return "", fmt.Errorf("parse issue maintenance watermark: %w", err)
	}
	return parsed.Add(-time.Second).Format(time.RFC3339Nano), nil
}

// listInventoryMergeRequestsPage owns the REST historical and maintenance
// merge-request request shapes. The historical scan traverses ascending by
// creation time; the maintenance scan traverses descending by update time and
// stops once it crosses the overlapped watermark.
func (p *gitHubClientProvider) listInventoryMergeRequestsPage(
	ctx context.Context,
	ref platform.RepoRef,
	cursor string,
	mode string,
	sortBy string,
	since time.Time,
) (platform.Page[platform.MergeRequest], error) {
	client, err := p.pageClient()
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, err
	}
	state, err := decodeGitHubArchiveCursor(cursor, ref, mode, since)
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, platform.ProviderContract(
			platform.KindGitHub, p.host, "archive_cursor", err,
		)
	}
	items, hasMore, err := client.ListInventoryPullRequestsPage(
		ctx, ref.Owner, ref.Name, sortBy, state.Page,
	)
	if err != nil {
		return platform.Page[platform.MergeRequest]{}, p.archiveTransportError(platform.ArchiveCapabilityHistoricalMergeRequests, err)
	}
	out := make([]platform.MergeRequest, 0, len(items))
	crossedWatermark := false
	overlapStart := since.Add(-time.Second)
	for _, item := range items {
		normalized, err := platformgithub.NormalizePullRequest(ref, item)
		if err != nil {
			return platform.Page[platform.MergeRequest]{}, err
		}
		if mode == "updated_merge_requests" && normalized.UpdatedAt.Before(overlapStart) {
			crossedWatermark = true
			continue
		}
		out = append(out, normalized)
	}
	if crossedWatermark {
		return platform.Page[platform.MergeRequest]{Items: out, Exhausted: true}, nil
	}
	return pageWithNext(out, state, hasMore)
}

var (
	_ platform.IssuePageReader        = (*gitHubClientProvider)(nil)
	_ platform.MergeRequestPageReader = (*gitHubClientProvider)(nil)
)

const githubPageSize = 100

type pageClient interface {
	ListInventoryIssuesPage(
		context.Context, string, string, string, string, string,
	) ([]*gh.Issue, string, bool, error)
	ListInventoryPullRequestsPage(
		context.Context, string, string, string, int,
	) ([]*gh.PullRequest, bool, error)
}

type githubArchiveCursor struct {
	Mode  string `json:"mode"`
	Host  string `json:"host"`
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Page  int    `json:"page"`
	After string `json:"after,omitempty"`
	Since string `json:"since,omitempty"`
}

type githubArchiveReviewCursor struct {
	Host         string                          `json:"host"`
	Owner        string                          `json:"owner"`
	Repo         string                          `json:"repo"`
	Number       int                             `json:"number"`
	Phase        string                          `json:"phase"`
	ThreadAfter  string                          `json:"thread_after,omitempty"`
	CommentAfter string                          `json:"comment_after,omitempty"`
	MoreThreads  bool                            `json:"more_threads,omitempty"`
	Thread       githubArchiveReviewThreadCursor `json:"thread,omitzero"`
}

type githubArchiveReviewThreadCursor struct {
	NodeID            string `json:"node_id"`
	IsResolved        bool   `json:"is_resolved,omitempty"`
	IsOutdated        bool   `json:"is_outdated,omitempty"`
	Path              string `json:"path,omitempty"`
	Side              string `json:"side,omitempty"`
	StartLine         *int   `json:"start_line,omitempty"`
	OriginalStartLine *int   `json:"original_start_line,omitempty"`
	Line              int    `json:"line,omitempty"`
	OriginalLine      int    `json:"original_line,omitempty"`
}

const archiveReviewThreadsQuery = `
query($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      reviewThreads(first: 100, after: $cursor) {
        edges {
          cursor
          node {
            id isResolved isOutdated path line originalLine startLine originalStartLine diffSide
            comments(first: 100) {
              nodes { id databaseId fullDatabaseId pullRequestReview { databaseId } subjectType body author { login } path line originalLine diffHunk url commit { oid } originalCommit { oid } isMinimized minimizedReason createdAt updatedAt }
              pageInfo { hasNextPage endCursor }
            }
          }
        }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}`

const archiveIssuesQuery = `
query($owner: String!, $repo: String!, $cursor: String, $orderField: IssueOrderField!, $since: DateTime) {
  repository(owner: $owner, name: $repo) {
    issues(first: 100, after: $cursor, states: [OPEN, CLOSED], filterBy: {since: $since}, orderBy: {field: $orderField, direction: ASC}) {
      nodes {
        id databaseId number title state body url createdAt updatedAt closedAt
        author { login }
        comments { totalCount }
        labels(first: 100) { nodes { name color description isDefault } }
        assignees(first: 100) { nodes { login } }
      }
      pageInfo { hasNextPage endCursor }
    }
  }
}`

type githubArchiveIssueNode struct {
	NodeID     string     `json:"id"`
	DatabaseID int64      `json:"databaseId"`
	Number     int        `json:"number"`
	Title      string     `json:"title"`
	State      string     `json:"state"`
	Body       string     `json:"body"`
	URL        string     `json:"url"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	ClosedAt   *time.Time `json:"closedAt"`
	Author     *struct {
		Login string `json:"login"`
	} `json:"author"`
	Comments struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
	Labels struct {
		Nodes []struct {
			Name        string `json:"name"`
			Color       string `json:"color"`
			Description string `json:"description"`
			IsDefault   bool   `json:"isDefault"`
		} `json:"nodes"`
	} `json:"labels"`
	Assignees struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
}

func (c *liveClient) ListInventoryIssuesPage(
	ctx context.Context,
	owner string,
	repo string,
	sortBy string,
	cursor string,
	since string,
) ([]*gh.Issue, string, bool, error) {
	orderField := "CREATED_AT"
	if sortBy == "updated" {
		orderField = "UPDATED_AT"
	}
	type response struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Repository *struct {
				Issues struct {
					Nodes    []githubArchiveIssueNode `json:"nodes"`
					PageInfo struct {
						HasNextPage bool    `json:"hasNextPage"`
						EndCursor   *string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"issues"`
			} `json:"repository"`
		} `json:"data"`
	}
	var decoded response
	ctx = tokenauth.WithGitHubOwner(ctx, owner)
	if err := c.doArchiveGraphQL(ctx, archiveIssuesQuery, map[string]any{
		"owner": owner, "repo": repo, "cursor": nullableCursor(cursor), "orderField": orderField,
		"since": nullableCursor(since),
	}, &decoded, &decoded.Errors); err != nil {
		return nil, "", false, fmt.Errorf("list archive issues for %s/%s: %w", owner, repo, err)
	}
	if decoded.Data.Repository == nil {
		return nil, "", false, errors.New("missing repository in archive issue response")
	}
	connection := decoded.Data.Repository.Issues
	items := make([]*gh.Issue, 0, len(connection.Nodes))
	for i := range connection.Nodes {
		items = append(items, githubArchiveIssueFromGraphQL(&connection.Nodes[i]))
	}
	return items, cursorValue(connection.PageInfo.EndCursor), !connection.PageInfo.HasNextPage, nil
}

func (c *liveClient) ListInventoryPullRequestsPage(
	ctx context.Context,
	owner string,
	repo string,
	sortBy string,
	page int,
) ([]*gh.PullRequest, bool, error) {
	direction := "asc"
	if sortBy == "updated" {
		direction = "desc"
	}
	opts := &gh.PullRequestListOptions{
		State:     "all",
		Sort:      sortBy,
		Direction: direction,
		ListOptions: gh.ListOptions{
			Page: page, PerPage: githubPageSize,
		},
	}
	items, resp, err := c.gh.PullRequests.List(withBypassETag(ctx), owner, repo, opts)
	c.trackRate(resp)
	if err != nil {
		return nil, false, fmt.Errorf("list archive pull requests for %s/%s: %w", owner, repo, err)
	}
	return items, resp != nil && resp.NextPage > 0, nil
}

func githubArchiveIssueFromGraphQL(node *githubArchiveIssueNode) *gh.Issue {
	state := strings.ToLower(node.State)
	issue := &gh.Issue{
		ID: new(node.DatabaseID), NodeID: new(node.NodeID), Number: new(node.Number),
		Title: new(node.Title), State: new(state), Body: new(node.Body), HTMLURL: new(node.URL),
		Comments:  new(node.Comments.TotalCount),
		CreatedAt: &gh.Timestamp{Time: node.CreatedAt}, UpdatedAt: &gh.Timestamp{Time: node.UpdatedAt},
	}
	if node.Author != nil {
		issue.User = &gh.User{Login: new(node.Author.Login)}
	}
	if node.ClosedAt != nil {
		issue.ClosedAt = &gh.Timestamp{Time: *node.ClosedAt}
	}
	for _, label := range node.Labels.Nodes {
		issue.Labels = append(issue.Labels, &gh.Label{
			Name: new(label.Name), Color: new(label.Color), Description: new(label.Description),
			Default: new(label.IsDefault),
		})
	}
	for _, assignee := range node.Assignees.Nodes {
		issue.Assignees = append(issue.Assignees, &gh.User{Login: new(assignee.Login)})
	}
	return issue
}
func (c *liveClient) ListIssueCommentsPage(
	ctx context.Context,
	owner string,
	repo string,
	number int,
	page int,
) ([]*gh.IssueComment, bool, error) {
	items, resp, err := c.gh.Issues.ListComments(ctx, owner, repo, number, &gh.IssueListCommentsOptions{
		ListOptions: gh.ListOptions{Page: page, PerPage: githubPageSize},
	})
	c.trackRate(resp)
	if err != nil {
		return nil, false, fmt.Errorf("list archive comments for %s/%s#%d: %w", owner, repo, number, err)
	}
	return items, resp != nil && resp.NextPage > 0, nil
}

func (c *liveClient) ListReviewsPage(
	ctx context.Context,
	owner string,
	repo string,
	number int,
	page int,
) ([]*gh.PullRequestReview, bool, error) {
	items, resp, err := c.gh.PullRequests.ListReviews(
		ctx, owner, repo, number,
		&gh.ListOptions{Page: page, PerPage: githubPageSize},
	)
	c.trackRate(resp)
	if err != nil {
		return nil, false, fmt.Errorf("list archive reviews for %s/%s#%d: %w", owner, repo, number, err)
	}
	return items, resp != nil && resp.NextPage > 0, nil
}

func (c *liveClient) ListInventoryReviewThreadsPage(
	ctx context.Context,
	host string,
	owner string,
	repo string,
	number int,
	cursor string,
) ([]PullRequestReviewThread, string, bool, error) {
	state, err := decodeGitHubArchiveReviewCursor(cursor, host, owner, repo, number)
	if err != nil {
		return nil, "", false, err
	}
	if state.Phase == "comments" {
		return c.listReviewThreadCommentsPage(ctx, owner, repo, number, state)
	}
	ctx = tokenauth.WithGitHubOwner(ctx, owner)
	type response struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Repository *struct {
				PullRequest *struct {
					ReviewThreads struct {
						Edges []struct {
							Cursor string `json:"cursor"`
							Node   struct {
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
							} `json:"node"`
						} `json:"edges"`
						PageInfo struct {
							HasNextPage bool    `json:"hasNextPage"`
							EndCursor   *string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	var decoded response
	after := nullableCursor(state.ThreadAfter)
	if err := c.doArchiveGraphQL(ctx, archiveReviewThreadsQuery, map[string]any{
		"owner": owner, "repo": repo, "number": number, "cursor": after,
	}, &decoded, &decoded.Errors); err != nil {
		return nil, "", false, fmt.Errorf("list archive review threads for %s/%s#%d: %w", owner, repo, number, err)
	}
	if decoded.Data.Repository == nil || decoded.Data.Repository.PullRequest == nil {
		return nil, "", false, errors.New("missing pull request in archive review response")
	}
	connection := decoded.Data.Repository.PullRequest.ReviewThreads
	if len(connection.Edges) == 0 {
		return nil, "", true, nil
	}
	threads := make([]PullRequestReviewThread, 0, len(connection.Edges))
	for i, edge := range connection.Edges {
		node := edge.Node
		thread := PullRequestReviewThread{
			NodeID: node.NodeID, IsResolved: node.IsResolved, IsOutdated: node.IsOutdated,
			Path: node.Path, Side: node.Side, StartLine: node.StartLine,
			OriginalStartLine: node.OriginalStartLine, Line: node.Line, OriginalLine: node.OriginalLine,
		}
		for _, comment := range node.Comments.Nodes {
			thread.Comments = append(thread.Comments, githubReviewThreadCommentFromGraphQL(comment))
		}
		threads = append(threads, thread)
		if node.Comments.PageInfo.HasNextPage {
			next, err := encodeGitHubArchiveReviewCursor(githubArchiveReviewCursor{
				Host: host, Owner: owner, Repo: repo, Number: number,
				Phase: "comments", CommentAfter: cursorValue(node.Comments.PageInfo.EndCursor),
				ThreadAfter: edge.Cursor,
				MoreThreads: i < len(connection.Edges)-1 || connection.PageInfo.HasNextPage,
				Thread:      archiveReviewThreadCursor(thread),
			})
			return threads, next, false, err
		}
	}
	if !connection.PageInfo.HasNextPage {
		return threads, "", true, nil
	}
	next, err := encodeGitHubArchiveReviewCursor(githubArchiveReviewCursor{
		Host: host, Owner: owner, Repo: repo, Number: number,
		Phase: "threads", ThreadAfter: cursorValue(connection.PageInfo.EndCursor),
	})
	return threads, next, false, err
}

func (c *liveClient) listReviewThreadCommentsPage(
	ctx context.Context,
	owner string,
	repo string,
	number int,
	state githubArchiveReviewCursor,
) ([]PullRequestReviewThread, string, bool, error) {
	ctx = tokenauth.WithGitHubOwner(ctx, owner)
	type response struct {
		Errors []graphQLError `json:"errors"`
		Data   struct {
			Node *struct {
				Comments graphQLReviewThreadCommentConnection `json:"comments"`
			} `json:"node"`
		} `json:"data"`
	}
	var decoded response
	if err := c.doArchiveGraphQL(ctx, pullRequestReviewThreadCommentsQuery, map[string]any{
		"threadID": state.Thread.NodeID, "cursor": nullableCursor(state.CommentAfter),
	}, &decoded, &decoded.Errors); err != nil {
		return nil, "", false, fmt.Errorf("list archive review thread comments for %s/%s#%d: %w", owner, repo, number, err)
	}
	if decoded.Data.Node == nil {
		return nil, "", false, errors.New("missing review thread in archive comment response")
	}
	thread := state.Thread.thread()
	for _, comment := range decoded.Data.Node.Comments.Nodes {
		thread.Comments = append(thread.Comments, githubReviewThreadCommentFromGraphQL(comment))
	}
	pageInfo := decoded.Data.Node.Comments.PageInfo
	if pageInfo.HasNextPage {
		state.CommentAfter = cursorValue(pageInfo.EndCursor)
		next, err := encodeGitHubArchiveReviewCursor(state)
		return []PullRequestReviewThread{thread}, next, false, err
	}
	if state.MoreThreads {
		next, err := encodeGitHubArchiveReviewCursor(githubArchiveReviewCursor{
			Host: state.Host, Owner: owner, Repo: repo, Number: number,
			Phase: "threads", ThreadAfter: state.ThreadAfter,
		})
		return []PullRequestReviewThread{thread}, next, false, err
	}
	return []PullRequestReviewThread{thread}, "", true, nil
}

func (c *liveClient) doArchiveGraphQL(
	ctx context.Context,
	query string,
	variables map[string]any,
	out any,
	graphQLErrors *[]graphQLError,
) error {
	payload, err := json.Marshal(graphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(
		withQuotaResource(ctx, QuotaResourceGraphQL),
		http.MethodPost,
		c.graphQLEndpoint,
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	c.trackGraphQLRateHeaders(resp)
	if resp.StatusCode != http.StatusOK {
		return gh.CheckResponse(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return err
	}
	if len(*graphQLErrors) > 0 {
		return githubArchiveGraphQLErrors(c.platformHost, resp, *graphQLErrors)
	}
	return nil
}

func githubArchiveGraphQLErrors(host string, resp *http.Response, graphQLErrors []graphQLError) error {
	cause := fmt.Errorf("graphql errors: %s", joinGraphQLErrorMessages(graphQLErrors))
	for _, graphQLError := range graphQLErrors {
		switch strings.ToUpper(graphQLError.Type) {
		case "RATE_LIMITED":
			return &platform.Error{
				Code: platform.ErrCodeRateLimited, Provider: platform.KindGitHub,
				PlatformHost: host, ResetAt: githubArchiveResetAt(resp), Err: cause,
			}
		case "FORBIDDEN", "UNAUTHORIZED":
			return platform.PermissionDenied(platform.KindGitHub, host, cause)
		}
	}
	return cause
}

func githubArchiveResetAt(resp *http.Response) *time.Time {
	if resp == nil {
		return nil
	}
	value, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil || value <= 0 {
		return nil
	}
	reset := time.Unix(value, 0).UTC()
	return &reset
}

func (p *gitHubClientProvider) archiveRepositoryProbeError(err error) error {
	mapped := p.archiveTransportError("", err)
	if errors.Is(mapped, platform.ErrRateLimited) {
		return mapped
	}
	switch githubStatusCode(err) {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return platform.PermissionDenied(platform.KindGitHub, p.host, err)
	default:
		return mapped
	}
}

func (p *gitHubClientProvider) archiveTransportError(capability platform.ArchiveCapability, err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch capability {
	case platform.ArchiveCapabilityHistoricalIssues:
		if disabled := githubRepositoryFeatureDisabled(
			p.host, platform.RepositoryFeatureIssues, err,
		); disabled != nil {
			return disabled
		}
	case platform.ArchiveCapabilityHistoricalMergeRequests:
		if disabled := githubRepositoryFeatureDisabled(
			p.host, platform.RepositoryFeatureMergeRequests, err,
		); disabled != nil {
			return disabled
		}
	}
	var existing *platform.Error
	if errors.As(err, &existing) {
		mapped := *existing
		if mapped.Provider == "" {
			mapped.Provider = platform.KindGitHub
		}
		if mapped.PlatformHost == "" {
			mapped.PlatformHost = p.host
		}
		if mapped.Capability == "" {
			mapped.Capability = string(capability)
		}
		return &mapped
	}
	response := githubArchiveErrorResponse(err)
	resetAt := githubArchiveResetAt(response)
	var rateLimit *gh.RateLimitError
	if errors.As(err, &rateLimit) {
		if !rateLimit.Rate.Reset.IsZero() {
			reset := rateLimit.Rate.Reset.UTC()
			resetAt = &reset
		}
		return &platform.Error{Code: platform.ErrCodeRateLimited, Provider: platform.KindGitHub,
			PlatformHost: p.host, Capability: string(capability), ResetAt: resetAt, Err: err}
	}
	var abuseLimit *gh.AbuseRateLimitError
	if errors.As(err, &abuseLimit) {
		if resetAt == nil && abuseLimit.RetryAfter != nil {
			reset := time.Now().UTC().Add(*abuseLimit.RetryAfter)
			resetAt = &reset
		}
		return &platform.Error{Code: platform.ErrCodeRateLimited, Provider: platform.KindGitHub,
			PlatformHost: p.host, Capability: string(capability), ResetAt: resetAt, Err: err}
	}
	status := githubStatusCode(err)
	if status == http.StatusTooManyRequests ||
		status == http.StatusForbidden && response != nil && response.Header.Get("X-RateLimit-Remaining") == "0" {
		return &platform.Error{Code: platform.ErrCodeRateLimited, Provider: platform.KindGitHub,
			PlatformHost: p.host, Capability: string(capability), ResetAt: resetAt, Err: err}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return &platform.Error{Code: platform.ErrCodePermissionDenied, Provider: platform.KindGitHub,
			PlatformHost: p.host, Capability: string(capability), Err: err}
	}
	return err
}

func githubArchiveErrorResponse(err error) *http.Response {
	var rateLimit *gh.RateLimitError
	if errors.As(err, &rateLimit) {
		return rateLimit.Response
	}
	var abuseLimit *gh.AbuseRateLimitError
	if errors.As(err, &abuseLimit) {
		return abuseLimit.Response
	}
	var response *gh.ErrorResponse
	if errors.As(err, &response) {
		return response.Response
	}
	return nil
}
func (p *gitHubClientProvider) pageClient() (pageClient, error) {
	client, ok := p.client.(pageClient)
	if !ok {
		return nil, platform.UnsupportedCapability(
			platform.KindGitHub, p.host, string(platform.ArchiveCapabilityHistoricalIssues),
		)
	}
	return client, nil
}

func pageWithNext[T any](
	items []T,
	cursor githubArchiveCursor,
	hasMore bool,
) (platform.Page[T], error) {
	if !hasMore {
		return platform.Page[T]{Items: items, Exhausted: true}, nil
	}
	cursor.Page++
	next, err := encodeGitHubArchiveCursor(cursor)
	if err != nil {
		return platform.Page[T]{}, err
	}
	return platform.Page[T]{Items: items, NextCursor: next}, nil
}

func decodeGitHubArchiveCursor(
	encoded string,
	ref platform.RepoRef,
	mode string,
	since time.Time,
) (githubArchiveCursor, error) {
	expectedSince := ""
	if !since.IsZero() {
		expectedSince = since.UTC().Format(time.RFC3339Nano)
	}
	if encoded == "" {
		return githubArchiveCursor{
			Mode: mode, Host: ref.Host, Owner: ref.Owner, Repo: ref.Name,
			Page: 1, Since: expectedSince,
		}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return githubArchiveCursor{}, fmt.Errorf("decode cursor: %w", err)
	}
	var cursor githubArchiveCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return githubArchiveCursor{}, fmt.Errorf("parse cursor: %w", err)
	}
	if cursor.Mode != mode || cursor.Host != ref.Host || cursor.Owner != ref.Owner || cursor.Repo != ref.Name ||
		cursor.Since != expectedSince || cursor.Page <= 0 {
		return githubArchiveCursor{}, errors.New("cursor does not match archive enumeration")
	}
	return cursor, nil
}

func encodeGitHubArchiveCursor(cursor githubArchiveCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode archive cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeGitHubArchiveReviewCursor(
	encoded string,
	host string,
	owner string,
	repo string,
	number int,
) (githubArchiveReviewCursor, error) {
	if encoded == "" {
		return githubArchiveReviewCursor{
			Host: host, Owner: owner, Repo: repo, Number: number, Phase: "threads",
		}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return githubArchiveReviewCursor{}, fmt.Errorf("decode review cursor: %w", err)
	}
	var cursor githubArchiveReviewCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return githubArchiveReviewCursor{}, fmt.Errorf("parse review cursor: %w", err)
	}
	if cursor.Host != host || cursor.Owner != owner || cursor.Repo != repo || cursor.Number != number {
		return githubArchiveReviewCursor{}, errors.New("review cursor does not match archive item")
	}
	if cursor.Phase != "threads" && cursor.Phase != "comments" {
		return githubArchiveReviewCursor{}, errors.New("invalid review cursor phase")
	}
	if cursor.Phase == "comments" && (cursor.Thread.NodeID == "" || cursor.CommentAfter == "") {
		return githubArchiveReviewCursor{}, errors.New("incomplete review comment cursor")
	}
	return cursor, nil
}

func encodeGitHubArchiveReviewCursor(cursor githubArchiveReviewCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode review cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func archiveReviewThreadCursor(thread PullRequestReviewThread) githubArchiveReviewThreadCursor {
	return githubArchiveReviewThreadCursor{
		NodeID: thread.NodeID, IsResolved: thread.IsResolved, IsOutdated: thread.IsOutdated,
		Path: thread.Path, Side: thread.Side, StartLine: thread.StartLine,
		OriginalStartLine: thread.OriginalStartLine, Line: thread.Line, OriginalLine: thread.OriginalLine,
	}
}

func (cursor githubArchiveReviewThreadCursor) thread() PullRequestReviewThread {
	return PullRequestReviewThread{
		NodeID: cursor.NodeID, IsResolved: cursor.IsResolved, IsOutdated: cursor.IsOutdated,
		Path: cursor.Path, Side: cursor.Side, StartLine: cursor.StartLine,
		OriginalStartLine: cursor.OriginalStartLine, Line: cursor.Line, OriginalLine: cursor.OriginalLine,
	}
}

func nullableCursor(cursor string) any {
	if cursor == "" {
		return nil
	}
	return cursor
}

func cursorValue(cursor *string) string {
	if cursor == nil {
		return ""
	}
	return *cursor
}

func githubArchiveDestination(ref platform.RepoRef, repositoryURL string) *platform.RepoRef {
	parsed, err := url.Parse(repositoryURL)
	if err != nil {
		return nil
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := range parts {
		if parts[i] != "repos" || i+2 >= len(parts) {
			continue
		}
		destination := ref
		destination.Owner = strings.ToLower(parts[i+1])
		destination.Name = strings.ToLower(parts[i+2])
		destination.RepoPath = destination.Owner + "/" + destination.Name
		destination.PlatformID = 0
		destination.PlatformExternalID = ""
		destination.WebURL = ""
		destination.CloneURL = ""
		destination.DefaultBranch = ""
		// GitHub owner/repo names are case-insensitive (canonical
		// kenn-forge identity lowercases them per the platform metadata's
		// LowercaseRepoNames), so a source ref that differs from the
		// returned repository URL only in casing is the same repository,
		// not a transfer.
		if strings.EqualFold(destination.Owner, ref.Owner) &&
			strings.EqualFold(destination.Name, ref.Name) {
			return nil
		}
		return &destination
	}
	return nil
}

func githubStatusCode(err error) int {
	var response *gh.ErrorResponse
	if errors.As(err, &response) && response.Response != nil {
		return response.Response.StatusCode
	}
	var redirect *url.Error
	if errors.As(err, &redirect) {
		var responseError *gh.ErrorResponse
		if errors.As(redirect.Err, &responseError) && responseError.Response != nil {
			return responseError.Response.StatusCode
		}
	}
	return 0
}

var _ pageClient = (*liveClient)(nil)

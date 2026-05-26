package github

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v84/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

// topLevelPageSize is the number of PRs fetched per GraphQL
// query page. Kept conservative to stay under GitHub's 500k
// node limit even with nested connections.
const topLevelPageSize = 10

// retryPageSize is used when the initial query fails (e.g.,
// complexity/node limit error). Half the default.
const retryPageSize = 5

// --- GraphQL query types (private) ---

type gqlPRQuery struct {
	Repository struct {
		PullRequests struct {
			TotalCount int
			Nodes      []gqlPR
			PageInfo   pageInfo
		} `graphql:"pullRequests(first: $pageSize, states: OPEN, after: $cursor)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type gqlPR struct {
	DatabaseId     int64 `graphql:"databaseId"`
	Number         int
	Title          string
	State          string
	IsDraft        bool
	Locked         bool
	Body           string
	URL            string
	Author         struct{ Login string }
	CreatedAt      time.Time
	UpdatedAt      time.Time
	MergedAt       *time.Time
	ClosedAt       *time.Time
	Additions      int
	Deletions      int
	Mergeable      string
	ReviewDecision string
	HeadRefName    string
	BaseRefName    string
	HeadRefOid     string `graphql:"headRefOid"`
	BaseRefOid     string `graphql:"baseRefOid"`
	HeadRepository *struct {
		URL string
	}
	Labels struct {
		Nodes []gqlLabel
	} `graphql:"labels(first: 100)"`
	Comments struct {
		Nodes    []gqlComment
		PageInfo pageInfo
	} `graphql:"comments(first: 100)"`
	Reviews struct {
		Nodes    []gqlReview
		PageInfo pageInfo
	} `graphql:"reviews(first: 100)"`
	AllCommits struct {
		Nodes    []gqlCommitNode
		PageInfo pageInfo
	} `graphql:"allCommits: commits(first: 100)"`
	LastCommit struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes    []gqlCheckContext
						PageInfo pageInfo
					} `graphql:"contexts(first: 100)"`
				}
			}
		}
	} `graphql:"lastCommit: commits(last: 1)"`
	TimelineItems struct {
		Nodes    []gqlPullRequestTimelineItem
		PageInfo pageInfo
	} `graphql:"timelineItems(itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT, COMMENT_DELETED_EVENT, CROSS_REFERENCED_EVENT, RENAMED_TITLE_EVENT, BASE_REF_CHANGED_EVENT, ASSIGNED_EVENT, UNASSIGNED_EVENT], first: 100)"`
}

type gqlComment struct {
	DatabaseId int64
	Author     struct{ Login string }
	Body       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type gqlReview struct {
	DatabaseId  int64
	Author      struct{ Login string }
	Body        string
	State       string
	SubmittedAt time.Time
}

type gqlCommitNode struct {
	Commit gqlCommit
}

type gqlCommit struct {
	OID     string `graphql:"oid"`
	Message string
	Author  struct {
		Name string
		Date time.Time
		User *struct{ Login string }
	}
}

type gqlPullRequestTimelineItem struct {
	Typename                string                     `graphql:"__typename"`
	Node                    gqlNodeFragment            `graphql:"... on Node"`
	HeadRefForcePushedEvent gqlHeadRefForcePushedEvent `graphql:"... on HeadRefForcePushedEvent"`
	CommentDeletedEvent     gqlCommentDeletedEvent     `graphql:"... on CommentDeletedEvent"`
	CrossReferencedEvent    gqlCrossReferencedEvent    `graphql:"... on CrossReferencedEvent"`
	RenamedTitleEvent       gqlRenamedTitleEvent       `graphql:"... on RenamedTitleEvent"`
	BaseRefChangedEvent     gqlBaseRefChangedEvent     `graphql:"... on BaseRefChangedEvent"`
	AssignedEvent           gqlAssignedEvent           `graphql:"... on AssignedEvent"`
	UnassignedEvent         gqlAssignedEvent           `graphql:"... on UnassignedEvent"`
}

type gqlIssueTimelineItem struct {
	Typename        string           `graphql:"__typename"`
	Node            gqlNodeFragment  `graphql:"... on Node"`
	AssignedEvent   gqlAssignedEvent `graphql:"... on AssignedEvent"`
	UnassignedEvent gqlAssignedEvent `graphql:"... on UnassignedEvent"`
}

type gqlNodeFragment struct {
	ID string
}

type gqlActorRef struct {
	Login string
}

type gqlHeadRefForcePushedEvent struct {
	Actor        *gqlActorRef
	BeforeCommit *struct {
		OID string `graphql:"oid"`
	}
	AfterCommit *struct {
		OID string `graphql:"oid"`
	}
	CreatedAt time.Time
	Ref       *struct {
		Name string
	}
}

type gqlCommentDeletedEvent struct {
	Actor                *gqlActorRef
	CreatedAt            time.Time
	DeletedCommentAuthor *gqlActorRef
}

type gqlCrossReferencedEvent struct {
	Actor             *gqlActorRef
	CreatedAt         time.Time
	IsCrossRepository bool
	WillCloseTarget   bool
	Source            gqlReferencedSubject
}

type gqlReferencedSubject struct {
	Typename    string                 `graphql:"__typename"`
	Issue       gqlReferencedIssueOrPR `graphql:"... on Issue"`
	PullRequest gqlReferencedIssueOrPR `graphql:"... on PullRequest"`
}

type gqlReferencedIssueOrPR struct {
	Number     int
	Title      string
	URL        string `graphql:"url"`
	Repository struct {
		Owner struct {
			Login string
		}
		Name string
	}
}

type gqlRenamedTitleEvent struct {
	Actor         *gqlActorRef
	CreatedAt     time.Time
	PreviousTitle string
	CurrentTitle  string
}

type gqlBaseRefChangedEvent struct {
	Actor           *gqlActorRef
	CreatedAt       time.Time
	PreviousRefName string
	CurrentRefName  string
}

type gqlAssignedEvent struct {
	Actor     *gqlActorRef
	Assignee  gqlAssignee
	CreatedAt time.Time
}

type gqlAssignee struct {
	Typename     string        `graphql:"__typename"`
	Bot          gqlAssigneeID `graphql:"... on Bot"`
	Mannequin    gqlAssigneeID `graphql:"... on Mannequin"`
	Organization gqlAssigneeID `graphql:"... on Organization"`
	User         gqlAssigneeID `graphql:"... on User"`
}

type gqlAssigneeID struct {
	Login string
}

func (a gqlAssignee) Login() string {
	switch a.Typename {
	case "Bot":
		return a.Bot.Login
	case "Mannequin":
		return a.Mannequin.Login
	case "Organization":
		return a.Organization.Login
	case "User":
		return a.User.Login
	default:
		return ""
	}
}

type gqlIssueQuery struct {
	Repository struct {
		Issues struct {
			TotalCount int
			Nodes      []gqlIssue
			PageInfo   pageInfo
		} `graphql:"issues(first: $pageSize, states: OPEN, after: $cursor)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type gqlIssue struct {
	DatabaseId int64 `graphql:"databaseId"`
	Number     int
	Title      string
	State      string
	Body       string
	URL        string `graphql:"url"`
	Author     struct{ Login string }
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ClosedAt   *time.Time
	Labels     struct {
		Nodes []gqlLabel
	} `graphql:"labels(first: 100)"`
	Comments struct {
		TotalCount int
		Nodes      []gqlComment
		PageInfo   pageInfo
	} `graphql:"comments(first: 100)"`
	TimelineItems struct {
		Nodes    []gqlIssueTimelineItem
		PageInfo pageInfo
	} `graphql:"timelineItems(itemTypes: [ASSIGNED_EVENT, UNASSIGNED_EVENT], first: 100)"`
}

type gqlLabel struct {
	Name        string
	Color       string
	Description string
	IsDefault   bool
}

type gqlCheckContext struct {
	Typename      string                 `graphql:"__typename"`
	CheckRun      gqlCheckRunFields      `graphql:"... on CheckRun"`
	StatusContext gqlStatusContextFields `graphql:"... on StatusContext"`
}

type gqlCheckRunFields struct {
	Name        string
	Status      string
	Conclusion  string
	DetailsURL  string `graphql:"detailsUrl"`
	StartedAt   *time.Time
	CompletedAt *time.Time
	CheckSuite  struct {
		CreatedAt *time.Time
		App       struct {
			Name string
		}
	}
}

type gqlStatusContextFields struct {
	Context   string
	State     string
	TargetURL string `graphql:"targetUrl"`
}

// --- Adapter functions ---

func adaptPR(gql *gqlPR) *gh.PullRequest {
	state := stateToREST(gql.State)
	pr := &gh.PullRequest{
		ID:        new(gql.DatabaseId),
		Number:    new(gql.Number),
		Title:     new(gql.Title),
		State:     new(state),
		Draft:     new(gql.IsDraft),
		Locked:    new(gql.Locked),
		Body:      new(gql.Body),
		HTMLURL:   new(gql.URL),
		Additions: new(gql.Additions),
		Deletions: new(gql.Deletions),
		User:      &gh.User{Login: new(gql.Author.Login)},
		Head: &gh.PullRequestBranch{
			Ref: new(gql.HeadRefName),
			SHA: new(gql.HeadRefOid),
		},
		Base: &gh.PullRequestBranch{
			Ref: new(gql.BaseRefName),
			SHA: new(gql.BaseRefOid),
		},
		MergeableState: new(mergeableToREST(gql.Mergeable)),
	}

	created := gh.Timestamp{Time: gql.CreatedAt}
	updated := gh.Timestamp{Time: gql.UpdatedAt}
	pr.CreatedAt = &created
	pr.UpdatedAt = &updated

	if gql.MergedAt != nil {
		t := gh.Timestamp{Time: *gql.MergedAt}
		pr.MergedAt = &t
		pr.Merged = new(true)
	}
	if gql.ClosedAt != nil {
		t := gh.Timestamp{Time: *gql.ClosedAt}
		pr.ClosedAt = &t
	}
	pr.Labels = adaptLabels(gql.Labels.Nodes)

	if gql.HeadRepository != nil {
		cloneURL := gql.HeadRepository.URL
		if !strings.HasSuffix(cloneURL, ".git") {
			cloneURL += ".git"
		}
		pr.Head.Repo = &gh.Repository{
			CloneURL: new(cloneURL),
		}
	}

	return pr
}

func adaptIssue(gql *gqlIssue) *gh.Issue {
	state := stateToREST(gql.State)
	issue := &gh.Issue{
		ID:       new(gql.DatabaseId),
		Number:   new(gql.Number),
		Title:    new(gql.Title),
		State:    new(state),
		Body:     new(gql.Body),
		HTMLURL:  new(gql.URL),
		Comments: new(gql.Comments.TotalCount),
		User:     &gh.User{Login: new(gql.Author.Login)},
	}

	created := gh.Timestamp{Time: gql.CreatedAt}
	updated := gh.Timestamp{Time: gql.UpdatedAt}
	issue.CreatedAt = &created
	issue.UpdatedAt = &updated

	if gql.ClosedAt != nil {
		t := gh.Timestamp{Time: *gql.ClosedAt}
		issue.ClosedAt = &t
	}
	issue.Labels = adaptLabels(gql.Labels.Nodes)

	return issue
}

func adaptLabels(labels []gqlLabel) []*gh.Label {
	out := make([]*gh.Label, 0, len(labels))
	for _, label := range labels {
		out = append(out, &gh.Label{
			Name:        new(label.Name),
			Color:       new(label.Color),
			Description: new(label.Description),
			Default:     new(label.IsDefault),
		})
	}
	return out
}

func stateToREST(graphqlState string) string {
	switch graphqlState {
	case "MERGED":
		return "closed"
	case "CLOSED":
		return "closed"
	default:
		return "open"
	}
}

func mergeableToREST(mergeable string) string {
	switch mergeable {
	case "MERGEABLE":
		return "clean"
	case "CONFLICTING":
		return "dirty"
	default:
		return "unknown"
	}
}

func adaptComment(gql *gqlComment) *gh.IssueComment {
	created := gh.Timestamp{Time: gql.CreatedAt}
	updated := gh.Timestamp{Time: gql.UpdatedAt}
	return &gh.IssueComment{
		ID:        new(gql.DatabaseId),
		Body:      new(gql.Body),
		User:      &gh.User{Login: new(gql.Author.Login)},
		CreatedAt: &created,
		UpdatedAt: &updated,
	}
}

func adaptReview(gql *gqlReview) *gh.PullRequestReview {
	submitted := gh.Timestamp{Time: gql.SubmittedAt}
	return &gh.PullRequestReview{
		ID:          new(gql.DatabaseId),
		Body:        new(gql.Body),
		State:       new(gql.State),
		User:        &gh.User{Login: new(gql.Author.Login)},
		SubmittedAt: &submitted,
	}
}

func adaptCommit(gql *gqlCommitNode) *gh.RepositoryCommit {
	c := &gh.RepositoryCommit{
		SHA: new(gql.Commit.OID),
		Commit: &gh.Commit{
			Message: new(gql.Commit.Message),
			Author: &gh.CommitAuthor{
				Name: new(gql.Commit.Author.Name),
				Date: &gh.Timestamp{Time: gql.Commit.Author.Date},
			},
		},
	}
	if gql.Commit.Author.User != nil {
		c.Author = &gh.User{Login: new(gql.Commit.Author.User.Login)}
	}
	return c
}

func splitCheckContexts(contexts []gqlCheckContext) ([]*gh.CheckRun, []*gh.RepoStatus) {
	var checks []*gh.CheckRun
	var statuses []*gh.RepoStatus
	for i := range contexts {
		c := &contexts[i]
		switch c.Typename {
		case "CheckRun":
			checks = append(checks, adaptCheckRun(&c.CheckRun))
		case "StatusContext":
			statuses = append(statuses, adaptStatusContext(&c.StatusContext))
		}
	}
	return checks, statuses
}

func adaptCheckRun(gql *gqlCheckRunFields) *gh.CheckRun {
	url := sanitizeURL(gql.DetailsURL)
	return &gh.CheckRun{
		Name:        new(gql.Name),
		Status:      new(toLower(gql.Status)),
		Conclusion:  new(toLower(gql.Conclusion)),
		HTMLURL:     new(url),
		DetailsURL:  new(gql.DetailsURL),
		StartedAt:   ghTimestampPtr(gql.StartedAt),
		CompletedAt: ghTimestampPtr(gql.CompletedAt),
		CheckSuite:  &gh.CheckSuite{CreatedAt: ghTimestampPtr(gql.CheckSuite.CreatedAt)},
		App:         &gh.App{Name: new(gql.CheckSuite.App.Name)},
	}
}

func adaptStatusContext(gql *gqlStatusContextFields) *gh.RepoStatus {
	return &gh.RepoStatus{
		Context:   new(gql.Context),
		State:     new(toLower(gql.State)),
		TargetURL: new(gql.TargetURL),
	}
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func ghTimestampPtr(t *time.Time) *gh.Timestamp {
	if t == nil {
		return nil
	}
	return &gh.Timestamp{Time: *t}
}

// --- Bulk result types ---

// RepoBulkResult holds all open PRs and issues fetched via GraphQL for a repo.
type RepoBulkResult struct {
	PullRequests []BulkPR
	Issues       []BulkIssue
}

// BulkIssue holds an issue and its nested comments from a single
// GraphQL query. CommentsComplete indicates whether the comments
// connection was fully paginated.
type BulkIssue struct {
	Issue            *gh.Issue
	Comments         []*gh.IssueComment
	TimelineEvents   []PullRequestTimelineEvent
	CommentsComplete bool
	TimelineComplete bool
}

// BulkPR holds a PR and its nested data from a single GraphQL query.
// The *Complete flags indicate whether each nested connection was
// fully paginated. When false, the data is partial and the detail
// drain should fill in via REST.
type BulkPR struct {
	PR               *gh.PullRequest
	Comments         []*gh.IssueComment
	Reviews          []*gh.PullRequestReview
	Commits          []*gh.RepositoryCommit
	TimelineEvents   []PullRequestTimelineEvent
	CheckRuns        []*gh.CheckRun
	Statuses         []*gh.RepoStatus
	CommentsComplete bool
	ReviewsComplete  bool
	CommitsComplete  bool
	TimelineComplete bool
	CIComplete       bool
}

func convertGQLIssue(gql *gqlIssue) BulkIssue {
	bulk := BulkIssue{
		Issue:            adaptIssue(gql),
		CommentsComplete: !gql.Comments.PageInfo.HasNextPage,
		TimelineComplete: !gql.TimelineItems.PageInfo.HasNextPage,
	}

	for i := range gql.Comments.Nodes {
		bulk.Comments = append(bulk.Comments, adaptComment(&gql.Comments.Nodes[i]))
	}
	for i := range gql.TimelineItems.Nodes {
		event, ok := adaptIssueTimelineEvent(&gql.TimelineItems.Nodes[i])
		if ok {
			bulk.TimelineEvents = append(bulk.TimelineEvents, event)
		}
	}

	return bulk
}

// --- GraphQL rate transport ---

type graphqlRateTransport struct {
	base        http.RoundTripper
	rateTracker *RateTracker
}

func (t *graphqlRateTransport) RoundTrip(
	req *http.Request,
) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if t.rateTracker != nil {
		t.rateTracker.RecordRequest()
		if rate := parseRateLimitHeaders(resp); rate.Limit > 0 {
			t.rateTracker.UpdateFromRate(rate)
		}
	}
	return resp, err
}

func parseRateLimitHeaders(resp *http.Response) Rate {
	var rate Rate
	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		rate.Remaining, _ = strconv.Atoi(v)
	}
	if v := resp.Header.Get("X-RateLimit-Limit"); v != "" {
		rate.Limit, _ = strconv.Atoi(v)
	}
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		epoch, _ := strconv.ParseInt(v, 10, 64)
		rate.Reset = time.Unix(epoch, 0)
	}
	return rate
}

// --- GraphQLFetcher ---

// GraphQLFetcher fetches PR data via GitHub's GraphQL API (v4).
type GraphQLFetcher struct {
	client      *githubv4.Client
	rateTracker *RateTracker
	host        string
}

// RateTracker returns the GraphQL rate tracker, or nil if none
// (or if called on a nil receiver).
func (f *GraphQLFetcher) RateTracker() *RateTracker {
	if f == nil {
		return nil
	}
	return f.rateTracker
}

// NewGraphQLFetcher creates a fetcher for the given host. budget may be nil.
func NewGraphQLFetcher(
	token string,
	platformHost string,
	rateTracker *RateTracker,
	budget *SyncBudget,
) *GraphQLFetcher {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(context.Background(), ts)

	base := tc.Transport
	if rateTracker != nil {
		base = &graphqlRateTransport{
			base:        base,
			rateTracker: rateTracker,
		}
	}
	if budget != nil {
		tc.Transport = &budgetTransport{
			base:   base,
			budget: budget,
		}
	} else {
		tc.Transport = base
	}

	var gqlClient *githubv4.Client
	if platformHost == "" || platformHost == "github.com" {
		gqlClient = githubv4.NewClient(tc)
	} else {
		endpoint := graphQLEndpointForHost(platformHost)
		gqlClient = githubv4.NewEnterpriseClient(endpoint, tc)
	}

	return &GraphQLFetcher{
		client:      gqlClient,
		rateTracker: rateTracker,
		host:        platformHost,
	}
}

// NewGraphQLFetcherWithClient wraps a pre-built githubv4.Client as a
// GraphQLFetcher. Used by tests that need to point the fetcher at a
// mock HTTP backend.
func NewGraphQLFetcherWithClient(
	client *githubv4.Client, rateTracker *RateTracker,
) *GraphQLFetcher {
	return &GraphQLFetcher{
		client:      client,
		rateTracker: rateTracker,
	}
}

func (g *GraphQLFetcher) ShouldBackoff() (bool, time.Duration) {
	if g.rateTracker == nil {
		return false, 0
	}
	return g.rateTracker.ShouldBackoff()
}

func (g *GraphQLFetcher) FetchRepoPRs(
	ctx context.Context, owner, name string,
) (*RepoBulkResult, error) {
	result, err := g.fetchRepoPRsWithPageSize(
		ctx, owner, name, topLevelPageSize,
	)
	if err != nil {
		slog.Warn("GraphQL query failed, retrying with smaller page",
			"owner", owner, "name", name,
			"err", err, "retryPageSize", retryPageSize,
		)
		result, err = g.fetchRepoPRsWithPageSize(
			ctx, owner, name, retryPageSize,
		)
	}
	return result, err
}

func (g *GraphQLFetcher) fetchRepoPRsWithPageSize(
	ctx context.Context, owner, name string, pageSize int,
) (*RepoBulkResult, error) {
	progress := newMergeRequestListFetchProgressLogger(RepoRef{
		Owner:        owner,
		Name:         name,
		PlatformHost: g.host,
	}, "graphql")
	gqlPRs, err := fetchAllPagesWithProgress(ctx, func(
		ctx context.Context, cursor *string,
	) ([]gqlPR, pageInfo, error) {
		var q gqlPRQuery
		vars := map[string]any{
			"owner":    githubv4.String(owner),
			"name":     githubv4.String(name),
			"pageSize": githubv4.Int(pageSize),
			"cursor":   cursorVar(cursor),
		}
		if err := g.client.Query(ctx, &q, vars); err != nil {
			return nil, pageInfo{}, err
		}
		progress.setTotal(q.Repository.PullRequests.TotalCount)
		return q.Repository.PullRequests.Nodes,
			q.Repository.PullRequests.PageInfo, nil
	}, progress.recordPage)
	if err != nil {
		return nil, err
	}
	progress.done()

	result := &RepoBulkResult{
		PullRequests: make([]BulkPR, 0, len(gqlPRs)),
	}
	for i := range gqlPRs {
		bulk := convertGQLPR(&gqlPRs[i])
		result.PullRequests = append(result.PullRequests, bulk)
	}
	return result, nil
}

func (g *GraphQLFetcher) FetchRepoIssues(
	ctx context.Context, owner, name string,
) (*RepoBulkResult, error) {
	result, err := g.fetchRepoIssuesWithPageSize(
		ctx, owner, name, topLevelPageSize,
	)
	if err != nil {
		slog.Warn("GraphQL issue query failed, retrying with smaller page",
			"owner", owner, "name", name,
			"err", err, "retryPageSize", retryPageSize,
		)
		result, err = g.fetchRepoIssuesWithPageSize(
			ctx, owner, name, retryPageSize,
		)
	}
	return result, err
}

func (g *GraphQLFetcher) fetchRepoIssuesWithPageSize(
	ctx context.Context, owner, name string, pageSize int,
) (*RepoBulkResult, error) {
	progress := newIssueListFetchProgressLogger(RepoRef{
		Owner:        owner,
		Name:         name,
		PlatformHost: g.host,
	}, "graphql")
	gqlIssues, err := fetchAllPagesWithProgress(ctx, func(
		ctx context.Context, cursor *string,
	) ([]gqlIssue, pageInfo, error) {
		var q gqlIssueQuery
		vars := map[string]any{
			"owner":    githubv4.String(owner),
			"name":     githubv4.String(name),
			"pageSize": githubv4.Int(pageSize),
			"cursor":   cursorVar(cursor),
		}
		if err := g.client.Query(ctx, &q, vars); err != nil {
			return nil, pageInfo{}, err
		}
		progress.setTotal(q.Repository.Issues.TotalCount)
		return q.Repository.Issues.Nodes,
			q.Repository.Issues.PageInfo, nil
	}, progress.recordPage)
	if err != nil {
		return nil, err
	}
	progress.done()

	result := &RepoBulkResult{
		Issues: make([]BulkIssue, 0, len(gqlIssues)),
	}
	for i := range gqlIssues {
		bulk := convertGQLIssue(&gqlIssues[i])
		result.Issues = append(result.Issues, bulk)
	}
	return result, nil
}

func cursorVar(cursor *string) *githubv4.String {
	if cursor == nil {
		return nil
	}
	s := githubv4.String(*cursor)
	return &s
}

func convertGQLPR(gql *gqlPR) BulkPR {
	bulk := BulkPR{
		PR:               adaptPR(gql),
		CommentsComplete: !gql.Comments.PageInfo.HasNextPage,
		ReviewsComplete:  !gql.Reviews.PageInfo.HasNextPage,
		CommitsComplete:  !gql.AllCommits.PageInfo.HasNextPage,
		TimelineComplete: !gql.TimelineItems.PageInfo.HasNextPage,
	}

	for i := range gql.Comments.Nodes {
		bulk.Comments = append(bulk.Comments, adaptComment(&gql.Comments.Nodes[i]))
	}
	for i := range gql.Reviews.Nodes {
		bulk.Reviews = append(bulk.Reviews, adaptReview(&gql.Reviews.Nodes[i]))
	}
	for i := range gql.AllCommits.Nodes {
		bulk.Commits = append(bulk.Commits, adaptCommit(&gql.AllCommits.Nodes[i]))
	}
	for i := range gql.TimelineItems.Nodes {
		event, ok := adaptPullRequestTimelineEvent(&gql.TimelineItems.Nodes[i])
		if ok {
			bulk.TimelineEvents = append(bulk.TimelineEvents, event)
		}
	}

	bulk.CIComplete = true
	if len(gql.LastCommit.Nodes) > 0 {
		rollup := gql.LastCommit.Nodes[0].Commit.StatusCheckRollup
		if rollup != nil {
			bulk.CIComplete = !rollup.Contexts.PageInfo.HasNextPage
			bulk.CheckRuns, bulk.Statuses = splitCheckContexts(
				rollup.Contexts.Nodes,
			)
		}
	}

	return bulk
}

func adaptIssueTimelineEvent(gql *gqlIssueTimelineItem) (PullRequestTimelineEvent, bool) {
	if gql == nil {
		return PullRequestTimelineEvent{}, false
	}
	event := PullRequestTimelineEvent{NodeID: gql.Node.ID}
	switch gql.Typename {
	case "AssignedEvent":
		copyAssignmentEvent(&event, "assigned", gql.AssignedEvent)
	case "UnassignedEvent":
		copyAssignmentEvent(&event, "unassigned", gql.UnassignedEvent)
	default:
		return PullRequestTimelineEvent{}, false
	}
	return event, true
}

func adaptPullRequestTimelineEvent(gql *gqlPullRequestTimelineItem) (PullRequestTimelineEvent, bool) {
	if gql == nil {
		return PullRequestTimelineEvent{}, false
	}
	event := PullRequestTimelineEvent{NodeID: gql.Node.ID}
	switch gql.Typename {
	case "HeadRefForcePushedEvent":
		src := gql.HeadRefForcePushedEvent
		event.EventType = "force_push"
		event.CreatedAt = src.CreatedAt
		if src.Actor != nil {
			event.Actor = src.Actor.Login
		}
		if src.BeforeCommit != nil {
			event.BeforeSHA = src.BeforeCommit.OID
		}
		if src.AfterCommit != nil {
			event.AfterSHA = src.AfterCommit.OID
		}
		if src.Ref != nil {
			event.Ref = src.Ref.Name
		}
	case "CommentDeletedEvent":
		src := gql.CommentDeletedEvent
		event.EventType = "comment_deleted"
		event.CreatedAt = src.CreatedAt
		if src.Actor != nil {
			event.Actor = src.Actor.Login
		}
		if src.DeletedCommentAuthor != nil {
			event.DeletedCommentAuthor = src.DeletedCommentAuthor.Login
		}
	case "CrossReferencedEvent":
		src := gql.CrossReferencedEvent
		event.EventType = "cross_referenced"
		event.CreatedAt = src.CreatedAt
		event.IsCrossRepository = src.IsCrossRepository
		event.WillCloseTarget = src.WillCloseTarget
		if src.Actor != nil {
			event.Actor = src.Actor.Login
		}
		event.SourceType = src.Source.Typename
		switch src.Source.Typename {
		case "Issue":
			copyReferencedSubject(&event, src.Source.Issue)
		case "PullRequest":
			copyReferencedSubject(&event, src.Source.PullRequest)
		}
	case "RenamedTitleEvent":
		src := gql.RenamedTitleEvent
		event.EventType = "renamed_title"
		event.CreatedAt = src.CreatedAt
		event.PreviousTitle = src.PreviousTitle
		event.CurrentTitle = src.CurrentTitle
		if src.Actor != nil {
			event.Actor = src.Actor.Login
		}
	case "BaseRefChangedEvent":
		src := gql.BaseRefChangedEvent
		event.EventType = "base_ref_changed"
		event.CreatedAt = src.CreatedAt
		event.PreviousRefName = src.PreviousRefName
		event.CurrentRefName = src.CurrentRefName
		if src.Actor != nil {
			event.Actor = src.Actor.Login
		}
	case "AssignedEvent":
		copyAssignmentEvent(&event, "assigned", gql.AssignedEvent)
	case "UnassignedEvent":
		copyAssignmentEvent(&event, "unassigned", gql.UnassignedEvent)
	default:
		return PullRequestTimelineEvent{}, false
	}
	return event, true
}

func copyAssignmentEvent(event *PullRequestTimelineEvent, eventType string, src gqlAssignedEvent) {
	event.EventType = eventType
	event.Assignee = src.Assignee.Login()
	event.CreatedAt = src.CreatedAt
	if src.Actor != nil {
		event.Actor = src.Actor.Login
	}
}

func copyReferencedSubject(event *PullRequestTimelineEvent, source gqlReferencedIssueOrPR) {
	event.SourceNumber = source.Number
	event.SourceTitle = source.Title
	event.SourceURL = source.URL
	event.SourceOwner = source.Repository.Owner.Login
	event.SourceRepo = source.Repository.Name
}

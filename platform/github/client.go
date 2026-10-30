package github

import (
	"context"
	"net/http"
	"sync"
	"time"

	gh "github.com/google/go-github/v93/github"
	"go.kenn.io/forge/platform"
)

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
	NodeID            string
	DatabaseID        int64
	ReviewDatabaseID  int64
	SubjectType       string
	Body              string
	AuthorLogin       string
	AuthorAssociation *string
	Path              string
	Line              int
	OriginalLine      int
	DiffHunk          string
	URL               string
	CommitID          string
	OriginalCommitID  string
	IsMinimized       bool
	MinimizedReason   string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// EditPullRequestOpts holds optional fields for editing a pull request.
// Nil pointer fields are omitted from the GitHub API call.
type EditPullRequestOpts struct {
	State *string
	Title *string
	Body  *string
}

type Client struct {
	gh *gh.Client
	// ghWrite/httpWriteClient carry mutation traffic on the user's own
	// credential (mutation-marked auth context, own go-github rate
	// cache). Their responses feed writeRateTracker, not rateTracker:
	// the credentials have separate budgets when a GitHub App is
	// configured, and mutation availability gates on the write
	// credential's budget. Nil in hand-built test clients; accessors
	// fall back to the read client.
	ghWrite *gh.Client
	// ghNotifications/httpNotificationClient carry user-scoped
	// notification APIs on the user's own credential, through the
	// background sync budget transport. In GitHub App split-auth mode
	// their PAT rate headers must not feed the app-token read tracker or
	// the mutation write tracker.
	ghNotifications *gh.Client
	// source is the credential chain reads resolve through. Split
	// behavior (the viewer-permission overlay in GetRepository) is
	// derived from its current descriptor on every call, so a config
	// reload that adds or removes a GitHub App candidate takes effect
	// without restart and repo-override chains that exclude the app
	// keep single-credential behavior.
	source                  platform.CredentialSource
	httpClient              *http.Client
	httpWriteClient         *http.Client
	httpNotificationClient  *http.Client
	markdownImageHTTPClient *http.Client
	rateTracker             platform.RateObserver
	writeRateTracker        platform.RateObserver
	notificationRateTracker platform.RateObserver
	graphQLRateTracker      platform.RateObserver
	writeGraphQLRateTracker platform.RateObserver
	platformHost            string
	graphQLEndpoint         string
	invalidateETags         func(string, string, ...string)
	auth                    Authentication
	now                     func() time.Time
	viewerCacheTTL          time.Duration
	readOnlyContext         func(context.Context) bool
	writesDisabled          bool
	ownerContext            func(context.Context, string) context.Context
	graphQLContext          func(context.Context) context.Context
	progressFactory         func(string, string, string) Progress
	warning                 func(string, ...any)
	viewerMu                sync.Mutex
	viewerLogin             string
	viewerLoginAt           time.Time
	viewerLoginCacheKey     string
	viewerRepoMu            sync.Mutex
	viewerRepos             map[string]viewerRepoOverlay
}

// viewerRepoOverlay is the user's view of a repository, kept with the ETag
// that revalidates it.
type viewerRepoOverlay struct {
	permissions   *gh.RepositoryPermissions
	mergeSettings bool
	allowSquash   bool
	allowMerge    bool
	allowRebase   bool
	etag          string
}

func (c *Client) writeGH() *gh.Client {
	if c.ghWrite != nil {
		return c.ghWrite
	}
	return c.gh
}

func (c *Client) notificationGH() *gh.Client {
	if c.ghNotifications != nil {
		return c.ghNotifications
	}
	return c.writeGH()
}

func (c *Client) writeHTTPClient() *http.Client {
	if c.httpWriteClient != nil {
		return c.httpWriteClient
	}
	return c.httpClient
}

// SetGraphQLRateTracker attaches the tracker used by Client's direct
// GraphQL HTTP helpers.
func (c *Client) SetGraphQLRateTracker(rateTracker platform.RateObserver) {
	c.graphQLRateTracker = rateTracker
}

// SetWriteRateTracker attaches the tracker fed by mutation-path
// responses (the user's own credential when a GitHub App handles
// sync reads).
func (c *Client) SetWriteRateTracker(rateTracker platform.RateObserver) {
	c.writeRateTracker = rateTracker
}

// SetWriteGraphQLRateTracker attaches the tracker fed by GraphQL
// mutation responses (ready-for-review), which consume the write
// credential's GraphQL budget rather than its REST budget.
func (c *Client) SetWriteGraphQLRateTracker(rateTracker platform.RateObserver) {
	c.writeGraphQLRateTracker = rateTracker
}

// splitAuthActive reports whether reads currently resolve through a
// GitHub App installation token while writes use the user's own
// credential (the mutation-marked chain skips the app candidate).
func (c *Client) splitAuthActive() bool {
	return c.auth.InstallationActive != nil && c.auth.InstallationActive("")
}

// splitAuthActiveForOwner is splitAuthActive scoped to a single owner:
// it reports whether reads for owner actually resolve through an app
// installation token. A host descriptor may carry app candidates for
// several owners, but a candidate scoped to one installation account is
// skipped for any other owner during token resolution. Gate
// installation-token-only endpoints on this so a PAT-backed owner on a
// host that also hosts another owner's app is not routed there.
func (c *Client) splitAuthActiveForOwner(owner string) bool {
	return c.auth.InstallationActive != nil && c.auth.InstallationActive(owner)
}

// ProbeWriteCredential resolves the mutation side of the route without
// sending a request. Identity-bound sources use this to detect a removed PAT
// or a token that rotated to another GitHub user before the UI offers writes.
func (c *Client) ProbeWriteCredential(ctx context.Context) error {
	if c.source == nil {
		return platform.ErrMissingToken
	}
	_, err := c.source.Token(c.authContext(ctx, "", true))
	return err
}

func (c *Client) InstallationAuthenticationActive() bool {
	return c.splitAuthActive()
}

// InvalidateListETagsForRepo evicts cached ETag entries for the repo's
// list endpoints. Pass any combination of "pulls", "issues", and
// "comments" to scope the invalidation; omitting endpoints clears
// every supported repo-scoped list path. Safe to call when the
// transport is nil (tests).
func (c *Client) InvalidateListETagsForRepo(owner, repo string, endpoints ...string) {
	if c.invalidateETags == nil {
		return
	}
	c.invalidateETags(owner, repo, endpoints...)
}

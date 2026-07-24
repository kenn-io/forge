package server

import (
	"time"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/server/workspaceapi"
)

type repoResponse struct {
	ID                  int64
	Platform            string
	PlatformHost        string
	Owner               string
	Name                string
	LastSyncStartedAt   *time.Time
	LastSyncCompletedAt *time.Time
	LastSyncError       string
	AllowSquashMerge    bool
	AllowMergeCommit    bool
	AllowRebaseMerge    bool
	ViewerCanMerge      bool
	CreatedAt           time.Time
	Capabilities        httpapi.ProviderCapabilitiesResponse `json:"capabilities"`
	Operations          httpapi.RepoOperations               `json:"operations"`
}

type repoSummaryAuthorResponse struct {
	Login     string `json:"login"`
	ItemCount int    `json:"item_count"`
}

type repoSummaryIssueResponse struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	Author         string `json:"author"`
	State          string `json:"state"`
	URL            string `json:"url"`
	LastActivityAt string `json:"last_activity_at"`
}

type repoSummaryReleaseResponse struct {
	TagName         string `json:"tag_name"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	TargetCommitish string `json:"target_commitish"`
	Prerelease      bool   `json:"prerelease"`
	PublishedAt     string `json:"published_at,omitempty"`
}

type repoSummaryCommitPointResponse struct {
	SHA         string `json:"sha"`
	Message     string `json:"message"`
	CommittedAt string `json:"committed_at"`
}

type repoSummaryResponse struct {
	Repo                 httpapi.RepoRefResponse          `json:"repo"`
	PlatformHost         string                           `json:"platform_host"`
	DefaultPlatformHost  string                           `json:"default_platform_host"`
	Owner                string                           `json:"owner"`
	Name                 string                           `json:"name"`
	LastSyncStartedAt    string                           `json:"last_sync_started_at,omitempty"`
	LastSyncCompletedAt  string                           `json:"last_sync_completed_at,omitempty"`
	LastSyncError        string                           `json:"last_sync_error,omitempty"`
	CachedPRCount        int                              `json:"cached_pr_count"`
	OpenPRCount          int                              `json:"open_pr_count"`
	DraftPRCount         int                              `json:"draft_pr_count"`
	CachedIssueCount     int                              `json:"cached_issue_count"`
	OpenIssueCount       int                              `json:"open_issue_count"`
	MostRecentActivityAt string                           `json:"most_recent_activity_at,omitempty"`
	LatestRelease        *repoSummaryReleaseResponse      `json:"latest_release,omitempty"`
	Releases             []repoSummaryReleaseResponse     `json:"releases"`
	CommitsSinceRelease  *int                             `json:"commits_since_release,omitempty"`
	CommitTimeline       []repoSummaryCommitPointResponse `json:"commit_timeline"`
	TimelineUpdatedAt    string                           `json:"timeline_updated_at,omitempty"`
	ActiveAuthors        []repoSummaryAuthorResponse      `json:"active_authors"`
	RecentIssues         []repoSummaryIssueResponse       `json:"recent_issues"`
	Operations           httpapi.RepoOperations           `json:"operations"`
}

type commentAutocompleteResponse struct {
	Users      []string                          `json:"users,omitempty"`
	References []db.CommentAutocompleteReference `json:"references,omitempty"`
}

type notificationResponse struct {
	ID                      int64  `json:"id"`
	PlatformHost            string `json:"platform_host"`
	Provider                string `json:"provider"`
	RepoPath                string `json:"repo_path"`
	PlatformThreadID        string `json:"platform_thread_id"`
	RepoOwner               string `json:"repo_owner"`
	RepoName                string `json:"repo_name"`
	SubjectType             string `json:"subject_type"`
	SubjectTitle            string `json:"subject_title"`
	SubjectURL              string `json:"subject_url"`
	SubjectLatestCommentURL string `json:"subject_latest_comment_url"`
	WebURL                  string `json:"web_url"`
	ItemNumber              *int   `json:"item_number,omitempty"`
	ItemType                string `json:"item_type"`
	ItemAuthor              string `json:"item_author"`
	Reason                  string `json:"reason"`
	Unread                  bool   `json:"unread"`
	Participating           bool   `json:"participating"`
	GitHubUpdatedAt         string `json:"github_updated_at"`
	GitHubLastReadAt        string `json:"github_last_read_at,omitempty"`
	DoneAt                  string `json:"done_at,omitempty"`
	DoneReason              string `json:"done_reason"`
	GitHubReadQueuedAt      string `json:"github_read_queued_at,omitempty"`
	GitHubReadSyncedAt      string `json:"github_read_synced_at,omitempty"`
	GitHubReadError         string `json:"github_read_error"`
	GitHubReadAttempts      int    `json:"github_read_attempts"`
	GitHubReadLastAttemptAt string `json:"github_read_last_attempt_at,omitempty"`
	GitHubReadNextAttemptAt string `json:"github_read_next_attempt_at,omitempty"`
}

type notificationSummaryResponse struct {
	TotalActive int            `json:"total_active"`
	Unread      int            `json:"unread"`
	Done        int            `json:"done"`
	ByReason    map[string]int `json:"by_reason"`
	ByRepo      map[string]int `json:"by_repo"`
}

type notificationSyncStatusResponse struct {
	Running        bool   `json:"running"`
	LastStartedAt  string `json:"last_started_at,omitempty"`
	LastFinishedAt string `json:"last_finished_at,omitempty"`
	LastError      string `json:"last_error"`
}

type notificationsResponse struct {
	Items   []notificationResponse         `json:"items"`
	Summary notificationSummaryResponse    `json:"summary"`
	Sync    notificationSyncStatusResponse `json:"sync"`
}

type notificationBulkFailure struct {
	ID    int64  `json:"id"`
	Error string `json:"error"`
}

type notificationBulkResponse struct {
	Succeeded []int64                   `json:"succeeded"`
	Queued    []int64                   `json:"queued"`
	Failed    []notificationBulkFailure `json:"failed"`
}

type resolveItemResponse struct {
	ItemType    string `json:"item_type" doc:"'pr' or 'issue'"`
	Number      int    `json:"number"`
	RepoTracked bool   `json:"repo_tracked"`
}

type diffResponse = httpapi.DiffResponse
type rateLimitHostStatus struct {
	Provider           string `json:"provider"`
	PlatformHost       string `json:"platform_host"`
	RequestsHour       int    `json:"requests_hour"`
	RateRemaining      int    `json:"rate_remaining"`
	RateLimit          int    `json:"rate_limit"`
	RateResetAt        string `json:"rate_reset_at"`
	HourStart          string `json:"hour_start"`
	SyncThrottleFactor int    `json:"sync_throttle_factor"`
	SyncPaused         bool   `json:"sync_paused"`
	ReserveBuffer      int    `json:"reserve_buffer"`
	Known              bool   `json:"known"`
	BudgetLimit        int    `json:"budget_limit"`
	BudgetSpent        int    `json:"budget_spent"`
	BudgetRemaining    int    `json:"budget_remaining"`
	GQLRemaining       int    `json:"gql_remaining"`
	GQLLimit           int    `json:"gql_limit"`
	GQLResetAt         string `json:"gql_reset_at"`
	GQLKnown           bool   `json:"gql_known"`
}

type rateLimitsResponse struct {
	Hosts map[string]rateLimitHostStatus `json:"hosts"`
}

const activitySafetyCap = 5000

type activityResponse struct {
	Items  []activityItemResponse `json:"items"`
	Capped bool                   `json:"capped"`
}

type activityItemResponse struct {
	ID             string                     `json:"id"`
	Cursor         string                     `json:"cursor"`
	ActivityType   string                     `json:"activity_type"`
	Repo           httpapi.RepoRefResponse    `json:"repo"`
	PlatformHost   string                     `json:"platform_host"`
	RepoOwner      string                     `json:"repo_owner"`
	RepoName       string                     `json:"repo_name"`
	ItemType       string                     `json:"item_type"`
	ItemNumber     int                        `json:"item_number"`
	ItemTitle      string                     `json:"item_title"`
	ItemURL        string                     `json:"item_url"`
	ItemState      string                     `json:"item_state"`
	Workspace      *workspaceapi.WorkspaceRef `json:"workspace,omitempty"`
	Author         string                     `json:"author"`
	ItemAuthor     string                     `json:"item_author,omitempty"`
	CreatedAt      string                     `json:"created_at"`
	BodyPreview    string                     `json:"body_preview"`
	BranchName     string                     `json:"branch_name,omitempty"`
	CommitSHA      string                     `json:"commit_sha,omitempty"`
	BeforeSHA      string                     `json:"before_sha,omitempty"`
	AfterSHA       string                     `json:"after_sha,omitempty"`
	AuthorName     string                     `json:"author_name,omitempty"`
	AuthorEmail    string                     `json:"author_email,omitempty"`
	CommitterName  string                     `json:"committer_name,omitempty"`
	CommitterEmail string                     `json:"committer_email,omitempty"`
	AuthoredAt     string                     `json:"authored_at,omitempty"`
	CommittedAt    string                     `json:"committed_at,omitempty"`
	ActivityURL    string                     `json:"activity_url,omitempty"`
	SubjectState   string                     `json:"subject_state,omitempty"`
}

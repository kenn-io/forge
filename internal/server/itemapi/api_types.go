package itemapi

import (
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

type RepoResponse struct {
	ID                  int64
	Platform            string
	PlatformHost        string
	PlatformRepoID      string
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

type RepoSummaryAuthorResponse struct {
	Login     string `json:"login"`
	ItemCount int    `json:"item_count"`
}

type RepoSummaryIssueResponse struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	Author         string `json:"author"`
	State          string `json:"state"`
	URL            string `json:"url"`
	LastActivityAt string `json:"last_activity_at"`
}

type RepoSummaryReleaseResponse struct {
	TagName         string `json:"tag_name"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	TargetCommitish string `json:"target_commitish"`
	Prerelease      bool   `json:"prerelease"`
	PublishedAt     string `json:"published_at,omitempty"`
}

type RepoSummaryCommitPointResponse struct {
	SHA         string `json:"sha"`
	Message     string `json:"message"`
	CommittedAt string `json:"committed_at"`
}

type RoborevConfiguredRepositoryResponse struct {
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	RepoPath     string `json:"repo_path"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
}

type RoborevConfiguredRepositoriesResponse struct {
	Repositories []RoborevConfiguredRepositoryResponse `json:"repositories"`
	Complete     bool                                  `json:"complete"`
}

type RepoSummaryResponse struct {
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
	LatestRelease        *RepoSummaryReleaseResponse      `json:"latest_release,omitempty"`
	Releases             []RepoSummaryReleaseResponse     `json:"releases"`
	CommitsSinceRelease  *int                             `json:"commits_since_release,omitempty"`
	CommitTimeline       []RepoSummaryCommitPointResponse `json:"commit_timeline"`
	TimelineUpdatedAt    string                           `json:"timeline_updated_at,omitempty"`
	ActiveAuthors        []RepoSummaryAuthorResponse      `json:"active_authors"`
	RecentIssues         []RepoSummaryIssueResponse       `json:"recent_issues"`
	Operations           httpapi.RepoOperations           `json:"operations"`
}

type CommentAutocompleteResponse struct {
	Users      []string                          `json:"users,omitempty"`
	References []db.CommentAutocompleteReference `json:"references,omitempty"`
}

type NotificationResponse struct {
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

type NotificationSummaryResponse struct {
	TotalActive int            `json:"total_active"`
	Unread      int            `json:"unread"`
	Done        int            `json:"done"`
	ByReason    map[string]int `json:"by_reason"`
	ByRepo      map[string]int `json:"by_repo"`
}

type NotificationSyncStatusResponse struct {
	Running        bool   `json:"running"`
	LastStartedAt  string `json:"last_started_at,omitempty"`
	LastFinishedAt string `json:"last_finished_at,omitempty"`
	LastError      string `json:"last_error"`
}

type NotificationsResponse struct {
	Items   []NotificationResponse         `json:"items"`
	Summary NotificationSummaryResponse    `json:"summary"`
	Sync    NotificationSyncStatusResponse `json:"sync"`
}

type NotificationBulkFailure struct {
	ID    int64  `json:"id"`
	Error string `json:"error"`
}

type NotificationBulkResponse struct {
	Succeeded []int64                   `json:"succeeded"`
	Queued    []int64                   `json:"queued"`
	Failed    []NotificationBulkFailure `json:"failed"`
}

type resolveItemResponse struct {
	ItemType    string `json:"item_type" doc:"'pr' or 'issue'"`
	Number      int    `json:"number"`
	RepoTracked bool   `json:"repo_tracked"`
}

type DiffResponse = httpapi.DiffResponse

type RateLimitResourceStatus struct {
	Remaining int    `json:"remaining"`
	Limit     int    `json:"limit"`
	ResetAt   string `json:"reset_at"`
	Known     bool   `json:"known"`
	Requests  int    `json:"requests"`
}

// rateLimitHostStatus is one credential principal's provider-side quota on one
// host. GitHub meters each principal independently, so an App installation and
// the user's PAT appear as separate entries for the same host.
type RateLimitHostStatus struct {
	Provider           string                  `json:"provider"`
	PlatformHost       string                  `json:"platform_host"`
	RatePrincipal      string                  `json:"rate_principal"`
	PrincipalLabel     string                  `json:"principal_label"`
	ReserveBuffer      int                     `json:"reserve_buffer"`
	SyncThrottleFactor int                     `json:"sync_throttle_factor"`
	SyncPaused         bool                    `json:"sync_paused"`
	REST               RateLimitResourceStatus `json:"rest"`
	GraphQL            RateLimitResourceStatus `json:"graphql"`
}

// localSyncCeilingStatus is kenn-forge's own hourly spend guard for one
// principal. It is independent of provider quota: the ceiling can be reached
// while GitHub still reports capacity, and it resets on its own clock.
type LocalSyncCeilingStatus struct {
	Provider        string `json:"provider"`
	PlatformHost    string `json:"platform_host"`
	RatePrincipal   string `json:"rate_principal"`
	PrincipalLabel  string `json:"principal_label"`
	Limit           int    `json:"limit"`
	BackgroundLimit int    `json:"background_limit"`
	Spent           int    `json:"spent"`
	Remaining       int    `json:"remaining"`
	ResetAt         string `json:"reset_at"`
}

type RateLimitsResponse struct {
	ProviderPools map[string]RateLimitHostStatus    `json:"provider_pools"`
	LocalCeilings map[string]LocalSyncCeilingStatus `json:"local_ceilings"`
}

const ActivitySafetyCap = 5000

type ActivityResponse struct {
	Items                          []ActivityItemResponse             `json:"items"`
	ItemActivity                   []ActivitySubjectResponse          `json:"item_activity"`
	WorkspaceActivity              []WorkspaceActivitySubjectResponse `json:"workspace_activity"`
	UseWorkspaceActivityForRecency bool                               `json:"use_workspace_activity_for_recency"`
	Capped                         bool                               `json:"capped"`
	ItemActivityCapped             bool                               `json:"item_activity_capped"`
	EventCursor                    string                             `json:"event_cursor"`
	NextCursor                     string                             `json:"next_cursor,omitempty"`
}

// activityRepoRefResponse carries only stable identity and current route
// metadata. Repeating provider capabilities on every Activity row made the
// parent-only projection several times larger without serving a consumer.
type ActivityRepoRefResponse struct {
	Provider       string `json:"provider"`
	PlatformHost   string `json:"platform_host"`
	PlatformRepoID string `json:"platform_repo_id,omitempty"`
	RepoPath       string `json:"repo_path"`
	Owner          string `json:"owner"`
	Name           string `json:"name"`
}

type ActivitySubjectResponse struct {
	Repo                ActivityRepoRefResponse    `json:"repo"`
	PlatformHost        string                     `json:"platform_host"`
	RepoOwner           string                     `json:"repo_owner"`
	RepoName            string                     `json:"repo_name"`
	ItemType            string                     `json:"item_type"`
	ItemNumber          int                        `json:"item_number"`
	ItemTitle           string                     `json:"item_title"`
	ItemURL             string                     `json:"item_url"`
	ItemState           string                     `json:"item_state"`
	ItemAuthor          string                     `json:"item_author,omitempty"`
	Workspace           *workspaceapi.WorkspaceRef `json:"workspace,omitempty"`
	ActivityAt          string                     `json:"activity_at" format:"date-time"`
	EventLedgerRevision string                     `json:"event_ledger_revision"`
}

type WorkspaceActivitySubjectResponse struct {
	Repo         ActivityRepoRefResponse    `json:"repo"`
	PlatformHost string                     `json:"platform_host"`
	RepoOwner    string                     `json:"repo_owner"`
	RepoName     string                     `json:"repo_name"`
	ItemType     string                     `json:"item_type"`
	ItemNumber   int                        `json:"item_number"`
	ItemTitle    string                     `json:"item_title"`
	ItemURL      string                     `json:"item_url"`
	ItemState    string                     `json:"item_state"`
	ItemAuthor   string                     `json:"item_author,omitempty"`
	Workspace    *workspaceapi.WorkspaceRef `json:"workspace,omitempty"`
	ActivityAt   string                     `json:"activity_at" format:"date-time"`
}

type ActivityAuthorsResponse struct {
	Authors                        []string `json:"authors"`
	UseWorkspaceActivityForRecency bool     `json:"use_workspace_activity_for_recency"`
}

type ActivityItemResponse struct {
	ID                 string                     `json:"id"`
	Cursor             string                     `json:"cursor"`
	ActivityType       string                     `json:"activity_type"`
	Repo               ActivityRepoRefResponse    `json:"repo"`
	PlatformHost       string                     `json:"platform_host"`
	RepoOwner          string                     `json:"repo_owner"`
	RepoName           string                     `json:"repo_name"`
	ItemType           string                     `json:"item_type"`
	ItemNumber         int                        `json:"item_number"`
	ItemTitle          string                     `json:"item_title"`
	ItemURL            string                     `json:"item_url"`
	ItemState          string                     `json:"item_state"`
	Workspace          *workspaceapi.WorkspaceRef `json:"workspace,omitempty"`
	Author             string                     `json:"author"`
	ItemAuthor         string                     `json:"item_author,omitempty"`
	CreatedAt          string                     `json:"created_at"`
	ItemLastActivityAt string                     `json:"item_last_activity_at,omitempty" format:"date-time"`
	BodyPreview        string                     `json:"body_preview"`
	BranchName         string                     `json:"branch_name,omitempty"`
	CommitSHA          string                     `json:"commit_sha,omitempty"`
	BeforeSHA          string                     `json:"before_sha,omitempty"`
	AfterSHA           string                     `json:"after_sha,omitempty"`
	AuthorName         string                     `json:"author_name,omitempty"`
	AuthorEmail        string                     `json:"author_email,omitempty"`
	CommitterName      string                     `json:"committer_name,omitempty"`
	CommitterEmail     string                     `json:"committer_email,omitempty"`
	AuthoredAt         string                     `json:"authored_at,omitempty"`
	CommittedAt        string                     `json:"committed_at,omitempty"`
	ActivityURL        string                     `json:"activity_url,omitempty"`
	SubjectState       string                     `json:"subject_state,omitempty"`
}

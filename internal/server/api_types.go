package server

import (
	"time"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/server/workspaceapi"
)

type worktreeLinkResponse struct {
	HostKey        string `json:"host_key,omitempty"`
	WorktreeKey    string `json:"worktree_key"`
	WorktreePath   string `json:"worktree_path,omitempty"`
	WorktreeBranch string `json:"worktree_branch,omitempty"`
}

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

// mergeRequestResponse extends db.MergeRequest with resolved repo owner/name fields.
type mergeRequestResponse struct {
	db.MergeRequest
	Repo            httpapi.RepoRefResponse    `json:"repo"`
	RepoOwner       string                     `json:"repo_owner"`
	RepoName        string                     `json:"repo_name"`
	PlatformHost    string                     `json:"platform_host"`
	WorktreeLinks   []worktreeLinkResponse     `json:"worktree_links"`
	Workspace       *workspaceapi.WorkspaceRef `json:"workspace,omitempty"`
	DetailLoaded    bool                       `json:"detail_loaded"`
	DetailFetchedAt string                     `json:"detail_fetched_at,omitempty"`
}

type mergeRequestEventResponse struct {
	ID                 int64
	MergeRequestID     int64
	PlatformID         *int64
	PlatformExternalID string
	EventType          string
	Author             string
	Summary            string
	Body               string
	MetadataJSON       string
	CreatedAt          time.Time
	DedupeKey          string
	DirectURL          string
	ThreadID           *string
	Resolvable         bool
	Resolved           bool
	DiffThread         *diffReviewThreadResponse `json:"diff_thread,omitempty"`
}

type workflowApprovalResponse struct {
	Checked  bool `json:"checked"`
	Required bool `json:"required"`
	Count    int  `json:"count"`
}

type mergeRequestDetailResponse struct {
	MergeRequest     *db.MergeRequest            `json:"merge_request"`
	Events           []mergeRequestEventResponse `json:"events"`
	Repo             httpapi.RepoRefResponse     `json:"repo"`
	RepoOwner        string                      `json:"repo_owner"`
	RepoName         string                      `json:"repo_name"`
	PlatformHost     string                      `json:"platform_host"`
	PlatformHeadSHA  string                      `json:"platform_head_sha"`
	PlatformBaseSHA  string                      `json:"platform_base_sha"`
	ReviewedHeadSHA  string                      `json:"reviewed_head_sha"`
	DiffHeadSHA      string                      `json:"diff_head_sha"`
	MergeBaseSHA     string                      `json:"merge_base_sha"`
	WorktreeLinks    []worktreeLinkResponse      `json:"worktree_links"`
	WorkflowApproval workflowApprovalResponse    `json:"workflow_approval"`
	Warnings         []string                    `json:"warnings,omitempty"`
	DetailLoaded     bool                        `json:"detail_loaded"`
	// DeferredMergePending reports whether a background "merge after CI"
	// worker is currently waiting on this pull request in this server
	// process, so the UI can show the queued state instead of a merge
	// action.
	DeferredMergePending bool                       `json:"deferred_merge_pending"`
	DetailFetchedAt      string                     `json:"detail_fetched_at,omitempty"`
	Workspace            *workspaceapi.WorkspaceRef `json:"workspace,omitempty"`
	Stack                *stackContextResponse      `json:"stack,omitempty"`
	// Checks is the merge request's CI checks decoded from its cached
	// ci_checks_json. Omitted when the merge request has no cached checks.
	Checks []db.CICheck `json:"checks,omitempty"`
}

var validKanbanStates = map[string]bool{
	"new":            true,
	"reviewing":      true,
	"waiting":        true,
	"awaiting_merge": true,
}

type workflowStateMetaResponse struct {
	Status        db.KanbanStatus `json:"status" enum:"new,reviewing,waiting,awaiting_merge"`
	UpdatedAt     string          `json:"updated_at,omitempty" format:"date-time"`
	UpdatedSource string          `json:"updated_source,omitempty"`
	UpdatedActor  string          `json:"updated_actor,omitempty"`
	UpdatedReason string          `json:"updated_reason,omitempty"`
}

type issueResponse struct {
	db.Issue
	Repo            httpapi.RepoRefResponse    `json:"repo"`
	PlatformHost    string                     `json:"platform_host"`
	RepoOwner       string                     `json:"repo_owner"`
	RepoName        string                     `json:"repo_name"`
	Workspace       *workspaceapi.WorkspaceRef `json:"workspace,omitempty"`
	DetailLoaded    bool                       `json:"detail_loaded"`
	DetailFetchedAt string                     `json:"detail_fetched_at,omitempty"`
}

type issueDetailResponse struct {
	Issue           *db.Issue                  `json:"issue"`
	Events          []db.IssueEvent            `json:"events"`
	Repo            httpapi.RepoRefResponse    `json:"repo"`
	PlatformHost    string                     `json:"platform_host"`
	RepoOwner       string                     `json:"repo_owner"`
	RepoName        string                     `json:"repo_name"`
	DetailLoaded    bool                       `json:"detail_loaded"`
	DetailFetchedAt string                     `json:"detail_fetched_at,omitempty"`
	Workspace       *workspaceapi.WorkspaceRef `json:"workspace,omitempty"`
	Workflow        *workflowStateMetaResponse `json:"workflow,omitempty"`
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
type filesResponse = httpapi.FilesResponse

type diffReviewLineRange struct {
	Path        string `json:"path"`
	OldPath     string `json:"old_path,omitempty"`
	Side        string `json:"side"`
	StartSide   string `json:"start_side,omitempty"`
	StartLine   *int   `json:"start_line,omitempty"`
	Line        int    `json:"line"`
	OldLine     *int   `json:"old_line,omitempty"`
	NewLine     *int   `json:"new_line,omitempty"`
	LineType    string `json:"line_type"`
	DiffHeadSHA string `json:"diff_head_sha,omitempty"`
	CommitSHA   string `json:"commit_sha,omitempty"`
}

type diffReviewDraftComment struct {
	ID          string `json:"id"`
	Body        string `json:"body"`
	Path        string `json:"path"`
	OldPath     string `json:"old_path,omitempty"`
	Side        string `json:"side"`
	StartSide   string `json:"start_side,omitempty"`
	StartLine   *int   `json:"start_line,omitempty"`
	Line        int    `json:"line"`
	OldLine     *int   `json:"old_line,omitempty"`
	NewLine     *int   `json:"new_line,omitempty"`
	LineType    string `json:"line_type"`
	DiffHeadSHA string `json:"diff_head_sha,omitempty"`
	CommitSHA   string `json:"commit_sha,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type diffReviewDraftResponse struct {
	DraftID               string                   `json:"draft_id,omitempty"`
	Comments              []diffReviewDraftComment `json:"comments"`
	SupportedActions      []string                 `json:"supported_actions"`
	NativeMultilineRanges bool                     `json:"native_multiline_ranges"`
}

type diffReviewThreadResponse struct {
	ID                string `json:"id"`
	ProviderCommentID string `json:"provider_comment_id,omitempty"`
	Path              string `json:"path"`
	OldPath           string `json:"old_path,omitempty"`
	Side              string `json:"side"`
	StartSide         string `json:"start_side,omitempty"`
	StartLine         *int   `json:"start_line,omitempty"`
	Line              int    `json:"line"`
	OldLine           *int   `json:"old_line,omitempty"`
	NewLine           *int   `json:"new_line,omitempty"`
	LineType          string `json:"line_type"`
	DiffHeadSHA       string `json:"diff_head_sha,omitempty"`
	CommitSHA         string `json:"commit_sha,omitempty"`
	Body              string `json:"body"`
	AuthorLogin       string `json:"author_login,omitempty"`
	Resolved          bool   `json:"resolved"`
	CanResolve        bool   `json:"can_resolve"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type filePreviewResponse = httpapi.FilePreviewResponse

type mrImportMetadataResponse struct {
	Number           int    `json:"number"`
	HeadBranch       string `json:"head_branch"`
	PlatformHeadSHA  string `json:"platform_head_sha"`
	HeadRepoCloneURL string `json:"head_repo_clone_url"`
	State            string `json:"state"`
	IsDraft          bool   `json:"is_draft"`
	Title            string `json:"title"`
}

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

type commitResponse = httpapi.CommitResponse
type commitsResponse = httpapi.CommitsResponse

const activitySafetyCap = 5000

type activityResponse struct {
	Items  []activityItemResponse `json:"items"`
	Capped bool                   `json:"capped"`
}

type stackMemberResponse struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	State          string `json:"state"`
	CIStatus       string `json:"ci_status"`
	ReviewDecision string `json:"review_decision"`
	MergeableState string `json:"mergeable_state"`
	Position       int    `json:"position"`
	IsDraft        bool   `json:"is_draft"`
	BaseBranch     string `json:"base_branch"`
	BlockedBy      *int   `json:"blocked_by"`
}

type stackResponse struct {
	ID        int64                 `json:"id"`
	Name      string                `json:"name"`
	RepoOwner string                `json:"repo_owner"`
	RepoName  string                `json:"repo_name"`
	Health    string                `json:"health"`
	Members   []stackMemberResponse `json:"members"`
}

type stackContextResponse struct {
	StackID   int64                 `json:"stack_id"`
	StackName string                `json:"stack_name"`
	Position  int                   `json:"position"`
	Size      int                   `json:"size"`
	Health    string                `json:"health"`
	Members   []stackMemberResponse `json:"members"`
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

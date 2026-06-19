package server

import (
	"time"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

type worktreeLinkResponse struct {
	WorktreeKey    string `json:"worktree_key"`
	WorktreePath   string `json:"worktree_path,omitempty"`
	WorktreeBranch string `json:"worktree_branch,omitempty"`
}

type providerCapabilitiesResponse struct {
	ReadRepositories       bool     `json:"read_repositories"`
	ReadMergeRequests      bool     `json:"read_merge_requests"`
	ReadIssues             bool     `json:"read_issues"`
	ReadComments           bool     `json:"read_comments"`
	ReadReleases           bool     `json:"read_releases"`
	ReadCI                 bool     `json:"read_ci"`
	ReadLabels             bool     `json:"read_labels"`
	CommentMutation        bool     `json:"comment_mutation"`
	StateMutation          bool     `json:"state_mutation"`
	MergeMutation          bool     `json:"merge_mutation"`
	ReviewMutation         bool     `json:"review_mutation"`
	WorkflowApproval       bool     `json:"workflow_approval"`
	ReadyForReview         bool     `json:"ready_for_review"`
	DraftMutation          bool     `json:"draft_mutation"`
	IssueMutation          bool     `json:"issue_mutation"`
	LabelMutation          bool     `json:"label_mutation"`
	AssigneeMutation       bool     `json:"assignee_mutation"`
	ReviewerMutation       bool     `json:"reviewer_mutation"`
	ThreadReply            bool     `json:"thread_reply"`
	ThreadResolve          bool     `json:"thread_resolve"`
	ReviewDraftMutation    bool     `json:"review_draft_mutation"`
	ReviewThreadResolution bool     `json:"review_thread_resolution"`
	ReadReviewThreads      bool     `json:"read_review_threads"`
	NativeMultilineRanges  bool     `json:"native_multiline_ranges"`
	MutationHeadBinding    bool     `json:"mutation_head_binding"`
	SupportedReviewActions []string `json:"supported_review_actions"`
}

type repoResponse struct {
	ID                       int64
	Platform                 string
	PlatformHost             string
	Owner                    string
	Name                     string
	LastSyncStartedAt        *time.Time
	LastSyncCompletedAt      *time.Time
	LastSyncError            string
	AllowSquashMerge         bool
	AllowMergeCommit         bool
	AllowRebaseMerge         bool
	ViewerCanMerge           bool
	BackfillPRPage           int
	BackfillPRComplete       bool
	BackfillPRCompletedAt    *time.Time
	BackfillIssuePage        int
	BackfillIssueComplete    bool
	BackfillIssueCompletedAt *time.Time
	CreatedAt                time.Time
	Capabilities             providerCapabilitiesResponse `json:"capabilities"`
	Operations               RepoOperations               `json:"operations"`
}

// mergeRequestResponse extends db.MergeRequest with resolved repo owner/name fields.
type mergeRequestResponse struct {
	db.MergeRequest
	Repo            repoRefResponse        `json:"repo"`
	RepoOwner       string                 `json:"repo_owner"`
	RepoName        string                 `json:"repo_name"`
	PlatformHost    string                 `json:"platform_host"`
	WorktreeLinks   []worktreeLinkResponse `json:"worktree_links"`
	Workspace       *workspaceRef          `json:"workspace,omitempty"`
	DetailLoaded    bool                   `json:"detail_loaded"`
	DetailFetchedAt string                 `json:"detail_fetched_at,omitempty"`
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
	Repo             repoRefResponse             `json:"repo"`
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
	DetailFetchedAt  string                      `json:"detail_fetched_at,omitempty"`
	Workspace        *workspaceRef               `json:"workspace,omitempty"`
	Stack            *stackContextResponse       `json:"stack,omitempty"`
}

var validKanbanStates = map[string]bool{
	"new":            true,
	"reviewing":      true,
	"waiting":        true,
	"awaiting_merge": true,
}

type issueResponse struct {
	db.Issue
	Repo            repoRefResponse `json:"repo"`
	PlatformHost    string          `json:"platform_host"`
	RepoOwner       string          `json:"repo_owner"`
	RepoName        string          `json:"repo_name"`
	Workspace       *workspaceRef   `json:"workspace,omitempty"`
	DetailLoaded    bool            `json:"detail_loaded"`
	DetailFetchedAt string          `json:"detail_fetched_at,omitempty"`
}

type issueDetailResponse struct {
	Issue           *db.Issue       `json:"issue"`
	Events          []db.IssueEvent `json:"events"`
	Repo            repoRefResponse `json:"repo"`
	PlatformHost    string          `json:"platform_host"`
	RepoOwner       string          `json:"repo_owner"`
	RepoName        string          `json:"repo_name"`
	DetailLoaded    bool            `json:"detail_loaded"`
	DetailFetchedAt string          `json:"detail_fetched_at,omitempty"`
	Workspace       *workspaceRef   `json:"workspace,omitempty"`
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
	Repo                 repoRefResponse                  `json:"repo"`
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
	Operations           RepoOperations                   `json:"operations"`
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

type diffResponse struct {
	Stale               bool                `json:"stale"`
	WhitespaceOnlyCount int                 `json:"whitespace_only_count"`
	Files               []gitclone.DiffFile `json:"files"`
}

type filesResponse struct {
	Stale               bool                `json:"stale"`
	WhitespaceOnlyCount int                 `json:"whitespace_only_count"`
	Files               []gitclone.DiffFile `json:"files"`
}

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

type filePreviewResponse struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Encoding  string `json:"encoding"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
}

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

type commitResponse struct {
	SHA        string    `json:"sha"         doc:"Full commit SHA"`
	Message    string    `json:"message"     doc:"First line of commit message"`
	AuthorName string    `json:"author_name" doc:"Commit author display name"`
	AuthoredAt time.Time `json:"authored_at" doc:"Commit author date (RFC3339)"`
}

type commitsResponse struct {
	Commits []commitResponse `json:"commits" doc:"Commits in newest-first order"`
}

// workspaceResponse describes one middleman-managed workspace.
//
// This payload exists so the UI can reopen a durable local workspace and render
// the correct item-specific presentation around it. It represents middleman's
// own persisted workspace model, not an arbitrary host worktree inventory.
type workspaceResponse struct {
	ID                 string          `json:"id"`
	Repo               repoRefResponse `json:"repo"`
	PlatformHost       string          `json:"platform_host"`
	RepoOwner          string          `json:"repo_owner"`
	RepoName           string          `json:"repo_name"`
	ItemType           string          `json:"item_type"`
	ItemNumber         int             `json:"item_number"`
	GitHeadRef         string          `json:"git_head_ref"`
	WorktreePath       string          `json:"worktree_path"`
	TmuxSession        string          `json:"tmux_session"`
	TmuxPaneTitle      *string         `json:"tmux_pane_title,omitempty"`
	TmuxWorking        bool            `json:"tmux_working"`
	TmuxActivitySource string          `json:"tmux_activity_source"`
	TmuxLastOutputAt   *string         `json:"tmux_last_output_at"`
	Status             string          `json:"status"`
	ErrorMessage       *string         `json:"error_message,omitempty"`
	CreatedAt          string          `json:"created_at"`
	ItemLastActivityAt *string         `json:"item_last_activity_at,omitempty"`
	MRTitle            *string         `json:"mr_title,omitempty"`
	MRState            *string         `json:"mr_state,omitempty"`
	MRIsDraft          *bool           `json:"mr_is_draft,omitempty"`
	MRCIStatus         *string         `json:"mr_ci_status,omitempty"`
	MRReviewDecision   *string         `json:"mr_review_decision,omitempty"`
	MRAdditions        *int            `json:"mr_additions,omitempty"`
	MRDeletions        *int            `json:"mr_deletions,omitempty"`
	CommitsAhead       *int            `json:"commits_ahead,omitempty"`
	CommitsBehind      *int            `json:"commits_behind,omitempty"`
	AssociatedPRNumber *int            `json:"associated_pr_number,omitempty"`
}

type workspaceRuntimeResponse struct {
	LaunchTargets []localruntime.LaunchTarget `json:"launch_targets"`
	Sessions      []localruntime.SessionInfo  `json:"sessions"`
}

type runtimeAttachSpecResponse struct {
	Version           int      `json:"version"`
	Kind              string   `json:"kind"`
	SessionKey        string   `json:"session_key"`
	TargetKey         string   `json:"target_key"`
	TmuxSession       string   `json:"tmux_session"`
	Command           []string `json:"command"`
	RequiresLocalHost bool     `json:"requires_local_host"`
}

// workspaceRef is the lightweight link from item detail APIs back to an
// existing middleman workspace.
//
// Its purpose is to let PR and issue detail screens switch from "create
// workspace" to "open workspace" without embedding the full workspace payload.
type workspaceRef struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// toWorkspaceResponse maps the DB workspace summary into the API shape used by
// the workspaces page and terminal view.
func toWorkspaceResponse(
	s *db.WorkspaceSummary,
) workspaceResponse {
	var itemLastActivityAt *string
	if s.ItemLastActivityAt != nil {
		formatted := s.ItemLastActivityAt.UTC().Format(time.RFC3339)
		itemLastActivityAt = &formatted
	}
	return workspaceResponse{
		ID:                 s.ID,
		Repo:               repoRefFromParts(s.Platform, s.PlatformHost, s.RepoOwner, s.RepoName),
		PlatformHost:       s.PlatformHost,
		RepoOwner:          s.RepoOwner,
		RepoName:           s.RepoName,
		ItemType:           s.ItemType,
		ItemNumber:         s.ItemNumber,
		GitHeadRef:         s.GitHeadRef,
		WorktreePath:       s.WorktreePath,
		TmuxSession:        s.TmuxSession,
		Status:             s.Status,
		TmuxActivitySource: tmuxActivitySourceUnknown,
		ErrorMessage:       s.ErrorMessage,
		CreatedAt:          s.CreatedAt.UTC().Format(time.RFC3339),
		ItemLastActivityAt: itemLastActivityAt,
		MRTitle:            s.MRTitle,
		MRState:            s.MRState,
		MRIsDraft:          s.MRIsDraft,
		MRCIStatus:         s.MRCIStatus,
		MRReviewDecision:   s.MRReviewDecision,
		MRAdditions:        s.MRAdditions,
		MRDeletions:        s.MRDeletions,
		AssociatedPRNumber: s.AssociatedPRNumber,
	}
}

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
	ID             string          `json:"id"`
	Cursor         string          `json:"cursor"`
	ActivityType   string          `json:"activity_type"`
	Repo           repoRefResponse `json:"repo"`
	PlatformHost   string          `json:"platform_host"`
	RepoOwner      string          `json:"repo_owner"`
	RepoName       string          `json:"repo_name"`
	ItemType       string          `json:"item_type"`
	ItemNumber     int             `json:"item_number"`
	ItemTitle      string          `json:"item_title"`
	ItemURL        string          `json:"item_url"`
	ItemState      string          `json:"item_state"`
	Workspace      *workspaceRef   `json:"workspace,omitempty"`
	Author         string          `json:"author"`
	ItemAuthor     string          `json:"item_author,omitempty"`
	CreatedAt      string          `json:"created_at"`
	BodyPreview    string          `json:"body_preview"`
	BranchName     string          `json:"branch_name,omitempty"`
	CommitSHA      string          `json:"commit_sha,omitempty"`
	BeforeSHA      string          `json:"before_sha,omitempty"`
	AfterSHA       string          `json:"after_sha,omitempty"`
	AuthorName     string          `json:"author_name,omitempty"`
	AuthorEmail    string          `json:"author_email,omitempty"`
	CommitterName  string          `json:"committer_name,omitempty"`
	CommitterEmail string          `json:"committer_email,omitempty"`
	AuthoredAt     string          `json:"authored_at,omitempty"`
	CommittedAt    string          `json:"committed_at,omitempty"`
	ActivityURL    string          `json:"activity_url,omitempty"`
	SubjectState   string          `json:"subject_state,omitempty"`
}

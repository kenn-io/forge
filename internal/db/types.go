package db

import (
	"cmp"
	"strings"
	"time"
)

type Label struct {
	ID                 int64      `json:"-"`
	RepoID             int64      `json:"-"`
	PlatformID         int64      `json:"-"`
	PlatformExternalID string     `json:"-"`
	Name               string     `json:"name"`
	Description        string     `json:"description,omitempty"`
	Color              string     `json:"color"`
	IsDefault          bool       `json:"is_default"`
	UpdatedAt          time.Time  `json:"-"`
	CatalogPresent     bool       `json:"-"`
	CatalogSeenAt      *time.Time `json:"-"`
}

// LabelCatalogFreshness records provider catalog sync state for a repository.
type LabelCatalogFreshness struct {
	SyncedAt  *time.Time
	CheckedAt *time.Time
	SyncError string
}

type Repo struct {
	ID                       int64
	Platform                 string
	PlatformHost             string
	PlatformRepoID           string `json:"-"`
	Owner                    string
	Name                     string
	RepoPath                 string `json:"-"`
	OwnerKey                 string `json:"-"`
	NameKey                  string `json:"-"`
	RepoPathKey              string `json:"-"`
	WebURL                   string `json:"-"`
	CloneURL                 string `json:"-"`
	DefaultBranch            string `json:"-"`
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
	LabelCatalogSyncedAt     *time.Time
	LabelCatalogCheckedAt    *time.Time
	LabelCatalogSyncError    string
	CreatedAt                time.Time
}

func (r Repo) FullName() string {
	return r.Owner + "/" + r.Name
}

type RepoIdentity struct {
	Platform       string
	PlatformHost   string
	PlatformRepoID string
	Owner          string
	Name           string
	RepoPath       string
	OwnerKey       string
	NameKey        string
	RepoPathKey    string
}

type RepoProviderMetadata struct {
	PlatformRepoID string
	WebURL         string
	CloneURL       string
	DefaultBranch  string
}

type RepoSummary struct {
	Repo                 Repo
	CachedPRCount        int
	OpenPRCount          int
	DraftPRCount         int
	CachedIssueCount     int
	OpenIssueCount       int
	MostRecentActivityAt *time.Time
	Overview             RepoOverview
	ActiveAuthors        []RepoActivityAuthor
	RecentIssues         []RepoIssueHeadline
}

type RepoOverview struct {
	LatestRelease       *RepoRelease
	Releases            []RepoRelease
	CommitsSinceRelease *int
	CommitTimeline      []RepoCommitTimelinePoint
	TimelineUpdatedAt   *time.Time
}

type RepoRelease struct {
	TagName         string
	Name            string
	URL             string
	TargetCommitish string
	Prerelease      bool
	PublishedAt     *time.Time
}

type RepoCommitTimelinePoint struct {
	SHA         string
	Message     string
	CommittedAt time.Time
}

type BranchCommit struct {
	RepoID         int64
	BranchName     string
	CommitSHA      string
	AuthorName     string
	AuthorEmail    string
	AuthoredAt     time.Time
	CommitterName  string
	CommitterEmail string
	CommittedAt    time.Time
	Subject        string
	ObservedOrder  int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type BranchTip struct {
	RepoID     int64
	BranchName string
	TipSHA     string
	ObservedAt time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type BranchForcePush struct {
	RepoID           int64
	BranchName       string
	BeforeSHA        string
	AfterSHA         string
	BeforeObservedAt time.Time
	DetectedAt       time.Time
	CreatedAt        time.Time
}

type RepoActivityAuthor struct {
	Login     string
	ItemCount int
}

type RepoIssueHeadline struct {
	Number         int
	Title          string
	Author         string
	State          string
	URL            string
	LastActivityAt time.Time
}

type MergeRequest struct {
	ID                 int64
	RepoID             int64
	PlatformID         int64
	PlatformExternalID string
	Number             int
	URL                string
	Title              string
	Author             string
	AuthorDisplayName  string
	State              MergeRequestState `enum:"open,closed,merged"`
	IsDraft            bool
	IsLocked           bool
	Body               string
	HeadBranch         string
	BaseBranch         string
	PlatformHeadSHA    string `json:"-"`
	PlatformBaseSHA    string `json:"-"`
	DiffHeadSHA        string `json:"-"`
	DiffBaseSHA        string `json:"-"`
	MergeBaseSHA       string `json:"-"`
	HeadRepoCloneURL   string
	Additions          int
	Deletions          int
	CommentCount       int
	ReviewDecision     string
	CIStatus           string
	CIChecksJSON       string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastActivityAt     time.Time
	MergedAt           *time.Time
	ClosedAt           *time.Time
	MergeableState     string
	DetailFetchedAt    *time.Time
	CIHadPending       bool
	// WorkflowApprovalCheckedAt is when middleman last reconciled the
	// workflow-approval state for this merge request. Nil means never
	// checked; the GET path treats persisted state as authoritative
	// only when WorkflowApprovalHeadSHA matches PlatformHeadSHA. Only
	// providers that surface a workflow-approval concept populate
	// these columns; others leave them zero.
	WorkflowApprovalCheckedAt *time.Time   `json:"-"`
	WorkflowApprovalHeadSHA   string       `json:"-"`
	WorkflowApprovalRequired  bool         `json:"-"`
	WorkflowApprovalCount     int          `json:"-"`
	KanbanStatus              KanbanStatus `enum:"new,reviewing,waiting,awaiting_merge"`
	Starred                   bool
	Labels                    []Label `json:"labels,omitempty"`
}

type MergeRequestState string

const (
	MergeRequestStateOpen   MergeRequestState = "open"
	MergeRequestStateClosed MergeRequestState = "closed"
	MergeRequestStateMerged MergeRequestState = "merged"
)

type KanbanStatus string

const (
	KanbanStatusNew           KanbanStatus = "new"
	KanbanStatusReviewing     KanbanStatus = "reviewing"
	KanbanStatusWaiting       KanbanStatus = "waiting"
	KanbanStatusAwaitingMerge KanbanStatus = "awaiting_merge"
)

func (mr MergeRequest) Compare(other MergeRequest) int {
	return cmp.Compare(mr.Number, other.Number)
}

// CICheck represents a single CI check run.
type CICheck struct {
	Name            string `json:"name"`
	Status          string `json:"status"`     // queued, in_progress, completed
	Conclusion      string `json:"conclusion"` // success, failure, neutral, cancelled, skipped, timed_out, action_required, or empty
	URL             string `json:"url"`        // link to the check run details page
	App             string `json:"app"`        // app name (e.g., "GitHub Actions")
	DurationSeconds *int64 `json:"duration_seconds,omitempty"`
}

func (c CICheck) Compare(other CICheck) int {
	leftFolded := strings.ToLower(c.Name)
	rightFolded := strings.ToLower(other.Name)
	if leftFolded != rightFolded {
		return cmp.Compare(leftFolded, rightFolded)
	}
	return cmp.Compare(c.Name, other.Name)
}

type MREvent struct {
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
	// ThreadID groups root comments and replies that belong to the same
	// provider conversation. GitLab calls this a discussion ID.
	ThreadID     *string
	PositionJSON string
	Resolvable   bool
	Resolved     bool
}

type ReviewLineRange struct {
	Path        string
	OldPath     string
	Side        string
	StartSide   string
	StartLine   *int
	Line        int
	OldLine     *int
	NewLine     *int
	LineType    string
	DiffHeadSHA string
	CommitSHA   string
}

type MRReviewDraft struct {
	ID             int64
	MergeRequestID int64
	Body           string
	Action         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Comments       []MRReviewDraftComment
}

type MRReviewDraftComment struct {
	ID        int64
	DraftID   int64
	Body      string
	Range     ReviewLineRange
	CreatedAt time.Time
	UpdatedAt time.Time
}

type MRReviewDraftCommentInput struct {
	Body  string
	Range ReviewLineRange
}

type MRReviewThread struct {
	ID                int64
	MergeRequestID    int64
	ProviderThreadID  string
	ProviderReviewID  string
	ProviderCommentID string
	Body              string
	AuthorLogin       string
	Range             ReviewLineRange
	Resolved          bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ResolvedAt        *time.Time
	MetadataJSON      string
}

type KanbanState struct {
	MergeRequestID int64
	Status         string
	UpdatedAt      time.Time
}

type ListMergeRequestsOpts struct {
	PlatformHost string
	RepoOwner    string
	RepoName     string
	RepoPath     string
	RepoFilters  []RepoFilter
	State        string
	KanbanState  string
	Starred      bool
	Search       string
	Limit        int
	Offset       int
}

type RepoFilter struct {
	PlatformHost string
	RepoOwner    string
	RepoName     string
	RepoPath     string
}

type Issue struct {
	ID                 int64
	RepoID             int64
	PlatformID         int64
	PlatformExternalID string
	Number             int
	URL                string
	Title              string
	Author             string
	State              string
	Body               string
	CommentCount       int
	LabelsJSON         string `json:"-"`
	AssigneesJSON      string `json:"-"` // JSON array of assignee usernames
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastActivityAt     time.Time
	ClosedAt           *time.Time
	DetailFetchedAt    *time.Time
	Starred            bool
	Labels             []Label  `json:"labels,omitempty"`
	Assignees          []string `json:"assignees,omitempty"` // Parsed assignees
}

type IssueEvent struct {
	ID                 int64
	IssueID            int64
	PlatformID         *int64
	PlatformExternalID string
	EventType          string
	Author             string
	Summary            string
	Body               string
	MetadataJSON       string
	CreatedAt          time.Time
	DedupeKey          string
	// ThreadID groups root comments and replies that belong to the same
	// provider conversation. GitLab calls this a discussion ID.
	ThreadID *string
}

type CommentAutocompleteReference struct {
	Kind   string `json:"kind"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
}

type ListIssuesOpts struct {
	PlatformHost string
	RepoOwner    string
	RepoName     string
	RepoPath     string
	RepoFilters  []RepoFilter
	State        string
	Starred      bool
	Search       string
	Assignee     string
	Limit        int
	Offset       int
}

type StarredItem struct {
	ItemType  string
	RepoID    int64
	Number    int
	StarredAt time.Time
}

// WorktreeLink associates a merge request with an external worktree.
type WorktreeLink struct {
	ID             int64
	MergeRequestID int64
	WorktreeKey    string
	WorktreePath   string
	WorktreeBranch string
	LinkedAt       time.Time
}

// RateLimit tracks per-host API rate limit state.
type RateLimit struct {
	ID            int64
	Platform      string
	PlatformHost  string
	APIType       string
	RequestsHour  int
	HourStart     time.Time
	RateRemaining int
	RateLimit     int
	RateResetAt   *time.Time
	UpdatedAt     time.Time
}

// ActivityItem represents one row in the unified activity feed.
type ActivityItem struct {
	ActivityType string // new_pr, new_issue, comment, review, commit, default_branch_*
	Source       string // pr, issue, pre, ise, bc, bfp
	SourceID     int64  // PK from the source table
	Platform     string
	PlatformHost string
	RepoOwner    string
	RepoName     string
	ItemType     string // pr, issue, or empty for repo-level activity
	ItemNumber   int
	ItemTitle    string
	ItemURL      string
	ItemState    string // open, merged, closed
	Author       string
	// ItemAuthor is the author of the parent PR/issue, carried on every
	// PR/issue row (open, comment, review, commit, force_push) so the
	// threaded feed can show the item's author rather than the latest
	// actor. Empty for repo-level rows (branch commits/force pushes).
	ItemAuthor     string
	CreatedAt      time.Time
	BodyPreview    string
	BranchName     string
	CommitSHA      string
	BeforeSHA      string
	AfterSHA       string
	AuthorName     string
	AuthorEmail    string
	CommitterName  string
	CommitterEmail string
	AuthoredAt     *time.Time
	CommittedAt    *time.Time
	ActivityURL    string
}

// Stack represents a detected chain of dependent PRs.
type Stack struct {
	ID         int64
	RepoID     int64
	BaseNumber int
	Name       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// StackMember links a merge request to a stack with a position.
type StackMember struct {
	StackID        int64
	MergeRequestID int64
	Position       int
}

// StackWithRepo extends Stack with resolved repo owner/name.
type StackWithRepo struct {
	Stack
	RepoOwner string
	RepoName  string
}

// StackMemberWithPR combines stack membership with PR fields needed for display.
type StackMemberWithPR struct {
	StackID        int64
	MergeRequestID int64
	Position       int
	Number         int
	Title          string
	State          string
	CIStatus       string
	ReviewDecision string
	IsDraft        bool
	BaseBranch     string
	MergeableState string
}

const (
	WorkspaceItemTypePullRequest = "pull_request"
	WorkspaceItemTypeIssue       = "issue"
)

// Workspace represents a middleman-managed git worktree linked to a
// pull request or issue.
type Workspace struct {
	ID                 string
	Platform           string
	PlatformHost       string
	RepoOwner          string
	RepoName           string
	ItemType           string
	ItemNumber         int
	AssociatedPRNumber *int
	GitHeadRef         string
	MRHeadRepo         *string // nil for same-repo PRs
	// WorkspaceBranch is the exact branch name checked out in the
	// worktree after setup. Before setup completes it may contain the
	// requested branch name or workspaceBranchUnknown.
	WorkspaceBranch string
	WorktreePath    string
	TmuxSession     string
	TerminalBackend string
	Status          string // "creating", "ready", "error"
	ErrorMessage    *string
	CreatedAt       time.Time
}

// WorkspaceSummary extends Workspace with joined MR metadata.
type WorkspaceSummary struct {
	Workspace
	MRTitle          *string
	MRState          *string
	MRIsDraft        *bool
	MRCIStatus       *string
	MRReviewDecision *string
	MRAdditions      *int
	MRDeletions      *int
}

type WorkspaceSetupEvent struct {
	ID          int64
	WorkspaceID string
	Stage       string
	Outcome     string
	Message     string
	CreatedAt   time.Time
}

type WorkspaceRuntimeSession struct {
	WorkspaceID string
	SessionKey  string
	TargetKey   string
	Label       string
	Kind        string
	Scope       string
	TmuxSession string
	CreatedAt   time.Time
}

// ListActivityOpts holds filters and pagination for the activity feed.
type ListActivityOpts struct {
	Repo        string       // "owner/name" filter
	RepoFilters []RepoFilter // one or more repository filters
	Types       []string     // activity type filter
	Search      string       // title/body search
	Limit       int          // page size (default 50, max 200)
	Since       *time.Time   // only return events created at or after this time
	// Cursor fields -- decoded from opaque token by the handler.
	BeforeTime     *time.Time
	BeforeSource   string
	BeforeSourceID int64
	AfterTime      *time.Time
	AfterSource    string
	AfterSourceID  int64
}

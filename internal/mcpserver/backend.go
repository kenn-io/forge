package mcpserver

import (
	"context"
	"time"
)

// Backend is the in-process Forge service boundary used by MCP tools.
type Backend interface {
	ListRepositories(context.Context) ([]RepositorySummary, error)
	ListActivity(context.Context, ActivityQuery) (ActivityPage, error)
	ListPulls(context.Context, ItemListQuery) ([]Pull, error)
	ListIssues(context.Context, ItemListQuery) ([]Issue, error)
	GetPull(context.Context, ItemIdentity) (PullDetail, error)
	GetIssue(context.Context, ItemIdentity) (IssueDetail, error)
	GetPullDiff(context.Context, ItemIdentity, bool) (Diff, error)
	GetPullStack(context.Context, ItemIdentity) (Stack, error)
	ListWorkflowStates(context.Context, WorkflowQuery) (WorkflowPage, error)
	SetWorkflowState(context.Context, ItemIdentity, WorkflowUpdate) (WorkflowMutation, error)
	ListLaunchTargets(context.Context) ([]LaunchTarget, error)
	ListWorkspaceAgentSessions(context.Context, string) ([]WorkspaceAgentSession, error)
	GetWorkspace(context.Context, string) (Workspace, error)
	CreatePullWorkspace(context.Context, ItemIdentity, bool) (Workspace, error)
	CreateIssueWorkspace(context.Context, ItemIdentity, bool) (Workspace, error)
	CreateAdHocWorkspace(context.Context, RepositoryIdentity, string) (Workspace, error)
	LaunchWorkspaceRuntime(context.Context, string, string) (RuntimeSession, error)
	GetWorkspaceRuntime(context.Context, string) (WorkspaceRuntime, error)
	SubmitInitialMessage(context.Context, InitialMessageRequest) (InitialMessageStatus, error)
	GetInitialMessage(context.Context, string, string) (InitialMessageStatus, error)
}

type RepositoryIdentity struct {
	Provider       string
	PlatformHost   string
	PlatformRepoID string
	RepoPath       string
	Owner          string
	Name           string
}

type RepositorySummary struct {
	Repository          RepositoryIdentity
	OpenPRCount         int
	OpenIssueCount      int
	LastSyncCompletedAt string
	LastSyncError       string
}

type ActivityQuery struct {
	Since         string
	Repository    RepositoryIdentity
	ActivityTypes []string
	ItemTypes     []string
	Search        string
	After         string
}

type ActivityPage struct {
	Items  []ActivityItem
	Capped bool
}

type ActivityItem struct {
	ID             string
	Cursor         string
	ActivityType   string
	Repository     RepositoryIdentity
	ItemType       string
	ItemNumber     int
	ItemTitle      string
	ItemURL        string
	ItemState      string
	Workspace      *WorkspaceRef
	Author         string
	ItemAuthor     string
	CreatedAt      string
	BodyPreview    string
	BranchName     string
	CommitSHA      string
	BeforeSHA      string
	AfterSHA       string
	AuthorName     string
	AuthorEmail    string
	CommitterName  string
	CommitterEmail string
	AuthoredAt     string
	CommittedAt    string
	ActivityURL    string
	SubjectState   string
}

type ItemListQuery struct {
	Repository RepositoryIdentity
	State      string
	Text       string
	Limit      int
	Offset     int
}

type WorkspaceRef struct {
	ID     string
	Status string
}

type Pull struct {
	Number          int
	Title           string
	State           string
	Author          string
	URL             string
	IsDraft         bool
	Body            string
	WorkflowStatus  string
	LastActivityAt  time.Time
	Repository      RepositoryIdentity
	Workspace       *WorkspaceRef
	DetailLoaded    bool
	DetailFetchedAt string
}

type Issue struct {
	Number          int
	Title           string
	State           string
	Author          string
	URL             string
	Body            string
	WorkflowStatus  string
	LastActivityAt  time.Time
	Repository      RepositoryIdentity
	Workspace       *WorkspaceRef
	DetailLoaded    bool
	DetailFetchedAt string
}

type ItemIdentity struct {
	Type           string
	Provider       string
	PlatformHost   string
	PlatformRepoID string
	Owner          string
	Name           string
	Number         int
}

type DetailEvent struct {
	EventType string
	Author    string
	Summary   string
	Body      string
	CreatedAt time.Time
}

type Check struct {
	Name            string
	Status          string
	Conclusion      string
	URL             string
	App             string
	DurationSeconds *int64
}

type PullDetail struct {
	Pull            *Pull
	Events          []DetailEvent
	DetailLoaded    bool
	DetailFetchedAt string
	Workspace       *WorkspaceRef
	Stack           *Stack
	Checks          []Check
}

type IssueDetail struct {
	Issue           *Issue
	Events          []DetailEvent
	DetailLoaded    bool
	DetailFetchedAt string
	Workspace       *WorkspaceRef
	Workflow        *WorkflowState
}

type Diff struct {
	Stale bool
	Files []DiffFile
}

type DiffFile struct {
	Path        string
	OldPath     string
	Status      string
	IsBinary    bool
	IsGenerated bool
	Additions   int
	Deletions   int
	Patch       string
}

type Stack struct {
	Position int
	Size     int
	Health   string
	Members  []StackMember
}

type StackMember struct {
	Number   int
	Title    string
	State    string
	Position int
	IsDraft  bool
}

type WorkflowQuery struct {
	Repository    RepositoryIdentity
	ItemTypes     []string
	States        []string
	IncludeClosed bool
	Limit         int
	Cursor        string
}

type WorkflowPage struct {
	Items      []WorkflowItem
	NextCursor string
}

type WorkflowItem struct {
	Identity       ItemIdentity
	Repository     RepositoryIdentity
	Title          string
	State          string
	URL            string
	Author         string
	IsDraft        bool
	LastActivityAt string
	Workflow       WorkflowState
}

type WorkflowState struct {
	Status        string
	UpdatedAt     string
	UpdatedSource string
	UpdatedActor  string
	UpdatedReason string
}

type WorkflowUpdate struct {
	Status         string
	ExpectedStatus string
	Force          bool
	Source         string
	Actor          string
	Reason         string
}

type WorkflowMutation struct {
	PreviousStatus string
	State          WorkflowState
}

type LaunchTarget struct {
	Key            string
	Label          string
	Kind           string
	Source         string
	Available      bool
	DisabledReason string
}

type InitialMessageStatus struct {
	State        string
	MessageBytes int
	DeliveredAt  *time.Time
}

type WorkspaceAgentSession struct {
	Agent             string
	SessionID         string
	RuntimeSessionKey string
	TargetKey         string
	State             string
	UpdatedAt         time.Time
	InitialMessage    *InitialMessageStatus
}

type Workspace struct {
	ID           string
	Status       string
	Created      bool
	GitHeadRef   string
	ErrorMessage *string
}

type RuntimeSession struct {
	Key       string
	TargetKey string
	Status    string
	CreatedAt time.Time
}

type WorkspaceRuntime struct {
	Sessions []RuntimeSession
}

type InitialMessageRequest struct {
	WorkspaceID       string
	RuntimeSessionKey string
	Agent             string
	SessionID         string
	Message           string
}

const (
	ErrorCodeWorkspaceAlreadyExists          = "workspaceAlreadyExists"
	ErrorCodeInitialMessageInputModeNotReady = "initialMessageInputModeNotReady"
)

// Error is the transport-neutral MCP service error.
type Error struct {
	Kind      string
	Code      string
	Message   string
	Retryable bool
	Ambiguous bool
	Details   map[string]any
}

func (e *Error) Error() string { return e.Message }

package platform

import "time"

type Kind string

const (
	KindGitHub  Kind = "github"
	KindGitLab  Kind = "gitlab"
	KindForgejo Kind = "forgejo"
	KindGitea   Kind = "gitea"
)

const (
	DefaultGitHubHost  = "github.com"
	DefaultGitLabHost  = "gitlab.com"
	DefaultForgejoHost = "codeberg.org"
	DefaultGiteaHost   = "gitea.com"
)

type RepoRef struct {
	Platform           Kind
	Host               string
	Owner              string
	Name               string
	RepoPath           string
	PlatformID         int64
	PlatformExternalID string
	WebURL             string
	CloneURL           string
	DefaultBranch      string
}

func (r RepoRef) DisplayName() string {
	if r.RepoPath != "" {
		return r.RepoPath
	}
	if r.Owner == "" {
		return r.Name
	}
	if r.Name == "" {
		return r.Owner
	}
	return r.Owner + "/" + r.Name
}

type Repository struct {
	Ref                RepoRef
	PlatformID         int64
	PlatformExternalID string
	Description        string
	Private            bool
	Archived           bool
	MergeSettings      *RepositoryMergeSettings
	ViewerCanMerge     *bool
	DefaultBranch      string
	WebURL             string
	CloneURL           string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type RepositoryMergeSettings struct {
	AllowSquashMerge bool
	AllowMergeCommit bool
	AllowRebaseMerge bool
}

type MergeRequest struct {
	Repo               RepoRef
	PlatformID         int64
	PlatformExternalID string
	Number             int
	URL                string
	Title              string
	Author             string
	AuthorDisplayName  string
	State              string
	IsDraft            bool
	IsLocked           bool
	Body               string
	HeadBranch         string
	BaseBranch         string
	HeadSHA            string
	BaseSHA            string
	HeadRepoCloneURL   string
	Additions          int
	Deletions          int
	CommentCount       int
	ReviewDecision     string
	CIStatus           string
	MergeableState     string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastActivityAt     time.Time
	MergedAt           *time.Time
	ClosedAt           *time.Time
	Labels             []Label
}

type Issue struct {
	Repo               RepoRef
	PlatformID         int64
	PlatformExternalID string
	Number             int
	URL                string
	Title              string
	Author             string
	State              string
	Body               string
	CommentCount       int
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastActivityAt     time.Time
	ClosedAt           *time.Time
	Labels             []Label
}

type Label struct {
	Repo               RepoRef
	PlatformID         int64
	PlatformExternalID string
	Name               string
	Description        string
	Color              string
	IsDefault          bool
}

type MergeRequestEvent struct {
	Repo               RepoRef
	PlatformID         int64
	PlatformExternalID string
	MergeRequestNumber int
	EventType          string
	Author             string
	Summary            string
	Body               string
	MetadataJSON       string
	CreatedAt          time.Time
	DedupeKey          string
	// ThreadID groups root comments and replies that belong to the same
	// provider conversation. GitLab calls this a discussion ID.
	ThreadID     string
	PositionJSON string
	Resolvable   bool
	Resolved     bool
}

type IssueEvent struct {
	Repo               RepoRef
	PlatformID         int64
	PlatformExternalID string
	IssueNumber        int
	EventType          string
	Author             string
	Summary            string
	Body               string
	MetadataJSON       string
	CreatedAt          time.Time
	DedupeKey          string
	// ThreadID groups root comments and replies that belong to the same
	// provider conversation. GitLab calls this a discussion ID.
	ThreadID string
}

type Release struct {
	Repo               RepoRef
	PlatformID         int64
	PlatformExternalID string
	TagName            string
	Name               string
	URL                string
	TargetCommitish    string
	Prerelease         bool
	PublishedAt        *time.Time
	CreatedAt          time.Time
}

type Tag struct {
	Repo               RepoRef
	PlatformID         int64
	PlatformExternalID string
	Name               string
	SHA                string
	URL                string
}

type CICheck struct {
	Repo               RepoRef
	PlatformID         int64
	PlatformExternalID string
	Name               string
	Status             string
	Conclusion         string
	URL                string
	App                string
	StartedAt          *time.Time
	CompletedAt        *time.Time
}

type MergeResult struct {
	Merged  bool
	SHA     string
	Message string
}

type Capabilities struct {
	ReadRepositories  bool
	ReadMergeRequests bool
	ReadIssues        bool
	ReadComments      bool
	ReadReleases      bool
	ReadCI            bool
	ReadLabels        bool
	CommentMutation   bool
	StateMutation     bool
	MergeMutation     bool
	ReviewMutation    bool
	WorkflowApproval  bool
	ReadyForReview    bool
	IssueMutation     bool
	LabelMutation     bool
	ThreadReply       bool
	ThreadResolve     bool
}

type RepositoryListOptions struct {
	Limit  int
	Offset int
}

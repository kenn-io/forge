package mcpserver

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.kenn.io/forge/internal/platform"
)

type repoFilterInput struct {
	Provider     string `json:"provider,omitempty" jsonschema:"provider kind, such as github or gitlab"`
	PlatformHost string `json:"platform_host,omitempty" jsonschema:"provider host; defaults to the provider public host"`
	RepoPath     string `json:"repo_path,omitempty" jsonschema:"full repository path from kenn_forge_list_repos; preferred for nested namespaces"`
	Owner        string `json:"owner,omitempty" jsonschema:"repository owner or namespace"`
	Name         string `json:"name,omitempty" jsonschema:"repository name"`
}

func (r repoFilterInput) queryValue() (string, error) {
	provider := strings.TrimSpace(r.Provider)
	host := strings.TrimSpace(r.PlatformHost)
	repoPath := strings.Trim(strings.TrimSpace(r.RepoPath), "/")
	owner := strings.Trim(strings.TrimSpace(r.Owner), "/")
	name := strings.Trim(strings.TrimSpace(r.Name), "/")
	if provider == "" && host == "" && repoPath == "" && owner == "" && name == "" {
		return "", nil
	}
	if provider == "" {
		return "", fmt.Errorf("repo provider is required")
	}
	kind, err := platform.NormalizeKind(provider)
	if err != nil {
		return "", err
	}
	meta, ok := platform.MetadataFor(kind)
	if !ok {
		return "", fmt.Errorf("unsupported provider %q", provider)
	}
	if host == "" {
		host = meta.DefaultHost
	}
	if repoPath != "" {
		return fmt.Sprintf("%s|%s/%s", kind, host, repoPath), nil
	}
	if owner == "" {
		return "", fmt.Errorf("repo owner is required")
	}
	if name == "" {
		return "", fmt.Errorf("repo name is required")
	}
	return fmt.Sprintf("%s|%s/%s/%s", kind, host, owner, name), nil
}

type itemRef struct {
	Type         string `json:"type"`
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	RepoPath     string `json:"repo_path"`
	Number       int    `json:"number"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	State        string `json:"state"`
	Author       string `json:"author"`
	IsDraft      bool   `json:"is_draft"`
}

type daemonRepoRef struct {
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	RepoPath     string `json:"repo_path"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
}

type daemonWorkspaceRef struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type daemonPull struct {
	Number          int                 `json:"Number"`
	Title           string              `json:"Title"`
	State           string              `json:"State"`
	Author          string              `json:"Author"`
	URL             string              `json:"URL"`
	IsDraft         bool                `json:"IsDraft"`
	KanbanStatus    string              `json:"KanbanStatus"`
	LastActivityAt  time.Time           `json:"LastActivityAt"`
	Repo            daemonRepoRef       `json:"repo"`
	PlatformHost    string              `json:"platform_host"`
	RepoOwner       string              `json:"repo_owner"`
	RepoName        string              `json:"repo_name"`
	Workspace       *daemonWorkspaceRef `json:"workspace"`
	DetailLoaded    bool                `json:"detail_loaded"`
	DetailFetchedAt string              `json:"detail_fetched_at"`
}

type daemonIssue struct {
	Number          int                 `json:"Number"`
	Title           string              `json:"Title"`
	State           string              `json:"State"`
	Author          string              `json:"Author"`
	URL             string              `json:"URL"`
	WorkflowStatus  string              `json:"WorkflowStatus"`
	LastActivityAt  time.Time           `json:"LastActivityAt"`
	Repo            daemonRepoRef       `json:"repo"`
	PlatformHost    string              `json:"platform_host"`
	RepoOwner       string              `json:"repo_owner"`
	RepoName        string              `json:"repo_name"`
	Workspace       *daemonWorkspaceRef `json:"workspace"`
	DetailLoaded    bool                `json:"detail_loaded"`
	DetailFetchedAt string              `json:"detail_fetched_at"`
}

type daemonRepoSummary struct {
	Repo                daemonRepoRef `json:"repo"`
	PlatformHost        string        `json:"platform_host"`
	Owner               string        `json:"owner"`
	Name                string        `json:"name"`
	OpenPRCount         int           `json:"open_pr_count"`
	OpenIssueCount      int           `json:"open_issue_count"`
	LastSyncCompletedAt string        `json:"last_sync_completed_at"`
	LastSyncError       string        `json:"last_sync_error"`
}

type daemonActivityResponse struct {
	Items  []daemonActivityItem `json:"items"`
	Capped bool                 `json:"capped"`
}

type daemonActivityItem struct {
	ID             string              `json:"id"`
	Cursor         string              `json:"cursor"`
	ActivityType   string              `json:"activity_type"`
	Repo           daemonRepoRef       `json:"repo"`
	PlatformHost   string              `json:"platform_host"`
	RepoOwner      string              `json:"repo_owner"`
	RepoName       string              `json:"repo_name"`
	ItemType       string              `json:"item_type"`
	ItemNumber     int                 `json:"item_number"`
	ItemTitle      string              `json:"item_title"`
	ItemURL        string              `json:"item_url"`
	ItemState      string              `json:"item_state"`
	Workspace      *daemonWorkspaceRef `json:"workspace"`
	Author         string              `json:"author"`
	ItemAuthor     string              `json:"item_author"`
	CreatedAt      string              `json:"created_at"`
	BodyPreview    string              `json:"body_preview"`
	BranchName     string              `json:"branch_name"`
	CommitSHA      string              `json:"commit_sha"`
	BeforeSHA      string              `json:"before_sha"`
	AfterSHA       string              `json:"after_sha"`
	AuthorName     string              `json:"author_name"`
	AuthorEmail    string              `json:"author_email"`
	CommitterName  string              `json:"committer_name"`
	CommitterEmail string              `json:"committer_email"`
	AuthoredAt     string              `json:"authored_at"`
	CommittedAt    string              `json:"committed_at"`
	ActivityURL    string              `json:"activity_url"`
	SubjectState   string              `json:"subject_state"`
}

func repoPathOrFallback(repo daemonRepoRef, owner, name string) string {
	if repo.RepoPath != "" {
		return repo.RepoPath
	}
	if owner == "" {
		owner = repo.Owner
	}
	if name == "" {
		name = repo.Name
	}
	if owner == "" {
		return name
	}
	if name == "" {
		return owner
	}
	return owner + "/" + name
}

func workflowStatusOrNew(status string) string {
	if status == "" {
		return "new"
	}
	return status
}

func formatMCPTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func seg(value string) string {
	return url.PathEscape(value)
}

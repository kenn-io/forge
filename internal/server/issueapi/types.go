package issueapi

import (
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

type WorkflowStateMetaResponse struct {
	Status        db.KanbanStatus `json:"status" enum:"new,reviewing,waiting,awaiting_merge"`
	UpdatedAt     string          `json:"updated_at,omitempty" format:"date-time"`
	UpdatedSource string          `json:"updated_source,omitempty"`
	UpdatedActor  string          `json:"updated_actor,omitempty"`
	UpdatedReason string          `json:"updated_reason,omitempty"`
}

type IssueResponse struct {
	db.Issue
	Repo                    httpapi.RepoRefResponse    `json:"repo"`
	PlatformHost            string                     `json:"platform_host"`
	RepoOwner               string                     `json:"repo_owner"`
	RepoName                string                     `json:"repo_name"`
	Workspace               *workspaceapi.WorkspaceRef `json:"workspace,omitempty"`
	LastWorkspaceActivityAt string                     `json:"last_workspace_activity_at,omitempty" format:"date-time"`
	DetailLoaded            bool                       `json:"detail_loaded"`
	DetailFetchedAt         string                     `json:"detail_fetched_at,omitempty"`
}

type IssueDetailResponse struct {
	Issue           *db.Issue                  `json:"issue"`
	Events          []db.IssueEvent            `json:"events"`
	Repo            httpapi.RepoRefResponse    `json:"repo"`
	PlatformHost    string                     `json:"platform_host"`
	RepoOwner       string                     `json:"repo_owner"`
	RepoName        string                     `json:"repo_name"`
	DetailLoaded    bool                       `json:"detail_loaded"`
	DetailFetchedAt string                     `json:"detail_fetched_at,omitempty"`
	Workspace       *workspaceapi.WorkspaceRef `json:"workspace,omitempty"`
	Workflow        *WorkflowStateMetaResponse `json:"workflow,omitempty"`
}

type listIssuesInput struct {
	Repo       string `query:"repo" doc:"Repository filter. Accepts provider|platform_host/repo_path, with comma-separated values for multiple repositories."`
	State      string `query:"state"`
	Starred    bool   `query:"starred"`
	InvolvesMe bool   `query:"involves_me" doc:"Only include issues involving the authenticated viewer."`
	Q          string `query:"q"`
	Assignee   string `query:"assignee"`
	Limit      int    `query:"limit"`
	Offset     int    `query:"offset"`
}

type listIssuesOutput = httpapi.BodyOutput[[]IssueResponse]

type issueRepoNumberInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
}

type issueRepoNumberHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
}

type getIssueOutput = httpapi.BodyOutput[IssueDetailResponse]

type createIssueInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
}

type createIssueHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
}

type createIssueOutput = httpapi.CreatedOutput[IssueResponse]

type editIssueContentInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Title *string `json:"title,omitempty"`
		Body  *string `json:"body,omitempty"`
	}
}

type editIssueContentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Title *string `json:"title,omitempty"`
		Body  *string `json:"body,omitempty"`
	}
}

type editIssueContentOutput = httpapi.BodyOutput[IssueDetailResponse]

type postIssueCommentInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Body string `json:"body"`
	}
}

type postIssueCommentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Body string `json:"body"`
	}
}

type editIssueCommentInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	CommentID    int64  `path:"comment_id"`
	Body         struct {
		Body string `json:"body"`
	}
}

type editIssueCommentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	CommentID    int64  `path:"comment_id"`
	Body         struct {
		Body string `json:"body"`
	}
}

type postIssueCommentOutput = httpapi.CreatedOutput[db.IssueEvent]
type editIssueCommentOutput = httpapi.BodyOutput[db.IssueEvent]

type deleteIssueCommentInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	CommentID    int64  `path:"comment_id"`
}

type deleteIssueCommentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	CommentID    int64  `path:"comment_id"`
}

type deleteIssueCommentOutput struct {
	Status int `status:"204"`
}

type setIssueLabelsInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.SetLabelsRequest
}

type setIssueLabelsHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.SetLabelsRequest
}

type setLabelsOutput = httpapi.BodyOutput[httpapi.ItemLabelsResponse]

type setIssueAssigneesInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.SetAssigneesRequest
}

type setIssueAssigneesHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.SetAssigneesRequest
}

type setAssigneesOutput = httpapi.BodyOutput[httpapi.ItemAssigneesResponse]

type githubStateInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		State string `json:"state"`
	}
}

type githubStateHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		State string `json:"state"`
	}
}

type githubStateOutput = httpapi.BodyOutput[httpapi.GithubStateOutputBody]

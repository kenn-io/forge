package workspaceapi

import (
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

type diffResponse = httpapi.DiffResponse
type filesResponse = httpapi.FilesResponse

type workspaceDiffWatchResponse struct {
	Changed bool   `json:"changed" doc:"True when the caller must reload the watched default-HEAD snapshot."`
	Version string `json:"version" doc:"Opaque version of the current default-HEAD snapshot; never a version from another diff scope."`
}

// WorkspaceDiffWatchResponse is shared with Fleet wire tests and proxy code.
type WorkspaceDiffWatchResponse = workspaceDiffWatchResponse

type filePreviewResponse = httpapi.FilePreviewResponse
type commitResponse = httpapi.CommitResponse
type commitsResponse = httpapi.CommitsResponse

// WorkspaceRef is the lightweight link from item detail APIs back to an
// existing middleman workspace.
type WorkspaceRef struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type workspaceResponse struct {
	ID                    string                    `json:"id"`
	Repo                  httpapi.RepoRefResponse   `json:"repo"`
	PlatformHost          string                    `json:"platform_host"`
	RepoOwner             string                    `json:"repo_owner"`
	RepoName              string                    `json:"repo_name"`
	ItemType              string                    `json:"item_type"`
	ItemNumber            int                       `json:"item_number"`
	ItemKey               string                    `json:"item_key"`
	GitHeadRef            string                    `json:"git_head_ref"`
	WorktreePath          string                    `json:"worktree_path"`
	TmuxSession           string                    `json:"tmux_session"`
	TmuxPaneTitle         *string                   `json:"tmux_pane_title,omitempty"`
	TmuxWorking           bool                      `json:"tmux_working"`
	TmuxActivitySource    string                    `json:"tmux_activity_source"`
	TmuxLastOutputAt      *string                   `json:"tmux_last_output_at"`
	Status                string                    `json:"status"`
	ErrorMessage          *string                   `json:"error_message,omitempty"`
	CreatedAt             string                    `json:"created_at"`
	ItemLastActivityAt    *string                   `json:"item_last_activity_at,omitempty"`
	MRTitle               *string                   `json:"mr_title,omitempty"`
	MRState               *string                   `json:"mr_state,omitempty"`
	MRIsDraft             *bool                     `json:"mr_is_draft,omitempty"`
	MRCIStatus            *string                   `json:"mr_ci_status,omitempty"`
	MRReviewDecision      *string                   `json:"mr_review_decision,omitempty"`
	MRAdditions           *int                      `json:"mr_additions,omitempty"`
	MRDeletions           *int                      `json:"mr_deletions,omitempty"`
	CommitsAhead          *int                      `json:"commits_ahead,omitempty"`
	CommitsBehind         *int                      `json:"commits_behind,omitempty"`
	EnrichmentStatus      string                    `json:"enrichment_status" enum:"not_applicable,pending,fresh,stale,failed" doc:"Aggregate git-divergence and tmux-activity reconciliation status. Failed responses retain last-known-good component fields when available."`
	EnrichmentRefreshedAt *string                   `json:"enrichment_refreshed_at,omitempty" doc:"Oldest successful refresh time across the populated enrichment components."`
	EnrichmentError       *string                   `json:"enrichment_error,omitempty" doc:"Combined error from the most recent reconciliation attempt; populated component fields may still contain last-known-good values."`
	AssociatedPRNumber    *int                      `json:"associated_pr_number,omitempty"`
	Kata                  *db.WorkspaceKataMetadata `json:"kata,omitempty"`
}

// WorkspaceResponse is the stable workspace DTO shared with dependent server
// domains. Routes continue to use the original unexported name so OpenAPI
// schema identities remain unchanged.
type WorkspaceResponse = workspaceResponse

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

// RuntimeAttachSpecResponse is shared with Fleet's runtime proxy.
type RuntimeAttachSpecResponse = runtimeAttachSpecResponse

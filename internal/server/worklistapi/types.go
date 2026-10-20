package worklistapi

import (
	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
)

type ListWorklistsResponse struct {
	Worklists []WorklistSummaryResponse `json:"worklists"`
}

type WorklistSummaryResponse struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	Recipe       string  `json:"recipe" doc:"The agent's criteria and searches in plain text. Forge stores it but never evaluates it."`
	CreatedBy    string  `json:"created_by" doc:"Label of the agent that last wrote the list; may be empty."`
	WorkspaceID  string  `json:"workspace_id" doc:"ACP workspace that last wrote the list; empty when none or for another node's read."`
	ItemCount    int     `json:"item_count"`
	ChangedCount int     `json:"changed_count"`
	GoneCount    int     `json:"gone_count"`
	CreatedAt    string  `json:"created_at" format:"date-time"`
	UpdatedAt    string  `json:"updated_at" format:"date-time"`
	DismissedAt  *string `json:"dismissed_at" format:"date-time"`
}

type WorklistResponse struct {
	ID          int64                  `json:"id"`
	Title       string                 `json:"title"`
	Recipe      string                 `json:"recipe"`
	CreatedBy   string                 `json:"created_by"`
	WorkspaceID string                 `json:"workspace_id" doc:"ACP workspace that last wrote the list; empty when none or for another node's read."`
	CreatedAt   string                 `json:"created_at" format:"date-time"`
	UpdatedAt   string                 `json:"updated_at" format:"date-time"`
	DismissedAt *string                `json:"dismissed_at" format:"date-time"`
	Items       []WorklistItemResponse `json:"items"`
}

// WorklistItemResponse is one ranked item. Pull or Issue carries the same row
// model the pull request or issue list returns while the item is a visible
// synced row; Live, Pull, and Issue are null otherwise.
type WorklistItemResponse struct {
	Position  int                           `json:"position"`
	Repo      httpapi.RepoRefResponse       `json:"repo"`
	ItemType  string                        `json:"item_type" enum:"pr,issue"`
	Number    int                           `json:"number"`
	Title     string                        `json:"title"`
	URL       string                        `json:"url"`
	Score     *int                          `json:"score" minimum:"1" maximum:"10"`
	Section   string                        `json:"section"`
	Note      string                        `json:"note"`
	Caveat    string                        `json:"caveat"`
	WrittenAt string                        `json:"written_at" format:"date-time"`
	Snapshot  WorklistItemStateResponse     `json:"snapshot" doc:"Synced state Forge recorded when the agent wrote the item."`
	Live      *WorklistItemStateResponse    `json:"live" doc:"Current synced state; null once the item is no longer visible."`
	Freshness string                        `json:"freshness" enum:"fresh,changed,gone"`
	Changes   WorklistChangesResponse       `json:"changes"`
	Pull      *pullapi.MergeRequestResponse `json:"pull" doc:"Pull request list row; null unless the item is a visible pull request."`
	Issue     *issueapi.IssueResponse       `json:"issue" doc:"Issue list row; null unless the item is a visible issue."`
}

// TransformSchema marks the object references that are null for items that
// are no longer visible. Huma rejects a nullable tag on a referenced object,
// so the null branch is declared here.
func (*WorklistItemResponse) TransformSchema(_ huma.Registry, schema *huma.Schema) *huma.Schema {
	for _, name := range []string{"live", "pull", "issue"} {
		property := schema.Properties[name]
		if property == nil || property.Ref == "" {
			continue
		}
		schema.Properties[name] = &huma.Schema{
			Description: property.Description,
			AnyOf:       []*huma.Schema{{Ref: property.Ref}, {Type: "null"}},
		}
	}
	return schema
}

type WorklistItemStateResponse struct {
	State          string  `json:"state"`
	HeadSHA        string  `json:"head_sha"`
	CommentCount   int     `json:"comment_count"`
	CIStatus       string  `json:"ci_status"`
	ReviewDecision string  `json:"review_decision"`
	LastActivityAt *string `json:"last_activity_at" format:"date-time"`
}

// WorklistChangesResponse names what changed since the item was written.
// Paired fields are present only when the values differ. Changes are
// informational and never certify mergeability.
type WorklistChangesResponse struct {
	StateFrom   string `json:"state_from,omitempty"`
	StateTo     string `json:"state_to,omitempty"`
	NewCommits  bool   `json:"new_commits"`
	NewComments int    `json:"new_comments"`
	CIFrom      string `json:"ci_from,omitempty"`
	CITo        string `json:"ci_to,omitempty"`
	ReviewFrom  string `json:"review_from,omitempty"`
	ReviewTo    string `json:"review_to,omitempty"`
}

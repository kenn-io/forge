package spokeapi

import (
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/platform"
)

type FederationWorkflowRepositoryIdentity mcpserver.RepositoryIdentity

type FederationWorkflowItemIdentity mcpserver.ItemIdentity

// The federation workflow identities are defined types, which do not inherit
// the MCP types' JSON methods; each flattens its repository key the same way.

func (r FederationWorkflowRepositoryIdentity) MarshalJSON() ([]byte, error) {
	type plain FederationWorkflowRepositoryIdentity
	return platform.MarshalKeyedJSON(plain(r))
}

func (r *FederationWorkflowRepositoryIdentity) UnmarshalJSON(data []byte) error {
	type plain FederationWorkflowRepositoryIdentity
	return platform.UnmarshalKeyedJSON(data, (*plain)(r))
}

func (i FederationWorkflowItemIdentity) MarshalJSON() ([]byte, error) {
	type plain FederationWorkflowItemIdentity
	return platform.MarshalKeyedJSON(plain(i))
}

func (i *FederationWorkflowItemIdentity) UnmarshalJSON(data []byte) error {
	type plain FederationWorkflowItemIdentity
	return platform.UnmarshalKeyedJSON(data, (*plain)(i))
}

type FederationWorkflowState mcpserver.WorkflowState

type FederationWorkflowPage struct {
	Items      []FederationWorkflowItem `json:"items" nullable:"false"`
	NextCursor string                   `json:"next_cursor"`
}

type FederationWorkflowItem struct {
	Identity       FederationWorkflowItemIdentity       `json:"identity"`
	Repository     FederationWorkflowRepositoryIdentity `json:"repository"`
	Title          string                               `json:"title"`
	State          string                               `json:"state"`
	URL            string                               `json:"url"`
	Author         string                               `json:"author"`
	IsDraft        bool                                 `json:"is_draft"`
	LastActivityAt string                               `json:"last_activity_at"`
	Workflow       FederationWorkflowState              `json:"workflow"`
}

type FederationWorkflowMutation struct {
	PreviousStatus string                  `json:"previous_status"`
	State          FederationWorkflowState `json:"state"`
}

func (page FederationWorkflowPage) mcp() mcpserver.WorkflowPage {
	items := make([]mcpserver.WorkflowItem, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, mcpserver.WorkflowItem{
			Identity:       mcpserver.ItemIdentity(item.Identity),
			Repository:     mcpserver.RepositoryIdentity(item.Repository),
			Title:          item.Title,
			State:          item.State,
			URL:            item.URL,
			Author:         item.Author,
			IsDraft:        item.IsDraft,
			LastActivityAt: item.LastActivityAt,
			Workflow:       mcpserver.WorkflowState(item.Workflow),
		})
	}
	return mcpserver.WorkflowPage{Items: items, NextCursor: page.NextCursor}
}

func (mutation FederationWorkflowMutation) mcp() mcpserver.WorkflowMutation {
	return mcpserver.WorkflowMutation{
		PreviousStatus: mutation.PreviousStatus,
		State:          mcpserver.WorkflowState(mutation.State),
	}
}

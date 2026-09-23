package spokeapi

import (
	"go.kenn.io/forge/internal/mcpserver"
)

type FederationWorkflowRepositoryIdentity mcpserver.RepositoryIdentity

type FederationWorkflowItemIdentity mcpserver.ItemIdentity

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

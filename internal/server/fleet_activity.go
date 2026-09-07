package server

import (
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

func overlayFleetActivityWorkspaces(response *activityResponse, workspaces []fleet.WorkspaceSummary) {
	overlays := make(map[providerplane.ItemIdentity]workspaceapi.WorkspaceRef)
	for _, workspace := range workspaces {
		if !workspace.Visible || workspace.FleetHostKey == "" {
			continue
		}
		identity := providerplane.ItemIdentity{
			Repository: providerplane.RepositoryIdentity{
				Provider: workspace.Repo.Provider, PlatformHost: workspace.Repo.PlatformHost,
				PlatformRepoID: workspace.Repo.PlatformRepoID,
			},
			ItemType: workspace.ItemType, ItemNumber: workspace.ItemNumber,
		}.Canonical()
		switch workspace.ItemType {
		case db.WorkspaceItemTypePullRequest:
			identity.ItemType = "pr"
		case db.WorkspaceItemTypeIssue:
			identity.ItemType = "issue"
		default:
			identity.ItemType = ""
		}
		ref := workspaceapi.WorkspaceRef{ID: workspace.ID, Status: workspace.Status}
		if workspace.SourceItemVisible && identity.Valid() {
			if _, exists := overlays[identity]; !exists {
				overlays[identity] = ref
			}
		}
		if workspace.AssociatedPRNumber != nil {
			identity.ItemType = "pr"
			identity.ItemNumber = *workspace.AssociatedPRNumber
			if identity.Valid() {
				if _, exists := overlays[identity]; !exists {
					overlays[identity] = ref
				}
			}
		}
	}
	for i := range response.Items {
		item := &response.Items[i]
		if ref, ok := overlays[activityItemIdentity(*item)]; ok && item.Workspace == nil {
			item.Workspace = &ref
		}
	}
	for i := range response.ItemActivity {
		item := &response.ItemActivity[i]
		if ref, ok := overlays[activitySubjectIdentity(*item)]; ok && item.Workspace == nil {
			item.Workspace = &ref
		}
	}
}

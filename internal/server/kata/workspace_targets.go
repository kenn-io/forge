package kata

import (
	"context"
	"slices"

	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

// WorkspaceKataTargetsAvailable follows Kata's shared catalog and local discovery.
// A configured remote daemon remains a supported capability while temporarily offline.
func (h *Handler) WorkspaceKataTargetsAvailable() bool {
	catalog, err := h.loadCatalog()
	if err != nil {
		return false
	}
	for _, entry := range catalog.Daemons {
		daemon, err := h.resolveDaemon(entry)
		if err != nil {
			continue
		}
		if daemon.URL != "" || daemon.Local && h.discoverLocalDaemonURL() != "" {
			return true
		}
	}
	return false
}

func workspaceKataTarget(link kataEffectiveLink) workspaceapi.WorkspaceTarget {
	source := "inherited"
	if slices.Contains(link.Provenance, kataLinkIntrinsic) {
		source = "owner"
	} else if link.DirectLinkID != nil {
		source = "tracked"
	}
	id := int64(0)
	if link.DirectLinkID != nil {
		id = *link.DirectLinkID
	}
	return workspaceapi.WorkspaceTarget{
		ID: id, Type: "kata", Title: link.Title, State: link.Status, Source: source, Unavailable: link.UnavailableReason != "",
		Kata: &workspaceapi.WorkspaceKataTarget{DaemonID: link.DaemonID, ProjectUID: link.ProjectUID, IssueUID: link.IssueUID, Reference: link.Reference},
	}
}

func (h *Handler) ListWorkspaceKataTargets(ctx context.Context, id string) ([]workspaceapi.WorkspaceTarget, error) {
	response, err := h.listWorkspaceKataLinks(ctx, &kataWorkspaceLinkInput{WorkspaceID: id})
	if err != nil {
		return nil, err
	}
	targets := make([]workspaceapi.WorkspaceTarget, 0, len(response.Body.Links))
	for _, link := range response.Body.Links {
		targets = append(targets, workspaceKataTarget(link))
	}
	return targets, nil
}

func (h *Handler) AddWorkspaceKataTarget(ctx context.Context, id string, target workspaceapi.WorkspaceKataTarget) (workspaceapi.WorkspaceTarget, error) {
	if !h.WorkspaceKataTargetsAvailable() {
		return workspaceapi.WorkspaceTarget{}, httpapi.ServiceUnavailable("Kata target linking is unavailable")
	}
	response, err := h.createWorkspaceKataLink(ctx, &kataWorkspaceCreateLinkInput{WorkspaceID: id, Body: kataCreateLinkRequest{DaemonID: target.DaemonID, ProjectUID: target.ProjectUID, IssueUID: target.IssueUID}})
	if err != nil {
		return workspaceapi.WorkspaceTarget{}, err
	}
	for _, link := range response.Body.Links {
		if link.DaemonID == target.DaemonID && link.ProjectUID == target.ProjectUID && link.IssueUID == target.IssueUID {
			return workspaceKataTarget(link), nil
		}
	}
	return workspaceapi.WorkspaceTarget{}, httpapi.Internal("added Kata target is missing from workspace links")
}

func (h *Handler) RemoveWorkspaceKataTarget(ctx context.Context, id string, targetID int64) error {
	if !h.WorkspaceKataTargetsAvailable() {
		return httpapi.ServiceUnavailable("Kata target linking is unavailable")
	}
	_, err := h.deleteWorkspaceKataLink(ctx, &kataWorkspaceDeleteLinkInput{WorkspaceID: id, LinkID: targetID})
	return err
}

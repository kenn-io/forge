package kata

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
)

type kataProviderLinkInput struct {
	Provider string `path:"provider"`
	Owner    string `path:"owner"`
	Name     string `path:"name"`
	Number   int    `path:"number" minimum:"1"`
}

type kataProviderLinkHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number" minimum:"1"`
}

type kataProviderCreateLinkInput struct {
	Provider string `path:"provider"`
	Owner    string `path:"owner"`
	Name     string `path:"name"`
	Number   int    `path:"number" minimum:"1"`
	Body     kataCreateLinkRequest
}

type kataProviderCreateLinkHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number" minimum:"1"`
	Body         kataCreateLinkRequest
}

type kataProviderDeleteLinkInput struct {
	Provider string `path:"provider"`
	Owner    string `path:"owner"`
	Name     string `path:"name"`
	Number   int    `path:"number" minimum:"1"`
	LinkID   int64  `path:"link_id" minimum:"1"`
}

type kataProviderDeleteLinkHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number" minimum:"1"`
	LinkID       int64  `path:"link_id" minimum:"1"`
}

type kataWorkspaceLinkInput struct {
	WorkspaceID string `path:"id"`
}

type kataWorkspaceCreateLinkInput struct {
	WorkspaceID string `path:"id"`
	Body        kataCreateLinkRequest
}

type kataWorkspaceDeleteLinkInput struct {
	WorkspaceID string `path:"id"`
	LinkID      int64  `path:"link_id" minimum:"1"`
}

type kataCreateLinkRequest struct {
	DaemonID   string `json:"daemon_id"`
	ProjectUID string `json:"project_uid"`
	IssueUID   string `json:"issue_uid"`
}

type kataEffectiveLinksOutput = httpapi.BodyOutput[kataEffectiveLinksResponse]

type kataDeleteLinkOutput struct {
	Status int `status:"204"`
}

type workspaceKataSubjectRequest struct {
	kind   db.KataLinkSubjectKind
	number int
}

func registerKataLinkAPI(api huma.API, h *Handler) {
	registerKataProviderLinkRoutes(api, h, db.KataLinkSubjectIssue,
		"/issues/{provider}/{owner}/{name}/{number}",
		"/host/{platform_host}/issues/{provider}/{owner}/{name}/{number}",
		"issue")
	registerKataProviderLinkRoutes(api, h, db.KataLinkSubjectPullRequest,
		"/pulls/{provider}/{owner}/{name}/{number}",
		"/host/{platform_host}/pulls/{provider}/{owner}/{name}/{number}",
		"pull-request")
	workspacePath := "/workspaces/{id}/kata-links"
	huma.Get(api, workspacePath, h.listWorkspaceKataLinks,
		httpapi.DocumentOperation("list-workspace-kata-links", "List effective Kata links", "Kata"))
	huma.Post(api, workspacePath, h.createWorkspaceKataLink,
		httpapi.DocumentOperation("create-workspace-kata-link", "Create Kata link", "Kata"))
	huma.Delete(api, workspacePath+"/{link_id}", h.deleteWorkspaceKataLink,
		httpapi.DocumentOperation("delete-workspace-kata-link", "Delete Kata link", "Kata"))
}

func registerKataProviderLinkRoutes(
	api huma.API,
	h *Handler,
	kind db.KataLinkSubjectKind,
	path, hostPath, operationName string,
) {
	linksPath := path + "/kata-links"
	hostLinksPath := hostPath + "/kata-links"
	huma.Get(api, linksPath, func(ctx context.Context, input *kataProviderLinkInput) (*kataEffectiveLinksOutput, error) {
		return h.listProviderKataLinks(ctx, kind, input.Provider, "", input.Owner, input.Name, input.Number)
	}, httpapi.DocumentOperation("list-"+operationName+"-kata-links", "List effective Kata links", "Kata"))
	huma.Get(api, hostLinksPath, func(ctx context.Context, input *kataProviderLinkHostInput) (*kataEffectiveLinksOutput, error) {
		return h.listProviderKataLinks(ctx, kind, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Number)
	}, httpapi.DocumentOperation("list-"+operationName+"-kata-links-on-host", "List effective Kata links", "Kata"))
	huma.Post(api, linksPath, func(ctx context.Context, input *kataProviderCreateLinkInput) (*kataEffectiveLinksOutput, error) {
		return h.createProviderKataLink(ctx, kind, input.Provider, "", input.Owner, input.Name, input.Number, input.Body)
	}, httpapi.DocumentOperation("create-"+operationName+"-kata-link", "Create Kata link", "Kata"))
	huma.Post(api, hostLinksPath, func(ctx context.Context, input *kataProviderCreateLinkHostInput) (*kataEffectiveLinksOutput, error) {
		return h.createProviderKataLink(ctx, kind, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Number, input.Body)
	}, httpapi.DocumentOperation("create-"+operationName+"-kata-link-on-host", "Create Kata link", "Kata"))
	huma.Delete(api, linksPath+"/{link_id}", func(ctx context.Context, input *kataProviderDeleteLinkInput) (*kataDeleteLinkOutput, error) {
		return h.deleteProviderKataLink(ctx, kind, input.Provider, "", input.Owner, input.Name, input.Number, input.LinkID)
	}, httpapi.DocumentOperation("delete-"+operationName+"-kata-link", "Delete Kata link", "Kata"))
	huma.Delete(api, hostLinksPath+"/{link_id}", func(ctx context.Context, input *kataProviderDeleteLinkHostInput) (*kataDeleteLinkOutput, error) {
		return h.deleteProviderKataLink(ctx, kind, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Number, input.LinkID)
	}, httpapi.DocumentOperation("delete-"+operationName+"-kata-link-on-host", "Delete Kata link", "Kata"))
}

func (h *Handler) listProviderKataLinks(
	ctx context.Context,
	kind db.KataLinkSubjectKind,
	provider, host, owner, name string,
	number int,
) (*kataEffectiveLinksOutput, error) {
	subject, err := h.resolveProviderKataLinkSubject(ctx, kind, provider, host, owner, name, number)
	if err != nil {
		return nil, err
	}
	links, err := h.db.ListKataIssueLinks(ctx, subject)
	if err != nil {
		return nil, httpapi.Internal("list Kata links failed")
	}
	return &kataEffectiveLinksOutput{Body: h.hydrateDirectKataLinks(ctx, links)}, nil
}

func (h *Handler) createProviderKataLink(
	ctx context.Context,
	kind db.KataLinkSubjectKind,
	provider, host, owner, name string,
	number int,
	request kataCreateLinkRequest,
) (*kataEffectiveLinksOutput, error) {
	subject, err := h.resolveProviderKataLinkSubject(ctx, kind, provider, host, owner, name, number)
	if err != nil {
		return nil, err
	}
	request, err = h.validateKataLinkRequest(ctx, request)
	if err != nil {
		return nil, err
	}
	_, err = h.db.CreateKataIssueLink(ctx, db.KataIssueLink{
		Subject: subject, DaemonID: request.DaemonID,
		ProjectUID: request.ProjectUID, IssueUID: request.IssueUID,
	})
	if err != nil {
		return nil, httpapi.Internal("create Kata link failed")
	}
	links, err := h.db.ListKataIssueLinks(ctx, subject)
	if err != nil {
		return nil, httpapi.Internal("list Kata links failed")
	}
	return &kataEffectiveLinksOutput{Body: h.hydrateDirectKataLinks(ctx, links)}, nil
}

func (h *Handler) validateKataLinkRequest(
	ctx context.Context,
	request kataCreateLinkRequest,
) (kataCreateLinkRequest, error) {
	ctx, cancel := context.WithTimeout(ctx, kataDaemonReadTimeout)
	defer cancel()
	request.DaemonID = strings.TrimSpace(request.DaemonID)
	request.ProjectUID = strings.TrimSpace(request.ProjectUID)
	request.IssueUID = strings.TrimSpace(request.IssueUID)
	if request.DaemonID == "" {
		return kataCreateLinkRequest{}, httpapi.Validation("body.daemon_id", "daemon_id is required")
	}
	if request.ProjectUID == "" {
		return kataCreateLinkRequest{}, httpapi.Validation("body.project_uid", "project_uid is required")
	}
	if request.IssueUID == "" {
		return kataCreateLinkRequest{}, httpapi.Validation("body.issue_uid", "issue_uid is required")
	}
	client, problem := h.kataClientForDaemon(request.DaemonID)
	if problem != nil {
		return kataCreateLinkRequest{}, problem
	}
	health, healthErr := client.Health(ctx)
	if healthErr != nil || health.State != "connected" {
		return kataCreateLinkRequest{}, kataDaemonUnavailableProblem(request.DaemonID, health)
	}
	detail, detailErr := client.IssueDetail(ctx, request.IssueUID)
	if detailErr != nil {
		return kataCreateLinkRequest{}, kataUpstreamProblem("verify Kata issue failed", request.DaemonID)
	}
	var identity struct {
		Issue struct {
			UID        string `json:"uid"`
			ProjectUID string `json:"project_uid"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(detail, &identity); err != nil {
		return kataCreateLinkRequest{}, kataUpstreamProblem("verify Kata issue failed", request.DaemonID)
	}
	if identity.Issue.UID != request.IssueUID || identity.Issue.ProjectUID != request.ProjectUID {
		return kataCreateLinkRequest{}, httpapi.Conflict(httpapi.CodeConflict, "Kata issue identity changed", map[string]any{
			"daemon":    request.DaemonID,
			"issue_uid": identity.Issue.UID, "project_uid": identity.Issue.ProjectUID,
		})
	}
	return request, nil
}

func (h *Handler) deleteProviderKataLink(
	ctx context.Context,
	kind db.KataLinkSubjectKind,
	provider, host, owner, name string,
	number int,
	linkID int64,
) (*kataDeleteLinkOutput, error) {
	subject, err := h.resolveProviderKataLinkSubject(ctx, kind, provider, host, owner, name, number)
	if err != nil {
		return nil, err
	}
	deleted, err := h.db.DeleteKataIssueLink(ctx, subject, linkID)
	if err != nil {
		return nil, httpapi.Internal("delete Kata link failed")
	}
	if !deleted {
		return nil, httpapi.NotFound(httpapi.CodeNotFound, "Kata link not found", nil)
	}
	return &kataDeleteLinkOutput{Status: http.StatusNoContent}, nil
}

func (h *Handler) listWorkspaceKataLinks(
	ctx context.Context,
	input *kataWorkspaceLinkInput,
) (*kataEffectiveLinksOutput, error) {
	workspace, subject, err := h.resolveWorkspaceKataLinkSubject(ctx, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	candidates := make(map[string]*kataLinkCandidate)
	if workspace.KataMetadata != nil {
		metadata := workspace.KataMetadata
		mergeKataLinkCandidate(
			candidates, metadata.DaemonID, metadata.ProjectUID, metadata.IssueUID, kataLinkIntrinsic,
		)
	}
	direct, err := h.db.ListKataIssueLinks(ctx, subject)
	if err != nil {
		return nil, httpapi.Internal("list workspace Kata links failed")
	}
	addStoredKataLinkCandidates(candidates, direct, kataLinkDirect, true)
	inheritedSubjects, err := h.workspaceInheritedKataSubjects(ctx, workspace)
	if err != nil {
		return nil, err
	}
	for _, inheritedSubject := range inheritedSubjects {
		inherited, err := h.db.ListKataIssueLinks(ctx, inheritedSubject)
		if err != nil {
			return nil, httpapi.Internal("list inherited Kata links failed")
		}
		addStoredKataLinkCandidates(candidates, inherited, kataLinkInherited, false)
	}
	return &kataEffectiveLinksOutput{Body: h.hydrateKataLinkCandidates(ctx, candidates)}, nil
}

func (h *Handler) createWorkspaceKataLink(
	ctx context.Context,
	input *kataWorkspaceCreateLinkInput,
) (*kataEffectiveLinksOutput, error) {
	_, subject, err := h.resolveWorkspaceKataLinkSubject(ctx, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	request, err := h.validateKataLinkRequest(ctx, input.Body)
	if err != nil {
		return nil, err
	}
	_, err = h.db.CreateKataIssueLink(ctx, db.KataIssueLink{
		Subject: subject, DaemonID: request.DaemonID,
		ProjectUID: request.ProjectUID, IssueUID: request.IssueUID,
	})
	if err != nil {
		return nil, httpapi.Internal("create workspace Kata link failed")
	}
	return h.listWorkspaceKataLinks(ctx, &kataWorkspaceLinkInput{WorkspaceID: input.WorkspaceID})
}

func (h *Handler) deleteWorkspaceKataLink(
	ctx context.Context,
	input *kataWorkspaceDeleteLinkInput,
) (*kataDeleteLinkOutput, error) {
	_, subject, err := h.resolveWorkspaceKataLinkSubject(ctx, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	deleted, err := h.db.DeleteKataIssueLink(ctx, subject, input.LinkID)
	if err != nil {
		return nil, httpapi.Internal("delete workspace Kata link failed")
	}
	if !deleted {
		return nil, httpapi.NotFound(httpapi.CodeNotFound, "Kata link not found", nil)
	}
	return &kataDeleteLinkOutput{Status: http.StatusNoContent}, nil
}

func (h *Handler) resolveWorkspaceKataLinkSubject(
	ctx context.Context,
	workspaceID string,
) (*db.Workspace, db.KataLinkSubject, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	workspace, err := h.db.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, db.KataLinkSubject{}, httpapi.Internal("get workspace failed")
	}
	if workspace == nil {
		return nil, db.KataLinkSubject{}, httpapi.NotFound(
			httpapi.CodeWorkspaceNotFound, "workspace not found", nil,
		)
	}
	return workspace, db.KataLinkSubject{
		Kind: db.KataLinkSubjectWorkspace, WorkspaceID: workspace.ID,
	}, nil
}

func (h *Handler) workspaceInheritedKataSubjects(
	ctx context.Context,
	workspace *db.Workspace,
) ([]db.KataLinkSubject, error) {
	hasHistoricalOccupants, err := h.db.WorkspaceRepoRouteHasHistoricalOccupants(
		ctx, workspace.Platform, workspace.PlatformHost, workspace.RepoOwner, workspace.RepoName,
	)
	if err != nil {
		return nil, httpapi.Internal("inspect workspace repository route failed")
	}
	if hasHistoricalOccupants {
		return []db.KataLinkSubject{}, nil
	}
	requests := make([]workspaceKataSubjectRequest, 0, 2)
	switch workspace.ItemType {
	case db.WorkspaceItemTypeIssue:
		requests = append(requests, workspaceKataSubjectRequest{
			kind: db.KataLinkSubjectIssue, number: workspace.ItemNumber,
		})
	case db.WorkspaceItemTypePullRequest:
		requests = append(requests, workspaceKataSubjectRequest{
			kind: db.KataLinkSubjectPullRequest, number: workspace.ItemNumber,
		})
	}
	if workspace.AssociatedPRNumber != nil &&
		(workspace.ItemType != db.WorkspaceItemTypePullRequest ||
			workspace.ItemNumber != *workspace.AssociatedPRNumber) {
		requests = append(requests, workspaceKataSubjectRequest{
			kind: db.KataLinkSubjectPullRequest, number: *workspace.AssociatedPRNumber,
		})
	}
	subjects := make([]db.KataLinkSubject, 0, len(requests))
	for _, request := range requests {
		subject, err := h.resolveProviderKataLinkSubject(
			ctx, request.kind, workspace.Platform, workspace.PlatformHost,
			workspace.RepoOwner, workspace.RepoName, request.number,
		)
		if err != nil {
			var problem *httpapi.ProblemError
			if errors.As(err, &problem) && kataInheritanceCanSkipProblem(problem.Code) {
				continue
			}
			return nil, err
		}
		subjects = append(subjects, subject)
	}
	return subjects, nil
}

func kataInheritanceCanSkipProblem(code httpapi.ProblemCode) bool {
	switch code {
	case httpapi.CodeIssueNotFound,
		httpapi.CodePullNotFound,
		httpapi.CodeRepoNotFound,
		httpapi.CodeResyncRequired:
		return true
	default:
		return false
	}
}

func (h *Handler) resolveProviderKataLinkSubject(
	ctx context.Context,
	kind db.KataLinkSubjectKind,
	provider, host, owner, name string,
	number int,
) (db.KataLinkSubject, error) {
	repo, err := h.resolver.LookupRoute(ctx, provider, host, owner, name)
	if err != nil {
		return db.KataLinkSubject{}, httpapi.ProviderRouteLookupError(err)
	}
	var externalID string
	switch kind {
	case db.KataLinkSubjectIssue:
		issue, err := h.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, number)
		if err != nil {
			return db.KataLinkSubject{}, httpapi.Internal("get issue failed")
		}
		if issue == nil {
			return db.KataLinkSubject{}, httpapi.NotFound(httpapi.CodeIssueNotFound, "issue not found", nil)
		}
		externalID = strings.TrimSpace(issue.PlatformExternalID)
	case db.KataLinkSubjectPullRequest:
		pull, err := h.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, number)
		if err != nil {
			return db.KataLinkSubject{}, httpapi.Internal("get pull request failed")
		}
		if pull == nil {
			return db.KataLinkSubject{}, httpapi.NotFound(httpapi.CodePullNotFound, "pull request not found", nil)
		}
		externalID = strings.TrimSpace(pull.PlatformExternalID)
	default:
		return db.KataLinkSubject{}, httpapi.Internal("unsupported Kata link subject")
	}
	if externalID == "" {
		return db.KataLinkSubject{}, httpapi.Conflict(
			httpapi.CodeResyncRequired,
			"provider item needs a successful resync before Kata links can be changed",
			map[string]any{"subject_kind": kind, "item_number": number},
		)
	}
	return db.KataLinkSubject{
		Kind: kind, RepoID: repo.ID, ProviderItemExternalID: externalID,
	}, nil
}

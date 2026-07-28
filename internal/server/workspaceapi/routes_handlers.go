package workspaceapi

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/tokenauth"
	"go.kenn.io/middleman/internal/workspace"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

type createWorkspaceInput struct {
	Body struct {
		Provider     string `json:"provider"`
		PlatformHost string `json:"platform_host"`
		Owner        string `json:"owner"`
		Name         string `json:"name"`
		MRNumber     int    `json:"mr_number"`
	}
}

type createIssueWorkspaceInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		GitHeadRef             *string `json:"git_head_ref,omitempty"`
		ReuseExistingBranch    bool    `json:"reuse_existing_branch,omitempty"`
		ReuseExistingDirectory bool    `json:"reuse_existing_directory,omitempty"`
	}
}

// CreateIssueWorkspaceInput is shared with provider-aware route wrappers.
type CreateIssueWorkspaceInput = createIssueWorkspaceInput

type createAdHocWorkspaceInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         struct {
		Branch              *string `json:"branch,omitempty" doc:"Branch for the new worktree; generated when empty"`
		ReuseExistingBranch bool    `json:"reuse_existing_branch,omitempty"`
	}
}

// CreateAdHocWorkspaceInput is shared with provider-aware route wrappers.
type CreateAdHocWorkspaceInput = createAdHocWorkspaceInput

const issueWorkspaceBranchConflictType = "urn:middleman:error:issue-workspace-branch-conflict"

const adHocWorkspaceBranchConflictType = "urn:middleman:error:workspace-branch-conflict"

type getWorkspaceInput struct {
	ID string `path:"id"`
}

type getWorkspaceFilesInput struct {
	ID         string `path:"id"`
	Base       string `query:"base"      doc:"Diff base: head, pushed, or merge-target"`
	Whitespace string `query:"whitespace" doc:"Set to hide to ignore whitespace-only changes"`
	Commit     string `query:"commit" doc:"Scope to a single commit SHA"`
	From       string `query:"from"   doc:"Start SHA for range diff (inclusive)"`
	To         string `query:"to"     doc:"End SHA for range diff (inclusive)"`
}

type watchWorkspaceDiffInput struct {
	ID      string `path:"id"`
	Version string `query:"version" doc:"Last observed opaque workspace diff snapshot version"`
}

type getWorkspaceDiffInput struct {
	ID         string `path:"id"`
	Base       string `query:"base"      doc:"Diff base: head, pushed, or merge-target"`
	Whitespace string `query:"whitespace" doc:"Set to hide to ignore whitespace-only changes"`
	Path       string `query:"path"      doc:"Optional file path to limit the returned patch"`
	Commit     string `query:"commit" doc:"Scope to a single commit SHA"`
	From       string `query:"from"   doc:"Start SHA for range diff (inclusive)"`
	To         string `query:"to"     doc:"End SHA for range diff (inclusive)"`
	Revision   string `query:"revision" doc:"Optional snapshot_version returned by the workspace files endpoint"`
}

type getWorkspaceFilePreviewInput struct {
	ID         string `path:"id"`
	Base       string `query:"base"       enum:"head,pushed,merge-target" doc:"Diff base: head, pushed, or merge-target"`
	Whitespace string `query:"whitespace" enum:"hide"                     doc:"Set to hide to ignore whitespace-only changes"`
	Path       string `query:"path"       doc:"Changed file path to preview"`
	Side       string `query:"side"       enum:"old,new"                  doc:"Optional diff side to read for context expansion"`
	Commit     string `query:"commit" doc:"Scope to a single commit SHA"`
	From       string `query:"from"   doc:"Start SHA for range diff (inclusive)"`
	To         string `query:"to"     doc:"End SHA for range diff (inclusive)"`
	Revision   string `query:"revision" doc:"Optional snapshot_version returned by the workspace files endpoint"`
}

type getWorkspaceCommitsInput struct {
	ID string `path:"id"`
}

type retryWorkspaceInput struct {
	ID string `path:"id"`
}

type refreshWorkspaceInput struct {
	ID string `path:"id"`
}

type getWorkspaceRuntimeInput struct {
	ID string `path:"id"`
}

type launchWorkspaceRuntimeSessionInput struct {
	ID   string `path:"id"`
	Body struct {
		TargetKey     string `json:"target_key"`
		DisplayRegion string `json:"display_region,omitempty"`
	}
}

// LaunchWorkspaceRuntimeSessionInput is the programmatic launch contract.
type LaunchWorkspaceRuntimeSessionInput = launchWorkspaceRuntimeSessionInput

type stopWorkspaceRuntimeSessionInput struct {
	ID         string `path:"id"`
	SessionKey string `path:"session_key"`
}

type renameWorkspaceRuntimeSessionInput struct {
	ID         string `path:"id"`
	SessionKey string `path:"session_key"`
	Body       struct {
		Label string `json:"label"`
	}
}

type getWorkspaceRuntimeSessionAttachSpecInput struct {
	ID         string `path:"id"`
	SessionKey string `path:"session_key"`
}

type deleteWorkspaceInput struct {
	ID    string `path:"id"`
	Force bool   `query:"force"`
}

type listWorkspacesOutputBody struct {
	Workspaces []workspaceResponse `json:"workspaces"`
}

type listWorkspacesOutput = httpapi.BodyOutput[listWorkspacesOutputBody]

type getWorkspaceOutput = httpapi.BodyOutput[workspaceResponse]

type getWorkspaceDiffOutput = httpapi.BodyOutput[diffResponse]
type getWorkspaceFilePreviewOutput = httpapi.BodyOutput[filePreviewResponse]
type getWorkspaceFilesOutput = httpapi.BodyOutput[filesResponse]
type watchWorkspaceDiffOutput = httpapi.BodyOutput[workspaceDiffWatchResponse]
type getWorkspaceCommitsOutput = httpapi.BodyOutput[commitsResponse]

type getWorkspaceRuntimeOutput = httpapi.BodyOutput[workspaceRuntimeResponse]

type workspaceRuntimeSessionOutput = httpapi.BodyOutput[localruntime.SessionInfo]

type runtimeAttachSpecOutput = httpapi.BodyOutput[runtimeAttachSpecResponse]

type createWorkspaceOutput = httpapi.AcceptedBodyOutput[workspaceResponse]

// CreateWorkspaceOutput is shared with Kata and provider-aware route wrappers.
type CreateWorkspaceOutput = createWorkspaceOutput

type refreshWorkspaceOutput = httpapi.BodyOutput[workspaceResponse]

type workspaceDiffRequest struct {
	Summary           *db.WorkspaceSummary
	Base              workspace.WorktreeDiffBase
	MergeTargetBranch string
	FromSHA           string
	ToSHA             string
}

// --- Workspaces ---

// createWorkspace creates or reuses a PR-backed middleman workspace.
//
// This API exists so a tracked pull request can have a durable local execution
// context that middleman owns and can reopen later. It is not a generic
// worktree-creation endpoint for arbitrary branches.
func (s *Handler) createWorkspace(
	ctx context.Context, input *createWorkspaceInput,
) (*createWorkspaceOutput, error) {
	if s.workspaces == nil {
		return nil, httpapi.ServiceUnavailable("workspace manager not configured")
	}
	provider, err := s.createWorkspaceProvider(ctx, input)
	if err != nil {
		return nil, err
	}

	ws, err := s.workspaces.Create(
		ctx,
		provider,
		input.Body.PlatformHost,
		input.Body.Owner,
		input.Body.Name,
		input.Body.MRNumber,
	)
	if err != nil {
		if errors.Is(err, workspace.ErrWorkspaceNotFound) {
			return nil, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, err.Error(), nil)
		}
		if errors.Is(err, workspace.ErrWorkspaceNotSynced) {
			return nil, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, err.Error(), nil)
		}
		if errors.Is(err, workspace.ErrWorkspaceDuplicate) {
			return nil, httpapi.Conflict(httpapi.CodeConflict,
				"workspace already exists for this MR", nil)
		}
		return nil, httpapi.Internal("create workspace: " + err.Error())
	}

	s.runWorkspaceSetup(ws)

	summary, err := s.workspaces.GetSummary(ctx, ws.ID)
	if err != nil {
		return nil, httpapi.Internal("get workspace summary: " + err.Error())
	}
	if summary == nil {
		return nil, httpapi.Internal("workspace summary missing after create")
	}
	resp := s.toWorkspaceResponse(ctx, summary)
	resp.Created = true
	return &createWorkspaceOutput{
		Status: http.StatusAccepted,
		Body:   resp,
	}, nil
}

func (s *Handler) createWorkspaceProvider(
	ctx context.Context, input *createWorkspaceInput,
) (string, error) {
	if strings.TrimSpace(input.Body.Provider) != "" {
		provider, err := normalizeRouteProvider(input.Body.Provider)
		if err != nil {
			return "", httpapi.Validation("body.provider", err.Error())
		}
		return provider, nil
	}
	_ = ctx
	return "", httpapi.Validation("body.provider", "provider is required")
}

func (s *Handler) runWorkspaceSetup(ws *workspace.Workspace) {
	s.runWorkspaceSetupWithBasePath(ws, "")
}

func (s *Handler) runWorkspaceSetupWithBasePath(ws *workspace.Workspace, basePath string) {
	s.runBackground(func(bgCtx context.Context) {
		for {
			setupErr := s.workspaces.SetupWithWorktreeBasePath(bgCtx, ws, basePath)
			summary, getErr := s.workspaces.GetSummary(
				bgCtx, ws.ID,
			)
			if getErr != nil {
				slog.Warn("get workspace summary after setup",
					"id", ws.ID, "err", getErr,
				)
				return
			}
			if summary == nil {
				return
			}
			resp := s.toWorkspaceResponse(bgCtx, summary)
			if setupErr != nil {
				slog.Warn("workspace setup failed",
					"id", ws.ID, "err", setupErr,
				)
			}
			s.hub.Broadcast(Event{
				Type: "workspace_status",
				Data: resp,
			})
			if setupErr == nil {
				s.runWorkspacePushedHeadObserverPass(bgCtx)
			}

			next, queued, queueErr := s.workspaces.StartQueuedRetryIfErrored(
				bgCtx, ws.ID,
			)
			if queueErr != nil {
				slog.Warn("start queued workspace retry",
					"id", ws.ID, "err", queueErr,
				)
				summary, getErr = s.workspaces.GetSummary(bgCtx, ws.ID)
				if getErr != nil {
					slog.Warn("get workspace summary after queued retry failure",
						"id", ws.ID, "err", getErr,
					)
					return
				}
				if summary != nil {
					s.hub.Broadcast(Event{
						Type: "workspace_status",
						Data: s.toWorkspaceResponse(bgCtx, summary),
					})
				}
				return
			}
			if !queued {
				return
			}
			if next == nil {
				return
			}
			ws = next
			summary, getErr = s.workspaces.GetSummary(bgCtx, ws.ID)
			if getErr != nil {
				slog.Warn("get workspace summary after queued retry",
					"id", ws.ID, "err", getErr,
				)
				return
			}
			if summary == nil {
				return
			}
			s.hub.Broadcast(Event{
				Type: "workspace_status",
				Data: s.toWorkspaceResponse(bgCtx, summary),
			})
		}
	})
}

// RunWorkspaceSetupWithBasePath starts asynchronous materialization for a
// workspace created by a dependent domain such as Kata.
func (s *Handler) RunWorkspaceSetupWithBasePath(ws *workspace.Workspace, basePath string) {
	s.runWorkspaceSetupWithBasePath(ws, basePath)
}

// createIssueWorkspace creates or reuses an issue-backed middleman workspace.
//
// This API exists so an issue can have its own durable local execution context
// even when there is no PR branch yet. These workspaces start from the repo's
// current origin/HEAD and are presented in the UI with issue-specific sidebar
// behavior instead of PR/reviews affordances.
func (s *Handler) createIssueWorkspace(
	ctx context.Context, input *createIssueWorkspaceInput,
) (*createWorkspaceOutput, error) {
	if s.workspaces == nil {
		return nil, httpapi.ServiceUnavailable("workspace manager not configured")
	}
	if input.Body.ReuseExistingBranch && input.Body.ReuseExistingDirectory {
		return nil, httpapi.Validation(
			"body.reuse_existing_directory",
			"reuse_existing_branch and reuse_existing_directory are mutually exclusive",
		)
	}
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}

	existing, err := s.workspaces.GetByIssueForProvider(
		ctx,
		repo.Platform,
		repo.PlatformHost,
		repo.Owner,
		repo.Name,
		input.Number,
	)
	if err != nil {
		return nil, httpapi.Internal("lookup existing issue workspace: " + err.Error())
	}
	if existing != nil {
		summary, getErr := s.workspaces.GetSummary(ctx, existing.ID)
		if getErr != nil {
			return nil, httpapi.Internal("get workspace summary: " + getErr.Error())
		}
		if summary == nil {
			return nil, httpapi.Internal("workspace summary missing for existing workspace")
		}
		return &createWorkspaceOutput{
			Status: http.StatusAccepted,
			Body:   s.toWorkspaceResponse(ctx, summary),
		}, nil
	}

	ws, err := s.workspaces.CreateIssue(
		ctx,
		repo.PlatformHost,
		repo.Owner,
		repo.Name,
		input.Number,
		workspace.CreateIssueOptions{
			Provider:               input.Provider,
			GitHeadRef:             strings.TrimSpace(derefString(input.Body.GitHeadRef)),
			ReuseExistingBranch:    input.Body.ReuseExistingBranch,
			ReuseExistingDirectory: input.Body.ReuseExistingDirectory,
		},
	)
	if err != nil {
		msg := err.Error()
		var recoveryErr *workspace.WorkspaceDirectoryRecoveryError
		if errors.As(err, &recoveryErr) {
			details := map[string]any{
				"reason": string(recoveryErr.Reason),
			}
			if recoveryErr.Reason == workspace.WorkspaceDirectoryBranchMismatch {
				details["expectedBranch"] = recoveryErr.ExpectedBranch
				details["actualBranch"] = recoveryErr.ActualBranch
			}
			return nil, httpapi.Conflict(
				httpapi.CodeWorkspaceDirectoryNotReusable,
				recoveryErr.Error(),
				details,
			)
		}
		var branchConflict *workspace.WorkspaceBranchConflictError
		if errors.As(err, &branchConflict) {
			// Branch-conflict gets the typed problem envelope with
			// Type carrying the URN and Details carrying the conflicting
			// branch + suggested alternative. The legacy Errors[]
			// entries are populated for callers still introspecting
			// per-field huma error details.
			conflict := httpapi.NewProblem(
				http.StatusConflict,
				httpapi.CodeBranchConflict,
				"A local branch with the requested name already exists.",
				map[string]any{
					"branch":            branchConflict.Branch,
					"suggestedBranch":   branchConflict.SuggestedBranch,
					"existingDirectory": branchConflict.ExistingDirectory,
				},
			)
			conflict.Type = issueWorkspaceBranchConflictType
			conflict.Title = "Issue workspace branch conflict"
			conflict.Errors = []*huma.ErrorDetail{
				{
					Message:  "Requested branch already exists",
					Location: "body.git_head_ref",
					Value:    branchConflict.Branch,
				},
				{
					Message:  "Suggested alternative branch name",
					Location: "body.suggested_git_head_ref",
					Value:    branchConflict.SuggestedBranch,
				},
			}
			return nil, conflict
		}
		if strings.Contains(msg, "not tracked") {
			return nil, httpapi.NotFound(httpapi.CodeNotFound, msg, nil)
		}
		if strings.Contains(msg, "not synced") {
			return nil, httpapi.NotFound(httpapi.CodeNotFound, msg, nil)
		}
		if strings.Contains(msg, "invalid branch name") {
			return nil, httpapi.Validation("body.git_head_ref", msg)
		}
		if strings.Contains(msg, "UNIQUE constraint") {
			existing, getErr := s.workspaces.GetByIssueForProvider(
				ctx,
				repo.Platform,
				repo.PlatformHost,
				repo.Owner,
				repo.Name,
				input.Number,
			)
			if getErr == nil && existing != nil {
				summary, summaryErr := s.workspaces.GetSummary(ctx, existing.ID)
				if summaryErr == nil && summary != nil {
					return &createWorkspaceOutput{
						Status: http.StatusAccepted,
						Body:   s.toWorkspaceResponse(ctx, summary),
					}, nil
				}
			}
		}
		return nil, httpapi.Internal("create issue workspace: " + msg)
	}

	createdBranch := ws.WorkspaceBranch != ""
	s.runWorkspaceSetup(ws)

	summary, err := s.workspaces.GetSummary(ctx, ws.ID)
	if err != nil {
		return nil, httpapi.Internal("get workspace summary: " + err.Error())
	}
	if summary == nil {
		return nil, httpapi.Internal("workspace summary missing after create")
	}

	resp := s.toWorkspaceResponse(ctx, summary)
	resp.Created = createdBranch
	return &createWorkspaceOutput{
		Status: http.StatusAccepted,
		Body:   resp,
	}, nil
}

// CreateIssueWorkspace creates or reuses an issue-backed workspace.
func (s *Handler) CreateIssueWorkspace(
	ctx context.Context, input *CreateIssueWorkspaceInput,
) (*CreateWorkspaceOutput, error) {
	return s.createIssueWorkspace(ctx, input)
}

// createAdHocWorkspace creates or reuses a workspace for new work in a tracked
// repository.
//
// This API exists so a maintainer can start work that has no pull request,
// issue, or Kata task behind it without inventing one first. The branch is the
// workspace's identity, so requesting the same branch twice returns the first
// workspace instead of a second worktree. Like issue-backed workspaces these
// start from the repository's current origin/HEAD.
func (s *Handler) createAdHocWorkspace(
	ctx context.Context, input *createAdHocWorkspaceInput,
) (*createWorkspaceOutput, error) {
	if s.workspaces == nil {
		return nil, httpapi.ServiceUnavailable("workspace manager not configured")
	}
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}

	branch := strings.TrimSpace(derefString(input.Body.Branch))
	itemKey := db.AdHocWorkspaceItemKey(branch)
	if itemKey != "" {
		existing, err := s.adHocWorkspaceForBranch(ctx, repo, itemKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}

	ws, err := s.workspaces.CreateAdHoc(
		ctx,
		repo.Platform,
		repo.PlatformHost,
		repo.Owner,
		repo.Name,
		workspace.CreateAdHocOptions{
			BranchName:          branch,
			ReuseExistingBranch: input.Body.ReuseExistingBranch,
		},
	)
	if err != nil {
		return s.adHocWorkspaceCreateError(ctx, repo, itemKey, err)
	}

	createdBranch := ws.WorkspaceBranch != ""
	s.runWorkspaceSetup(ws)

	summary, err := s.workspaces.GetSummary(ctx, ws.ID)
	if err != nil {
		return nil, httpapi.Internal("get workspace summary: " + err.Error())
	}
	if summary == nil {
		return nil, httpapi.Internal("workspace summary missing after create")
	}
	response := s.toWorkspaceResponse(ctx, summary)
	response.Created = createdBranch
	return &createWorkspaceOutput{
		Status: http.StatusAccepted,
		Body:   response,
	}, nil
}

// adHocWorkspaceForBranch returns the accepted response for an ad-hoc
// workspace that already owns the requested branch, or nil when none exists.
func (s *Handler) adHocWorkspaceForBranch(
	ctx context.Context, repo *db.Repo, itemKey string,
) (*createWorkspaceOutput, error) {
	existing, err := s.workspaces.GetByItemKeyForProvider(
		ctx,
		repo.Platform,
		repo.PlatformHost,
		repo.Owner,
		repo.Name,
		db.WorkspaceItemTypeAdHoc,
		itemKey,
	)
	if err != nil {
		return nil, httpapi.Internal("lookup existing workspace: " + err.Error())
	}
	if existing == nil {
		return nil, nil
	}
	summary, err := s.workspaces.GetSummary(ctx, existing.ID)
	if err != nil {
		return nil, httpapi.Internal("get workspace summary: " + err.Error())
	}
	if summary == nil {
		return nil, httpapi.Internal("workspace summary missing for existing workspace")
	}
	return &createWorkspaceOutput{
		Status: http.StatusAccepted,
		Body:   s.toWorkspaceResponse(ctx, summary),
	}, nil
}

func (s *Handler) adHocWorkspaceCreateError(
	ctx context.Context, repo *db.Repo, itemKey string, err error,
) (*createWorkspaceOutput, error) {
	msg := err.Error()
	var branchConflict *workspace.WorkspaceBranchConflictError
	if errors.As(err, &branchConflict) {
		conflict := httpapi.NewProblem(
			http.StatusConflict,
			httpapi.CodeBranchConflict,
			"A local branch with the requested name already exists.",
			map[string]any{
				"branch":          branchConflict.Branch,
				"suggestedBranch": branchConflict.SuggestedBranch,
			},
		)
		conflict.Type = adHocWorkspaceBranchConflictType
		conflict.Title = "Workspace branch conflict"
		return nil, conflict
	}
	if errors.Is(err, workspace.ErrWorkspaceNotFound) {
		return nil, httpapi.NotFound(httpapi.CodeNotFound, msg, nil)
	}
	if errors.Is(err, workspace.ErrInvalidBranchName) {
		return nil, httpapi.Validation("body.branch", msg)
	}
	// A racing create for the same branch loses on the workspace unique
	// index; hand back the winner instead of a conflict the caller cannot
	// act on.
	if itemKey != "" && errors.Is(err, workspace.ErrWorkspaceDuplicate) {
		existing, existingErr := s.adHocWorkspaceForBranch(ctx, repo, itemKey)
		if existingErr == nil && existing != nil {
			return existing, nil
		}
		return nil, httpapi.Conflict(httpapi.CodeConflict,
			"workspace already exists for this branch", nil)
	}
	return nil, httpapi.Internal("create workspace: " + msg)
}

// CreateAdHocWorkspace creates or reuses a workspace for new work.
func (s *Handler) CreateAdHocWorkspace(
	ctx context.Context, input *CreateAdHocWorkspaceInput,
) (*CreateWorkspaceOutput, error) {
	return s.createAdHocWorkspace(ctx, input)
}

// listWorkspaces returns middleman's persisted workspace records.
//
// Its purpose is to drive the workspaces page and terminal picker from
// middleman's own database model, rather than from ad hoc discovery of host
// worktrees.
func (s *Handler) listWorkspaces(
	ctx context.Context, _ *struct{},
) (*listWorkspacesOutput, error) {
	if s.workspaces == nil {
		out := &listWorkspacesOutput{}
		out.Body.Workspaces = []workspaceResponse{}
		return out, nil
	}

	s.scheduleWorkspaceTmuxPrune()

	summaries, err := s.workspaces.ListSummaries(ctx)
	if err != nil {
		return nil, httpapi.Internal("list workspaces failed")
	}
	s.trimWorkspaceEnrichmentCache(summaries)

	list := make([]workspaceResponse, len(summaries))
	for i := range summaries {
		list[i] = s.toCachedWorkspaceResponse(&summaries[i])
	}

	out := &listWorkspacesOutput{}
	out.Body.Workspaces = list
	return out, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// getWorkspace returns one persisted middleman workspace.
//
// The terminal view uses this to reopen an existing local execution context and
// determine whether the workspace is PR-backed or issue-backed.
func (s *Handler) getWorkspace(
	ctx context.Context, input *getWorkspaceInput,
) (*getWorkspaceOutput, error) {
	if s.workspaces == nil {
		return nil, httpapi.ServiceUnavailable("workspace manager not configured")
	}

	summary, err := s.workspaces.GetSummary(ctx, input.ID)
	if err != nil {
		return nil, httpapi.Internal("get workspace failed")
	}
	if summary == nil {
		return nil, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, "workspace not found", nil)
	}

	return &getWorkspaceOutput{
		Body: s.toCachedWorkspaceResponse(summary),
	}, nil
}

func (s *Handler) refreshWorkspace(
	ctx context.Context, input *refreshWorkspaceInput,
) (*refreshWorkspaceOutput, error) {
	if s.workspaces == nil {
		return nil, httpapi.ServiceUnavailable("workspace manager not configured")
	}
	if s.syncer == nil {
		return nil, httpapi.ServiceUnavailable("syncer not configured")
	}

	summary, err := s.workspaces.GetSummary(ctx, input.ID)
	if err != nil {
		return nil, httpapi.Internal("get workspace failed")
	}
	if summary == nil {
		return nil, httpapi.NotFound(
			httpapi.CodeWorkspaceNotFound, "workspace not found", nil,
		)
	}
	if s.workspaceDiffCache != nil {
		s.workspaceDiffCache.RevalidateWorkspace(input.ID)
	}

	provider := strings.TrimSpace(summary.Platform)
	if provider == "" {
		provider = string(platform.KindGitHub)
	}
	repo, err := s.lookupRepoByProviderRoute(
		ctx, provider, summary.PlatformHost, summary.RepoOwner, summary.RepoName,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	kind := repoProviderKind(*repo)
	host := repoProviderHost(*repo)

	switch summary.ItemType {
	case db.WorkspaceItemTypeIssue:
		if err := s.refreshWorkspaceIssue(
			ctx, kind, host, repo.Owner, repo.Name, summary.ItemNumber,
		); err != nil {
			return nil, err
		}
	case db.WorkspaceItemTypePullRequest:
		// The PR detail sync runs after the repo index refresh below so the
		// workspace response reflects the latest indexed PR row and diff.
	case db.WorkspaceItemTypeKataTask:
		// Kata tasks are not provider issues. The live task pane refreshes
		// through the Kata daemon; this route only refreshes the mapped repo.
	case db.WorkspaceItemTypeAdHoc:
		// Ad-hoc workspaces have no source item to refresh. Only the mapped
		// repo index and any PR later detected for the branch matter.
	default:
		return nil, httpapi.Internal("workspace has unsupported item type")
	}

	if err := s.refreshWorkspaceRepoIndex(
		ctx, kind, host, repo.Owner, repo.Name,
	); err != nil {
		return nil, err
	}

	if s.workspacePRMonitor != nil {
		update, changed, err := s.workspacePRMonitor.RefreshWorkspaceAssociation(
			ctx, input.ID,
		)
		if err != nil {
			return nil, httpapi.Internal("refresh workspace PR association: " + err.Error())
		}
		if changed {
			s.broadcastWorkspaceStatus(update.WorkspaceID)
			s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
		}
	}

	refreshed, err := s.workspaces.GetSummary(ctx, input.ID)
	if err != nil {
		return nil, httpapi.Internal("get workspace failed")
	}
	if refreshed == nil {
		return nil, httpapi.NotFound(
			httpapi.CodeWorkspaceNotFound, "workspace not found", nil,
		)
	}
	if prNumber, ok := workspaceAssociatedPRNumber(refreshed); ok {
		if err := s.refreshWorkspacePullRequest(
			ctx, kind, host, repo.Owner, repo.Name, prNumber,
		); err != nil {
			return nil, err
		}
		refreshed, err = s.workspaces.GetSummary(ctx, input.ID)
		if err != nil {
			return nil, httpapi.Internal("get workspace failed")
		}
		if refreshed == nil {
			return nil, httpapi.NotFound(
				httpapi.CodeWorkspaceNotFound, "workspace not found", nil,
			)
		}
	}

	resp := s.refreshWorkspaceResponse(ctx, refreshed)
	s.hub.Broadcast(Event{Type: "workspace_status", Data: resp})
	s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	return &refreshWorkspaceOutput{Body: resp}, nil
}

func workspaceAssociatedPRNumber(summary *db.WorkspaceSummary) (int, bool) {
	if summary == nil {
		return 0, false
	}
	if summary.ItemType == db.WorkspaceItemTypePullRequest {
		return summary.ItemNumber, summary.ItemNumber > 0
	}
	if summary.AssociatedPRNumber == nil {
		return 0, false
	}
	return *summary.AssociatedPRNumber, *summary.AssociatedPRNumber > 0
}

func (s *Handler) refreshWorkspaceRepoIndex(
	ctx context.Context,
	kind platform.Kind,
	host, owner, name string,
) error {
	err := s.syncer.SyncRepoOnProvider(ctx, kind, host, owner, name)
	if err == nil {
		return nil
	}
	// An issue-only partial failure is already recorded in repo sync
	// health, and the workspace flow depends on merge-request data, so the
	// refresh proceeds to association inspection and the targeted
	// PR-detail refresh. Partial failures touching the merge-request
	// scope (and hard failures) still abort: succeeding would return
	// stale association data for a PR that failed to sync.
	if partial, ok := ghclient.ExclusivePartialSyncFailure(err); ok && !partial.MergeRequests {
		slog.Warn("workspace refresh: issue sync partially failed",
			"owner", owner, "name", name, "err", err)
		return nil
	}
	if strings.Contains(err.Error(), "is not tracked") {
		return httpapi.Forbidden(err.Error(), nil)
	}
	return httpapi.ProviderCallProblemWithDetail(
		err, string(kind), host, "sync repo: "+err.Error(),
	)
}

func (s *Handler) refreshWorkspaceIssue(
	ctx context.Context,
	kind platform.Kind,
	host, owner, name string,
	number int,
) error {
	err := s.syncer.SyncIssueOnProvider(ctx, kind, host, owner, name, number)
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "is not tracked") {
		return httpapi.Forbidden(err.Error(), nil)
	}
	return httpapi.ProviderCallProblemWithDetail(
		err, string(kind), host, "sync issue: "+err.Error(),
	)
}

func (s *Handler) refreshWorkspacePullRequest(
	ctx context.Context,
	kind platform.Kind,
	host, owner, name string,
	number int,
) error {
	var diffErr *ghclient.DiffSyncError
	err := s.syncer.SyncMROnProvider(ctx, kind, host, owner, name, number)
	if err != nil && !errors.As(err, &diffErr) {
		if strings.Contains(err.Error(), "is not tracked") {
			return httpapi.Forbidden(err.Error(), nil)
		}
		return httpapi.ProviderCallProblemWithDetail(
			err, string(kind), host, "sync PR: "+err.Error(),
		)
	}
	if diffErr != nil {
		slog.Warn("diff sync failed during workspace refresh",
			"owner", owner,
			"name", name,
			"number", number,
			"code", diffErr.Code,
			"err", diffErr.Err,
		)
	}
	return nil
}

func (s *Handler) getWorkspaceCommits(
	ctx context.Context, input *getWorkspaceCommitsInput,
) (*getWorkspaceCommitsOutput, error) {
	req, err := s.workspaceDiffRequest(ctx, input.ID, "")
	if err != nil {
		return nil, err
	}

	commits, ok, err := s.workspaceCommits(ctx, req)
	if err != nil {
		slog.Error(
			"failed to list workspace commits",
			"workspace_id", input.ID,
			"err", err,
		)
		return nil, httpapi.Upstream("failed to list workspace commits", "", "")
	}
	if !ok {
		return nil, httpapi.NotFound(httpapi.CodeNotFound,
			"commits not available for this workspace", nil)
	}

	// Annotate each commit with whether it has reached the branch's upstream so
	// the UI can flag local-only commits. Push status is an enhancement over the
	// commit list, so a probe failure degrades to omitting the flag rather than
	// failing the request. When the branch has no upstream we cannot tell pushed
	// from unpushed (a fork PR head has no upstream yet already exists on its
	// remote), so the flag is omitted rather than guessed.
	unpushed, hasUpstream, pushErr := workspace.WorktreeUnpushedSHAs(
		ctx, req.Summary.WorktreePath,
	)
	if pushErr != nil {
		slog.Warn(
			"failed to determine unpushed workspace commits",
			"workspace_id", input.ID,
			"err", pushErr,
		)
	}

	resp := commitsResponse{Commits: make([]commitResponse, len(commits))}
	for i, c := range commits {
		cr := commitResponse{
			SHA:        c.SHA,
			Message:    c.Message,
			AuthorName: c.AuthorName,
			AuthoredAt: c.AuthoredAt.UTC(),
		}
		if pushErr == nil && hasUpstream {
			_, isUnpushed := unpushed[c.SHA]
			pushed := !isUnpushed
			cr.Pushed = &pushed
		}
		resp.Commits[i] = cr
	}
	return &getWorkspaceCommitsOutput{Body: resp}, nil
}

func (s *Handler) getWorkspaceFiles(
	ctx context.Context, input *getWorkspaceFilesInput,
) (*getWorkspaceFilesOutput, error) {
	req, err := s.workspaceDiffRequest(ctx, input.ID, input.Base)
	if err != nil {
		return nil, err
	}
	if err := s.applyWorkspaceDiffScope(
		ctx, &req, input.Commit, input.From, input.To,
	); err != nil {
		return nil, err
	}

	hideWhitespace := input.Whitespace == "hide"
	snapshot, _, diffErr := s.workspaceDiffCache.Get(
		ctx, s.workspaceDiffCacheKey(req, hideWhitespace),
	)
	if diffErr != nil {
		if errors.Is(diffErr, errWorkspaceDiffBaseUnavailable) {
			return nil, workspaceDiffBaseUnavailable(req.Base)
		}
		slog.Error(
			"failed to list workspace diff files",
			"workspace_id", input.ID,
			"base", req.Base,
			"err", diffErr,
		)
		return nil, httpapi.Upstream("failed to list workspace files", "", "")
	}
	return &getWorkspaceFilesOutput{Body: filesResponse{
		Stale:               snapshot.Diff.Stale,
		WhitespaceOnlyCount: snapshot.Diff.WhitespaceOnlyCount,
		Files:               snapshot.Files,
		SnapshotVersion:     snapshot.Version,
	}}, nil
}

const workspaceDiffWatchTimeout = 25 * time.Second

func (s *Handler) watchWorkspaceDiff(
	ctx context.Context,
	input *watchWorkspaceDiffInput,
) (*watchWorkspaceDiffOutput, error) {
	req, err := s.workspaceDiffRequest(ctx, input.ID, string(workspace.WorktreeDiffBaseHead))
	if err != nil {
		return nil, err
	}
	key := s.workspaceDiffCacheKey(req, false)
	events, hubDone := s.hub.Subscribe(ctx, false)
	releaseSelection := s.workspaceDiffCache.Select(
		input.ID,
		func(context.Context) (workspaceDiffLogicalKey, error) {
			return key, nil
		},
	)
	defer releaseSelection()

	snapshot, _, err := s.workspaceDiffCache.Get(ctx, key)
	if err != nil {
		if errors.Is(err, errWorkspaceDiffBaseUnavailable) {
			return nil, workspaceDiffBaseUnavailable(req.Base)
		}
		return nil, httpapi.Upstream("failed to prepare selected workspace diff", "", "")
	}
	if input.Version == "" || input.Version != snapshot.Version {
		return &watchWorkspaceDiffOutput{Body: workspaceDiffWatchResponse{
			Changed: true,
			Version: snapshot.Version,
		}}, nil
	}

	timer := time.NewTimer(workspaceDiffWatchTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-hubDone:
			return &watchWorkspaceDiffOutput{Body: workspaceDiffWatchResponse{
				Version: snapshot.Version,
			}}, nil
		case <-timer.C:
			return &watchWorkspaceDiffOutput{Body: workspaceDiffWatchResponse{
				Version: snapshot.Version,
			}}, nil
		case event, ok := <-events:
			if !ok {
				return &watchWorkspaceDiffOutput{Body: workspaceDiffWatchResponse{
					Version: snapshot.Version,
				}}, nil
			}
			if event.Type != "workspace_diff_ready" && event.Type != "workspace_diff_changed" {
				continue
			}
			data, ok := event.Data.(workspaceDiffEventData)
			if !ok || data.WorkspaceID != input.ID {
				continue
			}
			current, _, getErr := s.workspaceDiffCache.Get(ctx, key)
			if getErr != nil {
				return nil, httpapi.Upstream("failed to read selected workspace diff", "", "")
			}
			if current.Version == input.Version {
				continue
			}
			return &watchWorkspaceDiffOutput{Body: workspaceDiffWatchResponse{
				Changed: true,
				Version: current.Version,
			}}, nil
		}
	}
}

func (s *Handler) getWorkspaceDiff(
	ctx context.Context, input *getWorkspaceDiffInput,
) (*getWorkspaceDiffOutput, error) {
	req, err := s.workspaceDiffRequest(ctx, input.ID, input.Base)
	if err != nil {
		return nil, err
	}
	if err := s.applyWorkspaceDiffScope(
		ctx, &req, input.Commit, input.From, input.To,
	); err != nil {
		return nil, err
	}

	hideWhitespace := input.Whitespace == "hide"
	snapshot, _, diffErr := s.workspaceDiffCache.Get(
		ctx, s.workspaceDiffCacheKey(req, hideWhitespace),
	)
	if diffErr != nil {
		if errors.Is(diffErr, errWorkspaceDiffBaseUnavailable) {
			return nil, workspaceDiffBaseUnavailable(req.Base)
		}
		slog.Error(
			"failed to compute workspace diff",
			"workspace_id", input.ID,
			"base", req.Base,
			"err", diffErr,
		)
		return nil, httpapi.Upstream("failed to compute workspace diff", "", "")
	}
	if input.Revision != "" && input.Revision != snapshot.Version {
		return nil, httpapi.Conflict(
			httpapi.CodeConflict,
			"workspace diff snapshot changed; reload the file list",
			map[string]any{"reason": "snapshot_changed", "snapshot_version": snapshot.Version},
		)
	}
	files := snapshot.Diff.Files
	if input.Path != "" {
		files = filterWorkspaceDiffSnapshotPath(files, input.Path)
	}
	return &getWorkspaceDiffOutput{Body: diffResponse{
		Stale:               snapshot.Diff.Stale,
		WhitespaceOnlyCount: snapshot.Diff.WhitespaceOnlyCount,
		Files:               files,
		SnapshotVersion:     snapshot.Version,
	}}, nil
}

func (s *Handler) getWorkspaceFilePreview(
	ctx context.Context,
	input *getWorkspaceFilePreviewInput,
) (*getWorkspaceFilePreviewOutput, error) {
	if strings.TrimSpace(input.Path) == "" {
		return nil, httpapi.Validation("query.path", "path is required")
	}
	side := strings.TrimSpace(input.Side)
	if side != "" && side != "old" && side != "new" {
		return nil, httpapi.Validation("query.side", "side must be old or new")
	}

	req, err := s.workspaceDiffRequest(ctx, input.ID, input.Base)
	if err != nil {
		return nil, err
	}
	if err := s.applyWorkspaceDiffScope(
		ctx, &req, input.Commit, input.From, input.To,
	); err != nil {
		return nil, err
	}

	hideWhitespace := input.Whitespace == "hide"
	snapshot, _, err := s.workspaceDiffCache.Get(
		ctx, s.workspaceDiffCacheKey(req, hideWhitespace),
	)
	var content *gitclone.FileContent
	if err == nil {
		if input.Revision != "" && input.Revision != snapshot.Version {
			return nil, httpapi.Conflict(
				httpapi.CodeConflict,
				"workspace diff snapshot changed; reload the file list",
				map[string]any{"reason": "snapshot_changed", "snapshot_version": snapshot.Version},
			)
		}
		matching := filterWorkspaceDiffSnapshotPath(snapshot.Diff.Files, input.Path)
		if len(matching) == 0 {
			err = gitclone.ErrNotFound
		} else {
			content, err = workspace.ReadDiffSnapshotFile(
				ctx, snapshot.Resolved, matching[0], side, maxFilePreviewBytes,
			)
		}
	}
	if err != nil {
		if errors.Is(err, errWorkspaceDiffBaseUnavailable) {
			return nil, workspaceDiffBaseUnavailable(req.Base)
		}
		if errors.Is(err, gitclone.ErrNotFound) {
			return nil, httpapi.NotFound(httpapi.CodeNotFound, "workspace file preview not available: file is not changed in this diff", nil)
		}
		if errors.Is(err, gitclone.ErrTooLarge) {
			return nil, httpapi.PayloadTooLarge("file preview is too large", maxFilePreviewBytes)
		}
		slog.Error(
			"failed to read workspace file preview",
			"workspace_id", input.ID,
			"base", req.Base,
			"path", input.Path,
			"err", err,
		)
		return nil, httpapi.Upstream("failed to read workspace file preview", "", "")
	}
	if content == nil {
		return nil, httpapi.Upstream("failed to read workspace file preview", "", "")
	}
	return &getWorkspaceFilePreviewOutput{Body: filePreviewResponse{
		Path:      content.Path,
		MediaType: previewMediaType(content.Path, content.Data),
		Encoding:  "base64",
		Content:   base64.StdEncoding.EncodeToString(content.Data),
		Size:      content.Size,
	}}, nil
}

func (s *Handler) workspaceDiffRequest(
	ctx context.Context,
	id string,
	baseInput string,
) (workspaceDiffRequest, error) {
	if s.workspaces == nil {
		return workspaceDiffRequest{}, httpapi.ServiceUnavailable(
			"workspace manager not configured",
		)
	}

	summary, err := s.workspaces.GetSummary(ctx, id)
	if err != nil {
		return workspaceDiffRequest{}, httpapi.Internal("get workspace failed")
	}
	if summary == nil {
		return workspaceDiffRequest{}, httpapi.NotFound(
			httpapi.CodeWorkspaceNotFound, "workspace not found", nil,
		)
	}
	if summary.Status != "ready" {
		return workspaceDiffRequest{}, httpapi.Conflict(
			httpapi.CodeConflict, "workspace is not ready", nil,
		)
	}

	base := workspace.WorktreeDiffBase(baseInput)
	if base == "" {
		base = workspace.WorktreeDiffBaseHead
	}
	switch base {
	case workspace.WorktreeDiffBaseHead, workspace.WorktreeDiffBasePushed:
		return workspaceDiffRequest{Summary: summary, Base: base}, nil
	case workspace.WorktreeDiffBaseMergeTarget:
		targetBranch, ok, err := s.workspaceMergeTargetBranch(ctx, summary)
		if err != nil {
			return workspaceDiffRequest{}, err
		}
		if !ok {
			return workspaceDiffRequest{}, workspaceDiffBaseUnavailable(base)
		}
		return workspaceDiffRequest{
			Summary:           summary,
			Base:              base,
			MergeTargetBranch: targetBranch,
		}, nil
	default:
		return workspaceDiffRequest{}, httpapi.Validation(
			"query.base",
			"base must be head, pushed, or merge-target",
			"head", "pushed", "merge-target",
		)
	}
}

func (s *Handler) workspaceDiffCacheKey(
	req workspaceDiffRequest,
	hideWhitespace bool,
) workspaceDiffLogicalKey {
	return workspaceDiffLogicalKey{
		WorkspaceID: req.Summary.ID,
		Spec: workspace.DiffSnapshotSpec{
			WorktreePath:      req.Summary.WorktreePath,
			Base:              req.Base,
			MergeTargetBranch: req.MergeTargetBranch,
			FromSHA:           req.FromSHA,
			ToSHA:             req.ToSHA,
			HideWhitespace:    hideWhitespace,
		},
	}
}

// SelectWorkspaceDiff holds the selected-workspace refresh lease used by the
// root SSE stream until the returned release function is called.
func (s *Handler) SelectWorkspaceDiff(workspaceID string) func() {
	if s == nil || s.workspaceDiffCache == nil || workspaceID == "" {
		return func() {}
	}
	return s.workspaceDiffCache.Select(
		workspaceID,
		func(ctx context.Context) (workspaceDiffLogicalKey, error) {
			req, err := s.workspaceDiffRequest(
				ctx, workspaceID, string(workspace.WorktreeDiffBaseHead),
			)
			if err != nil {
				return workspaceDiffLogicalKey{}, err
			}
			return s.workspaceDiffCacheKey(req, false), nil
		},
	)
}

func filterWorkspaceDiffSnapshotPath(
	files []gitclone.DiffFile,
	path string,
) []gitclone.DiffFile {
	for i := range files {
		if files[i].Path == path {
			return []gitclone.DiffFile{files[i]}
		}
	}
	for i := range files {
		if files[i].OldPath == path {
			return []gitclone.DiffFile{files[i]}
		}
	}
	return []gitclone.DiffFile{}
}

func (s *Handler) workspaceCommits(
	ctx context.Context,
	req workspaceDiffRequest,
) ([]gitclone.Commit, bool, error) {
	targetBranch, ok, err := s.workspaceMergeTargetBranch(ctx, req.Summary)
	if err != nil || !ok {
		return nil, ok, err
	}
	return workspace.WorktreeCommitsAgainstMergeTarget(
		ctx,
		req.Summary.WorktreePath,
		targetBranch,
	)
}

func (s *Handler) applyWorkspaceDiffScope(
	ctx context.Context,
	req *workspaceDiffRequest,
	commit string,
	from string,
	to string,
) error {
	hasCommit := commit != ""
	hasFrom := from != ""
	hasTo := to != ""

	switch {
	case !hasCommit && !hasFrom && !hasTo:
		return nil

	case hasCommit && !hasFrom && !hasTo:
		if _, err := s.validateWorkspaceSHAs(ctx, *req, commit); err != nil {
			return err
		}
		parent, err := workspace.WorktreeParentOf(
			ctx, req.Summary.WorktreePath, commit,
		)
		if err != nil {
			return httpapi.Internal("failed to resolve parent: " + err.Error())
		}
		req.FromSHA = parent
		req.ToSHA = commit
		return nil

	case !hasCommit && hasFrom && hasTo:
		indexMap, err := s.validateWorkspaceSHAs(ctx, *req, from, to)
		if err != nil {
			return err
		}
		if indexMap[from] < indexMap[to] {
			return httpapi.Validation("query",
				"invalid range: 'from' must be older than or equal to 'to'",
			)
		}
		parent, err := workspace.WorktreeParentOf(
			ctx, req.Summary.WorktreePath, from,
		)
		if err != nil {
			return httpapi.Internal("failed to resolve parent: " + err.Error())
		}
		req.FromSHA = parent
		req.ToSHA = to
		return nil

	default:
		return httpapi.Validation("query",
			"invalid scope: use 'commit' alone or 'from'+'to' together",
		)
	}
}

func (s *Handler) validateWorkspaceSHAs(
	ctx context.Context,
	req workspaceDiffRequest,
	shas ...string,
) (map[string]int, error) {
	commits, ok, err := s.workspaceCommits(ctx, req)
	if err != nil {
		return nil, httpapi.Upstream(
			"failed to list workspace commits: "+err.Error(), "", "",
		)
	}
	if !ok {
		return nil, httpapi.NotFound(httpapi.CodeNotFound,
			"commits not available for this workspace", nil)
	}
	indexMap := make(map[string]int, len(commits))
	for i, c := range commits {
		indexMap[c.SHA] = i
	}
	for _, sha := range shas {
		if _, ok := indexMap[sha]; !ok {
			return nil, httpapi.Validation("query",
				"invalid scope: commit is not in this workspace branch",
			)
		}
	}
	return indexMap, nil
}

func (s *Handler) workspaceMergeTargetBranch(
	ctx context.Context,
	summary *db.WorkspaceSummary,
) (string, bool, error) {
	prNumber := summary.ItemNumber
	if summary.ItemType != db.WorkspaceItemTypePullRequest {
		if summary.AssociatedPRNumber == nil {
			return "", false, nil
		}
		prNumber = *summary.AssociatedPRNumber
	}
	if prNumber <= 0 {
		return "", false, nil
	}

	repo, err := s.db.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     summary.Platform,
		PlatformHost: summary.PlatformHost,
		Owner:        summary.RepoOwner,
		Name:         summary.RepoName,
	})
	if err != nil {
		return "", false, httpapi.Internal("get workspace repo failed")
	}
	if repo == nil {
		return "", false, nil
	}

	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repo.ID, prNumber,
	)
	if err != nil {
		return "", false, httpapi.Internal("get workspace pull request failed")
	}
	if mr == nil || strings.TrimSpace(mr.BaseBranch) == "" {
		return "", false, nil
	}
	return strings.TrimSpace(mr.BaseBranch), true, nil
}

func workspaceDiffBaseUnavailable(
	base workspace.WorktreeDiffBase,
) error {
	if base == workspace.WorktreeDiffBaseMergeTarget {
		return httpapi.NotFound(httpapi.CodeNotFound,
			"workspace merge target branch not available", nil)
	}
	return httpapi.NotFound(httpapi.CodeNotFound,
		"workspace pushed branch not available", nil)
}

func (s *Handler) retryWorkspace(
	ctx context.Context, input *retryWorkspaceInput,
) (*createWorkspaceOutput, error) {
	if s.workspaces == nil {
		return nil, httpapi.ServiceUnavailable("workspace manager not configured")
	}

	ws, startNow, err := s.workspaces.RequestRetry(ctx, input.ID)
	if err != nil {
		if errors.Is(err, workspace.ErrWorkspaceNotFound) {
			return nil, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, err.Error(), nil)
		}
		if errors.Is(err, workspace.ErrWorkspaceInvalidState) {
			return nil, httpapi.Conflict(httpapi.CodeConflict, err.Error(), nil)
		}
		return nil, httpapi.Internal("retry workspace: " + err.Error())
	}
	s.invalidateWorkspaceEnrichment(ws.ID)

	if startNow {
		s.runWorkspaceSetup(ws)
	}

	summary, err := s.workspaces.GetSummary(ctx, ws.ID)
	if err != nil {
		return nil, httpapi.Internal("get workspace summary: " + err.Error())
	}
	if summary == nil {
		return nil, httpapi.Internal("workspace summary missing after retry")
	}
	resp := s.toWorkspaceResponse(ctx, summary)
	s.hub.Broadcast(Event{
		Type: "workspace_status",
		Data: resp,
	})
	return &createWorkspaceOutput{
		Status: http.StatusAccepted,
		Body:   resp,
	}, nil
}

func (s *Handler) toWorkspaceResponse(
	ctx context.Context,
	summary *db.WorkspaceSummary,
) workspaceResponse {
	resp := s.workspaceResponseWithEnrichment(ctx, summary).response
	s.applyAgentActivity(&resp, summary)
	return resp
}

func (s *Handler) applyAgentActivity(
	resp *workspaceResponse,
	summary *db.WorkspaceSummary,
) {
	if s.agentActivity == nil || s.runtime == nil || resp == nil || summary == nil {
		return
	}
	liveSessionKeys := make([]string, 0)
	for _, session := range s.runtime.ListSessions(summary.ID) {
		if session.Kind == localruntime.LaunchTargetAgent &&
			(session.Status == localruntime.SessionStatusRunning ||
				session.Status == localruntime.SessionStatusStarting) {
			liveSessionKeys = append(liveSessionKeys, session.Key)
		}
	}
	snapshot, ok := s.agentActivity.SnapshotForWorkspace(
		summary.WorktreePath, liveSessionKeys,
	)
	if !ok {
		return
	}
	state := string(snapshot.State)
	updatedAt := snapshot.UpdatedAt.UTC().Format(time.RFC3339)
	resp.AgentState = &state
	resp.AgentStateUpdatedAt = &updatedAt
}

// Response returns the cached public DTO for a persisted workspace summary.
func (s *Handler) Response(
	ctx context.Context, summary *db.WorkspaceSummary,
) WorkspaceResponse {
	return s.toWorkspaceResponse(ctx, summary)
}

func (s *Handler) workspaceResponseWithEnrichment(
	ctx context.Context,
	summary *db.WorkspaceSummary,
) workspaceEnrichmentProbeResult {
	resp := toWorkspaceResponse(summary)
	resp.Repo = s.repoRefFromParts(
		summary.Platform, summary.PlatformHost, summary.RepoOwner, summary.RepoName,
	)
	if s.workspaces == nil ||
		summary.Status != "ready" {
		return workspaceEnrichmentProbeResult{response: resp}
	}

	divergenceErr := applyWorktreeDivergence(ctx, &resp, summary.WorktreePath)
	sessions, sessionsErr := s.workspaceTmuxActivitySessions(ctx, summary)
	activity, hasActivity, activityErr := s.probeWorkspaceTmuxActivity(
		ctx, summary, sessions,
	)
	if hasActivity {
		applyTmuxActivity(&resp, activity)
	}
	err := errors.Join(divergenceErr, sessionsErr, activityErr)
	result := workspaceEnrichmentProbeResult{
		response:           resp,
		divergenceComplete: divergenceErr == nil,
		tmuxComplete:       sessionsErr == nil && activityErr == nil,
		err:                err,
	}
	if err != nil {
		resp.EnrichmentStatus = workspaceEnrichmentFailed
		message := err.Error()
		resp.EnrichmentError = &message
		result.response = resp
		return result
	}
	resp.EnrichmentStatus = workspaceEnrichmentFresh
	refreshedAt := s.now().UTC().Format(time.RFC3339)
	resp.EnrichmentRefreshedAt = &refreshedAt
	result.response = resp
	return result
}

func (s *Handler) workspaceTmuxActivitySessions(
	ctx context.Context,
	summary *db.WorkspaceSummary,
) ([]string, error) {
	sessions := make([]string, 0, 1)
	seen := map[string]bool{}
	var listErr error
	if s.workspaces != nil {
		stored, err := s.workspaces.TmuxSessionsForWorkspace(
			ctx, summary.ID, summary.TmuxSession,
		)
		if err != nil {
			listErr = err
			slog.Debug(
				"list workspace tmux sessions",
				"workspace_id", summary.ID,
				"tmux_session", summary.TmuxSession,
				"err", err,
			)
		}
		for _, session := range stored {
			if session == "" || seen[session] {
				continue
			}
			sessions = append(sessions, session)
			seen[session] = true
		}
	}
	if summary.TmuxSession != "" && !seen[summary.TmuxSession] {
		sessions = append(sessions, summary.TmuxSession)
		seen[summary.TmuxSession] = true
	}
	if s.runtime == nil {
		return sessions, listErr
	}
	for _, session := range s.runtime.TmuxSessions(summary.ID) {
		if session == "" || seen[session] {
			continue
		}
		sessions = append(sessions, session)
		seen[session] = true
	}
	return sessions, listErr
}

func (s *Handler) probeWorkspaceTmuxActivity(
	ctx context.Context,
	summary *db.WorkspaceSummary,
	sessions []string,
) (tmuxActivityResult, bool, error) {
	if len(sessions) == 0 {
		return tmuxActivityResult{}, false, nil
	}
	tracker := s.tmuxActivity
	if tracker == nil {
		tracker = newTmuxActivityTracker(nil)
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, tmuxActivityProbeTimeout)
	defer cancelProbe()

	results := make([]tmuxActivityResult, 0, len(sessions))
	var probeErrs []error
	for _, session := range sessions {
		if s.tmuxActivity != nil {
			if result, ok := tracker.Cached(session); ok {
				results = append(results, result)
				continue
			}
		}
		result, ok, err := s.probeOneTmuxSession(
			probeCtx, tracker, summary, session,
		)
		if ok {
			results = append(results, result)
		}
		if err != nil {
			probeErrs = append(probeErrs, err)
		}
	}
	result, ok := mergeTmuxActivityResults(results)
	return result, ok, errors.Join(probeErrs...)
}

func (s *Handler) probeOneTmuxSession(
	ctx context.Context,
	tracker *tmuxActivityTracker,
	summary *db.WorkspaceSummary,
	session string,
) (tmuxActivityResult, bool, error) {
	probe := tracker.StartProbe(ctx, session)
	if !probe.Started {
		if probe.Wait != nil {
			select {
			case <-probe.Wait:
				result, ok := tracker.Cached(session)
				if !ok {
					if probe.HasFallback {
						return probe.Fallback, true,
							errors.New("tmux activity probe produced no sample")
					}
					return tmuxActivityResult{}, false,
						errors.New("tmux activity probe produced no sample")
				}
				return result, true, nil
			case <-ctx.Done():
				if probe.HasFallback {
					return probe.Fallback, true, ctx.Err()
				}
			}
		}
		if probe.HasFallback {
			err := ctx.Err()
			if err == nil {
				err = errors.New("tmux activity probe unavailable")
			}
			return probe.Fallback, true, err
		}
		return tmuxActivityResult{}, false, ctx.Err()
	}

	snapshot, err := s.workspaces.TerminalPaneSnapshot(
		ctx, &summary.Workspace, session,
	)
	if err != nil {
		probe.Probe.Cancel()
		slog.Debug(
			"read tmux pane snapshot",
			"workspace_id", summary.ID,
			"tmux_session", session,
			"err", err,
		)
		if probe.HasFallback {
			return probe.Fallback, true, err
		}
		return tmuxActivityResult{}, false, err
	}

	return probe.Probe.Finish(tmuxActivityObservation{
		PaneTitle: snapshot.Title,
		Output:    snapshot.Output,
		HasOutput: true,
	}), true, nil
}

func applyTmuxActivity(resp *workspaceResponse, activity tmuxActivityResult) {
	if activity.PaneTitle != "" {
		title := activity.PaneTitle
		resp.TmuxPaneTitle = &title
	}
	resp.TmuxWorking = activity.Working
	resp.TmuxActivitySource = activity.Source
	if activity.LastOutputAt != nil {
		lastOutputAt := activity.LastOutputAt.UTC().Format(time.RFC3339)
		resp.TmuxLastOutputAt = &lastOutputAt
	}
}

// worktreeDivergenceTimeout caps how long a single workspace's
// rev-list probe can run before the workspace list response moves
// on. Picked to be small enough that a stalled git won't hold up
// the whole list (probes already run in parallel).
const worktreeDivergenceTimeout = 750 * time.Millisecond

func applyWorktreeDivergence(
	ctx context.Context,
	resp *workspaceResponse,
	worktreePath string,
) error {
	if worktreePath == "" {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, worktreeDivergenceTimeout)
	defer cancel()

	div, ok, err := workspace.WorktreeDivergence(probeCtx, worktreePath)
	if err != nil {
		slog.Debug(
			"worktree divergence probe failed",
			"workspace_id", resp.ID,
			"path", worktreePath,
			"err", err,
		)
		return err
	}
	if !ok {
		return nil
	}
	ahead := div.Ahead
	behind := div.Behind
	resp.CommitsAhead = &ahead
	resp.CommitsBehind = &behind
	return nil
}

func isWorkingTmuxTitle(title string) bool {
	normalized := strings.TrimSpace(title)
	if normalized == "" {
		return false
	}

	for _, frame := range "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏" {
		if strings.HasPrefix(normalized, string(frame)+" ") {
			return true
		}
	}

	return false
}

func (s *Handler) getWorkspaceRuntime(
	ctx context.Context,
	input *getWorkspaceRuntimeInput,
) (*getWorkspaceRuntimeOutput, error) {
	summary, err := s.getRuntimeWorkspace(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	sessions, err := s.workspaceRuntimeSessions(ctx, summary.ID)
	if err != nil {
		return nil, httpapi.Internal("list runtime sessions: " + err.Error())
	}

	return &getWorkspaceRuntimeOutput{
		Body: workspaceRuntimeResponse{
			LaunchTargets: s.runtime.LaunchTargets(),
			Sessions:      sessions,
		},
	}, nil
}

func (s *Handler) workspaceRuntimeSessions(
	ctx context.Context,
	workspaceID string,
) ([]localruntime.SessionInfo, error) {
	sessions := s.runtime.ListSessions(workspaceID)
	if s.workspaces == nil {
		return sessions, nil
	}
	stored, err := s.workspaces.RuntimeSessionsForWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return mergeStoredRuntimeSessions(sessions, stored), nil
}

func mergeStoredRuntimeSessions(
	live []localruntime.SessionInfo,
	stored []db.WorkspaceRuntimeSession,
) []localruntime.SessionInfo {
	sessions := slices.Clone(live)
	storedByKey := make(map[string]db.WorkspaceRuntimeSession, len(stored))
	for _, session := range stored {
		if session.SessionKey != "" {
			storedByKey[session.SessionKey] = session
		}
	}
	seen := make(map[string]struct{}, len(sessions))
	for i, session := range sessions {
		seen[session.Key] = struct{}{}
		if storedSession, ok := storedByKey[session.Key]; ok {
			sessions[i].DisplayRegion = normalizeRuntimeDisplayRegion(
				storedSession.DisplayRegion,
				session,
			)
		} else {
			sessions[i].DisplayRegion = normalizeRuntimeDisplayRegion("", session)
		}
	}
	for _, session := range stored {
		if session.SessionKey == "" {
			continue
		}
		if _, ok := seen[session.SessionKey]; ok {
			continue
		}
		sessions = append(sessions, storedRuntimeSessionInfo(session))
		seen[session.SessionKey] = struct{}{}
	}
	slices.SortFunc(sessions, localruntime.SessionInfo.Compare)
	return sessions
}

func storedRuntimeSessionInfo(
	session db.WorkspaceRuntimeSession,
) localruntime.SessionInfo {
	targetKey := strings.TrimSpace(session.TargetKey)
	kind := localruntime.LaunchTargetKind(strings.TrimSpace(session.Kind))
	if kind == "" {
		kind = localruntime.LaunchTargetAgent
	}
	if targetKey == string(localruntime.LaunchTargetPlainShell) ||
		kind == localruntime.LaunchTargetPlainShell {
		targetKey = string(localruntime.LaunchTargetPlainShell)
		kind = localruntime.LaunchTargetPlainShell
	}
	label := strings.TrimSpace(session.Label)
	if label == "" {
		if kind == localruntime.LaunchTargetPlainShell {
			label = "Shell"
		} else {
			label = targetKey
		}
	}
	return localruntime.SessionInfo{
		Key:         session.SessionKey,
		WorkspaceID: session.WorkspaceID,
		TargetKey:   targetKey,
		Label:       label,
		Kind:        kind,
		Status:      localruntime.SessionStatusError,
		DisplayRegion: normalizeRuntimeDisplayRegion(session.DisplayRegion, localruntime.SessionInfo{
			TargetKey: targetKey,
			Kind:      kind,
		}),
		CreatedAt:   session.CreatedAt,
		TmuxSession: session.TmuxSession,
	}
}

func (s *Handler) launchWorkspaceRuntimeSession(
	ctx context.Context,
	input *launchWorkspaceRuntimeSessionInput,
) (*workspaceRuntimeSessionOutput, error) {
	summary, err := s.getReadyRuntimeWorkspace(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	targetKey := strings.TrimSpace(input.Body.TargetKey)
	if targetKey == "" {
		return nil, httpapi.Validation("body.target_key", "target_key is required")
	}
	if workspaceRuntimeTargetIsAgent(s.runtime, targetKey) {
		if err := s.workspaces.PrepareAgentLaunchContext(
			ctx,
			workspace.PrepareAgentLaunchContextOptions{
				WorkspaceID: summary.ID,
				TargetKey:   targetKey,
			},
		); err != nil {
			return nil, httpapi.Internal("prepare agent context: " + err.Error())
		}
	}

	session, err := s.runtime.Launch(
		ctx, summary.ID, summary.WorktreePath, targetKey,
	)
	if err != nil {
		return nil, workspaceRuntimeLaunchError(err)
	}
	session.DisplayRegion = normalizeRuntimeDisplayRegion(
		input.Body.DisplayRegion,
		session,
	)
	if err := s.recordRuntimeSession(
		ctx, summary.ID, session, "session",
	); err != nil {
		cleanupCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), 5*time.Second,
		)
		defer cancel()
		_ = s.runtime.Stop(cleanupCtx, summary.ID, session.Key)
		return nil, err
	}
	s.invalidateWorkspaceEnrichment(summary.ID)
	s.forgetRecordedRuntimeSessionIfExited(ctx, session)
	return &workspaceRuntimeSessionOutput{Body: session}, nil
}

// LaunchWorkspaceRuntimeSession launches a configured workspace target.
func (s *Handler) LaunchWorkspaceRuntimeSession(
	ctx context.Context, input *LaunchWorkspaceRuntimeSessionInput,
) (*workspaceRuntimeSessionOutput, error) {
	return s.launchWorkspaceRuntimeSession(ctx, input)
}

func workspaceRuntimeTargetIsAgent(runtime *localruntime.Manager, targetKey string) bool {
	if runtime == nil {
		return false
	}
	for _, target := range runtime.LaunchTargets() {
		if target.Key == targetKey {
			return target.Kind == localruntime.LaunchTargetAgent && target.Available
		}
	}
	return false
}

func (s *Handler) forgetRecordedRuntimeSessionIfExited(
	ctx context.Context,
	session localruntime.SessionInfo,
) {
	if s.workspaces == nil || s.runtime == nil || session.Key == "" {
		return
	}
	for _, live := range s.runtime.ListSessions(session.WorkspaceID) {
		if live.Key == session.Key {
			return
		}
	}
	cleanupCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), runtimeSessionCleanupTimeout,
	)
	defer cancel()
	if _, err := s.workspaces.ForgetRuntimeSessionAfterExit(
		cleanupCtx,
		session.WorkspaceID,
		session.Key,
		session.CreatedAt,
		session.TmuxSession,
	); err != nil {
		slog.Warn(
			"forget runtime session that exited before record completed",
			"workspace_id", session.WorkspaceID,
			"session_key", session.Key,
			"err", err,
		)
		return
	}
	s.invalidateWorkspaceEnrichment(session.WorkspaceID)
}

func (s *Handler) recordRuntimeSession(
	ctx context.Context,
	workspaceID string,
	session localruntime.SessionInfo,
	scope string,
) error {
	if scope == "" {
		scope = "session"
	}
	if err := s.workspaces.RecordRuntimeSession(
		ctx,
		db.WorkspaceRuntimeSession{
			WorkspaceID:   workspaceID,
			SessionKey:    session.Key,
			TargetKey:     session.TargetKey,
			Label:         session.Label,
			Kind:          string(session.Kind),
			DisplayRegion: session.DisplayRegion,
			Scope:         scope,
			TmuxSession:   session.TmuxSession,
			CreatedAt:     session.CreatedAt,
		},
	); err != nil {
		return httpapi.Internal("record runtime session: " + err.Error())
	}
	return nil
}

func normalizeRuntimeDisplayRegion(
	value string,
	session localruntime.SessionInfo,
) string {
	switch strings.TrimSpace(value) {
	case "workflow":
		return "workflow"
	case "terminal":
		return "terminal"
	}
	if session.TargetKey == string(localruntime.LaunchTargetPlainShell) ||
		session.Kind == localruntime.LaunchTargetPlainShell {
		return "terminal"
	}
	return "workflow"
}

func (s *Handler) stopWorkspaceRuntimeSession(
	ctx context.Context,
	input *stopWorkspaceRuntimeSessionInput,
) (*struct{}, error) {
	summary, err := s.getRuntimeWorkspace(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	if err := s.runtime.Stop(
		ctx, summary.ID, input.SessionKey,
	); err != nil {
		if errors.Is(err, localruntime.ErrSessionNotFound) {
			stopped, stopErr := s.workspaces.StopStoredRuntimeSessionByKey(
				ctx, summary.ID, input.SessionKey,
			)
			if stopErr != nil {
				return nil, httpapi.Internal(
					"stop stored runtime session: " + stopErr.Error(),
				)
			}
			if stopped {
				s.removeAgentActivityRuntimeSession(input.SessionKey)
				s.invalidateWorkspaceEnrichment(summary.ID)
				return nil, nil
			}
			return nil, httpapi.NotFound(httpapi.CodeNotFound, err.Error(), nil)
		}
		return nil, httpapi.Internal("stop runtime session: " + err.Error())
	}
	if err := s.workspaces.ForgetRuntimeSession(
		ctx, summary.ID, input.SessionKey,
	); err != nil {
		return nil, httpapi.Internal("forget runtime session: " + err.Error())
	}
	s.removeAgentActivityRuntimeSession(input.SessionKey)
	s.invalidateWorkspaceEnrichment(summary.ID)
	return nil, nil
}

func (s *Handler) getWorkspaceRuntimeSessionAttachSpec(
	ctx context.Context,
	input *getWorkspaceRuntimeSessionAttachSpecInput,
) (*runtimeAttachSpecOutput, error) {
	summary, err := s.getRuntimeWorkspace(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	if runtimeSessionIsNonTmux(
		s.runtime.ListSessions(summary.ID),
		input.SessionKey,
	) {
		return nil, httpapi.BadRequest(
			httpapi.CodeBadRequest, "runtime session is not tmux-backed", nil,
		)
	}
	stored, err := s.db.ListWorkspaceRuntimeTmuxSessions(ctx, summary.ID)
	if err != nil {
		return nil, httpapi.Internal("list runtime tmux sessions: " + err.Error())
	}
	targetKey, tmuxSession, ok := workspaceRuntimeAttachTarget(
		input.SessionKey, stored,
	)
	if !ok {
		return nil, httpapi.NotFound(httpapi.CodeNotFound, "runtime session not found", nil)
	}
	spec, err := runtimeAttachSpec(
		ctx, s.tmuxCommand(), input.SessionKey, targetKey, tmuxSession,
	)
	if err != nil {
		return nil, err
	}
	return &runtimeAttachSpecOutput{Body: spec}, nil
}

func runtimeSessionIsNonTmux(
	sessions []localruntime.SessionInfo,
	sessionKey string,
) bool {
	for _, session := range sessions {
		if session.Key == sessionKey {
			return session.TmuxSession == ""
		}
	}
	return false
}

func workspaceRuntimeAttachTarget(
	sessionKey string,
	stored []db.WorkspaceRuntimeSession,
) (targetKey string, tmuxSession string, ok bool) {
	for _, row := range stored {
		if row.SessionKey != sessionKey {
			continue
		}
		return row.TargetKey, row.TmuxSession, true
	}
	return "", "", false
}

func runtimeSessionTmuxSession(
	sessions []localruntime.SessionInfo,
	key string,
) string {
	for _, session := range sessions {
		if session.Key == key {
			return session.TmuxSession
		}
	}
	return ""
}

func (s *Handler) renameWorkspaceRuntimeSession(
	ctx context.Context,
	input *renameWorkspaceRuntimeSessionInput,
) (*workspaceRuntimeSessionOutput, error) {
	summary, err := s.getRuntimeWorkspace(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	label := strings.TrimSpace(input.Body.Label)
	if label == "" {
		return nil, httpapi.Validation("body.label", "label is required")
	}

	session, err := s.runtime.RenameSession(
		summary.ID, input.SessionKey, label,
	)
	if err != nil {
		if errors.Is(err, localruntime.ErrSessionNotFound) {
			stored, renamed, renameErr := s.renameStoredRuntimeSession(
				ctx, summary.ID, input.SessionKey, label,
			)
			if renameErr != nil {
				return nil, httpapi.Internal(
					"rename stored runtime session: " + renameErr.Error(),
				)
			}
			if renamed {
				return &workspaceRuntimeSessionOutput{Body: stored}, nil
			}
			return nil, httpapi.NotFound(httpapi.CodeNotFound, err.Error(), nil)
		}
		return nil, httpapi.Internal("rename runtime session: " + err.Error())
	}

	updated, err := s.workspaces.UpdateRuntimeSessionLabel(
		ctx, summary.ID, input.SessionKey, session.Label,
	)
	if err != nil {
		return nil, httpapi.Internal("update runtime session label: " + err.Error())
	}
	if !updated {
		if err := s.recordRuntimeSession(ctx, summary.ID, session, "session"); err != nil {
			return nil, err
		}
	}
	return &workspaceRuntimeSessionOutput{Body: session}, nil
}

func (s *Handler) renameStoredRuntimeSession(
	ctx context.Context,
	workspaceID string,
	sessionKey string,
	label string,
) (localruntime.SessionInfo, bool, error) {
	updated, err := s.workspaces.UpdateRuntimeSessionLabel(
		ctx, workspaceID, sessionKey, label,
	)
	if err != nil || !updated {
		return localruntime.SessionInfo{}, false, err
	}
	stored, err := s.workspaces.RuntimeSessionsForWorkspace(ctx, workspaceID)
	if err != nil {
		return localruntime.SessionInfo{}, false, err
	}
	for _, session := range stored {
		if session.SessionKey == sessionKey {
			return storedRuntimeSessionInfo(session), true, nil
		}
	}
	return localruntime.SessionInfo{}, false, nil
}

func (s *Handler) getRuntimeWorkspace(
	ctx context.Context,
	id string,
) (*db.WorkspaceSummary, error) {
	if s.workspaces == nil || s.runtime == nil {
		return nil, httpapi.ServiceUnavailable("workspace runtime not configured")
	}

	summary, err := s.workspaces.GetSummary(ctx, id)
	if err != nil {
		return nil, httpapi.Internal("get workspace failed")
	}
	if summary == nil {
		return nil, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, "workspace not found", nil)
	}
	return summary, nil
}

func (s *Handler) getReadyRuntimeWorkspace(
	ctx context.Context,
	id string,
) (*db.WorkspaceSummary, error) {
	summary, err := s.getRuntimeWorkspace(ctx, id)
	if err != nil {
		return nil, err
	}
	if summary.Status != "ready" {
		return nil, httpapi.Conflict(httpapi.CodeConflict,
			"workspace not ready (status: "+summary.Status+")", nil)
	}
	return summary, nil
}

func workspaceRuntimeLaunchError(err error) error {
	msg := err.Error()
	if errors.Is(err, tokenauth.ErrMissingToken) {
		return httpapi.BadRequest(httpapi.CodeBadRequest, msg, nil)
	}
	if strings.Contains(msg, "target not found") {
		return httpapi.NotFound(httpapi.CodeNotFound, msg, nil)
	}
	if strings.Contains(msg, "not available") ||
		strings.Contains(msg, "no command") {
		return httpapi.BadRequest(httpapi.CodeBadRequest, msg, nil)
	}
	return httpapi.Internal("launch session: " + msg)
}

// deleteWorkspace tears down a middleman-managed workspace.
//
// This exists to remove the persisted workspace entry plus its managed local
// resources. It is not intended to delete arbitrary worktrees on disk.
func (s *Handler) deleteWorkspace(
	ctx context.Context, input *deleteWorkspaceInput,
) (*struct{}, error) {
	if s.workspaces == nil {
		return nil, httpapi.ServiceUnavailable("workspace manager not configured")
	}

	if s.runtime != nil {
		// Block new launches before the dirty preflight; existing
		// sessions are stopped only after the preflight passes.
		s.runtime.BeginStopping(input.ID)
	}
	defer func() {
		if s.runtime != nil {
			s.runtime.EndStopping(input.ID)
		}
	}()
	dirty, err := s.workspaces.Delete(
		ctx, input.ID, input.Force,
		func(stopCtx context.Context) {
			if s.runtime != nil {
				sessions := s.runtime.ListSessions(input.ID)
				s.runtime.StopWorkspace(stopCtx, input.ID)
				for _, session := range sessions {
					s.removeAgentActivityRuntimeSession(session.Key)
				}
			}
		},
	)
	if err != nil {
		if errors.Is(err, workspace.ErrWorkspaceNotFound) {
			return nil, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, err.Error(), nil)
		}
		return nil, httpapi.Internal("delete workspace: " + err.Error())
	}
	if len(dirty) > 0 {
		return nil, httpapi.Conflict(httpapi.CodeConflict,
			"workspace has uncommitted changes: "+strings.Join(dirty, ", "), nil)
	}

	return nil, nil
}

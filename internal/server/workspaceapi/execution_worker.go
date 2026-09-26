package workspaceapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/devbox"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/workspace"
)

// WorkspaceContextExpiredReason identifies reads the controller can renew and retry.
const WorkspaceContextExpiredReason = "workspace_context_expired"

type WorkerCreateRequest struct {
	Repository          db.WorkspaceLaunchRepository `json:"repository"`
	Branch              string                       `json:"branch"`
	ReuseExistingBranch bool                         `json:"reuse_existing_branch"`
	LaunchSpec          *db.WorkspaceLaunchSpec      `json:"launch_spec,omitempty"`
}

func (s *Handler) RegisterWorker(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "create-worker-workspace", Method: http.MethodPost, Path: "/worker/workspaces",
		DefaultStatus: http.StatusAccepted, Tags: []string{"Devboxes"}, Summary: "Create a worker workspace from controller context",
	}, s.createWorkerWorkspace)
	huma.Register(api, huma.Operation{
		OperationID: "refresh-worker-context", Method: http.MethodPut, Path: "/worker/workspaces/{id}/context",
		Tags: []string{"Devboxes"}, Summary: "Refresh controller-supplied workspace context",
	}, s.refreshWorkerContext)
}

func (s *Handler) admitWorkerRepository(ctx context.Context, repository db.WorkspaceLaunchRepository) (*devbox.Credential, error) {
	if s.workerBroker == nil || repository.Provider != "github" || repository.PlatformHost != "github.com" {
		return nil, httpapi.Validation("repository", "execution worker requires an admitted github.com repository")
	}
	credential, err := s.workerBroker.Credential(ctx, repository.Owner+"/"+repository.Name, "git")
	if err != nil {
		return nil, httpapi.Forbidden(err.Error(), nil)
	}
	if credential.GitHubUserID != s.executionWorker.GitHubUserID ||
		(repository.PlatformRepoID != 0 && repository.PlatformRepoID != credential.RepositoryID) {
		return nil, httpapi.Validation("repository", "broker identity differs from the worker or supplied repository")
	}
	cloneURL := "https://github.com/" + repository.Owner + "/" + repository.Name + ".git"
	if repository.CloneURL != "" && repository.CloneURL != cloneURL {
		return nil, httpapi.Validation("repository.clone_url", "clone URL must match the admitted GitHub repository")
	}
	entry, err := s.db.ObserveRepository(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: credential.RepositoryID,
		Owner: repository.Owner, Name: repository.Name,
	})
	if err != nil {
		return nil, httpapi.Internal("record admitted repository: " + err.Error())
	}
	if err := s.db.UpdateRepoProviderObservation(ctx, entry.Repository.ID, db.RepoProviderMetadata{
		CloneURL: cloneURL, WebURL: strings.TrimSuffix(cloneURL, ".git"),
		DefaultBranch: credential.DefaultBranch,
	}, nil, nil); err != nil {
		return nil, httpapi.Internal("record admitted repository metadata: " + err.Error())
	}
	return credential, nil
}

func (s *Handler) createWorkerWorkspace(ctx context.Context, input *struct{ Body WorkerCreateRequest }) (*createWorkspaceOutput, error) {
	request := input.Body
	if request.LaunchSpec != nil {
		if request.Repository != request.LaunchSpec.Repository {
			return nil, httpapi.Validation("launch_spec", "launch context must match the requested repository")
		}
		if err := request.LaunchSpec.RequireVisible(s.now().UTC()); err != nil {
			return nil, workspaceLaunchSpecProblem(err)
		}
	}
	if _, err := s.admitWorkerRepository(ctx, request.Repository); err != nil {
		return nil, err
	}
	if request.LaunchSpec == nil {
		if strings.TrimSpace(request.Branch) == "" {
			return nil, httpapi.Validation("branch", "repository-only creation requires an explicit branch")
		}
		result, err := s.CreateAdHocWorkspaceService(ctx, CreateAdHocWorkspaceRequest{
			Provider: request.Repository.Provider, PlatformHost: request.Repository.PlatformHost,
			Owner: request.Repository.Owner, Name: request.Repository.Name,
			Branch: &request.Branch, ReuseExistingBranch: request.ReuseExistingBranch,
		})
		if err != nil {
			return nil, err
		}
		return &createWorkspaceOutput{Status: http.StatusAccepted, Body: result.Workspace}, nil
	}
	var ws *workspace.Workspace
	var err error
	if request.LaunchSpec.ItemType == db.WorkspaceItemTypePullRequest {
		if request.LaunchSpec.Pull == nil || request.LaunchSpec.Pull.BaseBranch == "" {
			return nil, httpapi.Validation("launch_spec.pull", "worker context requires the pull request base branch")
		}
		ws, err = s.workspaces.CreateFromLaunchSpec(ctx, *request.LaunchSpec)
	} else {
		ws, err = s.workspaces.CreateIssueFromLaunchSpec(ctx, *request.LaunchSpec, workspace.CreateIssueOptions{ReuseExistingBranch: request.ReuseExistingBranch})
	}
	if errors.Is(err, workspace.ErrWorkspaceDuplicate) {
		ws, err = s.workspaces.GetByLaunchSpecIdentity(ctx, *request.LaunchSpec)
	}
	if err != nil {
		return nil, workspaceLaunchSpecProblem(err)
	}
	if ws == nil {
		return nil, httpapi.Internal("workspace missing after creation")
	}
	if ws.Status == "creating" {
		s.runWorkspaceSetup(ws)
	}
	result, err := s.GetWorkspaceService(ctx, ws.ID)
	if err != nil {
		return nil, err
	}
	return &createWorkspaceOutput{Status: http.StatusAccepted, Body: result.Workspace}, nil
}

func (s *Handler) refreshWorkerContext(ctx context.Context, input *struct {
	ID   string `path:"id"`
	Body db.WorkspaceLaunchSpec
},
) (*struct{}, error) {
	if err := input.Body.RequireVisible(s.now().UTC()); err != nil {
		return nil, workspaceLaunchSpecProblem(err)
	}
	if _, err := s.admitWorkerRepository(ctx, input.Body.Repository); err != nil {
		return nil, err
	}
	if _, err := s.db.PutRefreshedWorkspaceLaunchSpec(ctx, input.ID, input.Body); err != nil {
		return nil, workspaceLaunchSpecProblem(err)
	}
	return &struct{}{}, nil
}

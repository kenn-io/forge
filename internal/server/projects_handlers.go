package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/fleet"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/projects"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

type platformIdentityPayload struct {
	Platform     string `json:"platform"`
	PlatformHost string `json:"platform_host"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
}

type registerProjectInput struct {
	Body struct {
		LocalPath        string                   `json:"local_path"`
		DisplayName      string                   `json:"display_name,omitempty"`
		DefaultBranch    string                   `json:"default_branch,omitempty"`
		PlatformIdentity *platformIdentityPayload `json:"platform_identity,omitempty"`
	}
}

type projectResponse struct {
	ID               string                   `json:"id"`
	DisplayName      string                   `json:"display_name"`
	LocalPath        string                   `json:"local_path"`
	PlatformIdentity *platformIdentityPayload `json:"platform_identity,omitempty"`
	DefaultBranch    string                   `json:"default_branch,omitempty"`
	CreatedAt        time.Time                `json:"created_at"`
	UpdatedAt        time.Time                `json:"updated_at"`
}

type registerProjectOutput struct {
	Body projectResponse
}

type listProjectsOutput struct {
	Body struct {
		Projects []projectResponse `json:"projects"`
	}
}

type projectIDInput struct {
	ProjectID string `path:"project_id"`
}

type projectWorktreeIDInput struct {
	ProjectID  string `path:"project_id"`
	WorktreeID string `path:"worktree_id"`
}

type getProjectOutput struct {
	Body projectResponse
}

type registerWorktreeInput struct {
	ProjectID string `path:"project_id"`
	Body      struct {
		Branch string `json:"branch"`
		// Path is required for registry-only registration. With
		// create_on_disk it is optional: when empty the destination
		// derives from base_dir (default "<project>-worktrees") plus
		// the slash-slugged branch.
		Path string `json:"path,omitempty"`
		// CreateOnDisk asks middleman to perform the git work (branch
		// attach/create plus git worktree add) before registering.
		// Without it the route only records a row for a worktree the
		// caller already created.
		CreateOnDisk bool `json:"create_on_disk,omitempty"`
		// BaseRef forces creation of a new branch starting at this ref.
		BaseRef string `json:"base_ref,omitempty"`
		// BaseDir overrides the derivation base used when path is empty.
		BaseDir string `json:"base_dir,omitempty"`
		// SetupScript is a per-call lifecycle hook run in the new
		// worktree after the git work; relative paths resolve against
		// the project root and may not escape it. A non-zero exit rolls
		// the create back.
		SetupScript string `json:"setup_script,omitempty"`
		// WorktreeName is the display name exported to hook scripts.
		WorktreeName string `json:"worktree_name,omitempty"`
	}
}

type removeWorktreeInput struct {
	ProjectID  string `path:"project_id"`
	WorktreeID string `path:"worktree_id"`
	Body       struct {
		// RemoveFromDisk asks middleman to run the git removal (and
		// optional branch delete) before dropping the registry row.
		// Without it the route only drops the row, matching the legacy
		// DELETE route.
		RemoveFromDisk bool `json:"remove_from_disk,omitempty"`
		// Force removes the worktree even when it has uncommitted
		// changes.
		Force bool `json:"force,omitempty"`
		// RemoveBranch also deletes the worktree's branch.
		RemoveBranch bool `json:"remove_branch,omitempty"`
		// TeardownScript is a per-call lifecycle hook run in the
		// worktree before removal; a non-zero exit aborts the delete.
		TeardownScript string `json:"teardown_script,omitempty"`
		// WorktreeName is the display name exported to hook scripts.
		WorktreeName string `json:"worktree_name,omitempty"`
	}
}

type createWorktreeFromMergeRequestInput struct {
	ProjectID string `path:"project_id"`
	Body      struct {
		// Number is the merge request to materialize; it must already be
		// synced into middleman.
		Number int `json:"number" minimum:"1"`
		// Branch is the local branch name for the new worktree.
		Branch string `json:"branch"`
		// Path/BaseDir/SetupScript/WorktreeName behave as on the
		// create_on_disk register.
		Path         string `json:"path,omitempty"`
		BaseDir      string `json:"base_dir,omitempty"`
		SetupScript  string `json:"setup_script,omitempty"`
		WorktreeName string `json:"worktree_name,omitempty"`
	}
}

type worktreeResponse struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
	// IsPrimary marks the project root checkout's own row: addressable
	// like any worktree but not removable.
	IsPrimary          bool      `json:"is_primary"`
	IsHidden           bool      `json:"is_hidden"`
	SessionBackend     string    `json:"session_backend"`
	LinkedIssueNumbers []int     `json:"linked_issue_numbers"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type registerWorktreeOutput struct {
	Body worktreeResponse
}

// mergeRequestSummary is the slice of merge-request metadata callers
// need to label an imported worktree without a second lookup.
type mergeRequestSummary struct {
	Number  int    `json:"number"`
	URL     string `json:"url,omitempty"`
	State   string `json:"state,omitempty"`
	Title   string `json:"title,omitempty"`
	IsDraft bool   `json:"is_draft,omitempty"`
}

// worktreeFromMergeRequestResponse is worktreeResponse plus the
// merge-request block. The worktree fields are spelled out rather than
// embedded because huma's schema transformer drops anonymous fields
// from the serialized body.
type worktreeFromMergeRequestResponse struct {
	ID                 string              `json:"id"`
	ProjectID          string              `json:"project_id"`
	Branch             string              `json:"branch"`
	Path               string              `json:"path"`
	IsPrimary          bool                `json:"is_primary"`
	IsHidden           bool                `json:"is_hidden"`
	SessionBackend     string              `json:"session_backend"`
	LinkedIssueNumbers []int               `json:"linked_issue_numbers"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
	MergeRequest       mergeRequestSummary `json:"merge_request"`
}

type worktreeFromMergeRequestOutput struct {
	Body worktreeFromMergeRequestResponse
}

type setWorktreeHiddenInput struct {
	ProjectID  string `path:"project_id"`
	WorktreeID string `path:"worktree_id"`
	Body       struct {
		Hidden bool `json:"hidden"`
	}
}

type setWorktreeSessionBackendInput struct {
	ProjectID  string `path:"project_id"`
	WorktreeID string `path:"worktree_id"`
	Body       struct {
		// A pointer so an explicit null clears the override; nil and the
		// empty string both mean "no override, fall back to the default".
		SessionBackend *string `json:"session_backend"`
	}
}

type setWorktreeLinkedIssuesInput struct {
	ProjectID  string `path:"project_id"`
	WorktreeID string `path:"worktree_id"`
	Body       struct {
		// The full replacement set for this worktree's explicit links; an
		// empty (or null) list clears them. Stored normalized (sorted,
		// deduped).
		LinkedIssueNumbers []int `json:"linked_issue_numbers"`
	}
}

type listWorktreesOutput struct {
	Body struct {
		Worktrees []worktreeResponse `json:"worktrees"`
	}
}

type listLaunchTargetsOutput struct {
	Body struct {
		LaunchTargets []localruntime.LaunchTarget `json:"launch_targets"`
	}
}

type projectWorktreeRuntimeResponse struct {
	LaunchTargets []localruntime.LaunchTarget     `json:"launch_targets"`
	Sessions      []projectWorktreeRuntimeSession `json:"sessions"`
	ShellSession  *projectWorktreeRuntimeSession  `json:"shell_session,omitempty"`
}

type projectWorktreeRuntimeSession struct {
	Key         string                        `json:"key"`
	ProjectID   string                        `json:"project_id"`
	WorktreeID  string                        `json:"worktree_id"`
	TargetKey   string                        `json:"target_key"`
	Label       string                        `json:"label"`
	Kind        localruntime.LaunchTargetKind `json:"kind"`
	Status      localruntime.SessionStatus    `json:"status"`
	TmuxSession string                        `json:"tmux_session,omitempty"`
	CreatedAt   time.Time                     `json:"created_at"`
	ExitedAt    *time.Time                    `json:"exited_at,omitempty"`
	ExitCode    *int                          `json:"exit_code,omitempty"`
}

type getProjectWorktreeRuntimeOutput struct {
	Body projectWorktreeRuntimeResponse
}

type launchProjectWorktreeRuntimeSessionInput struct {
	ProjectID  string `path:"project_id"`
	WorktreeID string `path:"worktree_id"`
	Body       struct {
		// TargetKey launches a configured launch target. Mutually
		// exclusive with command.
		TargetKey string `json:"target_key,omitempty"`
		// Command launches a caller-supplied argv in a tmux-backed
		// session. Mutually exclusive with target_key.
		Command []string `json:"command,omitempty"`
		// SessionKey optionally names a command session with a
		// caller-owned durable key. Re-launching with the same key
		// returns the existing live session (ensure semantics).
		// Command launches only.
		SessionKey string `json:"session_key,omitempty"`
		// Env names extra environment variables for the command's
		// pane. Keys must be shell identifiers. Command launches only.
		Env map[string]string `json:"env,omitempty"`
		// Label is the display label for the session. Command
		// launches only.
		Label string `json:"label,omitempty"`
		// CWD overrides the pane working directory; defaults to the
		// worktree path. Command launches only.
		CWD string `json:"cwd,omitempty"`
	}
}

type stopProjectWorktreeRuntimeSessionInput struct {
	ProjectID  string `path:"project_id"`
	WorktreeID string `path:"worktree_id"`
	SessionKey string `path:"session_key"`
}

type getProjectWorktreeRuntimeSessionAttachSpecInput struct {
	ProjectID  string `path:"project_id"`
	WorktreeID string `path:"worktree_id"`
	SessionKey string `path:"session_key"`
}

type projectWorktreeRuntimeSessionOutput struct {
	Body projectWorktreeRuntimeSession
}

// registerProject handles POST /api/v1/projects.
//
// Identity resolution:
//   - If the caller passes a platform_identity payload, it wins.
//   - Otherwise the handler runs `git remote get-url origin` against the path
//     and parses the result. Unparseable, missing, or non-git remotes leave
//     the project local-only.
//
// When an identity is established (caller-provided or parsed), the handler
// calls db.UpsertRepo to ensure a middleman_repos row exists for it and
// stores the row's id as the project's repo_id FK. UpsertRepo is pure DDL
// (INSERT ON CONFLICT DO NOTHING + SELECT id) and does NOT subscribe the
// repo to sync; sync subscription remains driven by the user's TOML config
// and the AddRepo settings handler. The middleman_repos row exists solely
// as a stable FK target so the project's identity cannot drift.
func (s *Server) registerProject(
	ctx context.Context, input *registerProjectInput,
) (*registerProjectOutput, error) {
	rawPath := strings.TrimSpace(input.Body.LocalPath)
	if rawPath == "" {
		return nil, problemValidation("body.local_path", "local_path is required")
	}
	abs, err := filepath.Abs(rawPath)
	if err != nil {
		return nil, problemValidation("body.local_path", "resolve local_path: "+err.Error())
	}
	stat, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, problemValidation("body.local_path", "local_path does not exist: "+abs)
		}
		return nil, problemInternal("stat local_path: " + err.Error())
	}
	if !stat.IsDir() {
		return nil, problemValidation("body.local_path", "local_path is not a directory: "+abs)
	}

	displayName := strings.TrimSpace(input.Body.DisplayName)
	if displayName == "" {
		displayName = filepath.Base(abs)
	}

	created, err := s.registerProjectAtPath(
		ctx, abs, displayName,
		input.Body.PlatformIdentity,
		strings.TrimSpace(input.Body.DefaultBranch),
	)
	if err != nil {
		return nil, err
	}
	return &registerProjectOutput{Body: projectResponseFromDB(created)}, nil
}

// registerProjectAtPath runs the post-validation registration core shared
// by project registration and clone-and-register: identity resolution,
// repo upsert, project row creation, and an immediate discovery pass.
func (s *Server) registerProjectAtPath(
	ctx context.Context,
	abs string,
	displayName string,
	callerIdentity *platformIdentityPayload,
	defaultBranch string,
) (*db.Project, error) {
	identity, err := s.resolveProjectIdentity(ctx, callerIdentity, abs)
	if err != nil {
		return nil, err
	}

	var repoID sql.NullInt64
	if identity != nil {
		id, upsertErr := s.db.UpsertRepo(ctx, db.RepoIdentity{
			Platform:     identity.Platform,
			PlatformHost: identity.Host,
			Owner:        identity.Owner,
			Name:         identity.Name,
		})
		if upsertErr != nil {
			return nil, problemInternal(
				"upsert repo identity: " + upsertErr.Error(),
			)
		}
		repoID = sql.NullInt64{Int64: id, Valid: true}
	}

	created, err := s.db.CreateProject(ctx, db.CreateProjectInput{
		DisplayName:   displayName,
		LocalPath:     abs,
		RepoID:        repoID,
		DefaultBranch: defaultBranch,
	})
	if err != nil {
		if errors.Is(err, db.ErrProjectPathTaken) {
			return nil, problemConflict(
				CodeConflict,
				"a project is already registered at "+abs,
				nil,
			)
		}
		return nil, problemInternal("register project: " + err.Error())
	}

	// Discover the checkout's worktrees and repository kind immediately so a
	// freshly registered project does not wait for the next background pass.
	if s.fleetWorktreeDiscoverer != nil {
		s.fleetWorktreeDiscoverer.refreshProject(ctx, created.ID, created.LocalPath)
	}

	return created, nil
}

// resolveProjectIdentity returns the platform identity to associate with a
// project. Caller-provided identity wins; otherwise the handler tries to
// parse the path's git origin remote. Returns (nil, nil) when neither is
// available - that path produces a local-only project.
func (s *Server) resolveProjectIdentity(
	ctx context.Context,
	caller *platformIdentityPayload,
	abs string,
) (*db.PlatformIdentity, error) {
	if caller != nil {
		platform := strings.TrimSpace(caller.Platform)
		host := strings.TrimSpace(caller.PlatformHost)
		owner := strings.TrimSpace(caller.Owner)
		name := strings.TrimSpace(caller.Name)
		if platform == "" || host == "" || owner == "" || name == "" {
			return nil, problemValidation(
				"body.platform_identity",
				"platform_identity requires platform, platform_host, owner, and name",
			)
		}
		return &db.PlatformIdentity{Platform: platform, Host: host, Owner: owner, Name: name}, nil
	}
	resolved, err := projects.ResolveIdentityFromPathWithKnownPlatforms(
		ctx, abs, s.knownProjectPlatformHosts(),
	)
	if err != nil {
		return nil, problemInternal(
			"resolve platform identity: " + err.Error(),
		)
	}
	return resolved, nil
}

func (s *Server) knownProjectPlatformHosts() []projects.KnownPlatformHost {
	if s.cfg == nil {
		return nil
	}
	known := make([]projects.KnownPlatformHost, 0, len(s.cfg.Platforms)+len(s.cfg.Repos)+1)
	known = append(known, projects.KnownPlatformHost{
		Platform: "github",
		Host:     s.cfg.DefaultPlatformHost,
	})
	for _, platform := range s.cfg.Platforms {
		known = append(known, projects.KnownPlatformHost{
			Platform: platform.Type,
			Host:     platform.Host,
		})
	}
	for _, repo := range s.cfg.Repos {
		known = append(known, projects.KnownPlatformHost{
			Platform: repo.PlatformOrDefault(),
			Host:     repo.PlatformHostOrDefault(),
		})
	}
	return known
}

func (s *Server) listProjects(
	ctx context.Context, _ *struct{},
) (*listProjectsOutput, error) {
	rows, err := s.db.ListProjects(ctx)
	if err != nil {
		return nil, problemInternal("list projects: " + err.Error())
	}
	out := &listProjectsOutput{}
	out.Body.Projects = projectResponsesFromDB(rows)
	return out, nil
}

func (s *Server) getProject(
	ctx context.Context, input *projectIDInput,
) (*getProjectOutput, error) {
	project, err := s.db.GetProjectByID(ctx, input.ProjectID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeProjectNotFound, "project not found", nil)
		}
		return nil, problemInternal("get project: " + err.Error())
	}
	return &getProjectOutput{Body: projectResponseFromDB(project)}, nil
}

// deleteProject handles DELETE /api/v1/projects/{project_id}. Live runtime
// sessions on the project's worktrees are stopped (and their tmux sessions
// killed) before the rows go away, so the delete cannot orphan running tmux
// state. The project row then cascades to worktrees and their stored runtime
// tmux sessions. Returns 404 when no project matches the id so a caller can
// distinguish a missing project from a successful delete.
func (s *Server) deleteProject(
	ctx context.Context, input *projectIDInput,
) (*struct{}, error) {
	worktrees, err := s.db.ListProjectWorktrees(ctx, input.ProjectID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeProjectNotFound, "project not found", nil)
		}
		return nil, problemInternal("list project worktrees: " + err.Error())
	}
	for _, worktree := range worktrees {
		release, err := s.stopWorktreeRuntimeState(ctx, worktree.ID)
		defer release()
		if err != nil {
			return nil, problemInternal(
				"stop worktree runtime sessions: " + err.Error(),
			)
		}
	}
	if err := s.db.DeleteProject(ctx, input.ProjectID); err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeProjectNotFound, "project not found", nil)
		}
		return nil, problemInternal("delete project: " + err.Error())
	}
	s.recomputeWorktreeLinksNow(ctx)
	return nil, nil
}

// stopWorktreeRuntimeState stops live runtime sessions for a registered
// worktree (blocking new launches while it drains) and kills any stored tmux
// sessions that survived a restart, so dropping the worktree's rows cannot
// orphan running tmux state. The returned release func keeps new launches
// blocked until it runs; callers deleting the worktree's rows must defer it
// past the DB delete so a concurrent launch cannot slip in between stopping
// the sessions and removing the rows. It is safe to call even on error.
func (s *Server) stopWorktreeRuntimeState(
	ctx context.Context, worktreeID string,
) (release func(), err error) {
	release = func() {}
	if s.runtime != nil {
		scope := projectWorktreeRuntimeScope(worktreeID)
		s.runtime.BeginStopping(scope)
		var once sync.Once
		release = func() {
			once.Do(func() { s.runtime.EndStopping(scope) })
		}
		s.runtime.StopWorkspace(ctx, scope)
	}
	if s.db == nil {
		return release, nil
	}
	rows, err := s.db.ListProjectWorktreeTmuxSessions(ctx, worktreeID)
	if err != nil {
		return release, fmt.Errorf("list stored tmux sessions: %w", err)
	}
	for _, row := range rows {
		if err := killProjectRuntimeTmuxSession(
			ctx, s.cfg.TmuxCommand(), row.SessionName,
		); err != nil {
			return release, fmt.Errorf(
				"kill stored tmux session %q: %w", row.SessionName, err,
			)
		}
		if err := s.db.DeleteProjectWorktreeTmuxSession(
			ctx, worktreeID, row.SessionKey,
		); err != nil {
			return release, fmt.Errorf("forget stored tmux session: %w", err)
		}
	}
	return release, nil
}

func (s *Server) registerWorktree(
	ctx context.Context, input *registerWorktreeInput,
) (*registerWorktreeOutput, error) {
	branch := strings.TrimSpace(input.Body.Branch)
	if branch == "" {
		return nil, problemValidation("body.branch", "branch is required")
	}
	path := strings.TrimSpace(input.Body.Path)
	if path == "" && !input.Body.CreateOnDisk {
		return nil, problemValidation("body.path", "path is required")
	}

	if input.Body.CreateOnDisk {
		return s.createWorktreeOnDisk(ctx, input, branch, path)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, problemValidation("body.path", "resolve path: "+err.Error())
	}

	created, err := s.db.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: input.ProjectID,
		Branch:    branch,
		Path:      abs,
	})
	if err != nil {
		switch {
		case errors.Is(err, db.ErrProjectNotFound):
			return nil, problemNotFound(CodeProjectNotFound, "project not found", nil)
		case errors.Is(err, db.ErrWorktreePathTaken):
			return nil, problemConflict(
				CodeConflict,
				"a worktree is already registered at "+abs,
				nil,
			)
		}
		return nil, problemInternal("register worktree: " + err.Error())
	}
	s.recomputeWorktreeLinksNow(ctx)
	return &registerWorktreeOutput{Body: worktreeResponseFromDB(
		created, s.projectRootPath(ctx, input.ProjectID),
	)}, nil
}

// createWorktreeOnDisk is the materializing half of registerWorktree: it
// performs the git work via the lifecycle engine, then registers the
// resulting worktree. A registry conflict after successful git work rolls
// the git work back so a retry is possible.
func (s *Server) createWorktreeOnDisk(
	ctx context.Context, input *registerWorktreeInput, branch, path string,
) (*registerWorktreeOutput, error) {
	project, err := s.db.GetProjectByID(ctx, input.ProjectID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeProjectNotFound, "project not found", nil)
		}
		return nil, problemInternal("get project: " + err.Error())
	}

	created, err := projects.CreateWorktreeOnDisk(ctx, projects.CreateWorktreeOptions{
		ProjectRoot:  project.LocalPath,
		Branch:       branch,
		Path:         path,
		BaseDir:      input.Body.BaseDir,
		BaseRef:      input.Body.BaseRef,
		SetupScript:  input.Body.SetupScript,
		WorktreeName: input.Body.WorktreeName,
	})
	if err != nil {
		return nil, worktreeLifecycleProblem(err, "body.setup_script")
	}

	return s.registerMaterializedWorktree(ctx, project, created)
}

// registerMaterializedWorktree records a worktree the lifecycle engine just
// created on disk. A registry refusal rolls the git work back so the
// conflict does not compound.
func (s *Server) registerMaterializedWorktree(
	ctx context.Context,
	project *db.Project,
	created projects.CreateWorktreeResult,
) (*registerWorktreeOutput, error) {
	row, err := s.db.CreateProjectWorktree(ctx, db.CreateProjectWorktreeInput{
		ProjectID: project.ID,
		Branch:    created.Branch,
		Path:      created.Path,
	})
	if err != nil {
		projects.RollbackCreatedWorktree(
			ctx, project.LocalPath, created.Path, created.Branch,
			created.BranchCreated,
		)
		if errors.Is(err, db.ErrWorktreePathTaken) {
			return nil, problemConflict(
				CodeDestinationExists,
				"a worktree is already registered at "+created.Path,
				nil,
			)
		}
		return nil, problemInternal("register worktree: " + err.Error())
	}
	s.recomputeWorktreeLinksNow(ctx)
	return &registerWorktreeOutput{Body: worktreeResponseFromDB(
		row, project.LocalPath,
	)}, nil
}

// createProjectWorktreeFromMergeRequest handles
// POST /api/v1/projects/{project_id}/worktrees/from-merge-request. It
// materializes the head of an already-synced merge request as a new
// worktree (fetch, branch, optional upstream tracking, optional setup
// hook) and registers it. Freshness is the caller's concern: sync the
// merge request first if staleness matters.
func (s *Server) createProjectWorktreeFromMergeRequest(
	ctx context.Context, input *createWorktreeFromMergeRequestInput,
) (*worktreeFromMergeRequestOutput, error) {
	branch := strings.TrimSpace(input.Body.Branch)
	if branch == "" {
		return nil, problemValidation("body.branch", "branch is required")
	}
	project, err := s.db.GetProjectByID(ctx, input.ProjectID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeProjectNotFound, "project not found", nil)
		}
		return nil, problemInternal("get project: " + err.Error())
	}
	identity := project.PlatformIdentity
	if identity == nil {
		return nil, problemBadRequest(
			CodeBadRequest,
			"project has no platform identity; merge requests cannot be resolved for it",
			nil,
		)
	}
	repo, err := s.lookupRepoByProviderRoute(
		ctx, identity.Platform, identity.Host, identity.Owner, identity.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repo.ID, input.Body.Number,
	)
	if err != nil {
		return nil, problemInternal("failed to query merge request")
	}
	if mr == nil {
		// Sync on demand so callers (e.g. a fleet hub proxying into
		// this host) need no separate sync step. A diff-sync failure
		// is non-fatal — the row itself is current after SyncMR.
		var diffErr *ghclient.DiffSyncError
		syncErr := s.syncer.SyncMROnProvider(
			ctx, repoProviderKind(*repo), repoProviderHost(*repo),
			repo.Owner, repo.Name, input.Body.Number,
		)
		if syncErr == nil || errors.As(syncErr, &diffErr) {
			mr, err = s.db.GetMergeRequestByRepoIDAndNumber(
				ctx, repo.ID, input.Body.Number,
			)
			if err != nil {
				return nil, problemInternal("failed to query merge request")
			}
		}
	}
	if mr == nil {
		return nil, problemNotFound(
			CodePullNotFound,
			"merge request not found; sync it before importing",
			nil,
		)
	}

	created, err := projects.CreateWorktreeFromMergeRequest(
		ctx, projects.MergeRequestWorktreeOptions{
			ProjectRoot:      project.LocalPath,
			Branch:           branch,
			Path:             input.Body.Path,
			BaseDir:          input.Body.BaseDir,
			SetupScript:      input.Body.SetupScript,
			WorktreeName:     input.Body.WorktreeName,
			Number:           mr.Number,
			HeadBranch:       mr.HeadBranch,
			HeadRepoCloneURL: mr.HeadRepoCloneURL,
			Platform:         identity.Platform,
			ProjectRepoIdentity: strings.ToLower(
				identity.Host + "/" + identity.Owner + "/" + identity.Name,
			),
		})
	if err != nil {
		return nil, worktreeLifecycleProblem(err, "body.setup_script")
	}
	registered, err := s.registerMaterializedWorktree(ctx, project, created)
	if err != nil {
		return nil, err
	}
	wt := registered.Body
	return &worktreeFromMergeRequestOutput{
		Body: worktreeFromMergeRequestResponse{
			ID:                 wt.ID,
			ProjectID:          wt.ProjectID,
			Branch:             wt.Branch,
			Path:               wt.Path,
			IsPrimary:          wt.IsPrimary,
			IsHidden:           wt.IsHidden,
			SessionBackend:     wt.SessionBackend,
			LinkedIssueNumbers: wt.LinkedIssueNumbers,
			CreatedAt:          wt.CreatedAt,
			UpdatedAt:          wt.UpdatedAt,
			MergeRequest: mergeRequestSummary{
				Number:  mr.Number,
				URL:     mr.URL,
				State:   string(mr.State),
				Title:   mr.Title,
				IsDraft: mr.IsDraft,
			},
		},
	}, nil
}

// projectRootPath resolves a project's local checkout path for primary-row
// labeling; unknown projects resolve to "" (no row matches).
func (s *Server) projectRootPath(
	ctx context.Context, projectID string,
) string {
	project, err := s.db.GetProjectByID(ctx, projectID)
	if err != nil {
		return ""
	}
	return project.LocalPath
}

// refusePrimaryWorktreeRemoval rejects removal of the primary worktree row —
// the project's own root checkout. Dropping the row would only be undone by
// the next discovery pass, and removing it from disk would remove the
// repository itself; unregister the project instead.
func (s *Server) refusePrimaryWorktreeRemoval(
	ctx context.Context, projectID string, worktree *db.ProjectWorktree,
) error {
	project, err := s.db.GetProjectByID(ctx, projectID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return problemInternal("get project: " + err.Error())
	}
	if normPath(worktree.Path) != normPath(project.LocalPath) {
		return nil
	}
	return problemConflict(
		CodeConflict,
		"refusing to remove the primary worktree (the project root checkout)",
		nil,
	)
}

// removeProjectWorktree handles
// POST /api/v1/projects/{project_id}/worktrees/{worktree_id}/delete. With
// remove_from_disk it owns the full lifecycle: protection checks (default
// branch, dirty state unless forced), the teardown hook, the git removal,
// the optional branch delete, and finally the registry row. Without
// remove_from_disk it only drops the row, like the legacy DELETE route.
func (s *Server) removeProjectWorktree(
	ctx context.Context, input *removeWorktreeInput,
) (*struct{}, error) {
	worktree, err := s.db.GetProjectWorktreeByID(ctx, input.WorktreeID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("get worktree: " + err.Error())
	}
	if worktree.ProjectID != input.ProjectID {
		return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
	}
	if problem := s.refusePrimaryWorktreeRemoval(ctx, input.ProjectID, worktree); problem != nil {
		return nil, problem
	}

	if input.Body.RemoveFromDisk {
		project, err := s.db.GetProjectByID(ctx, input.ProjectID)
		if err != nil {
			if errors.Is(err, db.ErrProjectNotFound) {
				return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
			}
			return nil, problemInternal("get project: " + err.Error())
		}
		if project.DefaultBranch != "" && worktree.Branch == project.DefaultBranch {
			return nil, problemConflict(
				CodeBranchProtected,
				"refusing to remove the default-branch worktree "+worktree.Branch,
				nil,
			)
		}
		if !input.Body.Force {
			if _, statErr := os.Stat(worktree.Path); statErr == nil {
				dirty, dirtyErr := projects.WorktreeIsDirty(ctx, worktree.Path)
				if dirtyErr != nil {
					return nil, problemInternal(dirtyErr.Error())
				}
				if dirty {
					return nil, problemConflict(
						CodeWorktreeDirty,
						"worktree has uncommitted changes; retry with force",
						nil,
					)
				}
			}
		}
		if _, err := projects.RemoveWorktreeFromDisk(ctx, projects.RemoveWorktreeOptions{
			ProjectRoot:    project.LocalPath,
			Path:           worktree.Path,
			Branch:         worktree.Branch,
			Force:          input.Body.Force,
			RemoveBranch:   input.Body.RemoveBranch,
			TeardownScript: input.Body.TeardownScript,
			WorktreeName:   input.Body.WorktreeName,
		}); err != nil {
			return nil, worktreeLifecycleProblem(err, "body.teardown_script")
		}
	}

	release, err := s.stopWorktreeRuntimeState(ctx, input.WorktreeID)
	defer release()
	if err != nil {
		return nil, problemInternal(
			"stop worktree runtime sessions: " + err.Error(),
		)
	}
	if err := s.db.DeleteProjectWorktree(
		ctx, input.ProjectID, input.WorktreeID,
	); err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("delete worktree: " + err.Error())
	}
	s.recomputeWorktreeLinksNow(ctx)
	return nil, nil
}

// worktreeLifecycleProblem maps the lifecycle engine's sentinel errors onto
// distinct problem codes so callers can branch (retry with force, pick a
// new branch) without parsing messages. hookField names the request body
// field carrying the lifecycle hook ("body.setup_script" or
// "body.teardown_script") so a confinement violation is reported against
// the field the caller actually sent.
func worktreeLifecycleProblem(err error, hookField string) error {
	var hookErr *projects.HookError
	switch {
	case errors.As(err, &hookErr):
		return newProblem(
			http.StatusUnprocessableEntity, CodeHookFailed,
			hookErr.Error(), map[string]any{
				"scriptPath": hookErr.Script,
				"exitCode":   hookErr.ExitCode,
				"stderr":     hookErr.Stderr,
			},
		)
	case errors.Is(err, projects.ErrWorktreeDestinationExists):
		return problemConflict(CodeDestinationExists, err.Error(), nil)
	case errors.Is(err, projects.ErrBranchInUse):
		return problemConflict(CodeBranchInUse, err.Error(), nil)
	case errors.Is(err, projects.ErrInvalidBranchName):
		return problemValidation("body.branch", err.Error())
	case errors.Is(err, projects.ErrHookOutsideProject):
		return problemValidation(hookField, err.Error())
	}
	return problemInternal("worktree lifecycle: " + err.Error())
}

// deleteProjectWorktree handles
// DELETE /api/v1/projects/{project_id}/worktrees/{worktree_id}. The caller must
// have already removed the worktree from disk; middleman only drops its
// registry row. Primary worktrees are synthesized from the project row and have
// no registry row, so they cannot be deleted through this route. A worktree id
// that does not exist under the given project is a 404.
func (s *Server) deleteProjectWorktree(
	ctx context.Context, input *projectWorktreeIDInput,
) (*struct{}, error) {
	// Verify ownership before touching runtime state so a request with the
	// wrong project id cannot kill another project's sessions and then 404.
	worktree, err := s.db.GetProjectWorktreeByID(ctx, input.WorktreeID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("get worktree: " + err.Error())
	}
	if worktree.ProjectID != input.ProjectID {
		return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
	}
	if problem := s.refusePrimaryWorktreeRemoval(ctx, input.ProjectID, worktree); problem != nil {
		return nil, problem
	}
	release, err := s.stopWorktreeRuntimeState(ctx, input.WorktreeID)
	defer release()
	if err != nil {
		return nil, problemInternal(
			"stop worktree runtime sessions: " + err.Error(),
		)
	}
	if err := s.db.DeleteProjectWorktree(
		ctx, input.ProjectID, input.WorktreeID,
	); err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("delete worktree: " + err.Error())
	}
	s.recomputeWorktreeLinksNow(ctx)
	return nil, nil
}

// setProjectWorktreeHidden handles
// PUT /api/v1/projects/{project_id}/worktrees/{worktree_id}/hidden. Hiding keeps
// the worktree registered and discoverable but out of the active list; the flag
// survives discovery reconciliation. Primary worktrees have no registry row and
// so cannot be hidden through this route.
func (s *Server) setProjectWorktreeHidden(
	ctx context.Context, input *setWorktreeHiddenInput,
) (*registerWorktreeOutput, error) {
	updated, err := s.db.SetProjectWorktreeHidden(
		ctx, input.ProjectID, input.WorktreeID, input.Body.Hidden, time.Now(),
	)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("set worktree hidden: " + err.Error())
	}
	return &registerWorktreeOutput{Body: worktreeResponseFromDB(
		updated, s.projectRootPath(ctx, input.ProjectID),
	)}, nil
}

// setProjectWorktreeSessionBackend handles
// PUT /api/v1/projects/{project_id}/worktrees/{worktree_id}/session-backend. It
// records a user override for how terminal sessions attach to the worktree; an
// empty (or null) value clears the override so the snapshot producer's
// empty->localPTY default applies again. The override survives discovery
// reconciliation. Primary worktrees have no registry row and so cannot carry an
// override through this route.
func (s *Server) setProjectWorktreeSessionBackend(
	ctx context.Context, input *setWorktreeSessionBackendInput,
) (*registerWorktreeOutput, error) {
	backend := ""
	if input.Body.SessionBackend != nil {
		backend = *input.Body.SessionBackend
	}
	switch backend {
	case "", fleet.SessionBackendLocalPTY,
		fleet.SessionBackendLocalTmux, fleet.SessionBackendRemoteTmux:
	default:
		return nil, problemValidation(
			"body.session_backend",
			"unsupported session backend "+strconv.Quote(backend),
			fleet.SessionBackendLocalPTY,
			fleet.SessionBackendLocalTmux,
			fleet.SessionBackendRemoteTmux,
		)
	}
	updated, err := s.db.SetProjectWorktreeSessionBackend(
		ctx, input.ProjectID, input.WorktreeID, backend, time.Now(),
	)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("set worktree session backend: " + err.Error())
	}
	return &registerWorktreeOutput{Body: worktreeResponseFromDB(
		updated, s.projectRootPath(ctx, input.ProjectID),
	)}, nil
}

// setProjectWorktreeLinkedIssues handles
// PUT /api/v1/projects/{project_id}/worktrees/{worktree_id}/linked-issues. It
// replaces the worktree's explicit linked issue numbers with the supplied set
// (normalized to sorted, deduped); an empty (or null) list clears them. The
// links survive discovery reconciliation. These are snapshot metadata attached
// to a worktree, not a relational issue-link model. Primary worktrees have no
// registry row and so cannot carry links through this route.
func (s *Server) setProjectWorktreeLinkedIssues(
	ctx context.Context, input *setWorktreeLinkedIssuesInput,
) (*registerWorktreeOutput, error) {
	updated, err := s.db.SetProjectWorktreeLinkedIssues(
		ctx, input.ProjectID, input.WorktreeID, input.Body.LinkedIssueNumbers, time.Now(),
	)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("set worktree linked issues: " + err.Error())
	}
	return &registerWorktreeOutput{Body: worktreeResponseFromDB(
		updated, s.projectRootPath(ctx, input.ProjectID),
	)}, nil
}

// refreshProjectWorktreeStats handles
// POST /api/v1/projects/{project_id}/worktrees/{worktree_id}/refresh-stats. It
// re-measures the worktree's git stats and upserts them now, so a caller that
// just mutated the worktree (a create/adopt lifecycle step, or an explicit
// refresh action) sees fresh diff/sync fields in the fleet snapshot without
// waiting for the background sampler's next pass. Primary worktrees are
// synthesized from the project row and have no registry row, so they cannot be
// refreshed through this route; an unknown worktree under the project is a 404.
func (s *Server) refreshProjectWorktreeStats(
	ctx context.Context, input *projectWorktreeIDInput,
) (*registerWorktreeOutput, error) {
	worktree, err := s.db.GetProjectWorktreeByID(ctx, input.WorktreeID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("get worktree: " + err.Error())
	}
	if worktree.ProjectID != input.ProjectID {
		return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
	}
	project, err := s.db.GetProjectByID(ctx, input.ProjectID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("get project: " + err.Error())
	}
	if err := s.fleetWorktreeStatsSampler.refreshWorktreeStats(
		ctx, worktree.Path, project.DefaultBranch,
	); err != nil {
		return nil, problemInternal("refresh worktree stats: " + err.Error())
	}
	return &registerWorktreeOutput{Body: worktreeResponseFromDB(
		worktree, s.projectRootPath(ctx, input.ProjectID),
	)}, nil
}

func (s *Server) listWorktrees(
	ctx context.Context, input *projectIDInput,
) (*listWorktreesOutput, error) {
	rows, err := s.db.ListProjectWorktrees(ctx, input.ProjectID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeProjectNotFound, "project not found", nil)
		}
		return nil, problemInternal("list worktrees: " + err.Error())
	}
	out := &listWorktreesOutput{}
	out.Body.Worktrees = worktreeResponsesFromDB(
		rows, s.projectRootPath(ctx, input.ProjectID),
	)
	return out, nil
}

func (s *Server) listLaunchTargets(
	ctx context.Context, input *projectIDInput,
) (*listLaunchTargetsOutput, error) {
	if _, err := s.db.GetProjectByID(ctx, input.ProjectID); err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeProjectNotFound, "project not found", nil)
		}
		return nil, problemInternal("get project: " + err.Error())
	}
	// Resolve fresh on every call so PATH changes (a newly installed
	// agent, a deleted binary) take effect without restarting the
	// server. The runtime manager caches targets at startup and is
	// only initialized when options.WorktreeDir is set; this endpoint
	// must work either way.
	var agents []config.Agent
	if s.cfg != nil {
		agents = s.cfg.Agents
	}
	targets := localruntime.ResolveLaunchTargets(agents, s.cfg.TmuxCommand(), nil)
	if targets == nil {
		targets = []localruntime.LaunchTarget{}
	}
	out := &listLaunchTargetsOutput{}
	out.Body.LaunchTargets = targets
	return out, nil
}

func (s *Server) getProjectWorktreeRuntime(
	ctx context.Context,
	input *projectWorktreeIDInput,
) (*getProjectWorktreeRuntimeOutput, error) {
	worktree, err := s.readyRuntimeProjectWorktree(ctx, input.ProjectID, input.WorktreeID)
	if err != nil {
		return nil, err
	}
	scope := projectWorktreeRuntimeScope(worktree.ID)
	sessions, err := s.projectWorktreeRuntimeSessions(
		ctx, input.ProjectID, worktree.ID, scope,
	)
	if err != nil {
		return nil, problemInternal("list project worktree runtime sessions: " + err.Error())
	}

	out := projectWorktreeRuntimeResponse{
		LaunchTargets: s.runtime.LaunchTargets(),
		Sessions:      sessions,
	}
	if shell := projectWorktreeShellSession(s.runtime.ListSessions(scope)); shell != nil {
		mapped := projectWorktreeRuntimeSessionFromRuntime(
			*shell, input.ProjectID, worktree.ID,
		)
		out.ShellSession = &mapped
	}
	return &getProjectWorktreeRuntimeOutput{Body: out}, nil
}

func (s *Server) ensureProjectWorktreeRuntimeShell(
	ctx context.Context,
	input *projectWorktreeIDInput,
) (*projectWorktreeRuntimeSessionOutput, error) {
	worktree, err := s.readyRuntimeProjectWorktree(ctx, input.ProjectID, input.WorktreeID)
	if err != nil {
		return nil, err
	}
	scope := projectWorktreeRuntimeScope(worktree.ID)
	var session localruntime.SessionInfo
	if shell := projectWorktreeShellSession(s.runtime.ListSessions(scope)); shell != nil {
		session = *shell
		session.Reused = true
	} else {
		session, err = s.runtime.Launch(
			ctx, scope, worktree.Path, string(localruntime.LaunchTargetPlainShell),
		)
		if err != nil {
			return nil, projectWorktreeRuntimeLaunchError(err)
		}
	}
	// Persist tmux-backed shells like every other launch: attach-spec lookup
	// and fleet snapshot construction only know about durable rows, so an
	// unpersisted tmux shell would 404 on attach and orphan after a restart.
	if session.TmuxSession != "" {
		if err := s.db.UpsertProjectWorktreeTmuxSession(
			ctx, &db.ProjectWorktreeTmuxSession{
				WorktreeID:  worktree.ID,
				SessionKey:  session.Key,
				SessionName: session.TmuxSession,
				TargetKey:   session.TargetKey,
				Label:       session.Label,
				CreatedAt:   session.CreatedAt,
			},
		); err != nil {
			// Only roll back a session this request launched: stopping
			// a reused live shell would kill someone else's work over
			// a bookkeeping failure.
			if !session.Reused {
				_ = s.runtime.Stop(ctx, scope, session.Key)
			}
			return nil, problemInternal(
				"record project worktree runtime tmux session: " + err.Error(),
			)
		}
	}
	return &projectWorktreeRuntimeSessionOutput{
		Body: projectWorktreeRuntimeSessionFromRuntime(session, input.ProjectID, worktree.ID),
	}, nil
}

func (s *Server) launchProjectWorktreeRuntimeSession(
	ctx context.Context,
	input *launchProjectWorktreeRuntimeSessionInput,
) (*projectWorktreeRuntimeSessionOutput, error) {
	worktree, err := s.readyRuntimeProjectWorktree(ctx, input.ProjectID, input.WorktreeID)
	if err != nil {
		return nil, err
	}
	targetKey := strings.TrimSpace(input.Body.TargetKey)
	if len(input.Body.Command) > 0 {
		if targetKey != "" {
			return nil, problemValidation(
				"body.command",
				"command and target_key are mutually exclusive",
			)
		}
		if strings.TrimSpace(input.Body.Command[0]) == "" {
			return nil, problemValidation(
				"body.command", "command executable must not be empty",
			)
		}
		return s.launchProjectWorktreeRuntimeCommandSession(
			ctx, input, worktree,
		)
	}
	if input.Body.SessionKey != "" || len(input.Body.Env) > 0 ||
		input.Body.Label != "" || input.Body.CWD != "" {
		return nil, problemValidation(
			"body.command",
			"session_key, env, label, and cwd require a command launch",
		)
	}
	if targetKey == "" {
		return nil, problemValidation("body.target_key", "target_key is required")
	}
	if targetKey == string(localruntime.LaunchTargetPlainShell) {
		return nil, problemBadRequest(
			CodeBadRequest,
			"plain_shell must be launched through /runtime/shell",
			nil,
		)
	}

	scope := projectWorktreeRuntimeScope(worktree.ID)
	session, err := s.runtime.Launch(ctx, scope, worktree.Path, targetKey)
	if err != nil {
		return nil, projectWorktreeRuntimeLaunchError(err)
	}
	if session.TmuxSession != "" {
		if err := s.db.UpsertProjectWorktreeTmuxSession(
			ctx, &db.ProjectWorktreeTmuxSession{
				WorktreeID:  worktree.ID,
				SessionKey:  session.Key,
				SessionName: session.TmuxSession,
				TargetKey:   session.TargetKey,
				Label:       session.Label,
				CreatedAt:   session.CreatedAt,
			},
		); err != nil {
			_ = s.runtime.Stop(ctx, scope, session.Key)
			return nil, problemInternal(
				"record project worktree runtime tmux session: " + err.Error(),
			)
		}
	}
	return &projectWorktreeRuntimeSessionOutput{
		Body: projectWorktreeRuntimeSessionFromRuntime(session, input.ProjectID, worktree.ID),
	}, nil
}

// launchProjectWorktreeRuntimeCommandSession handles the command variant of
// POST .../runtime/sessions: a caller-supplied argv launched in a tmux-backed
// session, optionally under a caller-owned durable session key with ensure
// semantics (re-launching with the same key returns the live session).
func (s *Server) launchProjectWorktreeRuntimeCommandSession(
	ctx context.Context,
	input *launchProjectWorktreeRuntimeSessionInput,
	worktree *db.ProjectWorktree,
) (*projectWorktreeRuntimeSessionOutput, error) {
	for key := range input.Body.Env {
		if !localruntime.IsShellIdentifier(key) {
			return nil, problemValidation(
				"body.env",
				"env var "+strconv.Quote(key)+" is not a valid shell identifier",
			)
		}
	}
	scope := projectWorktreeRuntimeScope(worktree.ID)
	sessionKey := strings.TrimSpace(input.Body.SessionKey)
	cwd := expandHomeCWD(strings.TrimSpace(input.Body.CWD))
	if cwd == "" {
		cwd = worktree.Path
	}
	session, err := s.runtime.EnsureCommandSession(
		ctx, scope, localruntime.CommandLaunchSpec{
			SessionKey: sessionKey,
			Command:    input.Body.Command,
			Env:        input.Body.Env,
			Label:      input.Body.Label,
			CWD:        cwd,
		},
	)
	if err != nil {
		return nil, projectWorktreeRuntimeLaunchError(err)
	}
	// Always upsert with the returned session's generation: ensure semantics
	// can return a live reused session or a brand-new one, and the stored
	// row must carry the live created_at so a stale generation's async exit
	// cleanup cannot delete it.
	if session.TmuxSession != "" {
		if err := s.db.UpsertProjectWorktreeTmuxSession(
			ctx, &db.ProjectWorktreeTmuxSession{
				WorktreeID:  worktree.ID,
				SessionKey:  session.Key,
				SessionName: session.TmuxSession,
				Label:       session.Label,
				CreatedAt:   session.CreatedAt,
			},
		); err != nil {
			// Only roll back a session this request launched (see the
			// shell ensure above).
			if !session.Reused {
				_ = s.runtime.Stop(ctx, scope, session.Key)
			}
			return nil, problemInternal(
				"record project worktree runtime tmux session: " + err.Error(),
			)
		}
	}
	return &projectWorktreeRuntimeSessionOutput{
		Body: projectWorktreeRuntimeSessionFromRuntime(
			session, input.ProjectID, worktree.ID,
		),
	}, nil
}

func (s *Server) stopProjectWorktreeRuntimeSession(
	ctx context.Context,
	input *stopProjectWorktreeRuntimeSessionInput,
) (*struct{}, error) {
	worktree, err := s.readyRuntimeProjectWorktree(ctx, input.ProjectID, input.WorktreeID)
	if err != nil {
		return nil, err
	}
	scope := projectWorktreeRuntimeScope(worktree.ID)
	tmuxSession := runtimeSessionTmuxSession(
		s.runtime.ListSessions(scope), input.SessionKey,
	)
	if err := s.runtime.Stop(ctx, scope, input.SessionKey); err != nil {
		if errors.Is(err, localruntime.ErrSessionNotFound) {
			stopped, stopErr := s.stopStoredProjectWorktreeRuntimeTmuxSession(
				ctx, worktree.ID, input.SessionKey,
			)
			if stopErr != nil {
				return nil, problemInternal(
					"stop stored project worktree runtime session: " + stopErr.Error(),
				)
			}
			if stopped {
				return nil, nil
			}
			return nil, problemNotFound(CodeNotFound, err.Error(), nil)
		}
		return nil, problemInternal("stop project worktree runtime session: " + err.Error())
	}
	if tmuxSession != "" {
		if err := s.db.DeleteProjectWorktreeTmuxSession(
			ctx, worktree.ID, input.SessionKey,
		); err != nil {
			return nil, problemInternal(
				"forget project worktree runtime tmux session: " + err.Error(),
			)
		}
	}
	return nil, nil
}

func (s *Server) getProjectWorktreeRuntimeSessionAttachSpec(
	ctx context.Context,
	input *getProjectWorktreeRuntimeSessionAttachSpecInput,
) (*runtimeAttachSpecOutput, error) {
	worktree, err := s.readyRuntimeProjectWorktree(
		ctx, input.ProjectID, input.WorktreeID,
	)
	if err != nil {
		return nil, err
	}
	scope := projectWorktreeRuntimeScope(worktree.ID)
	if runtimeSessionIsNonTmux(
		s.runtime.ListSessions(scope),
		input.SessionKey,
	) {
		return nil, problemBadRequest(
			CodeBadRequest, "runtime session is not tmux-backed", nil,
		)
	}
	rows, err := s.db.ListProjectWorktreeTmuxSessions(ctx, worktree.ID)
	if err != nil {
		return nil, problemInternal(
			"list project worktree runtime tmux sessions: " + err.Error(),
		)
	}
	targetKey, tmuxSession, ok := projectWorktreeRuntimeAttachTarget(
		input.SessionKey, rows,
	)
	if !ok {
		return nil, problemNotFound(CodeNotFound, "runtime session not found", nil)
	}
	spec, err := runtimeAttachSpec(
		ctx, s.cfg.TmuxCommand(), input.SessionKey, targetKey, tmuxSession,
	)
	if err != nil {
		return nil, err
	}
	return &runtimeAttachSpecOutput{Body: spec}, nil
}

func projectWorktreeRuntimeAttachTarget(
	sessionKey string,
	rows []db.ProjectWorktreeTmuxSession,
) (targetKey string, tmuxSession string, ok bool) {
	for _, row := range rows {
		if row.SessionKey != sessionKey {
			continue
		}
		return row.TargetKey, row.SessionName, true
	}
	return "", "", false
}

func (s *Server) stopStoredProjectWorktreeRuntimeTmuxSession(
	ctx context.Context,
	worktreeID string,
	sessionKey string,
) (bool, error) {
	rows, err := s.db.ListProjectWorktreeTmuxSessions(ctx, worktreeID)
	if err != nil {
		return false, fmt.Errorf("list stored tmux sessions: %w", err)
	}
	for _, row := range rows {
		if row.SessionKey != sessionKey {
			continue
		}
		if err := killProjectRuntimeTmuxSession(
			ctx, s.cfg.TmuxCommand(), row.SessionName,
		); err != nil {
			return true, err
		}
		if err := s.db.DeleteProjectWorktreeTmuxSession(
			ctx, worktreeID, sessionKey,
		); err != nil {
			return true, fmt.Errorf("forget stored tmux session: %w", err)
		}
		return true, nil
	}
	return false, nil
}

func killProjectRuntimeTmuxSession(
	ctx context.Context,
	command []string,
	session string,
) error {
	if session == "" {
		return nil
	}
	if len(command) == 0 {
		command = []string{"tmux"}
	}
	if command[0] == "" {
		return nil
	}
	args := append([]string{}, command[1:]...)
	args = append(args, "kill-session", "-t", session)
	cmd := procutil.CommandContext(ctx, command[0], args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := procutil.Run(ctx, cmd, "project worktree runtime tmux cleanup")
	if err == nil || projectRuntimeTmuxSessionAbsent(stderr.Bytes(), err) {
		return nil
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		return err
	}
	if strings.Contains(msg, "server exited unexpectedly") {
		return nil
	}
	return fmt.Errorf("%w: %s", err, msg)
}

func projectRuntimeTmuxSessionAbsent(stderr []byte, err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		return false
	}
	msg := string(stderr)
	return strings.Contains(msg, "can't find session") ||
		strings.Contains(msg, "no server running") ||
		(strings.Contains(msg, "error connecting to") &&
			strings.Contains(msg, "No such file or directory"))
}

func (s *Server) readyRuntimeProjectWorktree(
	ctx context.Context,
	projectID string,
	worktreeID string,
) (*db.ProjectWorktree, error) {
	if s.runtime == nil {
		return nil, problemServiceUnavailable("project worktree runtime not configured")
	}
	if _, err := s.db.GetProjectByID(ctx, projectID); err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeProjectNotFound, "project not found", nil)
		}
		return nil, problemInternal("get project: " + err.Error())
	}
	worktree, err := s.db.GetProjectWorktreeByID(ctx, worktreeID)
	if err != nil {
		if errors.Is(err, db.ErrProjectNotFound) {
			return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
		}
		return nil, problemInternal("get project worktree: " + err.Error())
	}
	if worktree.ProjectID != projectID {
		return nil, problemNotFound(CodeNotFound, "worktree not found", nil)
	}
	stat, err := os.Stat(worktree.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, problemConflict(
				CodeConflict, "worktree path does not exist: "+worktree.Path, nil,
			)
		}
		return nil, problemInternal("stat worktree path: " + err.Error())
	}
	if !stat.IsDir() {
		return nil, problemBadRequest(
			CodeBadRequest, "worktree path is not a directory: "+worktree.Path, nil,
		)
	}
	return worktree, nil
}

func projectWorktreeRuntimeScope(worktreeID string) string {
	return "project-worktree:" + worktreeID
}

// projectWorktreeShellSession returns the runtime's plain_shell session for a
// worktree scope, if one is live. Worktrees keep a single plain_shell session.
func projectWorktreeShellSession(
	sessions []localruntime.SessionInfo,
) *localruntime.SessionInfo {
	for i := range sessions {
		if sessions[i].Kind == localruntime.LaunchTargetPlainShell {
			return &sessions[i]
		}
	}
	return nil
}

func (s *Server) projectWorktreeRuntimeSessions(
	ctx context.Context,
	projectID string,
	worktreeID string,
	scope string,
) ([]projectWorktreeRuntimeSession, error) {
	runtimeSessions := s.runtime.ListSessions(scope)
	runtimeByKey := make(map[string]localruntime.SessionInfo, len(runtimeSessions))
	for _, session := range runtimeSessions {
		runtimeByKey[session.Key] = session
	}
	targets := s.runtime.LaunchTargets()
	targetByKey := make(map[string]localruntime.LaunchTarget, len(targets))
	for _, target := range targets {
		targetByKey[target.Key] = target
	}
	stored, err := s.db.ListProjectWorktreeTmuxSessions(ctx, worktreeID)
	if err != nil {
		return nil, err
	}
	out := make([]projectWorktreeRuntimeSession, 0, len(stored)+len(runtimeSessions))
	seen := make(map[string]struct{}, len(stored)+len(runtimeSessions))
	for _, row := range stored {
		seen[row.SessionKey] = struct{}{}
		if runtimeSession, ok := runtimeByKey[row.SessionKey]; ok {
			out = append(out, projectWorktreeRuntimeSessionFromRuntime(
				runtimeSession, projectID, worktreeID,
			))
			continue
		}
		out = append(out, projectWorktreeRuntimeSessionFromStored(
			row, projectID, worktreeID, targetByKey,
		))
	}
	for _, runtimeSession := range runtimeSessions {
		if _, ok := seen[runtimeSession.Key]; ok {
			continue
		}
		out = append(out, projectWorktreeRuntimeSessionFromRuntime(
			runtimeSession, projectID, worktreeID,
		))
	}
	return out, nil
}

func projectWorktreeRuntimeSessionFromRuntime(
	session localruntime.SessionInfo,
	projectID string,
	worktreeID string,
) projectWorktreeRuntimeSession {
	return projectWorktreeRuntimeSession{
		Key:         session.Key,
		ProjectID:   projectID,
		WorktreeID:  worktreeID,
		TargetKey:   session.TargetKey,
		Label:       session.Label,
		Kind:        session.Kind,
		Status:      session.Status,
		TmuxSession: session.TmuxSession,
		CreatedAt:   session.CreatedAt,
		ExitedAt:    session.ExitedAt,
		ExitCode:    session.ExitCode,
	}
}

func projectWorktreeRuntimeSessionFromStored(
	session db.ProjectWorktreeTmuxSession,
	projectID string,
	worktreeID string,
	targetByKey map[string]localruntime.LaunchTarget,
) projectWorktreeRuntimeSession {
	label := session.TargetKey
	kind := localruntime.LaunchTargetAgent
	if session.TargetKey == "" {
		// Command sessions have no launch target; the launch-time
		// label travels with the stored row.
		kind = localruntime.LaunchTargetCommand
	} else if target, ok := targetByKey[session.TargetKey]; ok {
		label = target.Label
		kind = target.Kind
	}
	if session.Label != "" {
		label = session.Label
	}
	return projectWorktreeRuntimeSession{
		Key:         session.SessionKey,
		ProjectID:   projectID,
		WorktreeID:  worktreeID,
		TargetKey:   session.TargetKey,
		Label:       label,
		Kind:        kind,
		Status:      localruntime.SessionStatusRunning,
		TmuxSession: session.SessionName,
		CreatedAt:   session.CreatedAt,
	}
}

func projectWorktreeRuntimeLaunchError(err error) error {
	msg := err.Error()
	if errors.Is(err, localruntime.ErrSessionKeyConflict) {
		return problemConflict(CodeConflict, msg, nil)
	}
	if strings.Contains(msg, "target not found") ||
		strings.Contains(msg, "not available") ||
		strings.Contains(msg, "no command") {
		return problemBadRequest(CodeBadRequest, msg, nil)
	}
	return problemInternal("launch project worktree session: " + msg)
}

func projectResponseFromDB(p *db.Project) projectResponse {
	resp := projectResponse{
		ID:            p.ID,
		DisplayName:   p.DisplayName,
		LocalPath:     p.LocalPath,
		DefaultBranch: p.DefaultBranch,
		CreatedAt:     p.CreatedAt,
		UpdatedAt:     p.UpdatedAt,
	}
	if p.PlatformIdentity != nil {
		resp.PlatformIdentity = &platformIdentityPayload{
			Platform:     p.PlatformIdentity.Platform,
			PlatformHost: p.PlatformIdentity.Host,
			Owner:        p.PlatformIdentity.Owner,
			Name:         p.PlatformIdentity.Name,
		}
	}
	return resp
}

func projectResponsesFromDB(rows []db.Project) []projectResponse {
	responses := make([]projectResponse, 0, len(rows))
	for i := range rows {
		responses = append(responses, projectResponseFromDB(&rows[i]))
	}
	return responses
}

func worktreeResponseFromDB(
	w *db.ProjectWorktree, projectRoot string,
) worktreeResponse {
	return worktreeResponse{
		ID:        w.ID,
		ProjectID: w.ProjectID,
		Branch:    w.Branch,
		Path:      w.Path,
		IsPrimary: projectRoot != "" &&
			normPath(w.Path) == normPath(projectRoot),
		IsHidden:           w.IsHidden,
		SessionBackend:     w.SessionBackend,
		LinkedIssueNumbers: w.LinkedIssueNumbers,
		CreatedAt:          w.CreatedAt,
		UpdatedAt:          w.UpdatedAt,
	}
}

func worktreeResponsesFromDB(
	rows []db.ProjectWorktree, projectRoot string,
) []worktreeResponse {
	responses := make([]worktreeResponse, 0, len(rows))
	for i := range rows {
		responses = append(responses, worktreeResponseFromDB(&rows[i], projectRoot))
	}
	return responses
}

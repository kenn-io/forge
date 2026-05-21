package server

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wesm/middleman/internal/config"
	"github.com/wesm/middleman/internal/db"
	"github.com/wesm/middleman/internal/projects"
	"github.com/wesm/middleman/internal/workspace/localruntime"
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

type getProjectOutput struct {
	Body projectResponse
}

type registerWorktreeInput struct {
	ProjectID string `path:"project_id"`
	Body      struct {
		Branch string `json:"branch"`
		Path   string `json:"path"`
	}
}

type worktreeResponse struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Branch    string    `json:"branch"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type registerWorktreeOutput struct {
	Body worktreeResponse
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

	identity, err := s.resolveProjectIdentity(ctx, input.Body.PlatformIdentity, abs)
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
		DefaultBranch: strings.TrimSpace(input.Body.DefaultBranch),
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

	return &registerProjectOutput{Body: projectResponseFromDB(created)}, nil
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

func (s *Server) registerWorktree(
	ctx context.Context, input *registerWorktreeInput,
) (*registerWorktreeOutput, error) {
	branch := strings.TrimSpace(input.Body.Branch)
	if branch == "" {
		return nil, problemValidation("body.branch", "branch is required")
	}
	path := strings.TrimSpace(input.Body.Path)
	if path == "" {
		return nil, problemValidation("body.path", "path is required")
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
	return &registerWorktreeOutput{Body: worktreeResponseFromDB(created)}, nil
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
	out.Body.Worktrees = worktreeResponsesFromDB(rows)
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

func worktreeResponseFromDB(w *db.ProjectWorktree) worktreeResponse {
	return worktreeResponse{
		ID:        w.ID,
		ProjectID: w.ProjectID,
		Branch:    w.Branch,
		Path:      w.Path,
		CreatedAt: w.CreatedAt,
		UpdatedAt: w.UpdatedAt,
	}
}

func worktreeResponsesFromDB(rows []db.ProjectWorktree) []worktreeResponse {
	responses := make([]worktreeResponse, 0, len(rows))
	for i := range rows {
		responses = append(responses, worktreeResponseFromDB(&rows[i]))
	}
	return responses
}

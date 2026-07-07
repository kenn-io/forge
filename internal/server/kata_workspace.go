package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/workspace"
)

// maxKataProjectTOMLBytes caps how much of a .kata.toml middleman will read.
// The file only carries a tiny [project] table, so this is generous while
// preventing untrusted repo content from forcing an unbounded read.
const maxKataProjectTOMLBytes = 64 << 10

type kataWorkspaceTaskRequest struct {
	DaemonID    string `json:"daemon_id"`
	ProjectUID  string `json:"project_uid"`
	ProjectName string `json:"project_name,omitempty"`
	IssueUID    string `json:"issue_uid"`
	ShortID     string `json:"short_id,omitempty"`
	QualifiedID string `json:"qualified_id,omitempty"`
	Title       string `json:"title,omitempty"`
}

type kataWorkspaceTaskInput struct {
	Body kataWorkspaceTaskRequest
}

type kataWorkspaceTargetResponse struct {
	Available         bool             `json:"available"`
	Repo              *repoRefResponse `json:"repo,omitempty"`
	ItemType          string           `json:"item_type,omitempty"`
	ItemKey           string           `json:"item_key,omitempty"`
	ExistingWorkspace *workspaceRef    `json:"existing_workspace,omitempty"`
}

type kataResolvedWorkspaceRepo struct {
	Provider     string
	PlatformHost string
	Owner        string
	Name         string
}

func (body kataWorkspaceTaskRequest) metadata() (db.WorkspaceKataMetadata, error) {
	metadata := db.WorkspaceKataMetadata{
		DaemonID:    strings.TrimSpace(body.DaemonID),
		ProjectUID:  strings.TrimSpace(body.ProjectUID),
		ProjectName: strings.TrimSpace(body.ProjectName),
		IssueUID:    strings.TrimSpace(body.IssueUID),
		ShortID:     strings.TrimSpace(body.ShortID),
		QualifiedID: strings.TrimSpace(body.QualifiedID),
		Title:       strings.TrimSpace(body.Title),
	}
	if metadata.ProjectUID == "" {
		return metadata, problemValidation("body.project_uid", "project_uid is required")
	}
	if metadata.DaemonID == "" {
		return metadata, problemValidation("body.daemon_id", "daemon_id is required")
	}
	if metadata.IssueUID == "" {
		return metadata, problemValidation("body.issue_uid", "issue_uid is required")
	}
	return metadata, nil
}

func (s *Server) kataWorkspaceTargetForMetadata(
	ctx context.Context,
	metadata db.WorkspaceKataMetadata,
) (kataWorkspaceTargetResponse, error) {
	target, ok, err := s.resolveKataWorkspaceRepo(ctx, metadata)
	if err != nil {
		return kataWorkspaceTargetResponse{}, err
	}
	if !ok {
		return kataWorkspaceTargetResponse{Available: false}, nil
	}
	repoRef := s.repoRefFromParts(
		target.Provider, target.PlatformHost, target.Owner, target.Name,
	)
	resp := kataWorkspaceTargetResponse{
		Available: true,
		Repo:      &repoRef,
		ItemType:  db.WorkspaceItemTypeKataTask,
		ItemKey:   db.KataWorkspaceItemKey(metadata),
	}
	existing, err := s.db.GetWorkspaceByItemKeyForProvider(
		ctx,
		target.Provider,
		target.PlatformHost,
		target.Owner,
		target.Name,
		db.WorkspaceItemTypeKataTask,
		resp.ItemKey,
	)
	if err != nil {
		return kataWorkspaceTargetResponse{}, problemInternal("lookup existing Kata workspace: " + err.Error())
	}
	if existing != nil {
		resp.ExistingWorkspace = &workspaceRef{
			ID:     existing.ID,
			Status: existing.Status,
		}
	}
	return resp, nil
}

func (s *Server) createKataWorkspace(
	ctx context.Context,
	input *kataWorkspaceTaskInput,
) (*createWorkspaceOutput, error) {
	if s.workspaces == nil {
		return nil, problemServiceUnavailable("workspace manager not configured")
	}
	metadata, err := input.Body.metadata()
	if err != nil {
		return nil, err
	}
	target, ok, err := s.resolveKataWorkspaceRepo(ctx, metadata)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, problemNotFound(
			CodeNotFound,
			"no repository mapping for Kata project",
			map[string]any{"project_uid": metadata.ProjectUID},
		)
	}

	existing, err := s.workspaces.GetByItemKeyForProvider(
		ctx,
		target.Provider,
		target.PlatformHost,
		target.Owner,
		target.Name,
		db.WorkspaceItemTypeKataTask,
		db.KataWorkspaceItemKey(metadata),
	)
	if err != nil {
		return nil, problemInternal("lookup existing Kata workspace: " + err.Error())
	}
	if existing != nil {
		return s.kataWorkspaceCreateOutput(ctx, existing.ID)
	}

	ws, err := s.workspaces.CreateKataTask(
		ctx,
		target.Provider,
		target.PlatformHost,
		target.Owner,
		target.Name,
		metadata,
	)
	if err != nil {
		if errors.Is(err, workspace.ErrWorkspaceDuplicate) {
			existing, getErr := s.workspaces.GetByItemKeyForProvider(
				ctx,
				target.Provider,
				target.PlatformHost,
				target.Owner,
				target.Name,
				db.WorkspaceItemTypeKataTask,
				db.KataWorkspaceItemKey(metadata),
			)
			if getErr == nil && existing != nil {
				return s.kataWorkspaceCreateOutput(ctx, existing.ID)
			}
			return nil, problemConflict(CodeConflict, "workspace already exists for this Kata task", nil)
		}
		if strings.Contains(err.Error(), "not tracked") {
			return nil, problemNotFound(CodeNotFound, err.Error(), nil)
		}
		if strings.Contains(err.Error(), "invalid branch name") {
			return nil, problemValidation("body.short_id", err.Error())
		}
		return nil, problemInternal("create Kata workspace: " + err.Error())
	}

	s.runWorkspaceSetup(ws)
	return s.kataWorkspaceCreateOutput(ctx, ws.ID)
}

func (s *Server) kataWorkspaceCreateOutput(
	ctx context.Context, workspaceID string,
) (*createWorkspaceOutput, error) {
	summary, err := s.workspaces.GetSummary(ctx, workspaceID)
	if err != nil {
		return nil, problemInternal("get workspace summary: " + err.Error())
	}
	if summary == nil {
		return nil, problemInternal("workspace summary missing after create")
	}
	return &createWorkspaceOutput{
		Status: httpStatusAccepted,
		Body:   s.toWorkspaceResponse(ctx, summary),
	}, nil
}

func (s *Server) resolveKataWorkspaceRepo(
	ctx context.Context,
	metadata db.WorkspaceKataMetadata,
) (kataResolvedWorkspaceRepo, bool, error) {
	if s.cfg == nil {
		return kataResolvedWorkspaceRepo{}, false, nil
	}
	s.cfgMu.Lock()
	repos := slices.Clone(s.cfg.Repos)
	mappings := slices.Clone(s.cfg.KataProjects)
	s.cfgMu.Unlock()

	if repo, ok := kataManualWorkspaceRepo(repos, mappings, metadata, true); ok {
		return kataResolvedRepoFromConfig(repo), true, nil
	}
	if repo, ok := kataManualWorkspaceRepo(repos, mappings, metadata, false); ok {
		return kataResolvedRepoFromConfig(repo), true, nil
	}
	repo, matches := kataAutomaticWorkspaceRepo(repos, metadata.ProjectUID, metadata.ProjectName)
	if matches == 1 {
		return kataResolvedRepoFromConfig(repo), true, nil
	}
	if matches > 1 {
		return kataResolvedWorkspaceRepo{}, false, nil
	}
	tracked, err := s.db.ListRepos(ctx)
	if err != nil {
		return kataResolvedWorkspaceRepo{}, false, fmt.Errorf("list tracked repos for Kata workspace: %w", err)
	}
	if target, matches := kataAutomaticWorkspaceRepoByTrackedRepos(repos, tracked, metadata.ProjectName); matches == 1 {
		return target, true, nil
	}
	return kataResolvedWorkspaceRepo{}, false, nil
}

func kataManualWorkspaceRepo(
	repos []config.Repo,
	mappings []config.KataProjectRepoMapping,
	metadata db.WorkspaceKataMetadata,
	daemonSpecific bool,
) (config.Repo, bool) {
	for _, mapping := range mappings {
		if mapping.ProjectUID != metadata.ProjectUID {
			continue
		}
		if daemonSpecific {
			if mapping.DaemonID == "" || mapping.DaemonID != metadata.DaemonID {
				continue
			}
		} else if mapping.DaemonID != "" {
			continue
		}
		for _, repo := range repos {
			if kataMappingMatchesRepo(mapping, repo) {
				return repo, true
			}
		}
	}
	return config.Repo{}, false
}

func kataMappingMatchesRepo(mapping config.KataProjectRepoMapping, repo config.Repo) bool {
	if repo.HasNameGlob() {
		return false
	}
	return strings.EqualFold(mapping.Provider, repo.PlatformOrDefault()) &&
		samePlatformHost(mapping.PlatformHost, repo.PlatformHostOrDefault()) &&
		strings.EqualFold(mapping.RepoPath, configRepoPath(repo))
}

func kataAutomaticWorkspaceRepo(repos []config.Repo, projectUID string, projectName string) (config.Repo, int) {
	if repo, matches := kataAutomaticWorkspaceRepoByTOML(repos, func(project kataProjectTOML) bool {
		return project.matchesProjectUID(projectUID)
	}); matches == 1 {
		return repo, 1
	} else if matches > 1 {
		return config.Repo{}, matches
	}
	name := strings.TrimSpace(projectName)
	if name == "" {
		return config.Repo{}, 0
	}
	// Name fallback is only for clones whose .kata.toml carries no stable
	// UID/identity. Restricting the match to identifier-less entries is the
	// guardrail: a clone with stable identity is matched by UID/identity only,
	// never by name, and a valid name-only project still resolves even when an
	// unrelated watched clone happens to have identity metadata.
	repo, matches := kataAutomaticWorkspaceRepoByTOML(repos, func(project kataProjectTOML) bool {
		return !project.hasIdentifier() && strings.EqualFold(project.Name, name)
	})
	if matches == 1 {
		return repo, 1
	}
	if matches > 1 {
		return config.Repo{}, matches
	}
	return config.Repo{}, 0
}

func kataAutomaticWorkspaceRepoByTOML(repos []config.Repo, matches func(kataProjectTOML) bool) (config.Repo, int) {
	var matched []config.Repo
	for _, repo := range repos {
		if repo.HasNameGlob() || strings.TrimSpace(repo.WorktreeBasePath) == "" {
			continue
		}
		project, ok := readKataProjectTOML(repo.WorktreeBasePath)
		if ok && matches(project) {
			matched = append(matched, repo)
		}
	}
	if len(matched) != 1 {
		return config.Repo{}, len(matched)
	}
	return matched[0], 1
}

func kataAutomaticWorkspaceRepoByTrackedRepos(
	configured []config.Repo,
	tracked []db.Repo,
	projectName string,
) (kataResolvedWorkspaceRepo, int) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return kataResolvedWorkspaceRepo{}, 0
	}
	var matched []kataResolvedWorkspaceRepo
	seen := make(map[string]struct{})
	for _, repo := range tracked {
		if !kataTrackedRepoMatchesAnyConfig(repo, configured) {
			continue
		}
		if kataTrackedRepoHasConfiguredProjectMetadata(repo, configured) {
			continue
		}
		if !strings.EqualFold(repo.Name, projectName) && !strings.EqualFold(kataTrackedRepoPath(repo), projectName) {
			continue
		}
		target := kataResolvedRepoFromDB(repo)
		key := strings.ToLower(target.Provider) + "\x00" +
			strings.ToLower(target.PlatformHost) + "\x00" +
			strings.ToLower(kataTrackedRepoPath(repo))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		matched = append(matched, target)
	}
	if len(matched) != 1 {
		return kataResolvedWorkspaceRepo{}, len(matched)
	}
	return matched[0], 1
}

func kataTrackedRepoMatchesAnyConfig(repo db.Repo, configured []config.Repo) bool {
	for _, raw := range configured {
		if kataTrackedRepoMatchesConfig(repo, raw) {
			return true
		}
	}
	return false
}

func kataTrackedRepoMatchesConfig(repo db.Repo, raw config.Repo) bool {
	if !strings.EqualFold(repo.Platform, raw.PlatformOrDefault()) ||
		!samePlatformHost(repo.PlatformHost, raw.PlatformHostOrDefault()) {
		return false
	}
	if raw.HasNameGlob() {
		if !strings.EqualFold(repo.Owner, raw.Owner) {
			return false
		}
		matched, _ := path.Match(
			strings.ToLower(raw.Name),
			strings.ToLower(repo.Name),
		)
		return matched
	}
	return strings.EqualFold(kataTrackedRepoPath(repo), configRepoPath(raw))
}

func kataTrackedRepoHasConfiguredProjectMetadata(repo db.Repo, configured []config.Repo) bool {
	for _, raw := range configured {
		if raw.HasNameGlob() || strings.TrimSpace(raw.WorktreeBasePath) == "" {
			continue
		}
		if !kataTrackedRepoMatchesConfig(repo, raw) {
			continue
		}
		project, ok := readKataProjectTOML(raw.WorktreeBasePath)
		if ok && project.hasAnyProjectMetadata() {
			return true
		}
	}
	return false
}

func kataTrackedRepoPath(repo db.Repo) string {
	if strings.TrimSpace(repo.RepoPath) != "" {
		return strings.TrimSpace(repo.RepoPath)
	}
	return repo.Owner + "/" + repo.Name
}

type kataProjectTOML struct {
	UID      string
	Identity string
	Name     string
}

func (project kataProjectTOML) matchesProjectUID(projectUID string) bool {
	projectUID = strings.TrimSpace(projectUID)
	if projectUID == "" {
		return false
	}
	return strings.TrimSpace(project.UID) == projectUID ||
		strings.TrimSpace(project.Identity) == projectUID
}

func (project kataProjectTOML) hasIdentifier() bool {
	return strings.TrimSpace(project.UID) != "" ||
		strings.TrimSpace(project.Identity) != ""
}

func (project kataProjectTOML) hasAnyProjectMetadata() bool {
	return project.hasIdentifier() || strings.TrimSpace(project.Name) != ""
}

func readKataProjectTOML(root string) (kataProjectTOML, bool) {
	path := filepath.Join(root, ".kata.toml")
	// .kata.toml lives in a repo whose contents are not trusted. A contributor
	// could commit it as a symlink to an endless or huge file (for example
	// /dev/zero) and stall or exhaust the middleman process when the worktree
	// is scanned. Lstat first and accept only a regular file (this rejects
	// symlinks, devices, FIFOs, and directories) before opening it.
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return kataProjectTOML{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return kataProjectTOML{}, false
	}
	defer f.Close()
	// Re-check the opened descriptor so a swap to a symlink/device between the
	// Lstat and the open cannot slip through, then read through an explicit cap
	// rather than slurping the whole file.
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return kataProjectTOML{}, false
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxKataProjectTOMLBytes+1))
	if err != nil || len(raw) > maxKataProjectTOMLBytes {
		return kataProjectTOML{}, false
	}
	var doc struct {
		Project struct {
			UID      string `toml:"uid"`
			Identity string `toml:"identity"`
			Name     string `toml:"name"`
		} `toml:"project"`
	}
	if _, err := toml.Decode(string(raw), &doc); err != nil {
		return kataProjectTOML{}, false
	}
	return kataProjectTOML{
		UID:      strings.TrimSpace(doc.Project.UID),
		Identity: strings.TrimSpace(doc.Project.Identity),
		Name:     strings.TrimSpace(doc.Project.Name),
	}, true
}

func kataResolvedRepoFromConfig(repo config.Repo) kataResolvedWorkspaceRepo {
	return kataResolvedWorkspaceRepo{
		Provider:     repo.PlatformOrDefault(),
		PlatformHost: repo.PlatformHostOrDefault(),
		Owner:        repo.Owner,
		Name:         repo.Name,
	}
}

func kataResolvedRepoFromDB(repo db.Repo) kataResolvedWorkspaceRepo {
	return kataResolvedWorkspaceRepo{
		Provider:     repo.Platform,
		PlatformHost: repo.PlatformHost,
		Owner:        repo.Owner,
		Name:         repo.Name,
	}
}

const httpStatusAccepted = 202

func registerKataWorkspaceAPI(api huma.API, s *Server) {
	huma.Register(api, huma.Operation{
		OperationID: "get-kata-task-detail",
		Method:      "GET",
		Path:        "/kata/tasks/{issue_uid}",
		Summary:     "Get Kata task detail with workspace target",
		Tags:        []string{"Kata"},
	}, s.kataTaskDetail)
	huma.Register(api, huma.Operation{
		OperationID:   "create-kata-workspace",
		Method:        "POST",
		Path:          "/kata/workspaces",
		DefaultStatus: httpStatusAccepted,
		Summary:       "Create Kata workspace",
		Tags:          []string{"Kata"},
	}, s.createKataWorkspace)
}

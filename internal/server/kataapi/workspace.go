package kataapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/workspace"
)

// maxKataProjectTOMLBytes caps how much of a .kata.toml kenn-forge will read.
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

type KataWorkspaceTargetResponse struct {
	Available         bool                       `json:"available"`
	Repo              *httpapi.RepoRefResponse   `json:"repo,omitempty"`
	ItemType          string                     `json:"item_type,omitempty"`
	ItemKey           string                     `json:"item_key,omitempty"`
	ExistingWorkspace *workspaceapi.WorkspaceRef `json:"existing_workspace,omitempty"`
}

type kataResolvedWorkspaceRepo struct {
	Provider     string
	PlatformHost string
	Owner        string
	Name         string
	BasePath     string
}

type kataWorkspaceRepoResolution struct {
	Target kataResolvedWorkspaceRepo
	Status string
	Source string
}

type kataProjectMappingDiagnostic struct {
	DaemonID    string                   `json:"daemon_id"`
	ProjectUID  string                   `json:"project_uid"`
	ProjectName string                   `json:"project_name"`
	Status      string                   `json:"status"`
	Source      string                   `json:"source,omitempty"`
	Repo        *httpapi.RepoRefResponse `json:"repo,omitempty"`
}

type kataProjectMappingsInput struct {
	DaemonID string `header:"X-Kenn-Forge-Kata-Daemon" doc:"Kata daemon id; the effective default daemon when empty"`
}

type kataProjectMappingsResponse struct {
	DaemonID string                         `json:"daemon_id"`
	Projects []kataProjectMappingDiagnostic `json:"projects" nullable:"false"`
	Targets  []kataMappingTargetResponse    `json:"targets" nullable:"false"`
}

type kataMappingTargetResponse struct {
	DisplayName string                  `json:"display_name"`
	Repo        httpapi.RepoRefResponse `json:"repo"`
}

type kataProjectMappingsOutput struct {
	Vary string `header:"Vary"`
	Body kataProjectMappingsResponse
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
		return metadata, httpapi.Validation("body.project_uid", "project_uid is required")
	}
	if metadata.DaemonID == "" {
		return metadata, httpapi.Validation("body.daemon_id", "daemon_id is required")
	}
	if metadata.IssueUID == "" {
		return metadata, httpapi.Validation("body.issue_uid", "issue_uid is required")
	}
	return metadata, nil
}

func (h *Handler) kataWorkspaceTargetForMetadata(
	ctx context.Context,
	metadata db.WorkspaceKataMetadata,
) (KataWorkspaceTargetResponse, error) {
	target, ok, err := h.resolveKataWorkspaceRepo(ctx, metadata)
	if err != nil {
		return KataWorkspaceTargetResponse{}, err
	}
	if !ok {
		return KataWorkspaceTargetResponse{Available: false}, nil
	}
	repoRef := h.repoRefFromParts(
		target.Provider, target.PlatformHost, target.Owner, target.Name,
	)
	resp := KataWorkspaceTargetResponse{
		Available: true,
		Repo:      &repoRef,
		ItemType:  db.WorkspaceItemTypeKataTask,
		ItemKey:   db.KataWorkspaceItemKey(metadata),
	}
	existing, err := h.db.GetWorkspaceByItemKeyForProvider(
		ctx,
		target.Provider,
		target.PlatformHost,
		target.Owner,
		target.Name,
		db.WorkspaceItemTypeKataTask,
		resp.ItemKey,
	)
	if err != nil {
		return KataWorkspaceTargetResponse{}, httpapi.Internal("lookup existing Kata workspace: " + err.Error())
	}
	if existing != nil {
		resp.ExistingWorkspace = &workspaceapi.WorkspaceRef{
			ID:     existing.ID,
			Status: existing.Status,
		}
	}
	return resp, nil
}

// WorkspaceTargetForMetadata resolves the workspace action embedded in Kata
// snapshot enrichment.
func (h *Handler) WorkspaceTargetForMetadata(
	ctx context.Context,
	metadata db.WorkspaceKataMetadata,
) (KataWorkspaceTargetResponse, error) {
	return h.kataWorkspaceTargetForMetadata(ctx, metadata)
}

func (h *Handler) createKataWorkspace(
	ctx context.Context,
	input *kataWorkspaceTaskInput,
) (*workspaceapi.CreateWorkspaceOutput, error) {
	if h.workspaces == nil {
		return nil, httpapi.ServiceUnavailable("workspace manager not configured")
	}
	metadata, err := input.Body.metadata()
	if err != nil {
		return nil, err
	}
	resolution, err := h.resolveKataWorkspaceRepoResolution(ctx, metadata)
	if err != nil {
		return nil, err
	}
	target := resolution.Target
	if resolution.Status != "mapped" {
		return nil, httpapi.NotFound(
			httpapi.CodeNotFound,
			"no repository mapping for Kata project",
			map[string]any{"project_uid": metadata.ProjectUID},
		)
	}

	existing, err := h.workspaces.GetByItemKeyForProvider(
		ctx,
		target.Provider,
		target.PlatformHost,
		target.Owner,
		target.Name,
		db.WorkspaceItemTypeKataTask,
		db.KataWorkspaceItemKey(metadata),
	)
	if err != nil {
		return nil, httpapi.Internal("lookup existing Kata workspace: " + err.Error())
	}
	if existing != nil {
		return h.kataWorkspaceCreateOutput(ctx, existing.ID, false)
	}

	ws, err := h.workspaces.CreateKataTask(
		ctx,
		target.Provider,
		target.PlatformHost,
		target.Owner,
		target.Name,
		metadata,
	)
	if err != nil {
		if errors.Is(err, workspace.ErrWorkspaceDuplicate) {
			existing, getErr := h.workspaces.GetByItemKeyForProvider(
				ctx,
				target.Provider,
				target.PlatformHost,
				target.Owner,
				target.Name,
				db.WorkspaceItemTypeKataTask,
				db.KataWorkspaceItemKey(metadata),
			)
			if getErr == nil && existing != nil {
				return h.kataWorkspaceCreateOutput(ctx, existing.ID, false)
			}
			return nil, httpapi.Conflict(httpapi.CodeConflict, "workspace already exists for this Kata task", nil)
		}
		if strings.Contains(err.Error(), "not tracked") {
			return nil, httpapi.NotFound(httpapi.CodeNotFound, err.Error(), nil)
		}
		if strings.Contains(err.Error(), "invalid branch name") {
			return nil, httpapi.Validation("body.short_id", err.Error())
		}
		return nil, httpapi.Internal("create Kata workspace: " + err.Error())
	}

	h.workspaceAPI.RunWorkspaceSetupWithBasePath(ws, target.BasePath)
	return h.kataWorkspaceCreateOutput(ctx, ws.ID, true)
}

func (h *Handler) kataWorkspaceCreateOutput(
	ctx context.Context, workspaceID string, created bool,
) (*workspaceapi.CreateWorkspaceOutput, error) {
	summary, err := h.workspaces.GetSummary(ctx, workspaceID)
	if err != nil {
		return nil, httpapi.Internal("get workspace summary: " + err.Error())
	}
	if summary == nil {
		return nil, httpapi.Internal("workspace summary missing after create")
	}
	resp := h.workspaceAPI.Response(ctx, summary)
	resp.Created = created
	return &workspaceapi.CreateWorkspaceOutput{
		Status: httpStatusAccepted,
		Body:   resp,
	}, nil
}

func (h *Handler) resolveKataWorkspaceRepo(
	ctx context.Context,
	metadata db.WorkspaceKataMetadata,
) (kataResolvedWorkspaceRepo, bool, error) {
	resolution, err := h.resolveKataWorkspaceRepoResolution(ctx, metadata)
	return resolution.Target, resolution.Status == "mapped", err
}

func (h *Handler) resolveKataWorkspaceRepoResolution(
	ctx context.Context,
	metadata db.WorkspaceKataMetadata,
) (kataWorkspaceRepoResolution, error) {
	snapshot := h.configSnapshot()
	repos := snapshot.Repos
	mappings := snapshot.KataProjects

	projects, err := h.db.ListProjects(ctx)
	if err != nil {
		return kataWorkspaceRepoResolution{}, fmt.Errorf("list registered projects for Kata workspace: %w", err)
	}
	for _, candidate := range []struct {
		daemonSpecific bool
		source         string
	}{{true, "manual_daemon"}, {false, "manual_global"}} {
		target, found, valid, err := h.kataManualWorkspaceTarget(
			ctx, repos, projects, mappings, metadata, candidate.daemonSpecific,
		)
		if err != nil {
			return kataWorkspaceRepoResolution{}, err
		}
		if found {
			if !valid {
				return kataWorkspaceRepoResolution{Status: "invalid", Source: candidate.source}, nil
			}
			return kataWorkspaceRepoResolution{Target: target, Status: "mapped", Source: candidate.source}, nil
		}
	}
	repo, matches := kataAutomaticWorkspaceRepo(repos, metadata.ProjectUID, metadata.ProjectName)
	if matches == 1 {
		return kataWorkspaceRepoResolution{Target: kataResolvedRepoFromConfig(repo), Status: "mapped", Source: "configured_clone"}, nil
	}
	if matches > 1 {
		return kataWorkspaceRepoResolution{Status: "ambiguous", Source: "configured_clone"}, nil
	}
	if target, matches := kataAutomaticWorkspaceRepoByRegisteredProjects(
		projects, metadata.ProjectUID, metadata.ProjectName,
	); matches == 1 {
		return kataWorkspaceRepoResolution{Target: target, Status: "mapped", Source: "registered_project"}, nil
	} else if matches > 1 {
		return kataWorkspaceRepoResolution{Status: "ambiguous", Source: "registered_project"}, nil
	}
	tracked, err := h.db.ListRepos(ctx)
	if err != nil {
		return kataWorkspaceRepoResolution{}, fmt.Errorf("list tracked repos for Kata workspace: %w", err)
	}
	if target, matches := h.kataAutomaticWorkspaceRepoByTrackedRepos(repos, tracked, metadata.ProjectName); matches == 1 {
		return kataWorkspaceRepoResolution{Target: target, Status: "mapped", Source: "tracked_repo"}, nil
	} else if matches > 1 {
		return kataWorkspaceRepoResolution{Status: "ambiguous", Source: "tracked_repo"}, nil
	}
	return kataWorkspaceRepoResolution{Status: "unmapped"}, nil
}

func (h *Handler) kataManualWorkspaceTarget(
	ctx context.Context,
	repos []config.Repo,
	projects []db.Project,
	mappings []config.KataProjectRepoMapping,
	metadata db.WorkspaceKataMetadata,
	daemonSpecific bool,
) (kataResolvedWorkspaceRepo, bool, bool, error) {
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
		target, ok := kataResolvedRepoFromMapping(mapping)
		if !ok {
			return kataResolvedWorkspaceRepo{}, true, false, nil
		}
		for _, repo := range repos {
			if !repo.HasNameGlob() && h.kataMappingMatchesRepo(mapping, repo) {
				return kataResolvedRepoFromConfig(repo), true, true, nil
			}
		}
		var registered []kataResolvedWorkspaceRepo
		for _, project := range projects {
			if project.IsStale || project.PlatformIdentity == nil {
				continue
			}
			if kataResolvedRepoKey(kataResolvedRepoFromProject(project)) == kataResolvedRepoKey(target) {
				registered = append(registered, kataResolvedRepoFromProject(project))
			}
		}
		if len(registered) == 1 {
			return registered[0], true, true, nil
		}
		if len(registered) > 1 {
			return kataResolvedWorkspaceRepo{}, true, false, nil
		}
		repo, err := h.db.GetRepoByIdentity(ctx, db.RepoIdentity{
			Platform: target.Provider, PlatformHost: target.PlatformHost,
			Owner: target.Owner, Name: target.Name,
		})
		if err != nil {
			return kataResolvedWorkspaceRepo{}, false, false, err
		}
		return target, true, repo != nil && h.kataTrackedRepoMatchesAnyConfig(*repo, repos), nil
	}
	return kataResolvedWorkspaceRepo{}, false, false, nil
}

func kataResolvedRepoFromMapping(mapping config.KataProjectRepoMapping) (kataResolvedWorkspaceRepo, bool) {
	repoPath := strings.Trim(strings.TrimSpace(mapping.RepoPath), "/")
	cut := strings.LastIndex(repoPath, "/")
	if cut <= 0 || cut == len(repoPath)-1 {
		return kataResolvedWorkspaceRepo{}, false
	}
	return kataResolvedWorkspaceRepo{
		Provider: mapping.Provider, PlatformHost: mapping.PlatformHost,
		Owner: repoPath[:cut], Name: repoPath[cut+1:],
	}, true
}

func kataAutomaticWorkspaceRepoByRegisteredProjects(
	projects []db.Project,
	projectUID string,
	projectName string,
) (kataResolvedWorkspaceRepo, int) {
	if target, matches := kataRegisteredProjectRepoMatches(projects, func(project db.Project) bool {
		metadata, ok := readKataProjectTOML(project.LocalPath)
		return ok && metadata.matchesProjectUID(projectUID)
	}); matches != 0 {
		return target, matches
	}

	name := strings.TrimSpace(projectName)
	if name == "" {
		return kataResolvedWorkspaceRepo{}, 0
	}
	if target, matches := kataRegisteredProjectRepoMatches(projects, func(project db.Project) bool {
		metadata, ok := readKataProjectTOML(project.LocalPath)
		return ok && !metadata.hasIdentifier() && strings.EqualFold(metadata.Name, name)
	}); matches != 0 {
		return target, matches
	}

	return kataRegisteredProjectRepoMatches(projects, func(project db.Project) bool {
		if metadata, ok := readKataProjectTOML(project.LocalPath); ok && metadata.hasAnyProjectMetadata() {
			return false
		}
		identity := project.PlatformIdentity
		return strings.EqualFold(project.DisplayName, name) ||
			strings.EqualFold(identity.Name, name) ||
			strings.EqualFold(identity.Owner+"/"+identity.Name, name)
	})
}

func kataRegisteredProjectRepoMatches(
	projects []db.Project,
	matches func(db.Project) bool,
) (kataResolvedWorkspaceRepo, int) {
	matched := make(map[string]kataResolvedWorkspaceRepo)
	for _, project := range projects {
		if project.IsStale || project.PlatformIdentity == nil || !matches(project) {
			continue
		}
		target := kataResolvedRepoFromProject(project)
		matched[kataResolvedRepoKey(target)+"\x00"+filepath.Clean(project.LocalPath)] = target
	}
	if len(matched) != 1 {
		return kataResolvedWorkspaceRepo{}, len(matched)
	}
	for _, target := range matched {
		return target, 1
	}
	return kataResolvedWorkspaceRepo{}, 0
}

func kataResolvedRepoKey(repo kataResolvedWorkspaceRepo) string {
	return strings.ToLower(repo.Provider) + "\x00" +
		strings.ToLower(repo.PlatformHost) + "\x00" +
		strings.ToLower(repo.Owner) + "\x00" +
		strings.ToLower(repo.Name)
}

func (h *Handler) kataMappingMatchesRepo(mapping config.KataProjectRepoMapping, repo config.Repo) bool {
	if repo.HasNameGlob() {
		return false
	}
	return strings.EqualFold(mapping.Provider, repo.PlatformOrDefault()) &&
		h.samePlatformHost(mapping.PlatformHost, repo.PlatformHostOrDefault()) &&
		strings.EqualFold(mapping.RepoPath, h.configRepoPath(repo))
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

func (h *Handler) kataAutomaticWorkspaceRepoByTrackedRepos(
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
		if !h.kataTrackedRepoMatchesAnyConfig(repo, configured) {
			continue
		}
		if h.kataTrackedRepoHasConfiguredProjectMetadata(repo, configured) {
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

func (h *Handler) kataTrackedRepoMatchesAnyConfig(repo db.Repo, configured []config.Repo) bool {
	for _, raw := range configured {
		if h.kataTrackedRepoMatchesConfig(repo, raw) {
			return true
		}
	}
	return false
}

func (h *Handler) kataTrackedRepoMatchesConfig(repo db.Repo, raw config.Repo) bool {
	if !strings.EqualFold(repo.Platform, raw.PlatformOrDefault()) ||
		!h.samePlatformHost(repo.PlatformHost, raw.PlatformHostOrDefault()) {
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
	return strings.EqualFold(kataTrackedRepoPath(repo), h.configRepoPath(raw))
}

func (h *Handler) kataTrackedRepoHasConfiguredProjectMetadata(repo db.Repo, configured []config.Repo) bool {
	for _, raw := range configured {
		if raw.HasNameGlob() || strings.TrimSpace(raw.WorktreeBasePath) == "" {
			continue
		}
		if !h.kataTrackedRepoMatchesConfig(repo, raw) {
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
	// /dev/zero) and stall or exhaust the kenn-forge process when the worktree
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
		BasePath:     repo.WorktreeBasePath,
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

func kataResolvedRepoFromProject(project db.Project) kataResolvedWorkspaceRepo {
	identity := project.PlatformIdentity
	return kataResolvedWorkspaceRepo{
		Provider:     identity.Platform,
		PlatformHost: identity.Host,
		Owner:        identity.Owner,
		Name:         identity.Name,
		BasePath:     project.LocalPath,
	}
}

func (h *Handler) getKataProjectMappings(
	ctx context.Context,
	input *kataProjectMappingsInput,
) (out *kataProjectMappingsOutput, err error) {
	defer func() {
		if err != nil {
			err = huma.ErrorWithHeaders(err, http.Header{"Vary": []string{kataDaemonHeaderName}})
		}
	}()
	daemon, problem := h.selectKataDaemonForID(input.DaemonID)
	if problem != nil {
		return nil, problem
	}
	client, baseURL, err := h.kataDaemonHTTPClient(daemon)
	if err != nil {
		return nil, httpapi.BadRequest("", "invalid Kata daemon target", map[string]any{"daemon": daemon.ID})
	}
	upstreamCtx, cancel := context.WithTimeout(ctx, kataDaemonReadTimeout)
	result := kataDaemonGet(upstreamCtx, client, daemon, baseURL+"/api/v1/projects")
	cancel()
	if result.err != nil || result.status < http.StatusOK || result.status >= http.StatusMultipleChoices {
		return nil, httpapi.NewProblem(http.StatusBadGateway, httpapi.CodeUpstreamError, "Kata daemon projects read failed", map[string]any{"daemon": daemon.ID})
	}
	var payload struct {
		Projects []struct {
			UID  string `json:"uid"`
			Name string `json:"name"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(result.body, &payload); err != nil {
		return nil, httpapi.NewProblem(http.StatusBadGateway, httpapi.CodeUpstreamError, "Kata daemon returned an unexpected projects payload", map[string]any{"daemon": daemon.ID})
	}
	out = &kataProjectMappingsOutput{Vary: kataDaemonHeaderName}
	out.Body.DaemonID = daemon.ID
	out.Body.Projects = make([]kataProjectMappingDiagnostic, 0, len(payload.Projects))
	for _, project := range payload.Projects {
		uid := strings.TrimSpace(project.UID)
		if uid == "" {
			continue
		}
		resolution, err := h.resolveKataWorkspaceRepoResolution(ctx, db.WorkspaceKataMetadata{
			DaemonID: daemon.ID, ProjectUID: uid, ProjectName: strings.TrimSpace(project.Name),
		})
		if err != nil {
			return nil, httpapi.Internal("resolve Kata project mapping: " + err.Error())
		}
		diagnostic := kataProjectMappingDiagnostic{
			DaemonID: daemon.ID, ProjectUID: uid, ProjectName: strings.TrimSpace(project.Name),
			Status: resolution.Status, Source: resolution.Source,
		}
		if resolution.Status == "mapped" {
			repo := h.repoRefFromParts(
				resolution.Target.Provider, resolution.Target.PlatformHost,
				resolution.Target.Owner, resolution.Target.Name,
			)
			diagnostic.Repo = &repo
		}
		out.Body.Projects = append(out.Body.Projects, diagnostic)
	}
	sort.Slice(out.Body.Projects, func(i, j int) bool {
		return strings.ToLower(out.Body.Projects[i].ProjectName) < strings.ToLower(out.Body.Projects[j].ProjectName)
	})
	out.Body.Targets, err = h.kataMappingTargets(ctx)
	if err != nil {
		return nil, httpapi.Internal("list Kata mapping targets: " + err.Error())
	}
	return out, nil
}

func (h *Handler) kataMappingTargets(ctx context.Context) ([]kataMappingTargetResponse, error) {
	seen := make(map[string]kataMappingTargetResponse)
	configured := h.configSnapshot().Repos
	for _, repo := range configured {
		if repo.HasNameGlob() {
			continue
		}
		target := kataResolvedRepoFromConfig(repo)
		seen[kataResolvedRepoKey(target)] = kataMappingTargetResponse{
			DisplayName: h.configRepoPath(repo),
			Repo:        h.repoRefFromParts(target.Provider, target.PlatformHost, target.Owner, target.Name),
		}
	}
	tracked, err := h.db.ListRepos(ctx)
	if err != nil {
		return nil, err
	}
	for _, repo := range tracked {
		if !h.kataTrackedRepoMatchesAnyConfig(repo, configured) {
			continue
		}
		target := kataResolvedRepoFromDB(repo)
		key := kataResolvedRepoKey(target)
		if _, exists := seen[key]; !exists {
			seen[key] = kataMappingTargetResponse{
				DisplayName: kataTrackedRepoPath(repo),
				Repo:        h.repoRefFromParts(target.Provider, target.PlatformHost, target.Owner, target.Name),
			}
		}
	}
	projects, err := h.db.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		if project.IsStale || project.PlatformIdentity == nil {
			continue
		}
		target := kataResolvedRepoFromProject(project)
		key := kataResolvedRepoKey(target)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = kataMappingTargetResponse{
			DisplayName: project.DisplayName,
			Repo:        h.repoRefFromParts(target.Provider, target.PlatformHost, target.Owner, target.Name),
		}
	}
	result := make([]kataMappingTargetResponse, 0, len(seen))
	for _, target := range seen {
		result = append(result, target)
	}
	sort.Slice(result, func(i, j int) bool {
		left := strings.ToLower(result[i].DisplayName) + "\x00" + result[i].Repo.RepoPath
		right := strings.ToLower(result[j].DisplayName) + "\x00" + result[j].Repo.RepoPath
		return left < right
	})
	return result, nil
}

const httpStatusAccepted = 202

func registerKataWorkspaceAPI(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		OperationID: "get-kata-project-mappings",
		Method:      "GET",
		Path:        "/kata/project-mappings",
		Summary:     "Inspect effective Kata project repository mappings",
		Tags:        []string{"Kata"},
	}, h.getKataProjectMappings)
	huma.Register(api, huma.Operation{
		OperationID:   "create-kata-workspace",
		Method:        "POST",
		Path:          "/kata/workspaces",
		DefaultStatus: httpStatusAccepted,
		Summary:       "Create Kata workspace",
		Tags:          []string{"Kata"},
	}, h.createKataWorkspace)
}

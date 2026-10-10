package workspaceapi

import (
	"context"
	"strings"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/platform"
)

type WorkspaceTargetInput struct {
	Repository *platform.RepositoryIdentity `json:"repository,omitempty" doc:"Verified target repository; omission uses the workspace repository"`
	Type       string                       `json:"type" enum:"pr,issue,kata"`
	Kata       *WorkspaceKataTarget         `json:"kata,omitempty"`
	Number     int                          `json:"number,omitempty"`
	URL        string                       `json:"url,omitempty" doc:"Canonical provider URL to verify before linking"`
}

type WorkspaceTarget struct {
	ID          int64                    `json:"id" doc:"Explicit link ID; zero for an implicit target"`
	Repo        *httpapi.RepoRefResponse `json:"repo,omitempty"`
	Type        string                   `json:"type" enum:"pr,issue,kata"`
	Number      int                      `json:"number"`
	URL         string                   `json:"url"`
	Title       string                   `json:"title"`
	State       string                   `json:"state"`
	Source      string                   `json:"source" enum:"owner,branch,tracked,inherited"`
	Kata        *WorkspaceKataTarget     `json:"kata,omitempty"`
	Unavailable bool                     `json:"unavailable" doc:"Provider metadata is currently unavailable; the link is retained"`
}

type WorkspaceTargetsResponse struct {
	Targets       []WorkspaceTarget `json:"targets"`
	KataAvailable bool              `json:"kata_available"`
}
type WorkspaceTargetMetadata struct{ URL, Title, State string }

// WorkspaceTargetSource reads hub-owned metadata without changing provider state.
type WorkspaceTargetSource interface {
	ReadWorkspaceTarget(context.Context, db.Repo, string, int) (WorkspaceTargetMetadata, error)
}

func (s *Handler) listWorkspaceTargets(ctx context.Context, in *getWorkspaceInput) (*httpapi.BodyOutput[WorkspaceTargetsResponse], error) {
	out, err := s.ListWorkspaceTargetsService(ctx, in.ID)
	return &httpapi.BodyOutput[WorkspaceTargetsResponse]{Body: out}, err
}

type WorkspaceTargetSelection struct {
	Repository platform.RepositoryIdentity `json:"repository"`
	Type       string                      `json:"type" enum:"pr,issue"`
	Number     int                         `json:"number" minimum:"1"`
	Hidden     bool                        `json:"hidden"`
}

type updateWorkspaceTargetInput struct {
	ID   string `path:"id"`
	Body WorkspaceTargetSelection
}

func (s *Handler) updateWorkspaceTarget(ctx context.Context, in *updateWorkspaceTargetInput) (*struct{}, error) {
	ws, err := s.db.GetWorkspace(ctx, in.ID)
	if err != nil {
		return nil, httpapi.Internal("get workspace failed")
	}
	if ws == nil {
		return nil, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, "workspace not found", nil)
	}
	repo, metadata, err := s.ResolveWorkspaceTargetService(ctx, in.Body)
	if err != nil {
		return nil, err
	}
	return nil, s.saveWorkspaceTarget(ctx, in.ID, repo.ID, in.Body, metadata.URL)
}

// ResolveWorkspaceTargetService reads provider-owned identity and display metadata.
// Controllers use it before sending a target to a worker with no provider replicas.
func (s *Handler) ResolveWorkspaceTargetService(ctx context.Context, selection WorkspaceTargetSelection) (db.Repo, WorkspaceTargetMetadata, error) {
	if !selection.Repository.Valid() {
		return db.Repo{}, WorkspaceTargetMetadata{}, httpapi.Validation("body.repository", "verified repository identity is required")
	}
	repo, err := s.db.GetRepositoryByProviderID(ctx, selection.Repository)
	if err != nil {
		return db.Repo{}, WorkspaceTargetMetadata{}, httpapi.Internal("get target repository failed")
	}
	if repo == nil {
		return db.Repo{}, WorkspaceTargetMetadata{}, httpapi.NotFound(httpapi.CodeRepoNotFound, "target repository not found", nil)
	}
	var metadata WorkspaceTargetMetadata
	if !selection.Hidden {
		kind := db.WorkspaceItemTypeIssue
		if selection.Type == "pr" {
			kind = db.WorkspaceItemTypePullRequest
		}
		metadata, err = s.readWorkspaceTarget(ctx, repo.Repository, kind, selection.Number, false)
	}
	return repo.Repository, metadata, err
}

func (s *Handler) saveWorkspaceTarget(ctx context.Context, id string, repoID int64, selection WorkspaceTargetSelection, targetURL string) error {
	kind := db.WorkspaceItemTypeIssue
	if selection.Type == "pr" {
		kind = db.WorkspaceItemTypePullRequest
	}
	_, err := s.db.AddWorkspaceTarget(ctx, db.WorkspaceTarget{
		WorkspaceID: id, RepoID: repoID, ItemType: kind, ItemNumber: selection.Number, URL: targetURL, Hidden: selection.Hidden,
	})
	if err != nil {
		return httpapi.Internal("save workspace target failed")
	}
	s.broadcastWorkspaceStatus(id)
	return nil
}

func (s *Handler) AddWorkspaceTargetService(ctx context.Context, id string, in WorkspaceTargetInput) (WorkspaceTarget, error) {
	if in.Type == "kata" {
		if s.kataTargets == nil {
			return WorkspaceTarget{}, httpapi.ServiceUnavailable("Kata target linking is unavailable")
		}
		if in.Kata == nil || strings.TrimSpace(in.Kata.DaemonID) == "" || strings.TrimSpace(in.Kata.ProjectUID) == "" || strings.TrimSpace(in.Kata.IssueUID) == "" || in.Repository != nil || in.Number != 0 || in.URL != "" {
			return WorkspaceTarget{}, httpapi.Validation("body", "Kata targets require only their daemon, project and task identity")
		}
		target, err := s.kataTargets.AddWorkspaceKataTarget(ctx, id, *in.Kata)
		if err == nil {
			s.broadcastWorkspaceStatus(id)
		}
		return target, err
	}
	if in.Kata != nil {
		return WorkspaceTarget{}, httpapi.Validation("body.kata", "Kata identity cannot accompany a provider target")
	}

	if in.Type != "pr" && in.Type != "issue" || in.Number <= 0 || strings.TrimSpace(in.URL) == "" {
		return WorkspaceTarget{}, httpapi.Validation("body", "target type, positive number and canonical URL are required")
	}
	ws, err := s.db.GetWorkspace(ctx, id)
	if err != nil {
		return WorkspaceTarget{}, httpapi.Internal("get workspace failed")
	}
	if ws == nil {
		return WorkspaceTarget{}, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, "workspace not found", nil)
	}
	var repo *db.ActiveRepo
	if in.Repository == nil {
		repo, err = s.db.GetActiveRepoByID(ctx, ws.RepoID)
	} else {
		if !in.Repository.Valid() {
			return WorkspaceTarget{}, httpapi.Validation("body.repository", "verified repository identity is required")
		}
		repo, err = s.db.GetActiveRepoByProviderID(ctx, in.Repository.Canonical())
	}
	if err != nil {
		return WorkspaceTarget{}, httpapi.Internal("get target repository failed")
	}
	if repo == nil {
		return WorkspaceTarget{}, httpapi.NotFound(httpapi.CodeRepoNotFound, "target repository is unavailable on this host", nil)
	}
	kind := db.WorkspaceItemTypeIssue
	if in.Type == "pr" {
		kind = db.WorkspaceItemTypePullRequest
	}
	metadata, err := s.readWorkspaceTarget(ctx, repo.Repo, kind, in.Number, true)
	if err != nil {
		return WorkspaceTarget{}, err
	}
	if metadata.URL != strings.TrimSpace(in.URL) {
		return WorkspaceTarget{}, httpapi.Validation("body.url", "URL does not match the verified target")
	}
	source := workspaceTargetSource(*ws, repo.ID, kind, in.Number)
	targetID, err := s.db.AddWorkspaceTarget(ctx, db.WorkspaceTarget{WorkspaceID: id, RepoID: repo.ID, ItemType: kind, ItemNumber: in.Number, URL: metadata.URL})
	if err != nil {
		return WorkspaceTarget{}, httpapi.Internal("add workspace target failed")
	}
	s.broadcastWorkspaceStatus(id)
	return workspaceTargetResponse(targetID, repo.Repo, in.Type, in.Number, source, metadata), nil
}

func workspaceTargetSource(ws db.Workspace, repoID int64, kind string, number int) string {
	if ws.RepoID == repoID {
		if ws.ItemType == kind && ws.ItemNumber == number {
			return "owner"
		}
		if kind == db.WorkspaceItemTypePullRequest && ws.AssociatedPRNumber != nil && *ws.AssociatedPRNumber == number {
			return "branch"
		}
	}
	return "tracked"
}

func (s *Handler) ListWorkspaceTargetsService(ctx context.Context, id string) (WorkspaceTargetsResponse, error) {
	out := WorkspaceTargetsResponse{Targets: make([]WorkspaceTarget, 0), KataAvailable: s.KataTargetsAvailable()}
	ws, err := s.db.GetWorkspace(ctx, id)
	if err != nil {
		return out, httpapi.Internal("get workspace failed")
	}
	if ws == nil {
		return out, httpapi.NotFound(httpapi.CodeWorkspaceNotFound, "workspace not found", nil)
	}
	rows, err := s.db.ListWorkspaceTargets(ctx, id)
	if err != nil {
		return out, httpapi.Internal("list workspace targets failed")
	}
	implicit := make([]db.WorkspaceTarget, 0, 2)
	if ws.ItemNumber > 0 && (ws.ItemType == db.WorkspaceItemTypePullRequest || ws.ItemType == db.WorkspaceItemTypeIssue) {
		implicit = append(implicit, db.WorkspaceTarget{RepoID: ws.RepoID, ItemType: ws.ItemType, ItemNumber: ws.ItemNumber})
	}
	if ws.AssociatedPRNumber != nil {
		implicit = append(implicit, db.WorkspaceTarget{RepoID: ws.RepoID, ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: *ws.AssociatedPRNumber})
	}
	seen := make(map[db.WorkspaceSubjectKey]bool)
	for _, row := range append(rows, implicit...) {
		key := db.WorkspaceSubjectKey{RepoID: row.RepoID, ItemType: row.ItemType, ItemNumber: row.ItemNumber}
		if seen[key] {
			continue
		}
		seen[key] = true
		if row.Hidden {
			continue
		}
		repo, err := s.db.GetRepoByID(ctx, row.RepoID)
		if err != nil {
			return out, httpapi.Internal("get target repository failed")
		}
		if repo == nil {
			continue
		}
		kind := "issue"
		if row.ItemType == db.WorkspaceItemTypePullRequest {
			kind = "pr"
		}
		metadata, readErr := s.readWorkspaceTarget(ctx, *repo, row.ItemType, row.ItemNumber, false)
		source := workspaceTargetSource(*ws, row.RepoID, row.ItemType, row.ItemNumber)
		target := workspaceTargetResponse(row.ID, *repo, kind, row.ItemNumber, source, metadata)
		if readErr != nil {
			target.Unavailable = true
			target.URL = row.URL
		}
		out.Targets = append(out.Targets, target)
	}
	if s.kataTargets != nil {
		targets, err := s.kataTargets.ListWorkspaceKataTargets(ctx, id)
		if err != nil {
			return out, err
		}
		out.Targets = append(out.Targets, targets...)
	}
	return out, nil
}

func (s *Handler) RemoveWorkspaceTargetService(ctx context.Context, id, kind string, targetID int64) error {
	if kind == "kata" {
		if s.kataTargets == nil {
			return httpapi.ServiceUnavailable("Kata target linking is unavailable")
		}
		err := s.kataTargets.RemoveWorkspaceKataTarget(ctx, id, targetID)
		if err == nil {
			s.broadcastWorkspaceStatus(id)
		}
		return err
	}
	if kind != "pr" && kind != "issue" {
		return httpapi.Validation("type", "target type is required")
	}
	ws, err := s.db.GetWorkspace(ctx, id)
	if err != nil {
		return httpapi.Internal("get workspace failed")
	}
	if ws == nil {
		return httpapi.NotFound(httpapi.CodeWorkspaceNotFound, "workspace not found", nil)
	}
	itemType := db.WorkspaceItemTypeIssue
	if kind == "pr" {
		itemType = db.WorkspaceItemTypePullRequest
	}
	removed, err := s.db.RemoveWorkspaceTarget(ctx, id, itemType, targetID)
	if err != nil {
		return httpapi.Internal("remove workspace target failed")
	}
	if !removed {
		return httpapi.NotFound(httpapi.CodeNotFound, "explicit workspace target not found", nil)
	}
	s.broadcastWorkspaceStatus(id)
	return nil
}

func workspaceTargetResponse(id int64, repo db.Repo, kind string, number int, source string, metadata WorkspaceTargetMetadata) WorkspaceTarget {
	return WorkspaceTarget{ID: id, Repo: &httpapi.RepoRefResponse{Provider: repo.Platform, PlatformHost: repo.PlatformHost, Key: repo.Key, Owner: repo.Owner, Name: repo.Name, RepoPath: repo.RepoPath}, Type: kind, Number: number, URL: metadata.URL, Title: metadata.Title, State: metadata.State, Source: source}
}

func (s *Handler) readWorkspaceTarget(ctx context.Context, repo db.Repo, kind string, number int, refreshMissing bool) (WorkspaceTargetMetadata, error) {
	active, err := s.db.GetActiveRepoByID(ctx, repo.ID)
	if err != nil {
		return WorkspaceTargetMetadata{}, err
	}
	if active == nil {
		return WorkspaceTargetMetadata{}, httpapi.NotFound(httpapi.CodeRepoNotFound, "target repository is unavailable", nil)
	}
	if s.workspaceTargetSource != nil {
		return s.workspaceTargetSource.ReadWorkspaceTarget(ctx, repo, kind, number)
	}
	if kind == db.WorkspaceItemTypePullRequest && refreshMissing {
		facts, err := s.resolveMergeRequestWorktreeFacts(ctx, repo, db.PlatformIdentity{Platform: repo.Platform, Host: repo.PlatformHost, Owner: repo.Owner, Name: repo.Name}, number)
		return WorkspaceTargetMetadata{URL: facts.URL, Title: facts.Title, State: facts.State}, err
	}
	key := db.WorkspaceSubjectKey{RepoID: repo.ID, ItemType: kind, ItemNumber: number}
	metadata, err := s.db.ListWorkspaceSubjectMetadata(ctx, []db.WorkspaceSubjectKey{key})
	if err != nil {
		return WorkspaceTargetMetadata{}, err
	}
	item, ok := metadata[key]
	if (!ok || item.URL == "") && refreshMissing && s.syncer != nil {
		if err := s.refreshWorkspaceIssue(ctx, repo.ID, repoProviderKind(repo), repo.PlatformHost, repo.Owner, repo.Name, number); err != nil {
			return WorkspaceTargetMetadata{}, err
		}
		metadata, err = s.db.ListWorkspaceSubjectMetadata(ctx, []db.WorkspaceSubjectKey{key})
		if err != nil {
			return WorkspaceTargetMetadata{}, err
		}
		item, ok = metadata[key]
	}
	if !ok || item.URL == "" {
		return WorkspaceTargetMetadata{}, httpapi.NotFound(httpapi.CodeNotFound, "target not found; sync it before linking", nil)
	}
	return WorkspaceTargetMetadata{URL: item.URL, Title: item.Title, State: item.State}, nil
}

// WorkspaceKataTarget keeps Kata authority separate from provider identities.
type WorkspaceKataTarget struct {
	DaemonID   string `json:"daemon_id"`
	ProjectUID string `json:"project_uid"`
	IssueUID   string `json:"issue_uid"`
	Reference  string `json:"reference,omitempty"`
}

type WorkspaceKataTargets interface {
	WorkspaceKataTargetsAvailable() bool
	ListWorkspaceKataTargets(context.Context, string) ([]WorkspaceTarget, error)
	AddWorkspaceKataTarget(context.Context, string, WorkspaceKataTarget) (WorkspaceTarget, error)
	RemoveWorkspaceKataTarget(context.Context, string, int64) error
}

// SetKataTargets connects the separate Kata domain during server composition.
func (s *Handler) SetKataTargets(source WorkspaceKataTargets) { s.kataTargets = source }

func (s *Handler) KataTargetsAvailable() bool {
	return s.kataTargets != nil && s.kataTargets.WorkspaceKataTargetsAvailable()
}

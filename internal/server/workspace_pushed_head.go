package server

import (
	"context"
	"strconv"

	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/workspace"
)

type workspacePushedHeadChangedPayload struct {
	WorkspaceID  string `json:"workspace_id"`
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	RepoPath     string `json:"repo_path"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Number       int    `json:"number"`
	OldSHA       string `json:"old_sha"`
	NewSHA       string `json:"new_sha"`
	Remote       string `json:"remote"`
	Branch       string `json:"branch"`
	TrackingRef  string `json:"tracking_ref"`
	ObservedAt   string `json:"observed_at"`
}

type workspacePRAssociatedPayload struct {
	WorkspaceID  string `json:"workspace_id"`
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	RepoPath     string `json:"repo_path"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	IssueNumber  int    `json:"issue_number"`
	PRNumber     int    `json:"pr_number"`
	AssociatedAt string `json:"associated_at"`
}

type workspacePRRefreshQueuedPayload struct {
	WorkspaceID  string `json:"workspace_id"`
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	RepoPath     string `json:"repo_path"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Number       int    `json:"number"`
	HeadSHA      string `json:"head_sha"`
	Priority     string `json:"priority"`
	QueuedAt     string `json:"queued_at"`
}

type prDetailRefreshedPayload struct {
	Provider     string   `json:"provider"`
	PlatformHost string   `json:"platform_host"`
	RepoPath     string   `json:"repo_path"`
	Owner        string   `json:"owner"`
	Name         string   `json:"name"`
	Number       int      `json:"number"`
	HeadSHA      string   `json:"head_sha"`
	SyncedAt     string   `json:"synced_at"`
	Warnings     []string `json:"warnings"`
}

type prCIRefreshQueuedPayload struct {
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	RepoPath     string `json:"repo_path"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Number       int    `json:"number"`
	HeadSHA      string `json:"head_sha"`
	Priority     string `json:"priority"`
	QueuedAt     string `json:"queued_at"`
}

type prCIRefreshedPayload struct {
	Provider     string   `json:"provider"`
	PlatformHost string   `json:"platform_host"`
	RepoPath     string   `json:"repo_path"`
	Owner        string   `json:"owner"`
	Name         string   `json:"name"`
	Number       int      `json:"number"`
	HeadSHA      string   `json:"head_sha"`
	RefreshedAt  string   `json:"refreshed_at"`
	Warnings     []string `json:"warnings"`
}

func (s *Server) enqueueWorkspacePushedHeadRefresh(change workspace.PushedHeadUpdate) bool {
	if s.syncer == nil {
		return false
	}
	key := pushedHeadPRKey(change)
	attrs := []any{
		"type", "pr",
		"provider", string(change.Provider),
		"platform_host", change.PlatformHost,
		"repo_path", change.RepoPath,
		"owner", change.Owner,
		"name", change.Name,
		"number", change.Number,
	}
	queuedAt := s.now().UTC()
	started := s.enqueueDetailSyncWithCompletion(
		key,
		attrs,
		func(ctx context.Context) error {
			return s.syncer.SyncMROnProvider(
				ctx,
				change.Provider,
				change.PlatformHost,
				change.Owner,
				change.Name,
				change.Number,
			)
		},
		func(ctx context.Context) {
			if s.workspacePushedHeadObserver != nil {
				s.workspacePushedHeadObserver.MarkRefreshSucceeded(change, s.now().UTC())
			}
			s.broadcastPRDetailRefreshed(ctx, change)
			s.maybeEnqueuePushedHeadCIRefresh(ctx, change)
		},
	)
	if !started {
		return false
	}
	if s.workspacePushedHeadObserver != nil {
		s.workspacePushedHeadObserver.MarkRefreshEnqueued(change, queuedAt)
	}
	s.hub.Broadcast(Event{
		Type: "workspace_pr_refresh_queued",
		Data: workspacePRRefreshQueuedPayload{
			WorkspaceID:  change.WorkspaceID,
			Provider:     string(change.Provider),
			PlatformHost: change.PlatformHost,
			RepoPath:     change.RepoPath,
			Owner:        change.Owner,
			Name:         change.Name,
			Number:       change.Number,
			HeadSHA:      change.NewSHA,
			Priority:     "high",
			QueuedAt:     formatUTCRFC3339(queuedAt),
		},
	})
	return true
}

func (s *Server) broadcastPRDetailRefreshed(ctx context.Context, change workspace.PushedHeadUpdate) {
	repo, mr := s.lookupPushedHeadMR(ctx, change)
	headSHA := change.NewSHA
	if mr != nil && mr.PlatformHeadSHA != "" {
		headSHA = mr.PlatformHeadSHA
	}
	payload := prDetailRefreshedPayload{
		Provider:     string(change.Provider),
		PlatformHost: change.PlatformHost,
		RepoPath:     change.RepoPath,
		Owner:        change.Owner,
		Name:         change.Name,
		Number:       change.Number,
		HeadSHA:      headSHA,
		SyncedAt:     formatUTCRFC3339(s.now().UTC()),
		Warnings:     []string{},
	}
	if repo != nil {
		payload.Provider = repo.Platform
		payload.PlatformHost = repo.PlatformHost
		payload.RepoPath = repo.RepoPath
		payload.Owner = repo.Owner
		payload.Name = repo.Name
	}
	s.hub.Broadcast(Event{Type: "pr_detail_refreshed", Data: payload})
}

func (s *Server) maybeEnqueuePushedHeadCIRefresh(ctx context.Context, change workspace.PushedHeadUpdate) {
	if s.syncer == nil {
		return
	}
	repo, mr := s.lookupPushedHeadMR(ctx, change)
	if repo == nil || mr == nil || !pushedHeadMRNeedsCIRefresh(mr.CIStatus, mr.CIHadPending, mr.WorkflowApprovalRequired) {
		return
	}
	headSHA := mr.PlatformHeadSHA
	if headSHA == "" {
		headSHA = change.NewSHA
	}
	queuedAt := s.now().UTC()
	key := "pr-ci:" + string(change.Provider) + ":" + change.PlatformHost + ":" + change.RepoPath + "#" + strconv.Itoa(change.Number)
	started := s.enqueueDetailSyncWithCompletion(
		key,
		[]any{"type", "pr-ci", "provider", string(change.Provider), "platform_host", change.PlatformHost, "repo_path", change.RepoPath, "number", change.Number},
		func(ctx context.Context) error {
			_, err := s.syncer.RefreshMRCIStatusOnProvider(
				ctx,
				ghclient.RepoRef{
					Platform:           repoProviderKind(*repo),
					Owner:              repo.Owner,
					Name:               repo.Name,
					PlatformHost:       repoProviderHost(*repo),
					RepoPath:           repo.RepoPath,
					PlatformExternalID: repo.PlatformRepoID,
					WebURL:             repo.WebURL,
					CloneURL:           repo.CloneURL,
					DefaultBranch:      repo.DefaultBranch,
				},
				repo.ID,
				change.Number,
				headSHA,
			)
			return err
		},
		func(context.Context) {
			s.hub.Broadcast(Event{
				Type: "pr_ci_refreshed",
				Data: prCIRefreshedPayload{
					Provider:     string(repoProviderKind(*repo)),
					PlatformHost: repoProviderHost(*repo),
					RepoPath:     repo.RepoPath,
					Owner:        repo.Owner,
					Name:         repo.Name,
					Number:       change.Number,
					HeadSHA:      headSHA,
					RefreshedAt:  formatUTCRFC3339(s.now().UTC()),
					Warnings:     []string{},
				},
			})
		},
	)
	if !started {
		return
	}
	s.hub.Broadcast(Event{
		Type: "pr_ci_refresh_queued",
		Data: prCIRefreshQueuedPayload{
			Provider:     string(repoProviderKind(*repo)),
			PlatformHost: repoProviderHost(*repo),
			RepoPath:     repo.RepoPath,
			Owner:        repo.Owner,
			Name:         repo.Name,
			Number:       change.Number,
			HeadSHA:      headSHA,
			Priority:     "low",
			QueuedAt:     formatUTCRFC3339(queuedAt),
		},
	})
}

func (s *Server) lookupPushedHeadMR(ctx context.Context, change workspace.PushedHeadUpdate) (*db.Repo, *db.MergeRequest) {
	repo, err := s.db.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     string(change.Provider),
		PlatformHost: change.PlatformHost,
		RepoPath:     change.RepoPath,
	})
	if err != nil || repo == nil {
		return nil, nil
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, change.Number)
	if err != nil {
		return repo, nil
	}
	return repo, mr
}

func pushedHeadMRNeedsCIRefresh(status string, hadPending, approvalRequired bool) bool {
	return status == "pending" || hadPending || approvalRequired
}

func pushedHeadPRKey(change workspace.PushedHeadUpdate) string {
	return "pr:" + string(change.Provider) + ":" + change.PlatformHost + ":" + change.RepoPath + "#" + strconv.Itoa(change.Number)
}

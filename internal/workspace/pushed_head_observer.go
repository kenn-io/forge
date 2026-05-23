package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

type remoteHeadKey struct {
	WorkspaceID  string
	Provider     platform.Kind
	PlatformHost string
	RepoPath     string
	ItemType     string
	ItemNumber   int
	RemoteName   string
	BranchName   string
	TrackingRef  string
}

type remoteHeadObservation struct {
	SHA                    string
	ObservedAt             time.Time
	LastRefreshEnqueuedAt  time.Time
	LastRefreshSucceededAt time.Time
}

type PushedHeadUpdate struct {
	WorkspaceID  string
	Provider     platform.Kind
	PlatformHost string
	RepoPath     string
	Owner        string
	Name         string
	Number       int
	OldSHA       string
	NewSHA       string
	RemoteName   string
	BranchName   string
	TrackingRef  string
	ObservedAt   time.Time
}

type WorkspacePRAssociation struct {
	WorkspaceID  string
	Provider     platform.Kind
	PlatformHost string
	RepoPath     string
	Owner        string
	Name         string
	IssueNumber  int
	PRNumber     int
	AssociatedAt time.Time
}

type PushedHeadPassResult struct {
	Associations []WorkspacePRAssociation
	HeadChanges  []PushedHeadUpdate
}

type remoteHeadGitReader interface {
	BranchName(ctx context.Context, dir string) (string, error)
	UpstreamState(ctx context.Context, dir, branch string) (upstreamState, error)
	RemoteTrackingSHA(ctx context.Context, dir, remote, branch string) (string, string, bool, error)
}

const (
	pushedHeadGitTimeout           = 2 * time.Second
	pushedHeadRefreshRetryInterval = 30 * time.Second
)

type gitRemoteHeadReader struct{}

func (gitRemoteHeadReader) BranchName(ctx context.Context, dir string) (string, error) {
	gitCtx, cancel := context.WithTimeout(ctx, pushedHeadGitTimeout)
	defer cancel()
	return gitBranchName(gitCtx, dir)
}

func (gitRemoteHeadReader) UpstreamState(ctx context.Context, dir, branch string) (upstreamState, error) {
	gitCtx, cancel := context.WithTimeout(ctx, pushedHeadGitTimeout)
	defer cancel()
	return gitUpstreamState(gitCtx, dir, branch)
}

func (gitRemoteHeadReader) RemoteTrackingSHA(ctx context.Context, dir, remote, branch string) (string, string, bool, error) {
	trackingRef := "refs/remotes/" + remote + "/" + branch
	gitCtx, cancel := context.WithTimeout(ctx, pushedHeadGitTimeout)
	defer cancel()
	out, err := gitOutput(gitCtx, dir, "rev-parse", "--verify", "--quiet", trackingRef+"^{commit}")
	if err != nil {
		return "", trackingRef, false, nil
	}
	return strings.TrimSpace(out), trackingRef, true, nil
}

type PushedHeadObserver struct {
	db       *db.DB
	monitor  *PRMonitor
	git      remoteHeadGitReader
	now      func() time.Time
	mu       sync.Mutex
	observed map[remoteHeadKey]remoteHeadObservation
	failures map[string]int
}

func NewPushedHeadObserver(database *db.DB) *PushedHeadObserver {
	return &PushedHeadObserver{
		db:       database,
		monitor:  NewPRMonitor(database),
		git:      gitRemoteHeadReader{},
		now:      time.Now,
		observed: make(map[remoteHeadKey]remoteHeadObservation),
		failures: make(map[string]int),
	}
}

func (o *PushedHeadObserver) SetGitReaderForTest(reader remoteHeadGitReader) {
	o.git = reader
}

func (o *PushedHeadObserver) SetNowForTest(now func() time.Time) {
	o.now = now
}

func (o *PushedHeadObserver) MarkRefreshEnqueued(update PushedHeadUpdate, at time.Time) {
	key := update.remoteHeadKey()
	o.mu.Lock()
	defer o.mu.Unlock()
	obs := o.observed[key]
	obs.SHA = update.NewSHA
	obs.LastRefreshEnqueuedAt = at
	o.observed[key] = obs
}

func (o *PushedHeadObserver) MarkRefreshSucceeded(update PushedHeadUpdate, at time.Time) {
	key := update.remoteHeadKey()
	o.mu.Lock()
	defer o.mu.Unlock()
	obs := o.observed[key]
	obs.SHA = update.NewSHA
	obs.LastRefreshSucceededAt = at
	o.observed[key] = obs
}

func (u PushedHeadUpdate) remoteHeadKey() remoteHeadKey {
	return remoteHeadKey{
		WorkspaceID:  u.WorkspaceID,
		Provider:     u.Provider,
		PlatformHost: u.PlatformHost,
		RepoPath:     u.RepoPath,
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   u.Number,
		RemoteName:   u.RemoteName,
		BranchName:   u.BranchName,
		TrackingRef:  u.TrackingRef,
	}
}

func (o *PushedHeadObserver) RunOnce(ctx context.Context) (PushedHeadPassResult, error) {
	workspaces, err := o.db.ListWorkspaces(ctx)
	if err != nil {
		return PushedHeadPassResult{}, fmt.Errorf("list workspaces: %w", err)
	}

	result := PushedHeadPassResult{}
	trackingCache := make(map[string]trackingLookup)
	for i := range workspaces {
		ws := workspaces[i]
		if !pushedHeadWorkspaceEligible(&ws) {
			continue
		}

		assoc, repo, mr, ok, err := o.resolveWorkspacePR(ctx, &ws)
		if err != nil {
			o.recordFailure(ws.ID, err)
			continue
		}
		if assoc != nil {
			result.Associations = append(result.Associations, *assoc)
		}
		if !ok || repo == nil || mr == nil {
			continue
		}

		update, changed, observeErr := o.observeWorkspacePR(ctx, &ws, *repo, *mr, trackingCache)
		if observeErr != nil {
			o.recordFailure(ws.ID, observeErr)
			continue
		}
		o.clearFailure(ws.ID)
		if changed {
			result.HeadChanges = append(result.HeadChanges, update)
		}
	}
	return result, nil
}

func pushedHeadWorkspaceEligible(ws *Workspace) bool {
	if ws == nil || ws.Status != "ready" || strings.TrimSpace(ws.WorktreePath) == "" {
		return false
	}
	if strings.TrimSpace(ws.PlatformHost) == "" || strings.TrimSpace(ws.RepoOwner) == "" || strings.TrimSpace(ws.RepoName) == "" {
		return false
	}
	return ws.ItemType == db.WorkspaceItemTypePullRequest || ws.ItemType == db.WorkspaceItemTypeIssue
}

func (o *PushedHeadObserver) resolveWorkspacePR(ctx context.Context, ws *Workspace) (*WorkspacePRAssociation, *db.Repo, *db.MergeRequest, bool, error) {
	repo, err := o.db.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     workspaceProvider(ws),
		PlatformHost: ws.PlatformHost,
		Owner:        ws.RepoOwner,
		Name:         ws.RepoName,
	})
	if err != nil {
		return nil, nil, nil, false, fmt.Errorf("get repo: %w", err)
	}
	if repo == nil {
		return nil, nil, nil, false, nil
	}

	prNumber := 0
	var assoc *WorkspacePRAssociation
	switch ws.ItemType {
	case db.WorkspaceItemTypePullRequest:
		prNumber = ws.ItemNumber
	case db.WorkspaceItemTypeIssue:
		if ws.AssociatedPRNumber != nil {
			prNumber = *ws.AssociatedPRNumber
		} else {
			detected, ok, err := o.monitor.detectAssociatedPR(ctx, ws)
			if err != nil {
				return nil, repo, nil, false, err
			}
			if !ok {
				return nil, repo, nil, false, nil
			}
			changed, err := o.db.SetWorkspaceAssociatedPRNumberIfNull(ctx, ws.ID, detected)
			if err != nil {
				return nil, repo, nil, false, fmt.Errorf("set associated PR: %w", err)
			}
			prNumber = detected
			if changed {
				assoc = &WorkspacePRAssociation{
					WorkspaceID:  ws.ID,
					Provider:     workspaceProviderKind(ws),
					PlatformHost: repoProviderHost(*repo),
					RepoPath:     repo.RepoPath,
					Owner:        repo.Owner,
					Name:         repo.Name,
					IssueNumber:  ws.ItemNumber,
					PRNumber:     detected,
					AssociatedAt: o.now().UTC(),
				}
			}
		}
	default:
		return nil, repo, nil, false, nil
	}
	if prNumber == 0 {
		return assoc, repo, nil, false, nil
	}

	mr, err := o.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, prNumber)
	if err != nil {
		return assoc, repo, nil, false, fmt.Errorf("get merge request: %w", err)
	}
	if mr == nil || mr.State != db.MergeRequestStateOpen {
		return assoc, repo, nil, false, nil
	}
	return assoc, repo, mr, true, nil
}

type trackingLookup struct {
	sha string
	ref string
	ok  bool
	err error
}

func (o *PushedHeadObserver) observeWorkspacePR(ctx context.Context, ws *Workspace, repo db.Repo, mr db.MergeRequest, trackingCache map[string]trackingLookup) (PushedHeadUpdate, bool, error) {
	branch, err := o.git.BranchName(ctx, ws.WorktreePath)
	if err != nil {
		return PushedHeadUpdate{}, false, err
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return PushedHeadUpdate{}, false, nil
	}

	upstream, err := o.git.UpstreamState(ctx, ws.WorktreePath, branch)
	if err != nil {
		return PushedHeadUpdate{}, false, err
	}
	if !upstream.hasTracking || upstream.remoteName == "" || upstream.branchName == "" {
		slog.Debug("workspace pushed-head observer missing upstream", "workspace_id", ws.ID, "branch", branch)
		return PushedHeadUpdate{}, false, nil
	}
	if upstream.branchName != mr.HeadBranch {
		return PushedHeadUpdate{}, false, nil
	}

	cacheKey := ws.WorktreePath + "\x00" + upstream.remoteName + "\x00" + upstream.branchName
	lookup, cached := trackingCache[cacheKey]
	if !cached {
		sha, ref, ok, err := o.git.RemoteTrackingSHA(ctx, ws.WorktreePath, upstream.remoteName, upstream.branchName)
		lookup = trackingLookup{sha: strings.TrimSpace(sha), ref: ref, ok: ok, err: err}
		trackingCache[cacheKey] = lookup
	}
	if lookup.err != nil {
		return PushedHeadUpdate{}, false, lookup.err
	}
	if !lookup.ok || lookup.sha == "" {
		slog.Debug("workspace pushed-head observer missing tracking ref", "workspace_id", ws.ID, "remote", upstream.remoteName, "branch", upstream.branchName)
		return PushedHeadUpdate{}, false, nil
	}

	provider := repoProviderKind(repo)
	host := repoProviderHost(repo)
	observedAt := o.now().UTC()
	key := remoteHeadKey{
		WorkspaceID:  ws.ID,
		Provider:     provider,
		PlatformHost: host,
		RepoPath:     repo.RepoPath,
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   mr.Number,
		RemoteName:   upstream.remoteName,
		BranchName:   upstream.branchName,
		TrackingRef:  lookup.ref,
	}
	update := PushedHeadUpdate{
		WorkspaceID:  ws.ID,
		Provider:     provider,
		PlatformHost: host,
		RepoPath:     repo.RepoPath,
		Owner:        repo.Owner,
		Name:         repo.Name,
		Number:       mr.Number,
		NewSHA:       lookup.sha,
		RemoteName:   upstream.remoteName,
		BranchName:   upstream.branchName,
		TrackingRef:  lookup.ref,
		ObservedAt:   observedAt,
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	prior, seen := o.observed[key]
	if !seen {
		o.observed[key] = remoteHeadObservation{SHA: lookup.sha, ObservedAt: observedAt}
		providerSHA := strings.TrimSpace(mr.PlatformHeadSHA)
		if providerSHA == "" || strings.EqualFold(lookup.sha, providerSHA) {
			return PushedHeadUpdate{}, false, nil
		}
		update.OldSHA = providerSHA
		return update, true, nil
	}
	if strings.EqualFold(prior.SHA, lookup.sha) {
		prior.ObservedAt = observedAt
		o.observed[key] = prior
		providerSHA := strings.TrimSpace(mr.PlatformHeadSHA)
		if providerSHA == "" || strings.EqualFold(providerSHA, lookup.sha) {
			return PushedHeadUpdate{}, false, nil
		}
		if !prior.LastRefreshEnqueuedAt.IsZero() && observedAt.Sub(prior.LastRefreshEnqueuedAt) < pushedHeadRefreshRetryInterval {
			return PushedHeadUpdate{}, false, nil
		}
		update.OldSHA = providerSHA
		return update, true, nil
	}
	update.OldSHA = prior.SHA
	prior.SHA = lookup.sha
	prior.ObservedAt = observedAt
	o.observed[key] = prior
	return update, true, nil
}

func (o *PushedHeadObserver) recordFailure(workspaceID string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failures[workspaceID]++
	if o.failures[workspaceID] > 1 {
		slog.Warn("workspace pushed-head observer git inspection failed", "workspace_id", workspaceID, "err", err)
		return
	}
	slog.Debug("workspace pushed-head observer git inspection failed", "workspace_id", workspaceID, "err", err)
}

func (o *PushedHeadObserver) clearFailure(workspaceID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.failures, workspaceID)
}

func workspaceProvider(ws *Workspace) string {
	provider := strings.TrimSpace(ws.Platform)
	if provider == "" {
		return string(platform.KindGitHub)
	}
	return provider
}

func workspaceProviderKind(ws *Workspace) platform.Kind {
	return platform.Kind(workspaceProvider(ws))
}

func repoProviderKind(repo db.Repo) platform.Kind {
	if strings.TrimSpace(repo.Platform) == "" {
		return platform.KindGitHub
	}
	return platform.Kind(repo.Platform)
}

func repoProviderHost(repo db.Repo) string {
	if strings.TrimSpace(repo.PlatformHost) != "" {
		return repo.PlatformHost
	}
	if host, ok := platform.DefaultHost(repoProviderKind(repo)); ok {
		return host
	}
	return platform.DefaultGitHubHost
}

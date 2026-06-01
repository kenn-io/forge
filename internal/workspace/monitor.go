package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"

	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
)

type PRAssociationUpdate struct {
	WorkspaceID string
	PRNumber    int
}

type PRMonitor struct {
	db *db.DB
}

func NewPRMonitor(database *db.DB) *PRMonitor {
	return &PRMonitor{db: database}
}

func (m *PRMonitor) RunOnce(
	ctx context.Context,
) ([]PRAssociationUpdate, error) {
	workspaces, err := m.db.ListWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}

	var updates []PRAssociationUpdate
	for i := range workspaces {
		ws := workspaces[i]
		if !workspacePRMonitorEligible(&ws) {
			continue
		}

		prNumber, ok, detectErr := m.detectAssociatedPR(ctx, &ws)
		if detectErr != nil {
			slog.Warn(
				"workspace PR monitor git inspection failed",
				"workspace_id", ws.ID,
				"path", ws.WorktreePath,
				"err", detectErr,
			)
			continue
		}
		if !ok {
			continue
		}

		changed, err := m.db.SetWorkspaceAssociatedPRNumberIfNull(
			ctx, ws.ID, prNumber,
		)
		if err != nil {
			slog.Warn(
				"workspace PR monitor persistence failed",
				"workspace_id", ws.ID,
				"pr_number", prNumber,
				"err", err,
			)
			continue
		}
		if changed {
			updates = append(updates, PRAssociationUpdate{
				WorkspaceID: ws.ID,
				PRNumber:    prNumber,
			})
		}
	}

	return updates, nil
}

func workspacePRMonitorEligible(ws *Workspace) bool {
	return ws != nil &&
		ws.ItemType == db.WorkspaceItemTypeIssue &&
		ws.AssociatedPRNumber == nil &&
		ws.Status == "ready" &&
		strings.TrimSpace(ws.WorktreePath) != ""
}

type upstreamState struct {
	branchName    string
	remoteName    string
	remoteURL     string
	hasTracking   bool
	allowFallback bool
}

func (m *PRMonitor) detectAssociatedPR(
	ctx context.Context,
	ws *Workspace,
) (int, bool, error) {
	currentBranch, err := gitBranchName(ctx, ws.WorktreePath)
	if err != nil {
		return 0, false, err
	}
	if currentBranch == "" {
		return 0, false, nil
	}
	// Skip while the workspace is still on its managed issue branch:
	// no associated PR can exist yet. The bare-form fallback only
	// applies when the workspace pre-dates the slug feature (empty
	// GitHeadRef); otherwise a user who hand-checks-out the legacy
	// bare branch name on a slug-style workspace would silently
	// suppress PR detection for an unrelated branch.
	managedBranch := ws.GitHeadRef
	if managedBranch == "" {
		managedBranch = issueWorkspaceBranch(ws.ItemNumber)
	}
	if currentBranch == managedBranch {
		return 0, false, nil
	}

	candidates, err := m.db.ListMergeRequests(ctx, db.ListMergeRequestsOpts{
		PlatformHost: ws.PlatformHost,
		RepoOwner:    ws.RepoOwner,
		RepoName:     ws.RepoName,
		State:        "open",
	})
	if err != nil {
		return 0, false, fmt.Errorf("list merge requests: %w", err)
	}

	upstream, err := gitUpstreamState(ctx, ws.WorktreePath, currentBranch)
	if err != nil {
		return 0, false, err
	}
	if upstream.hasTracking {
		if prNumber, ok := selectPRByUpstream(candidates, upstream); ok {
			return prNumber, true, nil
		}
		if !upstream.allowFallback {
			return 0, false, nil
		}
	}

	headSHA, err := gitHeadSHA(ctx, ws.WorktreePath)
	if err != nil {
		return 0, false, err
	}
	if prNumber, ok := selectPRByLocalBranch(candidates, currentBranch, headSHA); ok {
		return prNumber, true, nil
	}
	return 0, false, nil
}

func selectPRByUpstream(
	candidates []db.MergeRequest,
	upstream upstreamState,
) (int, bool) {
	if upstream.branchName == "" {
		return 0, false
	}

	remoteRepo := normalizeCloneRepoIdentity(upstream.remoteURL)
	if strings.TrimSpace(upstream.remoteURL) != "" && remoteRepo == "" {
		return 0, false
	}
	matches := make([]db.MergeRequest, 0, len(candidates))
	for i := range candidates {
		candidate := candidates[i]
		if candidate.HeadBranch != upstream.branchName {
			continue
		}
		candidateRepo := normalizeCloneRepoIdentity(candidate.HeadRepoCloneURL)
		if remoteRepo != "" {
			if candidateRepo == "" || candidateRepo != remoteRepo {
				continue
			}
		}
		matches = append(matches, candidate)
	}
	if len(matches) != 1 {
		return 0, false
	}
	return matches[0].Number, true
}

func selectPRByLocalBranch(
	candidates []db.MergeRequest,
	currentBranch, currentHeadSHA string,
) (int, bool) {
	currentHeadSHA = strings.TrimSpace(currentHeadSHA)
	if currentBranch == "" || currentHeadSHA == "" {
		return 0, false
	}

	matches := make([]db.MergeRequest, 0, len(candidates))
	for i := range candidates {
		candidate := candidates[i]
		if candidate.HeadBranch == currentBranch &&
			strings.EqualFold(candidate.PlatformHeadSHA, currentHeadSHA) {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return 0, false
	}
	return matches[0].Number, true
}

func gitBranchName(
	ctx context.Context,
	dir string,
) (string, error) {
	out, err := gitOutput(ctx, dir, "branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("git branch --show-current: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func gitHeadSHA(
	ctx context.Context,
	dir string,
) (string, error) {
	out, err := gitOutput(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func gitUpstreamState(
	ctx context.Context,
	dir, branch string,
) (upstreamState, error) {
	state := upstreamState{}
	remoteName, remoteErr := gitConfigValue(
		ctx, dir, "branch."+branch+".remote",
	)
	mergeRef, mergeErr := gitConfigValue(
		ctx, dir, "branch."+branch+".merge",
	)
	if remoteErr != nil || mergeErr != nil {
		state.allowFallback = true
		return state, nil
	}

	state.hasTracking = true
	state.allowFallback = false
	state.remoteName = remoteName
	state.branchName = strings.TrimPrefix(mergeRef, "refs/heads/")
	remoteURL, err := gitRemoteURL(ctx, dir, remoteName)
	if err != nil {
		return state, fmt.Errorf("git remote get-url %q: %w", remoteName, err)
	}
	state.remoteURL = remoteURL
	return state, nil
}

func gitConfigValue(
	ctx context.Context,
	dir, key string,
) (string, error) {
	out, err := gitOutput(ctx, dir, "config", "--get", key)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func gitRemoteURL(
	ctx context.Context,
	dir, remote string,
) (string, error) {
	out, err := gitOutput(ctx, dir, "remote", "get-url", remote)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func gitOutput(
	ctx context.Context,
	dir string,
	args ...string,
) (string, error) {
	cmd := gitcmd.New().Command(ctx, dir, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func normalizeCloneRepoIdentity(cloneURL string) string {
	cloneURL = strings.TrimSpace(cloneURL)
	if cloneURL == "" {
		return ""
	}
	if !strings.Contains(cloneURL, "://") && !strings.Contains(cloneURL, "@") {
		return ""
	}
	host := normalizeCloneURLHost(cloneURL)
	if host == "" {
		return ""
	}
	fullName := ghclient.ParseHeadRepoFullName(cloneURL)
	if fullName == "" {
		return ""
	}
	return strings.ToLower(host + "/" + fullName)
}

func normalizeCloneURLHost(cloneURL string) string {
	if strings.Contains(cloneURL, "://") {
		parsed, err := url.Parse(cloneURL)
		if err != nil {
			return ""
		}
		return normalizeHostPortIdentity(
			parsed.Scheme, parsed.Hostname(), parsed.Port(),
		)
	}
	beforePath, _, ok := strings.Cut(cloneURL, ":")
	if !ok {
		return ""
	}
	_, host, ok := strings.Cut(beforePath, "@")
	if !ok {
		return ""
	}
	return strings.ToLower(host)
}

func normalizePlatformHostIdentity(platformHost string) string {
	platformHost = strings.TrimSpace(platformHost)
	if platformHost == "" {
		return ""
	}
	if strings.Contains(platformHost, "://") {
		parsed, err := url.Parse(platformHost)
		if err != nil {
			return ""
		}
		return normalizeHostPortIdentity(
			parsed.Scheme, parsed.Hostname(), parsed.Port(),
		)
	}
	host, port, err := net.SplitHostPort(platformHost)
	if err == nil {
		return normalizeHostPortIdentity("https", host, port)
	}
	return strings.ToLower(platformHost)
}

func normalizeHostPortIdentity(scheme, host, port string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return ""
	}
	if port == "" || isDefaultCloneURLPort(scheme, port) {
		return host
	}
	return strings.ToLower(net.JoinHostPort(host, port))
}

func isDefaultCloneURLPort(scheme, port string) bool {
	switch strings.ToLower(scheme) {
	case "http":
		return port == "80"
	case "https":
		return port == "443"
	case "ssh":
		return port == "22"
	default:
		return false
	}
}

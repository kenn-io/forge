package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
)

const (
	deferredMergePollInterval = time.Minute
	deferredMergeMaxWait      = 24 * time.Hour
)

type deferredMergeCheckKey struct {
	App  string
	Name string
}

type deferredMergeCompletedPayload struct {
	Provider     string `json:"provider"`
	PlatformHost string `json:"platform_host"`
	RepoPath     string `json:"repo_path"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Number       int    `json:"number"`
	HeadSHA      string `json:"head_sha"`
	Status       string `json:"status"`
	Merged       bool   `json:"merged,omitempty"`
	SHA          string `json:"sha,omitempty"`
	Message      string `json:"message,omitempty"`
	Error        string `json:"error,omitempty"`
	CompletedAt  string `json:"completed_at"`
}

func (s *Server) deferMergePR(
	ctx context.Context,
	input *deferMergePRInput,
) (*deferMergePROutput, error) {
	body, err := s.enqueueDeferredMerge(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, input.Number, input.Body, deferredMergePollInterval)
	if err != nil {
		return nil, err
	}
	return &deferMergePROutput{Status: 202, Body: body}, nil
}

func (s *Server) enqueueDeferredMerge(
	ctx context.Context,
	provider string,
	platformHost string,
	owner string,
	name string,
	number int,
	body mergePRInputBody,
	pollInterval time.Duration,
) (deferMergePRBody, error) {
	if pollInterval <= 0 {
		pollInterval = deferredMergePollInterval
	}
	repo, err := s.requireRepoRouteCapability(
		ctx,
		provider, platformHost, owner, name,
		capabilityMergeMutation,
	)
	if err != nil {
		return deferMergePRBody{}, err
	}
	if err := s.requireSyncerCapability(*repo, capabilityMergeMutation); err != nil {
		return deferMergePRBody{}, err
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, number)
	if err != nil {
		return deferMergePRBody{}, problemInternal("get pull request failed")
	}
	if mr == nil {
		return deferMergePRBody{}, problemNotFound(CodePullNotFound, "pull request not found", nil)
	}
	pendingKeys, err := pendingDeferredMergeCheckKeys(mr.CIChecksJSON)
	if err != nil {
		return deferMergePRBody{}, problemValidation("ci_checks", err.Error())
	}
	if len(pendingKeys) == 0 {
		return deferMergePRBody{}, problemConflict(
			CodeConflict,
			"no pending CI checks to wait for",
			map[string]any{"reason": "no_pending_checks"},
		)
	}
	key := deferredMergeKey(*repo, number)
	if !s.markDeferredMergeInFlight(key) {
		return deferMergePRBody{}, problemConflict(
			CodeConflict,
			"a deferred merge is already waiting for this pull request",
			map[string]any{"reason": "already_pending"},
		)
	}
	started := s.runBackground(func(bgCtx context.Context) {
		defer s.clearDeferredMergeInFlight(key)
		s.runDeferredMerge(bgCtx, *repo, number, body, pendingKeys, pollInterval)
	})
	if !started {
		s.clearDeferredMergeInFlight(key)
		return deferMergePRBody{}, problemServiceUnavailable("server is shutting down")
	}
	return deferMergePRBody{
		Status:        "queued",
		PendingChecks: len(pendingKeys),
	}, nil
}

func (s *Server) runDeferredMerge(
	ctx context.Context,
	repo db.Repo,
	number int,
	body mergePRInputBody,
	pendingKeys []deferredMergeCheckKey,
	pollInterval time.Duration,
) {
	if len(pendingKeys) == 0 {
		s.completeDeferredMerge(ctx, repo, number, body)
		return
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	timeout := time.NewTimer(deferredMergeMaxWait)
	defer timeout.Stop()
	for {
		state, err := s.refreshDeferredMergeCI(ctx, repo, number, pendingKeys)
		if err != nil {
			s.broadcastDeferredMergeFailure(repo, number, body.ExpectedHeadSHA, err.Error())
			return
		}
		switch state {
		case "passed":
			s.completeDeferredMerge(ctx, repo, number, body)
			return
		case "failed":
			s.broadcastDeferredMergeFailure(repo, number, body.ExpectedHeadSHA, "a check that was pending failed; merge was not performed")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-timeout.C:
			s.broadcastDeferredMergeFailure(repo, number, body.ExpectedHeadSHA, "timed out waiting for pending CI checks to finish; merge was not performed")
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) refreshDeferredMergeCI(
	ctx context.Context,
	repo db.Repo,
	number int,
	pendingKeys []deferredMergeCheckKey,
) (string, error) {
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, number)
	if err != nil {
		return "", err
	}
	if mr == nil {
		return "", errors.New("pull request no longer exists")
	}
	warnings, err := s.syncer.RefreshMRCIStatusOnProvider(
		ctx,
		ghclient.RepoRef{
			Platform:           repoProviderKind(repo),
			Owner:              repo.Owner,
			Name:               repo.Name,
			PlatformHost:       repoProviderHost(repo),
			RepoPath:           repo.RepoPath,
			PlatformExternalID: repo.PlatformRepoID,
			WebURL:             repo.WebURL,
			CloneURL:           repo.CloneURL,
			DefaultBranch:      repo.DefaultBranch,
		},
		repo.ID,
		number,
		mr.PlatformHeadSHA,
	)
	if err != nil {
		return "", err
	}
	if len(warnings) > 0 {
		return "", errors.New("could not refresh CI checks; deferred merge was not performed: " + strings.Join(warnings, "; "))
	}
	refreshed, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, number)
	if err != nil {
		return "", err
	}
	if refreshed == nil {
		return "", errors.New("pull request no longer exists after CI refresh")
	}
	s.hub.Broadcast(Event{
		Type: "pr_ci_refreshed",
		Data: prCIRefreshedPayload{
			Provider:     string(repoProviderKind(repo)),
			PlatformHost: repoProviderHost(repo),
			RepoPath:     repo.RepoPath,
			Owner:        repo.Owner,
			Name:         repo.Name,
			Number:       number,
			HeadSHA:      refreshed.PlatformHeadSHA,
			RefreshedAt:  formatUTCRFC3339(s.now().UTC()),
			Warnings:     []string{},
		},
	})
	return deferredMergeCheckState(pendingKeys, refreshed.CIChecksJSON)
}

func (s *Server) completeDeferredMerge(
	ctx context.Context,
	repo db.Repo,
	number int,
	body mergePRInputBody,
) {
	result, err := s.mergePRWithBody(ctx, string(repoProviderKind(repo)), repoProviderHost(repo), repo.Owner, repo.Name, number, body)
	if err != nil {
		s.broadcastDeferredMergeFailure(repo, number, body.ExpectedHeadSHA, err.Error())
		return
	}
	s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	s.hub.Broadcast(Event{
		Type: "deferred_merge_completed",
		Data: deferredMergeCompletedPayload{
			Provider:     string(repoProviderKind(repo)),
			PlatformHost: repoProviderHost(repo),
			RepoPath:     repo.RepoPath,
			Owner:        repo.Owner,
			Name:         repo.Name,
			Number:       number,
			HeadSHA:      body.ExpectedHeadSHA,
			Status:       "merged",
			Merged:       result.Merged,
			SHA:          result.SHA,
			Message:      result.Message,
			CompletedAt:  formatUTCRFC3339(s.now().UTC()),
		},
	})
}

func (s *Server) broadcastDeferredMergeFailure(repo db.Repo, number int, headSHA string, message string) {
	slog.Warn("deferred merge failed",
		"provider", repoProviderKind(repo),
		"platform_host", repoProviderHost(repo),
		"repo_path", repo.RepoPath,
		"number", number,
		"err", message,
	)
	s.hub.Broadcast(Event{
		Type: "deferred_merge_completed",
		Data: deferredMergeCompletedPayload{
			Provider:     string(repoProviderKind(repo)),
			PlatformHost: repoProviderHost(repo),
			RepoPath:     repo.RepoPath,
			Owner:        repo.Owner,
			Name:         repo.Name,
			Number:       number,
			HeadSHA:      headSHA,
			Status:       "failed",
			Error:        message,
			CompletedAt:  formatUTCRFC3339(s.now().UTC()),
		},
	})
}

func pendingDeferredMergeCheckKeys(checksJSON string) ([]deferredMergeCheckKey, error) {
	var checks []db.CICheck
	if strings.TrimSpace(checksJSON) == "" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(checksJSON), &checks); err != nil {
		return nil, err
	}
	keys := make([]deferredMergeCheckKey, 0)
	for _, check := range checks {
		if check.Status != "completed" {
			keys = append(keys, deferredMergeCheckKey{App: check.App, Name: check.Name})
		}
	}
	return keys, nil
}

func deferredMergeCheckState(keys []deferredMergeCheckKey, checksJSON string) (string, error) {
	var checks []db.CICheck
	if strings.TrimSpace(checksJSON) != "" {
		if err := json.Unmarshal([]byte(checksJSON), &checks); err != nil {
			return "", err
		}
	}
	byKey := make(map[deferredMergeCheckKey]db.CICheck, len(checks))
	for _, check := range checks {
		byKey[deferredMergeCheckKey{App: check.App, Name: check.Name}] = check
	}
	pending := false
	for _, key := range keys {
		check, ok := byKey[key]
		if !ok {
			pending = true
			continue
		}
		if check.Status != "completed" {
			pending = true
			continue
		}
		switch check.Conclusion {
		case "success", "neutral", "skipped":
		default:
			return "failed", nil
		}
	}
	if pending {
		return "pending", nil
	}
	return "passed", nil
}

func deferredMergeKey(repo db.Repo, number int) string {
	return string(repoProviderKind(repo)) + ":" + repoProviderHost(repo) + ":" + repo.RepoPath + "#" + strconv.Itoa(number)
}

func (s *Server) markDeferredMergeInFlight(key string) bool {
	s.deferredMergeMu.Lock()
	defer s.deferredMergeMu.Unlock()
	if s.deferredMergeInFlight == nil {
		s.deferredMergeInFlight = make(map[string]struct{})
	}
	if _, ok := s.deferredMergeInFlight[key]; ok {
		return false
	}
	s.deferredMergeInFlight[key] = struct{}{}
	return true
}

func (s *Server) clearDeferredMergeInFlight(key string) {
	s.deferredMergeMu.Lock()
	defer s.deferredMergeMu.Unlock()
	delete(s.deferredMergeInFlight, key)
}

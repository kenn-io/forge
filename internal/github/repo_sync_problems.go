package github

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// RepoSyncProblem reports a tracked repository whose last attempted sync
// failed. It clears on the repository's next successful sync or removal from
// tracking; passes that skip the repository leave it in place.
type RepoSyncProblem struct {
	Repository string    `json:"repository"`
	Host       string    `json:"host"`
	Error      string    `json:"error"`
	Since      time.Time `json:"since"`
}

// recordRepoSync updates the repository's sync health from the result of a
// pass. Budget waits and cancellation say nothing about the repository, so they
// keep its previous health.
func (s *Syncer) recordRepoSync(repo RepoRef, err error) {
	if err != nil && (syncBudgetWait(err) || errors.Is(err, context.Canceled)) {
		return
	}
	// The pass snapshotted the repository before sync could resolve its
	// provider identity, so key the result by the ref tracked now.
	s.reposMu.Lock()
	if i, ok := s.trackedRepoSlotLocked(repo, repo); ok {
		repo = s.repos[i]
	}
	s.reposMu.Unlock()
	key := gitAccessKey(repo)
	routeKey := expandedRepoRouteKey(repo)
	s.repoSyncMu.Lock()
	previous, failing := s.repoSync[key]
	if !failing {
		// A failure recorded before the identity resolved is still route-keyed.
		previous, failing = s.repoSync[routeKey]
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	changed := false
	switch {
	case err == nil && failing:
		delete(s.repoSync, key)
		delete(s.repoSync, routeKey)
		changed = true
	case err != nil && (!failing || previous.Error != message):
		if s.repoSync == nil {
			s.repoSync = make(map[string]RepoSyncProblem)
		}
		since := previous.Since
		if !failing {
			since = s.nowUTC()
		}
		delete(s.repoSync, routeKey)
		s.repoSync[key] = RepoSyncProblem{
			Repository: repo.Owner + "/" + repo.Name,
			Host:       repoHost(repo),
			Error:      message,
			Since:      since,
		}
		changed = true
	}
	s.repoSyncMu.Unlock()
	if !changed {
		return
	}
	if err == nil {
		slog.Info("repository sync recovered",
			"repo", repo.Owner+"/"+repo.Name, "host", repoHost(repo))
	}
	s.statusMu.Lock()
	status := *s.Status()
	s.publishStatusLocked(&status)
	s.statusMu.Unlock()
}

// repoSyncProblems reconciles failures with the current tracked repositories.
func (s *Syncer) repoSyncProblems() []RepoSyncProblem {
	repos := s.TrackedRepos()
	s.repoSyncMu.Lock()
	defer s.repoSyncMu.Unlock()
	if len(s.repoSync) == 0 {
		return nil
	}
	current := make(map[string]RepoSyncProblem, len(s.repoSync))
	problems := make([]RepoSyncProblem, 0, len(s.repoSync))
	for _, repo := range repos {
		key := gitAccessKey(repo)
		problem, ok := s.repoSync[key]
		if !ok && !repo.Key.IsZero() {
			// Preserve a failure recorded before the provider resolved its ID.
			problem, ok = s.repoSync[expandedRepoRouteKey(repo)]
		}
		if !ok {
			continue
		}
		problem.Repository = repo.Owner + "/" + repo.Name
		problem.Host = repoHost(repo)
		current[key] = problem
		problems = append(problems, problem)
	}
	s.repoSync = current
	slices.SortFunc(problems, func(a, b RepoSyncProblem) int {
		return strings.Compare(
			strings.ToLower(a.Host+"/"+a.Repository), strings.ToLower(b.Host+"/"+b.Repository),
		)
	})
	return problems
}

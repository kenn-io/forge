package github

import (
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/tokenauth"
)

// GitAccessReason classifies why clone-store Git cannot reach a repository.
type GitAccessReason string

const (
	// GitAccessCredentialUnavailable means no credential route resolves a
	// token for the repository.
	GitAccessCredentialUnavailable GitAccessReason = "credential_unavailable"
	// GitAccessAuthenticationFailed means the provider rejected the token.
	GitAccessAuthenticationFailed GitAccessReason = "authentication_failed"
)

// GitAccessProblem reports a repository whose clone or fetch into the clone
// store fails for a credential reason. It clears on the repository's next
// successful clone or fetch.
type GitAccessProblem struct {
	Repository string          `json:"repository"`
	Host       string          `json:"host"`
	Reason     GitAccessReason `json:"reason" enum:"credential_unavailable,authentication_failed"`
	Since      time.Time       `json:"since"`
}

// reportedGitAccessError marks a credential failure that recordGitAccess has
// already logged, so callers do not repeat the warning on every pass.
type reportedGitAccessError struct {
	err error
}

func (e reportedGitAccessError) Error() string { return e.err.Error() }

func (e reportedGitAccessError) Unwrap() error { return e.err }

func isReportedGitAccessFailure(err error) bool {
	_, reported := errors.AsType[reportedGitAccessError](err)
	return reported
}

func gitAccessFailureReason(err error) GitAccessReason {
	_, missingRoute := errors.AsType[*MissingRouteError](err)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, gitclone.ErrCredentialUnavailable),
		errors.Is(err, tokenauth.ErrMissingToken),
		errors.Is(err, ErrMissingWriteIdentity),
		missingRoute:
		return GitAccessCredentialUnavailable
	case gitclone.IsAuthenticationFailure(err):
		return GitAccessAuthenticationFailed
	}
	return ""
}

// recordGitAccess updates the repository's clone-store Git access from the
// result of a clone or fetch. A credential failure is logged when it starts
// or changes reason and is returned as already reported.
func (s *Syncer) recordGitAccess(repo RepoRef, err error) error {
	host := repoHost(repo)
	path := repo.Owner + "/" + repo.Name
	key := strings.ToLower(host + "/" + path)
	reason := gitAccessFailureReason(err)
	if err != nil && reason == "" {
		return err
	}
	s.gitAccessMu.Lock()
	previous, failing := s.gitAccess[key]
	changed := false
	switch {
	case reason == "" && failing:
		delete(s.gitAccess, key)
		changed = true
	case reason != "" && (!failing || previous.Reason != reason):
		if s.gitAccess == nil {
			s.gitAccess = make(map[string]GitAccessProblem)
		}
		s.gitAccess[key] = GitAccessProblem{
			Repository: path, Host: host, Reason: reason, Since: s.nowUTC(),
		}
		changed = true
	}
	s.gitAccessMu.Unlock()
	if changed {
		if reason == "" {
			slog.Info("clone-store git access restored", "repo", path, "host", host)
		} else {
			slog.Warn("clone-store git access unavailable",
				"repo", path, "host", host, "reason", reason, "err", err,
			)
		}
		s.statusMu.Lock()
		status := *s.Status()
		s.publishStatusLocked(&status)
		s.statusMu.Unlock()
	}
	if reason == "" {
		return nil
	}
	return reportedGitAccessError{err: err}
}

func (s *Syncer) gitAccessProblems() []GitAccessProblem {
	s.gitAccessMu.Lock()
	defer s.gitAccessMu.Unlock()
	if len(s.gitAccess) == 0 {
		return nil
	}
	problems := make([]GitAccessProblem, 0, len(s.gitAccess))
	for _, problem := range s.gitAccess {
		problems = append(problems, problem)
	}
	slices.SortFunc(problems, func(a, b GitAccessProblem) int {
		return strings.Compare(
			strings.ToLower(a.Host+"/"+a.Repository), strings.ToLower(b.Host+"/"+b.Repository),
		)
	})
	return problems
}

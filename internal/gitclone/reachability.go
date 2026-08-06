package gitclone

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// CommitReachability reports which candidate commits are reachable from a
// head commit in the local bare clone. Live is populated only when
// HeadVerified is true; a head absent from the clone means the caller cannot
// certify anything this round.
type CommitReachability struct {
	HeadVerified bool
	// Live maps each evaluated candidate SHA (lowercase) to whether it is
	// an ancestor of the head (the head itself counts as live). Candidates
	// that cannot be represented as object hashes are omitted entirely so
	// callers leave their state untouched.
	Live map[string]bool
}

// contextCheckInterval bounds how many commits the ancestry walk visits
// between context-cancellation checks.
const contextCheckInterval = 1024

// CommitsReachableFrom answers reachability for all candidates with a single
// in-process ancestry walk via go-git: no subprocesses, no locks, and no
// argv limits. The walk starts at the head and terminates early once every
// candidate is resolved; candidates never reached — including ones absent
// from the object store — are dead, because a clone containing the head
// contains the head's entire ancestor closure.
func (m *Manager) CommitsReachableFrom(
	ctx context.Context,
	platform, host, owner, name, headSHA string,
	candidateSHAs []string,
) (CommitReachability, error) {
	clonePath, err := m.ClonePath(platform, host, owner, name)
	if err != nil {
		return CommitReachability{}, err
	}
	repo, err := gogit.PlainOpen(clonePath)
	if err != nil {
		if errors.Is(err, gogit.ErrRepositoryNotExists) {
			return CommitReachability{}, nil
		}
		return CommitReachability{}, fmt.Errorf("open clone %s: %w", clonePath, err)
	}

	headHash, ok := commitHash(headSHA)
	if !ok {
		return CommitReachability{}, nil
	}
	headCommit, err := repo.CommitObject(headHash)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return CommitReachability{}, nil
	}
	if err != nil {
		return CommitReachability{}, fmt.Errorf("resolve head %s: %w", headSHA, err)
	}

	live := make(map[string]bool, len(candidateSHAs))
	remaining := make(map[plumbing.Hash]string, len(candidateSHAs))
	for _, sha := range candidateSHAs {
		normalized := strings.ToLower(strings.TrimSpace(sha))
		hash, ok := commitHash(normalized)
		if !ok {
			continue
		}
		live[normalized] = false
		remaining[hash] = normalized
	}
	if len(remaining) == 0 {
		return CommitReachability{HeadVerified: true, Live: live}, nil
	}

	visited := 0
	iter := object.NewCommitPreorderIter(headCommit, nil, nil)
	err = iter.ForEach(func(commit *object.Commit) error {
		visited++
		if visited%contextCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if sha, ok := remaining[commit.Hash]; ok {
			live[sha] = true
			delete(remaining, commit.Hash)
			if len(remaining) == 0 {
				return storer.ErrStop
			}
		}
		return nil
	})
	if err != nil {
		return CommitReachability{}, fmt.Errorf("walk ancestry of %s: %w", headSHA, err)
	}
	return CommitReachability{HeadVerified: true, Live: live}, nil
}

// commitHash parses a full lowercase hex SHA into an object hash. Only
// 40-hex SHA-1 names are representable in the clone's object format today;
// anything else is reported unrepresentable rather than guessed at.
func commitHash(sha string) (plumbing.Hash, bool) {
	if len(sha) != 40 {
		return plumbing.Hash{}, false
	}
	for _, r := range sha {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return plumbing.Hash{}, false
		}
	}
	return plumbing.NewHash(sha), true
}

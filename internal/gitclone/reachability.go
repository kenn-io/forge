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

// maxAncestryVisits caps how many commits a single reachability walk may
// visit. A candidate that was force-pushed away is unreachable, so the walk
// cannot terminate early on it and would otherwise traverse the entire
// repository history — which a contributor controls via pushed heads. Hitting
// the cap reports the head unverifiable: callers leave liveness state exactly
// as it was, the safe direction.
const maxAncestryVisits = 50_000

// maxAncestryEdges caps the total parent references a walk may examine.
// Distinct-hash discovery is bounded by maxAncestryVisits, but a crafted
// history can point enormous parent lists at the same few commits; without
// an edge budget those repeated references still cost a lookup each, so the
// walk refuses histories whose edge count no real merge graph approaches.
const maxAncestryEdges = 500_000

// maxCommitObjectSize rejects pathological commit objects before anything is
// decoded. Reachability needs only hashes, never contents, so the walk reads
// each commit's header prefix (tree and parent lines) and nothing else — but
// serving even that reader can force the object store to materialize the
// object, so its encoded size is checked first. Real commit objects are a
// few hundred bytes; anything above this cap is contributor-controlled input
// the walk refuses, reporting the head unverifiable.
const maxCommitObjectSize = 1 << 20

// CommitsReachableFrom answers reachability for all candidates with a single
// in-process ancestry walk via go-git: no subprocesses, no locks, and no
// argv limits. The walk starts at the head and terminates early once every
// candidate is resolved; candidates never reached — including ones absent
// from the object store — are dead, because a clone containing the head
// contains the head's entire ancestor closure. Only commit headers are ever
// read: the walk needs parent hashes, not messages or trees.
func (m *Manager) CommitsReachableFrom(
	ctx context.Context,
	platform, host, owner, name, headSHA string,
	candidateSHAs []string,
) (CommitReachability, error) {
	clonePath, err := m.clonePathForContext(ctx, platform, host, owner, name)
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
		if _, ok, err := commitParents(repo.Storer, headHash); err != nil {
			return CommitReachability{}, fmt.Errorf("read commit %s: %w", headSHA, err)
		} else if !ok {
			return CommitReachability{}, nil
		}
		return CommitReachability{HeadVerified: true, Live: live}, nil
	}

	budget := m.ancestryVisitBudget
	if budget <= 0 {
		budget = maxAncestryVisits
	}
	// Hashes are marked discovered when enqueued, never re-enqueued, and
	// discovery counts against the budget immediately, so the frontier can
	// never outgrow the visit budget no matter how a crafted history shapes
	// its parent lists.
	visited := 0
	edges := 0
	seen := map[plumbing.Hash]bool{headHash: true}
	queue := []plumbing.Hash{headHash}
	for len(queue) > 0 {
		hash := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		visited++
		if visited%contextCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return CommitReachability{}, fmt.Errorf("walk ancestry of %s: %w", headSHA, err)
			}
		}
		if sha, ok := remaining[hash]; ok {
			live[sha] = true
			delete(remaining, hash)
			if len(remaining) == 0 {
				return CommitReachability{HeadVerified: true, Live: live}, nil
			}
		}
		parents, ok, err := commitParents(repo.Storer, hash)
		if err != nil {
			return CommitReachability{}, fmt.Errorf("read commit %s: %w", hash, err)
		}
		if !ok {
			// The head itself missing is the ordinary lagging-clone case;
			// anything else — a missing parent inside a supposedly complete
			// closure, or an object refusing bounded parsing — equally means
			// the walk cannot certify ancestry this round.
			return CommitReachability{}, nil
		}
		edges += len(parents)
		if edges > maxAncestryEdges {
			return CommitReachability{}, nil
		}
		for _, parent := range parents {
			if seen[parent] {
				continue
			}
			seen[parent] = true
			if len(seen) > budget {
				return CommitReachability{}, nil
			}
			queue = append(queue, parent)
		}
	}
	return CommitReachability{HeadVerified: true, Live: live}, nil
}

// commitParents returns a commit's parent hashes — the only thing the walk
// needs. The encoded size is checked before go-git decodes the object, so a
// pathological commit is refused without ever being materialized. ok=false
// means the object is missing, oversized, or malformed: the caller reports
// the head unverifiable rather than guessing at ancestry.
func commitParents(
	objects storer.EncodedObjectStorer,
	hash plumbing.Hash,
) ([]plumbing.Hash, bool, error) {
	obj, err := objects.EncodedObject(plumbing.CommitObject, hash)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if obj.Size() > maxCommitObjectSize {
		return nil, false, nil
	}
	commit, err := object.DecodeCommit(objects, obj)
	if err != nil {
		return nil, false, nil
	}
	return commit.ParentHashes, true, nil
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

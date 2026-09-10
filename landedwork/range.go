package landedwork

import (
	"context"
	"errors"
	"slices"
)

func proveRange(ctx context.Context, v *objectView, p *Interval, c Candidate) (Landing, Gap) {
	gap := Gap{CandidateID: c.ID, ObjectID: c.Terminal}
	reject := func(reason string) (Landing, Gap) { gap.Reason = reason; return Landing{}, gap }
	end := slices.Index(p.spine, c.Terminal) + 1
	if end < len(c.Source) || end == 0 {
		return reject("range_crosses_base")
	}
	sourceBefore, g := sourceBoundary(ctx, v, c)
	if g.Reason != "" {
		return Landing{}, g
	}
	parents, err := v.parents(ctx, c.Terminal)
	if err != nil {
		return reject(graphReason(err))
	}
	if len(parents) != 1 {
		return reject("topology_unproven")
	}
	attempts := rangeAlternatives(ctx, v, p, c, sourceBefore, parents[0])
	a := attempts[0]
	if c.Method == "fast_forward" {
		a = attempts[1]
	}
	if a.state == attemptMismatch {
		return reject("source_correspondence_unproven")
	}
	return a.landing, a.gap
}

// Compare the fixed range from its terminal backward. Any unequal pair rejects
// the range, even if another pair was ambiguous. A successful proof still reads
// every required commit and its boundary; it never shortens the owned range.
func rangeCorrespondence(ctx context.Context, v *objectView, c Candidate, sourceBefore, before string) (r firstParentRange, identical bool, object string, err error) {
	identical = true
	var rewritten [][]fileEdit
	var pending error
	var pendingObject string
	i := len(c.Source) - 1
	for pair, readErr := range v.firstParentSuffix(ctx, c.Terminal, before, len(c.Source)) {
		if readErr != nil {
			return r, identical, pair.before, readErr
		}
		r.commits = append(r.commits, pair.id)
		r.before = pair.before
		source := c.Source[i]
		parent := sourceBefore
		if i > 0 {
			parent = c.Source[i-1]
		}
		i--
		if source == pair.id {
			continue
		}
		identical = false
		if c.Method == "fast_forward" {
			return r, identical, source, errCorrespondence
		}
		edits, compareErr := v.compareTreeEdits(ctx, parent, source, pair.before, pair.id)
		if compareErr == nil {
			for _, previous := range rewritten {
				if sameEdits(previous, edits) {
					compareErr = errEdits
					break
				}
			}
			rewritten = append(rewritten, edits)
		}
		if errors.Is(compareErr, errEdits) {
			if pending == nil {
				pending, pendingObject = compareErr, source
			}
			continue
		}
		if compareErr != nil {
			return r, identical, source, compareErr
		}
	}
	slices.Reverse(r.commits)
	return r, identical, pendingObject, pending
}

func rangeAlternatives(ctx context.Context, v *objectView, p *Interval, c Candidate, sourceBefore, before string) [2]proofAttempt {
	r, identical, object, err := rangeCorrespondence(ctx, v, c, sourceBefore, before)
	if errors.Is(err, errCorrespondence) {
		return [2]proofAttempt{{state: attemptMismatch}, {state: attemptMismatch}}
	}
	a := proofAttempt{gap: Gap{CandidateID: c.ID, ObjectID: object, Reason: editReason(err)}}
	if err == nil {
		a = proofAttempt{state: attemptMatch,
			crossesBase: slices.Index(p.spine, c.Terminal)+1 < len(c.Source),
			landing: Landing{CandidateID: c.ID, Proofs: []string{"rebase"}, Before: r.before, Terminal: c.Terminal,
				Source: slices.Clone(c.Source), Spine: r.commits, Introduced: slices.Clone(r.commits)}}
	}
	fastForward := proofAttempt{state: attemptMismatch}
	if identical {
		fastForward = a
		if a.state == attemptMatch {
			fastForward.landing.Proofs = []string{"fast_forward"}
		}
	}
	return [2]proofAttempt{a, fastForward}
}

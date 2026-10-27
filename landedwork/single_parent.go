package landedwork

import (
	"context"
	"errors"
	"slices"
)

func sourceShape(ctx context.Context, v *objectView, c Candidate) (string, bool, Gap) {
	g := Gap{CandidateID: c.ID, ObjectID: c.SourceHead}
	if c.Source[len(c.Source)-1] != c.SourceHead {
		g.Reason = "source_head_mismatch"
		return "", false, g
	}
	before := ""
	linear := true
	var orderGap Gap
	seen := make(map[string]bool, len(c.Source))
	for i, id := range c.Source {
		g.ObjectID = id
		if id == "" || seen[id] {
			g.Reason = "source_invalid"
			return "", false, g
		}
		seen[id] = true
		parents, err := v.parents(ctx, id)
		if err != nil {
			g.Reason = graphReason(err)
			return "", false, g
		}
		if len(parents) == 0 {
			g.Reason = "topology_unproven"
			return "", false, g
		}
		linear = linear && len(parents) == 1
		if i == 0 {
			before = parents[0]
		} else if parents[0] != c.Source[i-1] {
			orderGap = Gap{CandidateID: c.ID, ObjectID: id, Reason: "source_order_unproven"}
		}
	}
	if !linear {
		return "", false, Gap{}
	}
	if orderGap.Reason != "" {
		return "", false, orderGap
	}
	if _, err := v.parents(ctx, before); err != nil {
		g.ObjectID, g.Reason = before, graphReason(err)
		return "", false, g
	}
	return before, true, Gap{}
}

func sourceBoundary(ctx context.Context, v *objectView, c Candidate) (string, Gap) {
	before, linear, g := sourceShape(ctx, v, c)
	if g.Reason == "" && !linear {
		g = Gap{CandidateID: c.ID, ObjectID: c.SourceHead, Reason: "topology_unproven"}
	}
	return before, g
}

func proveSquash(ctx context.Context, v *objectView, c Candidate, before string, declared map[string]string) (Landing, Gap) {
	sourceBefore, _, g := sourceShape(ctx, v, c)
	if g.Reason != "" {
		return Landing{}, g
	}
	if _, err := v.parents(ctx, before); err != nil {
		return Landing{}, Gap{CandidateID: c.ID, ObjectID: before, Reason: graphReason(err)}
	}
	matched, g := squashCorrespondence(ctx, v, c, sourceBefore, before, declared)
	if g.Reason != "" {
		return Landing{}, g
	}
	if !matched {
		landed, ok := squashedPart(ctx, v, c, sourceBefore, before, declared)
		if !ok {
			return Landing{}, Gap{CandidateID: c.ID, ObjectID: c.Terminal, Reason: "source_correspondence_unproven"}
		}
		c = landed
	}
	return squashLanding(c, before), Gap{}
}

// squashedPart finds the part of a linear source that a mismatched squash
// landed. GitHub lists commits from the base it recorded for the change. When
// that base is older than the branch's real base, the list starts with commits
// the target already holds, and they belong to the changes that landed them.
// A push that races the merge can leave the reported head ahead of the commit
// GitHub squashed, so the newest source commits never landed. Only an exact
// squash of the remaining source counts. Its witness still rejects a squash
// that could be the end of a rebase, and an unreadable or ambiguous part keeps
// the mismatch instead of trying a shorter one.
func squashedPart(ctx context.Context, v *objectView, c Candidate, sourceBefore, before string, declared map[string]string) (Candidate, bool) {
	if sourceBefore == "" {
		return Candidate{}, false
	}
	start := 0
	for ; start < len(c.Source)-1; start++ {
		present, err := v.ancestor(ctx, c.Source[start], before)
		if err != nil {
			return Candidate{}, false
		}
		if !present {
			break
		}
	}
	for end := len(c.Source); end > start; end-- {
		if start == 0 && end == len(c.Source) {
			continue // The full source already mismatched.
		}
		part := c
		part.Source = slices.Clone(c.Source[start:end])
		part.SourceHead = c.Source[end-1]
		partBefore := sourceBefore
		if start > 0 {
			partBefore = c.Source[start-1]
		}
		matched, g := squashCorrespondence(ctx, v, part, partBefore, before, declared)
		if g.Reason != "" {
			return Candidate{}, false
		}
		if matched {
			return part, true
		}
	}
	return Candidate{}, false
}

// False with no gap is a conclusive mismatch. Unreadable or ambiguous edits are
// not evidence against this alternative and must prevent choosing another one.
func squashCorrespondence(ctx context.Context, v *objectView, c Candidate, sourceBefore, before string, declared map[string]string) (bool, Gap) {
	g := Gap{CandidateID: c.ID}
	linear := sourceBefore != ""
	if !linear {
		// Nonlinear sources must contain every source-side commit, including
		// merged side branches. Target ancestors may be listed as context.
		_, err := sourceCorrespondence(ctx, v, c, before)
		if errors.Is(err, errCorrespondence) {
			return false, Gap{CandidateID: c.ID, ObjectID: c.SourceHead, Reason: "source_incomplete"}
		}
		if err != nil {
			return false, Gap{CandidateID: c.ID, ObjectID: c.SourceHead, Reason: editReason(err)}
		}
		matched, empty, err := v.exactMerge(ctx, "", before, c.SourceHead, c.Terminal)
		if err != nil {
			return false, Gap{CandidateID: c.ID, ObjectID: c.Terminal, Reason: editReason(err)}
		}
		if empty && !matched {
			return false, Gap{CandidateID: c.ID, ObjectID: c.Terminal, Reason: "empty_tree_edits"}
		}
		if !matched {
			return false, Gap{}
		}
		// A tree match alone can consume only part of the source edits when
		// the target already contains some of them. Compare the full net edits
		// since the shared branch-update boundary before the weaker witness.
		sourceBefore, err = v.mergeBase(ctx, before, c.SourceHead)
		if err != nil {
			return false, Gap{CandidateID: c.ID, ObjectID: c.SourceHead, Reason: editReason(err)}
		}
	}
	_, err := v.compareTreeEdits(ctx, sourceBefore, c.SourceHead, before, c.Terminal)
	if errors.Is(err, errCorrespondence) {
		return witnessedSquash(ctx, v, c, sourceBefore, before, linear, declared)
	}
	if errors.Is(err, editProofError("empty_tree_edits")) {
		// Without the witness, an empty terminal could be the last commit of a
		// rewritten range, so it stays unproven rather than a mismatch.
		if matched, g := witnessedSquash(ctx, v, c, sourceBefore, before, linear, declared); matched || g.Reason != "" {
			return matched, g
		}
	}
	if err != nil {
		g.ObjectID, g.Reason = c.Terminal, editReason(err)
		return false, g
	}
	return true, Gap{}
}

func editReason(err error) string {
	if failure, ok := errors.AsType[editProofError](err); ok {
		return string(failure)
	}
	return graphReason(err)
}

func proveSingleParent(ctx context.Context, v *objectView, p *Interval, c Candidate, before string, declared map[string]string) (Landing, Gap) {
	sourceBefore, linear, g := sourceShape(ctx, v, c)
	if g.Reason != "" {
		return Landing{}, g
	}
	if _, err := v.parents(ctx, before); err != nil {
		return Landing{}, Gap{CandidateID: c.ID, ObjectID: before, Reason: graphReason(err)}
	}
	matched, g := squashCorrespondence(ctx, v, c, sourceBefore, before, declared)
	squash := proofAttempt{state: attemptMismatch}
	if g.Reason != "" {
		squash = proofAttempt{gap: g}
	} else if matched {
		squash = proofAttempt{state: attemptMatch, landing: squashLanding(c, before)}
	}
	// A source graph containing merges cannot be the linear, one-to-one
	// source chain required by either rewritten-range alternative.
	ranges := [2]proofAttempt{{state: attemptMismatch}, {state: attemptMismatch}}
	if linear {
		ranges = rangeAlternatives(ctx, v, p, c, sourceBefore, before, declared)
	}
	// A rebase owns its own commits, so only a change that no alternative
	// explains can be a squash of part of its source.
	if squash.state == attemptMismatch && ranges[0].state == attemptMismatch && ranges[1].state == attemptMismatch {
		if landed, ok := squashedPart(ctx, v, c, sourceBefore, before, declared); ok {
			squash = proofAttempt{state: attemptMatch, landing: squashLanding(landed, before)}
		}
	}
	return resolveAlternatives(c, []proofAttempt{squash, ranges[0], ranges[1]}, v.meter.failed)
}

func squashLanding(c Candidate, before string) Landing {
	return Landing{
		CandidateID: c.ID, Proofs: []string{"squash"}, Before: before, Terminal: c.Terminal,
		Spine: []string{c.Terminal}, Source: slices.Clone(c.Source), Introduced: []string{c.Terminal},
	}
}

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

func proveSquash(ctx context.Context, v *objectView, p *Interval, c Candidate, before string, declared map[string]string) (Landing, Gap) {
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
		for _, part := range listedParts(ctx, v, p, c, sourceBefore, before, declared) {
			matched, g := squashCorrespondence(ctx, v, part.Candidate, part.sourceBefore, before, declared)
			if g.Reason != "" {
				break
			}
			if matched {
				return squashLanding(part.Candidate, before), Gap{}
			}
		}
		return Landing{}, Gap{CandidateID: c.ID, ObjectID: c.Terminal, Reason: "source_correspondence_unproven"}
	}
	return squashLanding(c, before), Gap{}
}

// sourcePart is part of a candidate's listed source, with the commit before it.
type sourcePart struct {
	Candidate
	sourceBefore string
}

// listedParts returns, longest first, the parts of a linear listed source that
// a mismatched landing may have landed instead. GitHub lists commits from the
// base it recorded for the change. When that base is older than the branch's
// real base, the list starts with commits the target already holds, and they
// belong to the changes that landed them: either history before this interval
// or another change's declared terminal. A commit of this interval that no
// other change declared can be this change's own landing, as in a
// fast-forward, so it stays in every part. A push that races the merge can
// leave the reported head ahead of the commit GitHub merged, so the newest
// source commits never landed. Parts that keep the commits the target holds
// come first, so a change whose earlier commits landed unchanged in an earlier
// interval still meets the rewritten-range checks instead of proving by its
// last commits alone. Callers prove each part exactly as they prove a full
// source, and stop at the first part that is unreadable or ambiguous instead
// of trying a shorter one.
func listedParts(ctx context.Context, v *objectView, p *Interval, c Candidate, sourceBefore, before string, declared map[string]string) []sourcePart {
	if sourceBefore == "" {
		return nil
	}
	start := 0
	for ; start < len(c.Source)-1; start++ {
		id := c.Source[start]
		if owner, ok := declared[id]; slices.Contains(p.spine, id) && (!ok || owner == c.ID) {
			break
		}
		present, err := v.ancestor(ctx, id, before)
		if err != nil {
			return nil
		}
		if !present {
			break
		}
	}
	var parts []sourcePart
	for _, from := range slices.Compact([]int{0, start}) {
		for end := len(c.Source); end > start; end-- {
			if from == 0 && end == len(c.Source) {
				continue // The full source already mismatched.
			}
			part := sourcePart{Candidate: c, sourceBefore: sourceBefore}
			part.Source = slices.Clone(c.Source[from:end])
			part.SourceHead = c.Source[end-1]
			if from > 0 {
				part.sourceBefore = c.Source[from-1]
			}
			parts = append(parts, part)
		}
	}
	return parts
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
	attempts := singleParentAttempts(ctx, v, p, c, sourceBefore, linear, before, declared)
	if mismatched(attempts) {
		// Each part gets the same alternatives as the full source, so a rebase
		// of the part is never mistaken for a squash by its terminal alone.
		for _, part := range listedParts(ctx, v, p, c, sourceBefore, before, declared) {
			partAttempts := singleParentAttempts(ctx, v, p, part.Candidate, part.sourceBefore, true, before, declared)
			if !mismatched(partAttempts) {
				if slices.ContainsFunc(partAttempts, func(a proofAttempt) bool { return a.state == attemptInconclusive }) {
					break
				}
				return resolveAlternatives(c, partAttempts, v.meter.failed)
			}
		}
	}
	return resolveAlternatives(c, attempts, v.meter.failed)
}

func singleParentAttempts(ctx context.Context, v *objectView, p *Interval, c Candidate, sourceBefore string, linear bool, before string, declared map[string]string) []proofAttempt {
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
	return []proofAttempt{squash, ranges[0], ranges[1]}
}

func mismatched(attempts []proofAttempt) bool {
	return !slices.ContainsFunc(attempts, func(a proofAttempt) bool { return a.state != attemptMismatch })
}

func squashLanding(c Candidate, before string) Landing {
	return Landing{
		CandidateID: c.ID, Proofs: []string{"squash"}, Before: before, Terminal: c.Terminal,
		Spine: []string{c.Terminal}, Source: slices.Clone(c.Source), Introduced: []string{c.Terminal},
	}
}

package landedwork

import (
	"context"
	"errors"
	"slices"
)

func sourceBoundary(ctx context.Context, v *objectView, c Candidate) (string, Gap) {
	g := Gap{CandidateID: c.ID, ObjectID: c.SourceHead}
	if c.Source[len(c.Source)-1] != c.SourceHead {
		g.Reason = "source_head_mismatch"
		return "", g
	}
	before := ""
	seen := make(map[string]bool, len(c.Source))
	for i, id := range c.Source {
		g.ObjectID = id
		if id == "" || seen[id] {
			g.Reason = "source_invalid"
			return "", g
		}
		seen[id] = true
		parents, err := v.parents(ctx, id)
		if err != nil {
			g.Reason = graphReason(err)
			return "", g
		}
		if len(parents) != 1 {
			g.Reason = "topology_unproven"
			return "", g
		}
		if i == 0 {
			before = parents[0]
		} else if parents[0] != c.Source[i-1] {
			g.Reason = "source_order_unproven"
			return "", g
		}
	}
	if _, err := v.parents(ctx, before); err != nil {
		g.ObjectID, g.Reason = before, graphReason(err)
		return "", g
	}
	return before, Gap{}
}

func proveSquash(ctx context.Context, v *objectView, c Candidate, before string) (Landing, Gap) {
	sourceBefore, g := sourceBoundary(ctx, v, c)
	if g.Reason != "" {
		return Landing{}, g
	}
	if _, err := v.parents(ctx, before); err != nil {
		return Landing{}, Gap{CandidateID: c.ID, ObjectID: before, Reason: graphReason(err)}
	}
	matched, g := squashCorrespondence(ctx, v, c, sourceBefore, before)
	if g.Reason != "" {
		return Landing{}, g
	}
	if !matched {
		return Landing{}, Gap{CandidateID: c.ID, ObjectID: c.Terminal, Reason: "source_correspondence_unproven"}
	}
	return squashLanding(c, before), Gap{}
}

// False with no gap is a conclusive mismatch. Unreadable or ambiguous edits are
// not evidence against this alternative and must prevent choosing another one.
func squashCorrespondence(ctx context.Context, v *objectView, c Candidate, sourceBefore, before string) (bool, Gap) {
	g := Gap{CandidateID: c.ID}
	_, err := v.compareTreeEdits(ctx, sourceBefore, c.SourceHead, before, c.Terminal)
	if errors.Is(err, errCorrespondence) {
		return false, Gap{}
	}
	if err != nil {
		g.ObjectID, g.Reason = c.Terminal, editReason(err)
		return false, g
	}
	return true, Gap{}
}

func editReason(err error) string {
	if errors.Is(err, errEdits) {
		return "edits_unavailable"
	}
	return graphReason(err)
}

func proveSingleParent(ctx context.Context, v *objectView, p *Interval, c Candidate, before string) (Landing, Gap) {
	sourceBefore, g := sourceBoundary(ctx, v, c)
	if g.Reason != "" {
		return Landing{}, g
	}
	if _, err := v.parents(ctx, before); err != nil {
		return Landing{}, Gap{CandidateID: c.ID, ObjectID: before, Reason: graphReason(err)}
	}
	matched, g := squashCorrespondence(ctx, v, c, sourceBefore, before)
	squash := proofAttempt{state: attemptMismatch}
	if g.Reason != "" {
		squash = proofAttempt{gap: g}
	} else if matched {
		squash = proofAttempt{state: attemptMatch, landing: squashLanding(c, before)}
	}
	ranges := rangeAlternatives(ctx, v, p, c, sourceBefore, before)
	return resolveAlternatives(c, []proofAttempt{squash, ranges[0], ranges[1]}, v.meter.failed)
}

func squashLanding(c Candidate, before string) Landing {
	return Landing{CandidateID: c.ID, Proofs: []string{"squash"}, Before: before, Terminal: c.Terminal,
		Spine: []string{c.Terminal}, Source: slices.Clone(c.Source), Introduced: []string{c.Terminal}}
}

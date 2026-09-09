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
	for i, id := range c.Source {
		g.ObjectID = id
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
	matched, g := squashCorrespondence(ctx, v, c, sourceBefore, before)
	if g.Reason != "" {
		return Landing{}, g
	}
	if !matched {
		return Landing{}, Gap{CandidateID: c.ID, ObjectID: c.Terminal, Reason: "source_correspondence_unproven"}
	}
	return Landing{CandidateID: c.ID, Proofs: []string{"squash"}, Before: before, Terminal: c.Terminal,
		Spine: []string{c.Terminal}, Source: slices.Clone(c.Source), Introduced: []string{c.Terminal}}, Gap{}
}

// False with no gap is a conclusive mismatch. Unreadable or ambiguous edits are
// not evidence against this alternative and must prevent choosing another one.
func squashCorrespondence(ctx context.Context, v *objectView, c Candidate, sourceBefore, before string) (bool, Gap) {
	g := Gap{CandidateID: c.ID}
	for _, id := range []string{sourceBefore, c.SourceHead, before, c.Terminal} {
		if _, err := v.parents(ctx, id); err != nil {
			g.ObjectID, g.Reason = id, graphReason(err)
			return false, g
		}
	}
	source, err := v.treeEdits(ctx, sourceBefore, c.SourceHead)
	if err != nil {
		g.ObjectID, g.Reason = c.SourceHead, editReason(err)
		return false, g
	}
	landed, err := v.treeEdits(ctx, before, c.Terminal)
	if err != nil {
		g.ObjectID, g.Reason = c.Terminal, editReason(err)
		return false, g
	}
	if len(source) == 0 || len(landed) == 0 {
		g.ObjectID, g.Reason = c.Terminal, "edits_unavailable"
		return false, g
	}
	return sameEdits(source, landed), Gap{}
}

func editReason(err error) string {
	if errors.Is(err, errEdits) {
		return "edits_unavailable"
	}
	return graphReason(err)
}

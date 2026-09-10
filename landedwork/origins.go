package landedwork

import (
	"cmp"
	"context"
	"slices"
)

func fullSpan(p *Interval) Span {
	return Span{Before: p.query.Bounds.Base, Through: p.query.Bounds.Head}
}

func candidateGap(p *Interval, c Candidate, g Gap) Gap {
	g.Span = fullSpan(p)
	if c.TerminalEvidence != "" && slices.Contains(p.spine, c.Terminal) {
		g.Span.Through = c.Terminal
	}
	if g.Reason == "input_budget_exhausted" {
		g.Span = fullSpan(p)
	}
	return g
}

func ownedSpine(l Landing) []string {
	return l.Spine
}

func landingOwners(landings []Landing) map[string]bool {
	owners := map[string]bool{}
	for _, l := range landings {
		for _, id := range ownedSpine(l) {
			owners[id] = true
		}
	}
	return owners
}

func blockedSpine(r *Result, p *Interval) map[string]bool {
	positions := map[string]int{p.query.Bounds.Base: -1}
	for i, id := range p.spine {
		positions[id] = i
	}
	blocked := map[string]bool{}
	for _, g := range r.Coverage.Gaps {
		span := g.Span
		if span == (Span{}) {
			span = fullSpan(p)
		}
		for i := positions[span.Before] + 1; i <= positions[span.Through]; i++ {
			blocked[p.spine[i]] = true
		}
	}
	return blocked
}

func resolveOrigins(ctx context.Context, v *objectView, p *Interval, candidates []Candidate, caps Capabilities, r *Result) error {
	terminals, ids := candidateCounts(candidates)
	candidates = slices.Clone(candidates)
	slices.SortFunc(candidates, func(a, b Candidate) int { return cmp.Compare(a.ID, b.ID) })
	var offSpine []Candidate
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if terminals[c.Terminal] > 1 || ids[c.ID] > 1 {
			r.Coverage.Gaps = append(r.Coverage.Gaps, candidateGap(p, c, Gap{CandidateID: c.ID, ObjectID: c.Terminal, Reason: "candidate_conflict"}))
			continue
		}
		if c.Terminal != "" && c.TerminalEvidence != "" && !slices.Contains(p.spine, c.Terminal) {
			offSpine = append(offSpine, c)
			continue
		}
		l, g := proveAt(ctx, v, p, c, caps)
		if g.Reason != "" {
			r.Coverage.Gaps = append(r.Coverage.Gaps, candidateGap(p, c, g))
		} else {
			r.Landings = append(r.Landings, l)
		}
		if v.meter.failed {
			break
		}
	}
	rejectOverlaps(r)
	if !v.meter.failed {
		for _, c := range offSpine {
			if err := ctx.Err(); err != nil {
				return err
			}
			through, g := integratedThrough(ctx, v, p, c, caps, r.Landings)
			if g.Reason != "" {
				r.Coverage.Gaps = append(r.Coverage.Gaps, candidateGap(p, c, g))
			} else {
				r.Integrated = append(r.Integrated, IntegratedCandidate{CandidateID: c.ID, ThroughCandidateID: through})
			}
			if v.meter.failed {
				break
			}
		}
	}
	if !v.meter.failed {
		return classifyDirectPushes(ctx, v, p, r)
	}
	return ctx.Err()
}

func integratedThrough(ctx context.Context, v *objectView, p *Interval, c Candidate, caps Capabilities, outers []Landing) (string, Gap) {
	g := Gap{CandidateID: c.ID, ObjectID: c.Terminal, Reason: "terminal_outside_spine"}
	if c.Method != "merge" && c.Method != "" {
		return "", g
	}
	inner, failed := proveAt(ctx, v, p, c, caps)
	if failed.Reason != "" {
		return "", failed
	}
	through := ""
	for _, outer := range outers {
		if err := ctx.Err(); err != nil {
			g.Reason = graphReason(err)
			return "", g
		}
		if !slices.Equal(outer.Proofs, []string{"merge"}) {
			continue
		}
		introduced := make(map[string]bool, len(outer.Introduced))
		for _, id := range outer.Introduced {
			if err := ctx.Err(); err != nil {
				g.Reason = graphReason(err)
				return "", g
			}
			introduced[id] = true
		}
		if !introduced[inner.Terminal] {
			continue
		}
		contained := true
		for _, id := range inner.Introduced {
			if err := ctx.Err(); err != nil {
				g.Reason = graphReason(err)
				return "", g
			}
			if !introduced[id] {
				contained = false
				break
			}
		}
		if contained {
			if through != "" {
				g.Reason = "candidate_conflict"
				return "", g
			}
			through = outer.CandidateID
		}
	}
	if through == "" {
		return "", g
	}
	return through, Gap{}
}

func rejectOverlaps(r *Result) {
	owners := map[string]int{}
	conflicts := map[int]bool{}
	for i, l := range r.Landings {
		for _, id := range ownedSpine(l) {
			if previous, ok := owners[id]; ok {
				conflicts[previous], conflicts[i] = true, true
			} else {
				owners[id] = i
			}
		}
	}
	accepted := r.Landings[:0]
	for i, l := range r.Landings {
		if conflicts[i] {
			r.Coverage.Gaps = append(r.Coverage.Gaps, Gap{CandidateID: l.CandidateID, ObjectID: l.Terminal, Reason: "candidate_conflict", Span: Span{Before: l.Before, Through: l.Terminal}})
		} else {
			accepted = append(accepted, l)
		}
	}
	r.Landings = accepted
}

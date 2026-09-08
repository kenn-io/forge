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
	if l.Method == "rebase" || l.Method == "fast_forward" {
		return l.Introduced
	}
	return []string{l.Terminal}
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
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if terminals[c.Terminal] > 1 || ids[c.ID] > 1 {
			r.Coverage.Gaps = append(r.Coverage.Gaps, candidateGap(p, c, Gap{CandidateID: c.ID, ObjectID: c.Terminal, Reason: "candidate_conflict"}))
			continue
		}
		l, g := prove(ctx, v, p, c, caps)
		if g.Reason != "" {
			r.Coverage.Gaps = append(r.Coverage.Gaps, candidateGap(p, c, g))
		} else {
			r.Landings = append(r.Landings, l)
		}
		if v.meter.failed {
			break
		}
	}
	rejectOverlaps(r, p)
	return ctx.Err()
}

func rejectOverlaps(r *Result, p *Interval) {
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
			r.Coverage.Gaps = append(r.Coverage.Gaps, Gap{CandidateID: l.CandidateID, ObjectID: l.Terminal, Reason: "candidate_conflict", Span: Span{Before: p.query.Bounds.Base, Through: l.Terminal}})
		} else {
			accepted = append(accepted, l)
		}
	}
	r.Landings = accepted
}

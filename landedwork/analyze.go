package landedwork

import (
	"cmp"
	"context"
	"errors"
	"slices"
)

// Analyze accepts only evidence for p.Query(). Missing proof returns a named
// coverage gap; malformed inputs, cancellation and unrepresentable output return
// errors. Callers must never publish a result returned with an error.
func Analyze(ctx context.Context, p *Interval, e Evidence, limits Limits) (r Result, err error) {
	if p == nil {
		return r, errors.New("prepared interval required")
	}
	if err = validate(ctx, p.query.Bounds, limits); err != nil {
		return r, err
	}
	r.Coverage = Coverage{Bounds: p.query.Bounds, Inventory: e.Inventory, CertifiedHead: p.query.Bounds.Base,
		Gaps: slices.Clone(p.query.Gaps)}
	m := &meter{limits: limits}
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil {
			for i := range r.Coverage.Gaps {
				if r.Coverage.Gaps[i].Span == (Span{}) {
					r.Coverage.Gaps[i].Span = fullSpan(p)
				}
			}
			if p.query.Complete {
				finishCoverage(&r, p)
			}
			err = checkResultOutput(r, limits)
		}
		if err != nil {
			r = Result{}
		}
	}()
	if err = validateEvidence(p, e, m); err != nil {
		if errors.Is(err, ErrInputBudget) {
			r.Coverage.Gaps = append(r.Coverage.Gaps, Gap{Reason: "input_budget_exhausted"})
			return r, nil
		}
		return r, err
	}
	if !p.query.Complete {
		return r, nil
	}
	if !e.Inventory.Supported || !e.Inventory.Complete {
		reason := e.Inventory.Reason
		if reason == "" {
			reason = "inventory_unknown"
		}
		r.Coverage.Gaps = append(r.Coverage.Gaps, Gap{ObjectID: e.Inventory.NextCommit, Reason: reason})
	}
	v, openErr := openView(ctx, p.path, m)
	if openErr != nil {
		r.Coverage.Gaps = append(r.Coverage.Gaps, Gap{Reason: graphReason(openErr)})
		return r, nil
	}
	defer func() { err = errors.Join(err, v.close()) }()
	if shallowErr := v.checkShallow(ctx, p.query.Bounds); shallowErr != nil {
		r.Coverage.Gaps = append(r.Coverage.Gaps, Gap{Reason: graphReason(shallowErr)})
		return r, nil
	}
	err = resolveOrigins(ctx, v, p, e.Candidates, e.Capabilities, &r)
	return r, err
}

func candidateCounts(candidates []Candidate) (map[string]int, map[string]int) {
	terminals, ids := map[string]int{}, map[string]int{}
	for _, c := range candidates {
		if c.Terminal != "" {
			terminals[c.Terminal]++
		}
		ids[c.ID]++
	}
	return terminals, ids
}

func finishCoverage(r *Result, p *Interval) {
	owners, positions := map[string]bool{}, map[string]int{}
	for _, landing := range r.Landings {
		for _, id := range ownedSpine(landing) {
			owners[id] = true
		}
	}
	blocked := blockedSpine(r, p)
	complete := true
	for index, id := range p.spine {
		positions[id] = index
		if blocked[id] {
			complete = false
		}
		if !owners[id] {
			r.Unattributed = append(r.Unattributed, id)
			complete = false
		}
		if complete {
			r.Coverage.CertifiedHead = id
		}
	}
	r.Coverage.Complete = complete && len(r.Coverage.Gaps) == 0
	slices.SortFunc(r.Landings, func(a, b Landing) int { return cmp.Compare(positions[a.Terminal], positions[b.Terminal]) })
	slices.SortFunc(r.Coverage.Gaps, func(a, b Gap) int {
		if n := cmp.Compare(a.CandidateID, b.CandidateID); n != 0 {
			return n
		}
		if n := cmp.Compare(a.Reason, b.Reason); n != 0 {
			return n
		}
		if n := cmp.Compare(a.ObjectID, b.ObjectID); n != 0 {
			return n
		}
		if n := cmp.Compare(a.Span.Before, b.Span.Before); n != 0 {
			return n
		}
		return cmp.Compare(a.Span.Through, b.Span.Through)
	})
}

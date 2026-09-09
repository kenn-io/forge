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
	start := end - len(c.Source)
	if start < 0 || end == 0 {
		return reject("range_crosses_base")
	}
	if c.Source[len(c.Source)-1] != c.SourceHead {
		return reject("source_head_mismatch")
	}
	if reason, object := checkSources(ctx, v, c); reason != "" {
		gap.ObjectID = object
		return reject(reason)
	}
	landed := p.spine[start:end]
	before := p.query.Bounds.Base
	if start > 0 {
		before = p.spine[start-1]
	}
	if _, err := v.parents(ctx, before); err != nil {
		gap.ObjectID = before
		return reject(graphReason(err))
	}
	for i, source := range c.Source {
		for _, id := range []string{source, landed[i]} {
			gap.ObjectID = id
			parents, err := v.parents(ctx, id)
			if err != nil {
				return reject(graphReason(err))
			}
			if len(parents) != 1 {
				return reject("topology_unproven")
			}
			if id == source && i > 0 && parents[0] != c.Source[i-1] {
				return reject("source_order_unproven")
			}
		}
	}
	if object, err := rangeCorrespondence(ctx, v, c, landed); err != nil {
		gap.ObjectID = object
		if errors.Is(err, errCorrespondence) {
			return reject("source_correspondence_unproven")
		}
		if errors.Is(err, errEdits) {
			return reject("edits_unavailable")
		}
		return reject(graphReason(err))
	}
	return Landing{CandidateID: c.ID, Proofs: []string{c.Method}, Spine: slices.Clone(landed), Before: before, Terminal: c.Terminal,
		Source: slices.Clone(c.Source), Introduced: slices.Clone(landed)}, Gap{}
}

func rangeCorrespondence(ctx context.Context, v *objectView, c Candidate, landed []string) (string, error) {
	var rewritten [][]fileEdit
	for i, source := range c.Source {
		if err := ctx.Err(); err != nil {
			return source, err
		}
		if source == landed[i] {
			continue
		}
		if c.Method == "fast_forward" {
			return source, errCorrespondence
		}
		a, err := v.commitEdits(ctx, source)
		if err != nil {
			return source, err
		}
		b, err := v.commitEdits(ctx, landed[i])
		if err != nil {
			return landed[i], err
		}
		if len(a) == 0 || len(b) == 0 {
			return source, errEdits
		}
		if !sameEdits(a, b) {
			return source, errCorrespondence
		}
		for _, previous := range rewritten {
			if err := ctx.Err(); err != nil {
				return source, err
			}
			if sameEdits(previous, a) {
				return source, errEdits
			}
		}
		rewritten = append(rewritten, a)
	}
	return "", nil
}

func rangeAlternatives(ctx context.Context, v *objectView, p *Interval, c Candidate) [2]proofAttempt {
	r, possible, object, err := v.firstParentSuffix(ctx, p, c.Terminal, len(c.Source))
	if err != nil {
		a := proofAttempt{gap: Gap{CandidateID: c.ID, ObjectID: object, Reason: graphReason(err)}}
		return [2]proofAttempt{a, a}
	}
	if !possible {
		return [2]proofAttempt{{state: attemptMismatch}, {state: attemptMismatch}}
	}
	var attempts [2]proofAttempt
	for i, method := range []string{"rebase", "fast_forward"} {
		c.Method = method
		object, err := rangeCorrespondence(ctx, v, c, r.commits)
		switch {
		case errors.Is(err, errCorrespondence):
			attempts[i].state = attemptMismatch
		case err != nil:
			attempts[i].gap = Gap{CandidateID: c.ID, ObjectID: object, Reason: editReason(err)}
		default:
			attempts[i] = proofAttempt{state: attemptMatch, crossesBase: r.crossesBase,
				landing: Landing{CandidateID: c.ID, Proofs: []string{method}, Before: r.before, Terminal: c.Terminal,
					Source: slices.Clone(c.Source), Spine: slices.Clone(r.commits), Introduced: slices.Clone(r.commits)}}
		}
	}
	return attempts
}

package landedwork

import (
	"context"
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
		if source != landed[i] {
			return reject("source_correspondence_unproven")
		}
	}
	return Landing{CandidateID: c.ID, Method: c.Method, Before: before, Terminal: c.Terminal,
		Source: slices.Clone(c.Source), Introduced: slices.Clone(landed)}, Gap{}
}

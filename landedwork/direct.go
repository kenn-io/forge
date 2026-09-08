package landedwork

import "context"

func classifyDirectPushes(ctx context.Context, v *objectView, p *Interval, r *Result) error {
	blocked := blockedSpine(r, p)
	owners := map[string]bool{}
	for _, l := range r.Landings {
		for _, id := range ownedSpine(l) {
			owners[id] = true
		}
	}
	before := p.query.Bounds.Base
	for _, id := range p.spine {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !blocked[id] && !owners[id] {
			d, g := directPush(ctx, v, before, id)
			if g.Reason != "" {
				g.Span = Span{Before: before, Through: id}
				if v.meter.failed {
					g.Span.Through = p.query.Bounds.Head
				}
				r.Coverage.Gaps = append(r.Coverage.Gaps, g)
				if v.meter.failed {
					break
				}
			} else {
				r.DirectPushes = append(r.DirectPushes, d)
			}
		}
		before = id
	}
	return ctx.Err()
}

func directPush(ctx context.Context, v *objectView, before, terminal string) (DirectPush, Gap) {
	g := Gap{ObjectID: terminal}
	parents, err := v.parents(ctx, terminal)
	if err != nil {
		g.Reason = graphReason(err)
		return DirectPush{}, g
	}
	if len(parents) == 0 || parents[0] != before {
		g.Reason = "topology_unproven"
		return DirectPush{}, g
	}
	if _, err = v.parents(ctx, before); err != nil {
		g.ObjectID = before
		g.Reason = graphReason(err)
		return DirectPush{}, g
	}
	ids, err := v.introduced(ctx, before, terminal)
	if err != nil {
		g.Reason = graphReason(err)
		return DirectPush{}, g
	}
	for _, id := range ids {
		if _, err = v.parents(ctx, id); err != nil {
			g.ObjectID = id
			g.Reason = graphReason(err)
			return DirectPush{}, g
		}
	}
	return DirectPush{Before: before, Terminal: terminal, Introduced: ids}, Gap{}
}

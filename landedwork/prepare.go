package landedwork

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
)

// Prepare enumerates the pinned interval without fetching or selecting a remote.
// Valid input with missing graph evidence returns an incomplete Interval.
func Prepare(ctx context.Context, path string, bounds Bounds, limits Limits) (p *Interval, err error) {
	if err = validate(ctx, bounds, limits); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("explicit repository path required")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p = &Interval{path: path, query: Query{Bounds: bounds}}
	m := &meter{limits: limits}
	defer func() {
		if ctx.Err() != nil {
			p, err = nil, ctx.Err()
			return
		}
		if err != nil {
			return
		}
		if err = checkQueryOutput(p.query, limits); err != nil {
			p = nil
		}
	}()
	v, openErr := openView(ctx, path, m)
	if openErr != nil {
		p.query.Gaps = []Gap{{Reason: graphReason(openErr)}}
		return p, nil
	}
	defer func() { err = errors.Join(err, v.close()) }()
	if shallowErr := v.checkShallow(ctx, bounds); shallowErr != nil {
		p.query.Gaps = []Gap{{Reason: graphReason(shallowErr)}}
		return p, nil
	}
	if _, readErr := v.parents(ctx, bounds.Base); readErr != nil {
		p.query.Gaps = []Gap{{ObjectID: bounds.Base, Reason: graphReason(readErr)}}
		return p, nil
	}
	current := bounds.Head
	for current != bounds.Base {
		parents, readErr := v.parents(ctx, current)
		if readErr != nil {
			p.query.Gaps = []Gap{{ObjectID: current, Reason: graphReason(readErr)}}
			return p, nil
		}
		if len(parents) == 0 {
			p.query.Gaps = []Gap{{Reason: "base_not_first_parent"}}
			return p, nil
		}
		p.spine = append(p.spine, current)
		current = parents[0]
	}
	slices.Reverse(p.spine)
	ids, readErr := v.introduced(ctx, bounds.Base, bounds.Head)
	if readErr != nil {
		p.query.Gaps = []Gap{{Reason: graphReason(readErr)}}
		return p, nil
	}
	slices.Sort(ids)
	p.query.Commits, p.query.Complete = ids, true
	return p, nil
}

func graphReason(err error) string {
	if errors.Is(err, ErrInputBudget) {
		return "input_budget_exhausted"
	}
	if errors.Is(err, errShallow) {
		return "shallow_boundary"
	}
	return "objects_unavailable"
}

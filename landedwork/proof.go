package landedwork

import (
	"context"
	"errors"
	"slices"
)

func prove(ctx context.Context, v *objectView, p *Interval, c Candidate, caps Capabilities) (Landing, Gap) {
	if c.Terminal != "" && c.TerminalEvidence != "" && !slices.Contains(p.spine, c.Terminal) {
		return Landing{}, Gap{CandidateID: c.ID, ObjectID: c.Terminal, Reason: "terminal_outside_spine"}
	}
	return proveAt(ctx, v, p, c, caps)
}

// proveAt checks the method independently of its admission to the target spine.
func proveAt(ctx context.Context, v *objectView, p *Interval, c Candidate, caps Capabilities) (Landing, Gap) {
	gap := Gap{CandidateID: c.ID, ObjectID: c.Terminal}
	reject := func(reason string) (Landing, Gap) { gap.Reason = reason; return Landing{}, gap }
	if (c.Method == "rebase" && !caps.Rebase) || (c.Method == "fast_forward" && !caps.FastForward) {
		return reject("method_unsupported")
	}
	if c.Terminal == "" || c.TerminalEvidence == "" {
		return reject("terminal_unproven")
	}
	if c.Method != "" && c.MethodEvidence == "" {
		return reject("method_unproven")
	}
	if !c.SourceComplete || len(c.Source) == 0 || c.SourceHead == "" {
		return reject("source_incomplete")
	}
	if c.Method == "rebase" || c.Method == "fast_forward" {
		return proveRange(ctx, v, p, c)
	}
	parents, err := v.parents(ctx, c.Terminal)
	if err != nil {
		return reject(graphReason(err))
	}
	method := c.Method
	if method == "" && len(parents) == 2 {
		method = "merge"
	}
	if method == "" {
		return reject("method_unproven")
	}
	if (method == "merge" && !caps.Merge) || (method == "squash" && !caps.Squash) {
		return reject("method_unsupported")
	}
	if (method == "merge" && len(parents) != 2) || (method == "squash" && len(parents) != 1) {
		return reject("topology_unproven")
	}
	if method == "merge" && parents[1] != c.SourceHead {
		return reject("source_head_mismatch")
	}
	if reason, object := checkSources(ctx, v, c); reason != "" {
		gap.ObjectID = object
		return reject(reason)
	}
	// Cached ancestry can outlive the object needed for the boundary diff.
	if err := v.meter.node(); err != nil {
		gap.ObjectID = parents[0]
		return reject(graphReason(err))
	}
	if _, err := v.run(ctx, "cat-file", "-e", parents[0]+"^{commit}"); err != nil {
		gap.ObjectID = parents[0]
		return reject(graphReason(err))
	}
	introduced := []string{c.Terminal}
	if method == "merge" {
		introduced, err = mergeCorrespondence(ctx, v, c, parents[0])
		if err != nil {
			if errors.Is(err, errCorrespondence) {
				return reject("source_correspondence_unproven")
			}
			return reject(graphReason(err))
		}
	}
	return Landing{CandidateID: c.ID, Method: method, Before: parents[0], Terminal: c.Terminal,
		Source: slices.Clone(c.Source), Introduced: introduced}, Gap{}
}

func checkSources(ctx context.Context, v *objectView, c Candidate) (string, string) {
	seen := make(map[string]bool, len(c.Source))
	for _, id := range c.Source {
		if id == "" || seen[id] {
			return "source_invalid", id
		}
		seen[id] = true
		if _, err := v.parents(ctx, id); err != nil {
			return graphReason(err), id
		}
	}
	if !seen[c.SourceHead] {
		return "source_incomplete", c.SourceHead
	}
	return "", ""
}

var errCorrespondence = errors.New("source correspondence unproven")

func mergeCorrespondence(ctx context.Context, v *objectView, c Candidate, before string) ([]string, error) {
	ids, err := v.introduced(ctx, before, c.Terminal)
	if err != nil {
		return nil, err
	}
	ids = slices.DeleteFunc(ids, func(id string) bool { return id == c.Terminal })
	expected := make([]string, 0, len(c.Source))
	for _, id := range c.Source {
		present, err := v.ancestor(ctx, id, before)
		if err != nil {
			return nil, err
		}
		if !present {
			expected = append(expected, id)
		}
	}
	slices.Sort(ids)
	slices.Sort(expected)
	if !slices.Equal(ids, expected) {
		return nil, errCorrespondence
	}
	return ids, nil
}

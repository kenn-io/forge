package landedwork

import (
	"bytes"
	"context"
	"go.kenn.io/forge/platform"
)

type originProof struct {
	candidate          platform.LandingCandidate
	start, end         int
	source, introduced []Commit
	invalid            bool
}

func prove(ctx context.Context, g *graph, candidate platform.LandingCandidate, caps platform.LandingCapabilities, spine []Commit, index int) (*originProof, string) {
	// Two-parent topology plus an independently provider-bound terminal proves
	// an ordinary merge. One-parent topology does not distinguish squash from
	// rebase and never selects a method by itself.
	if candidate.Method == "" && candidate.TerminalProof != "" && len(spine[index].Parents) == 2 {
		candidate.Method = platform.LandingMerge
		candidate.MethodProof = candidate.TerminalProof + "/two-parent-topology"
	}
	supported := false
	switch candidate.Method {
	case platform.LandingMerge:
		supported = caps.OrdinaryMerge
	case platform.LandingSquash:
		supported = caps.Squash
	case platform.LandingRebase:
		supported = caps.RebaseRange
	case platform.LandingFastForward:
		supported = caps.FastForwardRange
	}
	if !supported {
		return nil, "landing_method_unsupported"
	}
	if candidate.MethodProof == "" || !candidate.SourceComplete || len(candidate.SourceCommits) == 0 {
		return nil, "landing_proof_incomplete"
	}
	if err := g.meter.Records(int64(len(candidate.SourceCommits))); err != nil {
		return nil, "input_budget_exhausted"
	}
	proof := &originProof{candidate: candidate, start: index, end: index}
	seen := make(map[string]bool, len(candidate.SourceCommits))
	for _, id := range candidate.SourceCommits {
		if seen[id] {
			return nil, "source_correspondence_unproven"
		}
		seen[id] = true
		commit, err := g.get(ctx, id)
		if err != nil {
			return nil, "source_objects_unavailable"
		}
		proof.source = append(proof.source, commit)
	}
	terminal := spine[index]
	switch candidate.Method {
	case platform.LandingMerge:
		if len(terminal.Parents) != 2 || !candidate.SourceHeadSHA.Present || candidate.SourceHeadSHA.Value != terminal.Parents[1] {
			return nil, "landing_proof_incomplete"
		}
		var err error
		proof.introduced, err = g.introduced(ctx, terminal.Parents[0], terminal.ID)
		if err != nil {
			return nil, "source_objects_unavailable"
		}
		if len(proof.introduced) != len(seen) {
			return nil, "source_correspondence_unproven"
		}
		for _, commit := range proof.introduced {
			if !seen[commit.ID] {
				return nil, "source_correspondence_unproven"
			}
		}
	case platform.LandingSquash:
		if len(terminal.Parents) != 1 {
			return nil, "landing_proof_incomplete"
		}
		proof.introduced = []Commit{terminal}
	case platform.LandingRebase, platform.LandingFastForward:
		proof.start = index - len(proof.source) + 1
		if proof.start < 0 {
			return nil, "range_crosses_base"
		}
		sourceEdits := make(map[string]int)
		landedEdits := make(map[string]int)
		// Direct object identity is sufficient even for empty/duplicate edits.
		// Rewritten pairs must additionally be unique among every source and range
		// member; comparing only the rewritten subset would miss ambiguity.
		patches := make([]Patch, len(proof.source))
		landedPatches := make([]Patch, len(proof.source))
		rewritten := false
		for offset, source := range proof.source {
			landed := spine[proof.start+offset]
			if len(source.Parents) != 1 || len(landed.Parents) != 1 || offset > 0 && source.Parents[0] != proof.source[offset-1].ID {
				return nil, "range_topology_unproven"
			}
			if source.ID != landed.ID {
				rewritten = true
			}
		}
		if rewritten {
			for offset, source := range proof.source {
				landed := spine[proof.start+offset]
				a, err := g.source.Patch(ctx, source.Parents[0], source.ID, g.meter)
				if err != nil {
					return nil, "patch_correspondence_unavailable"
				}
				b, err := g.source.Patch(ctx, landed.Parents[0], landed.ID, g.meter)
				if err != nil {
					return nil, "patch_correspondence_unavailable"
				}
				patches[offset], landedPatches[offset] = a, b
				sourceEdits[string(a.Bytes)]++
				landedEdits[string(b.Bytes)]++
			}
		}
		for offset, source := range proof.source {
			landed := spine[proof.start+offset]
			if source.ID != landed.ID {
				a, b := patches[offset], landedPatches[offset]
				if a.Empty || b.Empty || !bytes.Equal(a.Bytes, b.Bytes) || sourceEdits[string(a.Bytes)] != 1 || landedEdits[string(b.Bytes)] != 1 {
					return nil, "patch_correspondence_unproven"
				}
			}
			proof.introduced = append(proof.introduced, landed)
		}
	}
	return proof, ""
}

package landedwork

import "slices"

type attemptState uint8

const (
	attemptInconclusive attemptState = iota
	attemptMismatch
	attemptMatch
)

type proofAttempt struct {
	state       attemptState
	landing     Landing
	gap         Gap
	crossesBase bool
}

type firstParentRange struct {
	before  string
	commits []string
}

// Order determines only the reported inconclusive reason, never a winning
// method. An unavailable alternative must not lose to an earlier match.
func resolveAlternatives(c Candidate, attempts []proofAttempt, budgetFailed bool) (Landing, Gap) {
	g := Gap{CandidateID: c.ID, ObjectID: c.Terminal}
	if budgetFailed {
		g.Reason = "input_budget_exhausted"
		return Landing{}, g
	}
	for _, a := range attempts {
		if a.state == attemptInconclusive {
			return Landing{}, a.gap
		}
	}
	for _, a := range attempts {
		if a.state == attemptMatch && a.crossesBase {
			g.Reason = "range_crosses_base"
			return Landing{}, g
		}
	}
	var accepted Landing
	for _, a := range attempts {
		if a.state != attemptMatch {
			continue
		}
		if accepted.CandidateID == "" {
			accepted = a.landing
		} else {
			if !sameOrigin(accepted, a.landing) {
				g.Reason = "origin_ambiguous"
				return Landing{}, g
			}
			accepted.Proofs = append(accepted.Proofs, a.landing.Proofs...)
		}
	}
	if accepted.CandidateID == "" {
		g.Reason = "source_correspondence_unproven"
		return Landing{}, g
	}
	slices.Sort(accepted.Proofs)
	accepted.Proofs = slices.Compact(accepted.Proofs)
	return accepted, Gap{}
}

func sameOrigin(a, b Landing) bool {
	if a.CandidateID != b.CandidateID || a.Before != b.Before || a.Terminal != b.Terminal || !slices.Equal(a.Spine, b.Spine) {
		return false
	}
	x, y := slices.Clone(a.Introduced), slices.Clone(b.Introduced)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

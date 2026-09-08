package landedwork

import (
	"errors"
	"slices"
)

// Inventory separates provider capability from observed collection coverage.
// Continuations identify where an incomplete collection stopped.
type Inventory struct {
	Supported, Complete          bool
	Reason, NextCommit, NextPage string
}

// Capabilities authorize generic proof paths, not inferred provider support.
type Capabilities struct{ Merge, Squash, Rebase, FastForward bool }

// Candidate contains provider facts bound to a stable target repository.
// Evidence labels name the facts establishing method and terminal, not guesses
// from subjects, current settings, or patch similarity. Source is complete only
// after every required source page was collected; order is preserved.
type Candidate struct {
	Repository                               Repository
	ID, Terminal, SourceHead                 string
	Source                                   []string
	SourceComplete                           bool
	Method, MethodEvidence, TerminalEvidence string
}

type Evidence struct {
	Query        Query
	Inventory    Inventory
	Capabilities Capabilities
	Candidates   []Candidate
}

type Landing struct {
	CandidateID, Method, Before, Terminal string
	Source, Introduced                    []string
}

type Coverage struct {
	Bounds        Bounds
	Inventory     Inventory
	Complete      bool
	CertifiedHead string
	Gaps          []Gap
}

// IntegratedCandidate arrived through an ordinary merge, without a second origin.
type IntegratedCandidate struct{ CandidateID, ThroughCandidateID string }

// DirectPush is a graph origin without an associated provider landing. It does
// not establish a pusher or a trusted ref-update time. Introduced includes merges.
type DirectPush struct {
	Before, Terminal string
	Introduced       []string
}

// Result is evidence, never a total when Coverage.Complete is false.
type Result struct {
	Landings     []Landing
	Integrated   []IntegratedCandidate
	DirectPushes []DirectPush
	Unattributed []string
	Coverage     Coverage
}

func validateEvidence(p *Interval, e Evidence, m *meter) error {
	if e.Query.Bounds != p.query.Bounds || e.Query.Complete != p.query.Complete ||
		!slices.Equal(e.Query.Commits, p.query.Commits) || !slices.Equal(e.Query.Gaps, p.query.Gaps) {
		return errors.New("evidence does not match the prepared query")
	}
	if err := m.records(int64(len(e.Candidates) + len(e.Query.Commits) + len(e.Query.Gaps))); err != nil {
		return err
	}
	for _, c := range e.Candidates {
		if err := m.records(int64(len(c.Source))); err != nil {
			return err
		}
		if err := m.input(candidateBytes(c)); err != nil {
			return err
		}
		if c.Repository != p.query.Bounds.Repository || c.ID == "" {
			return errors.New("candidate identity mismatch")
		}
		for _, id := range append([]string{c.Terminal, c.SourceHead}, c.Source...) {
			if id != "" && (!objectID(id) || len(id) != len(p.query.Bounds.Head)) {
				return errors.New("invalid candidate object ID")
			}
		}
		switch c.Method {
		case "", "merge", "squash", "rebase", "fast_forward":
		default:
			return errors.New("invalid landing method")
		}
	}
	return m.input(queryBytes(e.Query) + inventoryBytes(e.Inventory))
}

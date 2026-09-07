// Package landedwork proves landing boundaries from pinned Git objects and
// caller-supplied provider evidence. It neither collects nor resolves identities.
package landedwork

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	"go.kenn.io/forge/platform"
)

// Repository is a provider-verified identity, never an owner/name route.
type Repository struct {
	Provider platform.Kind
	Host, ID string
}

type Bounds struct {
	Repository Repository
	Base, Head string
}

// Limits are per-operation positive maxima, not production defaults. Records
// bounds collection entries; Nodes charges commit reads and reachability probes.
// InputBytes charges supplied evidence and Git stdout/stderr. OutputBytes is
// the sum of returned string bytes, counting every occurrence, not a wire size.
// Git's internal traversal work is bounded by the required context deadline.
type Limits struct{ Records, Nodes, InputBytes, OutputBytes int64 }

type Gap struct{ CandidateID, ObjectID, Reason string }

// Query is the exact newly reachable set to collect, including side ancestry.
type Query struct {
	Bounds   Bounds
	Commits  []string
	Complete bool
	Gaps     []Gap
}

// Interval retains private preparation state. Query returns independent copies.
type Interval struct {
	path  string
	query Query
	spine []string
}

func (p *Interval) Query() Query {
	q := p.query
	q.Commits = slices.Clone(q.Commits)
	q.Gaps = slices.Clone(q.Gaps)
	return q
}

// ErrInputBudget and ErrOutputBudget distinguish finite work from empty work.
var (
	ErrInputBudget  = errors.New("landing input budget exhausted")
	ErrOutputBudget = errors.New("landing output budget exhausted")
)

func validate(ctx context.Context, b Bounds, l Limits) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("landing analysis requires a deadline")
	}
	if l.Records <= 0 || l.Nodes <= 0 || l.InputBytes <= 0 || l.OutputBytes <= 0 {
		return errors.New("landing limits must be positive")
	}
	if !validRepository(b.Repository) || !objectID(b.Base) || !objectID(b.Head) || len(b.Base) != len(b.Head) {
		return errors.New("landing analysis requires repository identity and full object IDs")
	}
	return nil
}

func validRepository(r Repository) bool {
	switch r.Provider {
	case platform.KindGitHub, platform.KindGitLab, platform.KindForgejo, platform.KindGitea:
	default:
		return false
	}
	return r.ID != "" && r.Host != "" && !strings.ContainsAny(r.Host, " /\t\r\n") && strings.ToLower(r.Host) == r.Host
}

func objectID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}

package collect

import (
	"context"
	"reflect"
	"slices"
	"strconv"

	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
)

func (c *collector) observe(ctx context.Context, ref platform.LandingChangeRef, policy platform.LandingSourcePolicy) (Observation, error) {
	o := Observation{Change: platform.LandingChange{Ref: ref}}
	if !c.call() || !c.records(1) {
		return c.incomplete(o, "exhausted_limits", "detail", ""), nil
	}
	detail, err := c.reader.GetLandingChange(ctx, c.route, ref)
	if err != nil {
		return c.incomplete(o, c.reason(err), "detail", ""), nil
	}
	if err := c.identity(detail, ref); err != nil {
		return Observation{}, err
	}
	if !c.chargeAccounts(detail) {
		// The detail record was already charged; omit only the uncharged roles.
		detail.Author, detail.Merger = nil, nil
		o.Change = cloneChange(detail)
		return c.incomplete(o, "exhausted_limits", "detail", ""), nil
	}
	o.Change = cloneChange(detail)
	if detail.TargetID <= 0 || detail.Merged == nil {
		return c.incomplete(o, "invalid_observation", "detail", ""), nil
	}
	if !*detail.Merged {
		return o, nil
	}
	if !validChange(detail, len(c.result.Evidence.Query.Bounds.Head)) {
		return c.incomplete(o, "invalid_observation", "detail", ""), nil
	}
	if detail.SourceCount == nil && policy.RequireCount || detail.SourceCount != nil && (*detail.SourceCount < 0 || policy.MaxCommits > 0 && *detail.SourceCount > policy.MaxCommits) {
		return c.incomplete(o, "provider_truncation", "detail", ""), nil
	}
	o = c.sources(ctx, o)
	if o.Reason != "" {
		return o, nil
	}
	if detail.SourceCount != nil && int64(len(o.Source)) != *detail.SourceCount || !slices.Contains(o.Source, *detail.SourceHead) {
		return c.incomplete(o, "provider_truncation", "source", ""), nil
	}
	if !c.call() || !c.records(1) {
		return c.incomplete(o, "exhausted_limits", "detail_recheck", ""), nil
	}
	after, err := c.reader.GetLandingChange(ctx, c.route, ref)
	if err != nil {
		return c.incomplete(o, c.reason(err), "detail_recheck", ""), nil
	}
	if err := c.identity(after, ref); err != nil {
		return Observation{}, err
	}
	if !c.chargeAccounts(after) {
		return c.incomplete(o, "exhausted_limits", "detail_recheck", ""), nil
	}
	if after.TargetID <= 0 {
		return c.incomplete(o, "invalid_observation", "detail_recheck", ""), nil
	}
	if !sameProof(o.Change, after) {
		return c.incomplete(o, "changed_observation", "detail_recheck", ""), nil
	}
	o.Change = cloneChange(after)
	o.SourceComplete = true
	return o, nil
}

func sameProof(a, b platform.LandingChange) bool {
	// Metadata may change independently of the source/landing evidence. Compare
	// all proof fields, including pointer presence, without profile stabilization.
	a.Author, a.Merger, a.OpenedAt, a.MergedAt = nil, nil, nil, nil
	b.Author, b.Merger, b.OpenedAt, b.MergedAt = nil, nil, nil, nil
	return reflect.DeepEqual(a, b)
}

func (c *collector) chargeAccounts(d platform.LandingChange) bool {
	var n int64
	for _, a := range []*platform.Account{d.Author, d.Merger} {
		if a != nil {
			n++
		}
	}
	return c.records(n)
}

func (c *collector) sources(ctx context.Context, o Observation) Observation {
	cursor := ""
	seen, ids := make(map[string]bool), make(map[string]bool)
	for {
		if !c.call() {
			return c.incomplete(o, "exhausted_limits", "source", cursor)
		}
		page, err := c.reader.ListLandingSource(ctx, c.route, o.Change.Ref, cursor)
		if err != nil {
			return c.incomplete(o, c.reason(err), "source", cursor)
		}
		if r := checkPage(c.reader, cursor, page, seen); r != "" {
			return c.incomplete(o, r, "source", cursor)
		}
		if !c.records(1 + int64(len(page.Items))*2) {
			return c.incomplete(o, "exhausted_limits", "source", cursor)
		}
		for _, sha := range page.Items {
			if !objectID(sha, len(c.result.Evidence.Query.Bounds.Head)) || ids[sha] {
				return c.incomplete(o, "provider_truncation", "source", cursor)
			}
			ids[sha] = true
			o.Source = append(o.Source, sha)
		}
		if page.Exhausted {
			return o
		}
		cursor = page.NextCursor
	}
}

func (c *collector) incomplete(o Observation, reason, stage, page string) Observation {
	o.Reason, o.FailureStage, o.NextPage = reason, stage, page
	c.stop(reason, "", "")
	return o
}

func (c *collector) identity(d platform.LandingChange, ref platform.LandingChangeRef) error {
	if d.Ref != ref || d.TargetID != 0 && strconv.FormatInt(d.TargetID, 10) != c.result.Evidence.Query.Bounds.Repository.ID {
		return platform.ErrLandingIdentityMismatch
	}
	return nil
}

func validChange(d platform.LandingChange, size int) bool {
	if d.SourceHead == nil || !objectID(*d.SourceHead, size) || d.SourceID != nil && *d.SourceID <= 0 {
		return false
	}
	for _, sha := range []*string{d.MergeSHA, d.SquashSHA} {
		if sha != nil && *sha != "" && !objectID(*sha, size) {
			return false
		}
	}
	return d.Terminal == "" || objectID(d.Terminal, size)
}

func clonePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	return new(*p)
}

func cloneChange(d platform.LandingChange) platform.LandingChange {
	d.SourceID, d.Merged = clonePointer(d.SourceID), clonePointer(d.Merged)
	d.MergeSHA, d.SquashSHA, d.SourceHead = clonePointer(d.MergeSHA), clonePointer(d.SquashSHA), clonePointer(d.SourceHead)
	d.SourceCount = clonePointer(d.SourceCount)
	d.Author, d.Merger = cloneAccount(d.Author), cloneAccount(d.Merger)
	d.OpenedAt, d.MergedAt = clonePointer(d.OpenedAt), clonePointer(d.MergedAt)
	return d
}

func cloneAccount(a *platform.Account) *platform.Account {
	if a == nil {
		return nil
	}
	return &platform.Account{ID: clonePointer(a.ID), Login: clonePointer(a.Login), Type: a.Type}
}

func candidate(bounds landedwork.Bounds, o Observation) landedwork.Candidate {
	c := landedwork.Candidate{Repository: bounds.Repository, ID: strconv.FormatInt(o.Change.Ref.ID, 10), Source: slices.Clone(o.Source), SourceComplete: o.SourceComplete}
	// Preserve malformed provider values in the observation, not the analyzer's
	// object-ID fields: missing evidence is a gap, invalid input is an error.
	if objectID(o.Change.Terminal, len(bounds.Head)) {
		c.Terminal, c.TerminalEvidence = o.Change.Terminal, o.Change.TerminalEvidence
	}
	if o.Change.SourceHead != nil && objectID(*o.Change.SourceHead, len(bounds.Head)) {
		c.SourceHead = *o.Change.SourceHead
	}
	return c
}

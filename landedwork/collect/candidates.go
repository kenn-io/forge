package collect

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"

	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
)

func (c *collector) observe(ctx context.Context, ref platform.LandingChangeRef, policy platform.LandingSourcePolicy) (Observation, error) {
	o := Observation{Change: platform.LandingChange{Ref: ref}}
	if !c.call() || !c.records(1) {
		return c.incomplete(o, "exhausted_limits", ""), nil
	}
	detail, err := c.reader.GetLandingChange(ctx, c.route, ref)
	if err != nil {
		return c.incomplete(o, reason(err), ""), nil
	}
	if err := c.identity(detail, ref); err != nil {
		return Observation{}, err
	}
	o.Change = cloneChange(detail)
	if detail.Merged == nil {
		return c.incomplete(o, "invalid_observation", ""), nil
	}
	if !*detail.Merged {
		return o, nil
	}
	if !validChange(detail, len(c.result.Evidence.Query.Bounds.Head)) {
		return c.incomplete(o, "invalid_observation", ""), nil
	}
	if detail.SourceCount == nil && policy.RequireCount || detail.SourceCount != nil && (*detail.SourceCount < 0 || policy.MaxCommits > 0 && *detail.SourceCount > policy.MaxCommits) {
		return c.incomplete(o, "provider_truncation", ""), nil
	}
	o = c.sources(ctx, o)
	if o.Reason != "" {
		return o, nil
	}
	if detail.SourceCount != nil && int64(len(o.Source)) != *detail.SourceCount || !slices.Contains(o.Source, *detail.SourceHead) {
		return c.incomplete(o, "provider_truncation", ""), nil
	}
	if !c.call() || !c.records(1) {
		return c.incomplete(o, "exhausted_limits", ""), nil
	}
	after, err := c.reader.GetLandingChange(ctx, c.route, ref)
	if err != nil {
		return c.incomplete(o, reason(err), ""), nil
	}
	if err := c.identity(after, ref); err != nil {
		return Observation{}, err
	}
	if !reflect.DeepEqual(o.Change, after) {
		return c.incomplete(o, "changed_observation", ""), nil
	}
	o.SourceComplete = true
	return o, nil
}

func (c *collector) sources(ctx context.Context, o Observation) Observation {
	cursor := ""
	seen, ids := make(map[string]bool), make(map[string]bool)
	for {
		if !c.call() {
			return c.incomplete(o, "exhausted_limits", cursor)
		}
		page, err := c.reader.ListLandingSource(ctx, c.route, o.Change.Ref, cursor)
		if err != nil {
			return c.incomplete(o, reason(err), cursor)
		}
		if r := checkPage(c.reader, cursor, page, seen); r != "" {
			return c.incomplete(o, r, cursor)
		}
		if !c.records(1 + int64(len(page.Items))*2) {
			return c.incomplete(o, "exhausted_limits", cursor)
		}
		for _, sha := range page.Items {
			if !objectID(sha, len(c.result.Evidence.Query.Bounds.Head)) || ids[sha] {
				return c.incomplete(o, "provider_truncation", cursor)
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

func (c *collector) incomplete(o Observation, reason, page string) Observation {
	o.Reason = reason
	c.stop(reason, o.Change.Terminal, page)
	return o
}

func (c *collector) identity(d platform.LandingChange, ref platform.LandingChangeRef) error {
	if d.Ref != ref || d.TargetID <= 0 || strconv.FormatInt(d.TargetID, 10) != c.result.Evidence.Query.Bounds.Repository.ID {
		return errors.New("landing change identity mismatch")
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
	return d
}

func candidate(repo landedwork.Repository, o Observation) landedwork.Candidate {
	c := landedwork.Candidate{Repository: repo, ID: strconv.FormatInt(o.Change.Ref.ID, 10), Terminal: o.Change.Terminal, TerminalEvidence: o.Change.TerminalEvidence, Source: slices.Clone(o.Source), SourceComplete: o.SourceComplete}
	if o.Change.SourceHead != nil {
		c.SourceHead = *o.Change.SourceHead
	}
	return c
}

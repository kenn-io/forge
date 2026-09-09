// Package collect obtains provider evidence for a prepared local Git interval.
// It neither traverses Git nor supplies credentials or transport policy.
package collect

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
)

// Limits are positive per-invocation maxima. Calls count reader invocations,
// not HTTP attempts. Records includes all query commits and gaps, charged once
// before collection. OutputBytes counts each retained occurrence of a string.
type Limits struct{ Calls, Records, OutputBytes int64 }

type Observation struct {
	Change         platform.LandingChange
	Source         []string
	SourceComplete bool
	Reason         string
	// FailureStage names association, detail, source, or detail_recheck.
	// NextPage is a diagnostic source cursor, not a resumable collection token.
	FailureStage, NextPage string
}

type Result struct {
	Evidence     landedwork.Evidence
	Observations []Observation
}

type collector struct {
	reader    platform.LandingEvidenceReader
	route     platform.RepoRef
	remaining Limits
	result    Result
	refs      map[int64]platform.LandingChangeRef
	fatal     error
}

// Collect sweeps every queried commit and preserves unfinished candidates.
// Invalid input, identity mismatch, cancellation, or output overflow yields no
// result. Other failures return incomplete inventory, never proven absence.
// Inventory.NextCommit/NextPage describe association-sweep failures only;
// observation failures have their own stage and page. Neither supports resume.
func Collect(ctx context.Context, reader platform.LandingEvidenceReader, route platform.RepoRef, query landedwork.Query, limits Limits) (result Result, err error) {
	if err = validate(ctx, reader, route, query, limits); err != nil {
		return Result{}, err
	}
	query.Commits, query.Gaps = slices.Clone(query.Commits), slices.Clone(query.Gaps)
	support := reader.LandingEvidenceSupport()
	c := collector{reader: reader, route: route, remaining: limits, refs: make(map[int64]platform.LandingChangeRef), result: Result{Evidence: landedwork.Evidence{Query: query, Inventory: landedwork.Inventory{Supported: support.Inventory}, Capabilities: landedwork.Capabilities{Merge: support.OrdinaryMerge}}}}
	c.remaining.Records -= int64(len(query.Commits)) + int64(len(query.Gaps))
	defer func() {
		if c.fatal != nil {
			err = c.fatal
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil && resultBytes(result) > limits.OutputBytes {
			err = landedwork.ErrOutputBudget
		}
		if err != nil {
			result = Result{}
		}
	}()
	if !query.Complete {
		c.stop("preparation_incomplete", "", "")
		return c.result, nil
	}
	if !support.Inventory {
		c.stop("unsupported_discovery", "", "")
		return c.result, nil
	}
	if !c.call() {
		c.stop("exhausted_limits", "", "")
		return c.result, nil
	}
	repo, readErr := reader.GetRepository(ctx, route)
	if readErr != nil {
		c.stop(c.reason(readErr), "", "")
		return c.result, nil
	}
	if repo.Ref.Platform != query.Bounds.Repository.Provider || repo.Ref.Host != query.Bounds.Repository.Host || repo.PlatformID <= 0 || strconv.FormatInt(repo.PlatformID, 10) != query.Bounds.Repository.ID {
		return Result{}, errors.New("landing repository identity mismatch")
	}
	if !c.records(1) {
		c.stop("exhausted_limits", "", "")
		return c.result, nil
	}
	c.sweep(ctx, query.Commits)
	ids := make([]int64, 0, len(c.refs))
	for id := range c.refs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		ref := c.refs[id]
		observation := Observation{Change: platform.LandingChange{Ref: ref, TargetID: ref.TargetID}}
		if ref.TargetID > 0 && strconv.FormatInt(ref.TargetID, 10) != query.Bounds.Repository.ID {
			// Network-wide associations do not establish local PR absence.
			observation.Reason, observation.FailureStage = "foreign_repository", "association"
			c.result.Observations = append(c.result.Observations, observation)
			c.stop("ambiguous_absence", "", "")
			continue
		}
		if ref.TargetID <= 0 {
			observation = c.incomplete(observation, "invalid_observation", "association", "")
		}
		if c.result.Evidence.Inventory.Reason == "" {
			if observation, err = c.observe(ctx, c.refs[id], support.Sources); err != nil {
				return Result{}, err
			}
		}
		if observation.Change.Merged != nil && !*observation.Change.Merged {
			continue
		}
		c.result.Observations = append(c.result.Observations, observation)
		c.result.Evidence.Candidates = append(c.result.Evidence.Candidates, candidate(query.Bounds, observation))
	}
	c.result.Evidence.Inventory.Complete = c.result.Evidence.Inventory.Reason == ""
	return c.result, nil
}

func (c *collector) sweep(ctx context.Context, commits []string) {
	for _, sha := range commits {
		cursor := ""
		seen := make(map[string]bool)
		for {
			if !c.call() {
				c.stop("exhausted_limits", sha, cursor)
				return
			}
			page, err := c.reader.ListLandingAssociations(ctx, c.route, sha, cursor)
			if err != nil {
				c.stop(c.reason(err), sha, cursor)
				return
			}
			if r := checkPage(c.reader, cursor, page, seen); r != "" {
				c.stop(r, sha, cursor)
				return
			}
			if !c.records(1 + int64(len(page.Items))*3) {
				c.stop("exhausted_limits", sha, cursor)
				return
			}
			for _, ref := range page.Items {
				if ref.ID <= 0 || ref.Number <= 0 {
					c.stop("invalid_observation", sha, cursor)
					return
				}
				if old, ok := c.refs[ref.ID]; ok && old != ref {
					c.stop("changed_observation", sha, cursor)
					return
				}
				c.refs[ref.ID] = ref
			}
			if page.Exhausted {
				break
			}
			cursor = page.NextCursor
		}
	}
}

func checkPage[T any](r platform.LandingEvidenceReader, cursor string, p platform.Page[T], seen map[string]bool) string {
	if p.NextCursor != "" && (p.NextCursor == cursor || seen[p.NextCursor]) {
		return "repeated_cursor"
	}
	if platform.ValidatePage(r.Platform(), r.Host(), cursor, p) != nil {
		return "provider_truncation"
	}
	seen[p.NextCursor] = true
	return ""
}

func (c *collector) reason(err error) string {
	switch {
	case errors.Is(err, platform.ErrLandingIdentityMismatch), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		c.fatal = err
		return "request_failed"
	case errors.Is(err, platform.ErrLandingTransportLimit):
		return "exhausted_limits"
	case errors.Is(err, platform.ErrLandingAbsenceAmbiguous):
		return "ambiguous_absence"
	case errors.Is(err, platform.ErrUnsupportedCapability):
		return "unsupported_discovery"
	default:
		return "request_failed"
	}
}

func (c *collector) stop(reason, commit, page string) {
	if c.result.Evidence.Inventory.Reason != "" {
		return
	}
	c.result.Evidence.Inventory.Reason = reason
	c.result.Evidence.Inventory.NextCommit = commit
	c.result.Evidence.Inventory.NextPage = page
}

func (c *collector) call() bool {
	if c.remaining.Calls == 0 {
		return false
	}
	c.remaining.Calls--
	return true
}

func (c *collector) records(n int64) bool {
	if n > c.remaining.Records {
		return false
	}
	c.remaining.Records -= n
	return true
}

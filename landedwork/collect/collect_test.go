package collect_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/landedwork/collect"
	"go.kenn.io/forge/platform"
)

var (
	base   = strings.Repeat("a", 40)
	head   = strings.Repeat("b", 40)
	source = strings.Repeat("c", 40)
	route  = platform.RepoRef{Platform: platform.KindGitHub, Host: "github.com", Owner: "example", Name: "project"}
	query  = landedwork.Query{Bounds: landedwork.Bounds{Repository: landedwork.Repository{Provider: platform.KindGitHub, Host: "github.com", ID: "12"}, Base: base, Head: head}, Commits: []string{head, source}, Complete: true}
	limits = collect.Limits{Calls: 100, Records: 1000, OutputBytes: 100000}
)

type script struct {
	platform.LandingEvidenceReader
	t      *testing.T
	steps  []step
	policy platform.LandingSourcePolicy
}

type step struct {
	key   string
	value any
	err   error
}

func (s *script) next(key string) (any, error) {
	s.t.Helper()
	require.NotEmpty(s.t, s.steps, "unexpected call %s", key)
	v := s.steps[0]
	s.steps = s.steps[1:]
	require.Equal(s.t, v.key, key)
	return v.value, v.err
}

func (s *script) Platform() platform.Kind { return platform.KindGitHub }
func (s *script) Host() string            { return "github.com" }
func (s *script) LandingEvidenceSupport() platform.LandingEvidenceSupport {
	return platform.LandingEvidenceSupport{Inventory: true, OrdinaryMerge: true, Sources: s.policy}
}
func (s *script) GetRepository(_ context.Context, r platform.RepoRef) (platform.Repository, error) {
	require.Equal(s.t, route, r)
	v, err := s.next("repository")
	if err != nil {
		return platform.Repository{}, err
	}
	return v.(platform.Repository), nil
}
func (s *script) ListLandingAssociations(_ context.Context, r platform.RepoRef, sha, cursor string) (platform.Page[platform.LandingChangeRef], error) {
	require.Equal(s.t, route, r)
	v, err := s.next("association/" + sha + "/" + cursor)
	if err != nil {
		return platform.Page[platform.LandingChangeRef]{}, err
	}
	return v.(platform.Page[platform.LandingChangeRef]), nil
}
func (s *script) GetLandingChange(_ context.Context, _ platform.RepoRef, ref platform.LandingChangeRef) (platform.LandingChange, error) {
	require.Equal(s.t, platform.LandingChangeRef{ID: 7, Number: 3}, ref)
	v, err := s.next("detail")
	if err != nil {
		return platform.LandingChange{}, err
	}
	return v.(platform.LandingChange), nil
}
func (s *script) ListLandingSource(_ context.Context, _ platform.RepoRef, ref platform.LandingChangeRef, cursor string) (platform.Page[string], error) {
	require.Equal(s.t, platform.LandingChangeRef{ID: 7, Number: 3}, ref)
	v, err := s.next("source/" + cursor)
	if err != nil {
		return platform.Page[string]{}, err
	}
	return v.(platform.Page[string]), nil
}

func mergedScript(t *testing.T) *script {
	t.Helper()
	detail := platform.LandingChange{Ref: platform.LandingChangeRef{ID: 7, Number: 3}, TargetID: 12, TargetBranch: "main", Merged: new(true), MergeSHA: new(head), SourceHead: new(source), SourceCount: new(int64(1)), Terminal: head, TerminalEvidence: "merged_commit_sha"}
	s := &script{t: t, policy: platform.LandingSourcePolicy{RequireCount: true, MaxCommits: 250}, steps: []step{
		{key: "repository", value: platform.Repository{Ref: route, PlatformID: 12}},
		{key: "association/" + head + "/", value: platform.Page[platform.LandingChangeRef]{Items: []platform.LandingChangeRef{{ID: 7, Number: 3}}, NextCursor: "next"}},
		{key: "association/" + head + "/next", value: platform.Page[platform.LandingChangeRef]{Items: []platform.LandingChangeRef{{ID: 7, Number: 3}}, Exhausted: true}},
		{key: "association/" + source + "/", value: platform.Page[platform.LandingChangeRef]{Exhausted: true}},
		{key: "detail", value: detail},
		{key: "source/", value: platform.Page[string]{Items: []string{source}, Exhausted: true}},
		{key: "detail", value: detail},
	}}
	return s
}

func TestCollectCompleteSweep(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	s := mergedScript(t)
	got, err := collect.Collect(ctx, s, route, query, limits)
	require.NoError(t, err)
	assert := assert.New(t)
	assert.Empty(s.steps)
	assert.Equal(query, got.Evidence.Query)
	assert.True(got.Evidence.Inventory.Complete)
	require.Len(t, got.Evidence.Candidates, 1)
	assert.Equal("7", got.Evidence.Candidates[0].ID)
	assert.Equal([]string{source}, got.Evidence.Candidates[0].Source)
	assert.True(got.Evidence.Candidates[0].SourceComplete)
	assert.Equal(landedwork.Capabilities{Merge: true}, got.Evidence.Capabilities)
}

func TestCollectInterruptedSweepPreservesCandidate(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	s := mergedScript(t)
	s.steps = s.steps[:4]
	s.steps[3] = step{key: "association/" + source + "/", err: errors.New("unavailable")}
	got, err := collect.Collect(ctx, s, route, query, limits)
	require.NoError(t, err)
	assert := assert.New(t)
	assert.False(got.Evidence.Inventory.Complete)
	assert.Equal("request_failed", got.Evidence.Inventory.Reason)
	assert.Equal(source, got.Evidence.Inventory.NextCommit)
	require.Len(t, got.Evidence.Candidates, 1)
	assert.Equal("7", got.Evidence.Candidates[0].ID)
	assert.False(got.Evidence.Candidates[0].SourceComplete)
}

func TestCollectSourceProof(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		edit         func(*script)
	}{
		{"required count absent", "provider_truncation", func(s *script) {
			d := s.steps[4].value.(platform.LandingChange)
			d.SourceCount = nil
			s.steps[4].value = d
			s.steps = s.steps[:5]
		}},
		{"optional count absent", "", func(s *script) {
			s.policy.RequireCount = false
			d := s.steps[4].value.(platform.LandingChange)
			d.SourceCount = nil
			s.steps[4].value = d
			s.steps[6].value = d
		}},
		{"count beyond cap", "provider_truncation", func(s *script) {
			d := s.steps[4].value.(platform.LandingChange)
			d.SourceCount = new(int64(251))
			s.steps[4].value = d
			s.steps = s.steps[:5]
		}},
		{"count mismatch", "provider_truncation", func(s *script) {
			d := s.steps[4].value.(platform.LandingChange)
			d.SourceCount = new(int64(2))
			s.steps[4].value = d
			s.steps = s.steps[:6]
		}},
		{"head absent", "invalid_observation", func(s *script) {
			d := s.steps[4].value.(platform.LandingChange)
			d.SourceHead = nil
			s.steps[4].value = d
			s.steps = s.steps[:5]
		}},
		{"head outside sources", "provider_truncation", func(s *script) {
			s.steps[5].value = platform.Page[string]{Items: []string{base}, Exhausted: true}
			s.steps = s.steps[:6]
		}},
		{"duplicate source", "provider_truncation", func(s *script) {
			s.steps[5].value = platform.Page[string]{Items: []string{source, source}, Exhausted: true}
			s.steps = s.steps[:6]
		}},
		{"detail changed", "changed_observation", func(s *script) {
			d := s.steps[6].value.(platform.LandingChange)
			d.SourceCount = new(int64(2))
			s.steps[6].value = d
		}},
		{"source continuation failed", "request_failed", func(s *script) {
			s.steps[5].value = platform.Page[string]{Items: []string{source}, NextCursor: "next"}
			s.steps[6] = step{key: "source/next", err: errors.New("unavailable")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			s := mergedScript(t)
			tc.edit(s)
			got, err := collect.Collect(ctx, s, route, query, limits)
			require.NoError(t, err)
			assert := assert.New(t)
			assert.Empty(s.steps)
			assert.Equal(tc.reason, got.Evidence.Inventory.Reason)
			require.Len(t, got.Evidence.Candidates, 1)
			assert.Equal(tc.reason == "", got.Evidence.Candidates[0].SourceComplete)
			assert.Equal(tc.reason == "", got.Evidence.Inventory.Complete)
		})
	}
}

func TestCollectLimitsAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, reason   string
		calls, records int64
		fail           error
	}{
		{"calls", "exhausted_limits", 2, 1000, nil},
		{"whole page records", "exhausted_limits", 100, 5, nil},
		{"transport limit", "exhausted_limits", 100, 1000, platform.ErrLandingTransportLimit},
		{"ambiguous absence", "ambiguous_absence", 100, 1000, platform.ErrLandingAbsenceAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			s := mergedScript(t)
			if tc.fail != nil {
				s.steps[2] = step{key: "association/" + head + "/next", err: tc.fail}
			}
			got, err := collect.Collect(ctx, s, route, query, collect.Limits{Calls: tc.calls, Records: tc.records, OutputBytes: 100000})
			require.NoError(t, err)
			assert := assert.New(t)
			assert.Equal(tc.reason, got.Evidence.Inventory.Reason)
			assert.False(got.Evidence.Inventory.Complete)
			assert.Equal(head, got.Evidence.Inventory.NextCommit)
		})
	}
}

func TestCollectNoResultOnInvalidInput(t *testing.T) {
	for _, tc := range []string{"deadline", "canceled", "identity", "reader identity", "output", "duplicate query", "invalid limit"} {
		t.Run(tc, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			s := mergedScript(t)
			q, l := query, limits
			switch tc {
			case "deadline":
				ctx = t.Context()
			case "canceled":
				cancel()
			case "identity":
				s.steps[0].value = platform.Repository{Ref: route, PlatformID: 99}
			case "reader identity":
				s.steps[4] = step{key: "detail", err: platform.ErrLandingIdentityMismatch}
			case "output":
				l.OutputBytes = 1
			case "duplicate query":
				q.Commits = []string{head, head}
			case "invalid limit":
				l.Calls = 0
			}
			got, err := collect.Collect(ctx, s, route, q, l)
			require.Error(t, err)
			assert.Equal(t, collect.Result{}, got)
		})
	}
}

func TestCollectRetainsPreparationGapsWithoutReads(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	s := &script{t: t}
	q := query
	q.Complete = false
	q.Gaps = []landedwork.Gap{{ObjectID: base, Reason: "objects_unavailable"}}
	got, err := collect.Collect(ctx, s, route, q, limits)
	require.NoError(t, err)
	assert.Equal(t, q, got.Evidence.Query)
	assert.False(t, got.Evidence.Inventory.Complete)
}

func TestCollectOwnsObservations(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	s := mergedScript(t)
	d := s.steps[4].value.(platform.LandingChange)
	got, err := collect.Collect(ctx, s, route, query, limits)
	require.NoError(t, err)
	*d.SourceCount = 42
	got.Observations[0].Source[0] = base
	got.Evidence.Query.Commits[0] = base
	assert := assert.New(t)
	assert.Equal(int64(1), *got.Observations[0].Change.SourceCount)
	assert.Equal(source, got.Evidence.Candidates[0].Source[0])
	assert.Equal(head, query.Commits[0])
}

func TestCollectCursorCycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	s := mergedScript(t)
	s.steps = s.steps[:3]
	s.steps[2].value = platform.Page[platform.LandingChangeRef]{NextCursor: "again", ProgressOnly: true}
	s.steps = append(s.steps, step{key: "association/" + head + "/again", value: platform.Page[platform.LandingChangeRef]{NextCursor: "next", ProgressOnly: true}})
	got, err := collect.Collect(ctx, s, route, query, limits)
	require.NoError(t, err)
	assert.Equal(t, "repeated_cursor", got.Evidence.Inventory.Reason)
	assert.False(t, got.Evidence.Inventory.Complete)
}

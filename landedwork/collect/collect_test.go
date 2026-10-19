package collect_test

import (
	"context"
	"errors"
	"slices"
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
	t           *testing.T
	steps       []step
	policy      platform.LandingSourcePolicy
	unsupported bool
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
	return platform.LandingEvidenceSupport{Inventory: !s.unsupported, OrdinaryMerge: true, Sources: s.policy}
}

func TestCollectQueryRecordsBeforeEarlyReturn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		commits  []string
		gaps     []landedwork.Gap
		complete bool
	}{
		{name: "incomplete commits", commits: []string{head, source}},
		{name: "empty gaps", gaps: []landedwork.Gap{{}, {}}},
		{name: "combined rows", commits: []string{head}, gaps: []landedwork.Gap{{}}},
		{name: "unsupported discovery", commits: []string{head, source}, complete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			q := query
			q.Commits, q.Gaps, q.Complete = tc.commits, tc.gaps, tc.complete
			s := &script{t: t, unsupported: tc.complete}
			l := limits
			l.Records = 1
			got, err := collect.Collect(ctx, s, route, q, l)
			require.ErrorIs(err, landedwork.ErrOutputBudget)
			require.Equal(collect.Result{}, got)
			l.Records = 2
			got, err = collect.Collect(ctx, s, route, q, l)
			require.NoError(err)
			assert.Equal(t, q, got.Evidence.Query)
			assert.False(t, got.Evidence.Inventory.Complete)
		})
	}
}

func TestCollectQueryRecordsChargedOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	s := &script{t: t, steps: []step{
		{key: "repository", value: observedRepository(12)},
		{key: "association/" + head + "/", value: platform.Page[platform.LandingChangeRef]{Exhausted: true}},
		{key: "association/" + source + "/", value: platform.Page[platform.LandingChangeRef]{Exhausted: true}},
	}}
	l := limits
	// Two query commits, one repository, and two empty association pages.
	l.Records = 5
	got, err := collect.Collect(ctx, s, route, query, l)
	require.NoError(t, err)
	assert.True(t, got.Evidence.Inventory.Complete)
	assert.Empty(t, s.steps)
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
	require.Equal(s.t, platform.LandingChangeRef{ID: 7, Number: 3, TargetID: 12}, ref)
	v, err := s.next("detail")
	if err != nil {
		return platform.LandingChange{}, err
	}
	return v.(platform.LandingChange), nil
}

func (s *script) ListLandingSource(_ context.Context, _ platform.RepoRef, ref platform.LandingChangeRef, cursor string) (platform.Page[string], error) {
	require.Equal(s.t, platform.LandingChangeRef{ID: 7, Number: 3, TargetID: 12}, ref)
	v, err := s.next("source/" + cursor)
	if err != nil {
		return platform.Page[string]{}, err
	}
	return v.(platform.Page[string]), nil
}

func (s *script) ListLandingSourceRange(_ context.Context, _ platform.RepoRef, ref platform.LandingChangeRef, base, head, cursor string) (platform.Page[string], error) {
	require.Equal(s.t, platform.LandingChangeRef{ID: 7, Number: 3, TargetID: 12}, ref)
	v, err := s.next("range/" + base + "/" + head + "/" + cursor)
	if err != nil {
		return platform.Page[string]{}, err
	}
	return v.(platform.Page[string]), nil
}

func mergedScript(t *testing.T) *script {
	t.Helper()
	detail := platform.LandingChange{Ref: platform.LandingChangeRef{ID: 7, Number: 3, TargetID: 12}, TargetID: 12, TargetBranch: "main", Merged: new(true), MergeSHA: new(head), SourceHead: new(source), SourceCount: new(int64(1)), Terminal: head, TerminalEvidence: "merged_commit_sha"}
	s := &script{t: t, policy: platform.LandingSourcePolicy{RequireCount: true, MaxCommits: 250}, steps: []step{
		{key: "repository", value: observedRepository(12)},
		{key: "association/" + head + "/", value: platform.Page[platform.LandingChangeRef]{Items: []platform.LandingChangeRef{{ID: 7, Number: 3, TargetID: 12}}, NextCursor: "next"}},
		{key: "association/" + head + "/next", value: platform.Page[platform.LandingChangeRef]{Items: []platform.LandingChangeRef{{ID: 7, Number: 3, TargetID: 12}}, Exhausted: true}},
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
			assert.Empty(got.Evidence.Inventory.NextCommit, "candidate failures are not association sweep positions")
			assert.Empty(got.Evidence.Inventory.NextPage, "source cursors are not association cursors")
		})
	}
}

// A change above its list cap is listed from its reported source base, and
// that range must reproduce the reported count and head to be complete.
func TestCollectSourceRangeBeyondCap(t *testing.T) {
	older := strings.Repeat("d", 40)
	for _, tc := range []struct {
		name, reason, stage string
		edit                func(*script, *platform.LandingChange)
	}{
		{"range complete", "", "", func(*script, *platform.LandingChange) {}},
		{"range continues", "", "", func(s *script, _ *platform.LandingChange) {
			s.steps[5].value = platform.Page[string]{Items: []string{older}, NextCursor: "next"}
			s.steps = slices.Insert(s.steps, 6, step{key: "range/" + base + "/" + source + "/next", value: platform.Page[string]{Items: []string{source}, Exhausted: true}})
		}},
		{"no range support", "provider_truncation", "detail", func(s *script, _ *platform.LandingChange) {
			s.policy.RangeMaxCommits = 0
			s.steps = s.steps[:5]
		}},
		{"count beyond range ceiling", "provider_truncation", "detail", func(s *script, d *platform.LandingChange) {
			d.SourceCount = new(int64(4))
			s.steps = s.steps[:5]
		}},
		{"base absent", "provider_truncation", "detail", func(s *script, d *platform.LandingChange) {
			d.SourceBase = nil
			s.steps = s.steps[:5]
		}},
		{"base malformed", "invalid_observation", "detail", func(s *script, d *platform.LandingChange) {
			d.SourceBase = new("main")
			s.steps = s.steps[:5]
		}},
		{"range shorter than count", "provider_truncation", "source", func(s *script, _ *platform.LandingChange) {
			s.steps[5].value = platform.Page[string]{Items: []string{source}, Exhausted: true}
			s.steps = s.steps[:6]
		}},
		{"range omits head", "provider_truncation", "source", func(s *script, _ *platform.LandingChange) {
			s.steps[5].value = platform.Page[string]{Items: []string{older, base}, Exhausted: true}
			s.steps = s.steps[:6]
		}},
		{"base changed on recheck", "changed_observation", "detail_recheck", func(s *script, _ *platform.LandingChange) {
			d := s.steps[6].value.(platform.LandingChange)
			d.SourceBase = new(older)
			s.steps[6].value = d
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			s := mergedScript(t)
			s.policy = platform.LandingSourcePolicy{RequireCount: true, MaxCommits: 1, RangeMaxCommits: 3}
			d := s.steps[4].value.(platform.LandingChange)
			d.SourceBase, d.SourceCount = new(base), new(int64(2))
			s.steps[5] = step{key: "range/" + base + "/" + source + "/", value: platform.Page[string]{Items: []string{older, source}, Exhausted: true}}
			s.steps[6].value = d
			tc.edit(s, &d)
			s.steps[4].value = d
			got, err := collect.Collect(ctx, s, route, query, limits)
			require.NoError(t, err)
			assert := assert.New(t)
			assert.Empty(s.steps)
			assert.Equal(tc.reason, got.Evidence.Inventory.Reason)
			require.Len(t, got.Observations, 1)
			assert.Equal(tc.reason, got.Observations[0].Reason)
			assert.Equal(tc.stage, got.Observations[0].FailureStage)
			require.Len(t, got.Evidence.Candidates, 1)
			assert.Equal(tc.reason == "", got.Evidence.Candidates[0].SourceComplete)
			if tc.reason == "" {
				assert.Equal([]string{older, source}, got.Evidence.Candidates[0].Source)
				assert.Equal(new(base), got.Observations[0].Change.SourceBase)
			}
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

func TestCollectObservationFailureLocation(t *testing.T) {
	for _, tc := range []struct {
		name, stage, page string
		index             int
	}{
		{"first detail", "detail", "", 4},
		{"source continuation", "source", "next", 6},
		{"detail recheck", "detail_recheck", "", 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			s := mergedScript(t)
			key := "detail"
			if tc.stage == "source" {
				s.steps[5].value = platform.Page[string]{Items: []string{source}, NextCursor: "next"}
				key = "source/next"
			}
			s.steps[tc.index] = step{key: key, err: errors.New("unavailable")}
			s.steps = s.steps[:tc.index+1]
			got, err := collect.Collect(ctx, s, route, query, limits)
			require.NoError(t, err)
			require.Len(t, got.Observations, 1)
			assert := assert.New(t)
			assert.False(got.Evidence.Inventory.Complete)
			assert.Empty(got.Evidence.Inventory.NextCommit)
			assert.Empty(got.Evidence.Inventory.NextPage)
			assert.Equal(int64(7), got.Observations[0].Change.Ref.ID)
			assert.Equal("request_failed", got.Observations[0].Reason)
			assert.Equal(tc.stage, got.Observations[0].FailureStage)
			assert.Equal(tc.page, got.Observations[0].NextPage)
		})
	}
}

func TestCollectTargetIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, stage string
		index       int
		target      int64
	}{
		{"missing detail target", "detail", 4, 0},
		{"missing recheck target", "detail_recheck", 6, 0},
		{"conflicting detail target", "detail", 4, 99},
		{"conflicting recheck target", "detail_recheck", 6, 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			s := mergedScript(t)
			d := s.steps[tc.index].value.(platform.LandingChange)
			d.TargetID = tc.target
			s.steps[tc.index].value = d
			s.steps = s.steps[:tc.index+1]
			got, err := collect.Collect(ctx, s, route, query, limits)
			if tc.target != 0 {
				require.ErrorIs(err, platform.ErrLandingIdentityMismatch)
				assert.Equal(collect.Result{}, got)
				return
			}
			require.NoError(err)
			require.Len(got.Evidence.Candidates, 1)
			require.Len(got.Observations, 1)
			assert.False(got.Evidence.Inventory.Complete)
			assert.Equal("invalid_observation", got.Observations[0].Reason)
			assert.Equal(tc.stage, got.Observations[0].FailureStage)
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
				s.steps[0].value = observedRepository(99)
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

func observedRepository(id int64) platform.Repository {
	ref := route
	ref.Key = platform.RepositoryIDKey(id)
	return platform.Repository{Ref: ref}
}

// inventoryScript also lists every merged change that targets the repository.
type inventoryScript struct{ *script }

func (s inventoryScript) ListMergedLandingChanges(_ context.Context, r platform.RepoRef, cursor string) (platform.Page[platform.LandingChangeRef], error) {
	require.Equal(s.t, route, r)
	v, err := s.next("inventory/" + cursor)
	if err != nil {
		return platform.Page[platform.LandingChangeRef]{}, err
	}
	return v.(platform.Page[platform.LandingChangeRef]), nil
}

// Commit associations can name only changes in another repository of the same
// network. A complete list of the repository's own merged changes establishes
// local absence instead, so the foreign changes need not block collection.
func TestCollectForeignAssociations(t *testing.T) {
	foreign := platform.LandingChangeRef{ID: 8, Number: 3, TargetID: 99}
	local := platform.LandingChangeRef{ID: 7, Number: 3, TargetID: 12}
	detail := platform.LandingChange{Ref: local, TargetID: 12, TargetBranch: "main", Merged: new(true), MergeSHA: new(head), SourceHead: new(source), SourceCount: new(int64(1)), Terminal: head, TerminalEvidence: "merged_commit_sha"}
	for _, mode := range []string{"no inventory", "local change", "no local changes", "failed inventory", "foreign inventory"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			s := &script{t: t, policy: platform.LandingSourcePolicy{RequireCount: true, MaxCommits: 250}, steps: []step{
				{key: "repository", value: observedRepository(12)},
				{key: "association/" + head + "/", value: platform.Page[platform.LandingChangeRef]{Items: []platform.LandingChangeRef{foreign}, Exhausted: true}},
				{key: "association/" + source + "/", value: platform.Page[platform.LandingChangeRef]{Exhausted: true}},
			}}
			var reader platform.LandingEvidenceReader = inventoryScript{s}
			reason := ""
			switch mode {
			case "no inventory":
				reader, reason = s, "ambiguous_absence"
			case "local change":
				s.steps = append(s.steps,
					step{key: "inventory/", value: platform.Page[platform.LandingChangeRef]{Items: []platform.LandingChangeRef{local}, NextCursor: "next"}},
					step{key: "inventory/next", value: platform.Page[platform.LandingChangeRef]{Exhausted: true}},
					step{key: "detail", value: detail},
					step{key: "source/", value: platform.Page[string]{Items: []string{source}, Exhausted: true}},
					step{key: "detail", value: detail})
			case "no local changes":
				s.steps = append(s.steps, step{key: "inventory/", value: platform.Page[platform.LandingChangeRef]{Exhausted: true}})
			case "failed inventory":
				s.steps = append(s.steps, step{key: "inventory/", err: errors.New("unavailable")})
				reason = "request_failed"
			case "foreign inventory":
				s.steps = append(s.steps, step{key: "inventory/", value: platform.Page[platform.LandingChangeRef]{Items: []platform.LandingChangeRef{foreign}, Exhausted: true}})
				reason = "invalid_observation"
			}
			got, err := collect.Collect(ctx, reader, route, query, limits)
			require.NoError(t, err)
			assert := assert.New(t)
			assert.Empty(s.steps)
			assert.Equal(reason, got.Evidence.Inventory.Reason)
			assert.Equal(reason == "", got.Evidence.Inventory.Complete)
			var ids []string
			for _, c := range got.Evidence.Candidates {
				ids = append(ids, c.ID)
			}
			if mode == "local change" {
				require.Equal(t, []string{"7"}, ids)
				assert.True(got.Evidence.Candidates[0].SourceComplete)
			} else {
				assert.Empty(ids, "a foreign change is never a local candidate")
			}
			require.NotEmpty(t, got.Observations)
			last := got.Observations[len(got.Observations)-1]
			assert.Equal(int64(8), last.Change.Ref.ID)
			assert.Equal("foreign_repository", last.Reason)
		})
	}
}

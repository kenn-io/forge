package collect_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/landedwork/collect"
	"go.kenn.io/forge/platform"
)

func TestCollectRoleRecheck(t *testing.T) {
	for _, mode := range []string{"replace", "remove", "proof changed", "recheck failed"} {
		t.Run(mode, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			s := mergedScript(t)
			initial := s.steps[4].value.(platform.LandingChange)
			initial.Author = &platform.Account{ID: new(int64(21)), Login: new("user-a"), Type: platform.AccountTypeUser}
			initial.OpenedAt = new(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			s.steps[4].value = initial
			final := s.steps[6].value.(platform.LandingChange)
			if mode != "remove" {
				final.Author = &platform.Account{Login: new(""), Type: platform.AccountTypeUnknown}
				final.Merger = &platform.Account{ID: new(int64(22)), Login: new("merge-app"), Type: platform.AccountTypeBot}
				final.OpenedAt = new(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
				final.MergedAt = new(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC))
			}
			s.steps[6].value = final
			want, reason := final, ""
			switch mode {
			case "proof changed":
				final.TargetBranch = "other"
				s.steps[6].value = final
				want, reason = initial, "changed_observation"
			case "recheck failed":
				s.steps[6].err = errors.New("provider unavailable")
				want, reason = initial, "request_failed"
			}
			got, err := collect.Collect(ctx, s, route, query, limits)
			require.NoError(err)
			require.Len(got.Observations, 1)
			assert.Equal(want, got.Observations[0].Change)
			assert.Equal(reason, got.Evidence.Inventory.Reason)
			assert.Equal(reason == "", got.Evidence.Inventory.Complete)
			assert.Equal(reason == "", got.Observations[0].SourceComplete)
			assert.Empty(s.steps)
		})
	}
}

func TestCollectRolesOwnedSnapshot(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	s := mergedScript(t)
	d := s.steps[4].value.(platform.LandingChange)
	stamp := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	d.Author = &platform.Account{ID: new(int64(21)), Login: new("user-a"), Type: platform.AccountTypeUser}
	d.Merger = d.Author // Same provider account is still two independently owned roles.
	d.OpenedAt, d.MergedAt = new(stamp), new(stamp)
	s.steps[4].value, s.steps[6].value = d, d
	got, err := collect.Collect(ctx, s, route, query, limits)
	require.NoError(err)
	require.Len(got.Observations, 1)
	*d.Author.ID, *d.Author.Login, d.Author.Type = 99, "changed", platform.AccountTypeBot
	*d.OpenedAt, *d.MergedAt = time.Time{}, time.Time{}
	observation := got.Observations[0].Change
	want := &platform.Account{ID: new(int64(21)), Login: new("user-a"), Type: platform.AccountTypeUser}
	assert.Equal(want, observation.Author)
	assert.Equal(want, observation.Merger)
	assert.Equal(&stamp, observation.OpenedAt)
	assert.Equal(&stamp, observation.MergedAt)
	*observation.Author.Login = "local edit"
	assert.Equal(new("user-a"), observation.Merger.Login)
}

func TestCollectRoleBudgets(t *testing.T) {
	for _, tc := range []struct {
		name           string
		records, bytes int64
		stage          string
		outputError    bool
	}{
		// 17 original records plus two accounts on each detail read.
		// 515 original string bytes plus (6 login + 4 type) for each role.
		{"exact fit", 21, 535, "", false},
		{"initial accounts do not fit", 14, 100000, "detail", false},
		{"recheck accounts do not fit", 20, 100000, "detail_recheck", false},
		{"output one short", 21, 534, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			s := mergedScript(t)
			d := s.steps[4].value.(platform.LandingChange)
			d.Author = &platform.Account{ID: new(int64(21)), Login: new("user-a"), Type: platform.AccountTypeUser}
			d.Merger = d.Author
			s.steps[4].value, s.steps[6].value = d, d
			if tc.stage == "detail_recheck" {
				after := d
				after.Merger = &platform.Account{ID: new(int64(99)), Login: new("not-retained"), Type: platform.AccountTypeBot}
				s.steps[6].value = after
			}
			l := limits
			l.Records, l.OutputBytes = tc.records, tc.bytes
			got, err := collect.Collect(ctx, s, route, query, l)
			if tc.outputError {
				require.ErrorIs(err, landedwork.ErrOutputBudget)
				assert.Equal(collect.Result{}, got)
				return
			}
			require.NoError(err)
			require.Len(got.Observations, 1)
			o := got.Observations[0]
			assert.Equal(tc.stage, o.FailureStage)
			assert.Equal(tc.stage == "", got.Evidence.Inventory.Complete)
			if tc.stage != "" {
				assert.Equal("exhausted_limits", o.Reason)
			}
			if tc.stage == "detail" {
				assert.Nil(o.Change.Author)
				assert.Nil(o.Change.Merger)
				want := d
				want.Author, want.Merger = nil, nil
				assert.Equal(want, o.Change, "retain the already charged detail without uncharged accounts")
				require.Len(got.Evidence.Candidates, 1)
				assert.Equal(head, got.Evidence.Candidates[0].Terminal)
				assert.Equal("merged_commit_sha", got.Evidence.Candidates[0].TerminalEvidence)
				assert.Len(s.steps, 2, "must stop before source paging")
			} else {
				assert.Equal(d.Author, o.Change.Author)
				assert.Equal(d.Merger, o.Change.Merger)
				assert.Empty(s.steps)
			}
		})
	}
}

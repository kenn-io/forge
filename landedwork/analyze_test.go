package landedwork_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
)

func TestAnalyzeMergeCountsNetWorkOnceThenDirectPush(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	gitRun(t, dir, "switch", "-c", "feature")
	first := commitFiles(t, dir, "first edit", map[string]string{"main.go": "one\ntemporary\n"})
	second := commitFiles(t, dir, "second edit", map[string]string{"main.go": "one\nfinal\n"})
	gitRun(t, dir, "switch", "main")
	gitRun(t, dir, "merge", "--no-ff", "feature", "-m", "land feature")
	merged := gitRun(t, dir, "rev-parse", "HEAD")
	head := commitFiles(t, dir, "direct", map[string]string{"other.go": "direct\n"})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	budget := platform.Budget{MaxRecords: 1000, MaxNodes: 1000, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20}
	meter, err := platform.NewMeter(ctx, budget)
	require.NoError(err)
	source, err := landedwork.OpenGit(ctx, dir, meter)
	require.NoError(err)
	defer source.Close()
	snapshot := platform.LandingSnapshot{
		Schema:       platform.LandingSnapshotSchema,
		Repository:   platform.LandingRepository{Identity: platform.RepositoryIdentity{Provider: platform.KindGitHub, Instance: "github.com", ID: "17"}, Owner: "team-a", Name: "project-a", DefaultBranch: "main", HeadSHA: head},
		Capabilities: platform.LandingCapabilities{OrdinaryMerge: true, CompleteCandidateInventory: true},
		Coverage:     platform.LandingCoverage{Complete: true},
		Candidates:   []platform.LandingCandidate{{ID: "31", Number: 2, TerminalProof: "fixture:provider-bound-terminal", TerminalSHA: merged, SourceHeadSHA: platform.SHAField{Present: true, Value: second}, SourceCommits: []string{first, second}, SourceComplete: true, Author: &platform.Account{ID: "61", Type: platform.AccountUser}, Merger: &platform.Account{ID: "62", Type: platform.AccountBot}}},
	}
	request := landedwork.Request{Snapshot: snapshot, DefaultBranch: "main", BaseSHA: base, HeadSHA: head, Policy: landedwork.CodePolicy}
	result, err := landedwork.Analyze(ctx, request, source, meter)
	require.NoError(err)
	require.Len(result.Landings, 2)
	assert.Equal("change", result.Landings[0].Origin)
	assert.Equal("31", result.Landings[0].ChangeID)
	assert.Equal(platform.LandingMerge, result.Landings[0].Method)
	require.Len(result.Landings[0].Claims, 2)
	assert.Equal(landedwork.AssuranceVerified, result.Landings[0].Claims[0].Assurance)
	assert.Equal("61", result.Landings[0].Claims[0].ProviderUserID)
	assert.Equal(landedwork.RoleMerger, result.Landings[0].Claims[1].Role)
	assert.Equal(new(landedwork.LineCounts{Additions: 1, Deletions: 0}), result.Landings[0].Churn.Raw)
	require.NotNil(result.Landings[0].Churn.Code)
	assert.Equal(landedwork.LineCounts{Additions: 1, Deletions: 0}, *result.Landings[0].Churn.Code)
	require.Len(result.Landings[0].Introduced, 2)
	assert.Equal("direct_push", result.Landings[1].Origin)
	assert.Equal(head, result.CertifiedHead)
	assert.True(result.Complete)
	assert.Empty(result.Gaps)
	assert.Len(result.Digest, 64)
}

func TestIncrementalMergeDoesNotChargeHistoryBeforeBase(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := newHistory(t)
	var stream strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&stream, "commit refs/heads/main\ncommitter Author A <author@example.org> %d +0000\ndata 4\nbase\nM 100644 inline main.go\ndata 4\none\n\n", 1700000000+i)
	}
	_, stderr, err := gitsafe.Runner().Run(t.Context(), dir, strings.NewReader(stream.String()), "-c", "gc.auto=0", "-c", "maintenance.auto=false", "fast-import", "--quiet")
	require.NoError(err, "%s", stderr)
	gitRun(t, dir, "read-tree", "HEAD")
	gitRun(t, dir, "checkout-index", "--all")
	base := gitRun(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "switch", "-c", "feature")
	commitFiles(t, dir, "side", map[string]string{"side.go": "two\n"})
	gitRun(t, dir, "switch", "main")
	gitRun(t, dir, "merge", "--no-ff", "feature", "-m", "direct merge")
	head := gitRun(t, dir, "rev-parse", "HEAD")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	budget := platform.Budget{MaxRecords: 100, MaxNodes: 8, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20}
	meter, err := platform.NewMeter(ctx, budget)
	require.NoError(err)
	source, err := landedwork.OpenGit(ctx, dir, meter)
	require.NoError(err)
	defer source.Close()
	result, err := landedwork.Analyze(ctx, landedwork.Request{Snapshot: platform.LandingSnapshot{Schema: platform.LandingSnapshotSchema, Repository: platform.LandingRepository{Identity: platform.RepositoryIdentity{Provider: platform.KindGitHub, Instance: "github.com", ID: "17"}, DefaultBranch: "main", HeadSHA: head}, Coverage: platform.LandingCoverage{Complete: true}, Capabilities: platform.LandingCapabilities{CompleteCandidateInventory: true}}, DefaultBranch: "main", BaseSHA: base, HeadSHA: head, Policy: landedwork.CodePolicy}, source, meter)
	require.NoError(err)
	require.True(result.Complete, "%+v", result.Gaps)
	require.Len(result.Landings, 1)
	assert.Len(result.Landings[0].Introduced, 1)
	assert.Equal(new(landedwork.LineCounts{Additions: 1}), result.Landings[0].Churn.Raw)
}

func TestMalformedPinnedGeneratedAttributeLeavesCodeChurnUnknown(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n", ".gitattributes": "*.go linguist-generated=invalid-value\n"})
	head := commitFiles(t, dir, "change", map[string]string{"main.go": "one\ntwo\n"})
	result := runAnalysis(t, dir, base, head, nil, true)
	require.Len(result.Landings, 1)
	assert.Equal(new(landedwork.LineCounts{Additions: 1}), result.Landings[0].Churn.Raw)
	assert.Nil(result.Landings[0].Churn.Code)
	require.Len(result.Gaps, 1)
	assert.Equal("generated_attributes_unavailable", result.Gaps[0].Reason)
	assert.Equal(base, result.CertifiedHead)
}

func runAnalysis(t *testing.T, dir, base, head string, candidates []platform.LandingCandidate, complete bool) landedwork.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	budget := platform.Budget{MaxRecords: 1000, MaxNodes: 1000, MaxBytes: 1 << 20, MaxOutputBytes: 1 << 20}
	meter, err := platform.NewMeter(ctx, budget)
	require.NoError(t, err)
	source, err := landedwork.OpenGit(ctx, dir, meter)
	require.NoError(t, err)
	defer source.Close()
	result, err := landedwork.Analyze(ctx, landedwork.Request{
		Snapshot: platform.LandingSnapshot{Schema: platform.LandingSnapshotSchema,
			Repository:   platform.LandingRepository{Identity: platform.RepositoryIdentity{Provider: platform.KindGitHub, Instance: "github.com", ID: "17"}, DefaultBranch: "main", HeadSHA: head},
			Capabilities: platform.LandingCapabilities{OrdinaryMerge: true, Squash: true, RebaseRange: true, FastForwardRange: true, CompleteCandidateInventory: true},
			Coverage:     platform.LandingCoverage{Complete: complete}, Candidates: candidates},
		DefaultBranch: "main", BaseSHA: base, HeadSHA: head, Policy: landedwork.CodePolicy,
	}, source, meter)
	require.NoError(t, err)
	return result
}

func TestIncompleteInventoryCannotBecomeDirectPushes(t *testing.T) {
	assert := assert.New(t)
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	head := commitFiles(t, dir, "next", map[string]string{"main.go": "one\ntwo\n"})
	result := runAnalysis(t, dir, base, head, nil, false)
	assert.Empty(result.Landings)
	assert.Equal(base, result.CertifiedHead)
	assert.False(result.Complete)
	require.Len(t, result.Gaps, 1)
	assert.Equal("candidate_inventory_incomplete", result.Gaps[0].Reason)
}

func TestUnboundedCandidateBlocksDirectPushClassification(t *testing.T) {
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	head := commitFiles(t, dir, "next", map[string]string{"main.go": "one\ntwo\n"})
	result := runAnalysis(t, dir, base, head, []platform.LandingCandidate{{ID: "31"}}, true)
	assert.Empty(t, result.Landings)
	assert.Equal(t, base, result.CertifiedHead)
	assert.False(t, result.Complete)
}

func TestBoundedGapKeepsLaterFactsButStopsCertifiedHead(t *testing.T) {
	assert := assert.New(t)
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	gap := commitFiles(t, dir, "unknown origin", map[string]string{"main.go": "one\ntwo\n"})
	head := commitFiles(t, dir, "later direct", map[string]string{"other.go": "three\n"})
	result := runAnalysis(t, dir, base, head, []platform.LandingCandidate{{ID: "31", PossibleSpan: &platform.LandingSpan{FirstSHA: gap, LastSHA: gap}}}, true)
	require.Len(t, result.Landings, 1)
	assert.Equal(head, result.Landings[0].Terminal)
	assert.Equal(base, result.CertifiedHead)
	assert.False(result.Complete)
}

func TestMultipleOriginsNeverAssignTheSameLanding(t *testing.T) {
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	gitRun(t, dir, "switch", "-c", "feature")
	source := commitFiles(t, dir, "change", map[string]string{"main.go": "one\ntwo\n"})
	gitRun(t, dir, "switch", "main")
	gitRun(t, dir, "merge", "--no-ff", "feature", "-m", "land")
	head := gitRun(t, dir, "rev-parse", "HEAD")
	var candidates []platform.LandingCandidate
	for _, id := range []string{"31", "32", "33"} {
		candidates = append(candidates, platform.LandingCandidate{ID: id, Method: platform.LandingMerge, MethodProof: "fixture:merge", TerminalSHA: head, SourceHeadSHA: platform.SHAField{Present: true, Value: source}, SourceComplete: true, SourceCommits: []string{source}})
	}
	result := runAnalysis(t, dir, base, head, candidates, true)
	assert.Empty(t, result.Landings)
	assert.Equal(t, base, result.CertifiedHead)
	assert.False(t, result.Complete)
}

func TestSquashKeepsSourceEvidenceAndOneNetDiff(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	gitRun(t, dir, "switch", "-c", "feature")
	first := commitFiles(t, dir, "first", map[string]string{"main.go": "one\ntemporary\n"})
	second := commitFiles(t, dir, "second", map[string]string{"main.go": "one\nfinal\n"})
	gitRun(t, dir, "switch", "main")
	gitRun(t, dir, "merge", "--squash", "feature")
	head := commitFiles(t, dir, "squashed\n\nCo-authored-by: Peer <21+peer@users.noreply.github.com>", nil)
	result := runAnalysis(t, dir, base, head, []platform.LandingCandidate{{ID: "31", Method: platform.LandingSquash, MethodProof: "fixture:squash", TerminalSHA: head, SourceCommits: []string{first, second}, SourceComplete: true}}, true)
	require.Len(result.Landings, 1)
	landing := result.Landings[0]
	assert.Equal(new(landedwork.LineCounts{Additions: 1}), landing.Churn.Raw)
	require.Len(landing.Introduced, 1)
	assert.Equal(head, landing.Introduced[0].ID)
	require.Len(landing.Sources, 2)
	assert.Equal(first, landing.Sources[0].ID)
	assert.Equal(second, landing.Sources[1].ID)
	assert.True(result.Complete)
}

func TestFastForwardRangeIsOneLandingNotTwoDirectPushes(t *testing.T) {
	assert := assert.New(t)
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	first := commitFiles(t, dir, "first", map[string]string{"main.go": "one\ntemporary\n"})
	second := commitFiles(t, dir, "second", map[string]string{"main.go": "one\nfinal\n"})
	result := runAnalysis(t, dir, base, second, []platform.LandingCandidate{{ID: "31", Method: platform.LandingFastForward, MethodProof: "fixture:fast-forward", TerminalSHA: second, SourceCommits: []string{first, second}, SourceComplete: true}}, true)
	require.Len(t, result.Landings, 1)
	assert.Equal([]string{first, second}, result.Landings[0].Spine)
	assert.Equal(new(landedwork.LineCounts{Additions: 1}), result.Landings[0].Churn.Raw)
	assert.True(result.Complete)
}

func TestRebaseRangeRequiresExactOrderedEdits(t *testing.T) {
	for _, altered := range []bool{false, true} {
		t.Run(map[bool]string{false: "same edits", true: "conflict resolution differs"}[altered], func(t *testing.T) {
			assert := assert.New(t)
			dir := newHistory(t)
			base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\ntwo\n"})
			gitRun(t, dir, "switch", "-c", "feature")
			first := commitFiles(t, dir, "first", map[string]string{"main.go": "one\ninsert\ntwo\n"})
			second := commitFiles(t, dir, "second", map[string]string{"other.go": "other\n"})
			gitRun(t, dir, "switch", "main")
			newBase := commitFiles(t, dir, "prefix", map[string]string{"main.go": "header\none\ntwo\n"})
			content := "header\none\ninsert\ntwo\n"
			if altered {
				content = "header\none\nINSERT\ntwo\n"
			}
			replayedFirst := commitFiles(t, dir, "rebased first", map[string]string{"main.go": content})
			head := commitFiles(t, dir, "rebased second", map[string]string{"other.go": "other\n"})
			result := runAnalysis(t, dir, newBase, head, []platform.LandingCandidate{{ID: "31", Method: platform.LandingRebase, MethodProof: "fixture:rebase-terminal", TerminalSHA: head, SourceCommits: []string{first, second}, SourceComplete: true, PossibleSpan: &platform.LandingSpan{FirstSHA: replayedFirst, LastSHA: head}}}, true)
			if altered {
				assert.False(result.Complete)
				assert.Empty(result.Landings)
				return
			}
			require.Len(t, result.Landings, 1)
			assert.Equal([]string{replayedFirst, head}, result.Landings[0].Spine)
			assert.Equal(new(landedwork.LineCounts{Additions: 2}), result.Landings[0].Churn.Raw)
			assert.True(result.Complete)
			_ = base
		})
	}
}

func TestOriginsBeforeTheBaseDoNotBlockNewDirectWork(t *testing.T) {
	dir := newHistory(t)
	old := commitFiles(t, dir, "older landing", map[string]string{"main.go": "one\n"})
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\ntwo\n"})
	head := commitFiles(t, dir, "new direct", map[string]string{"other.go": "three\n"})
	for _, terminal := range []string{old, base} {
		result := runAnalysis(t, dir, base, head, []platform.LandingCandidate{{ID: "31", Method: platform.LandingSquash, MethodProof: "fixture:squash", TerminalSHA: terminal}}, true)
		require.Len(t, result.Landings, 1)
		assert.Equal(t, "direct_push", result.Landings[0].Origin)
		assert.True(t, result.Complete)
	}
}

func TestAnalysisCannotPublishPastItsOutputBudget(t *testing.T) {
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	head := commitFiles(t, dir, "next", map[string]string{"main.go": "one\ntwo\n"})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	budget := platform.Budget{MaxRecords: 100, MaxNodes: 100, MaxBytes: 1 << 20, MaxOutputBytes: 8}
	meter, err := platform.NewMeter(ctx, budget)
	require.NoError(t, err)
	source, err := landedwork.OpenGit(ctx, dir, meter)
	require.NoError(t, err)
	defer source.Close()
	_, err = landedwork.Analyze(ctx, landedwork.Request{BaseSHA: base, HeadSHA: head, DefaultBranch: "main", Policy: landedwork.CodePolicy,
		Snapshot: platform.LandingSnapshot{Schema: platform.LandingSnapshotSchema, Repository: platform.LandingRepository{Identity: platform.RepositoryIdentity{Provider: platform.KindGitHub, Instance: "github.com", ID: "17"}, DefaultBranch: "main", HeadSHA: head}, Capabilities: platform.LandingCapabilities{CompleteCandidateInventory: true}, Coverage: platform.LandingCoverage{Complete: true}},
	}, source, meter)
	require.ErrorIs(t, err, platform.ErrPageLimit)
}

func TestGitInputBudgetExhaustionReturnsAnExplicitGap(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	head := commitFiles(t, dir, strings.Repeat("x", 4096), map[string]string{"main.go": "one\ntwo\n"})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	budget := platform.Budget{MaxRecords: 100, MaxNodes: 100, MaxBytes: 1500, MaxOutputBytes: 8192}
	meter, err := platform.NewMeter(ctx, budget)
	require.NoError(err)
	source, err := landedwork.OpenGit(ctx, dir, meter)
	require.NoError(err)
	defer source.Close()
	result, err := landedwork.Analyze(ctx, landedwork.Request{BaseSHA: base, HeadSHA: head, DefaultBranch: "main", Policy: landedwork.CodePolicy,
		Snapshot: platform.LandingSnapshot{Schema: platform.LandingSnapshotSchema, Repository: platform.LandingRepository{Identity: platform.RepositoryIdentity{Provider: platform.KindGitHub, Instance: "github.com", ID: "17"}, DefaultBranch: "main", HeadSHA: head}, Capabilities: platform.LandingCapabilities{CompleteCandidateInventory: true}, Coverage: platform.LandingCoverage{Complete: true}},
	}, source, meter)
	require.NoError(err)
	require.Len(result.Gaps, 1)
	assert.Equal("input_budget_exhausted", result.Gaps[0].Reason)
	assert.False(result.Complete)
}

func TestDirectMergeExcludesAlreadyReachableSideAncestry(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	dir := newHistory(t)
	commitFiles(t, dir, "root", map[string]string{"main.go": "one\n"})
	gitRun(t, dir, "switch", "-c", "feature")
	commitFiles(t, dir, "first", map[string]string{"side.go": "first\n"})
	gitRun(t, dir, "switch", "main")
	gitRun(t, dir, "merge", "--no-ff", "feature", "-m", "first landing")
	base := gitRun(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "switch", "feature")
	second := commitFiles(t, dir, "second", map[string]string{"side.go": "first\nsecond\n"})
	gitRun(t, dir, "switch", "main")
	gitRun(t, dir, "merge", "--no-ff", "feature", "-m", "second landing")
	head := gitRun(t, dir, "rev-parse", "HEAD")
	result := runAnalysis(t, dir, base, head, nil, true)
	require.True(result.Complete)
	require.Len(result.Landings, 1)
	require.Len(result.Landings[0].Introduced, 1)
	assert.Equal(second, result.Landings[0].Introduced[0].ID)
	assert.Equal(new(landedwork.LineCounts{Additions: 1}), result.Landings[0].Churn.Raw)
}

func TestRangeContainingMergeCannotBecomeDirectPushes(t *testing.T) {
	assert := assert.New(t)
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	gitRun(t, dir, "switch", "-c", "feature")
	first := commitFiles(t, dir, "side", map[string]string{"side.go": "two\n"})
	gitRun(t, dir, "switch", "main")
	gitRun(t, dir, "merge", "--no-ff", "feature", "-m", "merge")
	merged := gitRun(t, dir, "rev-parse", "HEAD")
	head := commitFiles(t, dir, "next", map[string]string{"next.go": "three\n"})
	result := runAnalysis(t, dir, base, head, []platform.LandingCandidate{{ID: "31", Method: platform.LandingRebase, MethodProof: "fixture:range", TerminalSHA: head, SourceComplete: true, SourceCommits: []string{first, head}, PossibleSpan: &platform.LandingSpan{FirstSHA: merged, LastSHA: head}}}, true)
	assert.Empty(result.Landings)
	assert.Equal(base, result.CertifiedHead)
	require.Len(t, result.Gaps, 1)
	assert.Equal("range_topology_unproven", result.Gaps[0].Reason)
}

func TestMissingSourceAndPartialSourceNeverBecomeDirectPushes(t *testing.T) {
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	head := commitFiles(t, dir, "landing", map[string]string{"main.go": "one\ntwo\n"})
	for _, complete := range []bool{false, true} {
		result := runAnalysis(t, dir, base, head, []platform.LandingCandidate{{ID: "31", Method: platform.LandingSquash, MethodProof: "fixture:squash", TerminalSHA: head, SourceComplete: complete, SourceCommits: []string{strings.Repeat("1", 40)}}}, true)
		assert.Empty(t, result.Landings)
		assert.Equal(t, base, result.CertifiedHead)
		assert.False(t, result.Complete)
	}
}

func TestDivergenceDoesNotCertifyReplacementHistory(t *testing.T) {
	assert := assert.New(t)
	dir := newHistory(t)
	root := commitFiles(t, dir, "root", map[string]string{"main.go": "one\n"})
	base := commitFiles(t, dir, "old history", map[string]string{"old.go": "two\n"})
	gitRun(t, dir, "switch", "-c", "replacement", root)
	head := commitFiles(t, dir, "replacement", map[string]string{"new.go": "three\n"})
	result := runAnalysis(t, dir, base, head, nil, true)
	assert.True(result.Diverged)
	assert.Equal(base, result.CertifiedHead)
	assert.Empty(result.Landings)
	assert.False(result.Complete)
}

func TestDeclaredRevertsResolveOnlyExistingObjects(t *testing.T) {
	dir := newHistory(t)
	base := commitFiles(t, dir, "base", map[string]string{"main.go": "one\n"})
	head := commitFiles(t, dir, "revert intent\n\nThis reverts commit "+base+".\nThis reverts commit "+strings.Repeat("1", 40)+".", map[string]string{"main.go": "two\n"})
	result := runAnalysis(t, dir, base, head, nil, true)
	require.Len(t, result.Landings, 1)
	assert.Equal(t, []string{base}, result.Landings[0].TerminalCommit.DeclaredReverts)
	assert.True(t, result.Complete)
}

package report_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/archive/report"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/platform"
)

func TestReportRejectsOutputBeyondEnvelope(t *testing.T) {
	model := report.Model{LandedWork: &report.LandedSection{Evidence: landedwork.Result{Repository: platform.RepositoryIdentity{ID: strings.Repeat("x", report.MaxDetailedTextBytes)}}}}
	text, err := report.RenderMarkdown(model)
	require.ErrorIs(t, err, platform.ErrPageLimit)
	assert.Empty(t, text)
}

func TestLandedReportSeparatesGraphProviderAndGitTime(t *testing.T) {
	assert := assert.New(t)
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	result := landedwork.Result{Schema: landedwork.Schema, Complete: true, Landings: []landedwork.Landing{
		{Origin: "change", ChangeID: "31", MergedAt: new(start), Introduced: []landedwork.CommitEvidence{{ID: "one"}}, TerminalCommit: landedwork.CommitEvidence{CommitterTime: start}, Churn: landedwork.Churn{Raw: new(landedwork.LineCounts{Additions: 3, Deletions: 1}), Code: new(landedwork.LineCounts{Additions: 3})}},
		{Origin: "direct_push", Introduced: []landedwork.CommitEvidence{{ID: "two"}}, TerminalCommit: landedwork.CommitEvidence{CommitterTime: start}, Churn: landedwork.Churn{Raw: new(landedwork.LineCounts{Additions: 2}), Code: new(landedwork.LineCounts{Additions: 1})}},
		{Origin: "change", ChangeID: "32", MergedAt: new(end), TerminalCommit: landedwork.CommitEvidence{CommitterTime: end}, Churn: landedwork.Churn{Raw: new(landedwork.LineCounts{}), Code: new(landedwork.LineCounts{})}},
	}}
	section := report.BuildLandedSection(platform.LandingSnapshot{}, result, landedwork.Correspondence{Complete: true}, start, end)
	assert.Equal(2, section.Graph.ChangeLandings)
	assert.Equal(1, section.Graph.DirectPushLandings)
	assert.Equal(2, section.Graph.IntroducedCommits)
	require.NotNil(t, section.Graph.DirectPushLandingShare)
	assert.InDelta(1.0/3, *section.Graph.DirectPushLandingShare, 1e-10)
	assert.Equal(1, section.ProviderTime.Measures.ChangeLandings)
	assert.Zero(section.ProviderTime.Measures.DirectPushLandings)
	assert.Nil(section.ProviderTime.Measures.DirectPushLandingShare)
	assert.Equal(1, section.ProviderTime.UnknownTimeLandings)
	assert.Equal(1, section.GitTime.Measures.DirectPushLandings)
	assert.Equal("unverified", section.GitTime.Assurance)
	result.Complete = false
	result.Landings[0].Churn.Code = nil
	partial := report.BuildLandedSection(platform.LandingSnapshot{}, result, landedwork.Correspondence{Complete: true}, start, end)
	assert.Nil(partial.Graph.DirectPushLandingShare)
	assert.Nil(partial.Graph.CodeChurn)
	assert.Equal(new(landedwork.LineCounts{Additions: 5, Deletions: 1}), partial.Graph.RawChurn)
	markdown, err := report.RenderMarkdown(report.Model{Start: start, End: end, LandedWork: &partial})
	require.NoError(t, err)
	assert.Contains(markdown, "Graph interval (partial)")
	assert.Contains(markdown, "Direct-push landing share: unknown")
	assert.Contains(markdown, "Git-time window (unverified)")
}

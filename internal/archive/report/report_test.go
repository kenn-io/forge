package report

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yuin/goldmark"
)

func TestRenderMarkdownGoldenAndDeterministicSerialization(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)

	for _, tc := range []struct {
		name     string
		detailed bool
		golden   string
	}{
		{name: "summary", golden: "summary.md.golden"},
		{name: "detailed", detailed: true, golden: "detailed.md.golden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := reportFixture(tc.detailed)
			firstMarkdown, err := RenderMarkdown(model)
			require.NoError(err)
			secondMarkdown, err := RenderMarkdown(model)
			require.NoError(err)
			assert.Equal(firstMarkdown, secondMarkdown)
			assert.NotContains(firstMarkdown, "Generated")
			assert.NotContains(firstMarkdown, "\\")
			assert.NotContains(firstMarkdown, "\r")

			want, err := os.ReadFile(filepath.Join("testdata", tc.golden))
			require.NoError(err)
			assert.Equal(string(want), firstMarkdown)

			firstJSON, err := json.MarshalIndent(model, "", "  ")
			require.NoError(err)
			secondJSON, err := json.MarshalIndent(model, "", "  ")
			require.NoError(err)
			assert.JSONEq(string(firstJSON), string(secondJSON))
		})
	}
}

func TestCountsTotalActivity(t *testing.T) {
	t.Parallel()
	counts := Counts{
		IssuesOpened: 2, MergeRequestsOpened: 3, OrdinaryComments: 5,
		ReviewsSubmitted: 7, InlineReviewComments: 11,
	}
	assert.Equal(t, 28, counts.TotalActivity())
}

func TestRenderMarkdownStripsTerminalControlSequences(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	model := reportFixture(true)
	model.Activity[0].Title = "safe\x1b]52;c;clipboard\a title"
	model.Activity[0].Body = "first\n\tsecond\x1b[2J\u0085third\x7f"

	markdown, err := RenderMarkdown(model)
	require.NoError(err)
	assert.NotContains(markdown, "\x1b")
	assert.NotContains(markdown, "\a")
	assert.NotContains(markdown, "\u0085")
	assert.NotContains(markdown, "\x7f")
	assert.Contains(markdown, `safe\]52;c;clipboard title`)
	assert.Contains(markdown, "first\n\tsecond[2Jthird")
}

func TestRenderMarkdownEscapesProviderControlledMarkup(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	model := reportFixture(true)
	model.Activity[0].Title = `<h1>forged title</h1>`
	model.Activity[0].Author = `<img src=x onerror=alert(1)>`
	model.Activity[0].URL = `<javascript:alert(1)>`
	model.Activity[0].Body = "visible\n<!-- hide following sections -->\n## Forged section\n1. forged list\n~~~\n<script src=https://evil.invalid/x.js></script>\n[click](javascript:alert(1))\n```"

	markdown, err := RenderMarkdown(model)
	require.NoError(err)
	var rendered bytes.Buffer
	require.NoError(goldmark.Convert([]byte(markdown), &rendered))
	html := rendered.String()
	assert.NotContains(html, "<h2>Forged section</h2>")
	assert.NotContains(html, "<ol>")
	assert.NotContains(html, "<script")
	assert.NotContains(html, `href="javascript:`)
	assert.NotContains(markdown, "<javascript:")
	assert.Contains(html, "&lt;!-- hide following sections --&gt;")
	assert.Contains(html, "## Forged section")
	assert.Contains(html, "1. forged list")
	assert.Contains(html, "&lt;script")
}

func TestLimitErrorIncludesBothObservedLimits(t *testing.T) {
	t.Parallel()
	err := &LimitError{
		ObservedRecords: 10_001, MaxRecords: 10_000,
		ObservedTextBytes: 33_554_433, MaxTextBytes: 33_554_432,
	}
	assert.Equal(t,
		"archive report exceeds detailed limits: 10001 records (max 10000), 33554433 text bytes (max 33554432)",
		err.Error(),
	)
}

func TestValidateDetailedSizeBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		records   int
		textBytes int64
		wantError bool
	}{
		{name: "9999 records", records: 9_999, textBytes: MaxDetailedTextBytes},
		{name: "10000 records", records: 10_000, textBytes: MaxDetailedTextBytes},
		{name: "10001 records", records: 10_001, wantError: true},
		{name: "below text", records: 1, textBytes: MaxDetailedTextBytes - 1},
		{name: "equal text", records: 1, textBytes: MaxDetailedTextBytes},
		{name: "above text", records: 1, textBytes: MaxDetailedTextBytes + 1, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			err := ValidateDetailedSize(tc.records, tc.textBytes)
			if tc.wantError {
				var limit *LimitError
				require.ErrorAs(err, &limit)
				assert.Equal(tc.records, limit.ObservedRecords)
				assert.Equal(tc.textBytes, limit.ObservedTextBytes)
				return
			}
			require.NoError(err)
		})
	}
}

func reportFixture(detailed bool) Model {
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	ref := RepositoryRef{
		Provider: "github", PlatformHost: "github.example", Owner: "acme",
		Name: "widget", RepoPath: "acme/widget",
	}
	model := Model{
		Schema: Schema, Start: start, End: end,
		Repositories: []Repository{{
			Repository: ref,
			Coverage: Coverage{
				Status: "current", Issues: "supported", MergeRequests: "supported",
				Comments: "supported", Reviews: "supported",
				InlineComments: "supported",
			},
			Counts: Counts{IssuesOpened: 1, OrdinaryComments: 1},
		}},
		Totals: Counts{IssuesOpened: 1, OrdinaryComments: 1},
		Contributors: []Contributor{
			{Provider: "github", PlatformHost: "github.example", Login: "alice", Counts: Counts{IssuesOpened: 1}},
			{Provider: "github", PlatformHost: "github.example", Login: "", Counts: Counts{OrdinaryComments: 1}},
		},
	}
	if detailed {
		model.Activity = []Activity{
			{
				Repository: ref, Kind: ActivityIssue, ItemNumber: 7,
				ProviderExternalID: "issue-7", Title: "Synthetic issue", Author: "alice",
				OccurredAt: start.Add(time.Hour), Body: "Issue body", URL: "https://github.example/acme/widget/issues/7",
			},
			{
				Repository: ref, Kind: ActivityOrdinaryComment, ItemNumber: 7,
				ProviderExternalID: "comment-9", Title: "Synthetic issue", Author: "",
				OccurredAt: start.Add(2 * time.Hour), Body: "Comment body", URL: "https://github.example/acme/widget/issues/7#comment-9",
			},
		}
	}
	return model
}

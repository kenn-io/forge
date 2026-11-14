package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Comment bodies in the shape roborev's CI integration posts on a pull request.
const (
	roborevFindingsBody = "<!-- roborev-pr-comment -->\n## roborev: Combined Review (`%s`)\n\n" +
		"**Verdict:** Changes require fixes for 1 finding.\n\n### Medium\n\n- `main.go:3`: Problem.\n\n---\n*Reviewers: codex*\n"
	roborevCleanBody = "<!-- roborev-pr-comment -->\n## roborev: Combined Review (`%s`)\n\n" +
		"**Verdict:** No findings at or above medium severity.\n\n---\n*Reviewers: codex*\n"
	roborevNoIssuesBody = "<!-- roborev-pr-comment -->\n## roborev: Combined Review (`%s`)\n\n" +
		"**Verdict:** No issues found.\n"
	roborevFailedBody = "<!-- roborev-pr-comment -->\n## roborev: Review Failed (`%s`)\n\n" +
		"All review jobs in this batch failed.\n\n- Review 1: failed\n"
)

func TestRoborevHeadVerdictFiltersAndRows(t *testing.T) {
	t.Parallel()
	const head = "abc1234def5678abc1234def5678abc1234def56"
	type comment struct {
		eventType, body string
		// author defaults to the roborev-ci App's REST login.
		author, metadata string
	}
	onHead := func(body string) string { return fmt.Sprintf(body, "abc1234") }
	for _, tc := range []struct {
		name     string
		comments []comment // oldest first
		want     string
	}{
		{name: "findings on head", comments: []comment{{eventType: "issue_comment", body: onHead(roborevFindingsBody)}}, want: "findings"},
		{name: "clean on head", comments: []comment{{eventType: "issue_comment", body: onHead(roborevCleanBody)}}, want: "clean"},
		{name: "no issues on head", comments: []comment{{eventType: "issue_comment", body: onHead(roborevNoIssuesBody)}}, want: "clean"},
		{name: "older head had findings, head is clean", comments: []comment{
			{eventType: "issue_comment", body: fmt.Sprintf(roborevFindingsBody, "0000000")},
			{eventType: "issue_comment", body: onHead(roborevCleanBody)},
		}, want: "clean"},
		{name: "head review pending after older findings", comments: []comment{
			{eventType: "issue_comment", body: fmt.Sprintf(roborevFindingsBody, "0000000")},
		}},
		{name: "review of a commit not stored as head", comments: []comment{
			{eventType: "issue_comment", body: onHead(roborevCleanBody)},
			{eventType: "issue_comment", body: fmt.Sprintf(roborevFindingsBody, "fff9999")},
		}, want: "clean"},
		{name: "newest head review failed", comments: []comment{
			{eventType: "issue_comment", body: onHead(roborevFindingsBody)},
			{eventType: "issue_comment", body: onHead(roborevFailedBody)},
		}},
		{name: "quoted roborev comment", comments: []comment{{eventType: "issue_comment", body: "> " + onHead(roborevFindingsBody)}}},
		{name: "no comments"},
		{name: "provider comment event", comments: []comment{{eventType: "comment", body: onHead(roborevFindingsBody)}}, want: "findings"},
		{name: "newer genuine review on the same head", comments: []comment{
			{eventType: "issue_comment", body: onHead(roborevFindingsBody)},
			{eventType: "issue_comment", body: onHead(roborevCleanBody)},
		}, want: "clean"},
		{name: "newer copy from another commenter", comments: []comment{
			{eventType: "issue_comment", body: onHead(roborevFindingsBody)},
			{eventType: "issue_comment", body: onHead(roborevCleanBody), author: "mallory", metadata: `{"actor_account_type":"user"}`},
		}, want: "findings"},
		{name: "GraphQL login with bot account type", comments: []comment{
			{eventType: "issue_comment", body: onHead(roborevFindingsBody), author: "roborev-ci", metadata: `{"actor_account_type":"bot"}`},
		}, want: "findings"},
		{name: "user account named like the app", comments: []comment{
			{eventType: "issue_comment", body: onHead(roborevFindingsBody)},
			{eventType: "issue_comment", body: onHead(roborevCleanBody), author: "roborev-ci", metadata: `{"actor_account_type":"user"}`},
		}, want: "findings"},
		{name: "app-named login without an account type", comments: []comment{
			{eventType: "issue_comment", body: onHead(roborevCleanBody), author: "roborev-ci"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := openTestDB(t)
			repo := insertTestRepo(t, d, "acme", "roborev")
			mr := testMR(repo, 1)
			mr.PlatformHeadSHA = head
			mrID, err := d.UpsertMergeRequest(t.Context(), mr)
			require.NoError(t, err)
			for i, c := range tc.comments {
				platformID := int64(100 + i)
				author := c.author
				if author == "" {
					author = "roborev-ci[bot]"
				}
				require.NoError(t, d.UpsertMREvents(t.Context(), []MREvent{{
					MergeRequestID: mrID, PlatformID: &platformID, EventType: c.eventType,
					Author: author, Body: c.body, MetadataJSON: c.metadata,
					CreatedAt: baseTime().Add(time.Duration(i) * time.Minute),
					DedupeKey: fmt.Sprintf("comment-%d", i),
				}}))
			}

			verdicts, err := d.ListRoborevVerdictsForMRs(t.Context(), []int64{mrID})
			require.NoError(t, err)
			assert.Equal(t, tc.want, verdicts[mrID])
			for _, attribute := range []string{"roborev_findings", "roborev_clean"} {
				rows, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{State: "all", Attributes: []string{attribute}})
				require.NoError(t, err)
				matched := len(rows) == 1
				assert.Equal(t, attribute == "roborev_"+tc.want, matched, attribute)
			}
		})
	}
}

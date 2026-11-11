package db

import (
	"context"
	"encoding/json/v2"
	"fmt"
)

// Roborev verdicts for a pull request's current head, read from the review
// comments roborev's CI integration posts on the pull request.
const (
	RoborevVerdictFindings = "findings"
	RoborevVerdictClean    = "clean"
)

// roborev prefixes every PR comment it posts with this marker and a "## roborev: "
// header line. The header of a head review ends with the reviewed head's first
// seven SHA characters, as "(`abc1234`)", followed by a blank line and the verdict
// line (kenn-io/roborev internal/github/comment.go, internal/review/comment.go).
const (
	roborevCommentPrefix   = "<!-- roborev-pr-comment -->\n## roborev: "
	roborevFindingsVerdict = "**Verdict:** Changes require fixes for "
	roborevCleanVerdict    = "**Verdict:** No findings at or above "
	roborevNoIssuesVerdict = "**Verdict:** No issues found."
	roborevShortSHALength  = 7
)

// roborevHeadVerdictSQL is the verdict of the newest roborev comment that reviews
// the pull request's stored head: findings, clean, or an empty string when that
// review has no verdict line (a failed, skipped, or unstructured review). It is NULL when no
// comment reviews the stored head, which covers a PR with no review, a head whose
// review is still pending or running, and a review of a commit sync has not yet
// stored as the head.
func roborevHeadVerdictSQL(alias string) string {
	// The header line ends just before the comment's first blank line.
	blankLine := "instr(e.body, char(10) || char(10))"
	verdictLine := func(verdict string) string {
		return fmt.Sprintf("substr(e.body, %s + 2, %d) = '%s'", blankLine, len(verdict), verdict)
	}
	return fmt.Sprintf(`(SELECT CASE
			WHEN %[1]s THEN '%[2]s'
			WHEN %[3]s OR %[4]s THEN '%[5]s'
			ELSE '' END
		FROM forge_mr_events e
		WHERE e.merge_request_id = %[6]s.id
		  AND e.event_type IN ('issue_comment', 'comment')
		  AND length(%[6]s.platform_head_sha) >= %[7]d
		  AND substr(e.body, 1, %[8]d) = '%[9]s'
		  AND substr(e.body, %[10]s - %[11]d, %[11]d) = '(`+"`"+`' || lower(substr(%[6]s.platform_head_sha, 1, %[7]d)) || '`+"`"+`)'
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT 1)`,
		verdictLine(roborevFindingsVerdict), RoborevVerdictFindings,
		verdictLine(roborevCleanVerdict), verdictLine(roborevNoIssuesVerdict), RoborevVerdictClean,
		alias, roborevShortSHALength,
		len(roborevCommentPrefix), roborevCommentPrefix,
		blankLine, roborevShortSHALength+4,
	)
}

// ListRoborevVerdictsForMRs returns the roborev verdict for each pull request's
// current head, keyed by merge request ID. Pull requests whose head has no
// verdict are absent.
func (d *DB) ListRoborevVerdictsForMRs(ctx context.Context, mrIDs []int64) (map[int64]string, error) {
	verdicts := make(map[int64]string)
	if len(mrIDs) == 0 {
		return verdicts, nil
	}
	payload, err := json.Marshal(mrIDs)
	if err != nil {
		return nil, fmt.Errorf("encode roborev verdict ids: %w", err)
	}
	rows, err := d.roQueryContext(ctx, `
		SELECT id, verdict FROM (
			SELECT p.id, `+roborevHeadVerdictSQL("p")+` AS verdict
			FROM json_each(?) ids
			JOIN forge_merge_requests p ON p.id = CAST(ids.value AS INTEGER)
		) WHERE verdict != ''`, string(payload))
	if err != nil {
		return nil, fmt.Errorf("list roborev verdicts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var verdict string
		if err := rows.Scan(&id, &verdict); err != nil {
			return nil, fmt.Errorf("scan roborev verdict: %w", err)
		}
		verdicts[id] = verdict
	}
	return verdicts, rows.Err()
}

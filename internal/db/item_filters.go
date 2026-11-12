package db

import (
	"strconv"
	"strings"
)

// botAuthorCondition matches a pull request or issue author that is a bot account on its
// provider, as frontend/src/lib/utils/bot-author.ts decides: a -bot suffix anywhere, a [bot]
// suffix on GitHub, and a GitLab project or group access token user. SQLite has no REGEXP, so
// the GitLab pattern ^(project|group)_\d+_bot_[a-z0-9]+$ is matched in parts: the text between
// the prefix and the first "_bot_" must be digits, and the rest must be lowercase letters and
// digits. Like the browser it compares the lowercased login; SQLite lowers ASCII only.
func botAuthorCondition(authorExpr, providerExpr string) string {
	login := "lower(" + authorExpr + ")"
	gitlab := make([]string, 0, 2)
	for _, prefix := range []string{"project_", "group_"} {
		rest := "substr(" + login + ", " + strconv.Itoa(len(prefix)+1) + ")"
		at := "instr(" + rest + ", '_bot_')"
		gitlab = append(gitlab, "("+login+" GLOB '"+prefix+"[0-9]*_bot_?*'"+
			" AND substr("+rest+", 1, "+at+" - 1) NOT GLOB '*[^0-9]*'"+
			" AND substr("+rest+", "+at+" + 5) NOT GLOB '*[^a-z0-9]*')")
	}
	return "(" + login + " LIKE '%-bot'" +
		" OR (" + providerExpr + " = 'github' AND " + login + " LIKE '%[bot]')" +
		" OR (" + providerExpr + " = 'gitlab' AND (" + strings.Join(gitlab, " OR ") + ")))"
}

// labelConditions returns, for each name, a condition matching the pull request or issue whose
// id is idExpr when it carries a label of that name, ignoring ASCII case, so the same label in
// different repositories is one choice.
func labelConditions(itemType, idExpr string, names []string, args *[]any) []string {
	table, column := "forge_merge_request_labels", "merge_request_id"
	if itemType == ItemTypeIssue {
		table, column = "forge_issue_labels", "issue_id"
	}
	conditions := make([]string, 0, len(names))
	for _, name := range names {
		conditions = append(conditions, `EXISTS (
			SELECT 1 FROM `+table+` il JOIN forge_labels l ON l.id = il.label_id
			WHERE il.`+column+` = `+idExpr+` AND lower(l.name) = lower(?)
		)`)
		*args = append(*args, name)
	}
	return conditions
}

// labelFilterConditions keeps the item whose id is idExpr when it carries every required label
// and none of the excluded ones.
func labelFilterConditions(itemType, idExpr string, required, excluded []string, args *[]any) []string {
	conditions := labelConditions(itemType, idExpr, required, args)
	for _, condition := range labelConditions(itemType, idExpr, excluded, args) {
		conditions = append(conditions, "NOT "+condition)
	}
	return conditions
}

// referencedByPRCondition matches an issue aliased alias that a pull request references.
func referencedByPRCondition(alias string) string {
	return `EXISTS (
			SELECT 1 FROM forge_issue_pr_references ref
			WHERE ref.issue_id = ` + alias + `.id
		)`
}

// ActivityItemFilters narrow Activity by the pull request or issue a row belongs to. A row whose
// parent has not synced fails a requirement and passes an exclusion.
type ActivityItemFilters struct {
	// Labels keeps rows whose parent carries every named label, ignoring case.
	Labels []string
	// ExcludeLabels drops rows whose parent carries any named label, ignoring case.
	ExcludeLabels []string
	// HideBotAuthoredIssues drops issue activity on issues a bot opened.
	HideBotAuthoredIssues bool
	// Starred keeps only rows whose parent is starred.
	Starred bool
	// ReferencedByPR and NotReferencedByPR keep only issue activity on issues a pull request
	// does or does not reference; other activity passes.
	ReferencedByPR    bool
	NotReferencedByPR bool
}

// clauses returns the conditions over an activity row's repo_id, item_type, and item_number.
func (f ActivityItemFilters) clauses(args *[]any) []string {
	var clauses []string
	if len(f.Labels) > 0 {
		pr := strings.Join(labelConditions(ItemTypePR, "p.id", f.Labels, args), " AND ")
		issue := strings.Join(labelConditions(ItemTypeIssue, "i.id", f.Labels, args), " AND ")
		clauses = append(clauses, activityParentMatches(pr, issue))
	}
	if len(f.ExcludeLabels) > 0 {
		pr := strings.Join(labelConditions(ItemTypePR, "p.id", f.ExcludeLabels, args), " OR ")
		issue := strings.Join(labelConditions(ItemTypeIssue, "i.id", f.ExcludeLabels, args), " OR ")
		clauses = append(clauses, notCondition(activityParentMatches(pr, issue)))
	}
	if f.Starred {
		clauses = append(clauses,
			"(item_type, repo_id, item_number) IN (SELECT s.item_type, s.repo_id, s.number FROM forge_starred_items s)")
	}
	if f.HideBotAuthoredIssues {
		clauses = append(clauses, notCondition(activityIssueMatches(
			"FROM forge_issues i JOIN forge_repos r ON r.id = i.repo_id WHERE "+botAuthorCondition("i.author", "r.platform"))))
	}
	if f.ReferencedByPR {
		clauses = append(clauses, "(item_type != 'issue' OR "+activityIssueMatches("FROM forge_issues i WHERE "+referencedByPRCondition("i"))+")")
	}
	if f.NotReferencedByPR {
		clauses = append(clauses, notCondition(activityIssueMatches("FROM forge_issues i WHERE "+referencedByPRCondition("i"))))
	}
	return clauses
}

// activityParentMatches matches rows whose synced pull request satisfies prCond over p, or whose
// synced issue satisfies issueCond over i.
func activityParentMatches(prCond, issueCond string) string {
	return "((item_type = 'pr' AND (repo_id, item_number) IN (SELECT p.repo_id, p.number FROM forge_merge_requests p WHERE " +
		prCond + ")) OR " + activityIssueMatches("FROM forge_issues i WHERE "+issueCond) + ")"
}

// activityIssueMatches matches issue rows whose issue the query selects, given as its FROM clause
// with the issue aliased i.
func activityIssueMatches(from string) string {
	return "(item_type = 'issue' AND (repo_id, item_number) IN (SELECT i.repo_id, i.number " + from + "))"
}

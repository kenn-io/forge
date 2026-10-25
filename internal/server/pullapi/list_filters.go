package pullapi

import (
	"strings"

	"go.kenn.io/forge/internal/db"
)

func matchesPullAttributes(pr db.MergeRequest, attributes []string) bool {
	for _, attribute := range attributes {
		switch attribute {
		case "approved":
			if strings.EqualFold(strings.TrimSpace(pr.ReviewDecision), "APPROVED") {
				return true
			}
		case "draft":
			if pr.IsDraft {
				return true
			}
		case "ready":
			if pr.State == "open" && !pr.IsDraft {
				return true
			}
		case "merge_conflicts":
			if pr.MergeableState == "dirty" {
				return true
			}
		case "failed_ci":
			if hasFailedCI(pr) {
				return true
			}
		case "passed_ci":
			if hasPassedCI(pr) {
				return true
			}
		}
	}
	return false
}

func hasFailedCI(pr db.MergeRequest) bool {
	switch strings.ToLower(strings.TrimSpace(pr.CIStatus)) {
	case "failure", "failed", "error":
		return true
	}
	checks, err := decodeCIChecks(pr.CIChecksJSON)
	if err != nil {
		return false
	}
	for _, check := range checks {
		if ciCheckIsPending(check) {
			continue
		}
		switch check.Conclusion {
		case "failure", "cancelled", "timed_out", "action_required", "stale", "startup_failure":
			return true
		}
	}
	return false
}

// hasPassedCI reports green CI: a successful aggregate status (or, without
// one, at least one check) and no check that is still running or did not pass.
func hasPassedCI(pr db.MergeRequest) bool {
	status := strings.ToLower(strings.TrimSpace(pr.CIStatus))
	if status != "" && status != "success" {
		return false
	}
	checks, err := decodeCIChecks(pr.CIChecksJSON)
	if err != nil || (status == "" && len(checks) == 0) {
		return false
	}
	for _, check := range checks {
		if ciCheckIsPending(check) {
			return false
		}
		switch check.Conclusion {
		case "success", "skipped", "neutral":
		default:
			return false
		}
	}
	return true
}

func ciCheckIsPending(check db.CICheck) bool {
	switch check.Status {
	case "in_progress", "queued", "pending", "waiting":
		return true
	}
	return false
}

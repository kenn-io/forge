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
			switch strings.ToLower(strings.TrimSpace(pr.CIStatus)) {
			case "failure", "failed", "error":
				return true
			}
			checks, err := decodeCIChecks(pr.CIChecksJSON)
			if err != nil {
				continue
			}
			for _, check := range checks {
				switch check.Status {
				case "in_progress", "queued", "pending", "waiting":
					continue
				}
				switch check.Conclusion {
				case "failure", "cancelled", "timed_out", "action_required", "stale", "startup_failure":
					return true
				}
			}
		}
	}
	return false
}

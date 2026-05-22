package server

import "go.kenn.io/middleman/internal/db"

func computeStackHealth(members []db.StackMemberWithPR) string {
	if len(members) == 0 {
		return "in_progress"
	}

	hasMerged := false
	allGreen := true
	hasBlocker := false
	lowestOpenIdx := -1

	for i, m := range members {
		if m.State == "merged" {
			hasMerged = true
			continue
		}
		if lowestOpenIdx == -1 {
			lowestOpenIdx = i
		}

		// Drafts cannot be merged, so they never count as green.
		isGreen := !m.IsDraft && m.CIStatus == "success" && m.ReviewDecision == "APPROVED"
		if !isGreen {
			allGreen = false
		}

		if m.CIStatus == "failure" || m.ReviewDecision == "CHANGES_REQUESTED" {
			// A PR only counts as "blocked" when it actually blocks something
			// downstream — i.e. has at least one non-merged descendant. A
			// failing tip with nothing below it is not blocking anything.
			for j := i + 1; j < len(members); j++ {
				if members[j].State != "merged" {
					hasBlocker = true
					break
				}
			}
		}
	}

	switch {
	case hasBlocker:
		return "blocked"
	case hasMerged:
		return "partial_merge"
	case allGreen:
		return "all_green"
	case lowestOpenIdx >= 0:
		m := members[lowestOpenIdx]
		if !m.IsDraft && m.CIStatus == "success" && m.ReviewDecision == "APPROVED" {
			return "base_ready"
		}
	}
	return "in_progress"
}

func computeBlockedBy(members []db.StackMemberWithPR) map[int]int {
	blockedBy := make(map[int]int)
	var rootBlocker int
	for _, m := range members {
		if m.State == "merged" {
			continue
		}
		isBlocked := m.CIStatus == "failure" || m.ReviewDecision == "CHANGES_REQUESTED"
		if isBlocked && rootBlocker == 0 {
			rootBlocker = m.Number
		} else if rootBlocker != 0 && m.Number != rootBlocker {
			blockedBy[m.Number] = rootBlocker
		}
	}
	return blockedBy
}

func toStackMemberResponses(members []db.StackMemberWithPR) []stackMemberResponse {
	blocked := computeBlockedBy(members)
	out := make([]stackMemberResponse, len(members))
	for i, m := range members {
		out[i] = stackMemberResponse{
			Number:         m.Number,
			Title:          m.Title,
			State:          m.State,
			CIStatus:       m.CIStatus,
			ReviewDecision: m.ReviewDecision,
			Position:       m.Position,
			IsDraft:        m.IsDraft,
			BaseBranch:     m.BaseBranch,
		}
		if b, ok := blocked[m.Number]; ok {
			out[i].BlockedBy = &b
		}
	}
	return out
}

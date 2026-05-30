package platform

import (
	"encoding/json"
	"time"

	"go.kenn.io/middleman/internal/db"
)

// MarshalAssigneesJSON converts a list of assignee usernames to a JSON array string.
// Returns "[]" if the list is empty or if marshaling fails.
func MarshalAssigneesJSON(assignees []string) string {
	if len(assignees) == 0 {
		return "[]"
	}
	if b, err := json.Marshal(assignees); err == nil {
		return string(b)
	}
	return "[]"
}

func DBRepoIdentity(ref RepoRef) db.RepoIdentity {
	return db.RepoIdentity{
		Platform:       string(ref.Platform),
		PlatformHost:   ref.Host,
		PlatformRepoID: ref.PlatformExternalID,
		Owner:          ref.Owner,
		Name:           ref.Name,
		RepoPath:       ref.RepoPath,
	}
}

func DBRepositoryIdentity(repo Repository) db.RepoIdentity {
	identity := DBRepoIdentity(repo.Ref)
	if identity.PlatformRepoID == "" {
		identity.PlatformRepoID = repo.PlatformExternalID
	}
	return identity
}

func DBMergeRequest(repoID int64, mr MergeRequest) *db.MergeRequest {
	out := &db.MergeRequest{
		RepoID:             repoID,
		PlatformID:         mr.PlatformID,
		PlatformExternalID: mr.PlatformExternalID,
		Number:             mr.Number,
		URL:                mr.URL,
		Title:              mr.Title,
		Author:             mr.Author,
		AuthorDisplayName:  mr.AuthorDisplayName,
		State:              db.MergeRequestState(mr.State),
		IsDraft:            mr.IsDraft,
		IsLocked:           mr.IsLocked,
		Body:               mr.Body,
		HeadBranch:         mr.HeadBranch,
		BaseBranch:         mr.BaseBranch,
		PlatformHeadSHA:    mr.HeadSHA,
		PlatformBaseSHA:    mr.BaseSHA,
		HeadRepoCloneURL:   mr.HeadRepoCloneURL,
		Additions:          mr.Additions,
		Deletions:          mr.Deletions,
		CommentCount:       mr.CommentCount,
		ReviewDecision:     mr.ReviewDecision,
		CIStatus:           mr.CIStatus,
		MergeableState:     mr.MergeableState,
		CreatedAt:          mr.CreatedAt,
		UpdatedAt:          mr.UpdatedAt,
		LastActivityAt:     mr.LastActivityAt,
		MergedAt:           mr.MergedAt,
		ClosedAt:           mr.ClosedAt,
	}
	out.Labels = DBLabels(mr.Labels, itemLabelUpdatedAt(mr.UpdatedAt, mr.CreatedAt))
	return out
}

func DBIssue(repoID int64, issue Issue) *db.Issue {
	out := &db.Issue{
		RepoID:             repoID,
		PlatformID:         issue.PlatformID,
		PlatformExternalID: issue.PlatformExternalID,
		Number:             issue.Number,
		URL:                issue.URL,
		Title:              issue.Title,
		Author:             issue.Author,
		State:              issue.State,
		Body:               issue.Body,
		CommentCount:       issue.CommentCount,
		CreatedAt:          issue.CreatedAt,
		UpdatedAt:          issue.UpdatedAt,
		LastActivityAt:     issue.LastActivityAt,
		ClosedAt:           issue.ClosedAt,
		AssigneesJSON:      MarshalAssigneesJSON(issue.Assignees),
	}
	out.Labels = DBLabels(issue.Labels, itemLabelUpdatedAt(issue.UpdatedAt, issue.CreatedAt))
	return out
}

func DBMREvent(mrID int64, event MergeRequestEvent) db.MREvent {
	out := db.MREvent{
		MergeRequestID:     mrID,
		PlatformExternalID: event.PlatformExternalID,
		EventType:          event.EventType,
		Author:             event.Author,
		Summary:            event.Summary,
		Body:               event.Body,
		MetadataJSON:       event.MetadataJSON,
		CreatedAt:          event.CreatedAt,
		DedupeKey:          event.DedupeKey,
		PositionJSON:       event.PositionJSON,
		Resolvable:         event.Resolvable,
		Resolved:           event.Resolved,
	}
	if event.PlatformID != 0 || event.EventType == "issue_comment" || event.EventType == "review" {
		platformID := event.PlatformID
		out.PlatformID = &platformID
	}
	if event.ThreadID != "" {
		out.ThreadID = &event.ThreadID
	}
	return out
}

func DBIssueEvent(issueID int64, event IssueEvent) db.IssueEvent {
	out := db.IssueEvent{
		IssueID:            issueID,
		PlatformExternalID: event.PlatformExternalID,
		EventType:          event.EventType,
		Author:             event.Author,
		Summary:            event.Summary,
		Body:               event.Body,
		MetadataJSON:       event.MetadataJSON,
		CreatedAt:          event.CreatedAt,
		DedupeKey:          event.DedupeKey,
	}
	if event.PlatformID != 0 || event.EventType == "issue_comment" {
		platformID := event.PlatformID
		out.PlatformID = &platformID
	}
	if event.ThreadID != "" {
		out.ThreadID = &event.ThreadID
	}
	return out
}

func DBLabels(labels []Label, updatedAt time.Time) []db.Label {
	if len(labels) == 0 {
		return nil
	}
	out := make([]db.Label, 0, len(labels))
	for _, label := range labels {
		out = append(out, db.Label{
			PlatformID:         label.PlatformID,
			PlatformExternalID: label.PlatformExternalID,
			Name:               label.Name,
			Description:        label.Description,
			Color:              label.Color,
			IsDefault:          label.IsDefault,
			UpdatedAt:          updatedAt,
		})
	}
	return out
}

func DBCIChecks(checks []CICheck) []db.CICheck {
	if len(checks) == 0 {
		return nil
	}
	out := make([]db.CICheck, 0, len(checks))
	for _, check := range checks {
		out = append(out, db.CICheck{
			Name:            check.Name,
			Status:          check.Status,
			Conclusion:      check.Conclusion,
			URL:             check.URL,
			App:             check.App,
			DurationSeconds: CICheckDurationSeconds(check),
		})
	}
	return out
}

func CICheckDurationSeconds(check CICheck) *int64 {
	if check.StartedAt == nil || check.CompletedAt == nil {
		return nil
	}
	duration := check.CompletedAt.Sub(*check.StartedAt)
	if duration < 0 {
		return nil
	}
	seconds := int64(duration.Seconds())
	return &seconds
}

func itemLabelUpdatedAt(updatedAt, createdAt time.Time) time.Time {
	if !updatedAt.IsZero() {
		return updatedAt
	}
	return createdAt
}

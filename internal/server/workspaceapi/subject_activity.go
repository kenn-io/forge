package workspaceapi

import (
	"context"
	"time"

	"go.kenn.io/forge/internal/db"
)

type SubjectActivity struct {
	Subject    db.WorkspaceSubjectMetadata
	Workspace  WorkspaceRef
	ActivityAt *time.Time
	// HeadRepoKind is a pull request workspace's own head repository
	// classification (same_repo, fork, or unknown), or empty when no
	// workspace for the pull request recorded one.
	HeadRepoKind string
}

type WorkspaceSubjectSnapshot struct {
	OwnReferences map[db.WorkspaceSubjectKey]WorkspaceRef
	Subjects      map[db.WorkspaceSubjectKey]SubjectActivity
}

type workspaceSubjectCandidate struct {
	summary    db.WorkspaceSummary
	activityAt *time.Time
}

func (s *Handler) WorkspaceSubjectSnapshot(
	ctx context.Context,
) (WorkspaceSubjectSnapshot, error) {
	snapshot := emptyWorkspaceSubjectSnapshot()
	if s.db == nil {
		return snapshot, nil
	}
	s.scheduleWorkspaceTmuxPrune()
	summaries, err := s.db.ListWorkspaceSummaries(ctx)
	if err != nil {
		return WorkspaceSubjectSnapshot{}, err
	}

	keys := make([]db.WorkspaceSubjectKey, 0, len(summaries)*2)
	resolved := make(map[db.WorkspaceSubjectKey]workspaceSubjectCandidate)
	own := make(map[db.WorkspaceSubjectKey]db.WorkspaceSummary)
	headRepoKinds := make(map[db.WorkspaceSubjectKey]string)
	for i := range summaries {
		summary := summaries[i]
		if summary.RepoID == 0 {
			continue
		}
		ownKey, hasOwn := ownWorkspaceSubjectKey(summary)
		if hasOwn {
			keys = append(keys, ownKey)
			if current, ok := own[ownKey]; !ok || workspaceCandidatePreferred(summary, current, ownKey) {
				own[ownKey] = summary
			}
			if kind := mrHeadRepoKind(summary.ItemType, summary.MRHeadRepo); kind != "" &&
				headRepoKinds[ownKey] != mrHeadRepoKindFork && headRepoKinds[ownKey] != mrHeadRepoKindSameRepo {
				headRepoKinds[ownKey] = kind
			}
		}
		resolvedKey, ok := resolvedWorkspaceSubjectKey(summary)
		if !ok {
			continue
		}
		keys = append(keys, resolvedKey)
		entry, refreshDue := s.cachedWorkspaceEnrichment(summary.ID, workspaceEnrichmentTmux)
		if refreshDue && summary.Status == "ready" && !s.workspaceEnrichmentDisabled {
			s.scheduleWorkspaceTmuxEnrichment(summary)
		}
		var activityAt *time.Time
		if summary.Status == "ready" {
			activityAt = cachedTmuxActivityAt(entry)
		}
		candidate := workspaceSubjectCandidate{summary: summary, activityAt: activityAt}
		current, exists := resolved[resolvedKey]
		if !exists {
			resolved[resolvedKey] = candidate
			continue
		}
		if activityAt != nil && (current.activityAt == nil || activityAt.After(*current.activityAt)) {
			current.activityAt = activityAt
		}
		if workspaceCandidatePreferred(summary, current.summary, resolvedKey) {
			current.summary = summary
		}
		resolved[resolvedKey] = current
	}

	metadata, err := s.db.ListWorkspaceSubjectMetadata(ctx, keys)
	if err != nil {
		return WorkspaceSubjectSnapshot{}, err
	}
	for key, summary := range own {
		if _, ok := metadata[key]; !ok {
			continue
		}
		snapshot.OwnReferences[key] = s.workspaceReference(ctx, &summary)
	}
	for key, candidate := range resolved {
		subject, ok := metadata[key]
		if !ok {
			continue
		}
		snapshot.Subjects[key] = SubjectActivity{
			Subject:      subject,
			Workspace:    s.workspaceReference(ctx, &candidate.summary),
			ActivityAt:   candidate.activityAt,
			HeadRepoKind: headRepoKinds[key],
		}
	}
	return snapshot, nil
}

func emptyWorkspaceSubjectSnapshot() WorkspaceSubjectSnapshot {
	return WorkspaceSubjectSnapshot{
		OwnReferences: make(map[db.WorkspaceSubjectKey]WorkspaceRef),
		Subjects:      make(map[db.WorkspaceSubjectKey]SubjectActivity),
	}
}

func ownWorkspaceSubjectKey(summary db.WorkspaceSummary) (db.WorkspaceSubjectKey, bool) {
	if summary.ItemNumber <= 0 ||
		(summary.ItemType != db.WorkspaceItemTypePullRequest && summary.ItemType != db.WorkspaceItemTypeIssue) {
		return db.WorkspaceSubjectKey{}, false
	}
	return db.WorkspaceSubjectKey{
		RepoID: summary.RepoID, ItemType: summary.ItemType, ItemNumber: summary.ItemNumber,
	}, true
}

func resolvedWorkspaceSubjectKey(summary db.WorkspaceSummary) (db.WorkspaceSubjectKey, bool) {
	if summary.AssociatedPRVisible &&
		summary.AssociatedPRNumber != nil && *summary.AssociatedPRNumber > 0 {
		return db.WorkspaceSubjectKey{
			RepoID: summary.RepoID, ItemType: db.WorkspaceItemTypePullRequest,
			ItemNumber: *summary.AssociatedPRNumber,
		}, true
	}
	return ownWorkspaceSubjectKey(summary)
}

func cachedTmuxActivityAt(entry *workspaceEnrichmentCacheEntry) *time.Time {
	if entry == nil || !entry.hasTmux || entry.response.TmuxLastOutputAt == nil {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *entry.response.TmuxLastOutputAt)
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func workspaceCandidatePreferred(
	candidate db.WorkspaceSummary,
	current db.WorkspaceSummary,
	key db.WorkspaceSubjectKey,
) bool {
	candidateDirect := candidate.ItemType == key.ItemType && candidate.ItemNumber == key.ItemNumber
	currentDirect := current.ItemType == key.ItemType && current.ItemNumber == key.ItemNumber
	if candidateDirect != currentDirect {
		return candidateDirect
	}
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	return candidate.ID > current.ID
}

// workspaceReference uses the same live-session filtering as workspace responses.
func (s *Handler) workspaceReference(ctx context.Context, summary *db.WorkspaceSummary) WorkspaceRef {
	var response workspaceResponse
	s.applyAgentActivity(ctx, &response, summary)
	return WorkspaceRef{ID: summary.ID, Status: summary.Status, AgentState: response.AgentState}
}

// PullRequestItems lists the pull requests with a workspace in the snapshot, the ones whose
// list rows and Activity carry a workspace reference.
func (s WorkspaceSubjectSnapshot) PullRequestItems() []db.RepoItemNumber {
	items := []db.RepoItemNumber{}
	for key := range s.Subjects {
		if key.ItemType == db.WorkspaceItemTypePullRequest {
			items = append(items, db.RepoItemNumber{RepoID: key.RepoID, Number: key.ItemNumber})
		}
	}
	return items
}
